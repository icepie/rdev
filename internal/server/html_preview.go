package server

import (
	"context"
	"io"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
)

// Only web assets are exposed by a directory preview, never directory listings
// or arbitrary files. The device additionally enforces the root with OpenInRoot.
func previewResourceMIME(name string) string {
	switch strings.ToLower(pathpkg.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	}
	return videoMIME(name)
}

func validPreviewResource(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || pathpkg.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return previewResourceMIME(name) != ""
}

func (s *Server) servePreviewResource(w http.ResponseWriter, r *http.Request, ctx context.Context, client *ClientConn, grant mediaGrant, key, resource string) {
	if strings.HasSuffix(resource, "/") || resource == "" {
		resource += "index.html"
	}
	if !validPreviewResource(resource) {
		http.NotFound(w, r)
		return
	}
	root := pathpkg.Dir(strings.ReplaceAll(grant.path, "\\", "/"))
	_, meta, err := s.readMediaRangeInRoot(ctx, client, root, resource, 0, 1)
	if err != nil {
		http.Error(w, "cannot read preview resource: "+err.Error(), http.StatusBadGateway)
		return
	}
	reader := &mediaReader{srv: s, client: client, ctx: ctx, root: root, path: resource, size: meta.Size, modTime: meta.ModTime}
	// Scheme-less host sources inherit the page's scheme, including deployments
	// behind TLS termination. No forwarded headers or authored base element needed.
	if strings.ContainsAny(r.Host, "\"'; /\\\t\r\n") {
		http.Error(w, "invalid preview host", 400)
		return
	}
	scope := r.Host + "/api/media/" + key + "/"
	policy := "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' " + scope + "; style-src 'unsafe-inline' " + scope + "; img-src data: blob: " + scope + "; media-src data: blob: " + scope + "; font-src data: " + scope + "; connect-src " + scope + "; form-action 'none'; base-uri 'none'"
	w.Header().Set("Content-Security-Policy", policy)
	// Modules, fonts and fetch() originate from the sandbox's opaque origin.
	// This bearer endpoint never grants credentialed CORS or access to host APIs.
	if r.Header.Get("Origin") == "null" {
		w.Header().Set("Access-Control-Allow-Origin", "null")
		w.Header().Set("Vary", "Origin")
	}
	mime := previewResourceMIME(resource)
	if isHTMLPreviewPath(resource) {
		if meta.Size > agentFileReadCap {
			http.Error(w, "HTML preview exceeds size limit", 502)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
			return
		}
		content, err := io.ReadAll(io.LimitReader(reader, agentFileReadCap+1))
		if err != nil || int64(len(content)) != meta.Size {
			http.Error(w, "HTML preview changed or could not be read", 502)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(content)
		}
		return
	}
	w.Header().Set("Content-Type", mime)
	modTime, _ := time.Parse(time.RFC3339, meta.ModTime)
	http.ServeContent(w, r, pathpkg.Base(resource), modTime, reader)
}
