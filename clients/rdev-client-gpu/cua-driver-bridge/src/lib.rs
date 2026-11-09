#[cfg(feature = "cua-native")]
use anyhow::Context;
use anyhow::{bail, Result};
use serde::Deserialize;
use serde_json::{Map, Value};
use std::time::Duration;
#[cfg(feature = "cua-native")]
use std::{
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
};
#[cfg(feature = "cua-native")]
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader},
    process::{ChildStdin, ChildStdout},
    sync::Mutex,
};

const VERSION: &str = "0.45.2";
const ASSET_BASE: &str = "https://github.com/trycua/cua/releases/download/cua-driver-rs-v0.45.2/";
const MAX_ARCHIVE: u64 = 96 * 1024 * 1024;
const MAX_FRAME: usize = 8 * 1024 * 1024;
const MAX_ARGUMENTS: usize = 256 * 1024;
const RPC_TIMEOUT: Duration = Duration::from_secs(90);
const ALLOWED_TOOLS: &[&str] = &[
    "list_apps",
    "list_windows",
    "get_window_state",
    "get_desktop_state",
    "bring_to_front",
    "click",
    "scroll",
    "drag",
    "press_key",
    "type_text",
    "set_value",
    "perform_action",
];
const INSTALL_TIMEOUT: Duration = Duration::from_secs(240);
#[cfg(all(feature = "cua-native", target_os = "linux", not(target_env = "musl")))]
// Both pinned ELF executables require GLIBC_2.30 (readelf version needs).
const MIN_GLIBC: (u32, u32) = (2, 30);

#[derive(Debug, Deserialize)]
#[serde(tag = "action", rename_all = "snake_case", deny_unknown_fields)]
pub enum Request {
    Tools,
    Call {
        name: String,
        #[serde(default)]
        arguments: Map<String, Value>,
    },
}

#[cfg(not(feature = "cua-native"))]
pub fn supported() -> bool {
    false
}

#[cfg(feature = "cua-native")]
pub fn supported() -> bool {
    platform_asset().is_some()
}

#[cfg(feature = "cua-native")]
fn platform_asset() -> Option<(&'static str, &'static str, &'static str, &'static str)> {
    #[cfg(all(
        target_os = "macos",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ))]
    {
        if macos_version_at_least_13() {
            return Some((
                "cua-driver-rs-0.45.2-darwin-universal-binary.tar.gz",
                "348e08b1c3eca92b45c734fd85ba63555b47b4a4a3063e25f8466f108dedb3f4",
                "tar.gz",
                "cua-driver",
            ));
        }
    }
    #[cfg(all(
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64"),
        not(target_env = "musl")
    ))]
    {
        let version = unsafe { libc::gnu_get_libc_version() };
        if version.is_null() {
            return None;
        }
        let version = unsafe { std::ffi::CStr::from_ptr(version) }.to_str().ok()?;
        let mut pieces = version
            .split('.')
            .filter_map(|part| part.parse::<u32>().ok());
        let actual = (pieces.next()?, pieces.next()?);
        if actual < MIN_GLIBC {
            return None;
        }
        #[cfg(target_arch = "x86_64")]
        {
            return Some((
                "cua-driver-rs-0.45.2-linux-x86_64-binary.tar.gz",
                "717059450683d69e7e5e891b54e081b8a9d081461d463a837c9ccac45bdd33b6",
                "tar.gz",
                "cua-driver",
            ));
        }
        #[cfg(target_arch = "aarch64")]
        {
            return Some((
                "cua-driver-rs-0.45.2-linux-arm64-binary.tar.gz",
                "e11eb69982ea017583194273aa2c3d68d3800e761414255b7f6820bb33657d53",
                "tar.gz",
                "cua-driver",
            ));
        }
    }
    #[cfg(all(
        target_os = "windows",
        any(target_arch = "x86_64", target_arch = "aarch64")
    ))]
    {
        if windows_version_at_least_10() {
            #[cfg(target_arch = "x86_64")]
            {
                return Some((
                    "cua-driver-rs-0.45.2-windows-x86_64-binary.zip",
                    "81c88c122af896c5de6e661ccc4029737612185235eb28197d275efef60b642c",
                    "zip",
                    "cua-driver.exe",
                ));
            }
            #[cfg(target_arch = "aarch64")]
            {
                return Some((
                    "cua-driver-rs-0.45.2-windows-arm64-binary.zip",
                    "363c8b12e644bf6c0e3d74c77b1c52bc18346035d5745ceb75f8381e1a0d0fb2",
                    "zip",
                    "cua-driver.exe",
                ));
            }
        }
    }
    #[cfg(not(all(
        target_os = "linux",
        any(target_arch = "x86_64", target_arch = "aarch64"),
        not(target_env = "musl")
    )))]
    None
}

#[cfg(all(feature = "cua-native", windows))]
fn windows_version_at_least_10() -> bool {
    #[repr(C)]
    struct VersionInfo {
        size: u32,
        major: u32,
        minor: u32,
        build: u32,
        platform: u32,
        service_pack: [u16; 128],
    }
    #[link(name = "ntdll")]
    unsafe extern "system" {
        fn RtlGetVersion(info: *mut VersionInfo) -> i32;
    }
    let mut info = VersionInfo {
        size: std::mem::size_of::<VersionInfo>() as u32,
        major: 0,
        minor: 0,
        build: 0,
        platform: 0,
        service_pack: [0; 128],
    };
    unsafe { RtlGetVersion(&mut info) == 0 && info.major >= 10 }
}

#[cfg(all(feature = "cua-native", target_os = "macos"))]
fn macos_version_at_least_13() -> bool {
    let key = b"kern.osproductversion\0";
    let mut bytes = [0u8; 32];
    let mut length = bytes.len();
    let status = unsafe {
        libc::sysctlbyname(
            key.as_ptr().cast(),
            bytes.as_mut_ptr().cast(),
            &mut length,
            std::ptr::null_mut(),
            0,
        )
    };
    if status != 0 || length == 0 || length > bytes.len() {
        return false;
    }
    let Ok(version) = std::str::from_utf8(&bytes[..length.saturating_sub(1)]) else {
        return false;
    };
    version
        .split('.')
        .next()
        .and_then(|major| major.parse::<u32>().ok())
        .is_some_and(|major| major >= 13)
}

pub struct Session {
    #[cfg(feature = "cua-native")]
    inner: Mutex<Option<Runtime>>,
}

#[cfg(feature = "cua-native")]
struct Runtime {
    child: Child,
    #[cfg(windows)]
    job: Job,
    input: ChildStdin,
    output: BufReader<ChildStdout>,
    next_id: u64,
    session_id: String,
    session_fields: Vec<(String, String)>,
    tools_loaded: bool,
}

#[cfg(feature = "cua-native")]
impl Drop for Runtime {
    fn drop(&mut self) {
        #[cfg(windows)]
        self.job.terminate();
        terminate(&mut self.child);
    }
}

impl Session {
    pub fn new() -> Self {
        Self {
            #[cfg(feature = "cua-native")]
            inner: Mutex::new(None),
        }
    }

    pub async fn request(&mut self, request: Request) -> Result<Value> {
        if !supported() {
            bail!("native CUA is unsupported on this build or platform");
        }
        #[cfg(feature = "cua-native")]
        {
            if let Request::Call { name, arguments } = &request {
                if !ALLOWED_TOOLS.contains(&name.as_str()) {
                    bail!("native CUA tool is not allowed: {name}");
                }
                validate_arguments(arguments)?;
                if serde_json::to_vec(arguments)?.len() > MAX_ARGUMENTS {
                    bail!("native CUA arguments exceed size limit");
                }
            }
            let mut guard = self.inner.lock().await;
            let mut runtime = match guard.take() {
                Some(runtime) => runtime,
                None => launch().await?,
            };
            // The in-flight future owns the runtime: cancellation drops it and
            // synchronously kills/reaps the child before any later request.
            let result = async {
                match request {
                    Request::Tools => {
                        let response = rpc(&mut runtime, "tools/list", None).await?;
                        filter_tools(&mut runtime, response)
                    }
                    Request::Call {
                        name,
                        mut arguments,
                    } => {
                        if !runtime.tools_loaded {
                            let inventory = rpc(&mut runtime, "tools/list", None).await?;
                            filter_tools(&mut runtime, inventory)?;
                        }
                        for (_, field) in runtime
                            .session_fields
                            .iter()
                            .filter(|(tool, _)| tool == &name)
                        {
                            arguments
                                .insert(field.clone(), Value::String(runtime.session_id.clone()));
                        }
                        rpc(
                            &mut runtime,
                            "tools/call",
                            Some(serde_json::json!({"name":name,"arguments":arguments})),
                        )
                        .await
                    }
                }
            }
            .await;
            if result.is_ok() {
                *guard = Some(runtime);
            }
            result
        }
        #[cfg(not(feature = "cua-native"))]
        {
            let _ = request;
            unreachable!()
        }
    }

    #[cfg(feature = "cua-native")]
    pub async fn shutdown(&mut self) -> Result<()> {
        let mut guard = self.inner.lock().await;
        drop(guard.take());
        Ok(())
    }
    #[cfg(not(feature = "cua-native"))]
    pub async fn shutdown(&mut self) -> Result<()> {
        Ok(())
    }
}
impl Default for Session {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(feature = "cua-native")]
impl Drop for Session {
    fn drop(&mut self) {
        if let Ok(mut guard) = self.inner.try_lock() {
            drop(guard.take());
        }
    }
}

#[cfg(feature = "cua-native")]
async fn rpc(runtime: &mut Runtime, method: &str, params: Option<Value>) -> Result<Value> {
    let id = runtime.next_id;
    runtime.next_id += 1;
    let mut request = serde_json::json!({"jsonrpc":"2.0","id":id,"method":method});
    if let Some(params) = params {
        request["params"] = params;
    }
    let mut encoded = serde_json::to_vec(&request)?;
    if encoded.len() > MAX_FRAME {
        bail!("MCP request exceeds frame limit");
    }
    encoded.push(b'\n');
    tokio::time::timeout(RPC_TIMEOUT, async {
        runtime.input.write_all(&encoded).await?;
        runtime.input.flush().await?;
        loop {
            let mut line = Vec::new();
            loop {
                let available = runtime.output.fill_buf().await?;
                if available.is_empty() {
                    bail!("Cua Driver MCP process closed stdout");
                }
                let count = available
                    .iter()
                    .position(|byte| *byte == b'\n')
                    .map_or(available.len(), |index| index + 1);
                if line.len() + count > MAX_FRAME {
                    bail!("Cua Driver MCP response exceeds frame limit");
                }
                let complete = available.get(count.saturating_sub(1)) == Some(&b'\n');
                line.extend_from_slice(&available[..count]);
                runtime.output.consume(count);
                if complete {
                    break;
                }
            }
            let value: Value = serde_json::from_slice(&line)
                .context("invalid Cua Driver MCP JSON-RPC response")?;
            if value.get("id").and_then(Value::as_u64) != Some(id) {
                continue;
            }
            if let Some(error) = value.get("error") {
                bail!("Cua Driver MCP error: {error}");
            }
            return value
                .get("result")
                .cloned()
                .context("MCP response has no result");
        }
    })
    .await
    .context("Cua Driver MCP request timed out")?
}

#[cfg(feature = "cua-native")]
async fn launch() -> Result<Runtime> {
    let (asset, sha, format, executable) =
        platform_asset().context("unsupported Cua Driver target")?;
    let binary = tokio::time::timeout(
        INSTALL_TIMEOUT,
        ensure_binary(asset, sha, format, executable),
    )
    .await
    .context("Cua Driver installation timed out")??;
    let mut command = Command::new(binary);
    command
        .args(["mcp", "--direct"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        command.creation_flags(0x00000004);
    }
    let mut child = command
        .spawn()
        .context("spawn private Cua Driver MCP process")?;
    #[cfg(windows)]
    let job = match Job::attach_and_resume(&child) {
        Ok(job) => job,
        Err(error) => {
            terminate(&mut child);
            return Err(error);
        }
    };
    let pipes = (|| -> Result<_> {
        let input =
            ChildStdin::from_std(child.stdin.take().context("Cua Driver stdin unavailable")?)?;
        let stdout = ChildStdout::from_std(
            child
                .stdout
                .take()
                .context("Cua Driver stdout unavailable")?,
        )?;
        Ok((input, stdout))
    })();
    let (input, stdout) = match pipes {
        Ok(pipes) => pipes,
        Err(error) => {
            terminate(&mut child);
            return Err(error);
        }
    };
    let mut runtime = Runtime {
        child,
        #[cfg(windows)]
        job,
        input,
        output: BufReader::new(stdout),
        next_id: 1,
        session_id: uuid::Uuid::new_v4().to_string(),
        session_fields: Vec::new(),
        tools_loaded: false,
    };
    let result = tokio::time::timeout(RPC_TIMEOUT, async {
        let initialized = rpc(&mut runtime, "initialize", Some(serde_json::json!({"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"rdev","version":"0.1.0"}}))).await?;
        if initialized.get("protocolVersion").and_then(Value::as_str) != Some("2025-06-18") { bail!("unexpected MCP protocol version"); }
        let mut notification = serde_json::to_vec(&serde_json::json!({"jsonrpc":"2.0","method":"notifications/initialized"}))?;
        notification.push(b'\n');
        runtime.input.write_all(&notification).await?;
        runtime.input.flush().await?;
        Ok::<(), anyhow::Error>(())
    }).await;
    result.context("initialize Cua Driver MCP timed out")??;
    Ok(runtime)
}

#[cfg(feature = "cua-native")]
async fn ensure_binary(
    asset: &str,
    expected_sha: &str,
    format: &str,
    executable: &str,
) -> Result<PathBuf> {
    use sha2::{Digest, Sha256};
    let root = dirs::cache_dir()
        .context("user cache directory unavailable")?
        .join("rdev")
        .join("cua-driver")
        .join(VERSION)
        .join(asset);
    tokio::fs::create_dir_all(&root).await?;
    let root_metadata = std::fs::symlink_metadata(&root)?;
    if !root_metadata.is_dir() || root_metadata.file_type().is_symlink() {
        bail!("Cua cache root is not a real directory");
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if root_metadata.uid() != unsafe { libc::geteuid() } {
            bail!("Cua cache belongs to another user");
        }
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&root, std::fs::Permissions::from_mode(0o700))?;
    }
    let lock_path = root.join("install.lock");
    let mut options = std::fs::OpenOptions::new();
    options.create(true).read(true).write(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600).custom_flags(libc::O_NOFOLLOW);
    }
    let lock_file = options.open(lock_path)?;
    loop {
        match fs2::FileExt::try_lock_exclusive(&lock_file) {
            Ok(()) => break,
            Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                tokio::time::sleep(Duration::from_millis(50)).await
            }
            Err(error) => return Err(error.into()),
        }
    }
    let install = root.join("runtime");
    let bin = install.join(executable);
    let marker = root.join("verified.sha256");
    if tokio::fs::symlink_metadata(&install)
        .await
        .is_ok_and(|metadata| metadata.is_dir() && !metadata.file_type().is_symlink())
        && tokio::fs::symlink_metadata(&bin)
            .await
            .is_ok_and(|metadata| metadata.is_file() && !metadata.file_type().is_symlink())
        && tokio::fs::read_to_string(&marker).await.ok().as_deref() == Some(expected_sha)
    {
        return Ok(bin);
    }
    if tokio::fs::symlink_metadata(&install).await.is_ok() {
        tokio::fs::remove_dir_all(&install).await?;
    }
    let _ = tokio::fs::remove_file(&marker).await;
    let mut archive = reqwest::Client::new()
        .get(format!("{ASSET_BASE}{asset}"))
        .send()
        .await?
        .error_for_status()?;
    if archive
        .content_length()
        .is_some_and(|len| len > MAX_ARCHIVE)
    {
        bail!("CUA release asset exceeds size limit");
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = archive.chunk().await? {
        if bytes.len() as u64 + chunk.len() as u64 > MAX_ARCHIVE {
            bail!("CUA release asset exceeds size limit");
        }
        bytes.extend_from_slice(&chunk);
    }
    let digest = format!("{:x}", Sha256::digest(&bytes));
    if digest != expected_sha {
        bail!("Cua Driver archive SHA-256 mismatch");
    }
    let staging = Staging(root.join(format!(".extract-{}", uuid::Uuid::new_v4())));
    std::fs::create_dir(&staging.0)?;
    let zip = format == "zip";
    let executable = executable.to_owned();
    let (staging, _lock_file) = tokio::task::spawn_blocking(move || -> Result<_> {
        if zip {
            extract_zip(&bytes, &staging.0, &executable)?;
        } else {
            extract_tar_gz(&bytes, &staging.0, &executable)?;
        }
        Ok((staging, lock_file))
    })
    .await
    .context("Cua archive extraction worker failed")??;
    std::fs::rename(&staging.0, &install)?;
    std::fs::write(&marker, expected_sha)?;
    Ok(bin)
}

#[cfg(feature = "cua-native")]
fn extract_tar_gz(bytes: &[u8], destination: &Path, executable: &str) -> Result<()> {
    let decoder = flate2::read::GzDecoder::new(bytes);
    let mut archive = tar::Archive::new(decoder);
    let mut found = false;
    let mut total = 0u64;
    for item in archive.entries()? {
        let mut entry = item?;
        if entry.size() > 128 * 1024 * 1024 {
            bail!("archive member exceeds extraction limit");
        }
        total = total
            .checked_add(entry.size())
            .context("archive extraction size overflow")?;
        if total > 512 * 1024 * 1024 {
            bail!("archive exceeds total extraction limit");
        }
        let path = entry.path()?.into_owned();
        if path
            .components()
            .any(|part| !matches!(part, std::path::Component::Normal(_)))
        {
            bail!("unsafe release archive path");
        }
        if entry.header().entry_type().is_dir() {
            std::fs::create_dir_all(destination.join(&path))?;
            continue;
        }
        if !entry.header().entry_type().is_file() {
            bail!("release archive contains unsupported non-file member");
        }
        let output = destination.join(&path);
        if let Some(parent) = output.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&output)?;
        std::io::copy(&mut entry, &mut file)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let is_executable = path.file_name().and_then(|name| name.to_str()) == Some(executable)
                || (entry.header().mode()? & 0o111 != 0
                    && path.extension().and_then(|extension| extension.to_str()) != Some("sh"));
            std::fs::set_permissions(
                &output,
                std::fs::Permissions::from_mode(if is_executable { 0o700 } else { 0o600 }),
            )?;
        }
        if path.file_name().and_then(|n| n.to_str()) == Some(executable) {
            found = true;
        }
    }
    if !found {
        bail!("expected executable absent from release tarball");
    }
    Ok(())
}

#[cfg(feature = "cua-native")]
fn extract_zip(bytes: &[u8], destination: &Path, executable: &str) -> Result<()> {
    let mut archive = zip::ZipArchive::new(std::io::Cursor::new(bytes))?;
    let mut found = false;
    let mut total = 0u64;
    for index in 0..archive.len() {
        let mut entry = archive.by_index(index)?;
        if entry
            .unix_mode()
            .is_some_and(|mode| mode & 0o170000 == 0o120000)
        {
            bail!("release archive contains symlink");
        }
        if entry.is_dir() {
            continue;
        }
        if entry.size() > 128 * 1024 * 1024 {
            bail!("archive member exceeds extraction limit");
        }
        total = total
            .checked_add(entry.size())
            .context("archive extraction size overflow")?;
        if total > 512 * 1024 * 1024 {
            bail!("archive exceeds total extraction limit");
        }
        let path = Path::new(entry.name());
        if path
            .components()
            .any(|part| !matches!(part, std::path::Component::Normal(_)))
        {
            bail!("unsafe release archive path");
        }
        let output = destination.join(path);
        if let Some(parent) = output.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let mut file = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&output)?;
        let is_executable = path.file_name().and_then(|n| n.to_str()) == Some(executable);
        std::io::copy(&mut entry, &mut file)?;
        if is_executable {
            found = true;
        }
    }
    if !found {
        bail!("expected executable absent from release zip");
    }
    Ok(())
}

#[cfg(feature = "cua-native")]
struct Staging(PathBuf);

#[cfg(feature = "cua-native")]
impl Drop for Staging {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

#[cfg(feature = "cua-native")]
fn filter_tools(runtime: &mut Runtime, mut response: Value) -> Result<Value> {
    let tools = response
        .get_mut("tools")
        .and_then(Value::as_array_mut)
        .context("MCP tools/list omitted tools array")?;
    tools.retain(|tool| {
        tool.get("name")
            .and_then(Value::as_str)
            .is_some_and(|name| ALLOWED_TOOLS.contains(&name))
    });
    runtime.session_fields.clear();
    for tool in tools.iter_mut() {
        let name = tool
            .get("name")
            .and_then(Value::as_str)
            .context("tool schema has no name")?
            .to_owned();
        if let Some(schema) = tool.get_mut("inputSchema") {
            if let Some(properties) = schema.get_mut("properties").and_then(Value::as_object_mut) {
                for field in ["session", "session_id", "_session_id"] {
                    if properties.remove(field).is_some() {
                        runtime
                            .session_fields
                            .push((name.clone(), field.to_owned()));
                    }
                }
            }
            if let Some(required) = schema.get_mut("required").and_then(Value::as_array_mut) {
                required.retain(|field| {
                    !matches!(
                        field.as_str(),
                        Some("session" | "session_id" | "_session_id")
                    )
                });
            }
        }
    }
    runtime.tools_loaded = true;
    Ok(response)
}

fn validate_arguments(arguments: &Map<String, Value>) -> Result<()> {
    fn inspect(value: &Value) -> Result<()> {
        if let Value::Object(object) = value {
            for (key, child) in object {
                let key = key.to_ascii_lowercase();
                if matches!(key.as_str(), "session" | "session_id" | "_session_id") {
                    bail!("session argument is reserved by bridge");
                }
                if matches!(
                    key.as_str(),
                    "output_path" | "output_file" | "file_path" | "filename" | "save_to"
                ) {
                    bail!("file output arguments are not allowed");
                }
                inspect(child)?;
            }
        } else if let Value::Array(array) = value {
            for child in array {
                inspect(child)?;
            }
        }
        Ok(())
    }
    for (key, value) in arguments {
        let normalized = key.to_ascii_lowercase();
        if matches!(
            normalized.as_str(),
            "session" | "session_id" | "_session_id"
        ) {
            bail!("session argument is reserved by bridge");
        }
        if matches!(
            normalized.as_str(),
            "output_path" | "output_file" | "file_path" | "filename" | "save_to"
        ) {
            bail!("file output arguments are not allowed");
        }
        inspect(value)?;
    }
    Ok(())
}

#[cfg(feature = "cua-native")]
fn terminate(child: &mut Child) {
    kill_now(child);
    let _ = child.wait();
}
#[cfg(all(feature = "cua-native", unix))]
fn kill_now(child: &mut Child) {
    unsafe {
        libc::kill(-(child.id() as i32), libc::SIGKILL);
    }
    let _ = child.kill();
}
#[cfg(all(feature = "cua-native", windows))]
fn kill_now(child: &mut Child) {
    // Descendants are terminated by the Runtime-owned Job Object first.
    let _ = child.kill();
}

#[cfg(all(feature = "cua-native", windows))]
struct Job(std::os::windows::io::OwnedHandle);

#[cfg(all(feature = "cua-native", windows))]
impl Job {
    fn attach_and_resume(child: &Child) -> Result<Self> {
        use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
        use windows_sys::Win32::System::JobObjects::*;
        let handle = unsafe { CreateJobObjectW(std::ptr::null(), std::ptr::null()) };
        if handle.is_null() {
            return Err(std::io::Error::last_os_error().into());
        }
        let job = Self(unsafe { OwnedHandle::from_raw_handle(handle) });
        let mut limits = JOBOBJECT_EXTENDED_LIMIT_INFORMATION::default();
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        if unsafe {
            SetInformationJobObject(
                handle,
                JobObjectExtendedLimitInformation,
                (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                std::mem::size_of_val(&limits) as u32,
            )
        } == 0
        {
            return Err(std::io::Error::last_os_error().into());
        }
        if unsafe { AssignProcessToJobObject(handle, child.as_raw_handle()) } == 0 {
            return Err(std::io::Error::last_os_error().into());
        }
        #[link(name = "ntdll")]
        unsafe extern "system" {
            fn NtResumeProcess(process: *mut std::ffi::c_void) -> i32;
        }
        let status = unsafe { NtResumeProcess(child.as_raw_handle()) };
        if status < 0 {
            bail!("resume CUA driver failed with NTSTATUS {status:#x}");
        }
        Ok(job)
    }

    fn terminate(&self) {
        use std::os::windows::io::AsRawHandle;
        unsafe {
            windows_sys::Win32::System::JobObjects::TerminateJobObject(self.0.as_raw_handle(), 1);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_host_session_overrides_at_every_depth() {
        for value in [
            serde_json::json!({"session": "foreign"}),
            serde_json::json!({"target": {"Session_ID": "foreign"}}),
            serde_json::json!({"targets": [{"_session_id": "foreign"}]}),
        ] {
            assert!(validate_arguments(value.as_object().unwrap()).is_err());
        }
    }

    #[test]
    fn forbids_file_outputs_but_accepts_native_target_parameters() {
        assert!(validate_arguments(
            serde_json::json!({"output_path":"/tmp/capture.png"})
                .as_object()
                .unwrap()
        )
        .is_err());
        assert!(validate_arguments(
            serde_json::json!({"window_id":"123", "include_screenshot":false})
                .as_object()
                .unwrap()
        )
        .is_ok());
    }

    #[cfg(not(feature = "cua-native"))]
    #[test]
    fn feature_off_is_unsupported() {
        assert!(!supported());
    }
}
