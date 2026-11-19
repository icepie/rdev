package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type savedIdentity struct {
	DeviceIdentity string `json:"deviceIdentity"`
	ReconnectProof string `json:"reconnectProof"`
}

// DefaultClientID returns the stable short ID for this endpoint and identity directory.
// The empty requested-ID scope stores its seed; enrollment credentials remain
// scoped to the resulting requested ID, just like explicitly configured IDs.
func DefaultClientID(server string) (string, error) {
	saved, _, err := loadClientIdentity(server, "")
	if err != nil {
		return "", fmt.Errorf("initialize default client ID: %w", err)
	}
	return "rdev-" + saved.DeviceIdentity[:12], nil
}

func clientIdentityPath(server, clientID string) (string, error) {
	root := os.Getenv("RDEV_IDENTITY_DIR")
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(root, "rdev")
	}
	normalized := normalizeIdentityEndpoint(server)
	sum := sha256.Sum256([]byte(normalized + "\x00" + strings.TrimSpace(clientID)))
	return filepath.Join(root, "identity-"+hex.EncodeToString(sum[:16])+".json"), nil
}

func normalizeIdentityEndpoint(server string) string {
	server = strings.TrimSpace(strings.SplitN(server, ",", 2)[0])
	if parsed, err := url.Parse(server); err == nil && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		if (parsed.Scheme == "ws" || parsed.Scheme == "http") && parsed.Port() == "80" || (parsed.Scheme == "wss" || parsed.Scheme == "https") && parsed.Port() == "443" {
			host := parsed.Hostname()
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			parsed.Host = host
		} else if parsed.Port() != "" {
			parsed.Host = net.JoinHostPort(parsed.Hostname(), parsed.Port())
		}
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		return parsed.String()
	}
	return server
}

func loadClientIdentity(server, clientID string) (savedIdentity, string, error) {
	path, err := clientIdentityPath(server, clientID)
	if err != nil {
		return savedIdentity{}, "", err
	}
	saved, err := readClientIdentity(path)
	if err == nil {
		return saved, path, nil
	}
	if !os.IsNotExist(err) {
		return savedIdentity{}, "", err
	}
	var credential [64]byte
	if _, err := rand.Read(credential[:]); err != nil {
		return savedIdentity{}, "", err
	}
	saved = savedIdentity{DeviceIdentity: hex.EncodeToString(credential[:32]), ReconnectProof: hex.EncodeToString(credential[32:])}
	if err := saveClientIdentity(path, saved); err != nil && !os.IsExist(err) {
		return savedIdentity{}, "", err
	}
	// Another process may have published first. Always use the committed winner.
	saved, err = readClientIdentity(path)
	return saved, path, err
}

func validClientCredential(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func readClientIdentity(path string) (savedIdentity, error) {
	f, err := os.Open(path)
	if err != nil {
		return savedIdentity{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return savedIdentity{}, err
	}
	if len(data) > 4096 {
		return savedIdentity{}, fmt.Errorf("saved device identity exceeds size limit")
	}
	var saved savedIdentity
	if err := json.Unmarshal(data, &saved); err != nil {
		return savedIdentity{}, err
	}
	if !validClientCredential(saved.DeviceIdentity) || !validClientCredential(saved.ReconnectProof) {
		return savedIdentity{}, fmt.Errorf("invalid saved device identity in %s; restore the original credential rather than replacing its proof", path)
	}
	// A concurrent publisher may not yet have synced the directory entry.
	if err := syncClientIdentityDirectory(filepath.Dir(path)); err != nil {
		return savedIdentity{}, err
	}
	return saved, nil
}

func syncClientIdentityDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Credentials are immutable: publish a complete, synced file without replacing
// an existing enrollment, including when multiple client processes start together.
func saveClientIdentity(path string, saved savedIdentity) error {
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := syncClientIdentityDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".identity-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	return syncClientIdentityDirectory(root)
}
