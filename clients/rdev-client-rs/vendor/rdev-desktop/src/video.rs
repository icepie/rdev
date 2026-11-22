use std::ffi::CStr;
#[cfg(target_os = "linux")]
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::raw::{c_char, c_int, c_uchar, c_void};
use std::time::Instant;

use tracing::warn;

use crate::cerror::CError;

extern "C" {
    fn init_video_encoder(
        rust_ctx: *mut c_void,
        width_in: c_int,
        height_in: c_int,
        width_out: c_int,
        height_out: c_int,
        try_vaapi: c_int,
        try_nvenc: c_int,
        try_vulkan_video: c_int,
        try_videotoolbox: c_int,
        try_mediafoundation: c_int,
    ) -> *mut c_void;
    fn open_video(handle: *mut c_void, err: *mut CError);
    fn destroy_video_encoder(handle: *mut c_void);
    fn video_encoder_codec_name(handle: *mut c_void) -> *const c_char;
    fn video_encoder_supports_drm_prime(handle: *mut c_void) -> c_int;
    fn encode_video_frame(handle: *mut c_void, micros: c_int, err: *mut CError);

    fn fill_rgb(ctx: *mut c_void, data: *const u8, err: *mut CError);
    fn fill_rgb0(ctx: *mut c_void, data: *const u8, err: *mut CError);
    fn fill_bgr0(ctx: *mut c_void, data: *const u8, stride: c_int, err: *mut CError);
    #[cfg(target_os = "linux")]
    fn fill_drm_prime(
        ctx: *mut c_void,
        width: c_int,
        height: c_int,
        object_count: c_int,
        object_fds: *const c_int,
        object_sizes: *const usize,
        object_modifiers: *const u64,
        layer_count: c_int,
        layer_formats: *const u32,
        layer_plane_counts: *const c_int,
        plane_object_indices: *const c_int,
        plane_offsets: *const isize,
        plane_pitches: *const isize,
        err: *mut CError,
    );
}

// this is used as callback in lib/encode_video.c via ffmpegs AVIOContext
#[no_mangle]
fn write_video_packet(video_encoder: *mut c_void, buf: *const c_uchar, buf_size: c_int) -> c_int {
    let video_encoder = unsafe { (video_encoder as *mut VideoEncoder).as_mut().unwrap() };
    (video_encoder.write_data)(unsafe {
        std::slice::from_raw_parts(buf as *const u8, buf_size as usize)
    });
    0
}

pub enum PixelProvider<'a> {
    // 8 bits per color
    RGB(usize, usize, &'a [u8]),
    RGB0(usize, usize, &'a [u8]),
    BGR0(usize, usize, &'a [u8]),
    // width, height, stride
    BGR0S(usize, usize, usize, &'a [u8]),
}

impl<'a> PixelProvider<'a> {
    pub fn size(&self) -> (usize, usize) {
        match self {
            PixelProvider::RGB(w, h, _) => (*w, *h),
            PixelProvider::RGB0(w, h, _) => (*w, *h),
            PixelProvider::BGR0(w, h, _) => (*w, *h),
            PixelProvider::BGR0S(w, h, _, _) => (*w, *h),
        }
    }
}

#[cfg(target_os = "linux")]
pub struct DmaBufObject {
    pub fd: OwnedFd,
    pub size: usize,
    pub format_modifier: u64,
}

#[cfg(target_os = "linux")]
pub struct DmaBufLayer {
    pub format: u32,
    pub plane_count: usize,
}

#[cfg(target_os = "linux")]
pub struct DmaBufPlane {
    pub object_index: usize,
    pub offset: isize,
    pub pitch: isize,
}

#[cfg(target_os = "linux")]
pub struct DmaBufFrame {
    pub width: usize,
    pub height: usize,
    pub objects: Vec<DmaBufObject>,
    pub layers: Vec<DmaBufLayer>,
    pub planes: Vec<DmaBufPlane>,
}

pub enum CapturedFrame<'a> {
    Cpu(PixelProvider<'a>),
    #[cfg(target_os = "linux")]
    DrmPrime(DmaBufFrame),
}

impl<'a> CapturedFrame<'a> {
    pub fn size(&self) -> (usize, usize) {
        match self {
            CapturedFrame::Cpu(pixel_provider) => pixel_provider.size(),
            #[cfg(target_os = "linux")]
            CapturedFrame::DrmPrime(frame) => (frame.width, frame.height),
        }
    }

    pub fn is_drm_prime(&self) -> bool {
        match self {
            CapturedFrame::Cpu(_) => false,
            #[cfg(target_os = "linux")]
            CapturedFrame::DrmPrime(_) => true,
        }
    }
}

#[derive(Clone, Copy)]
pub struct EncoderOptions {
    pub try_vaapi: bool,
    pub try_nvenc: bool,
    pub try_vulkan_video: bool,
    pub try_videotoolbox: bool,
    pub try_mediafoundation: bool,
}

pub struct VideoEncoder {
    handle: *mut c_void,
    width_in: usize,
    height_in: usize,
    width_out: usize,
    height_out: usize,
    write_data: Box<dyn FnMut(&[u8])>,
    start_time: Instant,
}

impl VideoEncoder {
    pub fn new(
        width_in: usize,
        height_in: usize,
        width_out: usize,
        height_out: usize,
        mut write_data: impl FnMut(&[u8]) + 'static,
        options: EncoderOptions,
    ) -> Result<Box<Self>, CError> {
        let mut video_encoder = Box::new(Self {
            handle: std::ptr::null_mut(),
            width_in,
            height_in,
            width_out,
            height_out,
            write_data: Box::new(move |data| write_data(data)),
            start_time: Instant::now(),
        });
        let handle = unsafe {
            init_video_encoder(
                video_encoder.as_mut() as *mut _ as *mut c_void,
                width_in as c_int,
                height_in as c_int,
                width_out as c_int,
                height_out as c_int,
                options.try_vaapi.into(),
                options.try_nvenc.into(),
                options.try_vulkan_video.into(),
                options.try_videotoolbox.into(),
                options.try_mediafoundation.into(),
            )
        };
        video_encoder.handle = handle;

        let mut err = CError::new();
        unsafe { open_video(video_encoder.handle, &mut err) };
        if err.is_err() {
            return Err(err);
        }
        Ok(video_encoder)
    }

    pub fn encode(&mut self, frame: CapturedFrame<'_>) -> Result<(), CError> {
        let mut err = CError::new();
        match frame {
            CapturedFrame::Cpu(pixel_provider) => match pixel_provider {
                PixelProvider::BGR0(w, _, bgr0) => unsafe {
                    fill_bgr0(self.handle, bgr0.as_ptr(), (w * 4) as c_int, &mut err);
                },
                PixelProvider::BGR0S(_, _, stride, bgr0) => unsafe {
                    fill_bgr0(self.handle, bgr0.as_ptr(), stride as c_int, &mut err);
                },
                PixelProvider::RGB(_, _, rgb) => unsafe {
                    fill_rgb(self.handle, rgb.as_ptr(), &mut err);
                },
                PixelProvider::RGB0(_, _, rgb) => unsafe {
                    fill_rgb0(self.handle, rgb.as_ptr(), &mut err);
                },
            },
            #[cfg(target_os = "linux")]
            CapturedFrame::DrmPrime(frame) => {
                let object_fds: Vec<c_int> = frame
                    .objects
                    .iter()
                    .map(|object| object.fd.as_raw_fd())
                    .collect();
                let object_sizes: Vec<usize> =
                    frame.objects.iter().map(|object| object.size).collect();
                let object_modifiers: Vec<u64> = frame
                    .objects
                    .iter()
                    .map(|object| object.format_modifier)
                    .collect();
                let layer_formats: Vec<u32> =
                    frame.layers.iter().map(|layer| layer.format).collect();
                let layer_plane_counts: Vec<c_int> = frame
                    .layers
                    .iter()
                    .map(|layer| layer.plane_count as c_int)
                    .collect();
                let plane_object_indices: Vec<c_int> = frame
                    .planes
                    .iter()
                    .map(|plane| plane.object_index as c_int)
                    .collect();
                let plane_offsets: Vec<isize> =
                    frame.planes.iter().map(|plane| plane.offset).collect();
                let plane_pitches: Vec<isize> =
                    frame.planes.iter().map(|plane| plane.pitch).collect();
                unsafe {
                    fill_drm_prime(
                        self.handle,
                        frame.width as c_int,
                        frame.height as c_int,
                        object_fds.len() as c_int,
                        object_fds.as_ptr(),
                        object_sizes.as_ptr(),
                        object_modifiers.as_ptr(),
                        layer_formats.len() as c_int,
                        layer_formats.as_ptr(),
                        layer_plane_counts.as_ptr(),
                        plane_object_indices.as_ptr(),
                        plane_offsets.as_ptr(),
                        plane_pitches.as_ptr(),
                        &mut err,
                    );
                }
            }
        }
        if err.is_err() {
            warn!("Failed to fill video frame: {}", err);
            return Err(err);
        }
        unsafe {
            encode_video_frame(
                self.handle,
                (Instant::now() - self.start_time).as_millis() as c_int,
                &mut err,
            );
        }
        if err.is_err() {
            warn!("Failed to encode video frame: {}", err);
            return Err(err);
        }
        Ok(())
    }

    pub fn check_size(
        &self,
        width_in: usize,
        height_in: usize,
        width_out: usize,
        height_out: usize,
    ) -> bool {
        (self.width_in == width_in)
            && (self.height_in == height_in)
            && (self.width_out == width_out)
            && (self.height_out == height_out)
    }

    pub fn codec_name(&self) -> String {
        let codec_name = unsafe { video_encoder_codec_name(self.handle) };
        if codec_name.is_null() {
            return "未知".to_string();
        }
        unsafe { CStr::from_ptr(codec_name) }
            .to_string_lossy()
            .into_owned()
    }

    pub fn supports_drm_prime(&self) -> bool {
        unsafe { video_encoder_supports_drm_prime(self.handle) != 0 }
    }
}

impl Drop for VideoEncoder {
    fn drop(&mut self) {
        if !self.handle.is_null() {
            unsafe { destroy_video_encoder(self.handle) }
        }
    }
}
