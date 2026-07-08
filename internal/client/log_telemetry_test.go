package client

import (
	"strings"
	"testing"
)

func TestSanitizeClientLogRedactsSecrets(t *testing.T) {
	got := sanitizeClientLog("connect --password hunter2 token=abc authorization=Bearer cookie=x secret=y key=z -p pass")
	for _, bad := range []string{"hunter2", "abc", "Bearer", "cookie=x", "secret=y", "key=z", " pass"} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitizeClientLog leaked %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("sanitizeClientLog did not redact: %q", got)
	}
}

func TestClientLogLevelAllowed(t *testing.T) {
	if !logLevelAllowed("error", "info") || !logLevelAllowed("warn", "debug") {
		t.Fatal("expected higher-severity log levels to pass")
	}
	if logLevelAllowed("debug", "info") || logLevelAllowed("trace", "warn") {
		t.Fatal("expected lower-severity log levels to be filtered")
	}
}
