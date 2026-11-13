package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed browser_bridge.js
var browserBridge []byte

//go:embed browser_input.js
var browserInput []byte

//go:embed browser_resize.js
var browserResize []byte

//go:embed browser_locale.js
var browserLocale []byte

const browserIdleTimeout = 30 * time.Minute
const browserLimit = 4

type browserSession struct {
	client     *ClientConn
	device     string
	capability string
	ready      chan struct{}
	err        error
	endpoint   *url.URL
	cancel     context.CancelFunc
	done       chan struct{}
	lastUsed   time.Time // guarded by Server.browserMu
	language   *browserLanguage
}

type browserTarget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

// Chrome ships a matching DevTools frontend at /devtools/inspector.html.
// Assets and page CDP sockets are exposed through a per-instance capability,
// never through an unauthenticated debugging port or a reusable admin token.
func (s *Server) HandleBrowserAPI(w http.ResponseWriter, r *http.Request) {
	if !s.BrowserEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.requireAuth(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	var in struct {
		Device   string `json:"device"`
		Password string `json:"password"`
		Lang     string `json:"lang"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || !validClientID(in.Device) {
		http.Error(w, "invalid device", 400)
		return
	}
	if in.Lang != "" && in.Lang != "zh" && in.Lang != "en" {
		http.Error(w, "language must be zh or en", 400)
		return
	}
	client, ok := s.GetClient(in.Device)
	if !ok {
		http.Error(w, "device offline", 409)
		return
	}
	if !s.deviceAuthBypassed(r) {
		if _, ok = s.authenticateProxyDevice(in.Device, in.Password); !ok {
			http.Error(w, "device authentication required", 403)
			return
		}
	}
	b, err := s.browserFor(r.Context(), client)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if in.Lang != "" {
		if err := b.language.set(r.Context(), in.Lang); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"base": "/api/browser/view/" + b.capability, "device": b.device})
}

func browserExecutable() (string, error) {
	if configured := os.Getenv("RDEV_CHROME_PATH"); configured != "" {
		return exec.LookPath(configured)
	}
	for _, name := range []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"} {
		if executable, err := exec.LookPath(name); err == nil {
			return executable, nil
		}
	}
	return "", errors.New("Chrome is not installed on the server; set RDEV_CHROME_PATH")
}

func (s *Server) browserFor(ctx context.Context, client *ClientConn) (*browserSession, error) {
	s.browserMu.Lock()
	if s.browsers == nil {
		s.browsers = make(map[string]*browserSession)
	}
	b := s.browsers[client.ID]
	if b != nil && b.client != client {
		s.browserMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.ready:
		}
		if b.cancel != nil {
			b.cancel()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.done:
		}
		return s.browserFor(ctx, client)
	}
	if b == nil {
		if len(s.browsers) >= browserLimit {
			s.browserMu.Unlock()
			return nil, errors.New("browser capacity reached; close an unused browser")
		}
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			s.browserMu.Unlock()
			return nil, err
		}
		b = &browserSession{client: client, device: client.ID, capability: hex.EncodeToString(secret[:]), ready: make(chan struct{}), done: make(chan struct{}), lastUsed: time.Now()}
		s.browsers[client.ID] = b
		go s.startBrowser(b, client)
	}
	b.lastUsed = time.Now()
	s.browserMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ready:
		return b, b.err
	}
}

func (s *Server) startBrowser(b *browserSession, client *ClientConn) {
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	defer cancel()
	defer close(b.done)
	defer func() {
		s.browserMu.Lock()
		if s.browsers[b.device] == b {
			delete(s.browsers, b.device)
		}
		s.browserMu.Unlock()
	}()
	ready := false
	fail := func(err error) { b.err = err; close(b.ready); ready = true }
	executable, err := browserExecutable()
	if err != nil {
		fail(err)
		return
	}
	profile, err := os.MkdirTemp("", "rdev-browser-")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(profile)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
		return
	}
	defer listener.Close()
	var connMu sync.Mutex
	conns := make(map[net.Conn]bool)
	acceptDone := make(chan struct{})
	defer func() {
		_ = listener.Close()
		<-acceptDone
		connMu.Lock()
		defer connMu.Unlock()
		for c := range conns {
			_ = c.Close()
		}
	}()
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connMu.Lock()
			conns[conn] = true
			connMu.Unlock()
			go func() {
				defer func() { connMu.Lock(); delete(conns, conn); connMu.Unlock() }()
				(&proxyConn{srv: s, conn: conn, reader: bufio.NewReader(conn), boundClient: client}).run()
			}()
		}
	}()
	args := []string{
		"--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--user-data-dir=" + profile, "--proxy-server=http://" + listener.Addr().String(), "--proxy-bypass-list=<-loopback>",
		"--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--disable-background-networking",
		"--disable-extensions", "--disable-sync", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", "about:blank",
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	if err = cmd.Start(); err != nil {
		fail(fmt.Errorf("start Chrome: %w", err))
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { cancel(); <-exited }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for !ready {
		select {
		case err := <-exited:
			exited <- err
			fail(errors.New("Chrome exited during startup; check sandbox configuration"))
			return
		case <-deadline.C:
			fail(errors.New("Chrome startup failed; check executable and sandbox configuration"))
			return
		case <-poll.C:
			data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			if err != nil {
				continue
			}
			port, _, ok := strings.Cut(string(data), "\n")
			if !ok {
				continue
			}
			_, err = net.LookupPort("tcp", port)
			if err != nil {
				continue
			}
			b.endpoint, _ = url.Parse("http://127.0.0.1:" + port)
			b.language, err = newBrowserLanguage(ctx, b)
			if err != nil {
				fail(fmt.Errorf("initialize browser language: %w", err))
				return
			}
			defer b.language.close()
			close(b.ready)
			ready = true
		}
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err := <-exited:
			exited <- err
			return
		case <-ctx.Done():
			return
		case <-b.language.ctx.Done():
			return
		case <-ticker.C:
			s.browserMu.Lock()
			idle := time.Since(b.lastUsed) > browserIdleTimeout
			s.browserMu.Unlock()
			current, online := s.GetClient(b.device)
			if idle || !online || current != client {
				return
			}
		}
	}
}

// CloseBrowsers releases all Chrome child processes before server shutdown.
func (s *Server) CloseBrowsers() {
	s.browserMu.Lock()
	sessions := make([]*browserSession, 0, len(s.browsers))
	for _, b := range s.browsers {
		sessions = append(sessions, b)
	}
	s.browserMu.Unlock()
	for _, b := range sessions {
		<-b.ready
		if b.cancel != nil {
			b.cancel()
		}
		<-b.done
	}
}

func (s *Server) HandleBrowserView(w http.ResponseWriter, r *http.Request) {
	if !s.BrowserEnabled {
		http.NotFound(w, r)
		return
	}
	secret, path, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/browser/view/"), "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.browserMu.Lock()
	var b *browserSession
	for _, session := range s.browsers {
		if session.capability == secret {
			b = session
			break
		}
	}
	if b != nil {
		b.lastUsed = time.Now()
	}
	s.browserMu.Unlock()
	if b == nil {
		http.NotFound(w, r)
		return
	}
	select {
	case <-b.ready:
	case <-r.Context().Done():
		return
	}
	if b.err != nil {
		http.Error(w, "browser unavailable", 503)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if path == "language" {
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", "PUT")
			http.Error(w, "method not allowed", 405)
			return
		}
		var in struct {
			Lang string `json:"lang"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || (in.Lang != "zh" && in.Lang != "en") {
			http.Error(w, "language must be zh or en", 400)
			return
		}
		if err := b.language.set(r.Context(), in.Lang); err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if path == "session" && r.Method == http.MethodDelete {
		b.cancel()
		<-b.done
		w.WriteHeader(204)
		return
	}
	if path == "targets" && r.Method == http.MethodPost {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPut, b.endpoint.String()+"/json/new?about:blank", nil)
		response, err := (&http.Client{Timeout: 5 * time.Second, Transport: browserTransport}).Do(req)
		if err != nil {
			http.Error(w, "Chrome unavailable", 502)
			return
		}
		defer response.Body.Close()
		var target browserTarget
		if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&target) != nil {
			http.Error(w, "could not open tab", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(target)
		return
	}
	if path == "targets" && r.Method == http.MethodGet {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, b.endpoint.String()+"/json/list", nil)
		response, err := (&http.Client{Timeout: 5 * time.Second, Transport: browserTransport}).Do(req)
		if err != nil {
			http.Error(w, "Chrome unavailable", 502)
			return
		}
		defer response.Body.Close()
		var targets []browserTarget
		if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&targets) != nil {
			http.Error(w, "invalid Chrome response", 502)
			return
		}
		pages := make([]browserTarget, 0, len(targets))
		for _, target := range targets {
			if target.Type == "page" {
				pages = append(pages, target)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages)
		return
	}
	if path == "navigate" && r.Method == http.MethodPost {
		var in struct {
			Target string `json:"target"`
			URL    string `json:"url"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || !validBrowserTargetID(in.Target) {
			http.Error(w, "invalid browser target", http.StatusBadRequest)
			return
		}
		if err := browserNavigationURL(in.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var targets []browserTarget
		if err := browserJSON(r.Context(), b, http.MethodGet, "/json/list", &targets); err != nil {
			http.Error(w, "Chrome unavailable", http.StatusBadGateway)
			return
		}
		found := false
		for _, target := range targets {
			if target.ID == in.Target && target.Type == "page" {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "browser tab not found", http.StatusNotFound)
			return
		}
		connection, err := dialBrowserCDP(r.Context(), b, in.Target)
		if err != nil {
			http.Error(w, "Chrome unavailable", http.StatusBadGateway)
			return
		}
		defer connection.close()
		var result struct {
			ErrorText string `json:"errorText"`
		}
		if err := connection.call(r.Context(), "Page.navigate", map[string]any{"url": in.URL}, &result); err != nil {
			http.Error(w, "Chrome navigation failed", http.StatusBadGateway)
			return
		}
		if result.ErrorText != "" {
			http.Error(w, "navigation failed: "+result.ErrorText, http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodGet || !strings.HasPrefix(path, "devtools/") || strings.Contains(path, "..") || strings.HasPrefix(path, "devtools/browser/") {
		http.NotFound(w, r)
		return
	}
	if path == "devtools/rdev-resize.js" || path == "devtools/rdev-bridge.js" || path == "devtools/rdev-input.js" || path == "devtools/rdev-locale.js" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		if path == "devtools/rdev-resize.js" {
			_, _ = w.Write(browserResize)
		} else if path == "devtools/rdev-locale.js" {
			_, _ = w.Write(browserLocale)
		} else if path == "devtools/rdev-input.js" {
			_, _ = w.Write(browserInput)
		} else {
			_, _ = w.Write(browserBridge)
		}
		return
	}
	inspector := path == "devtools/inspector.html"
	preview := inspector && r.URL.Query().Get("rdevPreview") == "1"
	locale := inspector && (r.URL.Query().Get("rdevLang") == "zh" || r.URL.Query().Get("rdevLang") == "en-US")
	proxy := httputil.NewSingleHostReverseProxy(b.endpoint)
	proxy.Transport = browserTransport
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.URL.Path = "/" + path
		req.URL.RawPath = ""
		req.URL.RawQuery = ""
		req.Host = b.endpoint.Host
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Del("Origin")
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		req.Header.Del("X-RDev-Token")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if (preview || locale) && resp.StatusCode == http.StatusOK {
			data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if err != nil {
				return err
			}
			var bridge []byte
			if locale {
				bridge = append(bridge, []byte(`<script src="./rdev-locale.js"></script>`)...)
			}
			if preview {
				bridge = append(bridge, []byte(`<style id="rdev-preview-loading">html{visibility:hidden}</style><script type="module" src="./rdev-bridge.js"></script>`)...)
			}
			data = bytes.Replace(data, []byte("<head>"), append([]byte("<head>"), bridge...), 1)
			// Chrome's inspector may omit an explicit head element.
			if !bytes.Contains(data, bridge) {
				data = bytes.Replace(data, []byte("<meta charset="), append(bridge, []byte("<meta charset=")...), 1)
			}
			resp.Body = io.NopCloser(bytes.NewReader(data))
			resp.ContentLength = int64(len(data))
			resp.Header.Del("Content-Length")
		}
		resp.Header.Set("Cache-Control", "no-store")
		resp.Header.Set("Referrer-Policy", "no-referrer")
		return nil
	}
	proxy.ServeHTTP(w, r)
}

// Never let the host's HTTP_PROXY redirect Chrome's local control traffic.
var browserTransport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 10 * time.Second}
