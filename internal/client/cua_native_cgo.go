//go:build x264cgo && cuanative && cgo

package client

/*
#cgo CFLAGS: -I${SRCDIR}/../../clients/rdev-client-gpu/cua-driver-bridge/include
#cgo LDFLAGS: -L${SRCDIR}/../../internal/client/cua_native/rust-target/release -lrdev_cua_bridge -ldl -lpthread -lm
#cgo linux pkg-config: x11 xi xkbcommon
#include <stdlib.h>
#include "rdev_cua_bridge.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"sync"
	"unsafe"
)

type nativeCuaSession struct {
	mu sync.Mutex
	p  unsafe.Pointer
}

func newNativeCuaSession() (*nativeCuaSession, error) {
	p := C.rdev_cua_session_new()
	if p == nil {
		return nil, errors.New("create native Cua session failed")
	}
	return &nativeCuaSession{p: p}, nil
}

func (s *nativeCuaSession) request(raw json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return nil, errors.New("native Cua session is closed")
	}
	input := C.CString(string(raw))
	defer C.free(unsafe.Pointer(input))
	output := C.rdev_cua_session_request(s.p, input)
	if output == nil {
		return nil, errors.New("native Cua bridge returned no result")
	}
	defer C.rdev_cua_string_free(output)
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal([]byte(C.GoString(output)), &response); err != nil {
		return nil, err
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return response.Result, nil
}

func (s *nativeCuaSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p != nil {
		C.rdev_cua_session_free(s.p)
		s.p = nil
	}
}

func nativeCuaAvailable() bool { return bool(C.rdev_cua_supported()) }
