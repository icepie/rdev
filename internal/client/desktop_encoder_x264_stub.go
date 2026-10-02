//go:build !(amd64 || arm64)

package client

import "errors"

var errX264Unavailable = errors.New("libx264 backend unsupported on this architecture")

func newX264Encoder(width, height, quality, fps int) (desktopEncoder, error) {
	return nil, errX264Unavailable
}
