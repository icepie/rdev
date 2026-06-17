use crate::capturable::{Capturable, Geometry, Recorder};
use crate::video::PixelProvider;
use nvfbc_crate::{Box as NvFbcBox, Output as NvFbcOutput, Size as NvFbcSize, Status};
use std::any::Any;
use std::cell::Cell;
use std::error::Error;
use std::ffi::{c_void, CStr};
use std::io;
use std::mem::MaybeUninit;
use std::ptr::null_mut;
use std::time::Duration;

fn io_error(message: impl Into<String>) -> io::Error {
    io::Error::other(message.into())
}

#[derive(Clone)]
pub struct NvFbcCapturable {
    name: String,
    output_id: Option<u32>,
    region: NvFbcBox,
    desktop_size: NvFbcSize,
}

impl Capturable for NvFbcCapturable {
    fn as_any(&self) -> &dyn Any {
        self
    }

    fn name(&self) -> String {
        self.name.clone()
    }

    fn geometry(&self) -> Result<Geometry, Box<dyn Error>> {
        if self.output_id.is_some() && self.desktop_size.w > 0 && self.desktop_size.h > 0 {
            return Ok(Geometry::Relative(
                self.region.x as f64 / self.desktop_size.w as f64,
                self.region.y as f64 / self.desktop_size.h as f64,
                self.region.w as f64 / self.desktop_size.w as f64,
                self.region.h as f64 / self.desktop_size.h as f64,
            ));
        }
        Ok(Geometry::Relative(0.0, 0.0, 1.0, 1.0))
    }

    fn before_input(&mut self) -> Result<(), Box<dyn Error>> {
        Ok(())
    }

    fn recorder(&self, capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        Ok(Box::new(NvFbcRecorder::new(self.clone(), capture_cursor)?))
    }
}

pub fn get_capturables() -> Result<Vec<NvFbcCapturable>, Box<dyn Error>> {
    let capturer = NvFbcSystemCapturer::new()
        .map_err(|err| io_error(format!("NvFBC initialization failed: {err}")))?;
    let status = capturer
        .status()
        .map_err(|err| io_error(format!("NvFBC status query failed: {err}")))?;
    if !status.is_capture_possible {
        return Err(Box::new(io_error(
            "NvFBC capture is not possible on this driver/GPU/session",
        )));
    }
    if !status.can_create_now {
        return Err(Box::new(io_error(format!(
            "NvFBC cannot create a capture session now (currentlyCapturing={} inModeset={})",
            status.currently_capturing, status.in_modeset
        ))));
    }
    if status.screen_size.w == 0 || status.screen_size.h == 0 {
        return Err(Box::new(io_error("NvFBC reported an empty screen size")));
    }

    let mut capturables = vec![NvFbcCapturable {
        name: format!(
            "Desktop NvFBC {}x{}",
            status.screen_size.w, status.screen_size.h
        ),
        output_id: None,
        region: NvFbcBox {
            x: 0,
            y: 0,
            w: status.screen_size.w,
            h: status.screen_size.h,
        },
        desktop_size: status.screen_size,
    }];

    if status.xrandr_available {
        capturables.extend(
            status
                .outputs
                .iter()
                .map(|output| output_capturable(output, &status)),
        );
    }

    Ok(capturables)
}

fn output_capturable(output: &NvFbcOutput, status: &Status) -> NvFbcCapturable {
    NvFbcCapturable {
        name: format!(
            "Desktop NvFBC {} {}x{}+{}+{}",
            output.name,
            output.tracked_box.w,
            output.tracked_box.h,
            output.tracked_box.x,
            output.tracked_box.y
        ),
        output_id: Some(output.id),
        region: output.tracked_box,
        desktop_size: status.screen_size,
    }
}

pub struct NvFbcRecorder {
    capturer: NvFbcSystemCapturer,
    width: usize,
    height: usize,
}

impl NvFbcRecorder {
    fn new(capturable: NvFbcCapturable, capture_cursor: bool) -> Result<Self, Box<dyn Error>> {
        let mut capturer = NvFbcSystemCapturer::new()
            .map_err(|err| io_error(format!("NvFBC initialization failed: {err}")))?;
        let status = capturer
            .status()
            .map_err(|err| io_error(format!("NvFBC status query failed: {err}")))?;
        if !status.can_create_now {
            return Err(Box::new(io_error(format!(
                "NvFBC cannot create a capture session now (currentlyCapturing={} inModeset={})",
                status.currently_capturing, status.in_modeset
            ))));
        }
        capturer
            .start(capturable.output_id, capture_cursor, 60)
            .map_err(|err| io_error(format!("NvFBC start failed: {err}")))?;
        Ok(Self {
            capturer,
            width: capturable.region.w as usize,
            height: capturable.region.h as usize,
        })
    }
}

impl Drop for NvFbcRecorder {
    fn drop(&mut self) {
        if let Err(err) = self.capturer.stop() {
            tracing::debug!("NvFBC stop failed during drop: {err}");
        }
    }
}

impl Recorder for NvFbcRecorder {
    fn backend_name(&self) -> &'static str {
        "NvFBC ToSystem"
    }

    fn capture(&mut self) -> Result<PixelProvider<'_>, Box<dyn Error>> {
        let frame = self
            .capturer
            .next_frame(
                NvFbcCaptureMethod::Blocking,
                Some(Duration::from_millis(1000)),
            )
            .map_err(|err| io_error(format!("NvFBC frame capture failed: {err}")))?;
        self.width = frame.width as usize;
        self.height = frame.height as usize;
        Ok(PixelProvider::BGR0(self.width, self.height, frame.buffer))
    }
}

type NvFbcHandle = nvfbc_sys::NVFBC_SESSION_HANDLE;

const MAGIC_PRIVATE_DATA: [u32; 4] = [0xAEF57AC5, 0x401D1A39, 0x1B856BBE, 0x9ED0CEBA];

enum NvFbcCaptureMethod {
    Blocking =
        nvfbc_sys::NVFBC_TOSYS_GRAB_FLAGS_NVFBC_TOSYS_GRAB_FLAGS_NOWAIT_IF_NEW_FRAME_READY as isize,
}

struct NvFbcSystemFrameInfo<'a> {
    buffer: &'a [u8],
    width: u32,
    height: u32,
}

struct NvFbcSystemCapturer {
    handle: NvFbcHandle,
    buffer: Box<Cell<*mut c_void>>,
}

impl NvFbcSystemCapturer {
    fn new() -> Result<Self, nvfbc_crate::Error> {
        let mut params: nvfbc_sys::NVFBC_CREATE_HANDLE_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        params.dwVersion = nvfbc_sys::NVFBC_CREATE_HANDLE_PARAMS_VER;
        params.privateData = MAGIC_PRIVATE_DATA.as_ptr() as _;
        params.privateDataSize = std::mem::size_of_val(&MAGIC_PRIVATE_DATA) as u32;

        let mut handle = 0;
        let ret = unsafe { nvfbc_sys::NvFBCCreateHandle(&mut handle, &mut params) };
        if ret != nvfbc_sys::_NVFBCSTATUS_NVFBC_SUCCESS {
            return Err(nvfbc_crate::Error::new(ret, None));
        }

        Ok(Self {
            handle,
            buffer: Box::new(Cell::new(null_mut())),
        })
    }

    fn status(&self) -> Result<Status, nvfbc_crate::Error> {
        let mut params: nvfbc_sys::NVFBC_GET_STATUS_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        params.dwVersion = nvfbc_sys::NVFBC_GET_STATUS_PARAMS_VER;
        self.check_ret(unsafe { nvfbc_sys::NvFBCGetStatus(self.handle, &mut params) })?;
        Ok(params.into())
    }

    fn start(
        &mut self,
        output_id: Option<u32>,
        capture_cursor: bool,
        fps: u32,
    ) -> Result<(), nvfbc_crate::Error> {
        let mut session: nvfbc_sys::NVFBC_CREATE_CAPTURE_SESSION_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        session.dwVersion = nvfbc_sys::NVFBC_CREATE_CAPTURE_SESSION_PARAMS_VER;
        session.eCaptureType = nvfbc_sys::_NVFBC_CAPTURE_TYPE_NVFBC_CAPTURE_TO_SYS;
        session.eTrackingType = if output_id.is_some() {
            nvfbc_sys::NVFBC_TRACKING_TYPE_NVFBC_TRACKING_OUTPUT
        } else {
            nvfbc_sys::NVFBC_TRACKING_TYPE_NVFBC_TRACKING_DEFAULT
        };
        session.dwOutputId = output_id.unwrap_or(0);
        session.captureBox = nvfbc_sys::NVFBC_BOX {
            x: 0,
            y: 0,
            w: 0,
            h: 0,
        };
        session.frameSize = nvfbc_sys::NVFBC_SIZE { w: 0, h: 0 };
        session.bWithCursor = nvfbc_bool(capture_cursor);
        session.dwSamplingRateMs = 1000 / fps.max(1);
        self.check_ret(unsafe { nvfbc_sys::NvFBCCreateCaptureSession(self.handle, &mut session) })?;

        let mut setup: nvfbc_sys::NVFBC_TOSYS_SETUP_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        setup.dwVersion = nvfbc_sys::NVFBC_TOSYS_SETUP_PARAMS_VER;
        setup.eBufferFormat = nvfbc_sys::_NVFBC_BUFFER_FORMAT_NVFBC_BUFFER_FORMAT_BGRA;
        setup.ppBuffer = self.buffer.as_ptr();
        self.check_ret(unsafe { nvfbc_sys::NvFBCToSysSetUp(self.handle, &mut setup) })
    }

    fn stop(&self) -> Result<(), nvfbc_crate::Error> {
        let mut params: nvfbc_sys::NVFBC_DESTROY_CAPTURE_SESSION_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        params.dwVersion = nvfbc_sys::NVFBC_DESTROY_CAPTURE_SESSION_PARAMS_VER;
        self.check_ret(unsafe { nvfbc_sys::NvFBCDestroyCaptureSession(self.handle, &mut params) })
    }

    fn next_frame(
        &mut self,
        capture_method: NvFbcCaptureMethod,
        timeout: Option<Duration>,
    ) -> Result<NvFbcSystemFrameInfo<'_>, nvfbc_crate::Error> {
        let mut frame_info: nvfbc_sys::NVFBC_FRAME_GRAB_INFO =
            unsafe { MaybeUninit::zeroed().assume_init() };
        let mut params: nvfbc_sys::NVFBC_TOSYS_GRAB_FRAME_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        params.dwVersion = nvfbc_sys::NVFBC_TOSYS_GRAB_FRAME_PARAMS_VER;
        params.dwFlags = capture_method as u32;
        params.pFrameGrabInfo = &mut frame_info;
        if let Some(timeout) = timeout {
            params.dwTimeoutMs = timeout.as_millis() as u32;
        }
        self.check_ret(unsafe { nvfbc_sys::NvFBCToSysGrabFrame(self.handle, &mut params) })?;
        let buffer_ptr = unsafe { self.buffer.as_ptr().read_volatile().cast() };
        let buffer =
            unsafe { std::slice::from_raw_parts(buffer_ptr, frame_info.dwByteSize as usize) };

        Ok(NvFbcSystemFrameInfo {
            buffer,
            width: frame_info.dwWidth,
            height: frame_info.dwHeight,
        })
    }

    fn check_ret(&self, ret: nvfbc_sys::NVFBCSTATUS) -> Result<(), nvfbc_crate::Error> {
        if ret != nvfbc_sys::_NVFBCSTATUS_NVFBC_SUCCESS {
            return Err(nvfbc_crate::Error::new(ret, self.last_error()));
        }
        Ok(())
    }

    fn last_error(&self) -> Option<String> {
        let error = unsafe { nvfbc_sys::NvFBCGetLastErrorStr(self.handle) };
        if error.is_null() {
            return None;
        }
        unsafe { CStr::from_ptr(error) }
            .to_str()
            .ok()
            .map(|err| err.to_string())
    }
}

impl Drop for NvFbcSystemCapturer {
    fn drop(&mut self) {
        let mut params: nvfbc_sys::NVFBC_DESTROY_HANDLE_PARAMS =
            unsafe { MaybeUninit::zeroed().assume_init() };
        params.dwVersion = nvfbc_sys::NVFBC_DESTROY_HANDLE_PARAMS_VER;
        let _ = unsafe { nvfbc_sys::NvFBCDestroyHandle(self.handle, &mut params) };
    }
}

fn nvfbc_bool(value: bool) -> nvfbc_sys::NVFBC_BOOL {
    if value {
        nvfbc_sys::_NVFBC_BOOL_NVFBC_TRUE
    } else {
        nvfbc_sys::_NVFBC_BOOL_NVFBC_FALSE
    }
}
