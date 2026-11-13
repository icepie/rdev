package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

type cancelCDPInputHandler struct {
	gws.BuiltinEventHandler
	cancel   context.CancelFunc
	released chan struct{}
}

func (h *cancelCDPInputHandler) OnMessage(socket *gws.Conn, m *gws.Message) {
	defer m.Close()
	var request struct {
		ID     int `json:"id"`
		Params struct {
			Type string `json:"type"`
		} `json:"params"`
	}
	if json.Unmarshal(m.Bytes(), &request) != nil {
		return
	}
	if request.Params.Type == "mousePressed" {
		h.cancel()
		return
	}
	response, _ := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{}})
	_ = socket.WriteMessage(gws.OpcodeText, response)
	if request.Params.Type == "mouseReleased" {
		select {
		case h.released <- struct{}{}:
		default:
		}
	}
}
func TestBrowserInputCancellationReleasesHeldButton(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := &cancelCDPInputHandler{cancel: cancel, released: make(chan struct{}, 1)}
	upgrader := gws.NewUpgrader(handler, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r)
		if err == nil {
			go socket.ReadLoop()
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	b := &browserSession{endpoint: endpoint}
	c, err := dialBrowserCDP(ctx, b, "owned-page")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	err = browserInputPair(ctx, c, b, "owned-page", "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed"}, map[string]any{"type": "mouseReleased"})
	if err == nil {
		t.Fatal("canceled click reported success")
	}
	select {
	case <-handler.released:
	case <-time.After(time.Second):
		t.Fatal("mouse left held after cancellation")
	}
}

type browserNavigateCDPHandler struct {
	gws.BuiltinEventHandler
	navigated chan string
}

func (h *browserNavigateCDPHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			URL string `json:"url"`
		} `json:"params"`
	}
	if json.Unmarshal(message.Bytes(), &request) != nil || request.Method != "Page.navigate" {
		return
	}
	h.navigated <- request.Params.URL
	response, _ := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{"frameId": "frame-1"}})
	_ = socket.WriteMessage(gws.OpcodeText, response)
}

func TestBrowserViewNavigateUsesValidatedExistingTarget(t *testing.T) {
	handler := &browserNavigateCDPHandler{navigated: make(chan string, 1)}
	upgrader := gws.NewUpgrader(handler, nil)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/list" {
			_, _ = io.WriteString(w, `[{"id":"page-1","type":"page"},{"id":"worker-1","type":"service_worker"}]`)
			return
		}
		if r.URL.Path != "/devtools/page/page-1" {
			http.NotFound(w, r)
			return
		}
		socket, err := upgrader.Upgrade(w, r)
		if err == nil {
			go socket.ReadLoop()
		}
	}))
	defer backend.Close()
	endpoint, _ := url.Parse(backend.URL)
	ready := make(chan struct{})
	close(ready)
	s := NewServer()
	s.BrowserEnabled = true
	s.browsers = map[string]*browserSession{"device": {device: "device", capability: "cap", endpoint: endpoint, ready: ready}}

	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/browser/view/cap/navigate", strings.NewReader(body))
		s.HandleBrowserView(response, req)
		return response
	}
	if response := request(`{"target":"../version","url":"https://example.test"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid target status = %d", response.Code)
	}
	if response := request(`{"target":"page-1","url":"javascript:alert(1)"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe URL status = %d", response.Code)
	}
	if response := request(`{"target":"missing","url":"https://example.test"}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing target status = %d", response.Code)
	}
	response := request(`{"target":"page-1","url":"https://example.test/path"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("navigate status = %d, body=%s", response.Code, response.Body.String())
	}
	select {
	case got := <-handler.navigated:
		if got != "https://example.test/path" {
			t.Fatalf("navigated to %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Chrome did not receive Page.navigate")
	}
}
