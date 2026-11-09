package server

import (
	"bytes"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"text/template"
)

// Runner defaults come from the download origin and enabled listeners. Explicit
// command-line server arguments still override the rendered default.
func (s *Server) serveRunner(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	origin, err := url.Parse("http://" + r.Host)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		http.Error(w, "invalid host", http.StatusBadRequest)
		return
	}
	scheme := "ws://"
	if r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") {
		scheme = "wss://"
	}
	endpoints := make([]string, 0, 3)
	if s.TCPPort != "" {
		endpoints = append(endpoints, "tcp://"+net.JoinHostPort(origin.Hostname(), s.TCPPort))
	}
	if s.KCPPort != "" {
		endpoints = append(endpoints, "kcp://"+net.JoinHostPort(origin.Hostname(), s.KCPPort))
	}
	endpoints = append(endpoints, scheme+r.Host)
	server := strings.Join(endpoints, ",")
	// Each template places Server inside a single-quoted string in its language.
	if r.URL.Path == "/run.ps1" {
		server = strings.ReplaceAll(server, "'", "''")
	} else {
		server = strings.ReplaceAll(server, "'", "'\"'\"'")
	}
	source, err := fs.ReadFile(assets, strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		http.Error(w, "runner unavailable", http.StatusInternalServerError)
		return
	}
	tmpl, err := template.New("runner").Option("missingkey=error").Parse(string(source))
	var output bytes.Buffer
	if err == nil {
		err = tmpl.Execute(&output, struct{ Server string }{server})
	}
	if err != nil {
		http.Error(w, "runner template failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		_, _ = w.Write(output.Bytes())
	}
}
