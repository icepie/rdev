package client

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestClientIdentityConcurrentEnrollment(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	const count = 16
	values := make([]savedIdentity, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range values {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			values[i], _, errs[i] = loadClientIdentity("wss://example.test/Agent", "device")
		}(i)
	}
	wg.Wait()
	for i, value := range values {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if value != values[0] {
			t.Fatal("concurrent enrollments returned different credentials")
		}
	}
	reloaded, _, err := loadClientIdentity("wss://example.test/Agent", "device")
	if err != nil || reloaded != values[0] {
		t.Fatalf("reload = %#v %v", reloaded, err)
	}
}

func TestClientIdentityRejectsCorruptOrIncompleteStorage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RDEV_IDENTITY_DIR", root)
	path, err := clientIdentityPath("wss://example.test", "device")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{"deviceIdentity":"` + strings.Repeat("a", 64) + `"}`,
		`{"deviceIdentity":"` + strings.Repeat("a", 64) + `","reconnectProof":"` + strings.Repeat("A", 64) + `"}`,
		strings.Repeat("x", 4097),
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadClientIdentity("wss://example.test", "device"); err == nil {
			t.Fatal("corrupt credential accepted")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatal("corrupt credential silently overwritten")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadClientIdentity("wss://example.test", "device"); err == nil {
		t.Fatal("non-file credential accepted")
	}
}

func TestClientIdentityCanonicalScopeAndPrivateFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RDEV_IDENTITY_DIR", root)
	saved, path, err := loadClientIdentity("wss://EXAMPLE.test:443/Agent/,tcp://backup:1700", "device")
	if err != nil {
		t.Fatal(err)
	}
	other, otherPath, err := loadClientIdentity("wss://example.test/Agent", "device")
	if err != nil || saved != other || path != otherPath {
		t.Fatalf("canonical scope differs: %s %s %v", path, otherPath, err)
	}
	if filepath.Dir(path) != root || !validClientCredential(saved.DeviceIdentity) || !validClientCredential(saved.ReconnectProof) {
		t.Fatal("invalid persisted identity")
	}
	lower, _, err := loadClientIdentity("wss://example.test/agent", "device")
	if err != nil || lower == saved {
		t.Fatal("case-sensitive endpoint path conflated")
	}
}
