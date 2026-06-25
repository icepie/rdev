use std::{
    env,
    fs::File,
    path::{Path, PathBuf},
    thread,
    time::{Duration, Instant},
};

use anyhow::{anyhow, Context, Result};
use self_update::{backends::github, Download, Extract};
use tempfile::TempDir;
use tracing::{info, warn};

const REPO_OWNER: &str = "icepie";
const REPO_NAME: &str = "rdev";

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
        match check_and_apply(&cfg) {
            Ok(true) => {
                info!("auto-update applied; restart rdev-client-gpu to use the new version");
                return;
            }
            Ok(false) => failures.recovered(),
            Err(err) => failures.record(&err),
        }
        thread::sleep(cfg.interval);
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

    let latest = normalize_version(&release.version);
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
        .assets
        .iter()
        .find(|asset| asset.name == asset_name)
        .ok_or_else(|| {
            anyhow!(
                "release v{} asset {} not found",
                release.version,
                asset_name
            )
        })?;

    let tmp = TempDir::new().context("create updater temp dir")?;
    let archive_path = tmp.path().join(&asset.name);
    let download_url = github_download_url(&release.version, &asset.name);
    download_with_proxies(&download_url, &archive_path)
        .with_context(|| format!("download release asset {}", asset.name))?;

    let bin_path = PathBuf::from(package_dir_name(&asset.name)).join(binary_name());
    Extract::from_source(&archive_path)
        .extract_file(tmp.path(), &bin_path)
        .with_context(|| format!("extract {} from {}", bin_path.display(), asset.name))?;

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
        let _ = download.header("User-Agent", "rdev-auto-updater");
        let _ = download.header("Accept", "application/octet-stream");
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
}
