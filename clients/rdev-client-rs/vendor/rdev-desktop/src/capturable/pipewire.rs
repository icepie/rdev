use std::any::Any;
use std::collections::HashMap;
use std::error::Error;
#[cfg(feature = "pipewire-drm-prime")]
use std::ffi::c_int;
use std::io::Write;
use std::os::fd::AsRawFd;
#[cfg(feature = "pipewire-drm-prime")]
use std::os::fd::{FromRawFd, OwnedFd as StdOwnedFd};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tracing::{debug, trace, warn};

use dbus::{
    arg::{OwnedFd, PropMap, RefArg, Variant},
    blocking::{Proxy, SyncConnection},
    message::{MatchRule, MessageType},
    Message,
};

use gstreamer as gst;
use gstreamer::prelude::*;
use gstreamer_app::AppSink;

use crate::capturable::{Capturable, Geometry, Recorder, RecorderPreferences};
use crate::video::{CapturedFrame, PixelProvider};
#[cfg(feature = "pipewire-drm-prime")]
use crate::video::{DmaBufFrame, DmaBufLayer, DmaBufObject, DmaBufPlane};

use crate::capturable::remote_desktop_dbus::{
    OrgFreedesktopPortalRemoteDesktop, OrgFreedesktopPortalRequestResponse,
    OrgFreedesktopPortalScreenCast,
};

#[cfg(feature = "pipewire-drm-prime")]
#[link(name = "gstallocators-1.0")]
unsafe extern "C" {
    fn gst_is_dmabuf_memory(memory: *mut gst::ffi::GstMemory) -> c_int;
    fn gst_dmabuf_memory_get_fd(memory: *mut gst::ffi::GstMemory) -> c_int;
}

fn get_sway_rotation() -> u32 {
    use std::io::Read;
    let sock = match std::env::var("SWAYSOCK") {
        Ok(s) => s,
        Err(_) => return 0,
    };
    let mut stream = match std::os::unix::net::UnixStream::connect(&sock) {
        Ok(s) => s,
        Err(_) => return 0,
    };
    // sway IPC: magic + len + type
    let payload = b"GET_OUTPUTS";
    let mut msg = b"i3-ipc".to_vec();
    msg.extend_from_slice(&(payload.len() as u32).to_ne_bytes());
    msg.extend_from_slice(&3u32.to_ne_bytes()); // GET_OUTPUTS = 3
    msg.extend_from_slice(payload);
    if stream.write_all(&msg).is_err() {
        return 0;
    }
    let mut header = [0u8; 14];
    if stream.read_exact(&mut header).is_err() {
        return 0;
    }
    let len = u32::from_ne_bytes(header[6..10].try_into().unwrap_or([0; 4])) as usize;
    let mut body = vec![0u8; len];
    if stream.read_exact(&mut body).is_err() {
        return 0;
    }
    let s = String::from_utf8_lossy(&body);
    // parse "transform":"90" or "270" etc
    if let Some(pos) = s.find("\"transform\"") {
        let rest = &s[pos + 12..];
        if let Some(start) = rest.find('"') {
            let rest = &rest[start + 1..];
            if let Some(end) = rest.find('"') {
                let t = &rest[..end];
                return match t {
                    "90" => 1, // clockwise 90
                    "180" => 2,
                    "270" => 3, // clockwise 270
                    "flipped" => 4,
                    "flipped-90" => 5,
                    "flipped-180" => 6,
                    "flipped-270" => 7,
                    _ => 0,
                };
            }
        }
    }
    0
}

#[derive(Debug, Clone, Copy)]
struct PwStreamInfo {
    path: u64,
    source_type: u64,
    size: Option<(i32, i32)>,
}

#[derive(Debug)]
pub struct DBusError(String);

impl std::fmt::Display for DBusError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let Self(s) = self;
        write!(f, "{}", s)
    }
}

impl Error for DBusError {}

#[derive(Debug)]
pub struct GStreamerError(String);

impl std::fmt::Display for GStreamerError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let Self(s) = self;
        write!(f, "{}", s)
    }
}

impl Error for GStreamerError {}

#[derive(Clone)]
pub struct PortalRemoteDesktopSession {
    dbus_conn: Arc<SyncConnection>,
    session: dbus::Path<'static>,
    devices: u32,
}

impl PortalRemoteDesktopSession {
    pub fn connection(&self) -> Arc<SyncConnection> {
        self.dbus_conn.clone()
    }

    pub fn session_handle(&self) -> dbus::Path<'static> {
        self.session.clone()
    }

    pub fn devices(&self) -> u32 {
        self.devices
    }
}

#[derive(Clone)]
pub struct PipeWireCapturable {
    // connection needs to be kept alive for recording
    dbus_conn: Arc<SyncConnection>,
    fd: OwnedFd,
    path: u64,
    source_type: u64,
    size: Option<(i32, i32)>,
    portal_session: Option<PortalRemoteDesktopSession>,
    pub rotation: u32,
}

impl PipeWireCapturable {
    fn new(
        conn: Arc<SyncConnection>,
        fd: OwnedFd,
        portal_session: Option<PortalRemoteDesktopSession>,
        stream: PwStreamInfo,
    ) -> Self {
        let rotation = get_sway_rotation();
        Self {
            dbus_conn: conn,
            fd,
            path: stream.path,
            source_type: stream.source_type,
            size: stream.size,
            portal_session,
            rotation,
        }
    }

    pub fn portal_session(&self) -> Option<PortalRemoteDesktopSession> {
        self.portal_session.clone()
    }

    pub fn stream_id(&self) -> Option<u32> {
        u32::try_from(self.path).ok()
    }

    pub fn logical_size(&self) -> Option<(i32, i32)> {
        self.size
    }
}

impl std::fmt::Debug for PipeWireCapturable {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "PipeWireCapturable {{dbus: {}, fd: {}, path: {}, source_type: {}}}",
            self.dbus_conn.unique_name(),
            self.fd.as_raw_fd(),
            self.path,
            self.source_type
        )
    }
}

impl Capturable for PipeWireCapturable {
    fn as_any(&self) -> &dyn Any {
        self
    }

    fn name(&self) -> String {
        let type_str = match self.source_type {
            1 => "Desktop",
            2 => "Window",
            _ => "Unknown",
        };
        format!("Pipewire {}, path: {}", type_str, self.path)
    }

    fn geometry(&self) -> Result<Geometry, Box<dyn Error>> {
        Ok(Geometry::Relative(0.0, 0.0, 1.0, 1.0))
    }

    fn before_input(&mut self) -> Result<(), Box<dyn Error>> {
        Ok(())
    }

    fn recorder(&self, _capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        Ok(Box::new(PipeWireRecorder::new(self.clone())?))
    }

    fn recorder_with_preferences(
        &self,
        _capture_cursor: bool,
        preferences: RecorderPreferences,
    ) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        Ok(Box::new(PipeWireRecorder::new_with_preferences(
            self.clone(),
            preferences,
        )?))
    }
}

pub struct PipeWireRecorder {
    buffer: Option<gst::MappedBuffer<gst::buffer::Readable>>,
    #[cfg(feature = "pipewire-drm-prime")]
    drm_prime_buffer: Option<gst::Buffer>,
    buffer_cropped: Vec<u8>,
    pix_fmt: String,
    is_cropped: bool,
    pipeline: gst::Pipeline,
    appsink: AppSink,
    width: usize,
    height: usize,
    prefer_drm_prime: bool,
    #[cfg(feature = "pipewire-drm-prime")]
    warned_drm_prime_unavailable: bool,
}

impl PipeWireRecorder {
    pub fn new(capturable: PipeWireCapturable) -> Result<Self, Box<dyn Error>> {
        Self::new_with_preferences(capturable, RecorderPreferences::default())
    }

    fn new_with_preferences(
        capturable: PipeWireCapturable,
        preferences: RecorderPreferences,
    ) -> Result<Self, Box<dyn Error>> {
        let pipeline = gst::Pipeline::new();

        let src = gst::ElementFactory::make("pipewiresrc").build()?;
        src.set_property("fd", &capturable.fd.as_raw_fd());
        src.set_property("path", &format!("{}", capturable.path));

        // For some reason pipewire blocks on destruction of AppSink if this is not set to true,
        // see: https://gitlab.freedesktop.org/pipewire/pipewire/-/issues/982
        let prefer_drm_prime = pipewire_drm_prime_enabled(preferences.prefer_drm_prime);
        src.set_property("always-copy", &!prefer_drm_prime);

        let sink = gst::ElementFactory::make("appsink").build()?;
        sink.set_property("drop", &true);
        sink.set_property("max-buffers", &1u32);

        if capturable.rotation != 0 {
            let flip = gst::ElementFactory::make("videoflip").build()?;
            flip.set_property_from_str(
                "method",
                match capturable.rotation {
                    1 => "counterclockwise",
                    2 => "rotate-180",
                    3 => "clockwise",
                    4 => "horizontal-flip",
                    5 => "upper-left-diagonal",
                    6 => "vertical-flip",
                    7 => "upper-right-diagonal",
                    _ => "none",
                },
            );
            pipeline.add_many(&[&src, &flip, &sink])?;
            src.link(&flip)?;
            flip.link(&sink)?;
        } else {
            pipeline.add_many(&[&src, &sink])?;
            src.link(&sink)?;
        }
        let appsink = sink
            .dynamic_cast::<AppSink>()
            .map_err(|_| GStreamerError("Sink element is expected to be an appsink!".into()))?;
        let mut caps = gst::Caps::new_empty();
        if prefer_drm_prime && capturable.rotation == 0 {
            caps.get_mut().unwrap().append_structure_full(
                gst::structure::Structure::from_iter(
                    "video/x-raw",
                    [
                        ("format", "DMA_DRM".into()),
                        (
                            "drm-format",
                            (&gst::List::new(["XR24", "AR24", "XB24", "AB24"])).into(),
                        ),
                    ],
                ),
                Some(gst::CapsFeatures::new(["memory:DMABuf"])),
            );
        }
        caps.merge_structure(gst::structure::Structure::from_iter(
            "video/x-raw",
            [("format", "BGRx".into())],
        ));
        caps.merge_structure(gst::structure::Structure::from_iter(
            "video/x-raw",
            [("format", "RGBx".into())],
        ));
        appsink.set_caps(Some(&caps));

        pipeline.set_state(gst::State::Playing)?;
        Ok(Self {
            pipeline,
            appsink,
            buffer: None,
            #[cfg(feature = "pipewire-drm-prime")]
            drm_prime_buffer: None,
            pix_fmt: "".into(),
            width: 0,
            height: 0,
            buffer_cropped: vec![],
            is_cropped: false,
            prefer_drm_prime,
            #[cfg(feature = "pipewire-drm-prime")]
            warned_drm_prime_unavailable: false,
        })
    }
}

impl Recorder for PipeWireRecorder {
    fn backend_name(&self) -> &'static str {
        "Wayland/PipeWire"
    }

    fn set_preferences(&mut self, preferences: RecorderPreferences) {
        self.prefer_drm_prime = pipewire_drm_prime_enabled(preferences.prefer_drm_prime);
    }

    fn capture_frame(&mut self) -> Result<CapturedFrame<'_>, Box<dyn Error>> {
        if let Some(sample) = self
            .appsink
            .try_pull_sample(gst::ClockTime::from_mseconds(16))
        {
            let caps = sample
                .caps()
                .ok_or_else(|| GStreamerError("PipeWire sample did not include caps.".into()))?;
            let structure = caps
                .structure(0)
                .ok_or_else(|| GStreamerError("PipeWire sample caps are empty.".into()))?;
            let format: String = structure.value("format")?.get()?;
            if self.prefer_drm_prime && format == "DMA_DRM" {
                #[cfg(feature = "pipewire-drm-prime")]
                {
                    let drm_prime = sample
                        .buffer_owned()
                        .ok_or_else(|| {
                            Box::new(GStreamerError("Failed to get owned DMA_DRM buffer.".into()))
                                as Box<dyn Error>
                        })
                        .and_then(|buffer| self.buffer_to_drm_prime(buffer, caps));
                    match drm_prime {
                        Ok(frame) => return Ok(CapturedFrame::DrmPrime(frame)),
                        Err(err) => {
                            if !self.warned_drm_prime_unavailable {
                                debug!(
                                    "Wayland/PipeWire DRM PRIME zero-copy unavailable, using CPU fallback: {err}"
                                );
                                self.warned_drm_prime_unavailable = true;
                            }
                        }
                    }
                }
            } else if format == "DMA_DRM" {
                trace!("PipeWire delivered DMA_DRM while zero-copy is disabled, dropping frame.");
                return self.capture_frame();
            } else if self.store_cpu_sample(sample)? {
                return self.pixel_provider().map(CapturedFrame::Cpu);
            }
        } else {
            trace!("No new buffer available, falling back to previous one.");
        }

        self.pixel_provider().map(CapturedFrame::Cpu)
    }

    fn capture(&mut self) -> Result<PixelProvider<'_>, Box<dyn Error>> {
        if let Some(sample) = self
            .appsink
            .try_pull_sample(gst::ClockTime::from_mseconds(16))
        {
            self.store_cpu_sample(sample)?;
        } else {
            trace!("No new buffer available, falling back to previous one.");
        }
        self.pixel_provider()
    }
}

impl PipeWireRecorder {
    fn store_cpu_sample(&mut self, sample: gst::Sample) -> Result<bool, Box<dyn Error>> {
        let cap = sample.caps().unwrap().structure(0).unwrap();
        let w: i32 = cap.value("width")?.get()?;
        let h: i32 = cap.value("height")?.get()?;
        self.pix_fmt = cap.value("format")?.get()?;
        if self.pix_fmt == "DMA_DRM" {
            return Ok(false);
        }
        let w = w as usize;
        let h = h as usize;
        let buf = sample
            .buffer_owned()
            .ok_or_else(|| GStreamerError("Failed to get owned buffer.".into()))?;
        let mut crop = buf
            .meta::<gstreamer_video::VideoCropMeta>()
            .map(|m| m.rect());
        // only crop if necessary
        if Some((0, 0, w as u32, h as u32)) == crop {
            crop = None;
        }
        let buf = buf
            .into_mapped_buffer_readable()
            .map_err(|_| GStreamerError("Failed to map buffer.".into()))?;
        let buf_size = buf.size();
        // BGRx is 4 bytes per pixel
        if buf_size != (w * h * 4) {
            // for some reason the width and height of the caps do not guarantee correct buffer
            // size, so ignore those buffers, see:
            // https://gitlab.freedesktop.org/pipewire/pipewire/-/issues/985
            trace!(
                "Size of mapped buffer: {} does NOT match size of capturable {}x{}@BGRx, \
                dropping it!",
                buf_size,
                w,
                h
            );
            return Ok(false);
        }

        // Copy region specified by crop into self.buffer_cropped
        if let Some((x_off, y_off, w_crop, h_crop)) = crop {
            let x_off = x_off as usize;
            let y_off = y_off as usize;
            let w_crop = w_crop as usize;
            let h_crop = h_crop as usize;
            self.buffer_cropped.clear();
            let data = buf.as_slice();
            self.buffer_cropped.reserve(w_crop * h_crop * 4);
            for y in y_off..(y_off + h_crop) {
                let i = 4 * (w * y + x_off);
                self.buffer_cropped.extend(&data[i..i + 4 * w_crop]);
            }
            self.width = w_crop;
            self.height = h_crop;
        } else {
            self.width = w;
            self.height = h;
        }
        self.is_cropped = crop.is_some();
        self.buffer = Some(buf);
        Ok(true)
    }

    fn pixel_provider(&self) -> Result<PixelProvider<'_>, Box<dyn Error>> {
        if self.buffer.is_none() {
            return Err(Box::new(GStreamerError("No buffer available!".into())));
        }
        let buf = if self.is_cropped {
            self.buffer_cropped.as_slice()
        } else {
            self.buffer.as_ref().unwrap().as_slice()
        };
        match self.pix_fmt.as_str() {
            "BGRx" => Ok(PixelProvider::BGR0(self.width, self.height, buf)),
            "RGBx" => Ok(PixelProvider::RGB0(self.width, self.height, buf)),
            _ => unreachable!(),
        }
    }

    #[cfg(feature = "pipewire-drm-prime")]
    fn buffer_to_drm_prime(
        &mut self,
        buffer: gst::Buffer,
        caps: &gst::CapsRef,
    ) -> Result<DmaBufFrame, Box<dyn Error>> {
        let info = gstreamer_video::VideoInfoDmaDrm::from_caps(caps)?;
        let meta = buffer
            .meta::<gstreamer_video::VideoMeta>()
            .ok_or_else(|| GStreamerError("DMA_DRM buffer did not include VideoMeta.".into()))?;
        let plane_count = meta.n_planes() as usize;
        if plane_count == 0 || plane_count > 4 || plane_count > buffer.n_memory() {
            return Err(Box::new(GStreamerError(format!(
                "Unsupported DMA_DRM plane count: planes={} memories={}",
                plane_count,
                buffer.n_memory()
            ))));
        }

        let mut objects = Vec::with_capacity(plane_count);
        let mut planes = Vec::with_capacity(plane_count);
        for idx in 0..plane_count {
            let memory = buffer.peek_memory(idx);
            let fd = unsafe {
                if gst_is_dmabuf_memory(memory.as_mut_ptr()) == 0 {
                    return Err(Box::new(GStreamerError(format!(
                        "PipeWire memory {idx} is not DMABuf memory"
                    ))));
                }
                let fd = gst_dmabuf_memory_get_fd(memory.as_mut_ptr());
                if fd < 0 {
                    return Err(Box::new(GStreamerError(format!(
                        "PipeWire memory {idx} did not expose a valid DMABuf fd"
                    ))));
                }
                let dup_fd = libc::dup(fd);
                if dup_fd < 0 {
                    return Err(Box::new(std::io::Error::last_os_error()));
                }
                StdOwnedFd::from_raw_fd(dup_fd)
            };
            let offset = meta.offset()[idx].saturating_add(memory.offset());
            let pitch = meta.stride()[idx] as isize;
            objects.push(DmaBufObject {
                fd,
                size: memory.maxsize(),
                format_modifier: info.modifier(),
            });
            planes.push(DmaBufPlane {
                object_index: idx,
                offset: offset as isize,
                pitch,
            });
        }

        self.width = info.width() as usize;
        self.height = info.height() as usize;
        self.drm_prime_buffer = Some(buffer);
        Ok(DmaBufFrame {
            width: self.width,
            height: self.height,
            objects,
            layers: vec![DmaBufLayer {
                format: info.fourcc(),
                plane_count,
            }],
            planes,
        })
    }
}

#[cfg(feature = "pipewire-drm-prime")]
fn pipewire_drm_prime_enabled(requested: bool) -> bool {
    requested
}

#[cfg(not(feature = "pipewire-drm-prime"))]
fn pipewire_drm_prime_enabled(_requested: bool) -> bool {
    false
}

impl Drop for PipeWireRecorder {
    fn drop(&mut self) {
        if let Err(err) = self.pipeline.set_state(gst::State::Null) {
            warn!("Failed to stop GStreamer pipeline: {}.", err);
        }
    }
}

fn handle_response<F>(
    portal: Proxy<&SyncConnection>,
    path: dbus::Path<'static>,
    context: Arc<Mutex<CallBackContext>>,
    mut f: F,
) -> Result<dbus::channel::Token, dbus::Error>
where
    F: FnMut(
            OrgFreedesktopPortalRequestResponse,
            Proxy<&SyncConnection>,
            &Message,
            Arc<Mutex<CallBackContext>>,
        ) -> Result<(), Box<dyn Error>>
        + Send
        + Sync
        + 'static,
{
    let mut m = MatchRule::new();
    m.path = Some(path);
    m.msg_type = Some(MessageType::Signal);
    m.sender = Some("org.freedesktop.portal.Desktop".into());
    m.interface = Some("org.freedesktop.portal.Request".into());
    portal
        .connection
        .add_match(m, move |r: OrgFreedesktopPortalRequestResponse, c, m| {
            let portal = get_portal(c);
            debug!("Response from DBus: response: {:?}, message: {:?}", r, m);
            match r.response {
                0 => {}
                1 => {
                    context.lock().unwrap().failure = true;
                    warn!("DBus response: User cancelled interaction.");
                    return true;
                }
                c => {
                    context.lock().unwrap().failure = true;
                    warn!("DBus response: Unknown error, code: {}.", c);
                    return true;
                }
            }
            if let Err(err) = f(r, portal, m, context.clone()) {
                context.lock().unwrap().failure = true;
                warn!("Error requesting screen capture via dbus: {}", err);
            }
            true
        })
}

fn get_portal(conn: &SyncConnection) -> Proxy<'_, &SyncConnection> {
    conn.with_proxy(
        "org.freedesktop.portal.Desktop",
        "/org/freedesktop/portal/desktop",
        Duration::from_millis(1000),
    )
}

fn streams_from_response(response: &OrgFreedesktopPortalRequestResponse) -> Vec<PwStreamInfo> {
    (move || {
        Some(
            response
                .results
                .get("streams")?
                .as_iter()?
                .next()?
                .as_iter()?
                .filter_map(|stream| {
                    let mut itr = stream.as_iter()?;
                    let path = itr.next()?.as_u64()?;
                    let (keys, values): (Vec<(usize, &dyn RefArg)>, Vec<(usize, &dyn RefArg)>) =
                        itr.next()?
                            .as_iter()?
                            .enumerate()
                            .partition(|(i, _)| i % 2 == 0);
                    let attributes = keys
                        .iter()
                        .filter_map(|(_, key)| Some(key.as_str()?.to_owned()))
                        .zip(
                            values
                                .iter()
                                .map(|(_, arg)| *arg)
                                .collect::<Vec<&dyn RefArg>>(),
                        )
                        .collect::<HashMap<String, &dyn RefArg>>();
                    Some(PwStreamInfo {
                        path,
                        source_type: attributes
                            .get("source_type")
                            .map_or(Some(0), |v| v.as_u64())?,
                        size: attributes.get("size").and_then(|value| {
                            let mut iter = value.as_iter()?;
                            let width = iter.next()?.as_i64()? as i32;
                            let height = iter.next()?.as_i64()? as i32;
                            Some((width, height))
                        }),
                    })
                })
                .collect::<Vec<PwStreamInfo>>(),
        )
    })()
    .unwrap_or_default()
}

// mostly inspired by https://gitlab.gnome.org/snippets/19 and
// https://gitlab.gnome.org/-/snippets/39
struct CallBackContext {
    capture_cursor: bool,
    session: dbus::Path<'static>,
    streams: Vec<PwStreamInfo>,
    fd: Option<OwnedFd>,
    restore_token: Option<String>,
    has_remote_desktop: bool,
    devices: u32,
    failure: bool,
}

fn on_create_session_response(
    r: OrgFreedesktopPortalRequestResponse,
    portal: Proxy<&SyncConnection>,
    _msg: &Message,
    context: Arc<Mutex<CallBackContext>>,
) -> Result<(), Box<dyn Error>> {
    debug!("on_create_session_response");
    let session: dbus::Path = r
        .results
        .get("session_handle")
        .ok_or_else(|| {
            DBusError(format!(
                "Failed to obtain session_handle from response: {:?}",
                r
            ))
        })?
        .as_str()
        .ok_or_else(|| DBusError("Failed to convert session_handle to string.".into()))?
        .to_string()
        .into();

    context.lock().unwrap().session = session.clone();
    if context.lock().unwrap().has_remote_desktop {
        select_devices(portal, context)
    } else {
        select_sources(portal, context)
    }
}

fn select_devices(
    portal: Proxy<&SyncConnection>,
    context: Arc<Mutex<CallBackContext>>,
) -> Result<(), Box<dyn Error>> {
    let mut args: PropMap = HashMap::new();
    let t: usize = rand::random();
    args.insert(
        "handle_token".to_string(),
        Variant(Box::new(format!("weylus{t}"))),
    );

    // TODO
    //args.insert(
    //    "restore_token".to_string(),
    //    Variant(Box::new(format!("weylus{t}"))),
    //);

    // persist modes:
    // 0: Do not persist (default)
    // 1: Permissions persist as long as the application is running
    // 2: Permissions persist until explicitly revoked
    args.insert("persist_mode".to_string(), Variant(Box::new(2 as u32)));

    // device types
    // 1: KEYBOARD
    // 2: POINTER
    // 4: TOUCHSCREEN
    let device_types = portal.available_device_types()?;
    debug!("Available device types: {device_types}.");
    args.insert("types".to_string(), Variant(Box::new(device_types)));

    let path = portal.select_devices(context.lock().unwrap().session.clone(), args)?;
    handle_response(portal, path, context, |_, portal, _, context| {
        select_sources(portal, context)
    })?;
    Ok(())
}

fn select_sources(
    portal: Proxy<&SyncConnection>,
    context: Arc<Mutex<CallBackContext>>,
) -> Result<(), Box<dyn Error>> {
    debug!("select_sources");
    let mut args: PropMap = HashMap::new();

    let t: usize = rand::random();
    args.insert(
        "handle_token".to_string(),
        Variant(Box::new(format!("weylus{t}"))),
    );
    // https://flatpak.github.io/xdg-desktop-portal/docs/doc-org.freedesktop.portal.ScreenCast.html#org-freedesktop-portal-screencast-selectsources
    // allow multiple sources
    args.insert("multiple".into(), Variant(Box::new(true)));

    // 1: MONITOR
    // 2: WINDOW
    // 4: VIRTUAL
    let source_types = portal.available_source_types()?;
    debug!("Available source types: {source_types}.");
    args.insert("types".into(), Variant(Box::new(source_types)));

    let capture_cursor = context.lock().unwrap().capture_cursor;
    // 1: Hidden. The cursor is not part of the screen cast stream.
    // 2: Embedded: The cursor is embedded as part of the stream buffers.
    // 4: Metadata: The cursor is not part of the screen cast stream, but sent as PipeWire stream metadata.
    let available_cursor_modes = portal.available_cursor_modes().unwrap_or(0);
    let cursor_mode = if capture_cursor {
        if available_cursor_modes & 2 != 0 {
            Some(2u32)
        } else {
            None
        }
    } else {
        if available_cursor_modes & 1 != 0 {
            Some(1u32)
        } else if available_cursor_modes & 2 != 0 {
            Some(2u32)
        } else {
            None
        }
    };

    let is_plasma = std::env::var("DESKTOP_SESSION").map_or(false, |s| s.contains("plasma"));
    if is_plasma && capture_cursor {
        // Warn the user if capturing the cursor is tried on kde as this can crash
        // kwin_wayland and tear down the plasma desktop, see:
        // https://bugs.kde.org/show_bug.cgi?id=435042
        warn!(
            "You are attempting to capture the cursor under KDE Plasma, this may crash your \
                    desktop, see https://bugs.kde.org/show_bug.cgi?id=435042 for details! \
                    You have been warned."
        );
    }
    if let Some(mode) = cursor_mode {
        args.insert("cursor_mode".into(), Variant(Box::new(mode)));
    }

    if let Some(token) = context.lock().unwrap().restore_token.clone() {
        args.insert("restore_token".into(), Variant(Box::new(token)));
    }

    let path = portal.select_sources(context.lock().unwrap().session.clone(), args)?;
    handle_response(portal, path, context, on_select_sources_response)?;
    Ok(())
}

fn on_select_sources_response(
    _r: OrgFreedesktopPortalRequestResponse,
    portal: Proxy<&SyncConnection>,
    _msg: &Message,
    context: Arc<Mutex<CallBackContext>>,
) -> Result<(), Box<dyn Error>> {
    debug!("on_select_sources_response");
    let mut args: PropMap = HashMap::new();
    let t: usize = rand::random();
    args.insert(
        "handle_token".to_string(),
        Variant(Box::new(format!("weylus{t}"))),
    );
    let path = if context.lock().unwrap().has_remote_desktop {
        OrgFreedesktopPortalRemoteDesktop::start(
            &portal,
            context.lock().unwrap().session.clone(),
            "",
            args,
        )?
    } else {
        OrgFreedesktopPortalScreenCast::start(
            &portal,
            context.lock().unwrap().session.clone(),
            "",
            args,
        )?
    };
    handle_response(portal, path, context, on_start_response)?;
    Ok(())
}

fn on_start_response(
    r: OrgFreedesktopPortalRequestResponse,
    portal: Proxy<&SyncConnection>,
    _msg: &Message,
    context: Arc<Mutex<CallBackContext>>,
) -> Result<(), Box<dyn Error>> {
    debug!("on_start_response");
    let mut context = context.lock().unwrap();
    context.streams.append(&mut streams_from_response(&r));
    let session = context.session.clone();
    context
        .fd
        .replace(portal.open_pipe_wire_remote(session.clone(), HashMap::new())?);
    if let Some(Some(t)) = r.results.get("restore_token").map(|t| t.as_str()) {
        context.restore_token = Some(t.to_string());
        if let Some(path) = dirs::cache_dir().map(|p| p.join("weylus_restore_token")) {
            let _ = std::fs::write(&path, t);
        }
    }
    if let Some(devices) = r.results.get("devices").and_then(|v| v.as_u64()) {
        context.devices = devices as u32;
    }
    if context.has_remote_desktop {
        debug!("Remote Desktop Session started");
    } else {
        debug!("Screen Cast Session started");
    }
    Ok(())
}

fn request_remote_desktop(
    capture_cursor: bool,
) -> Result<
    (
        SyncConnection,
        OwnedFd,
        Vec<PwStreamInfo>,
        Option<(dbus::Path<'static>, u32)>,
    ),
    Box<dyn Error>,
> {
    let conn = SyncConnection::new_session()?;
    let portal = get_portal(&conn);

    // Disabled for KDE plasma due to https://bugs.kde.org/show_bug.cgi?id=484996
    // List of supported DEs: https://wiki.archlinux.org/title/XDG_Desktop_Portal#List_of_backends_and_interfaces
    let has_remote_desktop =
        std::env::var("DESKTOP_SESSION").map_or(false, |s| s.contains("gnome"));

    let restore_token_path = dirs::cache_dir()
        .map(|p| p.join("weylus_restore_token"))
        .and_then(|p| std::fs::read_to_string(&p).ok());

    let context = CallBackContext {
        capture_cursor,
        session: Default::default(),
        streams: Default::default(),
        fd: None,
        restore_token: restore_token_path,
        has_remote_desktop,
        devices: 0,
        failure: false,
    };
    let context = Arc::new(Mutex::new(context));

    let mut args: PropMap = HashMap::new();
    let t1: usize = rand::random();
    let t2: usize = rand::random();
    args.insert(
        "session_handle_token".to_string(),
        Variant(Box::new(format!("weylus{t1}"))),
    );
    args.insert(
        "handle_token".to_string(),
        Variant(Box::new(format!("weylus{t2}"))),
    );
    let path = if has_remote_desktop {
        OrgFreedesktopPortalRemoteDesktop::create_session(&portal, args)?
    } else {
        OrgFreedesktopPortalScreenCast::create_session(&portal, args)?
    };
    handle_response(portal, path, context.clone(), on_create_session_response)?;

    // wait 3 minutes for user interaction
    for _ in 0..1800 {
        conn.process(Duration::from_millis(100))?;
        let context = context.lock().unwrap();
        // Once we got a file descriptor we are done!
        if context.fd.is_some() {
            break;
        }

        if context.failure {
            break;
        }
    }
    let context = context.lock().unwrap();
    if context.fd.is_some() && !context.streams.is_empty() {
        let remote_desktop = if context.has_remote_desktop {
            Some((context.session.clone(), context.devices))
        } else {
            None
        };
        Ok((
            conn,
            context.fd.clone().unwrap(),
            context.streams.clone(),
            remote_desktop,
        ))
    } else {
        Err(Box::new(DBusError(
            "Failed to obtain screen capture.".into(),
        )))
    }
}

pub fn get_capturables(capture_cursor: bool) -> Result<Vec<PipeWireCapturable>, Box<dyn Error>> {
    let (conn, fd, streams, remote_desktop) = request_remote_desktop(capture_cursor)?;
    let conn = Arc::new(conn);
    let portal_session = remote_desktop.map(|(session, devices)| PortalRemoteDesktopSession {
        dbus_conn: conn.clone(),
        session,
        devices,
    });
    Ok(streams
        .into_iter()
        .map(|s| PipeWireCapturable::new(conn.clone(), fd.clone(), portal_session.clone(), s))
        .collect())
}
