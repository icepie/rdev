package server

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startGPUDesktopProxy runs the HTTP proxy for one request against a fake
// device tunnel and returns the recorder plus a channel closed when it ends.
// The handler aborts a truncated response with http.ErrAbortHandler, so the
// run reports the panic value instead of letting it escape the goroutine.
func startGPUDesktopProxy(t *testing.T, req *http.Request) (*gpuDesktopTunnel, *httptest.ResponseRecorder, <-chan any) {
	t.Helper()
	tunnel := newGPUDesktopTunnel("dev", nil)
	t.Cleanup(tunnel.close)
	rec := httptest.NewRecorder()
	aborted := make(chan any, 1)
	go func() {
		defer close(aborted)
		defer func() {
			if value := recover(); value != nil {
				aborted <- value
			}
		}()
		proxyGPUDesktopHTTP(rec, req, tunnel, "dev")
	}()
	return tunnel, rec, aborted
}

// awaitGPUDesktopStream waits for the tunnel to allocate the substream, which
// openStream registers before it sends the open frame.
func awaitGPUDesktopStream(t *testing.T, tunnel *gpuDesktopTunnel) *gpuDesktopStream {
	t.Helper()
	select {
	case frame := <-tunnel.send:
		if frame[0] != gpuDesktopTunnelFrameOpen {
			t.Fatalf("first tunnel frame type = %d, want open", frame[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel did not receive the open frame")
	}
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	for _, stream := range tunnel.streams {
		return stream
	}
	t.Fatal("substream was not registered")
	return nil
}

// feedGPUDesktopResponse pushes raw response bytes without overrunning the
// substream budget, then ends the stream.
func feedGPUDesktopResponse(t *testing.T, tunnel *gpuDesktopTunnel, stream *gpuDesktopStream, raw []byte) {
	t.Helper()
	const chunk = 64 << 10
	for len(raw) > 0 {
		n := min(chunk, len(raw))
		deadline := time.Now().Add(5 * time.Second)
		for {
			stream.dataMu.Lock()
			buffered := stream.buffered
			stream.dataMu.Unlock()
			if buffered < gpuDesktopStreamQueueBytes/2 || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-stream.closed:
			return
		default:
		}
		tunnel.dispatchData(stream.id, raw[:n])
		raw = raw[n:]
	}
	tunnel.closeStream(stream.id)
}

func waitGPUDesktopProxy(t *testing.T, aborted <-chan any) any {
	t.Helper()
	select {
	case value, ok := <-aborted:
		if !ok {
			return nil
		}
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not finish")
	}
	return nil
}

// A response larger than the rewrite cap must be streamed through, not
// buffered: bytes and order are preserved and the upstream length is not
// replayed as a promise the proxy cannot keep.
func TestGPUDesktopHTTPStreamsLargeResponse(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gpu-desktop/dev/blob.bin", nil)
	tunnel, rec, aborted := startGPUDesktopProxy(t, req)
	stream := awaitGPUDesktopStream(t, tunnel)

	body := bytes.Repeat([]byte("0123456789abcdef"), (12<<20)/16)
	header := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: 12582912\r\nX-Device: yes\r\n\r\n")
	go feedGPUDesktopResponse(t, tunnel, stream, append(header, body...))

	if value := waitGPUDesktopProxy(t, aborted); value != nil {
		t.Fatalf("proxy aborted: %v", value)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Device") != "yes" {
		t.Fatalf("upstream header dropped: %v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != "" && rec.Header().Get("Content-Length") != "12582912" {
		t.Fatalf("stale content-length = %q", rec.Header().Get("Content-Length"))
	}
	if got, want := sha256.Sum256(rec.Body.Bytes()), sha256.Sum256(body); got != want {
		t.Fatalf("streamed body differs: got %d bytes", rec.Body.Len())
	}
	if stream.buffered != 0 {
		t.Fatalf("consumed chunks were not released: buffered=%d", stream.buffered)
	}
}

func TestGPUDesktopHTTPRewritesFrontendAssets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gpu-desktop/dev/index.html?x=1", nil)
	tunnel, rec, aborted := startGPUDesktopProxy(t, req)
	stream := awaitGPUDesktopStream(t, tunnel)

	page := `<html><link rel="stylesheet" href="style.css"><script src="lib.js"></script></html>`
	go feedGPUDesktopResponse(t, tunnel, stream,
		[]byte("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n"+page))

	if value := waitGPUDesktopProxy(t, aborted); value != nil {
		t.Fatalf("proxy aborted: %v", value)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, want := range []string{`href="/gpu-desktop/dev/style.css?x=1"`, `src="/gpu-desktop/dev/lib.js?x=1"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten body missing %q: %s", want, got)
		}
	}
	if got, want := rec.Header().Get("Content-Length"), len(rec.Body.String()); got != strconv.Itoa(want) {
		t.Fatalf("content-length = %q, want %d", got, want)
	}
}

// A rewrite target that exceeds the cap must fail explicitly instead of
// returning a half-rewritten page.
func TestGPUDesktopHTTPRejectsOversizedRewrite(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gpu-desktop/dev/index.html", nil)
	tunnel, rec, aborted := startGPUDesktopProxy(t, req)
	stream := awaitGPUDesktopStream(t, tunnel)

	body := bytes.Repeat([]byte("a"), gpuDesktopRewriteBodyLimit+1)
	go feedGPUDesktopResponse(t, tunnel, stream,
		[]byte("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\n"+string(body)))

	if value := waitGPUDesktopProxy(t, aborted); value != nil {
		t.Fatalf("proxy aborted: %v", value)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too large to rewrite") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// A response that ends before its Content-Length must abort the connection, so
// the browser cannot mistake a short body for a complete one.
func TestGPUDesktopHTTPAbortsTruncatedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gpu-desktop/dev/blob.bin", nil)
	tunnel, rec, aborted := startGPUDesktopProxy(t, req)
	stream := awaitGPUDesktopStream(t, tunnel)

	go feedGPUDesktopResponse(t, tunnel, stream,
		[]byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: 4096\r\n\r\npartial"))

	if value := waitGPUDesktopProxy(t, aborted); !errors.Is(value.(error), http.ErrAbortHandler) {
		t.Fatalf("proxy panic = %v, want http.ErrAbortHandler", value)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 before the abort", rec.Code)
	}
	if rec.Body.String() != "partial" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGPUDesktopHTTPReportsTruncatedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gpu-desktop/dev/index.html", nil)
	tunnel, rec, aborted := startGPUDesktopProxy(t, req)
	stream := awaitGPUDesktopStream(t, tunnel)

	go feedGPUDesktopResponse(t, tunnel, stream, []byte("HTTP/1.1 200 OK\r\nContent-Ty"))

	if value := waitGPUDesktopProxy(t, aborted); value != nil {
		t.Fatalf("proxy aborted: %v", value)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "response headers") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestGPUDesktopHTTPRejectsOversizedRequestBody(t *testing.T) {
	body := bytes.Repeat([]byte("x"), gpuDesktopRequestBodyLimit+1)
	req := httptest.NewRequest(http.MethodPost, "/gpu-desktop/dev/upload", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	tunnel := newGPUDesktopTunnel("dev", nil)
	t.Cleanup(tunnel.close)

	proxyGPUDesktopHTTP(rec, req, tunnel, "dev")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestGPUDesktopStreamReaderBoundsIdleAndReleasesChunks(t *testing.T) {
	tunnel := newGPUDesktopTunnel("dev", nil)
	t.Cleanup(tunnel.close)
	stream := &gpuDesktopStream{id: 1, data: make(chan []byte, 1), closed: make(chan struct{})}
	tunnel.mu.Lock()
	tunnel.streams[stream.id] = stream
	tunnel.mu.Unlock()

	idle := &gpuDesktopStreamReader{stream: stream, tunnel: tunnel, idle: 20 * time.Millisecond}
	if _, err := idle.Read(make([]byte, 8)); !errors.Is(err, errGPUDesktopReadTimeout) {
		t.Fatalf("idle read error = %v, want timeout", err)
	}

	reader := &gpuDesktopStreamReader{stream: stream, tunnel: tunnel, idle: time.Second}
	tunnel.dispatchData(stream.id, []byte("chunk"))
	buf := make([]byte, 8)
	if n, err := reader.Read(buf); err != nil || string(buf[:n]) != "chunk" {
		t.Fatalf("read = %q err=%v", buf[:n], err)
	}
	if stream.buffered != 0 {
		t.Fatalf("buffered = %d after read", stream.buffered)
	}
	tunnel.closeStream(stream.id)
	if _, err := reader.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("closed stream error = %v, want EOF", err)
	}
}
