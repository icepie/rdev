package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
)

const repo = "icepie/rdev"

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
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			updated, err := CheckAndApply(ctx, cfg)
			if err != nil {
				cfg.Logger.Printf("auto-update check failed: %v", err)
			} else if updated {
				cfg.Logger.Printf("auto-update applied; restarting into new version")
				if err := restartSelf(); err != nil {
					cfg.Logger.Printf("auto-update restart failed: %v", err)
				}
				return
			}
			timer.Reset(cfg.Interval)
		}
	}
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
	data, err := downloadWithProxies(ctx, assetURL)
	if err != nil {
		return false, err
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
	data, err := downloadWithProxies(ctx, url)
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

func downloadWithProxies(ctx context.Context, target string) ([]byte, error) {
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
		return body, nil
	}
	if lastErr == nil {
		lastErr = errors.New("all update URLs failed")
	}
	return nil, lastErr
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
		out = append(out, prefix)
	}
	return out
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
