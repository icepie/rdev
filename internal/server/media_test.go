package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"rdev/internal/protocol"
	"strings"
	"testing"
	"time"
)

type mediaTestTransport struct {
	srv            *Server
	data           []byte
	old, stall     bool
	reads, cancels int
	noRoot         bool
	lastRead       protocol.Message
}

func (t *mediaTestTransport) WriteJSON(raw []byte) error {
	var m protocol.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.Type == protocol.MsgFileTransferCancel {
		t.cancels++
		return nil
	}
	if m.Type != protocol.MsgFileDownloadStart {
		return nil
	}
	t.reads++
	t.lastRead = m
	if t.stall {
		return nil
	}
	n := int64(len(t.data)) - m.Offset
	if m.Length > 0 {
		n = min(m.Length, n)
	}
	meta := &protocol.Message{Type: protocol.MsgFileDownloadStart, TaskID: m.TaskID, DownloadRoot: m.DownloadRoot, Size: int64(len(t.data)), Offset: m.Offset, Length: n, ModTime: "2026-11-08T00:00:00Z"}
	if t.noRoot {
		meta.DownloadRoot = ""
	}
	if t.old {
		meta.Length = 0
	}
	t.srv.routeAgentFileMsg(meta)
	if n > 0 {
		t.srv.routeAgentFileBinary(protocol.EncodeBinFrameOffset(protocol.BinFileDownloadChunk, m.TaskID, m.Offset, t.data[m.Offset:m.Offset+n]))
	}
	t.srv.routeAgentFileMsg(&protocol.Message{Type: protocol.MsgFileTransferEnd, TaskID: m.TaskID, Offset: m.Offset + n, Success: true})
	return nil
}
func (t *mediaTestTransport) WriteBinary([]byte) error { return nil }
func (t *mediaTestTransport) WritePing([]byte) error   { return nil }
func (t *mediaTestTransport) Close(string) error       { return nil }
func (t *mediaTestTransport) RemoteAddr() string       { return "test" }
func newMediaTestServer(t *testing.T) (*Server, *ClientConn, *mediaTestTransport) {
	s := newPrefsTestServer(t)
	s.agentFileTasks = make(map[string]*agentFileTask)
	tr := &mediaTestTransport{srv: s, data: bytes.Repeat([]byte("0123456789"), 100000)}
	c := &ClientConn{ID: "media", Password: "device-password", ConfinedDownloads: true, Transport: tr}
	s.clients = map[string]*ClientConn{c.ID: c}
	return s, c, tr
}
func TestMediaHTTPRangesAndAuthorization(t *testing.T) {
	s, c, tr := newMediaTestServer(t)
	url, err := s.grantFilePreview(c, "/fixture.mp4")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, rangeHeader string
		status, start, end        int
	}{
		{"full", "GET", "", 200, 0, 1000000}, {"middle", "GET", "bytes=17-100", 206, 17, 101},
		{"suffix", "GET", "bytes=-12", 206, 999988, 1000000}, {"open", "GET", "bytes=999980-", 206, 999980, 1000000},
		{"head", "HEAD", "", 200, 0, 0}, {"invalid", "GET", "bytes=1000001-", 416, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, url, nil)
			if tc.rangeHeader != "" {
				r.Header.Set("Range", tc.rangeHeader)
			}
			w := httptest.NewRecorder()
			s.HandleMedia(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.status < 400 && !bytes.Equal(w.Body.Bytes(), tr.data[tc.start:tc.end]) {
				t.Fatalf("body length %d", w.Body.Len())
			}
			if tc.status < 400 && (w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("Content-Type") != "video/mp4") {
				t.Fatal(w.Header())
			}
			if len(s.agentFileTasks) != 0 {
				t.Fatal("leaked transfer")
			}
		})
	}
	for _, tc := range []struct {
		name, url, origin, method string
		status                    int
	}{
		{"missing ticket", "/api/media/missing", "", "GET", 404},
		{"cross origin", url, "https://other.example", "GET", 403},
		{"mutation", url, "", "POST", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tr.reads
			r := httptest.NewRequest(tc.method, tc.url, nil)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.HandleMedia(w, r)
			if w.Code != tc.status || tr.reads != before {
				t.Fatalf("unauthorized request: %d reads=%d", w.Code, tr.reads-before)
			}
		})
	}
	c.Password = "changed"
	w := httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 404 {
		t.Fatal("password rotation did not revoke grant")
	}
	c.Password = "device-password"
	g := s.mediaGrants[strings.TrimPrefix(url, "/api/media/")]
	g.expires = time.Now().Add(-time.Second)
	s.mediaGrants[strings.TrimPrefix(url, "/api/media/")] = g
	w = httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 404 {
		t.Fatal("expired grant accepted")
	}
}
func TestMediaRejectsOldClientAndCancels(t *testing.T) {
	for _, old := range []bool{true, false} {
		s, c, tr := newMediaTestServer(t)
		tr.old = old
		tr.stall = !old
		ctx, cancel := context.WithCancel(context.Background())
		if !old {
			cancel()
		}
		defer cancel()
		_, _, err := s.readMediaRange(ctx, c, "/fixture.mp4", 0, 16)
		if err == nil || len(s.agentFileTasks) != 0 || tr.cancels != 1 {
			t.Fatalf("error %v, tasks %d cancels %d", err, len(s.agentFileTasks), tr.cancels)
		}
	}
}

func TestHTMLPreviewResponseSandboxAndSizeLimit(t *testing.T) {
	s, c, tr := newMediaTestServer(t)
	tr.data = []byte(`<!doctype html><h1>Preview</h1><script>document.body.dataset.ready="yes"</script>`)
	url, err := s.grantFilePreview(c, "/fixture.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		before := tr.reads
		w := httptest.NewRecorder()
		s.HandleMedia(w, httptest.NewRequest(method, url, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatal(w.Code, w.Header())
		}
		policy := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "sandbox allow-scripts;") || strings.Contains(policy, "allow-same-origin") || !strings.Contains(policy, "connect-src example.com/api/media/") {
			t.Fatal("unsafe standalone HTML policy", policy)
		}
		if method == "GET" && !bytes.Equal(w.Body.Bytes(), tr.data) {
			t.Fatal("authored HTML changed")
		}
		if method == "HEAD" && tr.reads-before != 1 {
			t.Fatal("HEAD downloaded the HTML body")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned body")
		}
		if len(s.agentFileTasks) != 0 {
			t.Fatal("HTML read leaked transfer")
		}
	}
	tr.data = bytes.Repeat([]byte("x"), agentFileReadCap+1)
	w := httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", url, nil))
	if w.Code != 502 || w.Header().Get("Content-Type") == "text/html; charset=utf-8" {
		t.Fatal("oversized HTML was served as a partial document")
	}
	if tr.cancels == 0 || len(s.agentFileTasks) != 0 {
		t.Fatal("oversized read not canceled")
	}
}
