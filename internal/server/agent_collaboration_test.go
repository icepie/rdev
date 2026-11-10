package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParticipantAuthorizationAndRevocation(t *testing.T) {
	srv := NewServer()
	srv.clients = map[string]*ClientConn{"a": {ID: "a"}, "b": {ID: "b", Password: "test-secret"}, "c": {ID: "c"}}
	srv.AdminToken = "configured-but-not-authenticated"
	scope := newAgentDeviceScope(srv, "a", "/a")
	if err := scope.update("b", "/b", "wrong", false, false); err == nil {
		t.Fatal("server config bypassed participant password")
	}
	if err := scope.update("b", "/b", "test-secret", false, false); err != nil {
		t.Fatal(err)
	}
	if _, cwd, err := scope.resolve("b"); err != nil || cwd != "/b" {
		t.Fatal("member cwd missing", err)
	}
	if _, _, err := scope.resolve("c"); err == nil {
		t.Fatal("unjoined device accessible")
	}
	ctx, finish, err := scope.begin(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	other, release, err := scope.begin(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := scope.update("b", "", "", false, true); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("removed member's operation not canceled")
	}
	if other.Err() != nil {
		t.Fatal("other device canceled")
	}
	if _, _, err := scope.begin(context.Background(), "b"); err == nil {
		t.Fatal("removed member can start queued work")
	}
}
func TestParticipantRestoreRequiresFreshProtectedGrant(t *testing.T) {
	srv := NewServer()
	srv.clients = map[string]*ClientConn{"a": {ID: "a"}, "b": {ID: "b", Password: "secret"}}
	scope := newAgentDeviceScope(srv, "a", "")
	if err := scope.update("b", "/work", "", true, false); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(scope.saved())
	if strings.Contains(string(data), "secret") {
		t.Fatal("persisted credential")
	}
	restored := newAgentDeviceScope(srv, "a", "")
	for _, member := range scope.saved() {
		restored.members[member.Device] = member.Cwd
	}
	if _, _, err := restored.resolve("b"); err == nil {
		t.Fatal("persisted membership granted access without authorization")
	}
	srv.clients["b"] = &ClientConn{ID: "b", Password: "changed"}
	if _, _, err := scope.resolve("b"); err == nil {
		t.Fatal("password change retained old grant")
	}
}
func TestParticipantToolsFilterAndScope(t *testing.T) {
	srv := NewServer()
	srv.clients = map[string]*ClientConn{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}}
	scope := newAgentDeviceScope(srv, "a", "")
	if err := scope.update("b", "/b", "", false, false); err != nil {
		t.Fatal(err)
	}
	list := &connectedDevicesTool{srv: srv, scope: scope}
	raw, err := list.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `"device":"c"`) {
		t.Fatal("unjoined device listed")
	}
	router := &remoteDeviceTool{srv: srv, scope: scope}
	if _, err := router.Execute(context.Background(), json.RawMessage(`{"device":"c","tool":"read_file","arguments":{"path":"secret"}}`)); err == nil {
		t.Fatal("routing bypassed membership")
	}
	tools, err := scope.tools("b")
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		wrapper, ok := tool.(*scopedAgentTool)
		if !ok || wrapper.device != "b" {
			t.Fatal("delegation not scoped")
		}
	}
}
