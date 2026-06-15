package updater

import (
	"runtime"
	"testing"
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
