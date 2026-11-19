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

func TestDefaultClientIDPersistsAcrossInitialization(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	const server = "wss://example.test/Agent"
	id, err := DefaultClientID(server)
	if err != nil {
		t.Fatal(err)
	}
	seed, seedPath, err := loadClientIdentity(server, "")
	if err != nil || id != "rdev-"+seed.DeviceIdentity[:12] {
		t.Fatalf("default ID = %q, seed = %#v, err = %v", id, seed, err)
	}
	first := NewClient(server, id, "", "")
	if first.identityErr != nil {
		t.Fatal(first.identityErr)
	}
	credentialPath, err := clientIdentityPath(server, id)
	if err != nil || credentialPath == seedPath {
		t.Fatalf("enrollment must use the derived ID scope: %q, %v", credentialPath, err)
	}
	for range 3 {
		reloadedID, err := DefaultClientID("wss://EXAMPLE.test:443/Agent/,tcp://backup:1700")
		if err != nil || reloadedID != id {
			t.Fatalf("reloaded ID = %q, want %q: %v", reloadedID, id, err)
		}
		reloaded := NewClient(server, reloadedID, "", "")
		if reloaded.identityErr != nil || reloaded.requestedID != id || reloaded.deviceIdentity != first.deviceIdentity || reloaded.reconnectProof != first.reconnectProof {
			t.Fatalf("reinitialization changed enrollment: %v", reloaded.identityErr)
		}
	}
}

func TestDefaultClientIDUsesSavedScope(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		root   string
		server string
		digit  string
	}{
		{firstRoot, "wss://example.test/Agent", "a"},
		{firstRoot, "wss://example.test/agent", "b"},
		{firstRoot, "wss://other.test/Agent", "c"},
		{secondRoot, "wss://example.test/Agent", "d"},
	} {
		t.Setenv("RDEV_IDENTITY_DIR", tc.root)
		path, err := clientIdentityPath(tc.server, "")
		if err != nil {
			t.Fatal(err)
		}
		seed := savedIdentity{DeviceIdentity: strings.Repeat(tc.digit, 64), ReconnectProof: strings.Repeat("e", 64)}
		if err := saveClientIdentity(path, seed); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			got, err := DefaultClientID(tc.server)
			want := "rdev-" + strings.Repeat(tc.digit, 12)
			if err != nil || got != want {
				t.Fatalf("DefaultClientID(%q) in %q = %q, %v; want %q", tc.server, tc.root, got, err, want)
			}
		}
	}
	t.Setenv("RDEV_IDENTITY_DIR", firstRoot)
	if got, err := DefaultClientID("wss://EXAMPLE.test:443/Agent/,tcp://backup:1700"); err != nil || got != "rdev-aaaaaaaaaaaa" {
		t.Fatalf("original canonical scope changed: %q, %v", got, err)
	}
}

func TestDefaultClientIDRejectsCorruptStorage(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	const server = "wss://example.test/Agent"
	path, err := clientIdentityPath(server, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{`,
		`{"deviceIdentity":"short","reconnectProof":"` + strings.Repeat("b", 64) + `"}`,
		`{"deviceIdentity":"` + strings.Repeat("a", 64) + `"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := DefaultClientID(server); err == nil || got != "" {
			t.Fatalf("corrupt default identity returned %q, %v", got, err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != content {
			t.Fatalf("corrupt default identity overwritten: %q, %v", got, err)
		}
	}
	// Explicit IDs never consult the default scope, even when it is corrupt.
	const explicitID = "configured-device"
	first := NewClient(server, explicitID, "", "")
	second := NewClient(server, explicitID, "", "")
	if first.identityErr != nil || second.identityErr != nil {
		t.Fatalf("explicit enrollment failed: %v, %v", first.identityErr, second.identityErr)
	}
	if first.requestedID != explicitID || second.requestedID != explicitID || first.deviceIdentity != second.deviceIdentity || first.reconnectProof != second.reconnectProof {
		t.Fatal("explicit ID or credentials changed")
	}
}

func TestDefaultClientIDRejectsPersistenceFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RDEV_IDENTITY_DIR", root)
	if got, err := DefaultClientID("wss://example.test/Agent"); err == nil || got != "" {
		t.Fatalf("persistence failure returned %q, %v", got, err)
	}
}

func TestDefaultClientIDConcurrentInitialization(t *testing.T) {
	t.Setenv("RDEV_IDENTITY_DIR", t.TempDir())
	const count = 16
	ids := make([]string, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = DefaultClientID("wss://example.test/Agent")
		}(i)
	}
	wg.Wait()
	for i, id := range ids {
		if errs[i] != nil || id == "" || id != ids[0] {
			t.Fatalf("concurrent initialization %d = %q, %v; want %q", i, id, errs[i], ids[0])
		}
	}
}
