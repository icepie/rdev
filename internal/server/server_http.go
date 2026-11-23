package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"rdev/internal/protocol"
)

// HandleAPI returns the list of connected clients as JSON
func (s *Server) HandleAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type clientInfo struct {
		ID             string                        `json:"id"`
		DisplayName    string                        `json:"displayName"`
		RequestedID    string                        `json:"requestedId,omitempty"`
		InstanceID     string                        `json:"instanceId,omitempty"`
		Version        string                        `json:"version,omitempty"`
		OS             string                        `json:"os,omitempty"`
		ConnectedAt    string                        `json:"connectedAt"`
		Sessions       int                           `json:"sessions"`
		Forwards       int                           `json:"forwards"`
		HasPassword    bool                          `json:"hasPassword"`
		Password       string                        `json:"password,omitempty"`
		FileOperations []string                      `json:"fileOperations,omitempty"`
		Desktop        *protocol.DesktopCapabilities `json:"desktop,omitempty"`
		GPUDesktop     bool                          `json:"gpuDesktop,omitempty"`
	}

	/* ?passwords=1 includes each device's access password: the endpoint is
	 * admin-token protected, and the dashboard's copy-connection action uses
	 * it to substitute real credentials for the <password> placeholders. */
	includePasswords := r.URL.Query().Get("passwords") == "1"

	clients := make([]clientInfo, 0, len(s.clients))
	for _, c := range s.clients {
		c.mu.Lock()
		n := len(c.Sessions)
		f := len(c.Forwards)
		c.mu.Unlock()
		info := clientInfo{
			ID:             c.ID,
			DisplayName:    clientDisplayName(c),
			RequestedID:    c.RequestedID,
			InstanceID:     c.InstanceID,
			Version:        c.Version,
			OS:             clientOS(c),
			ConnectedAt:    c.ConnectedAt.Format(time.RFC3339),
			Sessions:       n,
			Forwards:       f,
			HasPassword:    c.Password != "",
			Desktop:        cloneDesktopCapabilities(c.Desktop),
			FileOperations: c.FileOperations,
			GPUDesktop:     s.clientGPUDesktopAvailable(c),
		}
		if includePasswords {
			info.Password = c.Password
		}
		clients = append(clients, info)
	}

	sort.Slice(clients, func(i, j int) bool {
		if clients[i].ID != clients[j].ID {
			return clients[i].ID < clients[j].ID
		}
		return clients[i].ConnectedAt < clients[j].ConnectedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(clients)
}

// HandleConfigAPI returns server configuration for the web UI
func (s *Server) HandleConfigAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sshPort":            s.SSHPort,
		"httpHost":           s.HTTPHost,
		"tcpPort":            s.TCPPort,
		"kcpPort":            s.KCPPort,
		"vncAddr":            s.VNCAddr,
		"proxyAddr":          s.ProxyAddr,
		"authRequired":       map[bool]string{true: "true", false: "false"}[s.AdminToken != ""],
		"deviceAuthBypassed": map[bool]string{true: "true", false: "false"}[s.deviceAuthBypassed(r)],
		"browserEnabled":     map[bool]string{true: "true", false: "false"}[s.BrowserEnabled],
		"agentEnabled":       map[bool]string{true: "true", false: "false"}[s.Agent != nil && s.Agent.Enabled()],
		"agentModel":         agentDefaultModel(s.Agent),
		"agentProviders":     agentProviderIDs(s.Agent),
	})
}

// HandleReleaseLatestAPI resolves an optional exact asset to a published release.
func (s *Server) HandleReleaseLatestAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("asset") {
		asset, _, ok := releaseDownloadParams(r)
		if !ok {
			http.Error(w, "bad asset", http.StatusBadRequest)
			return
		}
		tag, err := s.latestReleaseAssetTag(r.Context(), asset)
		if err != nil {
			writeReleaseResolutionError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"tag": tag, "asset": asset, "fresh": true})
		return
	}
	tag, fresh := s.latestReleaseTag(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tag":   tag,
		"fresh": fresh,
	})
}

// HandleTerminalAPI returns available devices for the terminal page
func (s *Server) HandleTerminalAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type deviceInfo struct {
		ID             string                        `json:"id"`
		DisplayName    string                        `json:"displayName"`
		RequestedID    string                        `json:"requestedId,omitempty"`
		ConnectedAt    string                        `json:"connectedAt"`
		Version        string                        `json:"version,omitempty"`
		OS             string                        `json:"os,omitempty"`
		HasPassword    bool                          `json:"hasPassword"`
		FileOperations []string                      `json:"fileOperations,omitempty"`
		Desktop        *protocol.DesktopCapabilities `json:"desktop,omitempty"`
		GPUDesktop     bool                          `json:"gpuDesktop,omitempty"`
	}

	devices := make([]deviceInfo, 0, len(s.clients))
	for _, c := range s.clients {
		devices = append(devices, deviceInfo{
			ID:             c.ID,
			DisplayName:    clientDisplayName(c),
			RequestedID:    c.RequestedID,
			ConnectedAt:    c.ConnectedAt.Format(time.RFC3339),
			Version:        c.Version,
			OS:             clientOS(c),
			HasPassword:    c.Password != "",
			Desktop:        cloneDesktopCapabilities(c.Desktop),
			FileOperations: c.FileOperations,
			GPUDesktop:     s.clientGPUDesktopAvailable(c),
		})
	}

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].ID != devices[j].ID {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].ConnectedAt < devices[j].ConnectedAt
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(devices)
}

// StaticHandler returns an http.Handler for the embedded web UI
func (s *Server) StaticHandler() http.Handler {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/run.sh" || r.URL.Path == "/run.ps1" {
			s.serveRunner(w, r, sub)
			return
		}
		serveStatic(w, r, sub, strings.TrimPrefix(r.URL.Path, "/"))
	})
}

// StaticPageHandler serves a named embedded UI page without exposing the .html suffix.
func (s *Server) StaticPageHandler(name string) http.HandlerFunc {
	sub, err := fs.Sub(templateFS, "static")
	if err != nil {
		return http.NotFound
	}
	return func(w http.ResponseWriter, r *http.Request) {
		serveStatic(w, r, sub, name)
	}
}
