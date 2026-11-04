package main

import "testing"

func TestServerHTTPBasePrefersPublicEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		httpHost  string
		want      string
	}{
		{
			name:      "wss keeps tls and drops path",
			serverURL: "tcp://1.2.3.4:8081,kcp://1.2.3.4:8082,wss://r.niceaigc.net/ws",
			want:      "https://r.niceaigc.net",
		},
		{
			name:      "ws keeps port",
			serverURL: "ws://192.0.2.10:8080/ws",
			want:      "http://192.0.2.10:8080",
		},
		{
			name:      "http endpoint",
			serverURL: "https://rdev.example.com/path?q=1",
			want:      "https://rdev.example.com",
		},
		{
			name:      "falls back to advertised host",
			serverURL: "tcp://192.0.2.10:8081",
			httpHost:  "rdev.example.com:8080",
			want:      "http://rdev.example.com:8080",
		},
		{
			name:      "no http-capable endpoint",
			serverURL: "tcp://192.0.2.10:8081,kcp://192.0.2.10:8082",
			want:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serverHTTPBase(tt.serverURL, tt.httpHost); got != tt.want {
				t.Fatalf("serverHTTPBase(%q, %q) = %q, want %q", tt.serverURL, tt.httpHost, got, tt.want)
			}
		})
	}
}
