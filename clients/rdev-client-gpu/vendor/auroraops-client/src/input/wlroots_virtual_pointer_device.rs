use std::time::Instant;

use tracing::{debug, warn};
use wayland_client::globals::{registry_queue_init, GlobalListContents};
use wayland_client::protocol::{wl_pointer, wl_registry};
use wayland_client::{delegate_noop, Connection, Dispatch, EventQueue, QueueHandle};
use wayland_protocols_wlr::virtual_pointer::v1::client::{
    zwlr_virtual_pointer_manager_v1::ZwlrVirtualPointerManagerV1,
    zwlr_virtual_pointer_v1::ZwlrVirtualPointerV1,
};

use crate::capturable::{Capturable, Geometry};
use crate::input::device::{InputDevice, InputDeviceType};
use crate::protocol::{
    Button, KeyboardEvent, PointerEvent, PointerEventType, PointerType, WheelEvent,
};

const BTN_LEFT: u32 = 0x110;
const BTN_RIGHT: u32 = 0x111;
const BTN_MIDDLE: u32 = 0x112;
const BTN_SIDE: u32 = 0x113;
const BTN_EXTRA: u32 = 0x114;

struct WlrootsState;

impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for WlrootsState {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        event: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<WlrootsState>,
    ) {
        if let wl_registry::Event::Global {
            interface, version, ..
        } = event
        {
            debug!("Wayland global available: {interface} v{version}");
        }
    }
}

delegate_noop!(WlrootsState: ignore ZwlrVirtualPointerManagerV1);
delegate_noop!(WlrootsState: ignore ZwlrVirtualPointerV1);

pub struct WlrootsVirtualPointerDevice {
    conn: Connection,
    event_queue: EventQueue<WlrootsState>,
    state: WlrootsState,
    pointer: ZwlrVirtualPointerV1,
    capturable: Box<dyn Capturable>,
    created_at: Instant,
}

impl WlrootsVirtualPointerDevice {
    pub fn new(capturable: Box<dyn Capturable>) -> Result<Self, String> {
        let conn = Connection::connect_to_env()
            .map_err(|err| format!("failed to connect to Wayland compositor: {err}"))?;
        let (globals, mut event_queue) = registry_queue_init::<WlrootsState>(&conn)
            .map_err(|err| format!("failed to read Wayland globals: {err}"))?;
        let qh = event_queue.handle();
        let manager = globals
            .bind::<ZwlrVirtualPointerManagerV1, _, _>(&qh, 1..=2, ())
            .map_err(|err| format!("wlroots virtual pointer protocol is unavailable: {err}"))?;
        let pointer = manager.create_virtual_pointer(None, &qh, ());
        conn.flush()
            .map_err(|err| format!("failed to flush virtual pointer creation: {err}"))?;
        let mut state = WlrootsState;
        let _ = event_queue.roundtrip(&mut state);
        debug!("Using wlroots virtual pointer input backend");
        Ok(Self {
            conn,
            event_queue,
            state,
            pointer,
            capturable,
            created_at: Instant::now(),
        })
    }

    fn time_ms(&self) -> u32 {
        self.created_at.elapsed().as_millis().min(u32::MAX as u128) as u32
    }

    fn flush(&mut self) {
        if let Err(err) = self.conn.flush() {
            warn!("Failed to flush wlroots virtual pointer event: {err}");
        }
        while matches!(self.event_queue.dispatch_pending(&mut self.state), Ok(1..)) {}
    }

    fn pointer_button(button: Button) -> Option<u32> {
        match button {
            Button::PRIMARY => Some(BTN_LEFT),
            Button::SECONDARY => Some(BTN_RIGHT),
            Button::AUXILARY => Some(BTN_MIDDLE),
            Button::FOURTH => Some(BTN_SIDE),
            Button::FIFTH => Some(BTN_EXTRA),
            _ => None,
        }
    }

    fn absolute_coordinates(&self, x: f64, y: f64) -> (u32, u32, u32, u32) {
        let (extent_x, extent_y) = match self.capturable.geometry() {
            Ok(Geometry::Relative(_, _, width, height)) => {
                let w = (width * 1_000_000.0).round().max(1.0) as u32;
                let h = (height * 1_000_000.0).round().max(1.0) as u32;
                (w, h)
            }
            #[cfg(target_os = "windows")]
            Ok(Geometry::VirtualScreen(_, _, width, height, _, _)) => (width.max(1), height.max(1)),
            Err(_) => (1_000_000, 1_000_000),
        };
        let px = (x.clamp(0.0, 1.0) * extent_x as f64).round() as u32;
        let py = (y.clamp(0.0, 1.0) * extent_y as f64).round() as u32;
        (px.min(extent_x), py.min(extent_y), extent_x, extent_y)
    }
}

impl InputDevice for WlrootsVirtualPointerDevice {
    fn send_wheel_event(&mut self, event: &WheelEvent) {
        let time = self.time_ms();
        if event.dy != 0 {
            self.pointer
                .axis(time, wl_pointer::Axis::VerticalScroll, -(event.dy as f64));
        }
        if event.dx != 0 {
            self.pointer
                .axis(time, wl_pointer::Axis::HorizontalScroll, event.dx as f64);
        }
        self.pointer.frame();
        self.flush();
    }

    fn send_pointer_event(&mut self, event: &PointerEvent) {
        let time = self.time_ms();
        if matches!(
            event.event_type,
            PointerEventType::DOWN | PointerEventType::MOVE | PointerEventType::ENTER
        ) && !matches!(event.pointer_type, PointerType::Touch)
        {
            let (x, y, x_extent, y_extent) = self.absolute_coordinates(event.x, event.y);
            self.pointer
                .motion_absolute(time, x, y, x_extent.max(1), y_extent.max(1));
        }

        if let Some(button) = Self::pointer_button(event.button) {
            let state = match event.event_type {
                PointerEventType::DOWN => Some(wl_pointer::ButtonState::Pressed),
                PointerEventType::UP | PointerEventType::CANCEL | PointerEventType::LEAVE => {
                    Some(wl_pointer::ButtonState::Released)
                }
                _ => None,
            };
            if let Some(state) = state {
                self.pointer.button(time, button, state);
            }
        }
        self.pointer.frame();
        self.flush();
    }

    fn send_keyboard_event(&mut self, _event: &KeyboardEvent) {}

    fn set_capturable(&mut self, capturable: Box<dyn Capturable>) {
        self.capturable = capturable;
    }

    fn device_type(&self) -> InputDeviceType {
        InputDeviceType::WlrootsVirtualPointer
    }
}
