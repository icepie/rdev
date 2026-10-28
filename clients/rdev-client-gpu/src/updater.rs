use std::{
    env,
    fs::File,
    path::{Path, PathBuf},
    thread,
    time::{Duration, Instant},
};

use anyhow::{anyhow, Context, Result};
use base64::{engine::general_purpose::STANDARD as BASE64, Engine as _};
use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use self_update::{backends::github, Download, Extract};
use tempfile::TempDir;
use tracing::{info, warn};

const REPO_OWNER: &str = "icepie";
const REPO_NAME: &str = "rdev";

/// Embedded ed25519 public key (base64, 32 bytes) verifying release asset
/// signatures. Assets are signed in CI by scripts/sign with the private key
/// stored in the RDEV_UPDATE_SIGNING_KEY GitHub secret; unsigned or tampered
/// downloads must never be applied.
const UPDATE_PUBLIC_KEY_B64: &str = "jtVaB2ppsCLhcYyYYQjRKiviDvyPZvr+AS8nqOQlkUk=";

const DEFAULT_PROXY_PREFIXES: &[&str] = &[
    "",
    "https://gh-proxy.com/",
    "https://gh-proxy.net/",
    "https://gh.llkk.cc/",
    "https://hub.gitmirror.com/",
];

#[derive(Debug, Clone)]
pub struct Config {
    pub version: &'static str,
    pub enabled: bool,
    pub interval: Duration,
}

pub fn start(cfg: Config) {
    if !cfg.enabled {
        return;
    }
    let interval = if cfg.interval.is_zero() {
        Duration::from_secs(60)
    } else {
        cfg.interval
    };
    thread::spawn(move || run(Config { interval, ..cfg }));
}

fn run(cfg: Config) {
    let mut failures = FailureLog::default();
    thread::sleep(Duration::from_secs(60));
    loop {
        #[cfg(windows)]
        let executable = env::current_exe().ok();
        match check_and_apply(&cfg) {
            Ok(true) => {
                info!("auto-update applied; restarting rdev-client-gpu");
                #[cfg(windows)]
                if let Some(executable) = executable.as_deref() {
                    restart_self(executable).unwrap_or_else(|err| {
                        warn!("auto-update restart failed: {err:#}");
                    });
                }
                #[cfg(not(windows))]
                info!("auto-update applied; restart rdev-client-gpu to use the new version");
                return;
            }
            Ok(false) => failures.recovered(),
            Err(err) => failures.record(&err),
        }
        thread::sleep(cfg.interval);
    }
}

#[cfg(windows)]
fn restart_self(executable: &std::path::Path) -> Result<()> {
    use std::process::Command;

    let parent_pid = std::process::id().to_string();
    Command::new(executable)
        .args(env::args_os().skip(1))
        .env("RDEV_UPDATE_PARENT_PID", parent_pid)
        .spawn()
        .with_context(|| format!("spawn updated executable {}", executable.display()))?;
    std::process::exit(0);
}

#[cfg(windows)]
pub fn wait_for_update_parent() {
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{OpenProcess, WaitForSingleObject, INFINITE};

    const SYNCHRONIZE: u32 = 0x0010_0000;

    let Ok(pid) = env::var("RDEV_UPDATE_PARENT_PID") else {
        return;
    };
    let Ok(pid) = pid.parse::<u32>() else {
        return;
    };
    let handle = unsafe { OpenProcess(SYNCHRONIZE, 0, pid) };
    if handle.is_null() {
        return;
    }
    unsafe {
        WaitForSingleObject(handle, INFINITE);
        CloseHandle(handle);
    }
}

pub fn check_and_apply(cfg: &Config) -> Result<bool> {
    let current = normalize_version(cfg.version);
    if current.is_empty() || current == "dev" {
        return Ok(false);
    }

    let updater = github::Update::configure()
        .repo_owner(REPO_OWNER)
        .repo_name(REPO_NAME)
        .current_version(&current)
        .bin_name(binary_name())
        .show_output(false)
        .build()
        .context("build GitHub updater")?;

    let release = updater
        .get_latest_release()
        .context("fetch latest GitHub release")?;
    let Some(release) = release.latest() else {
        return Ok(false);
    };

    let latest = normalize_version(release.version());
    if latest.is_empty() || !newer_version(&latest, &current) {
        return Ok(false);
    }

    let asset_name = release_asset_name().ok_or_else(|| {
        anyhow!(
            "auto-update is not supported for {}/{}",
            env::consts::OS,
            env::consts::ARCH
        )
    })?;
    let asset = release
        .assets()
        .iter()
        .find(|asset| asset.name() == asset_name)
        .ok_or_else(|| {
            anyhow!(
                "release v{} asset {} not found",
                release.version(),
                asset_name
            )
        })?;

    let tmp = TempDir::new().context("create updater temp dir")?;
    let archive_path = tmp.path().join(asset.name());
    let download_url = github_download_url(release.version(), asset.name());
    download_with_proxies(&download_url, &archive_path)
        .with_context(|| format!("download release asset {}", asset.name()))?;

    // Security: downloads may pass through third-party proxies, so the
    // archive cannot be trusted until its detached ed25519 signature
    // (<asset>.sig from the same release) verifies against the embedded key.
    let sig_url = format!("{download_url}.sig");
    let sig_path = tmp.path().join(format!("{}.sig", asset.name()));
    download_with_proxies(&sig_url, &sig_path)
        .with_context(|| format!("download release signature {}.sig", asset.name()))?;
    let sig_b64 = std::fs::read_to_string(&sig_path).context("read release asset signature")?;
    let archive_bytes = std::fs::read(&archive_path)
        .with_context(|| format!("read downloaded asset {}", asset.name()))?;
    verify_asset_signature(&archive_bytes, &sig_b64)
        .with_context(|| format!("reject release asset {}", asset.name()))?;

    let bin_path = PathBuf::from(package_dir_name(asset.name())).join(binary_name());
    Extract::from_source(&archive_path)
        .extract_file(tmp.path(), &bin_path)
        .with_context(|| format!("extract {} from {}", bin_path.display(), asset.name()))?;

    let new_exe = tmp.path().join(&bin_path);
    self_replace::self_replace(&new_exe).context("replace current executable")?;

    Ok(true)
}

fn download_with_proxies(target: &str, archive_path: &Path) -> Result<()> {
    let mut last_err = None;
    for prefix in proxy_prefixes() {
        let url = proxied_url(&prefix, target);
        let mut file = File::create(archive_path)
            .with_context(|| format!("create {}", archive_path.display()))?;
        let mut download = Download::from_url(&url);
        let _ = download.request_header("User-Agent", "rdev-auto-updater");
        let _ = download.request_header("Accept", "application/octet-stream");
        if let Err(err) = download.download_to(&mut file) {
            last_err = Some(err);
            continue;
        }
        return Ok(());
    }
    Err(anyhow!(
        "{}",
        last_err
            .map(|err| err.to_string())
            .unwrap_or_else(|| "all update URLs failed".to_string())
    ))
}

/// verify_asset_signature rejects any asset whose bytes do not match the
/// detached base64 ed25519 signature produced by scripts/sign in release CI.
fn verify_asset_signature(data: &[u8], sig_b64: &str) -> Result<()> {
    let sig_bytes = BASE64
        .decode(sig_b64.trim())
        .context("decode release asset signature")?;
    let sig_bytes: [u8; 64] = sig_bytes
        .try_into()
        .map_err(|_| anyhow!("release asset signature must be 64 bytes"))?;
    let signature = Signature::from_bytes(&sig_bytes);
    let key_bytes = BASE64
        .decode(UPDATE_PUBLIC_KEY_B64)
        .context("decode embedded update public key")?;
    let key_bytes: [u8; 32] = key_bytes
        .try_into()
        .map_err(|_| anyhow!("embedded update public key must be 32 bytes"))?;
    let key = VerifyingKey::from_bytes(&key_bytes)
        .map_err(|err| anyhow!("invalid update public key: {err}"))?;
    key.verify(data, &signature)
        .map_err(|err| anyhow!("ed25519 signature mismatch: {err}"))
}

/// valid_proxy_prefix reports whether a proxy prefix uses an acceptable
/// scheme: https, or http only when targeting loopback (for local testing).
fn valid_proxy_prefix(prefix: &str) -> bool {
    if prefix.is_empty() {
        return true;
    }
    let Ok(url) = url::Url::parse(prefix) else {
        return false;
    };
    match url.scheme() {
        "https" => true,
        "http" => match url.host_str() {
            Some("localhost") => true,
            // url::Url keeps IPv6 brackets in host_str (e.g. "[::1]").
            Some(host) => host
                .trim_start_matches('[')
                .trim_end_matches(']')
                .parse::<std::net::IpAddr>()
                .map(|ip| ip.is_loopback())
                .unwrap_or(false),
            None => false,
        },
        _ => false,
    }
}

fn binary_name() -> &'static str {
    if cfg!(windows) {
        "rdev-client-gpu.exe"
    } else {
        "rdev-client-gpu"
    }
}

fn package_dir_name(asset_name: &str) -> &str {
    asset_name
        .strip_suffix(".tar.gz")
        .or_else(|| asset_name.strip_suffix(".zip"))
        .unwrap_or(asset_name)
}

fn github_download_url(version: &str, asset_name: &str) -> String {
    format!(
        "https://github.com/{REPO_OWNER}/{REPO_NAME}/releases/download/v{}/{}",
        normalize_version(version),
        asset_name
    )
}

fn release_asset_name() -> Option<String> {
    let arch = match env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        "arm" => "armv7",
        "x86" => "x86",
        other => other,
    };
    match env::consts::OS {
        "windows" => {
            #[cfg(windows)]
            if crate::winpty::is_legacy_windows() {
                return Some("rdev-client-gpu-windows-win7-amd64.zip".to_string());
            }
            Some(format!("rdev-client-gpu-windows-{arch}.zip"))
        }
        "linux" | "macos" | "android" => Some(format!(
            "rdev-client-gpu-{}-{arch}.tar.gz",
            if env::consts::OS == "macos" {
                "darwin"
            } else {
                env::consts::OS
            }
        )),
        _ => None,
    }
}

fn normalize_version(version: &str) -> String {
    let mut version = version.trim().trim_start_matches('v');
    if let Some(idx) = version.find(['-', '+']) {
        version = &version[..idx];
    }
    if version.is_empty() || !version.as_bytes()[0].is_ascii_digit() {
        return "dev".to_string();
    }
    version.to_string()
}

fn newer_version(latest: &str, current: &str) -> bool {
    let latest = version_parts(latest);
    let current = version_parts(current);
    latest > current
}

fn version_parts(version: &str) -> [u64; 3] {
    let mut out = [0; 3];
    for (idx, part) in version.split('.').take(3).enumerate() {
        out[idx] = part.parse::<u64>().unwrap_or(0);
    }
    out
}

fn proxy_prefixes() -> Vec<String> {
    let mut prefixes = Vec::new();
    if let Ok(env) = env::var("RDEV_UPDATE_PROXY") {
        prefixes.extend(
            env.split(',')
                .map(str::trim)
                .filter(|prefix| !prefix.is_empty())
                .map(ToOwned::to_owned),
        );
    }
    prefixes.extend(
        DEFAULT_PROXY_PREFIXES
            .iter()
            .map(|prefix| (*prefix).to_string()),
    );

    let mut out = Vec::with_capacity(prefixes.len());
    for prefix in prefixes {
        // Security: only https proxy prefixes are allowed (loopback http is
        // kept for tests); a plaintext proxy could otherwise serve tampered
        // assets/signatures and defeat the download path before verification.
        if !valid_proxy_prefix(&prefix) {
            warn!("ignoring insecure auto-update proxy prefix (https required): {prefix}");
            continue;
        }
        if !out.contains(&prefix) {
            out.push(prefix);
        }
    }
    out
}

fn proxied_url(prefix: &str, target: &str) -> String {
    if prefix.is_empty() {
        return target.to_string();
    }
    if prefix.contains("${url}") {
        return prefix.replace("${url}", target);
    }
    format!("{}/{}", prefix.trim_end_matches('/'), target)
}

#[derive(Debug)]
struct FailureLog {
    last_err: String,
    suppressed: usize,
    last_logged_at: Instant,
}

impl Default for FailureLog {
    fn default() -> Self {
        Self {
            last_err: String::new(),
            suppressed: 0,
            last_logged_at: Instant::now() - Duration::from_secs(15 * 60),
        }
    }
}

impl FailureLog {
    fn record(&mut self, err: &anyhow::Error) {
        let message = err.to_string();
        if message != self.last_err {
            if !self.last_err.is_empty() && self.suppressed > 0 {
                warn!(
                    "auto-update check changed after suppressing {} repeated failures",
                    self.suppressed
                );
            }
            self.last_err = message;
            self.suppressed = 0;
            self.last_logged_at = Instant::now();
            warn!("auto-update check failed: {err:#}");
            return;
        }

        self.suppressed += 1;
        if self.last_logged_at.elapsed() >= Duration::from_secs(15 * 60) {
            warn!(
                "auto-update check still failing; suppressed {} repeated failures: {err:#}",
                self.suppressed
            );
            self.suppressed = 0;
            self.last_logged_at = Instant::now();
        }
    }

    fn recovered(&mut self) {
        if self.last_err.is_empty() {
            return;
        }
        if self.suppressed > 0 {
            info!(
                "auto-update check recovered; suppressed {} repeated failures",
                self.suppressed
            );
        }
        self.last_err.clear();
        self.suppressed = 0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_versions() {
        assert_eq!(normalize_version("v0.2.98"), "0.2.98");
        assert_eq!(normalize_version("0.2.98-dirty"), "0.2.98");
        assert_eq!(normalize_version("main"), "dev");
    }

    #[test]
    fn compares_versions() {
        assert!(newer_version("0.2.99", "0.2.98"));
        assert!(newer_version("0.3.0", "0.2.99"));
        assert!(!newer_version("0.2.98", "0.2.98"));
        assert!(!newer_version("0.2.97", "0.2.98"));
    }

    #[test]
    fn strips_package_suffixes() {
        assert_eq!(
            package_dir_name("rdev-client-gpu-linux-amd64.tar.gz"),
            "rdev-client-gpu-linux-amd64"
        );
        assert_eq!(
            package_dir_name("rdev-client-gpu-windows-amd64.zip"),
            "rdev-client-gpu-windows-amd64"
        );
    }

    #[test]
    fn builds_proxy_urls() {
        let target = "https://github.com/icepie/rdev/releases/download/v0.2.98/a.zip";
        assert_eq!(proxied_url("", target), target);
        assert_eq!(
            proxied_url("https://gh-proxy.com/", target),
            format!("https://gh-proxy.com/{target}")
        );
        assert_eq!(
            proxied_url("http://proxy/?url=${url}", target),
            format!("http://proxy/?url={target}")
        );
    }

    #[test]
    fn builds_github_download_url() {
        assert_eq!(
            github_download_url("v0.2.98", "rdev-client-gpu-linux-amd64.tar.gz"),
            "https://github.com/icepie/rdev/releases/download/v0.2.98/rdev-client-gpu-linux-amd64.tar.gz"
        );
    }

    // Test-only key pair; the secret below is a fixed test vector, not the
    // production signing key.
    fn sign_test_payload(data: &[u8]) -> (VerifyingKey, String) {
        use ed25519_dalek::{Signer, SigningKey};
        let secret = [7u8; 32];
        let signing = SigningKey::from_bytes(&secret);
        let signature = signing.sign(data);
        (signing.verifying_key(), BASE64.encode(signature.to_bytes()))
    }

    fn verify_with_key(key: &VerifyingKey, data: &[u8], sig_b64: &str) -> Result<()> {
        let sig_bytes = BASE64.decode(sig_b64.trim()).context("decode sig")?;
        let sig_bytes: [u8; 64] = sig_bytes.try_into().map_err(|_| anyhow!("bad sig len"))?;
        key.verify(data, &Signature::from_bytes(&sig_bytes))
            .map_err(|err| anyhow!("mismatch: {err}"))
    }

    #[test]
    fn verifies_asset_signature() {
        let payload = b"release archive bytes";
        let (key, sig_b64) = sign_test_payload(payload);
        assert!(verify_with_key(&key, payload, &sig_b64).is_ok());

        let mut tampered = payload.to_vec();
        tampered[0] ^= 0xff;
        assert!(verify_with_key(&key, &tampered, &sig_b64).is_err());

        let (_, wrong_sig) = sign_test_payload(b"different payload");
        assert!(verify_with_key(&key, payload, &wrong_sig).is_err());

        assert!(verify_asset_signature(payload, "not-base64!!").is_err());
        assert!(verify_asset_signature(payload, &BASE64.encode([0u8; 10])).is_err());
    }

    #[test]
    fn validates_proxy_prefixes() {
        assert!(valid_proxy_prefix(""));
        assert!(valid_proxy_prefix("https://gh-proxy.com/"));
        assert!(valid_proxy_prefix("https://proxy.example/?url=${url}"));
        assert!(valid_proxy_prefix("http://127.0.0.1:8080/"));
        assert!(valid_proxy_prefix("http://localhost:8080/"));
        assert!(valid_proxy_prefix("http://[::1]:8080/"));
        assert!(!valid_proxy_prefix("http://evil.example/"));
        assert!(!valid_proxy_prefix("http://proxy/?url=${url}"));
        assert!(!valid_proxy_prefix("http://192.168.1.1/"));
        assert!(!valid_proxy_prefix("ftp://proxy.example/"));
        assert!(!valid_proxy_prefix("gh-proxy.com/"));
    }
}
