package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestReleaseAssetSkipsUnrelatedAndUnpublishedReleases(t *testing.T) {
	const asset = "rdev-client-cgo-windows-amd64.exe"
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.2.137","draft":true,"assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]},
			{"tag_name":"v0.2.137-rc1","prerelease":true,"assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]},
			{"tag_name":"v0.2.136","assets":[{"name":"rdev-client-gpu-windows-amd64.zip"}]},
			{"tag_name":"v0.2.135","assets":[{"name":"rdev-client-cgo-windows-arm64.exe"}]},
			{"tag_name":"v0.2.134","assets":[{"name":"rdev-client-cgo-windows-amd64.exe"}]}
		]`))
	}))
	defer metadata.Close()
	tag, err := fetchLatestReleaseAssetTag(context.Background(), metadata.Client(), metadata.URL, asset)
	if err != nil || tag != "v0.2.134" {
		t.Fatalf("resolved tag = %q, err = %v", tag, err)
	}
	_, err = fetchLatestReleaseAssetTag(context.Background(), metadata.Client(), metadata.URL, "missing.exe")
	if !errors.Is(err, errReleaseAssetNotFound) {
		t.Fatalf("missing asset error = %v", err)
	}
}

func TestLatestReleaseAssetPagination(t *testing.T) {
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			releases := make([]map[string]interface{}, 100)
			for i := range releases {
				releases[i] = map[string]interface{}{"tag_name": "v0.2.136", "assets": []interface{}{}}
			}
			_ = json.NewEncoder(w).Encode(releases)
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v0.2.134","assets":[{"name":"client.exe"}]}]`))
	}))
	defer metadata.Close()
	tag, err := fetchLatestReleaseAssetTag(context.Background(), metadata.Client(), metadata.URL, "client.exe")
	if err != nil || tag != "v0.2.134" {
		t.Fatalf("resolved tag = %q, err = %v", tag, err)
	}
}

func TestLatestReleaseAssetMetadataFailure(t *testing.T) {
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer metadata.Close()
	_, err := fetchLatestReleaseAssetTag(context.Background(), metadata.Client(), metadata.URL, "client.exe")
	if err == nil || errors.Is(err, errReleaseAssetNotFound) {
		t.Fatalf("upstream error must not be treated as an absent asset: %v", err)
	}
}

func TestReleaseAssetLatestLocalEndpoints(t *testing.T) {
	const asset = "rdev-client-cgo-windows-amd64.exe"
	s := NewServer()
	s.ReleaseDir = t.TempDir()
	for _, tag := range []string{"v0.2.134", "v0.2.136", "v0.2.137-rc1"} {
		if err := os.Mkdir(filepath.Join(s.ReleaseDir, tag), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tag := range []string{"v0.2.134", "v0.2.137-rc1"} {
		if err := os.WriteFile(filepath.Join(s.ReleaseDir, tag, asset), []byte("MZ-cgo-release"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, endpoint := range []string{"/download-release", "/download-release-proxy", "/api/release/latest"} {
		for _, query := range []string{"?asset=" + asset + "&tag=latest", "?asset=" + asset + "&tag=v0.2.136", "?asset=missing.exe&tag=latest", "?asset=../bad"} {
			r := httptest.NewRequest(http.MethodGet, endpoint+query, nil)
			w := httptest.NewRecorder()
			switch endpoint {
			case "/download-release":
				s.HandleReleaseDownload(w, r)
			case "/download-release-proxy":
				s.HandleReleaseDownloadProxy(w, r)
			default:
				s.HandleReleaseLatestAPI(w, r)
			}
			want := http.StatusOK
			if strings.Contains(query, "missing") || (endpoint != "/api/release/latest" && strings.Contains(query, "v0.2.136")) {
				want = http.StatusNotFound
			}
			if strings.Contains(query, "../") {
				want = http.StatusBadRequest
			}
			if w.Code != want {
				t.Fatalf("%s%s: status %d, want %d: %s", endpoint, query, w.Code, want, w.Body.String())
			}
			if want == http.StatusOK && endpoint == "/api/release/latest" && !strings.Contains(w.Body.String(), `"tag":"v0.2.134"`) {
				t.Fatalf("asset metadata = %s", w.Body.String())
			}
		}
	}
}

func TestReleaseAssetLatestPinsRedirectAndProxyCache(t *testing.T) {
	const asset, tag = "rdev-client-cgo-windows-amd64.exe", "v0.2.134"
	s := NewServer()
	s.DataDir = t.TempDir()
	s.releaseLatestAssets = map[string]releaseAssetTag{asset: {tag: tag, expiresAt: time.Now().Add(time.Minute)}}
	key := releaseDownloadChoiceKey(asset, tag)
	candidate := releaseDownloadDirectURL(asset, tag)
	cacheReleaseDownloadChoice(key, candidate, time.Now())
	t.Cleanup(func() {
		releaseDownloadChoiceMu.Lock()
		delete(releaseDownloadChoices, key)
		releaseDownloadChoiceMu.Unlock()
	})
	w := httptest.NewRecorder()
	s.HandleReleaseDownload(w, httptest.NewRequest(http.MethodGet, "/download-release?asset="+asset+"&tag=latest", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != candidate {
		t.Fatalf("redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	cache := s.releaseCachePath(tag, asset)
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte("MZ-cached-cgo"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.HandleReleaseDownloadProxy(w, httptest.NewRequest(http.MethodGet, "/download-release-proxy?asset="+asset+"&tag=latest", nil))
	if w.Code != http.StatusOK || w.Body.String() != "MZ-cached-cgo" {
		t.Fatalf("proxy = %d %q", w.Code, w.Body.String())
	}
}
