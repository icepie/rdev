use std::net::{IpAddr, Ipv4Addr};
use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct Config {
    pub access_code: Option<String>,
    pub bind_address: IpAddr,
    pub web_port: u16,
    pub auto_start: bool,
    pub no_gui: bool,
    pub service: bool,
    pub headless: bool,
    pub agent_config: Option<PathBuf>,
    pub agent_port: u16,
    pub agent_server: Option<String>,
    pub agent_name: Option<String>,
    pub install_service: bool,
    pub uninstall_service: bool,
    pub start_service: bool,
    pub stop_service: bool,
    pub restart_service: bool,
    pub windows_service: bool,
    pub session_agent: bool,
    pub print_index_html: bool,
    pub print_access_html: bool,
    pub print_style_css: bool,
    pub print_lib_js: bool,
    pub custom_index_html: Option<PathBuf>,
    pub custom_access_html: Option<PathBuf>,
    pub custom_style_css: Option<PathBuf>,
    pub custom_lib_js: Option<PathBuf>,
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
    #[cfg(target_os = "linux")]
    pub nvfbc_support: bool,
    #[cfg(target_os = "linux")]
    pub wayland_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_device: Option<String>,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            access_code: None,
            bind_address: IpAddr::V4(Ipv4Addr::LOCALHOST),
            web_port: 1701,
            auto_start: true,
            no_gui: true,
            service: false,
            headless: true,
            agent_config: None,
            agent_port: 18765,
            agent_server: None,
            agent_name: None,
            install_service: false,
            uninstall_service: false,
            start_service: false,
            stop_service: false,
            restart_service: false,
            windows_service: false,
            session_agent: false,
            print_index_html: false,
            print_access_html: false,
            print_style_css: false,
            print_lib_js: false,
            custom_index_html: None,
            custom_access_html: None,
            custom_style_css: None,
            custom_lib_js: None,
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
            #[cfg(target_os = "linux")]
            nvfbc_support: false,
            #[cfg(target_os = "linux")]
            wayland_support: false,
            #[cfg(target_os = "linux")]
            kms_support: false,
            #[cfg(target_os = "linux")]
            kms_device: None,
        }
    }
}
