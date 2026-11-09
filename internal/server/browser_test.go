package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestBrowserProxyRejectsReplacedDeviceConnection(t *testing.T) {
	s, old, transport := newProxyTestServer(t, "device", "")
	s.clients["device"] = &ClientConn{ID: "device"}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		(&proxyConn{srv: s, conn: local, reader: bufio.NewReader(local), boundClient: old}).run()
	}()
	_, _ = io.WriteString(remote, "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(remote), nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 407 {
		t.Fatalf("replaced connection: %d", response.StatusCode)
	}
	<-finished
	if transport.lastMsg() != nil {
		t.Fatal("stale browser dialed through old device")
	}
}

func TestBrowserAPIRequiresAdminAndDeviceAuth(t *testing.T) {
	s, _, _ := newProxyTestServer(t, "device", "password")
	s.AdminToken = "admin"
	request := func(body, token string) int {
		req := httptest.NewRequest("POST", "/api/browser", strings.NewReader(body))
		req.Header.Set("X-RDev-Token", token)
		response := httptest.NewRecorder()
		s.HandleBrowserAPI(response, req)
		return response.Code
	}
	if got := request(`{"device":"device"}`, ""); got != 401 {
		t.Fatalf("unauthorized = %d", got)
	}
	if got := request(`{"device":"absent"}`, "admin"); got != 409 {
		t.Fatalf("offline = %d", got)
	}
	s.AdminToken = ""
	if got := request(`{"device":"device","password":"wrong"}`, ""); got != 403 {
		t.Fatalf("bad password = %d", got)
	}
	t.Setenv("RDEV_CHROME_PATH", "/does-not-exist/rdev-chrome")
	s.authLimiter.success("device")
	if got := request(`{"device":"device","password":"password"}`, ""); got != 503 {
		t.Fatalf("missing Chrome = %d", got)
	}
	s.CloseBrowsers()
}

func TestBrowserViewScopesAssetsAndFiltersTargets(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-RDev-Token") != "" {
			t.Error("credentials forwarded to Chrome")
		}
		if r.URL.Path == "/json/list" {
			io.WriteString(w, `[{"id":"page","type":"page","title":"hello"},{"id":"worker","type":"service_worker"}]`)
			return
		}
		io.WriteString(w, r.URL.Path)
	}))
	defer backend.Close()
	endpoint, _ := url.Parse(backend.URL)
	ready := make(chan struct{})
	close(ready)
	s := NewServer()
	s.browsers = map[string]*browserSession{"device": {device: "device", capability: "cap", endpoint: endpoint, ready: ready}}
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"bad/targets", 404, ""}, {"cap/devtools/browser/root", 404, ""}, {"cap/json/version", 404, ""},
		{"cap/devtools/../json/version", 404, ""}, {"cap/devtools/entrypoints/inspector/inspector.js", 200, "/devtools/entrypoints/inspector/inspector.js"},
	} {
		req := httptest.NewRequest("GET", "/api/browser/view/"+tc.path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Cookie", "token=secret")
		req.Header.Set("X-RDev-Token", "secret")
		response := httptest.NewRecorder()
		s.HandleBrowserView(response, req)
		if response.Code != tc.status || (tc.body != "" && response.Body.String() != tc.body) {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	s.HandleBrowserView(response, httptest.NewRequest("GET", "/api/browser/view/cap/targets", nil))
	var targets []browserTarget
	if err := json.Unmarshal(response.Body.Bytes(), &targets); err != nil || len(targets) != 1 || targets[0].ID != "page" {
		t.Fatalf("targets: %s", response.Body)
	}
}

func TestBrowserConcurrentAttachSharesStartupFailure(t *testing.T) {
	t.Setenv("RDEV_CHROME_PATH", "/does-not-exist/rdev-chrome")
	s, client, _ := newProxyTestServer(t, "device", "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.browserFor(context.Background(), client); err == nil {
				t.Error("expected startup failure")
			}
		}()
	}
	wg.Wait()
	s.CloseBrowsers()
}
