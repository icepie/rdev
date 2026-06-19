use crate::config::Args;

#[cfg(feature = "embedded-rdev-desktop")]
pub struct GpuDesktopService {
    _weylus: rdev_desktop::Weylus,
    bind_addr: std::net::SocketAddr,
}

#[cfg(feature = "embedded-rdev-desktop")]
impl GpuDesktopService {
    pub fn bind_addr(&self) -> std::net::SocketAddr {
        self.bind_addr
    }
}

#[cfg(not(feature = "embedded-rdev-desktop"))]
pub struct GpuDesktopService;

#[cfg(not(feature = "embedded-rdev-desktop"))]
impl GpuDesktopService {
    pub fn bind_addr(&self) -> std::net::SocketAddr {
        std::net::SocketAddr::from(([127, 0, 0, 1], 1701))
    }
}

#[cfg(feature = "embedded-rdev-desktop")]
pub fn start(args: &Args) -> Option<GpuDesktopService> {
    if args.no_desktop || args.no_gpu_desktop_tunnel {
        return None;
    }
    let requested_addr: std::net::SocketAddr = match args.gpu_desktop_local.parse() {
        Ok(addr) => addr,
        Err(err) => {
            tracing::warn!(
                "invalid --gpu-desktop-local {}; embedded GPU desktop disabled: {err}",
                args.gpu_desktop_local
            );
            return None;
        }
    };
    for bind_addr in candidate_bind_addrs(requested_addr) {
        let config = desktop_service_config(args, bind_addr);
        match tokio::task::block_in_place(|| rdev_desktop::start_desktop_service(config)) {
            Some(weylus) => {
                if bind_addr != requested_addr {
                    tracing::warn!(
                        "embedded GPU desktop service fell back from {requested_addr} to {bind_addr}"
                    );
                }
                tracing::info!("embedded GPU desktop service listening on {bind_addr}");
                return Some(GpuDesktopService {
                    _weylus: weylus,
                    bind_addr,
                });
            }
            None => {
                tracing::warn!("embedded GPU desktop service failed to start on {bind_addr}");
            }
        }
    }
    None
}

#[cfg(feature = "embedded-rdev-desktop")]
fn desktop_service_config(
    args: &Args,
    bind_addr: std::net::SocketAddr,
) -> rdev_desktop::DesktopServiceConfig {
    rdev_desktop::DesktopServiceConfig {
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
    }
}

#[cfg(feature = "embedded-rdev-desktop")]
fn candidate_bind_addrs(requested_addr: std::net::SocketAddr) -> Vec<std::net::SocketAddr> {
    let mut addrs = vec![requested_addr];
    for port in 1702..=1720 {
        if port != requested_addr.port() {
            addrs.push(std::net::SocketAddr::new(requested_addr.ip(), port));
        }
    }
    addrs
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

#[cfg(all(test, feature = "embedded-rdev-desktop"))]
mod tests {
    use super::*;

    #[test]
    fn candidate_bind_addrs_include_default_fallback_range() {
        let requested = "127.0.0.1:1701".parse().unwrap();
        let addrs = candidate_bind_addrs(requested);
        assert_eq!(addrs.first().copied(), Some(requested));
        assert!(addrs.contains(&"127.0.0.1:1702".parse().unwrap()));
        assert!(addrs.contains(&"127.0.0.1:1720".parse().unwrap()));
    }

    #[test]
    fn candidate_bind_addrs_preserve_requested_ip() {
        let requested = "127.0.0.2:1701".parse().unwrap();
        let addrs = candidate_bind_addrs(requested);
        assert!(addrs.into_iter().all(|addr| addr.ip() == requested.ip()));
    }
}
