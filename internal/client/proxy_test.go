package client

import (
	"net/url"
	"testing"
)

func TestCanonicalProxyAddr(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"http://127.0.0.1:7890", "127.0.0.1:7890"},
		{"http://proxy.local", "proxy.local:80"},
		{"https://proxy.local", "proxy.local:443"},
		{"http://[::1]:7890", "[::1]:7890"},
	}
	for _, tt := range tests {
		parsed, err := url.Parse(tt.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalProxyAddr(parsed); got != tt.want {
			t.Fatalf("canonicalProxyAddr(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}
