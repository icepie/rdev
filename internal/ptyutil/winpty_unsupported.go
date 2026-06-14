//go:build !windows || (!amd64 && !386)

package ptyutil

func startWinPTY(cfg *Config) (*Process, error) {
	return nil, errWinPTYUnsupported
}
