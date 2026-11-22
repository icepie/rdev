//! On-demand CPU screenshots; no video encoder or external process is involved.
use crate::video::PixelProvider;
use base64::Engine;
use image::{ImageEncoder, RgbImage};

pub(crate) fn encode(
    pixels: PixelProvider<'_>,
    max_width: usize,
    max_height: usize,
) -> Result<String, String> {
    let (w, h, stride, channels, bgr, data) = match pixels {
        PixelProvider::RGB(w, h, data) => (w, h, w.checked_mul(3), 3, false, data),
        PixelProvider::RGB0(w, h, data) => (w, h, w.checked_mul(4), 4, false, data),
        PixelProvider::BGR0(w, h, data) => (w, h, w.checked_mul(4), 4, true, data),
        PixelProvider::BGR0S(w, h, stride, data) => (w, h, Some(stride), 4, true, data),
    };
    let stride = stride.ok_or("Invalid screenshot stride")?;
    if w == 0
        || h == 0
        || w.checked_mul(h).is_none_or(|n| n > 32_000_000)
        || stride < w * channels
        || stride.checked_mul(h).is_none_or(|n| n > data.len())
    {
        return Err("Invalid screenshot pixel buffer".into());
    }
    // Sample directly into the bounded output: avoid allocating a full-size RGB copy.
    let scale = (max_width.clamp(1, 1280) as f64 / w as f64)
        .min(max_height.clamp(1, 720) as f64 / h as f64)
        .min(1.0);
    let out_w = ((w as f64 * scale) as u32).max(1);
    let out_h = ((h as f64 * scale) as u32).max(1);
    let out = RgbImage::from_fn(out_w, out_h, |x, y| {
        let offset = (y as usize * h / out_h as usize) * stride
            + (x as usize * w / out_w as usize) * channels;
        let p = &data[offset..offset + 3];
        image::Rgb(if bgr {
            [p[2], p[1], p[0]]
        } else {
            [p[0], p[1], p[2]]
        })
    });
    let mut encoded = Vec::new();
    image::codecs::png::PngEncoder::new(&mut encoded)
        .write_image(out.as_raw(), out_w, out_h, image::ExtendedColorType::Rgb8)
        .map_err(|e| e.to_string())?;
    Ok(base64::engine::general_purpose::STANDARD.encode(encoded))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn respects_stride_channels_and_bounds() {
        let data = [0, 0, 255, 0, 0, 255, 0, 0, 99, 99, 99, 99];
        let encoded = encode(PixelProvider::BGR0S(2, 1, 12, &data), 1280, 720).unwrap();
        let raw = base64::engine::general_purpose::STANDARD
            .decode(encoded)
            .unwrap();
        let image = image::load_from_memory(&raw).unwrap().to_rgb8();
        assert_eq!(image.get_pixel(0, 0).0, [255, 0, 0]);
        assert_eq!(image.get_pixel(1, 0).0, [0, 255, 0]);
        assert!(encode(PixelProvider::RGB(2, 1, &[0; 5]), 1280, 720).is_err());
        assert!(encode(PixelProvider::BGR0S(2, 1, 4, &data), 1280, 720).is_err());
        let encoded = encode(PixelProvider::RGB(2, 1, &[0; 6]), 1, 1).unwrap();
        let raw = base64::engine::general_purpose::STANDARD
            .decode(encoded)
            .unwrap();
        assert_eq!(image::load_from_memory(&raw).unwrap().width(), 1);
    }
}
