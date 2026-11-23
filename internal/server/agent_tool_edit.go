package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// --- edit ---

// editTool replaces exact text in a device file (read → validate → replace →
// write back) and keeps the unified diff of the last successful change.
type editTool struct {
	reader *readFileTool
	writer *writeFileTool

	mu       sync.Mutex
	lastDiff string
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return "Edit a file on the remote device. Two modes: (1) old_string/new_string — replace exact text (a single occurrence unless replace_all is set); (2) line_edits — hashline patches referencing N#hash anchors from read_file anchors=true; each patch replaces lines start..end with new_string, and stale anchors are rejected before anything is written. Prefer this over write_file when modifying an existing file."
}

func (t *editTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path":        {"type": "string", "description": "File path on the device"},
			"old_string":  {"type": "string", "description": "Exact text to replace (mode 1)"},
			"new_string":  {"type": "string", "description": "Replacement text (mode 1)"},
			"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match (mode 1)"},
			"line_edits":  {"type": "array", "description": "Hashline patches (mode 2), anchors from read_file anchors=true", "items": {"type": "object", "properties": {"start": {"type": "string", "description": "Anchor of first line to replace, N#hash"}, "end": {"type": "string", "description": "Anchor of last line to replace (default: same as start)"}, "new_string": {"type": "string", "description": "Replacement lines; empty deletes the range"}}, "required": ["start", "new_string"]}}
		},
		"required": ["path"]
	}`)
}

// Dangerous marks edit as requiring user approval.
func (t *editTool) Dangerous() bool { return true }

func (t *editTool) Summary(raw json.RawMessage) string {
	return toolPathArg(raw)
}

// LastDiff returns the unified diff of the last successful edit ("" when the
// last call failed or changed nothing).
func (t *editTool) LastDiff() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastDiff
}

func (t *editTool) setLastDiff(diff string) {
	t.mu.Lock()
	t.lastDiff = diff
	t.mu.Unlock()
}

func (t *editTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path       string        `json:"path"`
		OldString  string        `json:"old_string"`
		NewString  string        `json:"new_string"`
		ReplaceAll bool          `json:"replace_all"`
		LineEdits  []lineEditArg `json:"line_edits"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(args.LineEdits) > 0 && args.OldString != "" {
		return "", fmt.Errorf("pass either old_string or line_edits, not both")
	}
	if len(args.LineEdits) == 0 && args.OldString == "" {
		return "", fmt.Errorf("old_string or line_edits is required")
	}
	args.Path = t.reader.resolve(args.Path)
	release, err := t.reader.srv.agentFileMutations.Acquire(ctx, t.reader.client.ID, clientOS(t.reader.client), args.Path)
	if err != nil {
		return "", err
	}
	defer release()

	data, err := t.reader.download(ctx, args.Path, readFileHardCap)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(sampleBytes(data), 0) >= 0 {
		return "", fmt.Errorf("edit %q: binary files are not supported", args.Path)
	}

	oldContent := string(data)
	var newContent string
	var n int
	if len(args.LineEdits) > 0 {
		newContent, n, err = applyHashlineEdits(oldContent, args.LineEdits)
	} else {
		newContent, n, err = applyEdit(oldContent, args.OldString, args.NewString, args.ReplaceAll)
	}
	if err != nil {
		return "", fmt.Errorf("edit %q: %v", args.Path, err)
	}
	if newContent == oldContent {
		t.setLastDiff("")
		return fmt.Sprintf("%s unchanged (replacement is identical)", args.Path), nil
	}

	diff := unifiedDiff(oldContent, newContent, args.Path)
	if _, err := t.writer.upload(ctx, args.Path, []byte(newContent), 0); err != nil {
		return "", err
	}
	t.setLastDiff(diff)

	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("edited %s (%d replacement%s)", args.Path, n, plural), nil
}

// applyEdit validates and performs the textual replacement: old_string must
// occur at least once, and exactly once unless replaceAll is set.
func applyEdit(content, oldString, newString string, replaceAll bool) (string, int, error) {
	if oldString == "" {
		return "", 0, fmt.Errorf("old_string must not be empty")
	}
	count := strings.Count(content, oldString)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in file")
	}
	if count > 1 && !replaceAll {
		return "", 0, fmt.Errorf("old_string matches %d times; add more context or set replace_all", count)
	}
	n := 1
	if replaceAll {
		n = count
	}
	return strings.Replace(content, oldString, newString, n), n, nil
}

// --- unified diff (self-contained line LCS) ---

// diffOpcode is one matching/edit block between two line lists, in the style
// of Python difflib: tag is 'e' (equal), 'd' (delete), 'i' (insert) or 'r'
// (replace); the block covers a[i1:i2] and b[j1:j2].
type diffOpcode struct {
	tag            byte
	i1, i2, j1, j2 int
}

// diffMaxCells caps the LCS dynamic-programming table; larger inputs fall
// back to a single replace block (still a correct diff, just coarser).
const diffMaxCells = 4 << 20

// diffOpcodes computes the edit script between line lists a and b via LCS
// dynamic programming.
func diffOpcodes(a, b []string) []diffOpcode {
	if (len(a)+1)*(len(b)+1) > diffMaxCells {
		switch {
		case len(a) == 0 && len(b) == 0:
			return nil
		case len(a) == 0:
			return []diffOpcode{{tag: 'i', j2: len(b)}}
		case len(b) == 0:
			return []diffOpcode{{tag: 'd', i2: len(a)}}
		default:
			return []diffOpcode{{tag: 'r', i2: len(a), j2: len(b)}}
		}
	}

	// dp[i][j] = LCS length of a[i:] and b[j:], filled bottom-up.
	w := len(b) + 1
	dp := make([]uint32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			} else {
				dp[i*w+j] = max(dp[(i+1)*w+j], dp[i*w+j+1])
			}
		}
	}

	var ops []diffOpcode
	push := func(tag byte, i1, i2, j1, j2 int) {
		if i1 == i2 && j1 == j2 {
			return
		}
		if n := len(ops); n > 0 && ops[n-1].tag == tag {
			ops[n-1].i2 = i2
			ops[n-1].j2 = j2
			return
		}
		ops = append(ops, diffOpcode{tag: tag, i1: i1, i2: i2, j1: j1, j2: j2})
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			push('e', i, i+1, j, j+1)
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			push('d', i, i+1, j, j)
			i++
		default:
			push('i', i, i, j, j+1)
			j++
		}
	}
	for i < len(a) {
		push('d', i, i+1, j, j)
		i++
	}
	for j < len(b) {
		push('i', i, i, j, j+1)
		j++
	}
	return ops
}

// diffContextLines is the number of unchanged lines shown around each hunk.
const diffContextLines = 3

// groupDiffOpcodes splits an opcode list into hunks: each hunk contains
// change blocks plus up to n lines of surrounding context, merging changes
// separated by at most 2n equal lines.
func groupDiffOpcodes(ops []diffOpcode, n int) [][]diffOpcode {
	var groups [][]diffOpcode
	var group []diffOpcode
	for _, op := range ops {
		if op.tag != 'e' {
			group = append(group, op)
			continue
		}
		size := op.i2 - op.i1
		if len(group) == 0 {
			// Leading context: keep only the last n lines.
			if size > n {
				op.i1 = op.i2 - n
				op.j1 = op.j2 - n
			}
			group = append(group, op)
			continue
		}
		if size > 2*n {
			// Gap too large: close this hunk with n trailing lines and open
			// a new one with n leading lines.
			group = append(group, diffOpcode{tag: 'e', i1: op.i1, i2: op.i1 + n, j1: op.j1, j2: op.j1 + n})
			groups = append(groups, group)
			group = []diffOpcode{{tag: 'e', i1: op.i2 - n, i2: op.i2, j1: op.j2 - n, j2: op.j2}}
			continue
		}
		group = append(group, op)
	}
	if len(group) > 0 {
		for _, op := range group {
			if op.tag != 'e' {
				groups = append(groups, group)
				break
			}
		}
	}
	return groups
}

// splitDiffLines splits text into lines keeping the newline terminators (the
// last line may lack one). Empty text yields no lines.
func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	// SplitAfter leaves a phantom empty element after a trailing newline.
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// writeDiffLine writes one diff body line (" "/"-"/"+" prefix), adding the
// missing-newline marker when the source line lacks its terminator.
func writeDiffLine(b *strings.Builder, prefix byte, line string) {
	b.WriteByte(prefix)
	if strings.HasSuffix(line, "\n") {
		b.WriteString(line)
		return
	}
	b.WriteString(line)
	b.WriteString("\n\\ No newline at end of file\n")
}

// unifiedDiff renders oldText→newText as a unified diff with ---/+++ file
// headers and @@ hunks with 3 lines of context. "" when identical.
func unifiedDiff(oldText, newText, path string) string {
	if oldText == newText {
		return ""
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	var out strings.Builder
	display := strings.TrimPrefix(path, "/")
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", display, display)
	for _, hunk := range groupDiffOpcodes(diffOpcodes(a, b), diffContextLines) {
		oldStart, oldEnd := hunk[0].i1, hunk[len(hunk)-1].i2
		newStart, newEnd := hunk[0].j1, hunk[len(hunk)-1].j2
		oldCount, newCount := oldEnd-oldStart, newEnd-newStart
		// Unified format numbers a zero-length range by the line before it.
		oldLine, newLine := oldStart+1, newStart+1
		if oldCount == 0 {
			oldLine = oldStart
		}
		if newCount == 0 {
			newLine = newStart
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldLine, oldCount, newLine, newCount)
		for _, op := range hunk {
			switch op.tag {
			case 'e':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, ' ', l)
				}
			case 'd':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, '-', l)
				}
			case 'i':
				for _, l := range b[op.j1:op.j2] {
					writeDiffLine(&out, '+', l)
				}
			case 'r':
				for _, l := range a[op.i1:op.i2] {
					writeDiffLine(&out, '-', l)
				}
				for _, l := range b[op.j1:op.j2] {
					writeDiffLine(&out, '+', l)
				}
			}
		}
	}
	return out.String()
}
