package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

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
