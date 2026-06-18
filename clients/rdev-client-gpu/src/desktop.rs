use crate::protocol::DesktopCapabilities;
#[cfg(feature = "embedded-rdev-desktop")]
use crate::protocol::DesktopSource;

pub fn capabilities(enabled: bool) -> DesktopCapabilities {
    if !enabled {
        return DesktopCapabilities {
            platform: std::env::consts::OS.to_string(),
            supported: false,
            view_only: true,
            input: false,
            clipboard: false,
            reason: "embedded GPU desktop service is not active".to_string(),
            ..Default::default()
        };
    }

    #[cfg(feature = "embedded-rdev-desktop")]
    {
        return DesktopCapabilities {
            platform: std::env::consts::OS.to_string(),
            display_server: display_server(),
            supported: true,
            view_only: false,
            input: true,
            clipboard: false,
            backends: desktop_backends(),
            input_backends: vec!["rdev-desktop".to_string()],
            video_codecs: vec!["h264".to_string()],
            encoder_backends: encoder_backends(),
            sources: vec![DesktopSource {
                id: "auto".to_string(),
                label: "Embedded RDev Desktop".to_string(),
                kind: "screen".to_string(),
                backend: "rdev-desktop".to_string(),
                primary: true,
                ..Default::default()
            }],
            ..Default::default()
        };
    }

    #[cfg(not(feature = "embedded-rdev-desktop"))]
    {
        DesktopCapabilities {
            platform: std::env::consts::OS.to_string(),
            supported: false,
            view_only: true,
            input: false,
            clipboard: false,
            reason: "embedded GPU desktop service is not included in this build".to_string(),
            ..Default::default()
        }
    }
}

#[cfg(feature = "embedded-rdev-desktop")]
fn desktop_backends() -> Vec<String> {
    let mut backends = vec!["rdev-desktop".to_string(), "gpu-desktop-tunnel".to_string()];
    if cfg!(feature = "embedded-rdev-desktop-wayland") {
        backends.push("wayland".to_string());
        backends.push("pipewire".to_string());
    }
    if cfg!(feature = "embedded-rdev-desktop-vaapi") {
        backends.push("vaapi".to_string());
    }
    if cfg!(feature = "embedded-rdev-desktop-hw") {
        backends.push("nvenc".to_string());
    }
    backends
}

#[cfg(feature = "embedded-rdev-desktop")]
fn encoder_backends() -> Vec<String> {
    let mut encoders = vec!["libx264".to_string()];
    if cfg!(feature = "embedded-rdev-desktop-vaapi") {
        encoders.push("vaapi".to_string());
    }
    if cfg!(feature = "embedded-rdev-desktop-hw") {
        encoders.push("nvenc".to_string());
    }
    encoders
}

#[cfg(feature = "embedded-rdev-desktop")]
fn display_server() -> String {
    if std::env::var_os("WAYLAND_DISPLAY").is_some() {
        "wayland".to_string()
    } else if std::env::var_os("DISPLAY").is_some() {
        "x11".to_string()
    } else {
        String::new()
    }
}
