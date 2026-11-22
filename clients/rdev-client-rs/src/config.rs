use clap::{ArgAction, Parser};

#[derive(Debug, Clone, Parser)]
#[command(
    name = "rdev-client-rs",
    version = crate::version::VERSION,
    disable_version_flag = true,
    about = "GPU-ready RDev client skeleton"
)]
pub struct Args {
    #[arg(short = 'v', long = "version", action = ArgAction::SetTrue)]
    pub version: bool,

    #[arg(
        short = 's',
        long = "server",
        env = "RDEV_SERVER",
        default_value = "tcp://127.0.0.1:8081,ws://127.0.0.1:8080"
    )]
    pub server: String,

    #[arg(short = 'i', long = "id", env = "RDEV_ID", default_value = "")]
    pub id: String,

    /// Device label, independent of the stable ID (defaults to hostname).
    #[arg(long = "display-name", env = "RDEV_DISPLAY_NAME", default_value = "", value_parser = parse_display_name)]
    pub display_name: String,

    #[arg(
        short = 'p',
        long = "password",
        env = "RDEV_PASSWORD",
        default_value = ""
    )]
    pub password: String,

    #[arg(long = "shell", env = "RDEV_SHELL")]
    pub shell: Option<String>,

    #[arg(long = "instance-id", env = "RDEV_INSTANCE_ID")]
    pub instance_id: Option<String>,
    #[arg(skip)]
    pub device_identity: String,
    #[arg(skip)]
    pub reconnect_proof: String,

    #[arg(long = "reconnect-delay", env = "RDEV_RECONNECT_DELAY", default_value = "2s", value_parser = parse_duration)]
    pub reconnect_delay: std::time::Duration,

    #[arg(long = "no-auto-update", env = "RDEV_NO_AUTO_UPDATE")]
    pub no_auto_update: bool,

    #[arg(long = "auto-update", env = "RDEV_AUTO_UPDATE", default_value = "true", value_parser = parse_bool)]
    pub auto_update: bool,

    #[arg(long = "update-interval", env = "RDEV_UPDATE_INTERVAL", default_value = "1m", value_parser = parse_duration)]
    pub update_interval: std::time::Duration,

    #[arg(long = "no-desktop", env = "RDEV_NO_DESKTOP")]
    pub no_desktop: bool,

    #[arg(
        long = "gpu-desktop-local",
        env = "RDEV_GPU_DESKTOP_LOCAL",
        default_value = "127.0.0.1:1701"
    )]
    pub gpu_desktop_local: String,

    #[arg(long = "no-gpu-desktop-tunnel", env = "RDEV_NO_GPU_DESKTOP_TUNNEL")]
    pub no_gpu_desktop_tunnel: bool,

    #[cfg(target_os = "linux")]
    #[arg(long = "gpu-desktop-wayland", env = "RDEV_GPU_DESKTOP_WAYLAND")]
    pub gpu_desktop_wayland: bool,

    #[cfg(target_os = "linux")]
    #[arg(long = "gpu-desktop-kms", env = "RDEV_GPU_DESKTOP_KMS")]
    pub gpu_desktop_kms: bool,

    #[cfg(target_os = "linux")]
    #[arg(long = "gpu-desktop-kms-device", env = "RDEV_GPU_DESKTOP_KMS_DEVICE")]
    pub gpu_desktop_kms_device: Option<String>,

    #[cfg(target_os = "linux")]
    #[arg(long = "gpu-desktop-nvfbc", env = "RDEV_GPU_DESKTOP_NVFBC")]
    pub gpu_desktop_nvfbc: bool,

    #[cfg(target_os = "linux")]
    #[arg(
        long = "gpu-desktop-vaapi",
        env = "RDEV_GPU_DESKTOP_VAAPI",
        default_value_t = false,
        action = ArgAction::Set,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = parse_bool
    )]
    pub gpu_desktop_vaapi: bool,

    #[cfg(any(target_os = "linux", target_os = "windows"))]
    #[arg(
        long = "gpu-desktop-nvenc",
        env = "RDEV_GPU_DESKTOP_NVENC",
        default_value_t = cfg!(feature = "embedded-rdev-desktop-hw"),
        action = ArgAction::Set,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = parse_bool
    )]
    pub gpu_desktop_nvenc: bool,

    #[cfg(target_os = "linux")]
    #[arg(
        long = "gpu-desktop-vulkan-video",
        env = "RDEV_GPU_DESKTOP_VULKAN_VIDEO",
        default_value_t = false,
        action = ArgAction::Set,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = parse_bool
    )]
    pub gpu_desktop_vulkan_video: bool,

    #[cfg(target_os = "macos")]
    #[arg(
        long = "gpu-desktop-videotoolbox",
        env = "RDEV_GPU_DESKTOP_VIDEOTOOLBOX",
        default_value_t = true,
        action = ArgAction::Set,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = parse_bool
    )]
    pub gpu_desktop_videotoolbox: bool,

    #[cfg(target_os = "windows")]
    #[arg(
        long = "gpu-desktop-mediafoundation",
        env = "RDEV_GPU_DESKTOP_MEDIAFOUNDATION",
        default_value_t = true,
        action = ArgAction::Set,
        num_args = 0..=1,
        default_missing_value = "true",
        value_parser = parse_bool
    )]
    pub gpu_desktop_mediafoundation: bool,

    #[cfg(target_os = "windows")]
    #[arg(
        long = "gpu-desktop-windows-capture-source",
        env = "RDEV_GPU_DESKTOP_WINDOWS_CAPTURE_SOURCE",
        default_value = "auto"
    )]
    pub gpu_desktop_windows_capture_source: String,
}

fn parse_display_name(value: &str) -> Result<String, String> {
    let value = value.trim();
    Ok(if value.is_empty() {
        default_display_name()
    } else {
        value.to_owned()
    })
}

fn default_display_name() -> String {
    #[cfg(unix)]
    {
        let mut name = [0u8; 256];
        if unsafe { libc::gethostname(name.as_mut_ptr().cast(), name.len()) } == 0 {
            let len = name.iter().position(|&b| b == 0).unwrap_or(name.len());
            return String::from_utf8_lossy(&name[..len]).into_owned();
        }
    }
    #[cfg(windows)]
    use windows_sys::Win32::System::SystemInformation::{
        ComputerNameDnsHostname, GetComputerNameExW,
    };
    #[cfg(windows)]
    {
        let mut name = [0u16; 256];
        let mut len = name.len() as u32;
        if unsafe { GetComputerNameExW(ComputerNameDnsHostname, name.as_mut_ptr(), &mut len) } != 0
        {
            return String::from_utf16_lossy(&name[..len as usize]);
        }
    }
    String::new()
}

pub fn parse_duration(value: &str) -> Result<std::time::Duration, String> {
    let trimmed = value.trim();
    if let Some(ms) = trimmed.strip_suffix("ms") {
        return ms
            .parse::<u64>()
            .map(std::time::Duration::from_millis)
            .map_err(|e| e.to_string());
    }
    if let Some(s) = trimmed.strip_suffix('s') {
        return s
            .parse::<u64>()
            .map(std::time::Duration::from_secs)
            .map_err(|e| e.to_string());
    }
    if let Some(m) = trimmed.strip_suffix('m') {
        return m
            .parse::<u64>()
            .map(|v| std::time::Duration::from_secs(v * 60))
            .map_err(|e| e.to_string());
    }
    trimmed
        .parse::<u64>()
        .map(std::time::Duration::from_secs)
        .map_err(|e| e.to_string())
}

pub fn parse_bool(value: &str) -> Result<bool, String> {
    match value.trim().to_ascii_lowercase().as_str() {
        "1" | "true" | "yes" | "y" | "on" | "enable" | "enabled" => Ok(true),
        "0" | "false" | "no" | "n" | "off" | "disable" | "disabled" => Ok(false),
        _ => Err(format!("invalid bool value: {value}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::Parser;

    #[test]
    fn blank_display_name_uses_hostname_without_changing_id() {
        for value in ["", " \t", "办公室电脑"] {
            let args = Args::try_parse_from([
                "rdev-client-rs",
                "--id",
                "stable-device",
                "--display-name",
                value,
            ])
            .unwrap();
            let expected = if value.trim().is_empty() {
                default_display_name()
            } else {
                value.to_owned()
            };
            assert_eq!(args.display_name, expected);
            assert_eq!(args.id, "stable-device");
        }
    }

    #[test]
    fn parses_duration_units() {
        assert_eq!(
            parse_duration("250ms").unwrap(),
            std::time::Duration::from_millis(250)
        );
        assert_eq!(
            parse_duration("2s").unwrap(),
            std::time::Duration::from_secs(2)
        );
        assert_eq!(
            parse_duration("3m").unwrap(),
            std::time::Duration::from_secs(180)
        );
        assert_eq!(
            parse_duration("4").unwrap(),
            std::time::Duration::from_secs(4)
        );
    }

    #[test]
    fn parses_bool_values() {
        assert!(parse_bool("true").unwrap());
        assert!(parse_bool("ON").unwrap());
        assert!(!parse_bool("false").unwrap());
        assert!(!parse_bool("0").unwrap());
        assert!(parse_bool("maybe").is_err());
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn parses_optional_bool_flags() {
        let args = Args::parse_from(["rdev-client-rs", "--gpu-desktop-vulkan-video"]);
        assert!(args.gpu_desktop_vulkan_video);

        let args = Args::parse_from([
            "rdev-client-rs",
            "--gpu-desktop-vaapi=false",
            "--gpu-desktop-nvenc=false",
            "--gpu-desktop-vulkan-video=false",
        ]);
        assert!(!args.gpu_desktop_vaapi);
        assert!(!args.gpu_desktop_nvenc);
        assert!(!args.gpu_desktop_vulkan_video);
    }
}
