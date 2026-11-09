package server

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestRunnerDefaults(t *testing.T) {
	s := NewServer()
	s.TCPPort, s.KCPPort = "8481", "8482"
	for _, tc := range []struct{ url, proto, want string }{
		{"http://example.com:8080/run.sh", "", "tcp://example.com:8481,kcp://example.com:8482,ws://example.com:8080"},
		{"https://example.com/run.sh", "", "tcp://example.com:8481,kcp://example.com:8482,wss://example.com"},
		{"http://example.com/run.sh", "https", "tcp://example.com:8481,kcp://example.com:8482,wss://example.com"},
		{"http://[::1]:8080/run.sh", "", "tcp://[::1]:8481,kcp://[::1]:8482,ws://[::1]:8080"},
	} {
		t.Run(tc.url+tc.proto, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.url, nil)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			w := httptest.NewRecorder()
			s.StaticHandler().ServeHTTP(w, req)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "RDEV_SERVER='"+tc.want+"'") {
				t.Fatalf("status %d: missing defaults %s", w.Code, tc.want)
			}
			// Execute only argument parsing, never the download or client startup.
			prefix, _, ok := strings.Cut(w.Body.String(), "# ── Random password")
			if !ok {
				t.Fatal("runner parse boundary missing")
			}
			for _, override := range []string{"", "wss://override.example"} {
				args := []string{"-s", "--"}
				if override != "" {
					args = append(args, "--server", override)
				}
				cmd := exec.Command("sh", args...)
				cmd.Stdin = strings.NewReader(prefix + "\nprintf '%s' \"$RDEV_SERVER\"\n")
				got, err := cmd.CombinedOutput()
				want := tc.want
				if override != "" {
					want = override
				}
				if err != nil || string(got) != want {
					t.Fatalf("shell defaults/override: %q, %v", got, err)
				}
			}
		})
	}
}

func TestRunnerWindowsAndMethods(t *testing.T) {
	s := NewServer()
	for _, path := range []string{"/run.sh", "/run.ps1"} {
		w := httptest.NewRecorder()
		s.StaticHandler().ServeHTTP(w, httptest.NewRequest("GET", "https://example.com"+path, nil))
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, "{{.Server}}") || !strings.Contains(body, "'wss://example.com'") || strings.Contains(body, "tcp://example.com:") {
			t.Fatalf("invalid runner %s", path)
		}
		if path == "/run.ps1" && !strings.Contains(body, "[object]$Server = 'wss://example.com'") {
			t.Fatal("missing PowerShell default")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("origin-dependent script must not be cached")
		}
		for _, method := range []string{"HEAD", "POST"} {
			w = httptest.NewRecorder()
			s.StaticHandler().ServeHTTP(w, httptest.NewRequest(method, "http://example.com"+path, nil))
			if method == "HEAD" && (w.Code != 200 || w.Body.Len() != 0) {
				t.Fatal("invalid HEAD response")
			}
			if method == "POST" && w.Code != http.StatusMethodNotAllowed {
				t.Fatal("POST accepted")
			}
		}
	}
}
