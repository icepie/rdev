use std::collections::HashMap;
use std::os::raw::c_int;
use std::time::Duration;

use dbus::arg::{PropMap, Variant};
use tracing::{debug, warn};

use crate::capturable::pipewire::{PipeWireCapturable, PortalRemoteDesktopSession};
use crate::capturable::Capturable;
use crate::input::device::{InputDevice, InputDeviceType};
use crate::input::uinput_keys::*;
use crate::protocol::{
    Button, KeyboardEvent, KeyboardEventType, KeyboardLocation, PointerEvent, PointerEventType,
    PointerType, WheelEvent,
};

use crate::capturable::remote_desktop_dbus::OrgFreedesktopPortalRemoteDesktop;

const DEVICE_KEYBOARD: u32 = 1;
const DEVICE_POINTER: u32 = 2;
const DEVICE_TOUCHSCREEN: u32 = 4;

const BTN_LEFT: i32 = 0x110;
const BTN_RIGHT: i32 = 0x111;
const BTN_MIDDLE: i32 = 0x112;
const BTN_SIDE: i32 = 0x113;
const BTN_EXTRA: i32 = 0x114;

const AXIS_VERTICAL: u32 = 0;
const AXIS_HORIZONTAL: u32 = 1;

#[derive(Clone, Copy)]
struct StreamInfo {
    id: u32,
    logical_size: Option<(i32, i32)>,
}

#[derive(Clone, Copy)]
struct MultiTouch {
    id: i64,
}

pub struct WaylandPortalDevice {
    capturable: Box<dyn Capturable>,
    session: PortalRemoteDesktopSession,
    stream: StreamInfo,
    touches: [Option<MultiTouch>; 5],
    warned_missing_size: bool,
}

impl WaylandPortalDevice {
    pub fn supports_capturable(capturable: &dyn Capturable) -> bool {
        capturable
            .as_any()
            .downcast_ref::<PipeWireCapturable>()
            .map(|pw| pw.portal_session().is_some() && pw.stream_id().is_some())
            .unwrap_or(false)
    }

    pub fn new(capturable: Box<dyn Capturable>) -> Result<Self, String> {
        let (session, stream) = Self::portal_state(capturable.as_ref())?;
        Ok(Self {
            capturable,
            session,
            stream,
            touches: Default::default(),
            warned_missing_size: false,
        })
    }

    fn portal_state(
        capturable: &dyn Capturable,
    ) -> Result<(PortalRemoteDesktopSession, StreamInfo), String> {
        let pw = capturable
            .as_any()
            .downcast_ref::<PipeWireCapturable>()
            .ok_or_else(|| "Capturable is not backed by PipeWire portal capture.".to_string())?;
        let session = pw.portal_session().ok_or_else(|| {
            "PipeWire capturable has no active RemoteDesktop portal session.".to_string()
        })?;
        let id = pw.stream_id().ok_or_else(|| {
            "PipeWire stream id does not fit into portal stream space.".to_string()
        })?;
        Ok((
            session,
            StreamInfo {
                id,
                logical_size: pw.logical_size(),
            },
        ))
    }

    fn refresh_portal_state(&mut self) -> Result<(), String> {
        let (session, stream) = Self::portal_state(self.capturable.as_ref())?;
        self.session = session;
        self.stream = stream;
        Ok(())
    }

    fn portal_options() -> PropMap {
        HashMap::<String, Variant<Box<dyn dbus::arg::RefArg>>>::new()
    }

    fn with_portal<R>(
        &self,
        f: impl FnOnce(
            &dbus::blocking::Proxy<&dbus::blocking::SyncConnection>,
            dbus::Path<'static>,
        ) -> Result<R, dbus::Error>,
    ) -> Result<R, dbus::Error> {
        let conn = self.session.connection();
        let proxy = conn.with_proxy(
            "org.freedesktop.portal.Desktop",
            "/org/freedesktop/portal/desktop",
            Duration::from_millis(1000),
        );
        f(&proxy, self.session.session_handle())
    }

    fn devices(&self) -> u32 {
        self.session.devices()
    }

    fn has_pointer(&self) -> bool {
        self.devices() & DEVICE_POINTER != 0
    }

    fn has_keyboard(&self) -> bool {
        self.devices() & DEVICE_KEYBOARD != 0
    }

    fn has_touch(&self) -> bool {
        self.devices() & DEVICE_TOUCHSCREEN != 0
    }

    fn absolute_coordinates(&mut self, x: f64, y: f64) -> (f64, f64) {
        if let Some((width, height)) = self.stream.logical_size {
            let max_x = (width - 1).max(0) as f64;
            let max_y = (height - 1).max(0) as f64;
            ((x * max_x).clamp(0.0, max_x), (y * max_y).clamp(0.0, max_y))
        } else {
            if !self.warned_missing_size {
                warn!(
                    "Wayland portal stream size is unavailable, falling back to normalized absolute coordinates."
                );
                self.warned_missing_size = true;
            }
            (x.clamp(0.0, 1.0), y.clamp(0.0, 1.0))
        }
    }

    fn button_code(button: Button) -> Option<i32> {
        match button {
            Button::PRIMARY => Some(BTN_LEFT),
            Button::SECONDARY => Some(BTN_RIGHT),
            Button::AUXILARY => Some(BTN_MIDDLE),
            Button::FOURTH => Some(BTN_SIDE),
            Button::FIFTH => Some(BTN_EXTRA),
            _ => None,
        }
    }

    fn keycode(code: &str, location: &KeyboardLocation) -> c_int {
        match (code, location) {
            ("Escape", _) => KEY_ESC,
            ("Digit0", KeyboardLocation::NUMPAD) => KEY_KP0,
            ("Digit1", KeyboardLocation::NUMPAD) => KEY_KP1,
            ("Digit2", KeyboardLocation::NUMPAD) => KEY_KP2,
            ("Digit3", KeyboardLocation::NUMPAD) => KEY_KP3,
            ("Digit4", KeyboardLocation::NUMPAD) => KEY_KP4,
            ("Digit5", KeyboardLocation::NUMPAD) => KEY_KP5,
            ("Digit6", KeyboardLocation::NUMPAD) => KEY_KP6,
            ("Digit7", KeyboardLocation::NUMPAD) => KEY_KP7,
            ("Digit8", KeyboardLocation::NUMPAD) => KEY_KP8,
            ("Digit9", KeyboardLocation::NUMPAD) => KEY_KP9,
            ("Minus", KeyboardLocation::NUMPAD) => KEY_KPMINUS,
            ("Equal", KeyboardLocation::NUMPAD) => KEY_KPEQUAL,
            ("Enter", KeyboardLocation::NUMPAD) => KEY_KPENTER,
            ("Digit0", _) => KEY_0,
            ("Digit1", _) => KEY_1,
            ("Digit2", _) => KEY_2,
            ("Digit3", _) => KEY_3,
            ("Digit4", _) => KEY_4,
            ("Digit5", _) => KEY_5,
            ("Digit6", _) => KEY_6,
            ("Digit7", _) => KEY_7,
            ("Digit8", _) => KEY_8,
            ("Digit9", _) => KEY_9,
            ("Minus", _) => KEY_MINUS,
            ("Equal", _) => KEY_EQUAL,
            ("Enter", _) => KEY_ENTER,
            ("Backspace", _) => KEY_BACKSPACE,
            ("Tab", _) => KEY_TAB,
            ("KeyA", _) => KEY_A,
            ("KeyB", _) => KEY_B,
            ("KeyC", _) => KEY_C,
            ("KeyD", _) => KEY_D,
            ("KeyE", _) => KEY_E,
            ("KeyF", _) => KEY_F,
            ("KeyG", _) => KEY_G,
            ("KeyH", _) => KEY_H,
            ("KeyI", _) => KEY_I,
            ("KeyJ", _) => KEY_J,
            ("KeyK", _) => KEY_K,
            ("KeyL", _) => KEY_L,
            ("KeyM", _) => KEY_M,
            ("KeyN", _) => KEY_N,
            ("KeyO", _) => KEY_O,
            ("KeyP", _) => KEY_P,
            ("KeyQ", _) => KEY_Q,
            ("KeyR", _) => KEY_R,
            ("KeyS", _) => KEY_S,
            ("KeyT", _) => KEY_T,
            ("KeyU", _) => KEY_U,
            ("KeyV", _) => KEY_V,
            ("KeyW", _) => KEY_W,
            ("KeyX", _) => KEY_X,
            ("KeyY", _) => KEY_Y,
            ("KeyZ", _) => KEY_Z,
            ("BracketLeft", _) => KEY_LEFTBRACE,
            ("BracketRight", _) => KEY_RIGHTBRACE,
            ("Semicolon", _) => KEY_SEMICOLON,
            ("Quote", _) => KEY_APOSTROPHE,
            ("Backquote", _) => KEY_GRAVE,
            ("Backslash", _) => KEY_BACKSLASH,
            ("Comma", _) => KEY_COMMA,
            ("Period", _) => KEY_DOT,
            ("Slash", _) => KEY_SLASH,
            ("Space", _) => KEY_SPACE,
            ("CapsLock", _) => KEY_CAPSLOCK,
            ("NumpadMultiply", _) => KEY_KPASTERISK,
            ("F1", _) => KEY_F1,
            ("F2", _) => KEY_F2,
            ("F3", _) => KEY_F3,
            ("F4", _) => KEY_F4,
            ("F5", _) => KEY_F5,
            ("F6", _) => KEY_F6,
            ("F7", _) => KEY_F7,
            ("F8", _) => KEY_F8,
            ("F9", _) => KEY_F9,
            ("F10", _) => KEY_F10,
            ("F11", _) => KEY_F11,
            ("F12", _) => KEY_F12,
            ("F13", _) => KEY_F13,
            ("F14", _) => KEY_F14,
            ("F15", _) => KEY_F15,
            ("F16", _) => KEY_F16,
            ("F17", _) => KEY_F17,
            ("F18", _) => KEY_F18,
            ("F19", _) => KEY_F19,
            ("F20", _) => KEY_F20,
            ("F21", _) => KEY_F21,
            ("F22", _) => KEY_F22,
            ("F23", _) => KEY_F23,
            ("F24", _) => KEY_F24,
            ("NumLock", _) => KEY_NUMLOCK,
            ("ScrollLock", _) => KEY_SCROLLLOCK,
            ("Numpad0", _) => KEY_KP0,
            ("Numpad1", _) => KEY_KP1,
            ("Numpad2", _) => KEY_KP2,
            ("Numpad3", _) => KEY_KP3,
            ("Numpad4", _) => KEY_KP4,
            ("Numpad5", _) => KEY_KP5,
            ("Numpad6", _) => KEY_KP6,
            ("Numpad7", _) => KEY_KP7,
            ("Numpad8", _) => KEY_KP8,
            ("Numpad9", _) => KEY_KP9,
            ("NumpadSubtract", _) => KEY_KPMINUS,
            ("NumpadAdd", _) => KEY_KPPLUS,
            ("IntlBackslash", _) => KEY_102ND,
            ("IntlRo", _) => KEY_RO,
            ("NumpadEnter", _) => KEY_KPENTER,
            ("NumpadDivide", _) => KEY_KPSLASH,
            ("NumpadEqual", _) => KEY_KPEQUAL,
            ("NumpadComma", _) => KEY_KPCOMMA,
            ("NumpadParenLeft", _) => KEY_KPLEFTPAREN,
            ("NumpadParenRight", _) => KEY_KPRIGHTPAREN,
            ("KanaMode", _) => KEY_KATAKANA,
            ("PrintScreen", _) => KEY_SYSRQ,
            ("Home", _) => KEY_HOME,
            ("ArrowUp", _) => KEY_UP,
            ("PageUp", _) => KEY_PAGEUP,
            ("ArrowLeft", _) => KEY_LEFT,
            ("ArrowRight", _) => KEY_RIGHT,
            ("End", _) => KEY_END,
            ("ArrowDown", _) => KEY_DOWN,
            ("PageDown", _) => KEY_PAGEDOWN,
            ("Insert", _) => KEY_INSERT,
            ("Delete", _) => KEY_DELETE,
            ("VolumeMute", _) | ("AudioVolumeMute", _) => KEY_MUTE,
            ("VolumeDown", _) | ("AudioVolumeDown", _) => KEY_VOLUMEDOWN,
            ("VolumeUp", _) | ("AudioVolumeUp", _) => KEY_VOLUMEUP,
            ("Pause", _) => KEY_PAUSE,
            ("Lang1", _) => KEY_HANGUEL,
            ("Lang2", _) => KEY_HANJA,
            ("IntlYen", _) => KEY_YEN,
            ("OSLeft", _) => KEY_LEFTMETA,
            ("OSRight", _) => KEY_RIGHTMETA,
            ("ContextMenu", _) => KEY_MENU,
            ("Cancel", _) => KEY_CANCEL,
            ("Again", _) => KEY_AGAIN,
            ("Props", _) => KEY_PROPS,
            ("Undo", _) => KEY_UNDO,
            ("Copy", _) => KEY_COPY,
            ("Open", _) => KEY_OPEN,
            ("Paste", _) => KEY_PASTE,
            ("Find", _) => KEY_FIND,
            ("Cut", _) => KEY_CUT,
            ("Help", _) => KEY_HELP,
            ("LaunchMail", _) => KEY_MAIL,
            ("Eject", _) => KEY_EJECTCD,
            ("MediaTrackNext", _) => KEY_NEXTSONG,
            ("MediaPlayPause", _) => KEY_PLAYPAUSE,
            ("MediaTrackPrevious", _) => KEY_PREVIOUSSONG,
            ("MediaStop", _) => KEY_STOPCD,
            ("MediaSelect", _) | ("LaunchMediaPlayer", _) => KEY_MEDIA,
            ("Power", _) => KEY_POWER,
            ("Sleep", _) => KEY_SLEEP,
            ("WakeUp", _) => KEY_WAKEUP,
            ("ControlLeft", _) => KEY_LEFTCTRL,
            ("ControlRight", _) => KEY_RIGHTCTRL,
            ("AltLeft", _) => KEY_LEFTALT,
            ("AltRight", _) => KEY_RIGHTALT,
            ("MetaLeft", _) => KEY_LEFTMETA,
            ("MetaRight", _) => KEY_RIGHTMETA,
            ("ShiftLeft", _) => KEY_LEFTSHIFT,
            ("ShiftRight", _) => KEY_RIGHTSHIFT,
            _ => KEY_UNKNOWN,
        }
    }

    fn pointer_button_state(event_type: &PointerEventType) -> Option<u32> {
        match event_type {
            PointerEventType::DOWN => Some(1),
            PointerEventType::UP
            | PointerEventType::CANCEL
            | PointerEventType::LEAVE
            | PointerEventType::OUT => Some(0),
            _ => None,
        }
    }

    fn find_slot(&self, id: i64) -> Option<usize> {
        self.touches
            .iter()
            .enumerate()
            .find_map(|(slot, touch)| match touch {
                Some(touch) if touch.id == id => Some(slot),
                _ => None,
            })
    }

    fn touch_slot_for_event(&mut self, event: &PointerEvent) -> Option<u32> {
        if let Some(slot) = self.find_slot(event.pointer_id) {
            return Some(slot as u32);
        }
        if matches!(
            event.event_type,
            PointerEventType::DOWN
                | PointerEventType::MOVE
                | PointerEventType::OVER
                | PointerEventType::ENTER
        ) {
            if let Some(slot) = self
                .touches
                .iter()
                .enumerate()
                .find_map(|(slot, touch)| touch.is_none().then_some(slot))
            {
                self.touches[slot] = Some(MultiTouch {
                    id: event.pointer_id,
                });
                return Some(slot as u32);
            }
        }
        None
    }

    fn send_pointer_absolute(&mut self, x: f64, y: f64) {
        if !self.has_pointer() {
            return;
        }
        let (x, y) = self.absolute_coordinates(x, y);
        if let Err(err) = self.with_portal(|portal, session| {
            portal.notify_pointer_motion_absolute(
                session,
                Self::portal_options(),
                self.stream.id,
                x,
                y,
            )
        }) {
            warn!("Wayland portal pointer motion failed: {}", err);
        }
    }

    fn send_pointer_button(&self, button: i32, state: u32) {
        if !self.has_pointer() {
            return;
        }
        if let Err(err) = self.with_portal(|portal, session| {
            portal.notify_pointer_button(session, Self::portal_options(), button, state)
        }) {
            warn!("Wayland portal pointer button failed: {}", err);
        }
    }
}

impl InputDevice for WaylandPortalDevice {
    fn send_wheel_event(&mut self, event: &WheelEvent) {
        if let Err(err) = self.capturable.before_input() {
            warn!(
                "Failed to activate capturable before portal wheel event ({})",
                err
            );
            return;
        }
        if !self.has_pointer() {
            return;
        }
        if let Err(err) = self.with_portal(|portal, session| {
            if event.dx != 0 || event.dy != 0 {
                portal.notify_pointer_axis(
                    session.clone(),
                    Self::portal_options(),
                    event.dx as f64,
                    event.dy as f64,
                )?;
            }
            if event.dy != 0 {
                portal.notify_pointer_axis_discrete(
                    session.clone(),
                    Self::portal_options(),
                    AXIS_VERTICAL,
                    event.dy.signum(),
                )?;
            }
            if event.dx != 0 {
                portal.notify_pointer_axis_discrete(
                    session,
                    Self::portal_options(),
                    AXIS_HORIZONTAL,
                    event.dx.signum(),
                )?;
            }
            Ok(())
        }) {
            warn!("Wayland portal wheel event failed: {}", err);
        }
    }

    fn send_pointer_event(&mut self, event: &PointerEvent) {
        if !event.is_primary
            && !matches!(event.pointer_type, PointerType::Mouse | PointerType::Touch)
        {
            return;
        }
        if let Err(err) = self.capturable.before_input() {
            warn!(
                "Failed to activate capturable before portal pointer event ({})",
                err
            );
            return;
        }

        match event.pointer_type {
            PointerType::Touch => {
                if !self.has_touch() {
                    if event.is_primary {
                        self.send_pointer_absolute(event.x, event.y);
                    }
                    return;
                }
                let (x, y) = self.absolute_coordinates(event.x, event.y);
                let Some(slot) = self.touch_slot_for_event(event) else {
                    return;
                };
                let result = self.with_portal(|portal, session| match event.event_type {
                    PointerEventType::DOWN => portal.notify_touch_down(
                        session,
                        Self::portal_options(),
                        self.stream.id,
                        slot,
                        x,
                        y,
                    ),
                    PointerEventType::MOVE | PointerEventType::OVER | PointerEventType::ENTER => {
                        portal.notify_touch_motion(
                            session,
                            Self::portal_options(),
                            self.stream.id,
                            slot,
                            x,
                            y,
                        )
                    }
                    PointerEventType::UP
                    | PointerEventType::CANCEL
                    | PointerEventType::LEAVE
                    | PointerEventType::OUT => {
                        portal.notify_touch_up(session, Self::portal_options(), slot)
                    }
                });
                if let Err(err) = result {
                    warn!("Wayland portal touch event failed: {}", err);
                } else if matches!(
                    event.event_type,
                    PointerEventType::UP
                        | PointerEventType::CANCEL
                        | PointerEventType::LEAVE
                        | PointerEventType::OUT
                ) {
                    if let Some(slot) = self.find_slot(event.pointer_id) {
                        self.touches[slot] = None;
                    }
                }
            }
            PointerType::Mouse | PointerType::Pen | PointerType::Unknown => {
                self.send_pointer_absolute(event.x, event.y);
                if let Some(state) = Self::pointer_button_state(&event.event_type) {
                    if let Some(button) = Self::button_code(event.button) {
                        self.send_pointer_button(button, state);
                    }
                }
            }
        }
    }

    fn send_keyboard_event(&mut self, event: &KeyboardEvent) {
        if let Err(err) = self.capturable.before_input() {
            warn!(
                "Failed to activate capturable before portal keyboard event ({})",
                err
            );
            return;
        }
        if !self.has_keyboard() {
            return;
        }
        let keycode = Self::keycode(&event.code, &event.location);
        if keycode == KEY_UNKNOWN {
            debug!(
                "Skipping unmapped Wayland portal key event: code={} key={}",
                event.code, event.key
            );
            return;
        }
        let state = match event.event_type {
            KeyboardEventType::UP => 0,
            KeyboardEventType::DOWN | KeyboardEventType::REPEAT => 1,
        };
        if let Err(err) = self.with_portal(|portal, session| {
            portal.notify_keyboard_keycode(session, Self::portal_options(), keycode, state)
        }) {
            warn!("Wayland portal keyboard event failed: {}", err);
        }
    }

    fn set_capturable(&mut self, capturable: Box<dyn Capturable>) {
        self.capturable = capturable;
        if let Err(err) = self.refresh_portal_state() {
            warn!(
                "Failed to refresh Wayland portal session after capturable switch: {}",
                err
            );
        }
    }

    fn device_type(&self) -> InputDeviceType {
        InputDeviceType::WaylandPortalDevice
    }
}
