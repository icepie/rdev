use clap::Parser;

#[derive(Debug, Clone, Parser)]
#[command(
    name = "rdev-client-gpu",
    version,
    about = "GPU-ready RDev client skeleton"
)]
pub struct Args {
    #[arg(
        short = 's',
        long = "server",
        env = "RDEV_SERVER",
        default_value = "ws://127.0.0.1:8080"
    )]
    pub server: String,

    #[arg(short = 'i', long = "id", env = "RDEV_ID", default_value = "")]
    pub id: String,

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

    #[arg(long = "reconnect-delay", env = "RDEV_RECONNECT_DELAY", default_value = "2s", value_parser = parse_duration)]
    pub reconnect_delay: std::time::Duration,

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
        default_value_t = cfg!(feature = "embedded-rdev-desktop-vaapi")
    )]
    pub gpu_desktop_vaapi: bool,

    #[cfg(any(target_os = "linux", target_os = "windows"))]
    #[arg(
        long = "gpu-desktop-nvenc",
        env = "RDEV_GPU_DESKTOP_NVENC",
        default_value_t = cfg!(feature = "embedded-rdev-desktop-hw")
    )]
    pub gpu_desktop_nvenc: bool,

    #[cfg(target_os = "linux")]
    #[arg(
        long = "gpu-desktop-vulkan-video",
        env = "RDEV_GPU_DESKTOP_VULKAN_VIDEO"
    )]
    pub gpu_desktop_vulkan_video: bool,

    #[cfg(target_os = "macos")]
    #[arg(
        long = "gpu-desktop-videotoolbox",
        env = "RDEV_GPU_DESKTOP_VIDEOTOOLBOX"
    )]
    pub gpu_desktop_videotoolbox: bool,

    #[cfg(target_os = "windows")]
    #[arg(
        long = "gpu-desktop-mediafoundation",
        env = "RDEV_GPU_DESKTOP_MEDIAFOUNDATION"
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

#[cfg(test)]
mod tests {
    use super::*;

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
}
