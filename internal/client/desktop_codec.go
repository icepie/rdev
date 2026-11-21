package client

import h264 "github.com/oops1/go.264"

func desktopVideoCodecs() ([]string, []string) {
	return []string{desktopFormatH264, desktopFormatMJPEG}, h264.Backends()
}
