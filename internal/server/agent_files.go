package server

import (
	"fmt"
	"sort"
	"strings"
)

// handleGetChanges replies with the current thread's git working-tree
// status. The probe round-trips the device, so it runs off the socket
// goroutine; failures come back in the reply's error field.
func (h *agentWSHandler) handleGetChanges(ac *agentConn, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	if th == nil {
		srv.agentSessionsMu.Unlock()
		ac.sendError("agent session not ready")
		return
	}
	cwd := agentEffectiveCwdLocked(entry, th)
	branch := th.branch
	threadID := th.id
	srv.agentSessionsMu.Unlock()

	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		reply := agentChangesMsg{Op: "changes", ID: requestID, Thread: threadID, Cwd: cwd, Branch: branch, Files: []agentChangeFile{}}
		if cwd == "" {
			reply.Error = "no working directory"
		} else if files, err := srv.probeGitChanges(entry.deviceID, cwd); err != nil {
			reply.Error = err.Error()
		} else {
			reply.Files = files
		}
		ac.writeChanges(reply)
	})
}

// handleGetDiff replies with the unified diff of one path in the current
// thread's working tree; untracked files come back with error "untracked".
func (h *agentWSHandler) handleGetDiff(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		ac.writeDiff(agentDiffMsg{Op: "diff", ID: requestID, Error: "missing path"})
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		reply := agentDiffMsg{Op: "diff", ID: requestID, Thread: threadID, Path: path}
		if cwd == "" {
			reply.Error = "no working directory"
		} else if diff, err := srv.probeGitDiff(entry.deviceID, cwd, path); err != nil {
			reply.Error = err.Error()
		} else {
			reply.Diff = diff
		}
		ac.writeDiff(reply)
	})
}

// handleFileRead answers one browser's file_read op with the file's content
// from the current thread's working directory (op "file_content", reply to
// the requesting socket only). Powers the right-panel file preview tabs.
func (h *agentWSHandler) handleFileRead(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "file_content", ID: requestID, Thread: threadID, Path: path}
	if path == "" {
		reply.Message = "missing path"
		ac.writeJSON(reply)
		return
	}
	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		switch {
		case cwd == "":
			reply.Message = "no working directory"
		case streamPreviewMIME(path) != "":
			client, ok := srv.GetClient(entry.deviceID)
			if !ok {
				reply.Message = "device offline"
				break
			}
			url, err := srv.grantFilePreview(client, resolveDevicePath(clientOS(client), cwd, path))
			if err != nil {
				reply.Message = err.Error()
			} else {
				reply.Content, reply.Encoding, reply.Mime = url, "url", streamPreviewMIME(path)
			}
		case imageMIME(path) != "":
			/* Image preview: base64-encoded body + mime; the browser renders
			 * an <img> instead of the text view. */
			b64, err := srv.probeReadImage(entry.deviceID, cwd, path)
			if err != nil {
				reply.Message = err.Error()
			} else {
				reply.Content = b64
				reply.Encoding = "base64"
				reply.Mime = imageMIME(path)
			}
		default:
			content, truncated, err := srv.probeReadFile(entry.deviceID, cwd, path)
			switch {
			case err != nil:
				reply.Message = err.Error()
			default:
				reply.Content = content
				reply.Truncated = truncated
				if isHTMLPreviewPath(path) && !truncated {
					if client, ok := srv.GetClient(entry.deviceID); ok {
						reply.PreviewURL, _ = srv.grantFilePreview(client, resolveDevicePath(clientOS(client), cwd, path))
					}
				}
			}
		}
		ac.writeJSON(reply)
	})
}

// imageMIME maps a path's extension to a browser-renderable image mime, or
// "" for non-images (kept conservative: only formats <img> can decode).
func imageMIME(path string) string {
	ext := ""
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		ext = strings.ToLower(path[i+1:])
	}
	switch ext {
	case "png", "apng":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "avif":
		return "image/avif"
	}
	return ""
}

// Image preview caps: 4 MiB of raw bytes ≈ 5.6 MB of base64.
const (
	agentImageReadCap = 4 << 20
	agentImageB64Cap  = (agentImageReadCap+2)/3*4 + 8
)

// probeReadImage base64-encodes an image file on the device (encode happens
// device-side so the wire frame stays printable). Runs off-lock.
func (s *Server) probeReadImage(deviceID, cwd, path string) (string, error) {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, imageReadProbeCommand(clientOS(client), cwd, path), agentFileReadTimeout, agentImageB64Cap+1024)
	if err != nil {
		return "", err
	}
	if code > 0 {
		return "", fmt.Errorf("cannot read %s", path)
	}
	/* GNU base64 wraps at 76 columns; strip the wraps server-side (a tr pipe
	 * device-side would mask base64's exit code behind tr's). */
	out = strings.ReplaceAll(out, "\n", "")
	if len(out) > agentImageB64Cap {
		return "", fmt.Errorf("image too large (over %d MiB)", agentImageReadCap>>20)
	}
	return out, nil
}

// imageReadProbeCommand builds the base64 read probe for the device OS;
// Windows uses .NET's Convert. Line wraps are stripped by the caller.
func imageReadProbeCommand(goos, cwd, path string) string {
	if goos == "windows" {
		q := func(s string) string { return strings.ReplaceAll(s, `'`, `''`) }
		return fmt.Sprintf(`powershell -NoProfile -Command "Set-Location -LiteralPath '%s'; [Console]::Write([Convert]::ToBase64String([IO.File]::ReadAllBytes('%s')))"`, q(cwd), q(path))
	}
	return "cd " + shellQuote(cwd) + " && base64 < " + shellQuote(path)
}

// agentFileSearchCap caps the raw `git ls-files` probe output (~17k paths);
// agentFileSearchLimit caps the filtered reply list.
const (
	agentFileSearchCap   = 512 * 1024
	agentFileSearchLimit = 50
)

// handleSearchFiles answers one browser's search_files op with the repo
// paths matching the query (op "file_search", reply to the requesting
// socket only). Powers the right-panel new-tab file search (bb's
// NewTabFileSearch). Git repos only; every query word must appear in the
// path (case-insensitive), basename hits rank first.
func (h *agentWSHandler) handleSearchFiles(ac *agentConn, q, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	q = strings.TrimSpace(q)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "file_search", ID: requestID, Thread: threadID, Q: q, Files: []string{}}
	if q == "" {
		ac.writeJSON(reply)
		return
	}
	h.srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		if cwd == "" {
			reply.Message = "no working directory"
			ac.writeJSON(reply)
			return
		}
		client, ok := srv.GetClient(entry.deviceID)
		if !ok {
			reply.Message = "device offline"
			ac.writeJSON(reply)
			return
		}
		out, code, err := srv.runDeviceProbe(client, gitLsFilesAllProbeCommand(clientOS(client), cwd), agentGitProbeTimeout, agentFileSearchCap)
		switch {
		case err != nil:
			reply.Message = err.Error()
		case code > 0:
			reply.Message = "not a git repository"
		default:
			paths := strings.FieldsFunc(out, func(r rune) bool { return r == '\n' })
			files, dropped := filterFileSearch(paths, q, agentFileSearchLimit)
			reply.Files = files
			reply.Truncated = dropped
		}
		ac.writeJSON(reply)
	})
}

// filterFileSearch AND-matches the lowercase query words against each path
// (case-insensitive). Results sort basename-word hits first, then shorter
// paths. The bool reports whether the limit dropped matches.
func filterFileSearch(paths []string, q string, limit int) ([]string, bool) {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return []string{}, false
	}
	type hit struct {
		path      string
		baseMatch bool
	}
	hits := []hit{}
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, exists := seen[p]; exists {
			continue
		}
		seen[p] = struct{}{}
		lower := strings.ToLower(p)
		ok := true
		for _, w := range words {
			if !strings.Contains(lower, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		base := lower
		if i := strings.LastIndex(lower, "/"); i >= 0 {
			base = lower[i+1:]
		}
		baseMatch := false
		for _, w := range words {
			if strings.Contains(base, w) {
				baseMatch = true
				break
			}
		}
		hits = append(hits, hit{path: p, baseMatch: baseMatch})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].baseMatch != hits[j].baseMatch {
			return hits[i].baseMatch
		}
		if len(hits[i].path) != len(hits[j].path) {
			return len(hits[i].path) < len(hits[j].path)
		}
		li, lj := strings.ToLower(hits[i].path), strings.ToLower(hits[j].path)
		if li != lj {
			return li < lj
		}
		return hits[i].path < hits[j].path
	})
	dropped := false
	if len(hits) > limit {
		hits = hits[:limit]
		dropped = true
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.path)
	}
	return out, dropped
}

// handleDirList answers one browser's dir_list op with the child entries of
// a directory on the device (op "dir_entries", reply to the requesting
// socket only). An empty path resolves to the current thread's effective
// working directory. Powers the create-project path browser (bb's
// RemotePathBrowser).
func (h *agentWSHandler) handleDirList(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "dir_entries", ID: requestID, Thread: threadID, Path: path}
	h.srv.goAgent(func() {
		if path == "" {
			if cwd == "" && th != nil {
				cwd = srv.awaitAgentDefaultCwd(entry)
			}
			path = cwd
			reply.Path = path
		}
		if path == "" {
			reply.Message = "no working directory"
			ac.writeJSON(reply)
			return
		}
		parent, entries, roots, err := srv.probeListDir(entry.deviceID, path)
		if err != nil {
			reply.Message = err.Error()
		} else {
			reply.Parent = parent
			reply.Entries = entries
			reply.Roots = roots
		}
		ac.writeJSON(reply)
	})
}

// handleDirMkdir answers one browser's dir_mkdir op, creating the directory
// (and parents) on the device (op "dir_mkdir", reply to the requesting
// socket only). Powers the path browser's "New folder" action.
func (h *agentWSHandler) handleDirMkdir(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	threadID := ""
	h.srv.agentSessionsMu.Lock()
	if th := entry.threads[ac.current]; th != nil {
		threadID = th.id
	}
	h.srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "dir_mkdir", ID: requestID, Thread: threadID, Path: path}
	if path == "" {
		reply.Message = "missing path"
		ac.writeJSON(reply)
		return
	}
	h.srv.goAgent(func() {
		if err := h.srv.probeMkdir(entry.deviceID, path); err != nil {
			reply.Message = err.Error()
		}
		ac.writeJSON(reply)
	})
}
