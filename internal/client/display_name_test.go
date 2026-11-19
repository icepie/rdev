package client

import (
	"os"
	"runtime"
	"testing"

	"rdev/internal/protocol"
)

func TestRegistrationDisplayNamePreservesIdentity(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	t.Setenv("RDEV_DISPLAY_NAME", "Office laptop")
	c := NewClient("wss://example.test", "stable-id", "password", "")
	if c.identityErr != nil {
		t.Fatal(c.identityErr)
	}
	identity, proof := c.deviceIdentity, c.reconnectProof
	for _, name := range []string{"Office laptop", "Renamed laptop"} {
		c.SetDisplayName(name)
		wire, err := protocol.Encode(c.registrationMessage())
		if err != nil {
			t.Fatal(err)
		}
		msg, err := protocol.Decode(wire)
		if err != nil {
			t.Fatal(err)
		}
		if msg.DisplayName != name || msg.ClientID != "stable-id" || msg.ClientOS != runtime.GOOS || msg.Password != "password" {
			t.Fatalf("registration metadata: %#v", msg)
		}
		if msg.DeviceIdentity != identity || msg.ReconnectProof != proof || c.clientID != "stable-id" {
			t.Fatal("display name changed identity or routing")
		}
	}
	t.Setenv("RDEV_DISPLAY_NAME", "Another label")
	restarted := NewClient("wss://example.test", "stable-id", "password", "")
	if restarted.identityErr != nil || restarted.deviceIdentity != identity || restarted.reconnectProof != proof {
		t.Fatal("display name changed persisted credential scope")
	}
	if restarted.displayName != "Another label" {
		t.Fatal("environment display name ignored")
	}
}

func TestRegistrationDisplayNameDefaultsToHostname(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	t.Setenv("RDEV_DISPLAY_NAME", "")
	hostname, err := os.Hostname()
	if err != nil {
		t.Skipf("hostname unavailable: %v", err)
	}
	c := NewClient("wss://example.test", "stable-id", "", "")
	if c.displayName != hostname || c.requestedID != "stable-id" {
		t.Fatalf("name=%q id=%q", c.displayName, c.requestedID)
	}
}
