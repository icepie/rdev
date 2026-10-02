//go:build !x264cgo

package client

// newDesktopEncoder returns the compatibility encoder used by the default
// CGO-free client. It always produces MJPEG and has no native dependencies.
func newDesktopEncoder(_ string, _ int, _ int, quality, _ int) desktopEncoder {
	return &mjpegEncoder{quality: quality}
}
