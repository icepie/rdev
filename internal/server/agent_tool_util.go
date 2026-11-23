package server

import (
	"encoding/json"
	"strings"
)

// resolveDevicePath joins a relative tool path onto the thread's working
// directory using the device OS's path separator. Absolute paths and an
// empty cwd pass through unchanged.
func resolveDevicePath(goos, cwd, path string) string {
	if cwd == "" || path == "" {
		return path
	}
	if goos == "windows" {
		// Drive-absolute (C:\x, C:/x), rooted (\x, /x) and UNC (\\srv\x).
		if strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "/") {
			return path
		}
		if len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') &&
			(path[0] >= 'a' && path[0] <= 'z' || path[0] >= 'A' && path[0] <= 'Z') {
			return path
		}
		if strings.HasSuffix(cwd, `\`) || strings.HasSuffix(cwd, "/") {
			return cwd + path
		}
		return cwd + `\` + path
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	if strings.HasSuffix(cwd, "/") {
		return cwd + path
	}
	return cwd + "/" + path
}

// --- shared argument helpers ---

// toolPathArg extracts the common "path" argument for one-line summaries.
func toolPathArg(raw json.RawMessage) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.Path
}

// toolPatternArg extracts the common "pattern" argument for one-line summaries.
func toolPatternArg(raw json.RawMessage) string {
	var args struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return ""
	}
	return args.Pattern
}

// truncateRunes cuts s to at most max runes, appending an ellipsis when cut.
func truncateRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// shellQuote wraps s in POSIX single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
