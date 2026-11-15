use anyhow::{Context, Result};
use rand::RngExt;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fmt::Write as _;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};

pub fn new_instance_id() -> String {
    let mut bytes = [0u8; 16];
    rand::rng().fill(&mut bytes);
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(out, "{byte:02x}");
    }
    out
}

#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct SavedIdentity {
    device_identity: String,
    reconnect_proof: String,
}

fn identity_root() -> Result<PathBuf> {
    if let Some(path) = std::env::var_os("RDEV_IDENTITY_DIR").filter(|s| !s.is_empty()) {
        return Ok(PathBuf::from(path));
    }
    #[cfg(target_os = "windows")]
    let root = PathBuf::from(std::env::var_os("APPDATA").filter(|s| !s.is_empty()).context("APPDATA identity config directory unavailable")?);
    #[cfg(target_os = "macos")]
    let root = PathBuf::from(std::env::var_os("HOME").filter(|s| !s.is_empty()).context("HOME identity config directory unavailable")?).join("Library/Application Support");
    #[cfg(not(any(target_os = "windows", target_os = "macos")))]
    let root = if let Some(path) = std::env::var_os("XDG_CONFIG_HOME").filter(|s| !s.is_empty()) {
        PathBuf::from(path)
    } else {
        PathBuf::from(std::env::var_os("HOME").filter(|s| !s.is_empty()).context("HOME identity config directory unavailable")?).join(".config")
    };
    Ok(root.join("rdev"))
}

fn valid_credential(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

fn sync_directory(path: &Path) -> std::io::Result<()> {
    #[cfg(not(target_os = "windows"))]
    { std::fs::File::open(path)?.sync_all() }
    #[cfg(target_os = "windows")]
    { let _ = path; Ok(()) }
}

fn read_identity(path: &Path) -> Result<SavedIdentity> {
    let file = std::fs::File::open(path)?;
    let mut bytes = Vec::new();
    file.take(4097).read_to_end(&mut bytes)?;
    anyhow::ensure!(bytes.len() <= 4096, "saved device identity exceeds size limit");
    let saved: SavedIdentity = serde_json::from_slice(&bytes).context("invalid saved device identity; restore the original credential")?;
    anyhow::ensure!(valid_credential(&saved.device_identity) && valid_credential(&saved.reconnect_proof), "invalid saved device identity format");
    // Also cover a concurrent publisher that has not synced its directory yet.
    sync_directory(path.parent().context("identity directory unavailable")?)?;
    Ok(saved)
}

pub fn load_device_identity(server: &str, client_id: &str) -> Result<(String, String)> {
    load_identity_at(&identity_root()?, server, client_id)
}

fn load_identity_at(root: &Path, server: &str, client_id: &str) -> Result<(String, String)> {
    let endpoint = server.split(',').next().unwrap_or(server).trim();
    let endpoint = match url::Url::parse(endpoint) {
        Ok(mut url) => {
            if let Some(host) = url.host_str().map(str::to_ascii_lowercase) { url.set_host(Some(&host))?; }
            let path = url.path().trim_end_matches('/').to_owned();
            url.set_path(&path);
            let mut normalized = url[..url::Position::BeforePath].to_owned();
            normalized.push_str(url.path().trim_end_matches('/'));
            normalized.push_str(&url[url::Position::AfterPath..]);
            normalized
        }
        Err(_) => endpoint.to_owned(),
    };
    let scope = format!("{}\0{}", endpoint, client_id.trim());
    let digest = Sha256::digest(scope.as_bytes());
    let mut name = String::with_capacity(32);
    for byte in &digest[..16] { let _ = write!(name, "{byte:02x}"); }
    let path = root.join(format!("identity-{name}.json"));
    match read_identity(&path) {
        Ok(saved) => return Ok((saved.device_identity, saved.reconnect_proof)),
        Err(err) if err.downcast_ref::<std::io::Error>().is_some_and(|e| e.kind() == std::io::ErrorKind::NotFound) => {}
        Err(err) => return Err(err),
    }
    std::fs::create_dir_all(root)?;
    if let Some(parent) = root.parent().filter(|p| !p.as_os_str().is_empty()) { sync_directory(parent)?; }
    let saved = SavedIdentity {
        device_identity: new_instance_id() + &new_instance_id(),
        reconnect_proof: new_instance_id() + &new_instance_id(),
    };
    // NamedTempFile is private (0600 on Unix); never truncate or replace an
    // existing enrollment. Hard-link publication is atomic across processes.
    let mut tmp = tempfile::NamedTempFile::new_in(root).context("create private identity file")?;
    tmp.write_all(&serde_json::to_vec(&saved)?)?;
    tmp.as_file().sync_all()?;
    match std::fs::hard_link(tmp.path(), &path) {
        Ok(()) => sync_directory(root)?,
        Err(err) if err.kind() == std::io::ErrorKind::AlreadyExists => {}
        Err(err) => return Err(err).context("publish device identity"),
    }
    let saved = read_identity(&path)?;
    Ok((saved.device_identity, saved.reconnect_proof))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn credentials_accept_digits_and_reject_uppercase() {
        assert!(valid_credential(&"0123456789abcdef".repeat(4)));
        assert!(!valid_credential(&"A".repeat(64)));
        assert!(!valid_credential(&"a".repeat(63)));
    }

    #[test]
    fn concurrent_enrollment_reloads_one_immutable_credential() {
        let root = tempfile::tempdir().unwrap();
        let values = std::thread::scope(|scope| {
            let handles: Vec<_> = (0..8).map(|_| scope.spawn(|| load_identity_at(root.path(), "wss://example.test/Agent", "device").unwrap())).collect();
            handles.into_iter().map(|h| h.join().unwrap()).collect::<Vec<_>>()
        });
        assert!(values.iter().all(|value| value == &values[0]));
        assert_eq!(load_identity_at(root.path(), "wss://example.test/Agent", "device").unwrap(), values[0]);
        assert_eq!(load_identity_at(root.path(), "wss://EXAMPLE.test:443/Agent/,tcp://backup:1700", "device").unwrap(), values[0]);
        assert_ne!(load_identity_at(root.path(), "wss://example.test/agent", "device").unwrap(), values[0]);
    }

    #[test]
    fn canonical_json_and_corrupt_storage_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let value = load_identity_at(root.path(), "wss://example.test", "device").unwrap();
        let path = std::fs::read_dir(root.path()).unwrap().next().unwrap().unwrap().path();
        let json: serde_json::Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        assert_eq!(json["deviceIdentity"], value.0);
        assert_eq!(json["reconnectProof"], value.1);
        std::fs::write(&path, b"{invalid").unwrap();
        assert!(load_identity_at(root.path(), "wss://example.test", "device").is_err());
        assert_eq!(std::fs::read(&path).unwrap(), b"{invalid");
    }
}
