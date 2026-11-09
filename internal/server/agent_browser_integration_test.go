package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/protocol"
)

// Opt-in: exercises real server Chrome, the device TCP tunnel, CDP operations,
// vision output, and two independent conversation targets. No external sites.
func TestBrowserAgentChromeIntegration(t *testing.T) {
	if os.Getenv("RDEV_TEST_CHROME") == "" {
		t.Skip("set RDEV_TEST_CHROME=1 to run real Chrome integration")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Device route fixture</title><input id="name"><button id="go" onclick="document.querySelector('#out').textContent=document.querySelector('#name').value">Apply</button><p id="out">Waiting</p>`)
	}))
	defer site.Close()
	s, client, transport := newProxyTestServer(t, "exit-device", "")
	s.BrowserEnabled = true
	tunnel := httptest.NewServer(http.HandlerFunc(s.HandleTCPTunnel))
	defer tunnel.Close()
	var routed atomic.Int32
	transport.onSend = func(m *protocol.Message) {
		if m.Type != protocol.MsgTCPConnect {
			return
		}
		go func() {
			if m.Host != "agent-route.invalid" {
				s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPFail, ForwardID: m.ForwardID, Error: "test destination refused"})
				return
			}
			routed.Add(1)
			target, err := net.DialTimeout("tcp", strings.TrimPrefix(site.URL, "http://"), time.Second)
			if err != nil {
				s.handleClientMessage(client, &protocol.Message{Type: protocol.MsgTCPFail, ForwardID: m.ForwardID, Error: err.Error()})
				return
			}
			socket, _, err := gws.NewClient(&testTunnelWSHandler{target: target}, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(tunnel.URL, "http") + m.Tunnel})
			if err != nil {
				target.Close()
				return
			}
			go socket.ReadLoop()
			go func() {
				defer target.Close()
				defer socket.NetConn().Close()
				buf := make([]byte, 64*1024)
				for {
					n, err := target.Read(buf)
					if n > 0 {
						if e := socket.WriteMessage(gws.OpcodeBinary, buf[:n]); e != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}()
	}
	defer s.CloseBrowsers()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	first := &browserTool{testAutomation(t, s, "@server", "a")}
	second := &browserTool{testAutomation(t, s, "@server", "b")}
	run := func(tool *browserTool, args map[string]any) string {
		t.Helper()
		args["device"] = client.ID
		raw, _ := json.Marshal(args)
		result, err := tool.Execute(ctx, raw)
		if err != nil {
			t.Fatalf("%s: %v", args["action"], err)
		}
		return result
	}
	run(first, map[string]any{"action": "open", "url": "http://agent-route.invalid/"})
	if routed.Load() == 0 {
		t.Fatal("browser bypassed selected client")
	}
	snapshot := run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(snapshot, "#name") || !strings.Contains(snapshot, "Device route fixture") {
		t.Fatalf("bad snapshot: %s", snapshot)
	}
	run(first, map[string]any{"action": "fill", "selector": "#name", "text": "你好🌏 'quoted'"})
	run(first, map[string]any{"action": "click", "selector": "#go"})
	snapshot = run(first, map[string]any{"action": "snapshot"})
	if !strings.Contains(snapshot, "你好🌏 'quoted'") {
		t.Fatalf("fill/click failed: %s", snapshot)
	}
	shot := run(first, map[string]any{"action": "screenshot"})
	if !strings.HasPrefix(shot, "[image preview]\ndata:image/jpeg;base64,") {
		t.Fatal("missing vision envelope")
	}
	run(second, map[string]any{"action": "open", "url": "http://agent-route.invalid/"})
	if first.state.pages[client.ID].target == second.state.pages[client.ID].target {
		t.Fatal("conversations share a tab")
	}
	if strings.Contains(run(second, map[string]any{"action": "snapshot"}), "你好") {
		t.Fatal("second tab inherited first form")
	}
	if !strings.Contains(run(first, map[string]any{"action": "snapshot"}), "你好") {
		t.Fatal("first tab lost its state")
	}
	c, err := dialBrowserCDP(ctx, first.state.pages[client.ID].browser, first.state.pages[client.ID].target)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.call(ctx, "Rdev.nonexistent", map[string]any{}, nil); err == nil {
		t.Fatal("CDP errors ignored")
	}
	c.close()
	s.retireAutomation(first.state)
	s.agentWG.Wait()
	if !strings.Contains(run(second, map[string]any{"action": "snapshot"}), "Device route fixture") {
		t.Fatal("retiring first conversation closed second target")
	}
}
