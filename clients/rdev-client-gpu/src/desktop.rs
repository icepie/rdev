use crate::protocol::{DesktopCapabilities, DesktopSource};

pub fn capabilities(enabled: bool) -> DesktopCapabilities {
    if !enabled {
        return DesktopCapabilities {
            platform: std::env::consts::OS.to_string(),
            supported: false,
            view_only: true,
            input: false,
            clipboard: false,
            reason: "desktop disabled; GPU video pipeline is staged for a later milestone"
                .to_string(),
            ..Default::default()
        };
    }
    DesktopCapabilities {
        platform: std::env::consts::OS.to_string(),
        supported: false,
        view_only: true,
        input: false,
        clipboard: false,
        backends: vec!["gpu-video-planned".to_string()],
        video_codecs: vec!["h264".to_string()],
        encoder_backends: planned_encoders(),
        sources: vec![DesktopSource {
            id: "auto".to_string(),
            label: "GPU video pipeline (planned)".to_string(),
            kind: "screen".to_string(),
            backend: "gpu".to_string(),
            primary: true,
            ..Default::default()
        }],
        reason: "terminal/session transport is ready; desktop video capture will be enabled after the Rust GPU pipeline lands".to_string(),
        ..Default::default()
    }
}

fn planned_encoders() -> Vec<String> {
    match std::env::consts::OS {
        "linux" => vec!["libx264", "vaapi", "nvenc"],
        "windows" => vec!["mediafoundation", "nvenc"],
        "macos" => vec!["videotoolbox"],
        _ => vec!["libx264"],
    }
    .into_iter()
    .map(str::to_string)
    .collect()
}
