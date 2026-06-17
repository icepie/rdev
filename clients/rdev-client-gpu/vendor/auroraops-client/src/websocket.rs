use fastwebsockets::{FragmentCollectorRead, Frame, OpCode, WebSocket, WebSocketError};
use hyper::upgrade::Upgraded;
use hyper_util::rt::TokioIo;
use std::convert::Infallible;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::mpsc::RecvTimeoutError;
use std::sync::{mpsc, Arc};
use std::thread::{spawn, JoinHandle};
use std::time::{Duration, Instant};
use tokio::sync::mpsc::channel;
use tracing::{debug, info, trace, warn};

use crate::capturable::{get_capturables, Capturable, Recorder, RecorderPreferences};
use crate::input::device::{InputDevice, InputDeviceType};
use crate::protocol::{
    ClientConfiguration, EncoderCapabilities, EncoderOption, InputCapabilities, InputOption,
    KeyboardEvent, MessageInbound, MessageOutbound, PointerEvent, RuntimeStatus, TextInputEvent,
    WeylusReceiver, WeylusSender, WheelEvent,
};

use crate::cerror::CErrorCode;
use crate::video::{EncoderOptions, VideoEncoder};

struct VideoConfig {
    capturable: Box<dyn Capturable>,
    capture_cursor: bool,
    max_width: usize,
    max_height: usize,
    frame_rate: f64,
    encoder: Option<String>,
}

enum VideoCommands {
    Start(VideoConfig),
    Pause,
    Resume,
    Restart,
}

fn normalize_encoder(value: Option<&str>) -> Option<String> {
    match value.unwrap_or("auto").trim().to_ascii_lowercase().as_str() {
        "" | "auto" => None,
        "libx264" | "x264" | "software" => Some("libx264".to_string()),
        "nvenc" | "h264_nvenc" => Some("nvenc".to_string()),
        "vaapi" | "h264_vaapi" => Some("vaapi".to_string()),
        "vulkan" | "vulkan-video" | "vulkan_video" | "h264_vulkan" => {
            Some("vulkan-video".to_string())
        }
        "mediafoundation" | "mf" | "h264_mf" => Some("mediafoundation".to_string()),
        "videotoolbox" | "h264_videotoolbox" => Some("videotoolbox".to_string()),
        _ => None,
    }
}

fn encoder_options_for_selection(base: EncoderOptions, selected: Option<&str>) -> EncoderOptions {
    match normalize_encoder(selected).as_deref() {
        Some("libx264") => EncoderOptions {
            try_vaapi: false,
            try_nvenc: false,
            try_vulkan_video: false,
            try_videotoolbox: false,
            try_mediafoundation: false,
        },
        Some("nvenc") => EncoderOptions {
            try_vaapi: false,
            try_nvenc: base.try_nvenc,
            try_vulkan_video: false,
            try_videotoolbox: false,
            try_mediafoundation: false,
        },
        Some("vaapi") => EncoderOptions {
            try_vaapi: base.try_vaapi,
            try_nvenc: false,
            try_vulkan_video: false,
            try_videotoolbox: false,
            try_mediafoundation: false,
        },
        Some("vulkan-video") => EncoderOptions {
            try_vaapi: false,
            try_nvenc: false,
            try_vulkan_video: base.try_vulkan_video,
            try_videotoolbox: false,
            try_mediafoundation: false,
        },
        Some("mediafoundation") => EncoderOptions {
            try_vaapi: false,
            try_nvenc: false,
            try_vulkan_video: false,
            try_videotoolbox: false,
            try_mediafoundation: base.try_mediafoundation,
        },
        Some("videotoolbox") => EncoderOptions {
            try_vaapi: false,
            try_nvenc: false,
            try_vulkan_video: false,
            try_videotoolbox: base.try_videotoolbox,
            try_mediafoundation: false,
        },
        _ => base,
    }
}

fn encoder_capabilities(options: EncoderOptions) -> EncoderCapabilities {
    let mut encoders = vec![
        EncoderOption {
            value: "auto".to_string(),
            label: "自动".to_string(),
        },
        EncoderOption {
            value: "libx264".to_string(),
            label: "libx264".to_string(),
        },
    ];
    if options.try_nvenc {
        encoders.push(EncoderOption {
            value: "nvenc".to_string(),
            label: "NVENC".to_string(),
        });
    }
    if options.try_vaapi {
        encoders.push(EncoderOption {
            value: "vaapi".to_string(),
            label: "VAAPI".to_string(),
        });
    }
    if options.try_vulkan_video {
        encoders.push(EncoderOption {
            value: "vulkan-video".to_string(),
            label: "Vulkan Video".to_string(),
        });
    }
    if options.try_mediafoundation {
        encoders.push(EncoderOption {
            value: "mediafoundation".to_string(),
            label: "MediaFoundation".to_string(),
        });
    }
    if options.try_videotoolbox {
        encoders.push(EncoderOption {
            value: "videotoolbox".to_string(),
            label: "VideoToolbox".to_string(),
        });
    }
    EncoderCapabilities { options: encoders }
}

fn input_capabilities() -> InputCapabilities {
    let base_options = vec![
        InputOption {
            value: "auto".to_string(),
            label: "自动".to_string(),
        },
        InputOption {
            value: "none".to_string(),
            label: "禁用".to_string(),
        },
    ];
    let mut pointer_options = base_options.clone();
    let mut keyboard_options = base_options;

    #[cfg(target_os = "linux")]
    {
        let portal = InputOption {
            value: "portal".to_string(),
            label: "Wayland Portal".to_string(),
        };
        pointer_options.push(portal.clone());
        keyboard_options.push(portal);
        #[cfg(feature = "pipewire")]
        pointer_options.push(InputOption {
            value: "wlroots-pointer".to_string(),
            label: "wlroots 指针".to_string(),
        });
        let uinput = InputOption {
            value: "uinput".to_string(),
            label: "uinput".to_string(),
        };
        pointer_options.push(uinput.clone());
        keyboard_options.push(uinput);
        let xtest = InputOption {
            value: "xtest".to_string(),
            label: "XTest".to_string(),
        };
        pointer_options.push(xtest.clone());
        keyboard_options.push(xtest);
    }
    #[cfg(target_os = "windows")]
    {
        let platform = InputOption {
            value: "platform".to_string(),
            label: "Windows SendInput".to_string(),
        };
        pointer_options.push(platform.clone());
        keyboard_options.push(platform);
    }
    #[cfg(target_os = "macos")]
    {
        let autopilot = InputOption {
            value: "autopilot".to_string(),
            label: "AutoPilot".to_string(),
        };
        pointer_options.push(autopilot.clone());
        keyboard_options.push(autopilot);
    }

    let mut options = pointer_options.clone();
    for option in &keyboard_options {
        if !options
            .iter()
            .any(|existing| existing.value == option.value)
        {
            options.push(option.clone());
        }
    }

    InputCapabilities {
        options,
        pointer_options,
        keyboard_options,
    }
}

fn send_message<S>(sender: &mut S, message: MessageOutbound)
where
    S: WeylusSender,
{
    if let Err(err) = sender.send_message(message) {
        warn!("Failed to send message to client: {err}");
    }
}

fn send_runtime_status<S>(sender: &mut S, capture_backend: &str, encoder_backend: &str)
where
    S: WeylusSender,
{
    send_message(
        sender,
        MessageOutbound::RuntimeStatus(RuntimeStatus {
            capture_backend: Some(capture_backend.to_string()),
            encoder_backend: Some(encoder_backend.to_string()),
            input_backend: None,
            pointer_backend: None,
            keyboard_backend: None,
        }),
    );
}

fn send_input_status<S>(sender: &mut S, input_backend: &str)
where
    S: WeylusSender,
{
    send_message(
        sender,
        MessageOutbound::RuntimeStatus(RuntimeStatus {
            capture_backend: None,
            encoder_backend: None,
            input_backend: Some(input_backend.to_string()),
            pointer_backend: None,
            keyboard_backend: None,
        }),
    );
}

fn send_pointer_status<S>(sender: &mut S, pointer_backend: impl Into<String>)
where
    S: WeylusSender,
{
    send_message(
        sender,
        MessageOutbound::RuntimeStatus(RuntimeStatus {
            capture_backend: None,
            encoder_backend: None,
            input_backend: None,
            pointer_backend: Some(pointer_backend.into()),
            keyboard_backend: None,
        }),
    );
}

#[cfg(target_os = "linux")]
#[cfg(target_os = "macos")]
fn has_x_display() -> bool {
    std::env::var_os("DISPLAY").is_some()
}

#[cfg(all(target_os = "linux", feature = "pipewire"))]
fn try_wayland_portal_input_device(
    capturable: Box<dyn Capturable>,
) -> Result<Box<dyn InputDevice>, String> {
    crate::input::wayland_portal_device::WaylandPortalDevice::new(capturable)
        .map(|device| Box::new(device) as Box<dyn InputDevice>)
}

#[cfg(all(target_os = "linux", feature = "pipewire"))]
fn try_wlroots_virtual_pointer_device(
    capturable: Box<dyn Capturable>,
) -> Result<Box<dyn InputDevice>, String> {
    crate::input::wlroots_virtual_pointer_device::WlrootsVirtualPointerDevice::new(capturable)
        .map(|device| Box::new(device) as Box<dyn InputDevice>)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum InputBackendSelection {
    Auto,
    Platform,
    Portal,
    WlrootsPointer,
    UInput,
    XTest,
    AutoPilot,
    None,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum DeviceSlot {
    Pointer,
    Keyboard,
}

fn parse_backend_value(value: &str) -> InputBackendSelection {
    match value.trim().to_ascii_lowercase().as_str() {
        "" | "auto" => InputBackendSelection::Auto,
        "platform" | "native" | "windows" | "sendinput" | "system" | "system-input" => {
            InputBackendSelection::Platform
        }
        "portal" | "wayland-portal" | "remote-desktop" => InputBackendSelection::Portal,
        "wlroots" | "wlroots-pointer" | "wlr" | "wlr-pointer" => {
            InputBackendSelection::WlrootsPointer
        }
        "uinput" => InputBackendSelection::UInput,
        "xtest" => InputBackendSelection::XTest,
        "autopilot" => InputBackendSelection::AutoPilot,
        "none" | "disabled" | "off" => InputBackendSelection::None,
        _ => InputBackendSelection::Auto,
    }
}

fn resolve_backend_value<'a>(
    config: &'a ClientConfiguration,
    explicit: Option<&'a str>,
) -> &'a str {
    let value = explicit.or(config.input_backend.as_deref());
    #[cfg(target_os = "linux")]
    {
        value.unwrap_or_else(|| {
            if config.uinput_support {
                "auto"
            } else {
                "xtest"
            }
        })
    }
    #[cfg(not(target_os = "linux"))]
    {
        value.unwrap_or("auto")
    }
}

fn pointer_backend_selection(config: &ClientConfiguration) -> InputBackendSelection {
    parse_backend_value(resolve_backend_value(
        config,
        config.pointer_backend.as_deref(),
    ))
}

fn keyboard_backend_selection(config: &ClientConfiguration) -> InputBackendSelection {
    parse_backend_value(resolve_backend_value(
        config,
        config.keyboard_backend.as_deref(),
    ))
}

pub struct WeylusClientHandler<S, R, FnUInput> {
    sender: S,
    receiver: Option<R>,
    video_sender: mpsc::Sender<VideoCommands>,
    pointer_device: Option<Box<dyn InputDevice>>,
    keyboard_device: Option<Box<dyn InputDevice>>,
    keyboard_shares_pointer: bool,
    capturables: Vec<Box<dyn Capturable>>,
    on_uinput_inaccessible: FnUInput,
    config: WeylusClientConfig,
    #[cfg(target_os = "linux")]
    capture_cursor: bool,
    client_name: Option<String>,
    video_thread: JoinHandle<()>,
}

#[derive(Clone)]
pub struct WeylusClientConfig {
    pub encoder_options: EncoderOptions,
    #[cfg(target_os = "linux")]
    pub wayland_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_support: bool,
    #[cfg(target_os = "linux")]
    pub kms_device: Option<String>,
    #[cfg(target_os = "linux")]
    pub nvfbc_support: bool,
    pub no_gui: bool,
}

impl<S, R, FnUInput> WeylusClientHandler<S, R, FnUInput> {
    pub fn new(
        sender: S,
        receiver: R,
        on_uinput_inaccessible: FnUInput,
        config: WeylusClientConfig,
    ) -> Self
    where
        R: WeylusReceiver,
        S: WeylusSender + Clone + Send + Sync + 'static,
    {
        let (video_sender, video_receiver) = mpsc::channel::<VideoCommands>();
        let video_thread = {
            let sender = sender.clone();
            // offload creating the videostream to another thread to avoid blocking the thread that
            // is receiving messages from the websocket
            spawn(move || handle_video(video_receiver, sender, config.encoder_options))
        };

        Self {
            sender,
            receiver: Some(receiver),
            video_sender,
            pointer_device: None,
            keyboard_device: None,
            keyboard_shares_pointer: false,
            capturables: vec![],
            on_uinput_inaccessible,
            config,
            #[cfg(target_os = "linux")]
            capture_cursor: false,
            client_name: None,
            video_thread,
        }
    }

    pub fn run(mut self)
    where
        R: WeylusReceiver,
        S: WeylusSender + Clone + Send + Sync + 'static,
        FnUInput: Fn(),
    {
        self.send_message(MessageOutbound::EncoderCapabilities(encoder_capabilities(
            self.config.encoder_options,
        )));
        self.send_message(MessageOutbound::InputCapabilities(input_capabilities()));
        for message in self.receiver.take().unwrap() {
            match message {
                Ok(message) => {
                    trace!("Received message: {message:?}");
                    match message {
                        MessageInbound::PointerEvent(event) => self.process_pointer_event(&event),
                        MessageInbound::WheelEvent(event) => self.process_wheel_event(&event),
                        MessageInbound::KeyboardEvent(event) => self.process_keyboard_event(&event),
                        MessageInbound::TextInputEvent(event) => {
                            self.process_text_input_event(&event)
                        }
                        MessageInbound::ReleaseKeyboard => self.release_keyboard(),
                        MessageInbound::GetCapturableList => self.send_capturable_list(),
                        MessageInbound::Config(config) => self.update_config(config),
                        MessageInbound::PauseVideo => {
                            self.video_sender.send(VideoCommands::Pause).unwrap()
                        }
                        MessageInbound::ResumeVideo => {
                            self.video_sender.send(VideoCommands::Resume).unwrap()
                        }
                        MessageInbound::RestartVideo => {
                            self.video_sender.send(VideoCommands::Restart).unwrap()
                        }
                        MessageInbound::ChooseCustomInputAreas => {
                            let (sender, receiver) = std::sync::mpsc::channel();
                            crate::gui::get_input_area(self.config.no_gui, sender);
                            let mut sender = self.sender.clone();
                            spawn(move || {
                                while let Ok(areas) = receiver.recv() {
                                    send_message(
                                        &mut sender,
                                        MessageOutbound::CustomInputAreas(areas),
                                    );
                                }
                            });
                        }
                    }
                }
                Err(err) => {
                    warn!("Failed to read message {err}!");
                    self.send_message(MessageOutbound::Error(
                        "Failed to read message!".to_string(),
                    ));
                }
            }
        }

        drop(self.video_sender);
        if let Err(err) = self.video_thread.join() {
            warn!("Failed to join video thread: {err:?}");
        }
    }

    fn send_message(&mut self, message: MessageOutbound)
    where
        S: WeylusSender,
    {
        send_message(&mut self.sender, message)
    }

    fn keyboard_device_mut(&mut self) -> Option<&mut Box<dyn InputDevice>> {
        if self.keyboard_shares_pointer {
            self.pointer_device.as_mut()
        } else {
            self.keyboard_device.as_mut()
        }
    }

    fn process_wheel_event(&mut self, event: &WheelEvent)
    where
        S: WeylusSender,
    {
        match &mut self.pointer_device {
            Some(i) => {
                i.send_wheel_event(event);
                let mut statuses = i.drain_keyboard_status();
                statuses.insert(
                    0,
                    format!("agent recv wheel dx={} dy={}", event.dx, event.dy),
                );
                for status in statuses {
                    send_pointer_status(&mut self.sender, status);
                }
            }
            None => warn!("Pointer device is not initalized, can not process WheelEvent!"),
        }
    }

    fn process_pointer_event(&mut self, event: &PointerEvent)
    where
        S: WeylusSender,
    {
        if self.pointer_device.is_some() {
            self.pointer_device
                .as_mut()
                .unwrap()
                .send_pointer_event(event);
            if !matches!(event.event_type, crate::protocol::PointerEventType::MOVE) {
                send_pointer_status(
                    &mut self.sender,
                    format!(
                        "agent recv {:?} type={:?} button={:?} buttons={:?} x={:.4} y={:.4}",
                        event.event_type,
                        event.pointer_type,
                        event.button,
                        event.buttons,
                        event.x,
                        event.y
                    ),
                );
            }
            for status in self
                .pointer_device
                .as_mut()
                .unwrap()
                .drain_keyboard_status()
            {
                send_pointer_status(&mut self.sender, status);
            }
        } else {
            warn!("Pointer device is not initalized, can not process PointerEvent!");
        }
    }

    fn process_keyboard_event(&mut self, event: &KeyboardEvent)
    where
        S: WeylusSender,
    {
        if self.keyboard_device_mut().is_some() {
            let keyboard_backend = format!(
                "agent recv {} code={} key={}",
                match event.event_type {
                    crate::protocol::KeyboardEventType::DOWN => "down",
                    crate::protocol::KeyboardEventType::UP => "up",
                    crate::protocol::KeyboardEventType::REPEAT => "repeat",
                },
                event.code,
                event.key
            );
            let mut statuses = self.keyboard_device_mut().unwrap().drain_keyboard_status();
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                self.keyboard_device_mut()
                    .unwrap()
                    .send_keyboard_event(event)
            }));
            if result.is_err() {
                warn!(
                    "Keyboard input handler panicked; event skipped: code={} key={}",
                    event.code, event.key
                );
            }
            statuses.insert(0, keyboard_backend);
            statuses.append(&mut self.keyboard_device_mut().unwrap().drain_keyboard_status());
            for status in statuses {
                self.send_message(MessageOutbound::RuntimeStatus(RuntimeStatus {
                    capture_backend: None,
                    encoder_backend: None,
                    input_backend: None,
                    pointer_backend: None,
                    keyboard_backend: Some(status),
                }));
            }
        } else {
            warn!("Keyboard device is not initalized, can not process KeyboardEvent!");
        }
    }

    fn process_text_input_event(&mut self, event: &TextInputEvent)
    where
        S: WeylusSender,
    {
        if self.keyboard_device_mut().is_some() {
            let text_preview: String = event.text.chars().take(16).collect();
            let keyboard_backend = format!(
                "agent recv text len={} text={:?}",
                event.text.chars().count(),
                text_preview
            );
            let mut statuses = self.keyboard_device_mut().unwrap().drain_keyboard_status();
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                self.keyboard_device_mut()
                    .unwrap()
                    .send_text_input_event(event)
            }));
            if result.is_err() {
                warn!("Text input handler panicked; event skipped");
            }
            statuses.insert(0, keyboard_backend);
            statuses.append(&mut self.keyboard_device_mut().unwrap().drain_keyboard_status());
            for status in statuses {
                self.send_message(MessageOutbound::RuntimeStatus(RuntimeStatus {
                    capture_backend: None,
                    encoder_backend: None,
                    input_backend: None,
                    pointer_backend: None,
                    keyboard_backend: Some(status),
                }));
            }
        } else {
            warn!("Keyboard device is not initalized, can not process TextInputEvent!");
        }
    }

    fn release_keyboard(&mut self)
    where
        S: WeylusSender,
    {
        if let Some(device) = self.keyboard_device_mut() {
            device.release_keyboard();
            let mut statuses = device.drain_keyboard_status();
            if statuses.is_empty() {
                statuses.push("keyboard release requested".to_string());
            }
            for status in statuses {
                self.send_message(MessageOutbound::RuntimeStatus(RuntimeStatus {
                    capture_backend: None,
                    encoder_backend: None,
                    input_backend: None,
                    pointer_backend: None,
                    keyboard_backend: Some(status),
                }));
            }
        }
    }

    fn send_capturable_list(&mut self)
    where
        S: WeylusSender,
    {
        let mut windows = Vec::<String>::new();
        self.capturables = get_capturables(
            #[cfg(target_os = "linux")]
            self.config.wayland_support,
            #[cfg(target_os = "linux")]
            self.capture_cursor,
            #[cfg(target_os = "linux")]
            self.config.kms_support,
            #[cfg(target_os = "linux")]
            self.config.kms_device.as_deref(),
            #[cfg(target_os = "linux")]
            self.config.nvfbc_support,
        );
        self.capturables.iter().for_each(|c| {
            windows.push(c.name());
        });
        if windows.is_empty() {
            warn!(
                "No capturables found. DISPLAY={:?}",
                std::env::var("DISPLAY")
            );
        }
        self.send_message(MessageOutbound::CapturableList(windows));
    }

    fn device_slot_mut(&mut self, slot: DeviceSlot) -> &mut Option<Box<dyn InputDevice>> {
        match slot {
            DeviceSlot::Pointer => &mut self.pointer_device,
            DeviceSlot::Keyboard => &mut self.keyboard_device,
        }
    }

    fn device_label(device: &Option<Box<dyn InputDevice>>) -> String {
        device
            .as_ref()
            .map(|device| {
                #[cfg(target_os = "windows")]
                if device.device_type() == InputDeviceType::WindowsInput {
                    return crate::input::autopilot_device_win::input_backend_label();
                }
                device.device_type().label().to_string()
            })
            .unwrap_or_else(|| "不可用".to_string())
    }

    #[cfg(target_os = "macos")]
    fn ensure_autopilot_device(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) {
        let slot_ref = self.device_slot_mut(slot);
        let reuse = slot_ref.as_ref().map_or(false, |device| {
            !client_name_changed && device.device_type() == InputDeviceType::AutoPilotDevice
        });
        if reuse {
            slot_ref.as_mut().unwrap().set_capturable(capturable);
        } else {
            *slot_ref = Some(Box::new(
                crate::input::autopilot_device::AutoPilotDevice::new(capturable),
            ));
        }
    }

    #[cfg(target_os = "windows")]
    fn ensure_windows_device(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) {
        let slot_ref = self.device_slot_mut(slot);
        let reuse = slot_ref.as_ref().map_or(false, |device| {
            !client_name_changed && device.device_type() == InputDeviceType::WindowsInput
        });
        if reuse {
            slot_ref.as_mut().unwrap().set_capturable(capturable);
        } else {
            *slot_ref = Some(Box::new(
                crate::input::autopilot_device_win::WindowsInput::new(capturable),
            ));
        }
    }

    #[cfg(target_os = "linux")]
    fn set_input_device(
        &mut self,
        slot: DeviceSlot,
        device_type: InputDeviceType,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
        create: impl FnOnce(&mut Self, Box<dyn Capturable>) -> Result<Box<dyn InputDevice>, String>,
    ) -> Result<(), String>
    where
        FnUInput: Fn(),
    {
        let reuse = self.device_slot_mut(slot).as_ref().map_or(false, |device| {
            !client_name_changed && device.device_type() == device_type
        });
        if reuse {
            if let Some(device) = self.device_slot_mut(slot).as_mut() {
                device.set_capturable(capturable);
            }
            return Ok(());
        }
        let device = create(self, capturable)?;
        *self.device_slot_mut(slot) = Some(device);
        Ok(())
    }

    #[cfg(target_os = "linux")]
    fn try_select_portal_input(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) -> Result<(), String>
    where
        FnUInput: Fn(),
    {
        #[cfg(feature = "pipewire")]
        {
            if !crate::input::wayland_portal_device::WaylandPortalDevice::supports_capturable(
                capturable.as_ref(),
            ) {
                return Err("selected capturable has no active Wayland portal session".to_string());
            }
            return self.set_input_device(
                slot,
                InputDeviceType::WaylandPortalDevice,
                capturable,
                client_name_changed,
                |_, capturable| try_wayland_portal_input_device(capturable),
            );
        }
        #[cfg(not(feature = "pipewire"))]
        {
            let _ = (slot, capturable, client_name_changed);
            Err("Wayland portal input is not available without the pipewire feature".to_string())
        }
    }

    #[cfg(target_os = "linux")]
    fn try_select_wlroots_pointer_input(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) -> Result<(), String>
    where
        FnUInput: Fn(),
    {
        #[cfg(feature = "pipewire")]
        {
            return self.set_input_device(
                slot,
                InputDeviceType::WlrootsVirtualPointer,
                capturable,
                client_name_changed,
                |_, capturable| try_wlroots_virtual_pointer_device(capturable),
            );
        }
        #[cfg(not(feature = "pipewire"))]
        {
            let _ = (slot, capturable, client_name_changed);
            Err("wlroots virtual pointer is not available without the pipewire feature".to_string())
        }
    }

    #[cfg(target_os = "linux")]
    fn try_select_uinput(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) -> Result<(), String>
    where
        FnUInput: Fn(),
    {
        self.set_input_device(
            slot,
            InputDeviceType::UInputDevice,
            capturable,
            client_name_changed,
            |this, capturable| {
                crate::input::uinput_device::UInputDevice::new(capturable, &this.client_name)
                    .map(|device| Box::new(device) as Box<dyn InputDevice>)
                    .map_err(|err| {
                        if let CErrorCode::UInputNotAccessible = err.to_enum() {
                            (this.on_uinput_inaccessible)();
                        }
                        err.to_string()
                    })
            },
        )
    }

    #[cfg(target_os = "linux")]
    fn try_select_xtest(
        &mut self,
        slot: DeviceSlot,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) -> Result<(), String>
    where
        FnUInput: Fn(),
    {
        self.set_input_device(
            slot,
            InputDeviceType::XTestDevice,
            capturable,
            client_name_changed,
            |_, capturable| {
                crate::input::xtest_device::XTestDevice::new(capturable)
                    .map(|device| Box::new(device) as Box<dyn InputDevice>)
            },
        )
    }

    #[cfg(target_os = "linux")]
    fn select_linux_input_device(
        &mut self,
        slot: DeviceSlot,
        selection: InputBackendSelection,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) -> Result<(), Vec<String>>
    where
        S: WeylusSender,
        FnUInput: Fn(),
    {
        if selection == InputBackendSelection::None {
            *self.device_slot_mut(slot) = None;
            return Ok(());
        }

        let mut errors = Vec::<String>::new();
        let auto_attempts = [
            InputBackendSelection::Portal,
            InputBackendSelection::UInput,
            InputBackendSelection::WlrootsPointer,
            InputBackendSelection::XTest,
        ];
        let selected_attempts = [selection];
        let attempts: &[InputBackendSelection] = match selection {
            InputBackendSelection::Auto => &auto_attempts,
            _ => &selected_attempts,
        };

        for attempt in attempts {
            let result = match *attempt {
                InputBackendSelection::Auto => unreachable!(),
                InputBackendSelection::Platform => {
                    self.try_select_uinput(slot, capturable.clone(), client_name_changed)
                }
                InputBackendSelection::Portal => {
                    self.try_select_portal_input(slot, capturable.clone(), client_name_changed)
                }
                InputBackendSelection::WlrootsPointer => self.try_select_wlroots_pointer_input(
                    slot,
                    capturable.clone(),
                    client_name_changed,
                ),
                InputBackendSelection::UInput => {
                    self.try_select_uinput(slot, capturable.clone(), client_name_changed)
                }
                InputBackendSelection::XTest => {
                    self.try_select_xtest(slot, capturable.clone(), client_name_changed)
                }
                InputBackendSelection::AutoPilot => Err("AutoPilot input is not available in the embedded Linux build".to_string()),
                InputBackendSelection::None => unreachable!(),
            };

            match result {
                Ok(()) => return Ok(()),
                Err(err) => {
                    debug!("Input backend {:?} unavailable: {}", attempt, err);
                    errors.push(format!("{attempt:?}: {err}"));
                }
            }
        }

        *self.device_slot_mut(slot) = None;
        Err(errors)
    }

    #[cfg(target_os = "macos")]
    fn select_macos_input_device(
        &mut self,
        slot: DeviceSlot,
        selection: InputBackendSelection,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) {
        match selection {
            InputBackendSelection::None => *self.device_slot_mut(slot) = None,
            InputBackendSelection::Auto
            | InputBackendSelection::Platform
            | InputBackendSelection::AutoPilot => {
                self.ensure_autopilot_device(slot, capturable, client_name_changed);
            }
            other => {
                warn!("Input backend {:?} is not supported on macOS", other);
                *self.device_slot_mut(slot) = None;
            }
        }
    }

    #[cfg(target_os = "windows")]
    fn select_windows_input_device(
        &mut self,
        slot: DeviceSlot,
        selection: InputBackendSelection,
        capturable: Box<dyn Capturable>,
        client_name_changed: bool,
    ) {
        match selection {
            InputBackendSelection::None => *self.device_slot_mut(slot) = None,
            InputBackendSelection::Auto | InputBackendSelection::Platform => {
                self.ensure_windows_device(slot, capturable, client_name_changed);
            }
            other => {
                warn!("Input backend {:?} is not supported on Windows", other);
                *self.device_slot_mut(slot) = None;
            }
        }
    }

    fn update_config(&mut self, config: ClientConfiguration)
    where
        S: WeylusSender,
        FnUInput: Fn(),
    {
        let client_name_changed = if self.client_name != config.client_name {
            self.client_name = config.client_name.clone();
            true
        } else {
            false
        };
        let mut capturable_id = config.capturable_id;
        if capturable_id >= self.capturables.len() {
            warn!(
                "Got invalid id for capturable: {}, current list has {} entries; refreshing list and falling back to 0.",
                capturable_id,
                self.capturables.len()
            );
            self.send_capturable_list();
            if self.capturables.is_empty() {
                self.send_message(MessageOutbound::ConfigError(format!(
                    "No capturable display is available. DISPLAY={:?}",
                    std::env::var("DISPLAY")
                )));
                return;
            }
            capturable_id = 0;
        }

        if capturable_id < self.capturables.len() {
            let capturable = self.capturables[capturable_id].clone();
            let pointer_selection = pointer_backend_selection(&config);
            let keyboard_selection = keyboard_backend_selection(&config);
            let share_devices = pointer_selection == keyboard_selection;
            info!(
                "Client config: capturable_id={} pointer_backend={} keyboard_backend={} capture_cursor={} max={}x{} frame_rate={:.1} encoder={} client_name={}",
                capturable_id,
                config
                    .pointer_backend
                    .as_deref()
                    .or(config.input_backend.as_deref())
                    .unwrap_or("auto"),
                config
                    .keyboard_backend
                    .as_deref()
                    .or(config.input_backend.as_deref())
                    .unwrap_or("auto"),
                config.capture_cursor,
                config.max_width,
                config.max_height,
                config.frame_rate,
                config.encoder.as_deref().unwrap_or("auto"),
                self.client_name.as_deref().unwrap_or("")
            );
            debug!(
                "Selected capturable[{}]: {}",
                capturable_id,
                capturable.name()
            );

            // When both pointer and keyboard request the same backend, drive them through a single
            // shared device. This avoids creating two backends (e.g. two Wayland portal prompts or
            // two uinput devices) for the common case where the whole input stack is unified.
            self.keyboard_shares_pointer = share_devices;

            #[cfg(target_os = "linux")]
            {
                self.capture_cursor = config.capture_cursor;
            }

            #[cfg(target_os = "linux")]
            {
                if let Err(errors) = self.select_linux_input_device(
                    DeviceSlot::Pointer,
                    pointer_selection,
                    capturable.clone(),
                    client_name_changed,
                ) {
                    warn!(
                        "Pointer input disabled: no usable Linux input backend ({})",
                        errors.join("; ")
                    );
                    self.send_message(MessageOutbound::Error(format!(
                        "Pointer input disabled: no usable Linux input backend ({})",
                        errors.join("; ")
                    )));
                }
                if share_devices {
                    self.keyboard_device = None;
                } else if let Err(errors) = self.select_linux_input_device(
                    DeviceSlot::Keyboard,
                    keyboard_selection,
                    capturable.clone(),
                    client_name_changed,
                ) {
                    warn!(
                        "Keyboard input disabled: no usable Linux input backend ({})",
                        errors.join("; ")
                    );
                    self.send_message(MessageOutbound::Error(format!(
                        "Keyboard input disabled: no usable Linux input backend ({})",
                        errors.join("; ")
                    )));
                }
            }

            #[cfg(target_os = "macos")]
            {
                self.select_macos_input_device(
                    DeviceSlot::Pointer,
                    pointer_selection,
                    capturable.clone(),
                    client_name_changed,
                );
                if share_devices {
                    self.keyboard_device = None;
                } else {
                    self.select_macos_input_device(
                        DeviceSlot::Keyboard,
                        keyboard_selection,
                        capturable.clone(),
                        client_name_changed,
                    );
                }
            }

            #[cfg(target_os = "windows")]
            {
                self.select_windows_input_device(
                    DeviceSlot::Pointer,
                    pointer_selection,
                    capturable.clone(),
                    client_name_changed,
                );
                if share_devices {
                    self.keyboard_device = None;
                } else {
                    self.select_windows_input_device(
                        DeviceSlot::Keyboard,
                        keyboard_selection,
                        capturable.clone(),
                        client_name_changed,
                    );
                }
            }

            let pointer_backend = Self::device_label(&self.pointer_device);
            let keyboard_backend = if share_devices {
                pointer_backend.clone()
            } else {
                Self::device_label(&self.keyboard_device)
            };
            info!("Input backend selected: pointer={pointer_backend} keyboard={keyboard_backend}");
            let combined = if share_devices {
                pointer_backend.clone()
            } else {
                format!("指针 {pointer_backend} / 键盘 {keyboard_backend}")
            };
            send_input_status(&mut self.sender, &combined);
            send_pointer_status(&mut self.sender, format!("backend {pointer_backend}"));
            self.send_message(MessageOutbound::RuntimeStatus(RuntimeStatus {
                capture_backend: None,
                encoder_backend: None,
                input_backend: None,
                pointer_backend: None,
                keyboard_backend: Some(format!("backend {keyboard_backend}")),
            }));

            if let Err(err) = self.video_sender.send(VideoCommands::Start(VideoConfig {
                capturable,
                capture_cursor: config.capture_cursor,
                max_width: config.max_width,
                max_height: config.max_height,
                frame_rate: config.frame_rate,
                encoder: config.encoder.clone(),
            })) {
                warn!("Failed to start video thread: {}", err);
                self.send_message(MessageOutbound::ConfigError(
                    "Video worker is not available; reconnect to restart the desktop session."
                        .to_string(),
                ));
            }
        }
    }
}

fn handle_video<S: WeylusSender + Clone + 'static>(
    receiver: mpsc::Receiver<VideoCommands>,
    mut sender: S,
    encoder_options: EncoderOptions,
) {
    const EFFECTIVE_INIFINITY: Duration = Duration::from_secs(3600 * 24 * 365 * 200);

    let mut recorder: Option<Box<dyn Recorder>> = None;
    let mut video_encoder: Option<Box<VideoEncoder>> = None;
    let mut active_encoder_options = encoder_options;
    let mut capture_backend = "未启动".to_string();
    let mut encoder_backend = "未启动".to_string();
    let mut drm_prime_enabled = false;

    let mut max_width = 1920;
    let mut max_height = 1080;
    let mut frame_duration = EFFECTIVE_INIFINITY;
    let mut last_frame = Instant::now();
    let mut paused = false;

    loop {
        let now = Instant::now();
        let elapsed = now - last_frame;
        let frames_passed = (elapsed.as_secs_f64() / frame_duration.as_secs_f64()) as u32;
        let next_frame = last_frame + (frames_passed + 1) * frame_duration;
        let timeout = next_frame - now;
        last_frame = next_frame;

        if frames_passed > 0 {
            trace!("Dropped {frames_passed} frame(s)!");
        }

        match receiver.recv_timeout(if paused { EFFECTIVE_INIFINITY } else { timeout }) {
            Ok(VideoCommands::Start(config)) => {
                #[allow(unused_assignments)]
                {
                    // gstpipewire can not handle setting a pipeline's state to Null after another
                    // pipeline has been created and its state has been set to Play.
                    // This line makes sure that there always is only a single recorder and thus
                    // single pipeline in this thread by forcing rust to call the destructor of the
                    // current pipeline here, right before creating a new pipeline.
                    // See: https://gitlab.freedesktop.org/pipewire/pipewire/-/issues/986
                    //
                    // This shouldn't affect other Recorder trait objects.
                    recorder = None;
                }
                const MAX_RETRIES: u32 = 5;
                const RETRY_DELAY: Duration = Duration::from_millis(500);
                active_encoder_options =
                    encoder_options_for_selection(encoder_options, config.encoder.as_deref());
                drm_prime_enabled = active_encoder_options.try_vaapi;
                let mut result = recorder_for_capturable(
                    config.capturable.as_ref(),
                    config.capture_cursor,
                    RecorderPreferences {
                        prefer_drm_prime: drm_prime_enabled,
                    },
                );
                for attempt in 1..MAX_RETRIES {
                    if result.is_ok() {
                        break;
                    }
                    warn!(
                        "Failed to init screen cast (attempt {}/{}), retrying...",
                        attempt, MAX_RETRIES
                    );
                    std::thread::sleep(RETRY_DELAY);
                    result = recorder_for_capturable(
                        config.capturable.as_ref(),
                        config.capture_cursor,
                        RecorderPreferences {
                            prefer_drm_prime: drm_prime_enabled,
                        },
                    );
                }
                match result {
                    Ok(mut r) => {
                        capture_backend = r.backend_name().to_string();
                        encoder_backend = "等待视频流".to_string();
                        r.set_preferences(RecorderPreferences {
                            prefer_drm_prime: drm_prime_enabled,
                        });
                        recorder = Some(r);
                        video_encoder = None;
                        max_width = config.max_width;
                        max_height = config.max_height;
                        send_message(&mut sender, MessageOutbound::ConfigOk);
                        send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                    }
                    Err(err) => {
                        capture_backend = "初始化失败".to_string();
                        encoder_backend = "未启动".to_string();
                        warn!("Failed to init screen cast: {}!", err);
                        send_message(
                            &mut sender,
                            MessageOutbound::Error(format!("Failed to init screen cast: {err}!")),
                        );
                        send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                    }
                }
                last_frame = Instant::now();

                // The Duration type can not handle infinity, if the frame rate is set to 0 we just
                // set the duration between two frames to a very long one, which is effectively
                // infinity.
                let d = 1.0 / config.frame_rate;
                frame_duration = if d.is_finite() {
                    Duration::from_secs_f64(d)
                } else {
                    EFFECTIVE_INIFINITY
                };
                frame_duration = frame_duration.min(EFFECTIVE_INIFINITY);
            }
            Ok(VideoCommands::Pause) => {
                paused = true;
            }
            Ok(VideoCommands::Resume) => {
                paused = false;
            }
            Ok(VideoCommands::Restart) => {
                video_encoder = None;
                encoder_backend = "重启中".to_string();
                active_encoder_options = encoder_options;
                drm_prime_enabled = active_encoder_options.try_vaapi;
                if let Some(recorder) = recorder.as_mut() {
                    recorder.set_preferences(RecorderPreferences {
                        prefer_drm_prime: drm_prime_enabled,
                    });
                }
                send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
            }
            Err(RecvTimeoutError::Timeout) => {
                if recorder.is_none() {
                    warn!("Screen capture not initalized, can not send video frame!");
                    continue;
                }
                let frame = recorder.as_mut().unwrap().capture_frame();
                if let Err(err) = frame {
                    warn!("Error capturing screen: {}", err);
                    continue;
                }
                let frame = frame.unwrap();
                let is_drm_prime_frame = frame.is_drm_prime();
                let (width_in, height_in) = frame.size();
                let scale =
                    (max_width as f64 / width_in as f64).min(max_height as f64 / height_in as f64);
                // limit video to 4K
                let scale_max = (3840.0 / width_in as f64).min(2160.0 / height_in as f64);
                let scale = scale.min(scale_max);
                let mut width_out = width_in;
                let mut height_out = height_in;
                if scale < 1.0 {
                    width_out = (width_out as f64 * scale) as usize;
                    height_out = (height_out as f64 * scale) as usize;
                }
                let mut disable_drm_prime_after_frame = false;
                // video encoder is not setup or setup for encoding the wrong size: restart it
                if video_encoder.is_none()
                    || !video_encoder
                        .as_ref()
                        .unwrap()
                        .check_size(width_in, height_in, width_out, height_out)
                {
                    send_message(&mut sender, MessageOutbound::NewVideo);
                    let mut video_sender = sender.clone();
                    let res = VideoEncoder::new(
                        width_in,
                        height_in,
                        width_out,
                        height_out,
                        move |data| {
                            if let Err(err) = video_sender.send_video(data) {
                                warn!("Failed to send video frame: {err}!");
                            }
                        },
                        active_encoder_options,
                    );
                    match res {
                        Ok(r) => {
                            encoder_backend = r.codec_name();
                            if drm_prime_enabled && !r.supports_drm_prime() {
                                debug!(
                                    "DRM PRIME zero-copy is not supported by the active encoder, disabling it for this session."
                                );
                                drm_prime_enabled = false;
                                disable_drm_prime_after_frame = true;
                            }
                            video_encoder = Some(r);
                            send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                        }
                        Err(e) => {
                            encoder_backend = "初始化失败".to_string();
                            send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                            warn!("{}", e);
                            continue;
                        }
                    };
                }
                if disable_drm_prime_after_frame && is_drm_prime_frame {
                    drop(frame);
                    if let Some(recorder) = recorder.as_mut() {
                        recorder.set_preferences(RecorderPreferences {
                            prefer_drm_prime: false,
                        });
                    }
                    continue;
                }
                let encode_result = video_encoder.as_mut().unwrap().encode(frame);
                if encode_result.is_ok() {
                    let current_backend = video_encoder.as_ref().unwrap().codec_name();
                    if current_backend != encoder_backend {
                        encoder_backend = current_backend;
                        send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                    }
                }
                if disable_drm_prime_after_frame {
                    if let Some(recorder) = recorder.as_mut() {
                        recorder.set_preferences(RecorderPreferences {
                            prefer_drm_prime: false,
                        });
                    }
                }
                if let Err(err) = encode_result {
                    if is_drm_prime_frame && drm_prime_enabled {
                        warn!(
                            "DRM PRIME zero-copy encoding failed, disabling it for this session: {}",
                            err
                        );
                        drm_prime_enabled = false;
                        if let Some(recorder) = recorder.as_mut() {
                            recorder.set_preferences(RecorderPreferences {
                                prefer_drm_prime: false,
                            });
                        }
                        video_encoder = None;
                        encoder_backend = "zero-copy降级".to_string();
                        send_runtime_status(&mut sender, &capture_backend, &encoder_backend);
                    }
                }
            }
            // stop thread once the channel is closed
            Err(RecvTimeoutError::Disconnected) => return,
        };
    }
}

fn recorder_for_capturable(
    capturable: &dyn Capturable,
    capture_cursor: bool,
    preferences: RecorderPreferences,
) -> Result<Box<dyn Recorder>, Box<dyn std::error::Error>> {
    catch_unwind(AssertUnwindSafe(|| {
        capturable.recorder_with_preferences(capture_cursor, preferences)
    }))
    .map_err(|_| "screen recorder initialization panicked".into())
    .and_then(|result| result)
}

pub struct WsWeylusReceiver {
    recv: tokio::sync::mpsc::Receiver<MessageInbound>,
}

impl Iterator for WsWeylusReceiver {
    type Item = Result<MessageInbound, Infallible>;

    fn next(&mut self) -> Option<Self::Item> {
        self.recv.blocking_recv().map(Ok)
    }
}

impl WeylusReceiver for WsWeylusReceiver {
    type Error = Infallible;
}

pub enum WsMessage {
    Frame(Frame<'static>),
    Video(Vec<u8>),
    MessageOutbound(MessageOutbound),
}

unsafe impl Send for WsMessage {}

#[derive(Clone)]
pub struct WsWeylusSender {
    sender: tokio::sync::mpsc::Sender<WsMessage>,
}

impl WeylusSender for WsWeylusSender {
    type Error = tokio::sync::mpsc::error::SendError<WsMessage>;

    fn send_message(&mut self, message: MessageOutbound) -> Result<(), Self::Error> {
        self.sender
            .blocking_send(WsMessage::MessageOutbound(message))
    }

    fn send_video(&mut self, bytes: &[u8]) -> Result<(), Self::Error> {
        self.sender.blocking_send(WsMessage::Video(bytes.to_vec()))
    }
}

pub fn weylus_websocket_channel(
    websocket: WebSocket<TokioIo<Upgraded>>,
    semaphore_shutdown: Arc<tokio::sync::Semaphore>,
) -> (WsWeylusSender, WsWeylusReceiver) {
    let (rx, mut tx) = websocket.split(|ws| tokio::io::split(ws));

    let mut rx = FragmentCollectorRead::new(rx);

    let (sender_inbound, receiver_inbound) = channel::<MessageInbound>(32);
    let (sender_outbound, mut receiver_outbound) = channel::<WsMessage>(32);

    {
        let sender_outbound = sender_outbound.clone();
        tokio::spawn(async move {
            let mut send_fn = |frame| async {
                if let Err(err) = sender_outbound.send(WsMessage::Frame(frame)).await {
                    warn!("Failed to send websocket frame while receiving fragmented frame: {err}.")
                };
                Ok(())
            };

            loop {
                let fut = rx.read_frame::<_, WebSocketError>(&mut send_fn);

                let frame = tokio::select! {
                    _ = semaphore_shutdown.acquire() => break,
                    frame = fut => match frame {
                        Ok(frame) => frame,
                        Err(err) => {
                            warn!("Invalid websocket frame: {err}.");
                            break;
                        },
                    },
                };
                match frame.opcode {
                    OpCode::Close => break,
                    OpCode::Text => match serde_json::from_slice(&frame.payload) {
                        Ok(msg) => {
                            if let Err(err) = sender_inbound.send(msg).await {
                                warn!("Failed to forward inbound message to WeylusClientHandler: {err}.");
                            }
                        }
                        Err(err) => warn!("Failed to parse message: {err}"),
                    },
                    _ => {}
                }
            }
        });
    }

    tokio::spawn(async move {
        loop {
            let msg = if let Some(msg) = receiver_outbound.recv().await {
                msg
            } else {
                break;
            };

            match msg {
                WsMessage::Frame(frame) => {
                    if let Err(err) = tx.write_frame(frame).await {
                        if let WebSocketError::ConnectionClosed = err {
                            break;
                        }
                        warn!("Failed to send frame: {err}");
                    }
                }
                WsMessage::Video(data) => {
                    if let Err(err) = tx.write_frame(Frame::binary(data.into())).await {
                        if let WebSocketError::ConnectionClosed = err {
                            break;
                        }
                        warn!("Failed to send video frame: {err}");
                    }
                }
                WsMessage::MessageOutbound(msg) => {
                    let json_string = serde_json::to_string(&msg).unwrap();
                    let data = json_string.as_bytes();
                    if let Err(err) = tx.write_frame(Frame::text(data.into())).await {
                        if let WebSocketError::ConnectionClosed = err {
                            break;
                        }
                        warn!("Failed to send outbound message: {err}");
                    }
                }
            }
        }
    });

    (
        WsWeylusSender {
            sender: sender_outbound,
        },
        WsWeylusReceiver {
            recv: receiver_inbound,
        },
    )
}
