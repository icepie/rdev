package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestOriginOK(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin header (non-browser client)", "example.com:8080", "", true},
		{"matching origin", "example.com:8080", "http://example.com:8080", true},
		{"matching origin https", "example.com:8080", "https://example.com:8080", true},
		{"case-insensitive host", "Example.com:8080", "http://example.com:8080", true},
		{"different host", "example.com:8080", "http://evil.com:8080", false},
		{"different port", "example.com:8080", "http://example.com:9090", false},
		{"subdomain is not a match", "example.com:8080", "http://sub.example.com:8080", false},
		{"unparseable origin", "example.com:8080", "://not a url", false},
		{"origin without host", "example.com:8080", "null", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/terminal", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := originOK(r); got != tc.want {
				t.Fatalf("originOK(host=%q origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func TestValidClientID(t *testing.T) {
	for _, id := range []string{"device", "PC-01", "my.host_1", "a", "Z9.-_"} {
		if !validClientID(id) {
			t.Fatalf("validClientID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",           // empty
		"has space",  // space
		"slash/path", // slash
		"quote\"id",  // quote
		"colon:id",   // colon
		"unié",       // non-ASCII
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 65 chars
	}
	for _, id := range invalid {
		if validClientID(id) {
			t.Fatalf("validClientID(%q) = true, want false", id)
		}
	}
	// Boundary: exactly 64 chars is allowed.
	sixtyFour := ""
	for len(sixtyFour) < 64 {
		sixtyFour += "x"
	}
	if !validClientID(sixtyFour) {
		t.Fatal("validClientID(64 chars) = false, want true")
	}
}

func TestAuthRateLimiterTriggersBackoffAndResets(t *testing.T) {
	now := time.Now()
	limiter := newAuthRateLimiter()
	limiter.now = func() time.Time { return now }

	const device = "dev-1"
	if d := limiter.wait(device); d != 0 {
		t.Fatalf("initial wait = %v, want 0", d)
	}

	// First failure imposes the base delay.
	if d := limiter.fail(device); d != authFailBaseDelay {
		t.Fatalf("first fail delay = %v, want %v", d, authFailBaseDelay)
	}
	if d := limiter.wait(device); d <= 0 {
		t.Fatal("expected cooldown after first failure")
	}

	// Cooldown expires with time.
	now = now.Add(authFailBaseDelay + time.Millisecond)
	if d := limiter.wait(device); d != 0 {
		t.Fatalf("wait after cooldown = %v, want 0", d)
	}

	// Failures grow exponentially up to the cap.
	prev := time.Duration(0)
	for range 10 {
		d := limiter.fail(device)
		if d > authFailMaxDelay {
			t.Fatalf("delay %v exceeds cap %v", d, authFailMaxDelay)
		}
		if d < prev {
			t.Fatalf("delay shrunk: %v < %v", d, prev)
		}
		prev = d
	}
	if prev != authFailMaxDelay {
		t.Fatalf("delay after many failures = %v, want capped %v", prev, authFailMaxDelay)
	}

	// Success clears the state.
	limiter.success(device)
	if d := limiter.wait(device); d != 0 {
		t.Fatalf("wait after success = %v, want 0", d)
	}
	if d := limiter.fail(device); d != authFailBaseDelay {
		t.Fatalf("delay after reset = %v, want base %v", d, authFailBaseDelay)
	}
}

func TestAuthRateLimiterIsPerDevice(t *testing.T) {
	limiter := newAuthRateLimiter()
	limiter.fail("dev-a")
	if d := limiter.wait("dev-b"); d != 0 {
		t.Fatalf("unrelated device throttled: wait = %v", d)
	}
}
