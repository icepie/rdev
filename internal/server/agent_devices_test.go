package server

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestServerAgentDiscoversLiveDevicesWithoutRegistration(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.clients = map[string]*ClientConn{"remote": {ID: "remote", OS: "linux"}}
	entry := &agentSessionEntry{deviceID: agentServerDeviceID}
	for _, tool := range srv.agentBaseTools(entry, &ClientConn{ID: agentServerDeviceID}, "") {
		if slices.Contains(agentRemoteToolNames, tool.Name()) {
			t.Errorf("host tool exposed: %s", tool.Name())
		}
	}
	list := &connectedDevicesTool{srv: srv}
	raw, err := list.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var devices []struct {
		Device string `json:"device"`
		Tools  []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Device != "remote" || len(devices[0].Tools) != len(agentRemoteToolNames) {
		t.Fatalf("discovery: %s", raw)
	}
	tool, err := srv.resolveDeviceTool("remote", "shell")
	if err != nil {
		t.Fatal(err)
	}
	if tool.(*shellTool).client != srv.clients["remote"] {
		t.Fatal("wrong device shell")
	}
	if _, err := srv.resolveDeviceTool("remote", "write_file"); err != nil {
		t.Fatal("registration still required", err)
	}
	for _, device := range []string{"", agentServerDeviceID, "offline"} {
		if _, err := srv.resolveDeviceTool(device, "shell"); err == nil {
			t.Fatalf("invalid target allowed: %q", device)
		}
	}
	if _, err := srv.resolveDeviceTool("remote", "bg"); err == nil {
		t.Fatal("unknown remote tool accepted")
	}
	srv.mu.Lock()
	delete(srv.clients, "remote")
	srv.mu.Unlock()
	if _, err := srv.resolveDeviceTool("remote", "shell"); err == nil {
		t.Fatal("offline device accepted")
	}
	raw, _ = list.Execute(context.Background(), nil)
	if raw != "[]" {
		t.Fatalf("stale discovery: %s", raw)
	}
}

func TestServerModePreservesProtectedDeviceAuthentication(t *testing.T) {
	srv := newPrefsTestServer(t)
	srv.clients = map[string]*ClientConn{"protected": {ID: "protected", Password: "test-only"}}
	if _, err := srv.resolveDeviceTool("protected", "read_file"); err == nil {
		t.Fatal("device password bypassed")
	}
	srv.AdminToken = "test-only-admin"
	if _, err := srv.resolveDeviceTool("protected", "read_file"); err != nil {
		t.Fatal(err)
	}
}
