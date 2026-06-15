package updater

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	tests := []struct {
		latest  string
		current string
		want    bool
	}{
		{"0.2.43", "0.2.42", true},
		{"0.3.0", "0.2.99", true},
		{"1.0.0", "0.9.9", true},
		{"0.2.42", "0.2.42", false},
		{"0.2.41", "0.2.42", false},
	}
	for _, tt := range tests {
		if got := newerVersion(tt.latest, tt.current); got != tt.want {
			t.Fatalf("newerVersion(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	if got := normalizeVersion("v0.2.43"); got != "0.2.43" {
		t.Fatalf("normalize tag = %q", got)
	}
	if got := normalizeVersion("main"); got != "dev" {
		t.Fatalf("normalize branch = %q", got)
	}
	if got := normalizeVersion("0.2.43-dirty"); got != "0.2.43" {
		t.Fatalf("normalize dirty = %q", got)
	}
}

func TestReleaseAssetName(t *testing.T) {
	name := releaseAssetName("client")
	expected := "rdev-client-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		expected += ".exe"
	}
	if name != expected {
		t.Fatalf("releaseAssetName = %q, want %q", name, expected)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}}
	if !looksLikeHTML(resp, []byte("ok")) {
		t.Fatal("content-type html was not detected")
	}
	resp = &http.Response{Header: http.Header{"Content-Type": []string{"application/octet-stream"}}}
	if !looksLikeHTML(resp, []byte("  <!doctype html><html></html>")) {
		t.Fatal("html body was not detected")
	}
	if looksLikeHTML(resp, []byte("MZ"+strings.Repeat("x", 1024))) {
		t.Fatal("binary body was detected as html")
	}
}

func TestUpdateFailureLoggerSuppressesRepeats(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	failureLog := updateFailureLogger{summaryEvery: 15 * time.Minute}
	now := time.Unix(1000, 0)

	failureLog.record(logger, errors.New("dns failed"), now)
	failureLog.record(logger, errors.New("dns failed"), now.Add(time.Minute))
	failureLog.record(logger, errors.New("dns failed"), now.Add(2*time.Minute))
	if got := strings.Count(buf.String(), "auto-update check failed"); got != 1 {
		t.Fatalf("initial repeated failures logged %d times, want 1\n%s", got, buf.String())
	}
	if strings.Contains(buf.String(), "still failing") {
		t.Fatalf("summary logged too early:\n%s", buf.String())
	}

	failureLog.record(logger, errors.New("dns failed"), now.Add(16*time.Minute))
	if !strings.Contains(buf.String(), "suppressed 3 repeated failures") {
		t.Fatalf("missing periodic summary:\n%s", buf.String())
	}
}

func TestUpdateFailureLoggerRecovered(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	failureLog := updateFailureLogger{summaryEvery: 15 * time.Minute}
	now := time.Unix(1000, 0)

	failureLog.record(logger, errors.New("dns failed"), now)
	failureLog.record(logger, errors.New("dns failed"), now.Add(time.Minute))
	failureLog.recovered(logger)
	if !strings.Contains(buf.String(), "recovered; suppressed 1 repeated failures") {
		t.Fatalf("missing recovery summary:\n%s", buf.String())
	}
}

func TestProxiedURL(t *testing.T) {
	target := "https://github.com/icepie/rdev/releases/download/v0.2.43/rdev-client-linux-amd64"
	if got := proxiedURL("", target); got != target {
		t.Fatalf("direct URL = %q", got)
	}
	if got := proxiedURL("https://gh-proxy.com/", target); got != "https://gh-proxy.com/"+target {
		t.Fatalf("proxy prefix URL = %q", got)
	}
	if got := proxiedURL("http://proxy/?url=${url}", target); got != "http://proxy/?url="+target {
		t.Fatalf("proxy template URL = %q", got)
	}
}
