use crate::capturable::Capturable;
use crate::protocol::{KeyboardEvent, PointerEvent, TextInputEvent, WheelEvent};

#[derive(PartialEq, Eq)]
pub enum InputDeviceType {
    AutoPilotDevice,
    UInputDevice,
    #[cfg(all(target_os = "linux", feature = "pipewire"))]
    WaylandPortalDevice,
    #[cfg(all(target_os = "linux", feature = "pipewire"))]
    WlrootsVirtualPointer,
    #[cfg(target_os = "linux")]
    XTestDevice,
    #[cfg(target_os = "windows")]
    WindowsInput,
}

impl InputDeviceType {
    pub fn label(&self) -> &'static str {
        match self {
            InputDeviceType::AutoPilotDevice => "AutoPilot",
            InputDeviceType::UInputDevice => "uinput",
            #[cfg(all(target_os = "linux", feature = "pipewire"))]
            InputDeviceType::WaylandPortalDevice => "Wayland Portal",
            #[cfg(all(target_os = "linux", feature = "pipewire"))]
            InputDeviceType::WlrootsVirtualPointer => "wlroots virtual pointer",
            #[cfg(target_os = "linux")]
            InputDeviceType::XTestDevice => "XTest",
            #[cfg(target_os = "windows")]
            InputDeviceType::WindowsInput => "Windows SendInput",
        }
    }
}

pub trait InputDevice {
    fn send_wheel_event(&mut self, event: &WheelEvent);
    fn send_pointer_event(&mut self, event: &PointerEvent);
    fn send_keyboard_event(&mut self, event: &KeyboardEvent);
    fn send_text_input_event(&mut self, _event: &TextInputEvent) {}
    fn release_keyboard(&mut self) {}
    fn drain_keyboard_status(&mut self) -> Vec<String> {
        Vec::new()
    }
    fn set_capturable(&mut self, capturable: Box<dyn Capturable>);
    fn device_type(&self) -> InputDeviceType;
}
