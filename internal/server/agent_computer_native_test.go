package server

import (
	"context"
	"encoding/json"
	"testing"

	"rdev/internal/protocol"
)

func TestComputerStatusCUACapability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desktop *protocol.DesktopCapabilities
		want    bool
	}{
		{"native", &protocol.DesktopCapabilities{Supported: true, NativeCua: true}, true},
		{"plain", &protocol.DesktopCapabilities{Supported: true}, false},
		{"no-desktop", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, client, _ := newProxyTestServer(t, "device", "")
			client.Desktop = tc.desktop
			tool := &computerTool{testAutomation(t, srv, client.ID, "thread")}
			out, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"status"}`))
			if err != nil {
				t.Fatal(err)
			}
			var status struct {
				Cua bool `json:"cua"`
			}
			if err := json.Unmarshal([]byte(out), &status); err != nil {
				t.Fatal(err)
			}
			if status.Cua != tc.want {
				t.Fatalf("status = %s, want cua=%v", out, tc.want)
			}
		})
	}
}
