//go:build !cuanative || !x264cgo || !cgo

package client

import (
	"encoding/json"
	"errors"
)

type nativeCuaSession struct{}

func newNativeCuaSession() (*nativeCuaSession, error) {
	return nil, errors.New("native Cua requires the x264cgo client build")
}

func (*nativeCuaSession) request(json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("native Cua requires the x264cgo client build")
}

func (*nativeCuaSession) close() {}

func nativeCuaAvailable() bool { return false }
