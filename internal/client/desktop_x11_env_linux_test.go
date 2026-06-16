//go:build linux

package client

import "testing"

func TestParseProcEnv(t *testing.T) {
	env := parseProcEnv([]byte("DISPLAY=:0\x00XAUTHORITY=/tmp/xauth\x00EMPTY=\x00"))
	if env["DISPLAY"] != ":0" {
		t.Fatalf("DISPLAY = %q", env["DISPLAY"])
	}
	if env["XAUTHORITY"] != "/tmp/xauth" {
		t.Fatalf("XAUTHORITY = %q", env["XAUTHORITY"])
	}
	if value, ok := env["EMPTY"]; !ok || value != "" {
		t.Fatalf("EMPTY = %q, %v", value, ok)
	}
}

func TestLinuxDBusAddress(t *testing.T) {
	if got := linuxDBusAddress(""); got != "" {
		t.Fatalf("empty runtime dir returned %q", got)
	}
}
