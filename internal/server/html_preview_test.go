package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTMLPreviewRelativeResourcesAndRevocation(t *testing.T) {
	s, c, tr := newMediaTestServer(t)
	entry, err := s.grantFilePreview(c, "/project/site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	base := entry[:strings.LastIndex(entry, "/")+1]
	tr.data = []byte("body{color:green}")
	bare := httptest.NewRecorder()
	s.HandleMedia(bare, httptest.NewRequest("GET", strings.TrimSuffix(base, "/"), nil))
	if bare.Code != 404 || tr.reads != 0 {
		t.Fatal("directory ticket bypassed confined reads")
	}

	for _, name := range []string{"assets/style.css", "app.mjs", "data.json", "logo.svg", "fonts/inter.woff2"} {
		r := httptest.NewRequest("GET", base+name+"?version=1", nil)
		r.Header.Set("Origin", "null")
		w := httptest.NewRecorder()
		s.HandleMedia(w, r)
		if w.Code != 200 || w.Body.String() != string(tr.data) || tr.lastRead.Path != name || tr.lastRead.DownloadRoot != "/project/site" {
			t.Fatalf("resource %s: status=%d body=%s request=%+v", name, w.Code, w.Body.String(), tr.lastRead)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "null" || w.Header().Get("Access-Control-Allow-Credentials") != "" || w.Header().Get("Content-Type") != previewResourceMIME(name) {
			t.Fatal(w.Header())
		}
		policy := w.Header().Get("Content-Security-Policy")
		if strings.Contains(policy, "'self'") || !strings.Contains(policy, "connect-src example.com"+base+";") || !strings.Contains(policy, "sandbox allow-scripts;") {
			t.Fatal(policy)
		}
	}
	for _, path := range []string{"../outside.css", "%2e%2e/outside.css", "%2Fetc/passwd.css", "assets%5cescape.css", ".env", ".git/config", "hidden/.secret.json", "C:/file.css", "code.go"} {
		before := tr.reads
		w := httptest.NewRecorder()
		s.HandleMedia(w, httptest.NewRequest("GET", base+path, nil))
		if w.Code != 404 || tr.reads != before {
			t.Fatalf("invalid resource %s: status=%d reads=%d", path, w.Code, tr.reads-before)
		}
	}
	before := tr.reads
	r := httptest.NewRequest("GET", base+"app.mjs", nil)
	r.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	s.HandleMedia(w, r)
	if w.Code != 403 || tr.reads != before {
		t.Fatal("foreign origin accepted")
	}
	tr.noRoot = true
	w = httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", entry, nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), string(tr.data)) {
		t.Fatal("old client leaked resource", w.Code, w.Body.String())
	}
	tr.noRoot = false
	c.Password = "rotated"
	w = httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", base+"app.mjs", nil))
	if w.Code != 404 {
		t.Fatal("password rotation did not revoke resource")
	}
	c.Password = "device-password"
	key := strings.Trim(strings.TrimPrefix(base, "/api/media/"), "/")
	grant := s.mediaGrants[key]
	grant.expires = time.Now().Add(-time.Second)
	s.mediaGrants[key] = grant
	w = httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", entry, nil))
	if w.Code != 404 {
		t.Fatal("expired resource grant accepted")
	}
	if len(s.agentFileTasks) != 0 {
		t.Fatal("preview tasks leaked")
	}
}

func TestLegacyHTMLPreviewRemainsSingleFile(t *testing.T) {
	s, c, tr := newMediaTestServer(t)
	c.ConfinedDownloads = false
	tr.data = []byte("<h1>Legacy inline preview</h1>")
	entry, err := s.grantFilePreview(c, "/site/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.TrimPrefix(entry, "/api/media/"), "/") {
		t.Fatal("legacy client given directory preview", entry)
	}
	w := httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", entry, nil))
	if w.Code != 200 || w.Body.String() != string(tr.data) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'none'") {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	before := tr.reads
	w = httptest.NewRecorder()
	s.HandleMedia(w, httptest.NewRequest("GET", entry+"/style.css", nil))
	if w.Code != 404 || tr.reads != before {
		t.Fatal("legacy grant allowed directory resource")
	}
	c.ConfinedDownloads = true
	upgraded, err := s.grantFilePreview(c, "/site/index.html")
	if err != nil || !strings.HasSuffix(upgraded, "/index.html") || strings.HasPrefix(upgraded, entry+"/") {
		t.Fatal("upgrade reused legacy authority", upgraded, err)
	}
}
