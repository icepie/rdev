package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
