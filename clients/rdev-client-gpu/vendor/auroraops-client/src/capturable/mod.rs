use std::any::Any;
use std::boxed::Box;
use std::error::Error;
use tracing::{debug, warn};

#[cfg(target_os = "macos")]
pub mod core_graphics;
#[cfg(target_os = "linux")]
pub mod kms;
#[cfg(all(target_os = "linux", feature = "nvfbc"))]
pub mod nvfbc_capture;
#[cfg(all(target_os = "linux", feature = "pipewire"))]
pub mod pipewire;
#[cfg(target_os = "linux")]
#[allow(dead_code)]
pub mod remote_desktop_dbus;
pub mod testsrc;

#[cfg(target_os = "windows")]
pub mod captrs_capture;
#[cfg(target_os = "windows")]
pub mod win_ctx;
#[cfg(target_os = "linux")]
pub mod x11;
pub trait Recorder {
    fn backend_name(&self) -> &'static str {
        "未知"
    }

    fn capture(&mut self) -> Result<crate::video::PixelProvider<'_>, Box<dyn Error>>;

    fn set_preferences(&mut self, _preferences: RecorderPreferences) {}

    fn capture_frame(&mut self) -> Result<crate::video::CapturedFrame<'_>, Box<dyn Error>> {
        Ok(crate::video::CapturedFrame::Cpu(self.capture()?))
    }
}

#[derive(Clone, Copy, Debug, Default)]
pub struct RecorderPreferences {
    pub prefer_drm_prime: bool,
}

pub trait BoxCloneCapturable {
    fn box_clone(&self) -> Box<dyn Capturable>;
}

impl<T> BoxCloneCapturable for T
where
    T: Clone + Capturable + 'static,
{
    fn box_clone(&self) -> Box<dyn Capturable> {
        Box::new(self.clone())
    }
}
/// Relative: x, y, width, height of the Capturable as floats relative to the absolute size of the
/// screen. For example x=0.5, y=0.0, width=0.5, height=1.0 means the right half of the screen.
/// VirtualScreen: offset_x, offset_y, width, height for a capturable using a virtual screen. (Windows)
pub enum Geometry {
    Relative(f64, f64, f64, f64),
    #[cfg(target_os = "windows")]
    VirtualScreen(i32, i32, u32, u32, i32, i32),
}

pub trait Capturable: Send + BoxCloneCapturable {
    fn as_any(&self) -> &dyn Any;

    /// Name of the Capturable, for example the window title, if it is a window.
    fn name(&self) -> String;

    /// Return Geometry of the Capturable.
    fn geometry(&self) -> Result<Geometry, Box<dyn Error>>;

    /// Callback that is called right before input is simulated.
    /// Useful to focus the window on input.
    fn before_input(&mut self) -> Result<(), Box<dyn Error>>;

    /// Return a Recorder that can record the current capturable.
    fn recorder(&self, capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>>;

    fn recorder_with_preferences(
        &self,
        capture_cursor: bool,
        preferences: RecorderPreferences,
    ) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        let mut recorder = self.recorder(capture_cursor)?;
        recorder.set_preferences(preferences);
        Ok(recorder)
    }
}

impl Clone for Box<dyn Capturable> {
    fn clone(&self) -> Self {
        self.box_clone()
    }
}

pub fn get_capturables(
    #[cfg(target_os = "linux")] wayland_support: bool,
    #[cfg(target_os = "linux")] capture_cursor: bool,
    #[cfg(target_os = "linux")] kms_support: bool,
    #[cfg(target_os = "linux")] kms_device: Option<&str>,
    #[cfg(target_os = "linux")] nvfbc_support: bool,
) -> Vec<Box<dyn Capturable>> {
    let mut capturables: Vec<Box<dyn Capturable>> = vec![];
    #[cfg(target_os = "linux")]
    {
        let _ = capture_cursor;
        #[cfg(feature = "nvfbc")]
        if nvfbc_support {
            use crate::capturable::nvfbc_capture::get_capturables as get_capturables_nvfbc;
            match get_capturables_nvfbc() {
                Ok(captrs) => {
                    for c in captrs {
                        capturables.push(Box::new(c));
                    }
                }
                Err(err) => warn!("Failed to get list of capturables via NvFBC: {}", err),
            }
        }
        #[cfg(not(feature = "nvfbc"))]
        if nvfbc_support {
            warn!("NvFBC capture not available (built without 'nvfbc' feature)");
        }

        #[cfg(feature = "pipewire")]
        if wayland_support {
            use crate::capturable::pipewire::get_capturables as get_capturables_pw;
            match get_capturables_pw(capture_cursor) {
                Ok(captrs) => {
                    for c in captrs {
                        capturables.push(Box::new(c));
                    }
                }
                Err(err) => warn!(
                    "Failed to get list of capturables via dbus/pipewire: {}",
                    err
                ),
            }
        }
        #[cfg(not(feature = "pipewire"))]
        if wayland_support {
            warn!("Wayland/PipeWire capture not available (built without 'pipewire' feature)");
        }

        if kms_support {
            use crate::capturable::kms::get_capturables as get_capturables_kms;
            match get_capturables_kms(kms_device) {
                Ok(captrs) => {
                    for c in captrs {
                        capturables.push(Box::new(c));
                    }
                }
                Err(err) => warn!("Failed to get list of capturables via KMS: {}", err),
            }
        }

        use crate::capturable::x11::X11Context;
        let x11ctx = X11Context::new();
        if let Some(mut x11ctx) = x11ctx {
            match x11ctx.capturables() {
                Ok(captrs) => {
                    for c in captrs {
                        capturables.push(Box::new(c));
                    }
                }
                Err(err) => warn!("Failed to get list of capturables via X11: {}", err),
            }
        };
    }

    #[cfg(target_os = "macos")]
    {
        use crate::capturable::core_graphics::get_displays as get_displays_cg;
        use crate::capturable::core_graphics::get_windows as get_windows_cg;
        match get_displays_cg() {
            Ok(captrs) => {
                for c in captrs {
                    capturables.push(Box::new(c));
                }
            }
            Err(err) => warn!("Failed to get list of displays via CoreGraphics: {}", err),
        }

        match get_windows_cg() {
            Ok(mut captrs) => {
                captrs.sort_by(|a, b| a.name().to_lowercase().cmp(&b.name().to_lowercase()));
                for c in captrs {
                    capturables.push(Box::new(c));
                }
            }
            Err(err) => warn!("Failed to get list of windows via CoreGraphics: {}", err),
        }
    }

    #[cfg(target_os = "windows")]
    {
        use crate::capturable::captrs_capture::{
            get_window_capturables, DesktopCapturable, WindowsCaptureSource,
        };
        use crate::capturable::win_ctx::WinCtx;
        let configured_source = std::env::var("AURORAOPS_WINDOWS_CAPTURE")
            .map(|value| WindowsCaptureSource::parse(&value))
            .unwrap_or(WindowsCaptureSource::Auto);
        let sources: Vec<WindowsCaptureSource> = if configured_source == WindowsCaptureSource::Auto
            && crate::input::autopilot_device_win::is_input_desktop_winlogon()
        {
            vec![WindowsCaptureSource::Gdi]
        } else {
            match configured_source {
                WindowsCaptureSource::Auto => vec![
                    WindowsCaptureSource::Auto,
                    WindowsCaptureSource::Dxgi,
                    WindowsCaptureSource::Gdi,
                ],
                source => vec![source],
            }
        };
        let winctx = WinCtx::new();
        for output in winctx.get_capture_outputs() {
            let name_len = output
                .desc
                .DeviceName
                .iter()
                .position(|ch| *ch == 0)
                .unwrap_or(output.desc.DeviceName.len());
            let name = String::from_utf16_lossy(&output.desc.DeviceName[..name_len]);
            for source in &sources {
                let captr = DesktopCapturable::new(
                    output.capture_id,
                    name.clone(),
                    output.desc.DesktopCoordinates,
                    winctx.get_union_rect().clone(),
                    *source,
                );
                capturables.push(Box::new(captr));
            }
        }
        let mut windows = get_window_capturables(winctx.get_union_rect().clone());
        windows.sort_by(|a, b| a.name().to_lowercase().cmp(&b.name().to_lowercase()));
        for window in windows {
            capturables.push(Box::new(window));
        }
    }

    if crate::log::get_log_level() >= tracing::Level::DEBUG {
        for (width, height) in [
            (200, 200),
            (800, 600),
            (1080, 720),
            (1920, 1080),
            (3840, 2160),
            (15360, 2160),
        ]
        .iter()
        {
            use testsrc::PixelFormat;
            for pixel_format in [PixelFormat::BGR0, PixelFormat::RGB0, PixelFormat::RGB] {
                capturables.push(Box::new(testsrc::TestCapturable {
                    width: *width,
                    height: *height,
                    pixel_format,
                }));
            }
        }
    }

    if crate::log::get_log_level() >= tracing::Level::DEBUG {
        for (index, capturable) in capturables.iter().enumerate() {
            debug!("Capturable[{index}]: {}", capturable.name());
        }
    }

    capturables
}
