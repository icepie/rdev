package updater

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
)

const repo = "icepie/rdev"

// updatePublicKeyB64 is the embedded ed25519 public key (base64, 32 bytes)
// used to verify release asset signatures. Assets are signed in CI by
// scripts/sign with the private key stored in the RDEV_UPDATE_SIGNING_KEY
// GitHub secret; unsigned or tampered downloads must never be applied.
const updatePublicKeyB64 = "jtVaB2ppsCLhcYyYYQjRKiviDvyPZvr+AS8nqOQlkUk="

var updatePublicKey = mustDecodePublicKey(updatePublicKeyB64)

func mustDecodePublicKey(b64 string) ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(key) != ed25519.PublicKeySize {
		panic("invalid embedded update public key")
	}
	return ed25519.PublicKey(key)
}

var defaultProxyPrefixes = []string{
	"",
	"https://gh-proxy.com/",
	"https://gh-proxy.net/",
	"https://gh.llkk.cc/",
	"https://hub.gitmirror.com/",
}

type Config struct {
	App      string
	Version  string
	Enabled  bool
	Interval time.Duration
	Logger   *log.Logger
}

type releaseInfo struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func Start(ctx context.Context, cfg Config) {
	if !cfg.Enabled {
		return
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	go run(ctx, cfg)
}

func run(ctx context.Context, cfg Config) {
	failureLog := updateFailureLogger{summaryEvery: 15 * time.Minute}
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			updated, err := CheckAndApply(ctx, cfg)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				failureLog.record(cfg.Logger, err, time.Now())
			} else if updated {
				cfg.Logger.Printf("auto-update applied; restarting into new version")
				if err := restartSelf(); err != nil {
					cfg.Logger.Printf("auto-update restart failed: %v", err)
				}
				return
			} else {
				failureLog.recovered(cfg.Logger)
			}
			timer.Reset(cfg.Interval)
		}
	}
}

type updateFailureLogger struct {
	lastErr      string
	suppressed   int
	lastLoggedAt time.Time
	summaryEvery time.Duration
}

func (l *updateFailureLogger) record(logger *log.Logger, err error, now time.Time) {
	message := err.Error()
	if l.summaryEvery <= 0 {
		l.summaryEvery = 15 * time.Minute
	}
	if message != l.lastErr {
		if l.lastErr != "" && l.suppressed > 0 {
			logger.Printf("auto-update check changed after suppressing %d repeated failures", l.suppressed)
		}
		l.lastErr = message
		l.suppressed = 0
		l.lastLoggedAt = now
		logger.Printf("auto-update check failed: %v", err)
		return
	}
	l.suppressed++
	if now.Sub(l.lastLoggedAt) >= l.summaryEvery {
		logger.Printf("auto-update check still failing; suppressed %d repeated failures: %v", l.suppressed, err)
		l.suppressed = 0
		l.lastLoggedAt = now
	}
}

func (l *updateFailureLogger) recovered(logger *log.Logger) {
	if l.lastErr == "" {
		return
	}
	if l.suppressed > 0 {
		logger.Printf("auto-update check recovered; suppressed %d repeated failures", l.suppressed)
	}
	l.lastErr = ""
	l.suppressed = 0
	l.lastLoggedAt = time.Time{}
}

func CheckAndApply(ctx context.Context, cfg Config) (bool, error) {
	current := normalizeVersion(cfg.Version)
	if current == "" || current == "dev" {
		return false, nil
	}
	release, err := latestRelease(ctx)
	if err != nil {
		return false, err
	}
	latest := normalizeVersion(release.TagName)
	if latest == "" || !newerVersion(latest, current) {
		return false, nil
	}
	assetName := releaseAssetName(cfg.App)
	assetURL := ""
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			assetURL = asset.URL
			break
		}
	}
	if assetURL == "" {
		return false, fmt.Errorf("release %s asset %s not found", release.TagName, assetName)
	}
	data, err := downloadAssetWithProxies(ctx, assetURL)
	if err != nil {
		return false, err
	}
	// Security: the release JSON and asset URL pass through third-party
	// proxies, so the downloaded bytes cannot be trusted until the ed25519
	// signature (fetched as <asset>.sig from the same release) verifies.
	sigData, err := downloadSignatureWithProxies(ctx, assetURL+".sig")
	if err != nil {
		return false, fmt.Errorf("release %s asset %s rejected: %w", release.TagName, assetName, err)
	}
	if err := verifyAssetSignature(updatePublicKey, data, sigData); err != nil {
		return false, fmt.Errorf("release %s asset %s rejected: %w", release.TagName, assetName, err)
	}
	if err := selfupdate.Apply(bytes.NewReader(data), selfupdate.Options{}); err != nil {
		if rollbackErr := selfupdate.RollbackError(err); rollbackErr != nil {
			return false, fmt.Errorf("apply update failed: %w; rollback failed: %v", err, rollbackErr)
		}
		return false, err
	}
	return true, nil
}

func latestRelease(ctx context.Context) (*releaseInfo, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	data, err := downloadJSONWithProxies(ctx, url)
	if err != nil {
		return nil, err
	}
	var release releaseInfo
	if err := json.Unmarshal(data, &release); err != nil {
		return nil, err
	}
	if release.TagName == "" || release.Draft || release.Prerelease {
		return nil, errors.New("no stable latest release")
	}
	return &release, nil
}

func downloadJSONWithProxies(ctx context.Context, target string) ([]byte, error) {
	return downloadWithProxies(ctx, target, func(url string, resp *http.Response, body []byte) error {
		if !json.Valid(body) {
			return fmt.Errorf("%s returned non-json response", url)
		}
		return nil
	})
}

func downloadAssetWithProxies(ctx context.Context, target string) ([]byte, error) {
	return downloadWithProxies(ctx, target, func(url string, resp *http.Response, body []byte) error {
		if looksLikeHTML(resp, body) {
			return fmt.Errorf("%s returned html instead of release asset", url)
		}
		if len(body) == 0 {
			return fmt.Errorf("%s returned empty release asset", url)
		}
		return nil
	})
}

// downloadSignatureWithProxies fetches a base64-encoded ed25519 signature and
// validates its shape; the signature itself is checked separately.
func downloadSignatureWithProxies(ctx context.Context, target string) ([]byte, error) {
	var sig []byte
	_, err := downloadWithProxies(ctx, target, func(url string, resp *http.Response, body []byte) error {
		decoded, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body)))
		if err != nil || len(decoded) != ed25519.SignatureSize {
			return fmt.Errorf("%s returned invalid asset signature", url)
		}
		sig = decoded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sig, nil
}

// verifyAssetSignature rejects any asset whose bytes do not match the
// detached ed25519 signature produced by scripts/sign in release CI.
func verifyAssetSignature(pub ed25519.PublicKey, data, sig []byte) error {
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("invalid signature length %d", len(sig))
	}
	if !ed25519.Verify(pub, data, sig) {
		return errors.New("ed25519 signature mismatch")
	}
	return nil
}

func downloadWithProxies(ctx context.Context, target string, validate func(string, *http.Response, []byte) error) ([]byte, error) {
	client := &http.Client{Timeout: 3 * time.Minute}
	var lastErr error
	for _, prefix := range proxyPrefixes() {
		url := proxiedURL(prefix, target)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "rdev-auto-updater")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("%s returned %s", url, resp.Status)
			continue
		}
		if validate != nil {
			if err := validate(url, resp, body); err != nil {
				lastErr = err
				continue
			}
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = errors.New("all update URLs failed")
	}
	return nil, lastErr
}

func looksLikeHTML(resp *http.Response, body []byte) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") {
		return true
	}
	prefix := bytes.TrimSpace(body)
	if len(prefix) > 256 {
		prefix = prefix[:256]
	}
	prefix = bytes.ToLower(prefix)
	return bytes.HasPrefix(prefix, []byte("<html")) || bytes.HasPrefix(prefix, []byte("<!doctype html")) || bytes.HasPrefix(prefix, []byte("<"))
}

func proxyPrefixes() []string {
	prefixes := []string{}
	if env := strings.TrimSpace(os.Getenv("RDEV_UPDATE_PROXY")); env != "" {
		for _, part := range strings.Split(env, ",") {
			if p := strings.TrimSpace(part); p != "" {
				prefixes = append(prefixes, p)
			}
		}
	}
	prefixes = append(prefixes, defaultProxyPrefixes...)
	seen := map[string]bool{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		// Security: only https proxy prefixes are allowed (loopback http is
		// kept for tests); a plaintext proxy could otherwise serve tampered
		// assets/signatures and defeat the download path before verification.
		if !validProxyPrefix(prefix) {
			continue
		}
		out = append(out, prefix)
	}
	return out
}

// validProxyPrefix reports whether a proxy prefix uses an acceptable scheme:
// https, or http only when targeting loopback (for local testing).
func validProxyPrefix(prefix string) bool {
	if prefix == "" {
		return true
	}
	u, err := url.Parse(prefix)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}

func proxiedURL(prefix, target string) string {
	if prefix == "" {
		return target
	}
	if strings.Contains(prefix, "${url}") {
		return strings.ReplaceAll(prefix, "${url}", target)
	}
	return strings.TrimRight(prefix, "/") + "/" + target
}

func releaseAssetName(app string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("rdev-%s-%s-%s%s", app, runtime.GOOS, runtime.GOARCH, ext)
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(version, "v")
	if idx := strings.IndexAny(version, "-+"); idx >= 0 {
		version = version[:idx]
	}
	if version == "" || version[0] < '0' || version[0] > '9' {
		return "dev"
	}
	return version
}

func newerVersion(latest, current string) bool {
	la := versionParts(latest)
	cu := versionParts(current)
	for i := 0; i < 3; i++ {
		if la[i] != cu[i] {
			return la[i] > cu[i]
		}
	}
	return false
}

func versionParts(version string) [3]int {
	var out [3]int
	parts := strings.Split(version, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		fmt.Sscanf(parts[i], "%d", &out[i])
	}
	return out
}
