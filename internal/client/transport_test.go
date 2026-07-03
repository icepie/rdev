package client

import "testing"

func TestNormalizeServerEndpoint(t *testing.T) {
	cases := []struct {
		in           string
		wantKind     string
		wantEndpoint string
	}{
		{"1.2.3.4:8081", "tcp", "tcp://1.2.3.4:8081"},
		{"tcp:///1.2.3.4:8081", "tcp", "tcp://1.2.3.4:8081"},
		{"kcp://1.2.3.4:8082", "kcp", "kcp://1.2.3.4:8082"},
		{"udp://1.2.3.4:8082", "kcp", "udp://1.2.3.4:8082"},
		{"ws://1.2.3.4:8080", "ws", "ws://1.2.3.4:8080"},
		{"https://rdev.example.com", "ws", "https://rdev.example.com"},
	}
	for _, tc := range cases {
		kind, endpoint, err := normalizeServerEndpoint(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if kind != tc.wantKind || endpoint != tc.wantEndpoint {
			t.Fatalf("%s: got %s %s, want %s %s", tc.in, kind, endpoint, tc.wantKind, tc.wantEndpoint)
		}
	}
}

func TestSplitServerEndpoints(t *testing.T) {
	got := splitServerEndpoints(" tcp://a:1, kcp://a:2 ,, ws://a:3 ")
	want := []string{"tcp://a:1", "kcp://a:2", "ws://a:3"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}
