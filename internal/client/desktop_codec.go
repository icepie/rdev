package client

func desktopVideoCodecs() ([]string, []string) {
	return []string{desktopFormatH264, desktopFormatMJPEG}, []string{"go264"}
}
