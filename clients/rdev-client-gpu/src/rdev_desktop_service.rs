use crate::config::Args;

#[cfg(feature = "embedded-rdev-desktop")]
pub struct GpuDesktopService {
    _weylus: rdev_desktop::Weylus,
}

#[cfg(not(feature = "embedded-rdev-desktop"))]
pub struct GpuDesktopService;

#[cfg(feature = "embedded-rdev-desktop")]
pub fn start(args: &Args) -> Option<GpuDesktopService> {
    if args.no_desktop || args.no_gpu_desktop_tunnel {
        return None;
    }
    let bind_addr: std::net::SocketAddr = match args.gpu_desktop_local.parse() {
        Ok(addr) => addr,
        Err(err) => {
            tracing::warn!(
                "invalid --gpu-desktop-local {}; embedded GPU desktop disabled: {err}",
                args.gpu_desktop_local
            );
            return None;
        }
    };
    let config = rdev_desktop::DesktopServiceConfig {
        bind_addr,
        no_gui: true,
        #[cfg(target_os = "linux")]
        wayland_support: args.gpu_desktop_wayland,
        #[cfg(target_os = "linux")]
        kms_support: args.gpu_desktop_kms,
        #[cfg(target_os = "linux")]
        kms_device: args.gpu_desktop_kms_device.clone(),
        #[cfg(target_os = "linux")]
        nvfbc_support: args.gpu_desktop_nvfbc,
        #[cfg(target_os = "linux")]
        try_vaapi: args.gpu_desktop_vaapi,
        #[cfg(any(target_os = "linux", target_os = "windows"))]
        try_nvenc: args.gpu_desktop_nvenc,
        #[cfg(target_os = "linux")]
        try_vulkan_video: args.gpu_desktop_vulkan_video,
        #[cfg(target_os = "macos")]
        try_videotoolbox: args.gpu_desktop_videotoolbox,
        #[cfg(target_os = "windows")]
        try_mediafoundation: args.gpu_desktop_mediafoundation,
        #[cfg(target_os = "windows")]
        windows_capture_source: args.gpu_desktop_windows_capture_source.clone(),
        ..Default::default()
    };
    match tokio::task::block_in_place(|| rdev_desktop::start_desktop_service(config)) {
        Some(weylus) => {
            tracing::info!("embedded GPU desktop service listening on {bind_addr}");
            Some(GpuDesktopService { _weylus: weylus })
        }
        None => {
            tracing::warn!("embedded GPU desktop service failed to start on {bind_addr}");
            None
        }
    }
}

#[cfg(not(feature = "embedded-rdev-desktop"))]
pub fn start(args: &Args) -> Option<GpuDesktopService> {
    if !args.no_desktop && !args.no_gpu_desktop_tunnel {
        tracing::warn!(
            "embedded GPU desktop service is not included in this build; rebuild with --features embedded-rdev-desktop"
        );
    }
    None
}
