//go:build !x264cgo

package client

func desktopVideoCodecs() ([]string, []string) {
	return []string{desktopFormatMJPEG}, nil
}
