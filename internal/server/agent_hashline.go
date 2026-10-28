package server

// Hashline anchors (ported from omp's pi-edit hashline mode): read_file can
// prefix every line with a stable `N#hash|` anchor; edit then accepts
// line-based patches referencing those anchors. Anchors are verified against
// the file's current bytes before applying — a stale anchor rejects the
// patch before it corrupts anything and reports the fresh anchors so the
// model can retry without re-reading the whole file.

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// anchorHash returns the short content hash of one line (trailing \r
// stripped so CRLF files share anchors with LF reads).
func anchorHash(line string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSuffix(line, "\r")))
	return h.Sum32()
}

// formatAnchor renders the `N#hash` anchor token for 1-based line n.
func formatAnchor(n int, line string) string {
	return fmt.Sprintf("%d#%08x", n, anchorHash(line))
}

// parseAnchor parses an `N#hash` token into its line number and hash.
func parseAnchor(tok string) (n int, hash uint32, err error) {
	i := strings.IndexByte(tok, '#')
	if i <= 0 {
		return 0, 0, fmt.Errorf("malformed anchor %q (want N#hash)", tok)
	}
	n, err = strconv.Atoi(tok[:i])
	if err != nil || n < 1 {
		return 0, 0, fmt.Errorf("malformed anchor %q (bad line number)", tok)
	}
	v, perr := strconv.ParseUint(strings.TrimPrefix(tok[i+1:], "0x"), 16, 32)
	if perr != nil {
		return 0, 0, fmt.Errorf("malformed anchor %q (bad hash)", tok)
	}
	return n, uint32(v), nil
}

// splitLines splits s into lines without keeping terminators; a trailing
// newline does not produce a phantom empty final line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// verifyAnchor checks that lines[n-1] exists and hashes to want.
func verifyAnchor(lines []string, n int, want uint32) error {
	if n < 1 || n > len(lines) {
		return fmt.Errorf("line %d out of range (file has %d lines)", n, len(lines))
	}
	if got := anchorHash(lines[n-1]); got != want {
		return fmt.Errorf("stale anchor at line %d: expected #%08x, current is #%08x — re-read the file (read_file anchors:true) and retry", n, want, got)
	}
	return nil
}

// lineEditArg is the wire shape of one hashline patch in the edit tool's
// line_edits argument.
type lineEditArg struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	NewString string `json:"new_string"`
}

// applyHashlineEdits parses, verifies and applies hashline patches to
// content, returning the new content and the number of patches applied.
// A trailing newline is preserved when the original had one.
func applyHashlineEdits(content string, args []lineEditArg) (string, int, error) {
	if len(args) == 0 {
		return "", 0, fmt.Errorf("line_edits must not be empty")
	}
	edits := make([]lineEdit, len(args))
	for i, a := range args {
		sn, sh, err := parseAnchor(a.Start)
		if err != nil {
			return "", 0, err
		}
		en, eh := sn, sh
		if a.End != "" && a.End != a.Start {
			en, eh, err = parseAnchor(a.End)
			if err != nil {
				return "", 0, err
			}
		}
		edits[i] = lineEdit{Start: sn, End: en, StartHash: sh, EndHash: eh, New: splitLines(a.NewString)}
	}
	lines, err := applyLineEdits(splitLines(content), edits)
	if err != nil {
		return "", 0, err
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(content, "\n") || content == "" {
		out += "\n"
	}
	return out, len(edits), nil
}

// lineEdit is one hashline patch: replace lines start..end (inclusive,
// 1-based) with newString. end==start replaces a single line; content may
// span multiple lines or be empty (deletion).
type lineEdit struct {
	Start     int
	End       int
	StartHash uint32
	EndHash   uint32
	New       []string
}

// applyLineEdits verifies all anchors against lines, then applies the edits
// bottom-up so earlier line numbers stay valid. Returns the new lines.
func applyLineEdits(lines []string, edits []lineEdit) ([]string, error) {
	for _, e := range edits {
		if e.End < e.Start {
			return nil, fmt.Errorf("line edit %d..%d: end before start", e.Start, e.End)
		}
		if err := verifyAnchor(lines, e.Start, e.StartHash); err != nil {
			return nil, err
		}
		if err := verifyAnchor(lines, e.End, e.EndHash); err != nil {
			return nil, err
		}
	}
	// Overlap check (same start line or crossing ranges).
	for i := range edits {
		for j := i + 1; j < len(edits); j++ {
			a, b := edits[i], edits[j]
			if a.Start <= b.End && b.Start <= a.End {
				return nil, fmt.Errorf("overlapping line edits %d..%d and %d..%d", a.Start, a.End, b.Start, b.End)
			}
		}
	}
	// Bottom-up: highest start first.
	order := make([]int, len(edits))
	for i := range order {
		order[i] = i
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if edits[order[j]].Start > edits[order[i]].Start {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	out := lines
	for _, idx := range order {
		e := edits[idx]
		next := make([]string, 0, len(out)-(e.End-e.Start+1)+len(e.New))
		next = append(next, out[:e.Start-1]...)
		next = append(next, e.New...)
		next = append(next, out[e.End:]...)
		out = next
	}
	return out, nil
}
