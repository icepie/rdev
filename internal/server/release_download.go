package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func releaseDownloadDirectURL(asset, tag string) string {
	asset = url.PathEscape(asset)
	if tag == "" || tag == "latest" {
		return "https://github.com/icepie/rdev/releases/latest/download/" + asset
	}
	return "https://github.com/icepie/rdev/releases/download/" + url.PathEscape(tag) + "/" + asset
}

func releaseDownloadParams(r *http.Request) (asset, tag string, ok bool) {
	asset = strings.TrimSpace(r.URL.Query().Get("asset"))
	tag = strings.TrimSpace(r.URL.Query().Get("tag"))
	if tag == "" {
		tag = "latest"
	}
	if asset == "" || strings.Contains(asset, "/") || strings.Contains(asset, "\\") || strings.Contains(asset, "..") {
		return "", "", false
	}
	if tag != "latest" {
		clean := strings.TrimPrefix(tag, "v")
		if clean == "" || strings.ContainsAny(clean, "/\\?") || strings.Contains(tag, "..") {
			return "", "", false
		}
		if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
	}
	return asset, tag, true
}

func releaseDownloadCandidates(asset, tag string) []string {
	direct := releaseDownloadDirectURL(asset, tag)
	candidates := make([]string, 0, len(releaseDownloadMirrors)+1)
	for _, mirror := range releaseDownloadMirrors {
		candidates = append(candidates, "https://"+mirror+"/"+direct)
	}
	return append(candidates, direct)
}

func releaseDownloadProxyCandidates(asset, tag string) []string {
	direct := releaseDownloadDirectURL(asset, tag)
	candidates := []string{direct}
	for _, mirror := range releaseDownloadMirrors {
		candidates = append(candidates, "https://"+mirror+"/"+direct)
	}
	return candidates
}

func releaseDownloadChoiceKey(asset, tag string) string {
	return tag + "\x00" + asset
}

func cachedReleaseDownloadChoice(key string, now time.Time) string {
	releaseDownloadChoiceMu.Lock()
	defer releaseDownloadChoiceMu.Unlock()
	choice, ok := releaseDownloadChoices[key]
	if !ok || !choice.expiresAt.After(now) {
		delete(releaseDownloadChoices, key)
		return ""
	}
	return choice.url
}

func cacheReleaseDownloadChoice(key, candidate string, now time.Time) {
	releaseDownloadChoiceMu.Lock()
	releaseDownloadChoices[key] = releaseDownloadChoice{url: candidate, expiresAt: now.Add(releaseDownloadChoiceTTL)}
	releaseDownloadChoiceMu.Unlock()
}

type releaseDownloadProbeResult struct {
	candidate string
	bytes     int64
	duration  time.Duration
}

// fastestReleaseDownloadCandidate measures a small range from every source.
// The selected URL is cached so ordinary client installs redirect immediately.
func fastestReleaseDownloadCandidate(ctx context.Context, candidates []string) string {
	ctx, cancel := context.WithTimeout(ctx, releaseDownloadProbeTimeout)
	defer cancel()

	results := make(chan releaseDownloadProbeResult, len(candidates))
	var wg sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
			if err != nil {
				return
			}
			req.Header.Set("Range", "bytes=0-65535")
			req.Header.Set("User-Agent", "rdev-server")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				return
			}
			n, _ := io.CopyN(io.Discard, resp.Body, releaseDownloadProbeBytes)
			if n > 0 {
				results <- releaseDownloadProbeResult{candidate: candidate, bytes: n, duration: time.Since(started)}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	best := ""
	var bestRate float64
	for result := range results {
		if result.duration <= 0 {
			continue
		}
		rate := float64(result.bytes) / result.duration.Seconds()
		if rate > bestRate {
			best, bestRate = result.candidate, rate
		}
	}
	return best
}

// HandleReleaseDownload redirects clients to the fastest reachable release source.
// Unlike the streaming proxy, the client downloads directly from the selected source.
func (s *Server) HandleReleaseDownload(w http.ResponseWriter, r *http.Request) {
	asset, tag, ok := releaseDownloadParams(r)
	if !ok {
		http.Error(w, "bad asset", http.StatusBadRequest)
		return
	}
	if s.serveLocalReleaseAsset(w, r, asset, tag) {
		return
	}
	if tag == "latest" {
		var err error
		tag, err = s.latestReleaseAssetTag(r.Context(), asset)
		if err != nil {
			writeReleaseResolutionError(w, err)
			return
		}
	}
	key := releaseDownloadChoiceKey(asset, tag)
	if candidate := cachedReleaseDownloadChoice(key, time.Now()); candidate != "" {
		http.Redirect(w, r, candidate, http.StatusFound)
		return
	}
	candidate := fastestReleaseDownloadCandidate(r.Context(), releaseDownloadCandidates(asset, tag))
	if candidate == "" {
		candidate = releaseDownloadDirectURL(asset, tag)
	}
	cacheReleaseDownloadChoice(key, candidate, time.Now())
	http.Redirect(w, r, candidate, http.StatusFound)
}

// serveLocalReleaseAsset serves <ReleaseDir>/<tag>/<asset> directly. It
// returns false when no ReleaseDir is configured; otherwise it always writes
// a response (404 when the file is absent) and never touches GitHub.
func (s *Server) serveLocalReleaseAsset(w http.ResponseWriter, r *http.Request, asset, tag string) bool {
	if s.ReleaseDir == "" {
		return false
	}
	if tag == "latest" {
		var err error
		tag, err = s.latestReleaseAssetTag(r.Context(), asset)
		if err != nil {
			writeReleaseResolutionError(w, err)
			return true
		}
	}
	if tag == "" || tag == "latest" {
		http.Error(w, "release not found", http.StatusNotFound)
		return true
	}
	path := filepath.Join(s.ReleaseDir, tag, asset)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "release not found", http.StatusNotFound)
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "release not found", http.StatusNotFound)
		return true
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+asset+"\"")
	http.ServeContent(w, r, asset, info.ModTime(), f)
	return true
}

// parseReleaseTag splits "v1.2.3" into its numeric components. Pre-release
// suffixes ("-rc1") sort below the plain release of the same version.
func parseReleaseTag(tag string) (nums [3]int, pre bool, ok bool) {
	tag = strings.TrimPrefix(tag, "v")
	if i := strings.IndexByte(tag, '-'); i >= 0 {
		pre = true
		tag = tag[:i]
	}
	parts := strings.Split(tag, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return nums, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, false, false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// lessReleaseTag orders release tags by semantic version, pre-releases first.
func lessReleaseTag(a, b string) bool {
	an, apre, aok := parseReleaseTag(a)
	bn, bpre, bok := parseReleaseTag(b)
	if !aok || !bok {
		return a < b
	}
	if an != bn {
		for i := range an {
			if an[i] != bn[i] {
				return an[i] < bn[i]
			}
		}
	}
	return apre && !bpre
}

// localLatestReleaseTag picks the highest stable tag containing asset (if specified).
func localLatestReleaseTag(dir, asset string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, pre, ok := parseReleaseTag(name); !ok || pre {
			continue
		}
		if asset != "" {
			info, err := os.Stat(filepath.Join(dir, name, asset))
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				continue
			}
		}
		if best == "" || lessReleaseTag(best, name) {
			best = name
		}
	}
	return best
}

// isHTMLContent reports whether data looks like an HTML error page. Mirrors
// sometimes answer a missing release asset with a 200 HTML page, which must
// never be cached or served as a binary.
func isHTMLContent(data []byte) bool {
	return len(data) > 0 && strings.HasPrefix(http.DetectContentType(data), "text/html")
}

func cachedFileIsHTML(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return isHTMLContent(buf[:n])
}

// releaseCachePath locates the on-disk cache copy of a release asset.
// "" disables caching (no data dir, or an unresolvable "latest" tag —
// caching under the literal "latest" key would go stale on new releases).
func (s *Server) releaseCachePath(tag, asset string) string {
	if s.DataDir == "" || tag == "" || tag == "latest" || asset == "" {
		return ""
	}
	return filepath.Join(s.DataDir, "release-cache", tag, asset)
}

// HandleReleaseDownloadProxy streams a release asset through the RDev server as a last-resort fallback.
// Successful downloads are cached under DataDir/release-cache/<tag>/ so repeat
// installs are served from local disk instead of re-fetching upstream.
func (s *Server) HandleReleaseDownloadProxy(w http.ResponseWriter, r *http.Request) {
	asset, tag, ok := releaseDownloadParams(r)
	if !ok {
		http.Error(w, "bad asset", http.StatusBadRequest)
		return
	}
	if s.serveLocalReleaseAsset(w, r, asset, tag) {
		return
	}
	if tag == "latest" {
		var err error
		tag, err = s.latestReleaseAssetTag(r.Context(), asset)
		if err != nil {
			writeReleaseResolutionError(w, err)
			return
		}
	}
	cachePath := s.releaseCachePath(tag, asset)
	if cachePath != "" {
		if info, err := os.Stat(cachePath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			// A GitHub mirror can return a 200 HTML error page. Never serve
			// (or keep) such a response as if it were a release asset.
			if cachedFileIsHTML(cachePath) {
				_ = os.Remove(cachePath)
			} else if f, err := os.Open(cachePath); err == nil {
				defer f.Close()
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", "attachment; filename=\""+asset+"\"")
				http.ServeContent(w, r, asset, info.ModTime(), f)
				return
			}
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	client := &http.Client{Transport: transport}
	candidates := releaseDownloadProxyCandidates(asset, tag)
	selected := ""
	for _, candidate := range candidates {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, candidate, nil)
		if err == nil {
			req.Header.Set("User-Agent", "rdev-server")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					selected = candidate
				}
			}
		}
		cancel()
		if selected != "" {
			break
		}
	}
	if selected == "" {
		selected = releaseDownloadDirectURL(asset, tag)
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, selected, nil)
	if err != nil {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "rdev-server")
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	body := bufio.NewReader(resp.Body)
	peek, _ := body.Peek(512)
	if isHTMLContent(peek) || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		http.Error(w, "download failed: html response", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+asset+"\"")
	if length := resp.Header.Get("Content-Length"); length != "" {
		w.Header().Set("Content-Length", length)
	}
	/* Stream to the client while teeing into a temp file; the atomic rename
	 * publishes only complete downloads, so readers never see partials. */
	var dst io.Writer = w
	var tmp *os.File
	if cachePath != "" {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
			/* A killed process can orphan temp files; sweep the stale ones. */
			if orphans, _ := filepath.Glob(filepath.Join(filepath.Dir(cachePath), ".tmp-*")); len(orphans) > 0 {
				cutoff := time.Now().Add(-time.Hour)
				for _, p := range orphans {
					if info, err := os.Stat(p); err == nil && info.ModTime().Before(cutoff) {
						_ = os.Remove(p)
					}
				}
			}
			if f, err := os.CreateTemp(filepath.Dir(cachePath), ".tmp-*"); err == nil {
				tmp = f
				dst = io.MultiWriter(w, tmp)
			}
		}
	}
	_, copyErr := io.Copy(dst, body)
	if tmp != nil {
		name := tmp.Name()
		if copyErr != nil || tmp.Close() != nil || os.Rename(name, cachePath) != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}
}

func normalizeReleaseTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return tag
}

func fetchLatestReleaseTag(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/icepie/rdev/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "rdev-server")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", io.ErrUnexpectedEOF
	}
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data); err != nil {
		return "", err
	}
	tag := normalizeReleaseTag(data.TagName)
	if tag == "" {
		return "", io.ErrUnexpectedEOF
	}
	return tag, nil
}

func (s *Server) latestReleaseTag(ctx context.Context) (string, bool) {
	s.releaseLatestMu.Lock()
	if s.releaseLatestTag != "" && time.Since(s.releaseLatestAt) < releaseLatestTTL {
		tag := s.releaseLatestTag
		s.releaseLatestMu.Unlock()
		return tag, true
	}
	s.releaseLatestMu.Unlock()

	if s.ReleaseDir != "" {
		tag := localLatestReleaseTag(s.ReleaseDir, "")
		if tag == "" {
			fallback := normalizeReleaseTag(s.ReleaseVersion)
			if fallback == "" {
				fallback = "latest"
			}
			return fallback, false
		}
		s.releaseLatestMu.Lock()
		s.releaseLatestTag = tag
		s.releaseLatestAt = time.Now()
		s.releaseLatestMu.Unlock()
		return tag, true
	}

	tag, err := fetchLatestReleaseTag(ctx)
	if err != nil {
		fallback := normalizeReleaseTag(s.ReleaseVersion)
		if fallback == "" {
			fallback = "latest"
		}
		return fallback, false
	}

	s.releaseLatestMu.Lock()
	s.releaseLatestTag = tag
	s.releaseLatestAt = time.Now()
	s.releaseLatestMu.Unlock()
	return tag, true
}

var errReleaseAssetNotFound = errors.New("no published stable release contains the requested asset")

type releaseAssetTag struct {
	tag       string
	expiresAt time.Time
}

// GitHub's repository-wide latest release may contain only GPU or Android
// artifacts. Resolve the exact asset before constructing any download URL.
func fetchLatestReleaseAssetTag(ctx context.Context, client *http.Client, endpoint, asset string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?per_page=100&page="+strconv.Itoa(page), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "rdev-server")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("release metadata: HTTP %d", resp.StatusCode)
		}
		var releases []struct {
			TagName    string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
			Assets     []struct {
				Name string `json:"name"`
			} `json:"assets"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&releases)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		for _, release := range releases {
			if release.Draft || release.Prerelease {
				continue
			}
			if _, pre, ok := parseReleaseTag(release.TagName); !ok || pre {
				continue
			}
			for _, candidate := range release.Assets {
				if candidate.Name == asset {
					return release.TagName, nil
				}
			}
		}
		if len(releases) < 100 {
			return "", errReleaseAssetNotFound
		}
	}
}

func (s *Server) latestReleaseAssetTag(ctx context.Context, asset string) (string, error) {
	if s.ReleaseDir != "" {
		if tag := localLatestReleaseTag(s.ReleaseDir, asset); tag != "" {
			return tag, nil
		}
		return "", errReleaseAssetNotFound
	}
	s.releaseLatestMu.Lock()
	cached := s.releaseLatestAssets[asset]
	s.releaseLatestMu.Unlock()
	if time.Now().Before(cached.expiresAt) {
		return cached.tag, nil
	}
	tag, err := fetchLatestReleaseAssetTag(ctx, http.DefaultClient, "https://api.github.com/repos/icepie/rdev/releases", asset)
	if err != nil {
		return "", err
	}
	s.releaseLatestMu.Lock()
	if s.releaseLatestAssets == nil {
		s.releaseLatestAssets = make(map[string]releaseAssetTag)
	}
	s.releaseLatestAssets[asset] = releaseAssetTag{tag: tag, expiresAt: time.Now().Add(releaseLatestTTL)}
	s.releaseLatestMu.Unlock()
	return tag, nil
}

func writeReleaseResolutionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errReleaseAssetNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, "could not resolve release asset", http.StatusBadGateway)
}
