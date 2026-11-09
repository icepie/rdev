package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"rdev/internal/protocol"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

type autojsFixture struct {
	gws.BuiltinEventHandler
	capabilities string
	result       string
	connections  atomic.Int32
	requests     chan json.RawMessage
	opened       chan struct{}
	closed       chan struct{}
}

func (h *autojsFixture) OnOpen(c *gws.Conn) {
	h.connections.Add(1)
	h.opened <- struct{}{}
	if h.capabilities != "" {
		_ = c.WriteMessage(gws.OpcodeText, []byte(h.capabilities))
	}
}
func (h *autojsFixture) OnClose(_ *gws.Conn, _ error) { h.closed <- struct{}{} }
func (h *autojsFixture) OnMessage(c *gws.Conn, m *gws.Message) {
	defer m.Close()
	raw := append(json.RawMessage(nil), m.Bytes()...)
	h.requests <- raw
	var request struct {
		AutoJs struct{ Action string }
	}
	_ = json.Unmarshal(raw, &request)
	result := h.result
	if request.AutoJs.Action == "status" {
		result = `{"engine":"Auto.js","version":"6.8.0","accessibility":true}`
	}
	if result != "" {
		_ = c.WriteMessage(gws.OpcodeText, []byte(`{"AutoJsResult":`+result+`}`))
	}
}

func newAutojsFixture(t *testing.T, capabilities, result string) (*autojsTool, *autojsFixture) {
	t.Helper()
	h := &autojsFixture{capabilities: capabilities, result: result, requests: make(chan json.RawMessage, 16), closed: make(chan struct{}, 16), opened: make(chan struct{}, 16)}
	upgrade := gws.NewUpgrader(h, nil)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrade.Upgrade(w, r)
		if err == nil {
			go c.ReadLoop()
		}
	}))
	t.Cleanup(web.Close)
	srv, client, transport := newProxyTestServer(t, "android-device", "")
	client.Desktop = &protocol.DesktopCapabilities{Platform: "android"}
	srv.gpuDesktopTunnels[client.ID] = gpuFixtureTunnel(t, client.ID, strings.TrimPrefix(web.URL, "http://"))
	t.Cleanup(func() {
		if transport.lastMsg() != nil {
			t.Error("Auto.js sent legacy desktop/video protocol")
		}
	})
	return &autojsTool{testAutomation(t, srv, client.ID, "autojs")}, h
}

func awaitAutojsClose(t *testing.T, h *autojsFixture) {
	t.Helper()
	select {
	case <-h.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Auto.js invocation leaked its desktop stream")
	}
}

func autojsRequest(t *testing.T, h *autojsFixture) map[string]json.RawMessage {
	t.Helper()
	select {
	case raw := <-h.requests:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope) != 1 || envelope["AutoJs"] == nil {
			t.Fatalf("unexpected desktop request (must not configure/start video): %s", raw)
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(envelope["AutoJs"], &request); err != nil {
			t.Fatal(err)
		}
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("Auto.js request missing")
		return nil
	}
}

func TestAutojsRequiresCapabilityBeforeSending(t *testing.T) {
	tool, h := newAutojsFixture(t, `{"AgentCapabilities":{"version":1}}`, "")
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"run","script":"throw 'must not execute'"}`))
	if err == nil || !strings.Contains(err.Error(), "update the Android client") {
		t.Fatalf("old client not rejected: %v", err)
	}
	awaitAutojsClose(t, h)
	if len(h.requests) != 0 {
		t.Fatal("sent unsupported request to old client")
	}
}

func TestAutojsFailuresPreserveLogsInConsumerError(t *testing.T) {
	for _, status := range []string{"error", "timeout", "canceled", "unexpected"} {
		t.Run(status, func(t *testing.T) {
			result := `{"status":"` + status + `","value":"","logs":["before exception"],"error":"device exception with stack","truncated":true}`
			tool, h := newAutojsFixture(t, `{"AgentCapabilities":{"version":1,"autojs":true}}`, result)
			out, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"run","script":"throw Error('device exception')"}`))
			if err == nil || err.Error() != result || out != result {
				t.Fatalf("consumer discards output on error; full JSON must survive in error: out=%s err=%v", out, err)
			}
			awaitAutojsClose(t, h)
		})
	}
}

func TestAutojsTimeoutAndScriptByteBoundaries(t *testing.T) {
	tool, h := newAutojsFixture(t, `{"AgentCapabilities":{"version":1,"autojs":true}}`, `{"status":"finished","value":"","logs":[]}`)
	for _, timeout := range []int{1, 25000, 30000} {
		script := strings.Repeat("\n", autojsMaxScriptBytes-1) + "1"
		raw, _ := json.Marshal(map[string]any{"action": "run", "script": script, "timeoutMs": timeout})
		if _, err := tool.Execute(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		request := autojsRequest(t, h)
		var gotScript string
		var gotTimeout int
		_ = json.Unmarshal(request["script"], &gotScript)
		_ = json.Unmarshal(request["timeoutMs"], &gotTimeout)
		if gotScript != script || gotTimeout != min(timeout, 25000) {
			t.Fatal("max-size escaped script or bounded timeout changed")
		}
		awaitAutojsClose(t, h)
	}
	before := h.connections.Load()
	for _, raw := range []string{
		`{"action":"bad"}`,
		`{"action":"run"}`,
		`{"action":"run","script":"  "}`,
		`{"action":"status","script":"1"}`,
		`{"action":"run","script":"1","timeoutMs":0}`,
		`{"action":"run","script":"1","timeoutMs":-1}`,
		`{"action":"run","script":"1","timeoutMs":1.5}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid request: %s", raw)
		}
	}
	raw, _ := json.Marshal(map[string]any{"action": "run", "script": strings.Repeat("界", 21846)})
	if _, err := tool.Execute(context.Background(), raw); err == nil {
		t.Fatal("script limit counted characters instead of UTF-8 bytes")
	}
	if h.connections.Load() != before {
		t.Fatal("invalid request opened a desktop stream")
	}
}

func TestAutojsAuthorizationAndSharedDesktopOwnership(t *testing.T) {
	srv := NewServer()
	srv.clients = map[string]*ClientConn{
		"android":   {ID: "android", OS: "android"},
		"other":     {ID: "other", OS: "android"},
		"protected": {ID: "protected", OS: "android", Password: "secret"},
		"linux":     {ID: "linux", OS: "linux"},
	}
	for _, tc := range []struct{ bound, device, want string }{
		{"android", "other", "bound to device"},
		{"@server", "protected", "admin token"},
		{"@server", "", "explicit connected device"},
		{"@server", "linux", "Android device"},
		{"@server", "offline", "offline"},
	} {
		tool := autojsTool{testAutomation(t, srv, tc.bound, "auth")}
		raw, _ := json.Marshal(map[string]string{"action": "status", "device": tc.device})
		if _, err := tool.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("routing %s -> %s: %v", tc.bound, tc.device, err)
		}
	}
	computer := computerTool{testAutomation(t, srv, "android", "computer")}
	if err := computer.claim("android"); err != nil {
		t.Fatal(err)
	}
	tool := autojsTool{testAutomation(t, srv, "@server", "script")}
	for _, action := range []string{"status", "run"} {
		args := map[string]string{"action": action, "device": "android"}
		if action == "run" {
			args["script"] = "1"
		}
		raw, _ := json.Marshal(args)
		if _, err := tool.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "another conversation") {
			t.Fatalf("%s bypassed computer ownership: %v", action, err)
		}
	}
}

func TestAutojsManualTakeoverCancelsAndClosesStream(t *testing.T) {
	tool, h := newAutojsFixture(t, `{"AgentCapabilities":{"version":1,"autojs":true}}`, "")
	result := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"run","script":"while(true) sleep(100)"}`))
		result <- err
	}()
	autojsRequest(t, h)
	other := autojsTool{testAutomation(t, tool.srv, "android-device", "other")}
	if _, err := other.Execute(context.Background(), json.RawMessage(`{"action":"status"}`)); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("competing script claimed desktop: %v", err)
	}
	takeover := make(chan struct{})
	go func() { tool.srv.interruptDesktopAutomation("android-device"); close(takeover) }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled script succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual takeover did not cancel script promptly")
	}
	awaitAutojsClose(t, h)
	select {
	case <-takeover:
	case <-time.After(2 * time.Second):
		t.Fatal("manual takeover did not drain execution")
	}
	if err := other.claim("android-device"); err != nil {
		t.Fatal("manual takeover did not release desktop", err)
	}
}

func TestAutojsCancellationWhileWaitingForCapabilities(t *testing.T) {
	tool, h := newAutojsFixture(t, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, json.RawMessage(`{"action":"status"}`))
		result <- err
	}()
	select {
	case <-h.opened:
	case <-time.After(2 * time.Second):
		t.Fatal("capability stream did not open")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil || (!errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "disconnected")) {
			t.Fatalf("capability cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capability negotiation ignored cancellation")
	}
	awaitAutojsClose(t, h)
	if len(h.requests) != 0 {
		t.Fatal("request sent without capability negotiation")
	}
}

func TestAutojsServerConversationUsesExplicitAndroidDevice(t *testing.T) {
	tool, h := newAutojsFixture(t, `{"AgentCapabilities":{"version":1,"autojs":true}}`, `{"status":"finished","value":"device result","logs":[]}`)
	tool.automationTool = testAutomation(t, tool.srv, "@server", "server-script")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"run","device":"android-device","script":"'device result'"}`)); err != nil {
		t.Fatal(err)
	}
	awaitAutojsClose(t, h)
	computer := computerTool{testAutomation(t, tool.srv, "android-device", "computer")}
	if err := computer.claim("android-device"); err == nil {
		t.Fatal("Auto.js did not retain the shared desktop ownership lease")
	}
}
