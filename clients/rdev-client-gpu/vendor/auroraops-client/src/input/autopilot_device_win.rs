use std::collections::HashSet;
use std::iter;
use std::os::windows::ffi::OsStrExt;
use std::ptr;

use winapi::shared::basetsd::{UINT32, ULONG_PTR};
use winapi::shared::minwindef::{BOOL, DWORD, FALSE, LPARAM, TRUE, ULONG, WORD};
use winapi::shared::windef::{HDESK, HWND, POINT};
use winapi::um::handleapi::CloseHandle;
use winapi::um::libloaderapi::{GetModuleHandleW, GetProcAddress};
use winapi::um::processthreadsapi::{
    CreateProcessW, GetCurrentProcessId, GetCurrentThreadId, ProcessIdToSessionId,
    PROCESS_INFORMATION, STARTUPINFOW,
};
use winapi::um::winuser::*;

use serde::{Deserialize, Serialize};
use tracing::warn;

use crate::input::device::{InputDevice, InputDeviceType};
use crate::protocol::{
    Button, KeyboardEvent, PointerEvent, PointerEventType, PointerType, TextInputEvent, WheelEvent,
};

use crate::capturable::{Capturable, Geometry};

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowsDesktopStatus {
    pub input_desktop: String,
    pub is_winlogon: bool,
    pub backend: String,
}

pub struct WindowsInput {
    capturable: Box<dyn Capturable>,
    synthetic_pointer: Option<SyntheticPointerRuntime>,
    multitouch_map: std::collections::HashMap<i64, POINTER_TYPE_INFO>,
    keyboard: KeyboardInputWorker,
    pending_keyboard_status: Vec<String>,
}

impl WindowsInput {
    pub fn new(capturable: Box<dyn Capturable>) -> Self {
        let mut pending_keyboard_status = Vec::new();
        let synthetic_pointer = match SyntheticPointerRuntime::new() {
            Some(runtime) => Some(runtime),
            None => {
                pending_keyboard_status.push(
                    "Windows synthetic pointer API unavailable; pen/touch uses mouse fallback"
                        .to_string(),
                );
                None
            }
        };
        Self {
            capturable: capturable.clone(),
            synthetic_pointer,
            multitouch_map: std::collections::HashMap::new(),
            keyboard: KeyboardInputWorker::new(),
            pending_keyboard_status,
        }
    }
}

struct SyntheticPointerApi {
    initialize_touch_injection: unsafe extern "system" fn(UINT32, DWORD) -> BOOL,
    create_synthetic_pointer_device: unsafe extern "system" fn(
        POINTER_INPUT_TYPE,
        ULONG,
        POINTER_FEEDBACK_MODE,
    ) -> HSYNTHETICPOINTERDEVICE,
    inject_synthetic_pointer_input: unsafe extern "system" fn(
        HSYNTHETICPOINTERDEVICE,
        *const POINTER_TYPE_INFO,
        UINT32,
    ) -> BOOL,
    destroy_synthetic_pointer_device: unsafe extern "system" fn(HSYNTHETICPOINTERDEVICE),
}

struct SyntheticPointerRuntime {
    api: SyntheticPointerApi,
    pointer_device_handle: HSYNTHETICPOINTERDEVICE,
    touch_device_handle: HSYNTHETICPOINTERDEVICE,
}

impl SyntheticPointerRuntime {
    fn new() -> Option<Self> {
        let api = SyntheticPointerApi::load()?;
        unsafe {
            (api.initialize_touch_injection)(5, TOUCH_FEEDBACK_DEFAULT);
            Some(Self {
                pointer_device_handle: (api.create_synthetic_pointer_device)(PT_PEN, 1, 1),
                touch_device_handle: (api.create_synthetic_pointer_device)(PT_TOUCH, 5, 1),
                api,
            })
        }
    }
}

impl Drop for SyntheticPointerRuntime {
    fn drop(&mut self) {
        unsafe {
            if !self.pointer_device_handle.is_null() {
                (self.api.destroy_synthetic_pointer_device)(self.pointer_device_handle);
            }
            if !self.touch_device_handle.is_null() {
                (self.api.destroy_synthetic_pointer_device)(self.touch_device_handle);
            }
        }
    }
}

impl SyntheticPointerApi {
    fn load() -> Option<Self> {
        unsafe {
            let user32 = GetModuleHandleW(wide("user32.dll").as_ptr());
            if user32.is_null() {
                return None;
            }
            Some(Self {
                initialize_touch_injection: load_user32_proc(
                    user32,
                    b"InitializeTouchInjection\0",
                )?,
                create_synthetic_pointer_device: load_user32_proc(
                    user32,
                    b"CreateSyntheticPointerDevice\0",
                )?,
                inject_synthetic_pointer_input: load_user32_proc(
                    user32,
                    b"InjectSyntheticPointerInput\0",
                )?,
                destroy_synthetic_pointer_device: load_user32_proc(
                    user32,
                    b"DestroySyntheticPointerDevice\0",
                )?,
            })
        }
    }
}

unsafe fn load_user32_proc<T>(
    module: winapi::shared::minwindef::HMODULE,
    name: &[u8],
) -> Option<T> {
    let proc = GetProcAddress(module, name.as_ptr().cast());
    if proc.is_null() {
        return None;
    }
    Some(std::mem::transmute_copy(&proc))
}

struct KeyboardInputWorker {
    pressed_keys: HashSet<WORD>,
    event_count: u64,
}

impl KeyboardInputWorker {
    fn new() -> Self {
        Self {
            pressed_keys: HashSet::new(),
            event_count: 0,
        }
    }

    fn send_keyboard_event(&mut self, event: &KeyboardEvent) -> Vec<String> {
        let is_up = matches!(event.event_type, crate::protocol::KeyboardEventType::UP);
        let is_repeat = matches!(event.event_type, crate::protocol::KeyboardEventType::REPEAT);
        let mut ok = true;

        let mut statuses = Vec::new();
        self.event_count += 1;
        let event_label = format!(
            "#{} {} code={} key={}",
            self.event_count,
            match event.event_type {
                crate::protocol::KeyboardEventType::DOWN => "down",
                crate::protocol::KeyboardEventType::UP => "up",
                crate::protocol::KeyboardEventType::REPEAT => "repeat",
            },
            event.code,
            event.key
        );
        statuses.push(format!("keyboard target: {}", foreground_window_label()));

        if let Some(vk) = map_keyboard_code(&event.code, &event.location) {
            self.sync_event_modifiers(event, Some(vk));
            ok = self.send_key(vk, !is_up, is_repeat);
            statuses.push(Self::keyboard_status(&event_label, ok, "scancode"));
            return statuses;
        }

        if !is_up && !event.ctrl && !event.alt && !event.meta && should_send_unicode(&event.key) {
            for ch in event.key.chars().filter(|ch| !ch.is_control()) {
                ok &= self.tap_unicode_key(ch);
            }
            statuses.push(Self::keyboard_status(&event_label, ok, "unicode"));
            return statuses;
        }
        self.sync_event_modifiers(event, None);
        statuses.push(Self::keyboard_status(&event_label, true, "modifier"));
        warn!(
            "Skipping unmapped Windows keyboard event: code={} key={}",
            event.code, event.key
        );
        statuses
    }

    fn send_key(&mut self, vk: WORD, down: bool, repeat: bool) -> bool {
        if down && !repeat && !self.pressed_keys.insert(vk) {
            return true;
        }
        if down && repeat {
            self.pressed_keys.insert(vk);
        }
        if !down {
            self.pressed_keys.remove(&vk);
        }
        self.send_vk(vk, down)
    }

    fn tap_unicode_key(&mut self, ch: char) -> bool {
        let mut buf = [0u16; 2];
        let mut ok = true;
        for unit in ch.encode_utf16(&mut buf) {
            ok &= self.send_unicode(*unit, true);
            ok &= self.send_unicode(*unit, false);
        }
        ok
    }

    fn sync_modifier(&mut self, vk: WORD, down: bool) {
        if down && !self.pressed_keys.contains(&vk) {
            self.send_key(vk, true, false);
        } else if !down && self.pressed_keys.contains(&vk) {
            self.send_key(vk, false, false);
        }
    }

    fn sync_event_modifiers(&mut self, event: &KeyboardEvent, exclude_vk: Option<WORD>) {
        self.sync_modifier_unless(VK_CONTROL as WORD, event.ctrl, exclude_vk);
        self.sync_modifier_unless(VK_MENU as WORD, event.alt, exclude_vk);
        self.sync_modifier_unless(VK_SHIFT as WORD, event.shift, exclude_vk);
        self.sync_modifier_unless(VK_LWIN as WORD, event.meta, exclude_vk);
    }

    fn sync_modifier_unless(&mut self, vk: WORD, down: bool, exclude_vk: Option<WORD>) {
        if exclude_vk.is_some_and(|exclude| modifier_family_matches(exclude, vk)) {
            return;
        }
        self.sync_modifier(vk, down);
    }

    fn send_vk(&mut self, vk: WORD, down: bool) -> bool {
        send_scancode(vk, down)
    }

    fn send_unicode(&mut self, scan: WORD, down: bool) -> bool {
        send_unicode(scan, down)
    }

    fn keyboard_status(event_label: &str, ok: bool, mode: &str) -> String {
        let state = if ok {
            "sendinput ok"
        } else {
            "sendinput failed"
        };
        format!("{state} {mode} {event_label}")
    }

    fn release_all(&mut self) -> usize {
        let keys: Vec<WORD> = self.pressed_keys.drain().collect();
        for vk in &keys {
            send_vk(*vk, false);
        }
        keys.len()
    }
}

impl Drop for KeyboardInputWorker {
    fn drop(&mut self) {
        self.release_all();
    }
}

impl InputDevice for WindowsInput {
    fn send_wheel_event(&mut self, event: &WheelEvent) {
        let desktop = attach_thread_to_input_desktop();
        self.pending_keyboard_status
            .push(desktop.status_line("wheel attach desktop"));
        if let Err(err) = self.capturable.before_input() {
            warn!("Failed to activate target before wheel input ({})", err);
            self.pending_keyboard_status
                .push(format!("wheel before_input failed: {err}"));
            return;
        }
        let (ok, fallback) = dispatch_mouse_input(MOUSEEVENTF_WHEEL, event.dy as DWORD);
        self.pending_keyboard_status.push(mouse_dispatch_status(
            "wheel",
            MOUSEEVENTF_WHEEL,
            event.dy as DWORD,
            ok,
            fallback,
        ));
    }

    fn send_pointer_event(&mut self, event: &PointerEvent) {
        let desktop = attach_thread_to_input_desktop();
        self.pending_keyboard_status
            .push(desktop.status_line("pointer attach desktop"));
        if let Err(err) = self.capturable.before_input() {
            warn!("Failed to activate window, sending no input ({})", err);
            self.pending_keyboard_status
                .push(format!("pointer before_input failed: {err}"));
            return;
        }
        let Geometry::VirtualScreen(offset_x, offset_y, width, height, left, top) =
            self.capturable.geometry().unwrap()
        else {
            unreachable!()
        };

        let (x, y) = (
            (event.x * width as f64) as i32 + offset_x,
            (event.y * height as f64) as i32 + offset_y,
        );
        let mut pointer_flags = match event.event_type {
            PointerEventType::DOWN => {
                POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT | POINTER_FLAG_DOWN
            }
            PointerEventType::MOVE | PointerEventType::OVER | PointerEventType::ENTER => {
                POINTER_FLAG_INRANGE | POINTER_FLAG_UPDATE
            }
            PointerEventType::UP => POINTER_FLAG_UP,
            PointerEventType::CANCEL | PointerEventType::LEAVE | PointerEventType::OUT => {
                POINTER_FLAG_INRANGE | POINTER_FLAG_UPDATE | POINTER_FLAG_CANCELED
            }
        };
        let button_change_type = match event.buttons {
            Button::PRIMARY => {
                pointer_flags |= POINTER_FLAG_INCONTACT;
                POINTER_CHANGE_FIRSTBUTTON_DOWN
            }
            Button::SECONDARY => POINTER_CHANGE_SECONDBUTTON_DOWN,
            Button::AUXILARY => POINTER_CHANGE_THIRDBUTTON_DOWN,
            Button::NONE => POINTER_CHANGE_NONE,
            _ => POINTER_CHANGE_NONE,
        };
        if event.is_primary {
            pointer_flags |= POINTER_FLAG_PRIMARY;
        }
        match event.pointer_type {
            PointerType::Pen => {
                let Some(runtime) = &self.synthetic_pointer else {
                    self.pending_keyboard_status.push(
                        "pen fallback to mouse pointer; synthetic pointer API unavailable"
                            .to_string(),
                    );
                    let screen_x = (event.x * width as f64) as i32 + left;
                    let screen_y = (event.y * height as f64) as i32 + top;
                    self.pending_keyboard_status
                        .extend(send_mouse_pointer_event(event, screen_x, screen_y));
                    return;
                };
                unsafe {
                    let mut pointer_type_info = POINTER_TYPE_INFO {
                        type_: PT_PEN,
                        u: std::mem::zeroed(),
                    };
                    *pointer_type_info.u.penInfo_mut() = POINTER_PEN_INFO {
                        pointerInfo: POINTER_INFO {
                            pointerType: PT_PEN,
                            pointerId: event.pointer_id as u32,
                            frameId: 0,
                            pointerFlags: pointer_flags,
                            sourceDevice: 0 as *mut winapi::ctypes::c_void, //maybe use syntheticPointerDeviceHandle here but works with 0
                            hwndTarget: 0 as HWND,
                            ptPixelLocation: POINT { x: x, y: y },
                            ptHimetricLocation: POINT { x: 0, y: 0 },
                            ptPixelLocationRaw: POINT { x: x, y: y },
                            ptHimetricLocationRaw: POINT { x: 0, y: 0 },
                            dwTime: 0,
                            historyCount: 1,
                            InputData: 0,
                            dwKeyStates: 0,
                            PerformanceCount: 0,
                            ButtonChangeType: button_change_type,
                        },
                        penFlags: PEN_FLAG_NONE,
                        penMask: PEN_MASK_PRESSURE
                            | PEN_MASK_ROTATION
                            | PEN_MASK_TILT_X
                            | PEN_MASK_TILT_Y,
                        pressure: (event.pressure * 1024f64) as u32,
                        rotation: event.twist as u32,
                        tiltX: event.tilt_x,
                        tiltY: event.tilt_y,
                    };
                    (runtime.api.inject_synthetic_pointer_input)(
                        runtime.pointer_device_handle,
                        &pointer_type_info,
                        1,
                    );
                }
            }
            PointerType::Touch => {
                let Some(runtime) = &self.synthetic_pointer else {
                    self.pending_keyboard_status.push(
                        "touch fallback to mouse pointer; synthetic pointer API unavailable"
                            .to_string(),
                    );
                    let screen_x = (event.x * width as f64) as i32 + left;
                    let screen_y = (event.y * height as f64) as i32 + top;
                    self.pending_keyboard_status
                        .extend(send_mouse_pointer_event(event, screen_x, screen_y));
                    return;
                };
                unsafe {
                    let mut pointer_type_info = POINTER_TYPE_INFO {
                        type_: PT_TOUCH,
                        u: std::mem::zeroed(),
                    };

                    let mut pointer_touch_info: POINTER_TOUCH_INFO = std::mem::zeroed();
                    pointer_touch_info.pointerInfo = std::mem::zeroed();
                    pointer_touch_info.pointerInfo.pointerType = PT_TOUCH;
                    pointer_touch_info.pointerInfo.pointerFlags = pointer_flags;
                    pointer_touch_info.pointerInfo.pointerId = event.pointer_id as u32; //event.pointer_id as u32; Using the actual pointer id causes errors in the touch injection
                    pointer_touch_info.pointerInfo.ptPixelLocation = POINT { x, y };
                    pointer_touch_info.touchFlags = TOUCH_FLAG_NONE;
                    pointer_touch_info.touchMask = TOUCH_MASK_PRESSURE;
                    pointer_touch_info.pressure = (event.pressure * 1024f64) as u32;

                    pointer_touch_info.pointerInfo.ButtonChangeType = button_change_type;

                    *pointer_type_info.u.touchInfo_mut() = pointer_touch_info;
                    self.multitouch_map
                        .insert(event.pointer_id, pointer_type_info);
                    let len = self.multitouch_map.len();

                    let mut pointer_type_info_vec: Vec<POINTER_TYPE_INFO> = Vec::new();
                    for (_i, info) in self.multitouch_map.iter().enumerate() {
                        pointer_type_info_vec.push(*info.1);
                    }
                    let b: Box<[POINTER_TYPE_INFO]> = pointer_type_info_vec.into_boxed_slice();
                    let m: *mut POINTER_TYPE_INFO = Box::into_raw(b) as _;

                    (runtime.api.inject_synthetic_pointer_input)(
                        runtime.touch_device_handle,
                        m,
                        len as u32,
                    );

                    match event.event_type {
                        PointerEventType::DOWN
                        | PointerEventType::MOVE
                        | PointerEventType::OVER
                        | PointerEventType::ENTER => {}

                        PointerEventType::UP
                        | PointerEventType::CANCEL
                        | PointerEventType::LEAVE
                        | PointerEventType::OUT => {
                            self.multitouch_map.remove(&event.pointer_id);
                        }
                    }
                }
            }
            PointerType::Mouse => {
                let screen_x = (event.x * width as f64) as i32 + left;
                let screen_y = (event.y * height as f64) as i32 + top;
                self.pending_keyboard_status
                    .extend(send_mouse_pointer_event(event, screen_x, screen_y));
            }
            PointerType::Unknown => todo!(),
        }
    }

    fn send_keyboard_event(&mut self, event: &KeyboardEvent) {
        let desktop = attach_thread_to_input_desktop();
        self.pending_keyboard_status
            .push(desktop.status_line("keyboard attach desktop"));
        if let Err(err) = self.capturable.before_input() {
            warn!("Failed to activate target before keyboard input ({})", err);
            self.pending_keyboard_status
                .push(format!("keyboard before_input failed: {err}"));
            return;
        }
        let focused = focus_window_under_cursor();
        self.pending_keyboard_status.push(format!(
            "keyboard focus under cursor={focused} target={}",
            foreground_window_label()
        ));
        for status in self.keyboard.send_keyboard_event(event) {
            warn!("Windows keyboard status: {}", status);
            self.pending_keyboard_status.push(status);
        }
    }

    fn send_text_input_event(&mut self, event: &TextInputEvent) {
        let desktop = attach_thread_to_input_desktop();
        self.pending_keyboard_status
            .push(desktop.status_line("text attach desktop"));
        if event.text.is_empty() {
            return;
        }
        if let Err(err) = self.capturable.before_input() {
            warn!("Failed to activate target before text input ({})", err);
            self.pending_keyboard_status
                .push(format!("text before_input failed: {err}"));
            return;
        }
        let focused = focus_window_under_cursor();
        self.pending_keyboard_status.push(format!(
            "text focus under cursor={focused} target={}",
            foreground_window_label()
        ));
        let ok = send_unicode_text(&event.text);
        let text_len = event.text.chars().count();
        let preview: String = event.text.chars().take(16).collect();
        let status = if ok {
            "sendinput ok"
        } else {
            "sendinput failed"
        };
        self.pending_keyboard_status
            .push(format!("{status} text len={text_len} text={preview:?}"));
    }

    fn release_keyboard(&mut self) {
        let released = self.keyboard.release_all();
        self.pending_keyboard_status
            .push(format!("keyboard released tracked keys count={released}"));
    }

    fn drain_keyboard_status(&mut self) -> Vec<String> {
        std::mem::take(&mut self.pending_keyboard_status)
    }

    fn set_capturable(&mut self, capturable: Box<dyn Capturable>) {
        self.capturable = capturable;
    }

    fn device_type(&self) -> InputDeviceType {
        InputDeviceType::WindowsInput
    }
}

pub fn input_backend_label() -> String {
    let mut session_id: DWORD = 0;
    let ok = unsafe { ProcessIdToSessionId(GetCurrentProcessId(), &mut session_id) };
    if ok == FALSE {
        return format!(
            "Windows SendInput (session unknown: {})",
            std::io::Error::last_os_error()
        );
    }
    format!("Windows SendInput (session {session_id})")
}

pub fn diagnose_keyboard_context() -> Vec<String> {
    let mut lines = Vec::new();
    lines.push(format!("backend={}", input_backend_label()));
    lines.push(format!("input_desktop={}", input_desktop_label()));
    lines.push(format!("target={}", foreground_window_label()));
    lines
}

pub fn desktop_status() -> WindowsDesktopStatus {
    let input_desktop = input_desktop_label();
    WindowsDesktopStatus {
        is_winlogon: input_desktop.eq_ignore_ascii_case("winlogon"),
        input_desktop,
        backend: input_backend_label(),
    }
}

pub fn is_input_desktop_winlogon() -> bool {
    input_desktop_label().eq_ignore_ascii_case("winlogon")
}

fn send_mouse_input(flags: DWORD, data: DWORD) -> bool {
    let mut input = INPUT {
        type_: INPUT_MOUSE,
        u: unsafe { std::mem::zeroed() },
    };
    unsafe {
        *input.u.mi_mut() = MOUSEINPUT {
            dx: 0,
            dy: 0,
            mouseData: data,
            dwFlags: flags,
            time: 0,
            dwExtraInfo: 0 as ULONG_PTR,
        };
        let sent = SendInput(1, &mut input, std::mem::size_of::<INPUT>() as i32);
        if sent == 0 {
            warn!(
                "Windows mouse SendInput failed: {}",
                std::io::Error::last_os_error()
            );
            return false;
        }
    }
    true
}

fn send_mouse_event_fallback(flags: DWORD, data: DWORD) {
    unsafe { mouse_event(flags, 0, 0, data, 0) };
}

/// Sends a mouse event through `SendInput(INPUT_MOUSE)`, the primary path. Only when SendInput
/// reports failure does it fall back to the legacy `mouse_event` API. Returns whether the primary
/// path succeeded and whether the `mouse_event` fallback was triggered, so callers can surface both
/// in their status logs instead of hiding the fallback behind a success.
fn dispatch_mouse_input(flags: DWORD, data: DWORD) -> (bool, bool) {
    if send_mouse_input(flags, data) {
        (true, false)
    } else {
        send_mouse_event_fallback(flags, data);
        (false, true)
    }
}

fn mouse_dispatch_status(
    prefix: &str,
    flags: DWORD,
    data: DWORD,
    ok: bool,
    fallback: bool,
) -> String {
    format!(
        "{prefix} SendInput flags=0x{flags:x} data={data} ok={ok} fallback={}",
        if fallback { "mouse_event" } else { "false" }
    )
}

fn send_mouse_pointer_event(event: &PointerEvent, screen_x: i32, screen_y: i32) -> Vec<String> {
    let mut dw_flags = 0;
    match event.event_type {
        PointerEventType::DOWN => match event.button {
            Button::PRIMARY => dw_flags |= MOUSEEVENTF_LEFTDOWN,
            Button::SECONDARY => dw_flags |= MOUSEEVENTF_RIGHTDOWN,
            Button::AUXILARY => dw_flags |= MOUSEEVENTF_MIDDLEDOWN,
            _ => {}
        },
        PointerEventType::MOVE | PointerEventType::OVER | PointerEventType::ENTER => {}
        PointerEventType::UP => match event.button {
            Button::PRIMARY => dw_flags |= MOUSEEVENTF_LEFTUP,
            Button::SECONDARY => dw_flags |= MOUSEEVENTF_RIGHTUP,
            Button::AUXILARY => dw_flags |= MOUSEEVENTF_MIDDLEUP,
            _ => {}
        },
        PointerEventType::CANCEL | PointerEventType::LEAVE | PointerEventType::OUT => {
            if event.buttons.contains(Button::PRIMARY) || event.button == Button::PRIMARY {
                dw_flags |= MOUSEEVENTF_LEFTUP;
            }
            if event.buttons.contains(Button::SECONDARY) || event.button == Button::SECONDARY {
                dw_flags |= MOUSEEVENTF_RIGHTUP;
            }
            if event.buttons.contains(Button::AUXILARY) || event.button == Button::AUXILARY {
                dw_flags |= MOUSEEVENTF_MIDDLEUP;
            }
        }
    }
    if matches!(event.event_type, PointerEventType::DOWN) {
        focus_window_at_point(screen_x, screen_y);
    }
    let mut lines = Vec::new();
    let cursor_ok = unsafe { SetCursorPos(screen_x, screen_y) != FALSE };
    lines.push(format!(
        "pointer SetCursorPos x={screen_x} y={screen_y} ok={cursor_ok}"
    ));
    if dw_flags != 0 {
        let (ok, fallback) = dispatch_mouse_input(dw_flags, 0);
        lines.push(mouse_dispatch_status("pointer", dw_flags, 0, ok, fallback));
    }
    lines
}

pub fn diagnose_keyboard_sendinput() -> Vec<String> {
    let mut lines = Vec::new();
    let desktop = attach_thread_to_input_desktop();
    lines.push(desktop.status_line("probe attach desktop"));
    let down = send_vk(b'A' as WORD, true);
    let up = send_vk(b'A' as WORD, false);
    lines.push(format!("sendinput key=A down={down} up={up}"));
    lines
}

pub fn diagnose_input_probe(
    text: Option<&str>,
    send_enter: bool,
    click: Option<(f64, f64)>,
) -> Vec<String> {
    let mut lines = diagnose_keyboard_context();
    let desktop = attach_thread_to_input_desktop();
    lines.push(desktop.status_line("probe attach desktop"));

    if let Some((x, y)) = click {
        let (screen_x, screen_y) = normalized_virtual_screen_point(x, y);
        let cursor_ok = unsafe { SetCursorPos(screen_x, screen_y) != FALSE };
        lines.push(format!(
            "probe click point={x:.4},{y:.4} screen={screen_x},{screen_y} set_cursor={cursor_ok}"
        ));
        let (down_ok, down_fb) = dispatch_mouse_input(MOUSEEVENTF_LEFTDOWN, 0);
        lines.push(mouse_dispatch_status(
            "probe click down",
            MOUSEEVENTF_LEFTDOWN,
            0,
            down_ok,
            down_fb,
        ));
        let (up_ok, up_fb) = dispatch_mouse_input(MOUSEEVENTF_LEFTUP, 0);
        lines.push(mouse_dispatch_status(
            "probe click up",
            MOUSEEVENTF_LEFTUP,
            0,
            up_ok,
            up_fb,
        ));
    }

    if let Some(text) = text.filter(|value| !value.is_empty()) {
        let ok = send_unicode_text(text);
        let preview: String = text.chars().take(16).collect();
        lines.push(format!(
            "probe text len={} text={preview:?} ok={ok}",
            text.chars().count()
        ));
    }

    if send_enter {
        let down = send_vk(VK_RETURN as WORD, true);
        let up = send_vk(VK_RETURN as WORD, false);
        lines.push(format!("probe enter down={down} up={up}"));
    }

    lines.push(format!("input_desktop_after={}", input_desktop_label()));
    lines.push(format!("target_after={}", foreground_window_label()));
    lines
}

pub fn diagnose_notepad_keyboard_input() -> Vec<String> {
    let mut lines = diagnose_keyboard_context();
    unsafe {
        let probe_text = "AuroraOpsInput";
        let mut command = wide("notepad.exe");
        let mut desktop = wide("winsta0\\default");
        let mut startup: STARTUPINFOW = std::mem::zeroed();
        startup.cb = std::mem::size_of::<STARTUPINFOW>() as DWORD;
        startup.lpDesktop = desktop.as_mut_ptr();
        let mut process: PROCESS_INFORMATION = std::mem::zeroed();
        let created = CreateProcessW(
            ptr::null(),
            command.as_mut_ptr(),
            ptr::null_mut(),
            ptr::null_mut(),
            FALSE,
            0,
            ptr::null_mut(),
            ptr::null(),
            &mut startup,
            &mut process,
        );
        if created == FALSE {
            lines.push(format!(
                "notepad=create failed: {}",
                std::io::Error::last_os_error()
            ));
            return lines;
        }

        let idle = WaitForInputIdle(process.hProcess, 3000);
        lines.push(format!(
            "notepad=pid {} wait_idle={idle}",
            process.dwProcessId
        ));
        std::thread::sleep(std::time::Duration::from_millis(500));
        let hwnd = find_top_window_for_pid(process.dwProcessId)
            .unwrap_or_else(|| FindWindowW(wide("Notepad").as_ptr(), ptr::null()));
        if hwnd.is_null() {
            lines.push("notepad_window=not found".to_string());
        } else {
            let foreground = force_foreground_window(hwnd);
            lines.push(format!(
                "notepad_window={hwnd:p} set_foreground={foreground}"
            ));
            std::thread::sleep(std::time::Duration::from_millis(300));
            if let Some(edit_hwnd) = find_text_input_child(hwnd) {
                let class_name = window_class_name(edit_hwnd);
                let focused = SetFocus(edit_hwnd);
                lines.push(format!(
                    "notepad_edit={edit_hwnd:p} class={class_name:?} set_focus={focused:p}"
                ));
                std::thread::sleep(std::time::Duration::from_millis(100));
            } else {
                lines.push("notepad_edit=not found".to_string());
            }
            lines.extend(diagnose_keyboard_context());
            let sent = send_unicode_text(probe_text);
            lines.push(format!("sendinput text={probe_text:?} ok={sent}"));
            std::thread::sleep(std::time::Duration::from_millis(500));
            if let Some(text) = read_notepad_text(hwnd) {
                lines.push(format!("notepad_text={text:?}"));
                lines.push(format!("notepad_received={}", text.contains(probe_text)));
            } else {
                lines.push("notepad_text=unavailable".to_string());
            }
        }

        let _ = CloseHandle(process.hThread);
        let _ = CloseHandle(process.hProcess);
    }
    lines
}

struct FindWindowForPid {
    pid: DWORD,
    hwnd: HWND,
}

unsafe extern "system" fn enum_windows_for_pid(hwnd: HWND, lparam: LPARAM) -> BOOL {
    let data = &mut *(lparam as *mut FindWindowForPid);
    let mut pid: DWORD = 0;
    GetWindowThreadProcessId(hwnd, &mut pid);
    if pid == data.pid && IsWindowVisible(hwnd) != FALSE {
        data.hwnd = hwnd;
        return FALSE;
    }
    TRUE
}

fn find_top_window_for_pid(pid: DWORD) -> Option<HWND> {
    let mut data = FindWindowForPid {
        pid,
        hwnd: ptr::null_mut(),
    };
    unsafe {
        EnumWindows(
            Some(enum_windows_for_pid),
            (&mut data as *mut FindWindowForPid) as LPARAM,
        );
    }
    if data.hwnd.is_null() {
        None
    } else {
        Some(data.hwnd)
    }
}

struct FindTextInputChild {
    hwnd: HWND,
}

unsafe extern "system" fn enum_text_input_child(hwnd: HWND, lparam: LPARAM) -> BOOL {
    let data = &mut *(lparam as *mut FindTextInputChild);
    let class_name = window_class_name(hwnd).to_ascii_lowercase();
    if class_name.contains("edit") || class_name.contains("richedit") {
        data.hwnd = hwnd;
        return FALSE;
    }
    TRUE
}

fn find_text_input_child(hwnd: HWND) -> Option<HWND> {
    let mut data = FindTextInputChild {
        hwnd: ptr::null_mut(),
    };
    unsafe {
        EnumChildWindows(
            hwnd,
            Some(enum_text_input_child),
            (&mut data as *mut FindTextInputChild) as LPARAM,
        );
    }
    if data.hwnd.is_null() {
        None
    } else {
        Some(data.hwnd)
    }
}

fn force_foreground_window(hwnd: HWND) -> BOOL {
    unsafe {
        let foreground = GetForegroundWindow();
        let current_thread = GetCurrentThreadId();
        let foreground_thread = if foreground.is_null() {
            0
        } else {
            GetWindowThreadProcessId(foreground, ptr::null_mut())
        };
        let target_thread = GetWindowThreadProcessId(hwnd, ptr::null_mut());
        if foreground_thread != 0 && foreground_thread != current_thread {
            AttachThreadInput(current_thread, foreground_thread, TRUE);
        }
        if target_thread != 0 && target_thread != current_thread {
            AttachThreadInput(current_thread, target_thread, TRUE);
        }
        ShowWindow(hwnd, SW_RESTORE);
        let ok = SetForegroundWindow(hwnd);
        BringWindowToTop(hwnd);
        SetActiveWindow(hwnd);
        if target_thread != 0 && target_thread != current_thread {
            AttachThreadInput(current_thread, target_thread, FALSE);
        }
        if foreground_thread != 0 && foreground_thread != current_thread {
            AttachThreadInput(current_thread, foreground_thread, FALSE);
        }
        ok
    }
}

fn window_class_name(hwnd: HWND) -> String {
    unsafe {
        let mut buf = vec![0u16; 256];
        let copied = GetClassNameW(hwnd, buf.as_mut_ptr(), buf.len() as i32);
        if copied <= 0 {
            return String::new();
        }
        String::from_utf16_lossy(&buf[..copied as usize])
    }
}

fn read_notepad_text(hwnd: HWND) -> Option<String> {
    let edit = find_text_input_child(hwnd)?;
    unsafe {
        let len = SendMessageW(edit, WM_GETTEXTLENGTH, 0, 0) as usize;
        let mut buf = vec![0u16; len.saturating_add(1)];
        let copied = SendMessageW(edit, WM_GETTEXT, buf.len(), buf.as_mut_ptr() as LPARAM) as usize;
        Some(String::from_utf16_lossy(&buf[..copied.min(buf.len())]))
    }
}

fn is_text_key(key: &str) -> bool {
    let mut chars = key.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    chars.next().is_none() && !first.is_control()
}

fn should_send_unicode(key: &str) -> bool {
    is_text_key(key) && !key.is_ascii()
}

fn wide(value: &str) -> Vec<u16> {
    std::ffi::OsStr::new(value)
        .encode_wide()
        .chain(iter::once(0))
        .collect()
}

fn send_vk(vk: WORD, down: bool) -> bool {
    let mut flags = 0;
    if !down {
        flags |= KEYEVENTF_KEYUP;
    }
    if is_extended_key(vk) {
        flags |= KEYEVENTF_EXTENDEDKEY;
    }
    send_keyboard_input(vk, 0, flags)
}

fn send_scancode(vk: WORD, down: bool) -> bool {
    let scan = unsafe { MapVirtualKeyW(vk as u32, MAPVK_VK_TO_VSC_EX) } as WORD;
    if scan == 0 {
        return send_vk(vk, down);
    }
    let scan = scan & 0xff;
    let mut flags = KEYEVENTF_SCANCODE;
    if !down {
        flags |= KEYEVENTF_KEYUP;
    }
    if is_extended_key(vk) {
        flags |= KEYEVENTF_EXTENDEDKEY;
    }
    send_keyboard_input(vk, scan, flags)
}

fn send_unicode(scan: WORD, down: bool) -> bool {
    let mut flags = KEYEVENTF_UNICODE;
    if !down {
        flags |= KEYEVENTF_KEYUP;
    }
    send_keyboard_input(0, scan, flags)
}

fn send_unicode_text(text: &str) -> bool {
    let mut ok = true;
    for unit in text.encode_utf16() {
        ok &= send_unicode(unit, true);
        ok &= send_unicode(unit, false);
    }
    ok
}

fn send_keyboard_input(vk: WORD, scan: WORD, flags: DWORD) -> bool {
    let mut input = INPUT {
        type_: INPUT_KEYBOARD,
        u: unsafe { std::mem::zeroed() },
    };
    unsafe {
        *input.u.ki_mut() = KEYBDINPUT {
            wVk: vk,
            wScan: scan,
            dwFlags: flags,
            time: 0,
            dwExtraInfo: 0 as ULONG_PTR,
        };
        let sent = SendInput(1, &mut input, std::mem::size_of::<INPUT>() as i32);
        if sent == 0 {
            warn!(
                "Windows SendInput failed: {}",
                std::io::Error::last_os_error()
            );
            return false;
        }
    }
    true
}

fn focus_window_under_cursor() -> bool {
    unsafe {
        let mut point = POINT { x: 0, y: 0 };
        if GetCursorPos(&mut point) == FALSE {
            warn!(
                "GetCursorPos failed before keyboard focus: {}",
                std::io::Error::last_os_error()
            );
            return false;
        }
        focus_window_at_point(point.x, point.y)
    }
}

fn focus_window_at_point(screen_x: i32, screen_y: i32) -> bool {
    unsafe {
        let point = POINT {
            x: screen_x,
            y: screen_y,
        };
        let mut hwnd = WindowFromPoint(point);
        if hwnd.is_null() {
            return false;
        }
        let root = GetAncestor(hwnd, GA_ROOT);
        if !root.is_null() {
            hwnd = root;
        }
        force_foreground_window(hwnd) != FALSE
    }
}

fn desktop_name(desktop: HDESK) -> Option<String> {
    unsafe {
        let mut needed: DWORD = 0;
        let _ = GetUserObjectInformationW(
            desktop.cast(),
            UOI_NAME as i32,
            ptr::null_mut(),
            0,
            &mut needed,
        );
        if needed == 0 {
            return None;
        }
        let mut buf = vec![0u16; (needed as usize + 1) / 2];
        let ok = GetUserObjectInformationW(
            desktop.cast(),
            UOI_NAME as i32,
            buf.as_mut_ptr().cast(),
            needed,
            &mut needed,
        );
        if ok == FALSE {
            return None;
        }
        let len = buf.iter().position(|ch| *ch == 0).unwrap_or(buf.len());
        Some(String::from_utf16_lossy(&buf[..len]))
    }
}

fn input_desktop_label() -> String {
    unsafe {
        let input = OpenInputDesktop(0, FALSE, DESKTOP_READOBJECTS | DESKTOP_ENUMERATE);
        if input.is_null() {
            return format!("unavailable: {}", std::io::Error::last_os_error());
        }
        let label = desktop_name(input).unwrap_or_else(|| "unknown".to_string());
        CloseDesktop(input);
        label
    }
}

struct InputDesktopHandle(HDESK);

impl Drop for InputDesktopHandle {
    fn drop(&mut self) {
        unsafe {
            if !self.0.is_null() {
                CloseDesktop(self.0);
            }
        }
    }
}

struct InputDesktopAttachment {
    _handle: Option<InputDesktopHandle>,
    desktop: String,
    open_error: Option<String>,
    set_thread_ok: Option<bool>,
    set_thread_error: Option<String>,
}

impl InputDesktopAttachment {
    fn status_line(&self, prefix: &str) -> String {
        let open = if let Some(err) = &self.open_error {
            format!("failed: {err}")
        } else {
            "ok".to_string()
        };
        let set_thread = match (self.set_thread_ok, self.set_thread_error.as_ref()) {
            (Some(true), _) => "ok".to_string(),
            (Some(false), Some(err)) => format!("failed: {err}"),
            (Some(false), None) => "failed".to_string(),
            (None, _) => "skipped".to_string(),
        };
        format!(
            "{prefix}: inputDesktop={} openInputDesktop={open} setThreadDesktop={set_thread}",
            self.desktop
        )
    }
}

fn attach_thread_to_input_desktop() -> InputDesktopAttachment {
    unsafe {
        let input = OpenInputDesktop(
            0,
            FALSE,
            DESKTOP_READOBJECTS
                | DESKTOP_WRITEOBJECTS
                | DESKTOP_CREATEWINDOW
                | DESKTOP_CREATEMENU
                | DESKTOP_HOOKCONTROL
                | DESKTOP_JOURNALRECORD
                | DESKTOP_JOURNALPLAYBACK
                | DESKTOP_ENUMERATE
                | DESKTOP_SWITCHDESKTOP,
        );
        if input.is_null() {
            let err = std::io::Error::last_os_error().to_string();
            warn!("OpenInputDesktop failed before Windows input: {}", err);
            return InputDesktopAttachment {
                _handle: None,
                desktop: format!("unavailable: {err}"),
                open_error: Some(err),
                set_thread_ok: None,
                set_thread_error: None,
            };
        }
        let desktop = desktop_name(input).unwrap_or_else(|| "unknown".to_string());
        if SetThreadDesktop(input) == FALSE {
            let err = std::io::Error::last_os_error().to_string();
            warn!(
                "SetThreadDesktop(input desktop) failed before Windows input: {}",
                err
            );
            return InputDesktopAttachment {
                _handle: Some(InputDesktopHandle(input)),
                desktop,
                open_error: None,
                set_thread_ok: Some(false),
                set_thread_error: Some(err),
            };
        }
        InputDesktopAttachment {
            _handle: Some(InputDesktopHandle(input)),
            desktop,
            open_error: None,
            set_thread_ok: Some(true),
            set_thread_error: None,
        }
    }
}

fn normalized_virtual_screen_point(x: f64, y: f64) -> (i32, i32) {
    let x = x.clamp(0.0, 1.0);
    let y = y.clamp(0.0, 1.0);
    unsafe {
        let left = GetSystemMetrics(SM_XVIRTUALSCREEN);
        let top = GetSystemMetrics(SM_YVIRTUALSCREEN);
        let width = GetSystemMetrics(SM_CXVIRTUALSCREEN).max(1);
        let height = GetSystemMetrics(SM_CYVIRTUALSCREEN).max(1);
        (
            left + (x * f64::from(width.saturating_sub(1))) as i32,
            top + (y * f64::from(height.saturating_sub(1))) as i32,
        )
    }
}

fn foreground_window_label() -> String {
    unsafe {
        let hwnd = GetForegroundWindow();
        if hwnd.is_null() {
            return "none".to_string();
        }
        let mut pid: DWORD = 0;
        let tid = GetWindowThreadProcessId(hwnd, &mut pid);
        let len = GetWindowTextLengthW(hwnd);
        let title = if len > 0 {
            let mut buf = vec![0u16; len as usize + 1];
            let copied = GetWindowTextW(hwnd, buf.as_mut_ptr(), buf.len() as i32);
            String::from_utf16_lossy(&buf[..copied.max(0) as usize])
        } else {
            String::new()
        };
        format!("hwnd={hwnd:p} pid={pid} tid={tid} title={title:?}")
    }
}

fn is_extended_key(vk: WORD) -> bool {
    matches!(
        vk as i32,
        VK_RCONTROL
            | VK_RMENU
            | VK_INSERT
            | VK_DELETE
            | VK_HOME
            | VK_END
            | VK_PRIOR
            | VK_NEXT
            | VK_LEFT
            | VK_RIGHT
            | VK_UP
            | VK_DOWN
            | VK_DIVIDE
            | VK_NUMLOCK
            | VK_LWIN
            | VK_RWIN
    )
}

fn modifier_family_matches(vk: WORD, modifier_vk: WORD) -> bool {
    matches!(
        (vk as i32, modifier_vk as i32),
        (VK_CONTROL, VK_CONTROL)
            | (VK_LCONTROL, VK_CONTROL)
            | (VK_RCONTROL, VK_CONTROL)
            | (VK_MENU, VK_MENU)
            | (VK_LMENU, VK_MENU)
            | (VK_RMENU, VK_MENU)
            | (VK_SHIFT, VK_SHIFT)
            | (VK_LSHIFT, VK_SHIFT)
            | (VK_RSHIFT, VK_SHIFT)
            | (VK_LWIN, VK_LWIN)
            | (VK_RWIN, VK_LWIN)
    )
}

fn map_keyboard_code(code: &str, location: &crate::protocol::KeyboardLocation) -> Option<WORD> {
    let vk = match code {
        "KeyA" => b'A' as i32,
        "KeyB" => b'B' as i32,
        "KeyC" => b'C' as i32,
        "KeyD" => b'D' as i32,
        "KeyE" => b'E' as i32,
        "KeyF" => b'F' as i32,
        "KeyG" => b'G' as i32,
        "KeyH" => b'H' as i32,
        "KeyI" => b'I' as i32,
        "KeyJ" => b'J' as i32,
        "KeyK" => b'K' as i32,
        "KeyL" => b'L' as i32,
        "KeyM" => b'M' as i32,
        "KeyN" => b'N' as i32,
        "KeyO" => b'O' as i32,
        "KeyP" => b'P' as i32,
        "KeyQ" => b'Q' as i32,
        "KeyR" => b'R' as i32,
        "KeyS" => b'S' as i32,
        "KeyT" => b'T' as i32,
        "KeyU" => b'U' as i32,
        "KeyV" => b'V' as i32,
        "KeyW" => b'W' as i32,
        "KeyX" => b'X' as i32,
        "KeyY" => b'Y' as i32,
        "KeyZ" => b'Z' as i32,
        "Digit0" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD0,
            _ => b'0' as i32,
        },
        "Digit1" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD1,
            _ => b'1' as i32,
        },
        "Digit2" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD2,
            _ => b'2' as i32,
        },
        "Digit3" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD3,
            _ => b'3' as i32,
        },
        "Digit4" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD4,
            _ => b'4' as i32,
        },
        "Digit5" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD5,
            _ => b'5' as i32,
        },
        "Digit6" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD6,
            _ => b'6' as i32,
        },
        "Digit7" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD7,
            _ => b'7' as i32,
        },
        "Digit8" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD8,
            _ => b'8' as i32,
        },
        "Digit9" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_NUMPAD9,
            _ => b'9' as i32,
        },
        "Escape" => VK_ESCAPE,
        "Enter" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_RETURN,
            _ => VK_RETURN,
        },
        "Backspace" => VK_BACK,
        "Tab" => VK_TAB,
        "Space" => VK_SPACE,
        "CapsLock" => VK_CAPITAL,
        "ShiftLeft" => VK_LSHIFT,
        "ShiftRight" => VK_RSHIFT,
        "ControlLeft" => VK_LCONTROL,
        "ControlRight" => VK_RCONTROL,
        "AltLeft" => VK_LMENU,
        "AltRight" => VK_RMENU,
        "MetaLeft" => VK_LWIN,
        "MetaRight" => VK_RWIN,
        "ContextMenu" => VK_APPS,
        "ArrowUp" => VK_UP,
        "ArrowDown" => VK_DOWN,
        "ArrowLeft" => VK_LEFT,
        "ArrowRight" => VK_RIGHT,
        "Home" => VK_HOME,
        "End" => VK_END,
        "PageUp" => VK_PRIOR,
        "PageDown" => VK_NEXT,
        "Insert" => VK_INSERT,
        "Delete" => VK_DELETE,
        "PrintScreen" => VK_SNAPSHOT,
        "ScrollLock" => VK_SCROLL,
        "Pause" => VK_PAUSE,
        "NumLock" => VK_NUMLOCK,
        "Numpad0" => VK_NUMPAD0,
        "Numpad1" => VK_NUMPAD1,
        "Numpad2" => VK_NUMPAD2,
        "Numpad3" => VK_NUMPAD3,
        "Numpad4" => VK_NUMPAD4,
        "Numpad5" => VK_NUMPAD5,
        "Numpad6" => VK_NUMPAD6,
        "Numpad7" => VK_NUMPAD7,
        "Numpad8" => VK_NUMPAD8,
        "Numpad9" => VK_NUMPAD9,
        "NumpadDecimal" => VK_DECIMAL,
        "NumpadDivide" => VK_DIVIDE,
        "NumpadMultiply" => VK_MULTIPLY,
        "NumpadSubtract" => VK_SUBTRACT,
        "NumpadAdd" => VK_ADD,
        "NumpadEnter" => VK_RETURN,
        "Semicolon" => VK_OEM_1,
        "Equal" => VK_OEM_PLUS,
        "Comma" => VK_OEM_COMMA,
        "Minus" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_SUBTRACT,
            _ => VK_OEM_MINUS,
        },
        "Period" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_DECIMAL,
            _ => VK_OEM_PERIOD,
        },
        "Slash" => match location {
            crate::protocol::KeyboardLocation::NUMPAD => VK_DIVIDE,
            _ => VK_OEM_2,
        },
        "Backquote" => VK_OEM_3,
        "BracketLeft" => VK_OEM_4,
        "Backslash" => VK_OEM_5,
        "BracketRight" => VK_OEM_6,
        "Quote" => VK_OEM_7,
        f if f.starts_with('F') => f
            .get(1..)
            .and_then(|n| n.parse::<i32>().ok())
            .filter(|n| (1..=24).contains(n))
            .map(|n| VK_F1 + n - 1)?,
        _ => return None,
    };
    Some(vk as WORD)
}
