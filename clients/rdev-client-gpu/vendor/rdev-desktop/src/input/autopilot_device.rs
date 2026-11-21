use autopilot::geometry::Size;
use autopilot::mouse;
use autopilot::mouse::ScrollDirection;
use autopilot::screen::size as screen_size;

use tracing::warn;

use crate::input::device::{InputDevice, InputDeviceType};
use crate::protocol::{Button, KeyboardEvent, KeyboardEventType, PointerEvent, WheelEvent};

use crate::capturable::{Capturable, Geometry};

/// Posts a key event directly through CoreGraphics. autopilot's macOS toggle
/// skips keycode `0` (`if code != 0`), which is the physical keycode of `A`,
/// so the letter could never be typed. `0` is a valid CGKeyCode and
/// `CGEventCreateKeyboardEvent` accepts it.
#[cfg(target_os = "macos")]
fn post_mac_key(keycode: u16, down: bool, flags: &[autopilot::key::Flag]) {
    use core_graphics::event::{CGEvent, CGEventFlags, CGEventTapLocation};
    use core_graphics::event_source::{CGEventSource, CGEventSourceStateID};

    let Ok(source) = CGEventSource::new(CGEventSourceStateID::HIDSystemState) else {
        warn!("Could not create CGEventSource for keyboard input");
        return;
    };
    let Ok(event) = CGEvent::new_keyboard_event(source, keycode, down) else {
        warn!("Could not create keyboard event for keycode {keycode}");
        return;
    };
    let mut event_flags = CGEventFlags::empty();
    for flag in flags {
        event_flags |= match flag {
            autopilot::key::Flag::Shift => CGEventFlags::CGEventFlagShift,
            autopilot::key::Flag::Control => CGEventFlags::CGEventFlagControl,
            autopilot::key::Flag::Alt => CGEventFlags::CGEventFlagAlternate,
            autopilot::key::Flag::Meta => CGEventFlags::CGEventFlagCommand,
            autopilot::key::Flag::Help => continue,
        };
    }
    event.set_flags(event_flags);
    // HID tap so macOS applies the modifier flags before dispatching the key.
    event.post(CGEventTapLocation::HID);
}

#[cfg(any(target_os = "macos", test))]
fn mac_keycode(code: &str) -> Option<u16> {
    let keycode = match code {
        "KeyA" => 0x00,
        "KeyS" => 0x01,
        "KeyD" => 0x02,
        "KeyF" => 0x03,
        "KeyH" => 0x04,
        "KeyG" => 0x05,
        "KeyZ" => 0x06,
        "KeyX" => 0x07,
        "KeyC" => 0x08,
        "KeyV" => 0x09,
        "KeyB" => 0x0b,
        "KeyQ" => 0x0c,
        "KeyW" => 0x0d,
        "KeyE" => 0x0e,
        "KeyR" => 0x0f,
        "KeyY" => 0x10,
        "KeyT" => 0x11,
        "KeyO" => 0x1f,
        "KeyU" => 0x20,
        "KeyI" => 0x22,
        "KeyP" => 0x23,
        "KeyL" => 0x25,
        "KeyJ" => 0x26,
        "KeyK" => 0x28,
        "KeyN" => 0x2d,
        "KeyM" => 0x2e,
        "Digit1" => 0x12,
        "Digit2" => 0x13,
        "Digit3" => 0x14,
        "Digit4" => 0x15,
        "Digit6" => 0x16,
        "Digit5" => 0x17,
        "Digit9" => 0x19,
        "Digit7" => 0x1a,
        "Digit8" => 0x1c,
        "Digit0" => 0x1d,
        "Equal" => 0x18,
        "Minus" => 0x1b,
        "BracketRight" => 0x1e,
        "BracketLeft" => 0x21,
        "Quote" => 0x27,
        "Semicolon" => 0x29,
        "Backslash" => 0x2a,
        "Comma" => 0x2b,
        "Slash" => 0x2c,
        "Period" => 0x2f,
        "Backquote" => 0x32,
        _ => return None,
    };
    Some(keycode)
}

pub struct AutoPilotDevice {
    capturable: Box<dyn Capturable>,
}

impl AutoPilotDevice {
    pub fn new(capturable: Box<dyn Capturable>) -> Self {
        Self { capturable }
    }
}

impl InputDevice for AutoPilotDevice {
    fn send_wheel_event(&mut self, event: &WheelEvent) {
        match event.dy {
            1..=i32::MAX => mouse::scroll(ScrollDirection::Up, 1),
            i32::MIN..=-1 => mouse::scroll(ScrollDirection::Down, 1),
            0 => {}
        }
    }

    fn send_pointer_event(&mut self, event: &PointerEvent) {
        if !event.is_primary {
            return;
        }
        if let Err(err) = self.capturable.before_input() {
            warn!("Failed to activate window, sending no input ({})", err);
            return;
        }
        let (x_rel, y_rel, width_rel, height_rel) = match self.capturable.geometry().unwrap() {
            Geometry::Relative(x, y, width, height) => (x, y, width, height),
            #[cfg(target_os = "windows")]
            _ => {
                warn!("Failed to get window geometry, sending no input");
                return;
            }
        };
        #[cfg(not(target_os = "macos"))]
        let Size { width, height } = screen_size();
        #[cfg(target_os = "macos")]
        let (_, _, width, height) = match crate::capturable::core_graphics::screen_coordsys() {
            Ok(bounds) => bounds,
            Err(err) => {
                warn!("Could not determine global coordinate system: {}", err);
                return;
            }
        };
        if let Err(err) = mouse::move_to(autopilot::geometry::Point::new(
            (event.x * width_rel + x_rel) * width,
            (event.y * height_rel + y_rel) * height,
        )) {
            warn!("Could not move mouse: {}", err);
        }
        match event.button {
            Button::PRIMARY => {
                mouse::toggle(mouse::Button::Left, event.buttons.contains(event.button))
            }
            Button::AUXILARY => {
                mouse::toggle(mouse::Button::Middle, event.buttons.contains(event.button))
            }
            Button::SECONDARY => {
                mouse::toggle(mouse::Button::Right, event.buttons.contains(event.button))
            }
            _ => (),
        }
    }

    fn send_keyboard_event(&mut self, event: &KeyboardEvent) {
        use autopilot::key::{Character, Code, KeyCode};

        let state = match event.event_type {
            KeyboardEventType::UP => false,
            KeyboardEventType::DOWN => true,
            // autopilot doesn't handle this, so just do nothing
            KeyboardEventType::REPEAT => return,
        };

        fn map_key(code: &str) -> Option<KeyCode> {
            match code {
                "Escape" => Some(KeyCode::Escape),
                "Enter" => Some(KeyCode::Return),
                "Backspace" => Some(KeyCode::Backspace),
                "Tab" => Some(KeyCode::Tab),
                "Space" => Some(KeyCode::Space),
                "CapsLock" => Some(KeyCode::CapsLock),
                "F1" => Some(KeyCode::F1),
                "F2" => Some(KeyCode::F2),
                "F3" => Some(KeyCode::F3),
                "F4" => Some(KeyCode::F4),
                "F5" => Some(KeyCode::F5),
                "F6" => Some(KeyCode::F6),
                "F7" => Some(KeyCode::F7),
                "F8" => Some(KeyCode::F8),
                "F9" => Some(KeyCode::F9),
                "F10" => Some(KeyCode::F10),
                "F11" => Some(KeyCode::F11),
                "F12" => Some(KeyCode::F12),
                "F13" => Some(KeyCode::F13),
                "F14" => Some(KeyCode::F14),
                "F15" => Some(KeyCode::F15),
                "F16" => Some(KeyCode::F16),
                "F17" => Some(KeyCode::F17),
                "F18" => Some(KeyCode::F18),
                "F19" => Some(KeyCode::F19),
                "F20" => Some(KeyCode::F20),
                "F21" => Some(KeyCode::F21),
                "F22" => Some(KeyCode::F22),
                "F23" => Some(KeyCode::F23),
                "F24" => Some(KeyCode::F24),
                "Home" => Some(KeyCode::Home),
                "ArrowUp" => Some(KeyCode::UpArrow),
                "PageUp" => Some(KeyCode::PageUp),
                "ArrowLeft" => Some(KeyCode::LeftArrow),
                "ArrowRight" => Some(KeyCode::RightArrow),
                "End" => Some(KeyCode::End),
                "ArrowDown" => Some(KeyCode::DownArrow),
                "PageDown" => Some(KeyCode::PageDown),
                "Delete" => Some(KeyCode::Delete),
                "ControlLeft" | "ControlRight" => Some(KeyCode::Control),
                "AltLeft" | "AltRight" => Some(KeyCode::Alt),
                "MetaLeft" | "MetaRight" => Some(KeyCode::Meta),
                "ShiftLeft" | "ShiftRight" => Some(KeyCode::Shift),
                _ => None,
            }
        }

        let key = map_key(&event.code);
        let mut flags = Vec::new();
        if event.ctrl {
            flags.push(autopilot::key::Flag::Control);
        }
        if event.alt {
            flags.push(autopilot::key::Flag::Alt);
        }
        if event.meta {
            flags.push(autopilot::key::Flag::Meta);
        }
        if event.shift {
            flags.push(autopilot::key::Flag::Shift);
        }
        #[cfg(target_os = "macos")]
        if let Some(keycode) = mac_keycode(&event.code) {
            post_mac_key(keycode, state, &flags);
            return;
        }
        match key {
            Some(key) => autopilot::key::toggle(&Code(key), state, &flags, 0),
            None => {
                for c in event.key.chars() {
                    autopilot::key::toggle(&Character(c), state, &flags, 0);
                }
            }
        }
    }

    fn set_capturable(&mut self, capturable: Box<dyn Capturable>) {
        self.capturable = capturable;
    }

    fn device_type(&self) -> InputDeviceType {
        InputDeviceType::AutoPilotDevice
    }
}

#[cfg(test)]
mod tests {
    use super::mac_keycode;

    #[test]
    fn mac_a_keycode_is_valid_zero() {
        assert_eq!(mac_keycode("KeyA"), Some(0));
    }
}
