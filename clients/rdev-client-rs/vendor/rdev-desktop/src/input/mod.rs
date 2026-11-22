#[cfg(target_os = "macos")]
pub mod autopilot_device;
pub mod device;

#[cfg(target_os = "windows")]
pub mod autopilot_device_win;
#[cfg(target_os = "linux")]
pub mod uinput_device;
#[cfg(target_os = "linux")]
#[allow(dead_code)]
pub mod uinput_keys;
#[cfg(all(target_os = "linux", feature = "pipewire"))]
pub mod wayland_portal_device;
#[cfg(all(target_os = "linux", feature = "pipewire"))]
pub mod wlroots_virtual_pointer_device;
#[cfg(target_os = "linux")]
#[allow(dead_code)]
pub mod x11_keys;
#[cfg(target_os = "linux")]
pub mod xtest_device;
