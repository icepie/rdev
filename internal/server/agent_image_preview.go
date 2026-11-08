package server

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"rdev/internal/agent"
)

// Store a bounded preview separately from the provider's original image. This
// keeps replay and JSONL history small and avoids decoding extreme dimensions.
func agentImagePreview(data []byte) string { return encodeAgentImagePreview(data, 192*1024, true) }

// Tool results retain JPEG for the provider image conversion contract.
func boundedAgentImagePreview(data []byte, maxBytes int) string {
	return encodeAgentImagePreview(data, maxBytes, false)
}

func encodeAgentImagePreview(data []byte, maxBytes int, preserveAlpha bool) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return ""
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	transparent := false
	if preserveAlpha {
		if opaque, ok := src.(interface{ Opaque() bool }); ok {
			transparent = !opaque.Opaque()
		}
	}
	width, height := cfg.Width, cfg.Height
	if width > 640 || height > 640 {
		if width >= height {
			height = max(1, height*640/width)
			width = 640
		} else {
			width = max(1, width*640/height)
			height = 640
		}
	}
	for {
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		if !transparent {
			draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
		}
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		var buf bytes.Buffer
		mime := "image/jpeg"
		if transparent {
			mime = "image/png"
			err = png.Encode(&buf, dst)
		} else {
			err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 75})
		}
		if err != nil {
			return ""
		}
		if buf.Len() <= maxBytes {
			return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		}
		if width == 1 && height == 1 {
			break
		}
		width = max(1, width/2)
		height = max(1, height/2)
	}
	return ""
}

func agentImagePreviews(images []agent.MessageImage) []string {
	if len(images) == 0 {
		return nil
	}
	previews := make([]string, len(images))
	for i, img := range images {
		previews[i] = img.Preview
	}
	return previews
}
