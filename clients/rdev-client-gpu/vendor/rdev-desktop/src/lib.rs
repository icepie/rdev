pub mod capturable;
pub mod cerror;
pub mod config;
mod gui;
pub mod input;
pub mod log;
pub mod protocol;
pub mod video;
pub mod web;
pub mod websocket;
pub mod weylus;

use std::net::{IpAddr, SocketAddr};
use std::path::PathBuf;

pub use config::Config;
pub use weylus::Weylus;

#[derive(Debug, Clone)]
pub struct DesktopServiceConfig {
    pub bind_addr: SocketAddr,
    pub access_code: Option<String>,
    pub custom_index_html: Option<PathBuf>,
    pub custom_access_html: Option<PathBuf>,
    pub custom_style_css: Option<PathBuf>,
    pub custom_lib_js: Option<PathBuf>,
    pub no_gui: bool,
    #[cfg(target_os = "linux")]
    pub wayland_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_device: Option<String>,
    #[cfg(target_os = "linux")]
    pub nvfbc_support: bool,
    #[cfg(target_os = "linux")]
    pub try_vaapi: bool,
    #[cfg(any(target_os = "linux", target_os = "windows"))]
    pub try_nvenc: bool,
    #[cfg(target_os = "linux")]
    pub try_vulkan_video: bool,
    #[cfg(target_os = "macos")]
    pub try_videotoolbox: bool,
    #[cfg(target_os = "windows")]
    pub try_mediafoundation: bool,
    #[cfg(target_os = "windows")]
    pub windows_capture_source: String,
}

impl Default for DesktopServiceConfig {
    fn default() -> Self {
        Self {
            bind_addr: SocketAddr::from(([127, 0, 0, 1], 1701)),
            access_code: None,
            custom_index_html: None,
            custom_access_html: None,
            custom_style_css: None,
            custom_lib_js: None,
            no_gui: true,
            #[cfg(target_os = "linux")]
            wayland_support: false,
            #[cfg(target_os = "linux")]
            kms_support: false,
            #[cfg(target_os = "linux")]
            kms_device: None,
            #[cfg(target_os = "linux")]
            nvfbc_support: false,
            #[cfg(target_os = "linux")]
            try_vaapi: false,
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            try_nvenc: false,
            #[cfg(target_os = "linux")]
            try_vulkan_video: false,
            #[cfg(target_os = "macos")]
            try_videotoolbox: false,
            #[cfg(target_os = "windows")]
            try_mediafoundation: false,
            #[cfg(target_os = "windows")]
            windows_capture_source: "auto".to_string(),
        }
    }
}

pub fn start_desktop_service(config: DesktopServiceConfig) -> Option<Weylus> {
    #[cfg(target_os = "linux")]
    capturable::x11::x11_init();

    let mut weylus_config = Config::default();
    weylus_config.bind_address = config.bind_addr.ip();
    weylus_config.web_port = config.bind_addr.port();
    weylus_config.access_code = config.access_code;
    weylus_config.no_gui = config.no_gui;
    weylus_config.custom_index_html = config.custom_index_html;
    weylus_config.custom_access_html = config.custom_access_html;
    weylus_config.custom_style_css = config.custom_style_css;
    weylus_config.custom_lib_js = config.custom_lib_js;
    #[cfg(target_os = "linux")]
    {
        weylus_config.wayland_support = config.wayland_support;
        weylus_config.kms_support = config.kms_support;
        weylus_config.kms_device = config.kms_device;
        weylus_config.nvfbc_support = config.nvfbc_support;
        weylus_config.try_vaapi = config.try_vaapi;
        weylus_config.try_nvenc = config.try_nvenc;
        weylus_config.try_vulkan_video = config.try_vulkan_video;
    }
    #[cfg(target_os = "macos")]
    {
        weylus_config.try_videotoolbox = config.try_videotoolbox;
    }
    #[cfg(target_os = "windows")]
    {
        weylus_config.try_nvenc = config.try_nvenc;
        weylus_config.try_mediafoundation = config.try_mediafoundation;
        weylus_config.windows_capture_source = config.windows_capture_source;
    }

    let mut weylus = Weylus::new();
    if weylus.start(&weylus_config, |_| {}) {
        Some(weylus)
    } else {
        None
    }
}

pub fn socket_addr_from_ip_port(ip: IpAddr, port: u16) -> SocketAddr {
    SocketAddr::new(ip, port)
}
