use crate::capturable::{Capturable, Geometry, Recorder, RecorderPreferences};
use crate::cerror::CError;
use crate::video::{
    CapturedFrame, DmaBufFrame, DmaBufLayer, DmaBufObject, DmaBufPlane, PixelProvider,
};
use std::any::Any;

use drm::control::{connector, crtc, framebuffer, plane, Device as ControlDevice, PlaneType};
use drm::{ClientCapability, Device};
use drm_fourcc::{DrmFourcc, DrmModifier};
use linux_raw_sys::ioctl::DMA_BUF_IOCTL_SYNC;
use rustix::mm::{self, MapFlags, ProtFlags};
use std::error::Error;
use std::ffi::{c_char, c_int, c_void, CString};
use std::fs::{self, File, OpenOptions};
use std::io;
use std::num::NonZeroU32;
use std::os::fd::{AsFd, AsRawFd, BorrowedFd, OwnedFd, RawFd};
use std::ptr;
use tracing::{debug, warn};

fn io_error(message: impl Into<String>) -> io::Error {
    io::Error::other(message.into())
}

#[repr(C)]
#[derive(Clone, Copy, Debug, Default)]
struct KmsFrameMetadata {
    length: u32,
    src_x: u32,
    src_y: u32,
    src_w: u32,
    src_h: u32,
    dst_w: u32,
    dst_h: u32,
    fb_width: u32,
    fb_height: u32,
    fourcc: u32,
    modifier: u64,
    offsets: [u32; 4],
    pitches: [u32; 4],
}

extern "C" {
    fn init_kms_egl(card_path: *const c_char, err: *mut CError) -> *mut c_void;
    fn destroy_kms_egl(ctx: *mut c_void);
    fn kms_egl_read_bgra(
        ctx: *mut c_void,
        md: *const KmsFrameMetadata,
        fds: *const c_int,
        dst: *mut c_char,
        dst_len: usize,
        err: *mut CError,
    );
}

const IOC_NRBITS: u64 = 8;
const IOC_TYPEBITS: u64 = 8;
const IOC_SIZEBITS: u64 = 14;
const IOC_NRSHIFT: u64 = 0;
const IOC_TYPESHIFT: u64 = IOC_NRSHIFT + IOC_NRBITS;
const IOC_SIZESHIFT: u64 = IOC_TYPESHIFT + IOC_TYPEBITS;
const IOC_DIRSHIFT: u64 = IOC_SIZESHIFT + IOC_SIZEBITS;
const IOC_WRITE: u64 = 1;
const IOC_READ: u64 = 2;
const DRM_IOCTL_BASE: u64 = b'd' as u64;
const DRM_IOCTL_GEM_FLINK_NR: u64 = 0x0a;
const DRM_IOCTL_GEM_FLINK: libc::c_ulong = (((IOC_READ | IOC_WRITE) << IOC_DIRSHIFT)
    | ((std::mem::size_of::<DrmGemFlink>() as u64) << IOC_SIZESHIFT)
    | (DRM_IOCTL_BASE << IOC_TYPESHIFT)
    | (DRM_IOCTL_GEM_FLINK_NR << IOC_NRSHIFT))
    as libc::c_ulong;

const DMA_BUF_SYNC_READ: u64 = 1 << 0;
const DMA_BUF_SYNC_START: u64 = 0 << 2;
const DMA_BUF_SYNC_END: u64 = 1 << 2;
const DRM_FORMAT_MOD_INVALID_RAW: u64 = (1u64 << 56) - 1;

#[repr(C)]
struct DmaBufSync {
    flags: u64,
}

#[repr(C)]
#[derive(Default)]
struct DrmGemFlink {
    handle: u32,
    name: u32,
}

fn dma_buf_sync(fd: RawFd, flags: u64) {
    let mut sync = DmaBufSync { flags };
    unsafe {
        libc::ioctl(fd, DMA_BUF_IOCTL_SYNC as _, &mut sync);
    }
}

fn gem_flink(fd: BorrowedFd<'_>, handle: u32) -> io::Result<DrmGemFlink> {
    let mut flink = DrmGemFlink { handle, name: 0 };
    let ret = unsafe { libc::ioctl(fd.as_raw_fd(), DRM_IOCTL_GEM_FLINK, &mut flink) };
    if ret != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(flink)
}

pub struct KmsCard(File);

impl AsFd for KmsCard {
    fn as_fd(&self) -> BorrowedFd<'_> {
        self.0.as_fd()
    }
}

impl Device for KmsCard {}
impl ControlDevice for KmsCard {}

impl KmsCard {
    fn open(path: &str) -> io::Result<Self> {
        let file = OpenOptions::new().read(true).write(true).open(path)?;
        let card = Self(file);
        if let Err(err) = card.set_client_capability(ClientCapability::UniversalPlanes, true) {
            debug!("Failed to enable DRM universal planes on {path}: {err}");
        }
        if let Err(err) = card.set_client_capability(ClientCapability::Atomic, true) {
            debug!("Failed to enable DRM atomic mode-setting on {path}: {err}");
        }
        // Weylus only reads framebuffer contents, so it should not keep DRM master.
        let _ = card.release_master_lock();
        Ok(card)
    }
}

#[derive(Clone)]
struct ActiveOutput {
    connector_name: String,
    crtc_handle: crtc::Handle,
    x: u32,
    y: u32,
    width: u32,
    height: u32,
    fb_handle: framebuffer::Handle,
}

#[derive(Clone)]
pub struct KmsCapturable {
    device_path: String,
    connector_name: String,
    x: u32,
    y: u32,
    crtc_handle: crtc::Handle,
    width: u32,
    height: u32,
    desktop_x: u32,
    desktop_y: u32,
    desktop_width: u32,
    desktop_height: u32,
    fb_handle: framebuffer::Handle,
    capture_all: bool,
}

impl Capturable for KmsCapturable {
    fn as_any(&self) -> &dyn Any {
        self
    }

    fn name(&self) -> String {
        format!("KMS {} {}", self.device_path, self.connector_name)
    }

    fn geometry(&self) -> Result<Geometry, Box<dyn Error>> {
        if self.capture_all || self.desktop_width == 0 || self.desktop_height == 0 {
            return Ok(Geometry::Relative(0.0, 0.0, 1.0, 1.0));
        }

        Ok(Geometry::Relative(
            self.x.saturating_sub(self.desktop_x) as f64 / self.desktop_width as f64,
            self.y.saturating_sub(self.desktop_y) as f64 / self.desktop_height as f64,
            self.width as f64 / self.desktop_width as f64,
            self.height as f64 / self.desktop_height as f64,
        ))
    }

    fn before_input(&mut self) -> Result<(), Box<dyn Error>> {
        Ok(())
    }

    fn recorder(&self, capture_cursor: bool) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        if capture_cursor {
            warn!("KMS capture does not support cursor compositing yet, ignoring request.");
        }
        Ok(Box::new(KmsRecorder::new(self.clone())?))
    }

    fn recorder_with_preferences(
        &self,
        capture_cursor: bool,
        preferences: RecorderPreferences,
    ) -> Result<Box<dyn Recorder>, Box<dyn Error>> {
        if capture_cursor {
            warn!("KMS capture does not support cursor compositing yet, ignoring request.");
        }
        let mut recorder = KmsRecorder::new(self.clone())?;
        recorder.set_preferences(preferences);
        Ok(Box::new(recorder))
    }
}

fn probe_outputs(card: &KmsCard) -> Result<Vec<ActiveOutput>, Box<dyn Error>> {
    let resources = card.resource_handles()?;
    let mut outputs = Vec::new();

    for &connector_handle in resources.connectors() {
        let connector = card.get_connector(connector_handle, false)?;
        if connector.state() != connector::State::Connected {
            continue;
        }

        let encoder_handle = match connector.current_encoder() {
            Some(handle) => handle,
            None => continue,
        };
        let encoder = card.get_encoder(encoder_handle)?;
        let crtc_handle = match encoder.crtc() {
            Some(handle) => handle,
            None => continue,
        };
        let crtc = card.get_crtc(crtc_handle)?;
        let mode = match crtc.mode() {
            Some(mode) => mode,
            None => continue,
        };
        let fb_handle = match crtc.framebuffer() {
            Some(handle) => handle,
            None => continue,
        };

        let (width, height) = mode.size();
        outputs.push(ActiveOutput {
            connector_name: format!("{connector}"),
            crtc_handle,
            x: crtc.position().0,
            y: crtc.position().1,
            width: width as u32,
            height: height as u32,
            fb_handle,
        });
    }

    Ok(outputs)
}

fn probe_card(path: &str) -> Result<Vec<KmsCapturable>, Box<dyn Error>> {
    let card =
        KmsCard::open(path).map_err(|err| io_error(format!("Failed to open {path}: {err}")))?;
    let outputs = probe_outputs(&card)?;
    let desktop_x = outputs.iter().map(|output| output.x).min().unwrap_or(0);
    let desktop_y = outputs.iter().map(|output| output.y).min().unwrap_or(0);
    let desktop_width = outputs
        .iter()
        .map(|output| output.x.saturating_add(output.width))
        .max()
        .unwrap_or(desktop_x)
        .saturating_sub(desktop_x);
    let desktop_height = outputs
        .iter()
        .map(|output| output.y.saturating_add(output.height))
        .max()
        .unwrap_or(desktop_y)
        .saturating_sub(desktop_y);
    let mut capturables = Vec::new();

    if outputs.len() > 1 && desktop_width > 0 && desktop_height > 0 {
        if let Some(output) = outputs.iter().find(|output| {
            card.get_framebuffer(output.fb_handle)
                .map(|fb| {
                    let (width, height) = fb.size();
                    width >= desktop_width && height >= desktop_height
                })
                .unwrap_or(false)
        }) {
            capturables.push(KmsCapturable {
                device_path: path.to_string(),
                connector_name: "所有屏幕".to_string(),
                x: desktop_x,
                y: desktop_y,
                crtc_handle: output.crtc_handle,
                width: desktop_width,
                height: desktop_height,
                desktop_x,
                desktop_y,
                desktop_width,
                desktop_height,
                fb_handle: output.fb_handle,
                capture_all: true,
            });
        }
    }

    capturables.extend(outputs.into_iter().map(|output| KmsCapturable {
        device_path: path.to_string(),
        connector_name: output.connector_name,
        x: output.x,
        y: output.y,
        crtc_handle: output.crtc_handle,
        width: output.width,
        height: output.height,
        desktop_x,
        desktop_y,
        desktop_width,
        desktop_height,
        fb_handle: output.fb_handle,
        capture_all: false,
    }));
    Ok(capturables)
}

pub fn get_capturables(device_path: Option<&str>) -> Result<Vec<KmsCapturable>, Box<dyn Error>> {
    if let Some(path) = device_path {
        let capturables = probe_card(path)?;
        if capturables.is_empty() {
            return Err(Box::new(io_error(format!(
                "{path}: no active KMS outputs found"
            ))));
        }
        return Ok(capturables);
    }

    let mut entries: Vec<_> = fs::read_dir("/dev/dri")?
        .filter_map(|entry| entry.ok())
        .filter(|entry| {
            entry
                .file_name()
                .to_str()
                .is_some_and(|name| name.starts_with("card"))
        })
        .collect();
    entries.sort_by_key(|entry| entry.file_name());

    let mut capturables = Vec::new();
    let mut last_error: Option<Box<dyn Error>> = None;

    for entry in entries {
        let path = entry.path();
        let path = path.to_string_lossy().into_owned();
        match probe_card(&path) {
            Ok(mut found) => {
                if !found.is_empty() {
                    capturables.append(&mut found);
                }
            }
            Err(err) => {
                debug!("Failed to probe KMS outputs on {path}: {err}");
                last_error = Some(err);
            }
        }
    }

    if capturables.is_empty() {
        if let Some(err) = last_error {
            return Err(err);
        }
        return Err(Box::new(io_error(
            "No usable KMS outputs found under /dev/dri/card*",
        )));
    }

    Ok(capturables)
}

struct CachedBuffer {
    gem_handle: drm::buffer::Handle,
    close_handle: u32,
    ptr: *mut c_void,
    size: usize,
    format: DrmFourcc,
    pitch: u32,
    prime_fd: Option<OwnedFd>,
}

struct KmsFrameInfo {
    metadata: KmsFrameMetadata,
    prime_fds: Vec<OwnedFd>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct PlaneRegion {
    src_x: u32,
    src_y: u32,
    src_w: u32,
    src_h: u32,
    dst_w: u32,
    dst_h: u32,
}

struct FrameView<'a> {
    fb_handle: framebuffer::Handle,
    width: u32,
    height: u32,
    format: DrmFourcc,
    pitch: u32,
    raw: &'a [u8],
    prime_fd: Option<RawFd>,
}

struct KmsFrameSource {
    device_path: String,
    card: KmsCard,
    crtc_handle: crtc::Handle,
    default_fb: framebuffer::Handle,
    width: u32,
    height: u32,
    capture_all: bool,
    use_fb2: Option<bool>,
    use_prime: Option<bool>,
    force_map_mode: bool,
    last_fb_handle: Option<framebuffer::Handle>,
    logged_mapping: bool,
    cache: Vec<CachedBuffer>,
    egl: Option<*mut c_void>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct PlaneCandidate {
    plane_handle: plane::Handle,
    fb_handle: framebuffer::Handle,
    plane_type: Option<PlaneType>,
    zpos: i64,
    width: u32,
    height: u32,
    region: Option<PlaneRegion>,
}

impl KmsFrameSource {
    fn new(capturable: &KmsCapturable) -> Result<Self, Box<dyn Error>> {
        let card = KmsCard::open(&capturable.device_path).map_err(|err| {
            io_error(format!(
                "Failed to open KMS device {}: {err}",
                capturable.device_path
            ))
        })?;

        let use_prime = match std::env::var("WEYLUS_KMS_MAP").ok().as_deref() {
            Some("prime") => Some(true),
            Some("dumb") => Some(false),
            _ => None,
        };
        let force_map_mode = use_prime.is_some();

        Ok(Self {
            device_path: capturable.device_path.clone(),
            card,
            crtc_handle: capturable.crtc_handle,
            default_fb: capturable.fb_handle,
            width: capturable.width,
            height: capturable.height,
            capture_all: capturable.capture_all,
            use_fb2: None,
            use_prime,
            force_map_mode,
            last_fb_handle: None,
            logged_mapping: false,
            cache: Vec::new(),
            egl: None,
        })
    }

    fn frame(&mut self) -> Result<FrameView<'_>, Box<dyn Error>> {
        let (fb_handle, _region) = self.current_framebuffer()?;
        let gem_handle = self.get_gem_handle(fb_handle)?;

        let entry_index = match self
            .cache
            .iter()
            .position(|entry| entry.gem_handle == gem_handle)
        {
            Some(index) => index,
            None => {
                let entry = self.map_buffer(fb_handle)?;
                if self.cache.len() >= 4 {
                    let evicted = self.cache.remove(0);
                    self.evict_entry(evicted);
                }
                self.cache.push(entry);
                self.cache.len() - 1
            }
        };
        let entry = &self.cache[entry_index];
        let raw = unsafe { std::slice::from_raw_parts(entry.ptr.cast::<u8>(), entry.size) };
        Ok(FrameView {
            fb_handle,
            width: self.width,
            height: self.height,
            format: entry.format,
            pitch: entry.pitch,
            raw,
            prime_fd: entry.prime_fd.as_ref().map(|fd| fd.as_fd().as_raw_fd()),
        })
    }

    fn capture_bgra_with_helper(&mut self) -> Result<(Vec<u8>, usize, usize), Box<dyn Error>> {
        let (fb_handle, region) = self.current_framebuffer()?;
        let frame = self.frame_info(fb_handle, region)?;
        let egl = self.ensure_egl()?;
        let mut dst =
            vec![0u8; (frame.metadata.dst_w as usize) * (frame.metadata.dst_h as usize) * 4];
        let mut err = CError::new();
        let mut fds = [-1 as c_int; 4];
        for (idx, fd) in frame.prime_fds.iter().enumerate().take(4) {
            fds[idx] = fd.as_fd().as_raw_fd();
        }
        unsafe {
            kms_egl_read_bgra(
                egl,
                &frame.metadata,
                fds.as_ptr(),
                dst.as_mut_ptr().cast(),
                dst.len(),
                &mut err,
            );
        }
        if err.is_err() {
            return Err(Box::new(io_error(format!(
                "KMS EGL helper capture failed: {err}"
            ))));
        }
        Ok((
            dst,
            frame.metadata.dst_w as usize,
            frame.metadata.dst_h as usize,
        ))
    }

    fn drm_prime_frame(&mut self) -> Result<DmaBufFrame, Box<dyn Error>> {
        let (fb_handle, region) = self.current_framebuffer()?;
        let frame = self.frame_info(fb_handle, region)?;
        let metadata = frame.metadata;
        let plane_count = metadata.length as usize;
        if plane_count == 0 || plane_count > 4 || plane_count != frame.prime_fds.len() {
            return Err(Box::new(io_error(format!(
                "KMS DRM PRIME frame has unsupported plane count: metadata={} fds={}",
                plane_count,
                frame.prime_fds.len()
            ))));
        }
        if metadata.src_x != 0
            || metadata.src_y != 0
            || metadata.src_w != metadata.dst_w
            || metadata.src_h != metadata.dst_h
            || metadata.fb_width != metadata.dst_w
            || metadata.fb_height != metadata.dst_h
        {
            return Err(Box::new(io_error(format!(
                "KMS DRM PRIME zero-copy currently requires full-frame scanout, got src={}x{}+{}+{} dst={}x{} fb={}x{}",
                metadata.src_w,
                metadata.src_h,
                metadata.src_x,
                metadata.src_y,
                metadata.dst_w,
                metadata.dst_h,
                metadata.fb_width,
                metadata.fb_height
            ))));
        }
        if metadata.fourcc != DrmFourcc::Nv12 as u32 {
            return Err(Box::new(io_error(format!(
                "KMS DRM PRIME zero-copy currently only supports NV12 scanout, got {}",
                DrmFourcc::try_from(metadata.fourcc)
                    .map(|format| format.to_string())
                    .unwrap_or_else(|_| format!("0x{:08x}", metadata.fourcc))
            ))));
        }

        let mut objects = Vec::with_capacity(plane_count);
        let mut planes = Vec::with_capacity(plane_count);
        for (idx, fd) in frame.prime_fds.into_iter().enumerate() {
            let offset = metadata.offsets[idx] as usize;
            let pitch = metadata.pitches[idx] as usize;
            let size = offset.saturating_add(pitch.saturating_mul(metadata.fb_height as usize));
            objects.push(DmaBufObject {
                fd,
                size,
                format_modifier: metadata.modifier,
            });
            planes.push(DmaBufPlane {
                object_index: idx,
                offset: metadata.offsets[idx] as isize,
                pitch: metadata.pitches[idx] as isize,
            });
        }

        Ok(DmaBufFrame {
            width: metadata.dst_w as usize,
            height: metadata.dst_h as usize,
            objects,
            layers: vec![DmaBufLayer {
                format: metadata.fourcc,
                plane_count,
            }],
            planes,
        })
    }

    fn ensure_egl(&mut self) -> Result<*mut c_void, Box<dyn Error>> {
        if let Some(ctx) = self.egl {
            return Ok(ctx);
        }
        let path = CString::new(self.device_path.clone())?;
        let mut err = CError::new();
        let ctx = unsafe { init_kms_egl(path.as_ptr(), &mut err) };
        if ctx.is_null() || err.is_err() {
            return Err(Box::new(io_error(format!(
                "Failed to initialize KMS EGL helper: {}",
                if err.is_err() {
                    err.to_string()
                } else {
                    "null helper context".to_string()
                }
            ))));
        }
        self.egl = Some(ctx);
        Ok(ctx)
    }

    fn frame_info(
        &mut self,
        fb_handle: framebuffer::Handle,
        region: PlaneRegion,
    ) -> Result<KmsFrameInfo, Box<dyn Error>> {
        let info = match self.card.get_planar_framebuffer(fb_handle) {
            Ok(info) => info,
            Err(err) => {
                debug!("KMS GET_FB2 failed, falling back to GET_FB metadata: {err}");
                return self.frame_info_legacy(fb_handle, region);
            }
        };
        let (fb_width, fb_height) = info.size();

        let mut metadata = KmsFrameMetadata {
            length: 0,
            src_x: region.src_x,
            src_y: region.src_y,
            src_w: region.src_w,
            src_h: region.src_h,
            dst_w: region.dst_w,
            dst_h: region.dst_h,
            fb_width,
            fb_height,
            fourcc: info.pixel_format() as u32,
            modifier: info
                .modifier()
                .map(u64::from)
                .unwrap_or(DRM_FORMAT_MOD_INVALID_RAW),
            offsets: [0; 4],
            pitches: [0; 4],
        };

        let mut prime_fds: Vec<OwnedFd> = Vec::new();
        for (idx, handle) in info.buffers().iter().enumerate() {
            let Some(handle) = handle else { break };
            if idx >= 4 {
                break;
            }
            let fd = self
                .card
                .buffer_to_prime_fd(*handle, drm::CLOEXEC)
                .map_err(|err| io_error(format!("KMS PRIME export failed: {err}")))?;
            metadata.offsets[idx] = info.offsets()[idx];
            metadata.pitches[idx] = info.pitches()[idx];
            prime_fds.push(fd);
            metadata.length += 1;
        }
        if metadata.length == 0 {
            return Err(Box::new(io_error(
                "KMS framebuffer did not expose any dma-buf planes",
            )));
        }
        Ok(KmsFrameInfo {
            metadata,
            prime_fds,
        })
    }

    fn frame_info_legacy(
        &mut self,
        fb_handle: framebuffer::Handle,
        region: PlaneRegion,
    ) -> Result<KmsFrameInfo, Box<dyn Error>> {
        let info = self
            .card
            .get_framebuffer(fb_handle)
            .map_err(|err| io_error(format!("KMS GET_FB failed: {err}")))?;
        let (fb_width, fb_height) = info.size();
        let gem_handle = info.buffer().ok_or_else(|| {
            Box::new(io_error(
                "KMS framebuffer handle unavailable; CAP_SYS_ADMIN may be required",
            )) as Box<dyn Error>
        })?;

        let fourcc = match (info.bpp(), info.depth()) {
            (32, 24) => DrmFourcc::Xrgb8888,
            (32, 32) => DrmFourcc::Argb8888,
            (16, 16) => DrmFourcc::Rgb565,
            (bpp, depth) => {
                return Err(Box::new(io_error(format!(
                    "Unsupported KMS legacy framebuffer format: {bpp}bpp depth={depth}"
                ))))
            }
        };

        let prime_fd = self
            .card
            .buffer_to_prime_fd(gem_handle, drm::CLOEXEC)
            .map_err(|err| io_error(format!("KMS PRIME export failed: {err}")))?;

        Ok(KmsFrameInfo {
            metadata: KmsFrameMetadata {
                length: 1,
                src_x: region.src_x,
                src_y: region.src_y,
                src_w: region.src_w,
                src_h: region.src_h,
                dst_w: region.dst_w,
                dst_h: region.dst_h,
                fb_width,
                fb_height,
                fourcc: fourcc as u32,
                modifier: DRM_FORMAT_MOD_INVALID_RAW,
                offsets: [0; 4],
                pitches: [info.pitch(), 0, 0, 0],
            },
            prime_fds: vec![prime_fd],
        })
    }

    fn current_framebuffer(
        &mut self,
    ) -> Result<(framebuffer::Handle, PlaneRegion), Box<dyn Error>> {
        let crtc = self
            .card
            .get_crtc(self.crtc_handle)
            .map_err(|err| io_error(format!("Failed to query KMS CRTC: {err}")))?;
        let legacy_fb = crtc.framebuffer().unwrap_or(self.default_fb);
        let legacy_region = PlaneRegion {
            src_x: crtc.position().0,
            src_y: crtc.position().1,
            src_w: self.width,
            src_h: self.height,
            dst_w: self.width,
            dst_h: self.height,
        };
        let (plane, plane_debug) = self.scanout_plane_framebuffer()?;
        let fb_handle = plane
            .as_ref()
            .map(|candidate| candidate.fb_handle)
            .unwrap_or(legacy_fb);
        let mut region = plane
            .as_ref()
            .and_then(|candidate| candidate.region)
            .unwrap_or(legacy_region);
        if self.capture_all {
            region = PlaneRegion {
                src_x: 0,
                src_y: 0,
                src_w: self.width,
                src_h: self.height,
                dst_w: self.width,
                dst_h: self.height,
            };
        }

        if self.last_fb_handle != Some(fb_handle) {
            debug!(
                "KMS selected framebuffer {} for CRTC {} src={}x{}+{}+{} dst={}x{} candidates=[{}]",
                u32::from(fb_handle),
                u32::from(self.crtc_handle),
                region.src_w,
                region.src_h,
                region.src_x,
                region.src_y,
                region.dst_w,
                region.dst_h,
                plane_debug
            );
            self.last_fb_handle = Some(fb_handle);
        }

        Ok((fb_handle, region))
    }

    fn scanout_plane_framebuffer(
        &self,
    ) -> Result<(Option<PlaneCandidate>, String), Box<dyn Error>> {
        let mut candidates = Vec::new();
        for plane_handle in self.card.plane_handles()? {
            let info = self.card.get_plane(plane_handle)?;
            if info.crtc() != Some(self.crtc_handle) {
                continue;
            }
            let Some(fb_handle) = info.framebuffer() else {
                continue;
            };
            let plane_type = self.plane_type(plane_handle)?;
            if plane_type == Some(PlaneType::Cursor) {
                continue;
            }
            let zpos = self.plane_zpos(plane_handle)?.unwrap_or(0);
            let (width, height) = self
                .card
                .get_framebuffer(fb_handle)
                .map(|fb| fb.size())
                .unwrap_or((0, 0));
            let region = self.plane_region(plane_handle, width, height)?;
            candidates.push(PlaneCandidate {
                plane_handle,
                fb_handle,
                plane_type,
                zpos,
                width,
                height,
                region,
            });
        }

        let debug_info = if candidates.is_empty() {
            "none".to_string()
        } else {
            candidates
                .iter()
                .map(|candidate| {
                    let region = candidate.region.map_or_else(
                        || "region=unknown".to_string(),
                        |region| {
                            format!(
                                "src={}x{}+{}+{} dst={}x{}",
                                region.src_w,
                                region.src_h,
                                region.src_x,
                                region.src_y,
                                region.dst_w,
                                region.dst_h
                            )
                        },
                    );
                    format!(
                        "plane={} type={:?} zpos={} fb={} {}x{} {}",
                        u32::from(candidate.plane_handle),
                        candidate.plane_type,
                        candidate.zpos,
                        u32::from(candidate.fb_handle),
                        candidate.width,
                        candidate.height,
                        region,
                    )
                })
                .collect::<Vec<_>>()
                .join("; ")
        };

        candidates.sort_by_key(|candidate| {
            (
                candidate.width == self.width && candidate.height == self.height,
                candidate.zpos,
                matches!(candidate.plane_type, Some(PlaneType::Overlay)),
                matches!(candidate.plane_type, Some(PlaneType::Primary)),
            )
        });

        Ok((candidates.last().copied(), debug_info))
    }

    fn plane_type(&self, plane_handle: plane::Handle) -> Result<Option<PlaneType>, Box<dyn Error>> {
        let props = self.card.get_properties(plane_handle)?;
        for (prop_handle, raw_value) in props.iter() {
            let info = self.card.get_property(*prop_handle)?;
            if info.name().to_bytes() != b"type" {
                continue;
            }
            let value_type = info.value_type();
            let value = value_type.convert_value(*raw_value);
            let Some(value) = value.as_enum() else {
                continue;
            };
            let plane_type = match value.name().to_bytes() {
                b"Primary" => Some(PlaneType::Primary),
                b"Overlay" => Some(PlaneType::Overlay),
                b"Cursor" => Some(PlaneType::Cursor),
                _ => None,
            };
            return Ok(plane_type);
        }
        Ok(None)
    }

    fn plane_region(
        &self,
        plane_handle: plane::Handle,
        fb_width: u32,
        fb_height: u32,
    ) -> Result<Option<PlaneRegion>, Box<dyn Error>> {
        let props = self.card.get_properties(plane_handle)?;
        let mut crtc_w = None;
        let mut crtc_h = None;
        let mut src_x = None;
        let mut src_y = None;
        let mut src_w = None;
        let mut src_h = None;

        for (prop_handle, raw_value) in props.iter() {
            let info = self.card.get_property(*prop_handle)?;
            let name = info.name().to_bytes();
            let value_type = info.value_type();
            let value = value_type.convert_value(*raw_value);
            let raw = value
                .as_unsigned_range()
                .or_else(|| value.as_signed_range().map(|value| value.max(0) as u64))
                .or_else(|| match value {
                    drm::control::property::Value::Unknown(raw) => Some(raw),
                    _ => None,
                });
            let Some(raw) = raw else { continue };

            match name {
                b"CRTC_W" => crtc_w = Some(raw as u32),
                b"CRTC_H" => crtc_h = Some(raw as u32),
                b"SRC_X" => src_x = Some((raw >> 16) as u32),
                b"SRC_Y" => src_y = Some((raw >> 16) as u32),
                b"SRC_W" => src_w = Some((raw >> 16) as u32),
                b"SRC_H" => src_h = Some((raw >> 16) as u32),
                _ => {}
            }
        }

        let src_x = src_x.unwrap_or(0).min(fb_width);
        let src_y = src_y.unwrap_or(0).min(fb_height);
        let max_w = fb_width.saturating_sub(src_x);
        let max_h = fb_height.saturating_sub(src_y);
        let src_w = src_w.unwrap_or(max_w).min(max_w);
        let src_h = src_h.unwrap_or(max_h).min(max_h);
        let dst_w = crtc_w.unwrap_or(self.width);
        let dst_h = crtc_h.unwrap_or(self.height);

        if src_w == 0 || src_h == 0 || dst_w == 0 || dst_h == 0 {
            return Ok(None);
        }

        Ok(Some(PlaneRegion {
            src_x,
            src_y,
            src_w,
            src_h,
            dst_w,
            dst_h,
        }))
    }

    fn plane_zpos(&self, plane_handle: plane::Handle) -> Result<Option<i64>, Box<dyn Error>> {
        let props = self.card.get_properties(plane_handle)?;
        for (prop_handle, raw_value) in props.iter() {
            let info = self.card.get_property(*prop_handle)?;
            if info.name().to_bytes() != b"zpos" {
                continue;
            }
            let value_type = info.value_type();
            let value = value_type.convert_value(*raw_value);
            return Ok(value
                .as_signed_range()
                .or_else(|| value.as_unsigned_range().map(|value| value as i64)));
        }
        Ok(None)
    }

    fn using_prime(&self) -> bool {
        self.use_prime == Some(true)
    }

    fn map_mode_forced(&self) -> bool {
        self.force_map_mode
    }

    fn switch_map_mode(&mut self, use_prime: bool) {
        if self.use_prime == Some(use_prime) {
            return;
        }
        let entries: Vec<_> = self.cache.drain(..).collect();
        for entry in entries {
            self.evict_entry(entry);
        }
        self.use_prime = Some(use_prime);
        self.logged_mapping = false;
    }

    fn get_gem_handle(
        &self,
        fb_handle: framebuffer::Handle,
    ) -> Result<drm::buffer::Handle, Box<dyn Error>> {
        match self.use_fb2 {
            Some(true) | None => {
                if let Ok(info) = self.card.get_planar_framebuffer(fb_handle) {
                    if let Some(handle) = info.buffers()[0] {
                        return Ok(handle);
                    }
                }
                if self.use_fb2 == Some(true) {
                    return Err(Box::new(io_error(
                        "KMS GET_FB2 did not expose a GEM handle",
                    )));
                }
            }
            Some(false) => {}
        }

        let info = self
            .card
            .get_framebuffer(fb_handle)
            .map_err(|err| io_error(format!("KMS GET_FB failed: {err}")))?;
        info.buffer().ok_or_else(|| {
            Box::new(io_error(
                "KMS framebuffer handle unavailable; CAP_SYS_ADMIN may be required",
            )) as Box<dyn Error>
        })
    }

    fn map_buffer(
        &mut self,
        fb_handle: framebuffer::Handle,
    ) -> Result<CachedBuffer, Box<dyn Error>> {
        match self.use_fb2 {
            Some(true) | None => match self.map_fb2(fb_handle) {
                Ok(entry) => {
                    self.use_fb2 = Some(true);
                    return Ok(entry);
                }
                Err(err) => {
                    if self.use_fb2 == Some(true) {
                        return Err(err);
                    }
                    debug!("KMS GET_FB2 mapping failed, falling back to GET_FB: {err}");
                }
            },
            Some(false) => {}
        }

        let entry = self.map_fb1(fb_handle)?;
        self.use_fb2 = Some(false);
        Ok(entry)
    }

    fn map_fb2(&mut self, fb_handle: framebuffer::Handle) -> Result<CachedBuffer, Box<dyn Error>> {
        let info = self
            .card
            .get_planar_framebuffer(fb_handle)
            .map_err(|err| io_error(format!("KMS GET_FB2 failed: {err}")))?;

        if let Some(modifier) = info.modifier() {
            if modifier != DrmModifier::Linear {
                return Err(Box::new(io_error(format!(
                    "KMS framebuffer uses non-linear modifier {modifier:?}"
                ))));
            }
        }

        let gem_handle = info.buffers()[0].ok_or_else(|| {
            Box::new(io_error(
                "KMS framebuffer does not expose a plane buffer handle",
            )) as Box<dyn Error>
        })?;
        let pitch = info.pitches()[0];
        let format = info.pixel_format();

        self.map_gem(gem_handle, pitch, format)
    }

    fn map_fb1(&mut self, fb_handle: framebuffer::Handle) -> Result<CachedBuffer, Box<dyn Error>> {
        let info = self
            .card
            .get_framebuffer(fb_handle)
            .map_err(|err| io_error(format!("KMS GET_FB failed: {err}")))?;
        let gem_handle = info.buffer().ok_or_else(|| {
            Box::new(io_error(
                "KMS framebuffer handle unavailable; CAP_SYS_ADMIN may be required",
            )) as Box<dyn Error>
        })?;

        let format = match (info.bpp(), info.depth()) {
            (32, 24) => DrmFourcc::Xrgb8888,
            (32, 32) => DrmFourcc::Argb8888,
            (16, 16) => DrmFourcc::Rgb565,
            (bpp, depth) => {
                return Err(Box::new(io_error(format!(
                    "Unsupported KMS framebuffer format: {bpp}bpp depth={depth}"
                ))))
            }
        };

        self.map_gem(gem_handle, info.pitch(), format)
    }

    fn map_gem(
        &mut self,
        gem_handle: drm::buffer::Handle,
        pitch: u32,
        format: DrmFourcc,
    ) -> Result<CachedBuffer, Box<dyn Error>> {
        let size = self.height as usize * pitch as usize;

        match self.use_prime {
            Some(false) | None => match self.map_dumb(gem_handle, size, format, pitch) {
                Ok(entry) => {
                    if !self.logged_mapping {
                        debug!(
                            "KMS using dumb-map framebuffer: gem_handle={} pitch={} format={:?} size={}",
                            u32::from(gem_handle),
                            pitch,
                            format,
                            size
                        );
                        self.logged_mapping = true;
                    }
                    self.use_prime = Some(false);
                    return Ok(entry);
                }
                Err(err) => {
                    if self.use_prime == Some(false) {
                        return Err(err);
                    }
                    debug!("KMS dumb-map failed, falling back to PRIME mmap: {err}");
                }
            },
            Some(true) => {}
        }

        let entry = self.map_prime(gem_handle, size, format, pitch)?;
        if !self.logged_mapping {
            debug!(
                "KMS using PRIME-mmap framebuffer: gem_handle={} pitch={} format={:?} size={}",
                u32::from(gem_handle),
                pitch,
                format,
                size
            );
            self.logged_mapping = true;
        }
        self.use_prime = Some(true);
        Ok(entry)
    }

    fn map_prime(
        &self,
        gem_handle: drm::buffer::Handle,
        size: usize,
        format: DrmFourcc,
        pitch: u32,
    ) -> Result<CachedBuffer, Box<dyn Error>> {
        let reopened = self.reopen_gem_handle(gem_handle)?;
        let prime_fd = self
            .card
            .buffer_to_prime_fd(
                drm::buffer::Handle::from(
                    NonZeroU32::new(reopened.handle)
                        .ok_or_else(|| io_error("Failed to convert reopened GEM handle"))?,
                ),
                drm::RDWR,
            )
            .map_err(|err| io_error(format!("KMS PRIME export failed: {err}")))?;

        let ptr = unsafe {
            mm::mmap(
                ptr::null_mut(),
                size,
                ProtFlags::READ,
                MapFlags::SHARED,
                &prime_fd,
                0,
            )
            .map_err(|err| io_error(format!("KMS PRIME mmap failed: {err}")))?
        };

        Ok(CachedBuffer {
            gem_handle,
            close_handle: reopened.handle,
            ptr,
            size,
            format,
            pitch,
            prime_fd: Some(prime_fd),
        })
    }

    fn map_dumb(
        &self,
        gem_handle: drm::buffer::Handle,
        _size: usize,
        format: DrmFourcc,
        pitch: u32,
    ) -> Result<CachedBuffer, Box<dyn Error>> {
        let reopened = self.reopen_gem_handle(gem_handle)?;
        let mapping = drm_ffi::mode::dumbbuffer::map(self.card.as_fd(), reopened.handle, 0, 0)
            .map_err(|err| io_error(format!("DRM_IOCTL_MODE_MAP_DUMB failed: {err}")))?;
        let mmap_size = reopened.size as usize;

        let ptr = unsafe {
            mm::mmap(
                ptr::null_mut(),
                mmap_size,
                ProtFlags::READ,
                MapFlags::SHARED,
                self.card.as_fd(),
                mapping.offset,
            )
            .map_err(|err| io_error(format!("KMS dumb-buffer mmap failed: {err}")))?
        };

        Ok(CachedBuffer {
            gem_handle,
            close_handle: reopened.handle,
            ptr,
            size: mmap_size,
            format,
            pitch,
            prime_fd: None,
        })
    }

    fn evict_entry(&self, entry: CachedBuffer) {
        unsafe {
            let _ = mm::munmap(entry.ptr, entry.size);
        }
        let _ = drm_ffi::gem::close(self.card.as_fd(), entry.close_handle);
    }

    fn reopen_gem_handle(
        &self,
        gem_handle: drm::buffer::Handle,
    ) -> Result<drm_ffi::drm_sys::drm_gem_open, Box<dyn Error>> {
        let flink = gem_flink(self.card.as_fd(), u32::from(gem_handle))
            .map_err(|err| io_error(format!("DRM_IOCTL_GEM_FLINK failed: {err}")))?;
        drm_ffi::gem::open(self.card.as_fd(), flink.name)
            .map_err(|err| io_error(format!("DRM_IOCTL_GEM_OPEN failed: {err}")).into())
    }
}

impl Drop for KmsFrameSource {
    fn drop(&mut self) {
        for entry in self.cache.drain(..) {
            unsafe {
                let _ = mm::munmap(entry.ptr, entry.size);
            }
            let _ = drm_ffi::gem::close(self.card.as_fd(), entry.close_handle);
        }
        if let Some(ctx) = self.egl.take() {
            unsafe { destroy_kms_egl(ctx) };
        }
    }
}

pub struct KmsRecorder {
    frame_source: KmsFrameSource,
    converted_frame: Vec<u8>,
    logged_sample: bool,
    retried_with_prime: bool,
    helper_disabled: bool,
    prefer_drm_prime: bool,
    drm_prime_disabled: bool,
}

impl KmsRecorder {
    fn new(capturable: KmsCapturable) -> Result<Self, Box<dyn Error>> {
        Ok(Self {
            frame_source: KmsFrameSource::new(&capturable)?,
            converted_frame: Vec::new(),
            logged_sample: false,
            retried_with_prime: false,
            helper_disabled: false,
            prefer_drm_prime: false,
            drm_prime_disabled: false,
        })
    }

    fn capture_helper_frame(&mut self) -> Result<Option<(usize, usize)>, Box<dyn Error>> {
        if self.helper_disabled {
            return Ok(None);
        }
        let (frame, width, height) = self.frame_source.capture_bgra_with_helper()?;
        self.converted_frame = frame;
        Ok(Some((width, height)))
    }
}

impl Recorder for KmsRecorder {
    fn backend_name(&self) -> &'static str {
        "KMS/DRM"
    }

    fn set_preferences(&mut self, preferences: RecorderPreferences) {
        self.prefer_drm_prime = preferences.prefer_drm_prime;
    }

    fn capture_frame(&mut self) -> Result<CapturedFrame<'_>, Box<dyn Error>> {
        if self.prefer_drm_prime && !self.drm_prime_disabled {
            match self.frame_source.drm_prime_frame() {
                Ok(frame) => return Ok(CapturedFrame::DrmPrime(frame)),
                Err(err) => {
                    debug!(
                        "KMS DRM PRIME zero-copy unavailable for this frame, using CPU path: {err}"
                    );
                    self.drm_prime_disabled = true;
                }
            }
        }
        Ok(CapturedFrame::Cpu(self.capture()?))
    }

    fn capture(&mut self) -> Result<PixelProvider<'_>, Box<dyn Error>> {
        match self.capture_helper_frame() {
            Ok(Some((width, height))) => {
                return Ok(PixelProvider::BGR0(width, height, &self.converted_frame));
            }
            Ok(None) => {}
            Err(err) => {
                warn!("KMS EGL helper failed, falling back to direct framebuffer mapping: {err}");
                self.helper_disabled = true;
            }
        }
        let map_mode_forced = self.frame_source.map_mode_forced();
        let using_prime = self.frame_source.using_prime();
        let mut sample_info = None;
        let mut retry_fb_handle = None;
        let frame_size;

        {
            let frame = self.frame_source.frame()?;
            frame_size = (frame.width as usize, frame.height as usize);
            if let Some(prime_fd) = frame.prime_fd {
                dma_buf_sync(prime_fd, DMA_BUF_SYNC_START | DMA_BUF_SYNC_READ);
            }
            let result = convert_to_bgr0(
                &mut self.converted_frame,
                frame.raw,
                frame.width,
                frame.height,
                frame.pitch,
                frame.format,
            );
            if let Some(prime_fd) = frame.prime_fd {
                dma_buf_sync(prime_fd, DMA_BUF_SYNC_END | DMA_BUF_SYNC_READ);
            }
            result?;

            if !self.logged_sample {
                let raw_sample_len = frame.raw.len().min(32);
                let converted_sample_len = self.converted_frame.len().min(32);
                let raw_sample = frame.raw[..raw_sample_len].to_vec();
                let converted_sample = self.converted_frame[..converted_sample_len].to_vec();
                let raw_nonzero = raw_sample.iter().filter(|byte| **byte != 0).count();
                let converted_nonzero = converted_sample.iter().filter(|byte| **byte != 0).count();
                sample_info = Some((
                    u32::from(frame.fb_handle),
                    frame.format,
                    frame.pitch,
                    raw_nonzero,
                    raw_sample_len,
                    raw_sample,
                    converted_nonzero,
                    converted_sample_len,
                    converted_sample,
                ));
            }

            if !self.retried_with_prime
                && !map_mode_forced
                && !using_prime
                && converted_frame_looks_solid(
                    &self.converted_frame,
                    frame.width as usize,
                    frame.height as usize,
                )
            {
                retry_fb_handle = Some(u32::from(frame.fb_handle));
            }
        }

        if let Some(fb_handle) = retry_fb_handle {
            debug!(
                "KMS dumb-map sample looks like a solid frame on fb={}, retrying with PRIME mmap",
                fb_handle
            );
            self.retried_with_prime = true;
            self.frame_source.switch_map_mode(true);
            return self.capture();
        }

        if let Some((
            fb_handle,
            format,
            pitch,
            raw_nonzero,
            raw_sample_len,
            raw_sample,
            converted_nonzero,
            converted_sample_len,
            converted_sample,
        )) = sample_info
        {
            debug!(
                "KMS sample fb={} format={:?} pitch={} raw_nonzero={}/{} raw={:02x?} converted_nonzero={}/{} converted={:02x?}",
                fb_handle,
                format,
                pitch,
                raw_nonzero,
                raw_sample_len,
                raw_sample,
                converted_nonzero,
                converted_sample_len,
                converted_sample,
            );
            self.logged_sample = true;
        }
        Ok(PixelProvider::BGR0(
            frame_size.0,
            frame_size.1,
            &self.converted_frame,
        ))
    }
}

unsafe impl Send for KmsRecorder {}

fn converted_frame_looks_solid(buf: &[u8], width: usize, height: usize) -> bool {
    if width == 0 || height == 0 || buf.len() < width * height * 4 {
        return false;
    }

    let coords = [
        (0usize, 0usize),
        (width / 2, height / 2),
        (width.saturating_sub(1), height.saturating_sub(1)),
        (width / 3, height / 3),
        (width * 2 / 3, height * 2 / 3),
    ];

    let first = pixel_at(buf, width, coords[0].0, coords[0].1);
    let Some(first) = first else {
        return false;
    };

    let all_same = coords
        .iter()
        .all(|&(x, y)| pixel_at(buf, width, x, y) == Some(first));
    if !all_same {
        return false;
    }

    matches!(first, [0x00, 0x00, 0x00, 0xff] | [0xff, 0xff, 0xff, 0xff])
}

fn pixel_at(buf: &[u8], width: usize, x: usize, y: usize) -> Option<[u8; 4]> {
    let offset = (y.checked_mul(width)?).checked_add(x)?.checked_mul(4)?;
    let bytes = buf.get(offset..offset + 4)?;
    Some([bytes[0], bytes[1], bytes[2], bytes[3]])
}

fn convert_to_bgr0(
    dst: &mut Vec<u8>,
    src: &[u8],
    width: u32,
    height: u32,
    pitch: u32,
    format: DrmFourcc,
) -> Result<(), Box<dyn Error>> {
    match format {
        DrmFourcc::Xrgb8888 | DrmFourcc::Argb8888 => {
            let row_bytes = (width * 4) as usize;
            let total = row_bytes * height as usize;
            dst.clear();
            dst.reserve(total);
            if pitch as usize == row_bytes {
                dst.extend_from_slice(&src[..total]);
            } else {
                for y in 0..height as usize {
                    let row_start = y * pitch as usize;
                    dst.extend_from_slice(&src[row_start..row_start + row_bytes]);
                }
            }
            Ok(())
        }
        DrmFourcc::Xbgr8888 | DrmFourcc::Abgr8888 => {
            let total = (width * height * 4) as usize;
            dst.clear();
            dst.reserve(total);
            for y in 0..height {
                let row = &src[(y * pitch) as usize..];
                for x in 0..width as usize {
                    let offset = x * 4;
                    dst.push(row[offset + 2]);
                    dst.push(row[offset + 1]);
                    dst.push(row[offset]);
                    dst.push(0xff);
                }
            }
            Ok(())
        }
        DrmFourcc::Rgb565 => {
            let total = (width * height * 4) as usize;
            dst.clear();
            dst.reserve(total);
            for y in 0..height {
                let row = &src[(y * pitch) as usize..];
                for x in 0..width as usize {
                    let offset = x * 2;
                    let lo = row[offset] as u16;
                    let hi = row[offset + 1] as u16;
                    let pixel = lo | (hi << 8);
                    let r = ((pixel >> 11) & 0x1f) as u8;
                    let g = ((pixel >> 5) & 0x3f) as u8;
                    let b = (pixel & 0x1f) as u8;
                    dst.push((b << 3) | (b >> 2));
                    dst.push((g << 2) | (g >> 4));
                    dst.push((r << 3) | (r >> 2));
                    dst.push(0xff);
                }
            }
            Ok(())
        }
        other => Err(Box::new(io_error(format!(
            "Unsupported KMS pixel format {other:?}"
        )))),
    }
}
