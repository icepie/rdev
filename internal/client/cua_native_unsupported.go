package client

import (
	"encoding/json"
	"errors"
)

type nativeCuaSession struct{}

func newNativeCuaSession() (*nativeCuaSession, error) {
	return nil, errors.New("native Cua is unavailable in the Go client; use the Rust GPU client")
}

func (*nativeCuaSession) request(json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("native Cua is unavailable in the Go client; use the Rust GPU client")
}

func (*nativeCuaSession) close() {}

func nativeCuaAvailable() bool { return false }
