package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rdev/internal/agent"
)

// Agent session history persistence: one JSONL file per thread under
// <dataDir>/agent/sessions/<device>/<thread>.jsonl (a legacy flat
// <device>.jsonl is migrated to t_1). Line 1 is a meta record, the rest are
// messages.
const (
	agentHistoryVersion      = 1
	agentHistoryMaxMessages  = 200     // compact-rewrite beyond this many messages
	agentHistoryKeepMessages = 150     // messages retained by a compaction
	agentHistoryMaxBytes     = 1 << 20 // compact-rewrite beyond this file size
	agentHistoryMaxLineBytes = 8 << 20
	agentHistoryMaxLoadBytes = 16 << 20
)

// agentHistoryToolCall is the on-disk shape for a tool call. agent.ToolCall's
// MarshalJSON emits the OpenAI wire shape without a matching UnmarshalJSON,
// so history uses its own plain representation.
type agentHistoryToolCall struct {
	IsError   bool   `json:"isError,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   string `json:"summary,omitempty"`
	Diff      string `json:"diff,omitempty"`
}

// agentHistoryLine is one JSONL record: meta (t=="meta") or message (t=="msg").
type agentHistoryLine struct {
	MessageID     string                   `json:"messageId,omitempty"`
	ContextOnly   bool                     `json:"contextOnly,omitempty"`
	Goal          *agentGoal               `json:"goal,omitempty"`
	Loop          *agentLoop               `json:"loop,omitempty"`
	Anthropic     *agent.ProviderReplay    `json:"anthropic,omitempty"`
	Responses     *agent.ResponseReplay    `json:"responses,omitempty"`
	OutcomeAt     string                   `json:"outcomeAt,omitempty"`
	Outcome       string                   `json:"outcome,omitempty"`
	Diagnostic    *agent.FailureDiagnostic `json:"diagnostic,omitempty"`
	Display       json.RawMessage          `json:"display,omitempty"`
	ImagePreviews []string                 `json:"imagePreviews,omitempty"`
	T             string                   `json:"t"`
	V             int                      `json:"v,omitempty"`
	Device        string                   `json:"device,omitempty"`
	Thread        string                   `json:"thread,omitempty"`
	Model         string                   `json:"model,omitempty"`
	Cwd           string                   `json:"cwd,omitempty"`
	Title         string                   `json:"title,omitempty"`
	Updated       string                   `json:"updated,omitempty"`
	Role          string                   `json:"role,omitempty"`
	Content       string                   `json:"content,omitempty"`
	Reasoning     string                   `json:"reasoning,omitempty"`
	ToolCalls     []agentHistoryToolCall   `json:"tool_calls,omitempty"`
	ToolCallID    string                   `json:"tool_call_id,omitempty"`
	// Images holds the file names of a user message's image attachments;
	// the image data itself is never persisted.
	Images []string `json:"images,omitempty"`
}

// agentHistoryEntry is one rendered conversation entry sent in auth_ok.
type agentHistoryEntry struct {
	MessageID     string             `json:"messageId"`
	Display       json.RawMessage    `json:"display,omitempty"`
	ImagePreviews []string           `json:"imagePreviews,omitempty"`
	Role          string             `json:"role"` // "user" | "assistant"
	Text          string             `json:"text,omitempty"`
	Reasoning     string             `json:"reasoning,omitempty"`
	Tools         []agentHistoryTool `json:"tools,omitempty"`
	Images        []string           `json:"images,omitempty"`
}

type agentHistoryTool struct {
	IsError bool   `json:"isError,omitempty"`
	Name    string `json:"name"`
	Args    string `json:"args,omitempty"`
	Result  string `json:"result,omitempty"`
	Summary string `json:"summary,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// agentThreadMeta describes one persisted thread of a device.
type agentThreadMeta struct {
	OutcomeAt  string
	Outcome    string
	Diagnostic *agent.FailureDiagnostic
	ID         string
	Title      string // explicit meta title if set, else derived from the first user message
	// MetaTitle is the explicit title stored in the meta line ("" when the
	// thread was never renamed).
	MetaTitle string
	Model     string
	Cwd       string
	UpdatedAt time.Time
}

// agentThreadExtras are the meta-line fields (besides the model) carried
// through rewrites: the thread's working directory and explicit title.
type agentThreadExtras struct {
	Goal       *agentGoal
	Loop       *agentLoop
	OutcomeAt  string
	Outcome    string
	Diagnostic *agent.FailureDiagnostic
	Cwd        string
	Title      string
}

func validatedAgentOutcome(outcome string) string {
	switch outcome {
	case "done", "error", "stopped", "running":
		return outcome
	}
	return ""
}

// A running marker read from disk has no surviving foreground process after
// restart. Report interruption instead of reviving its previous success.
func restoredAgentOutcome(outcome string) string {
	if outcome == "running" {
		return "stopped"
	}
	return validatedAgentOutcome(outcome)
}

func outcomeDiagnostic(outcome string, diagnostic *agent.FailureDiagnostic) *agent.FailureDiagnostic {
	if outcome != "error" && outcome != "stopped" {
		return nil
	}
	return diagnostic.Safe()
}

// cleanup deletes history files idle beyond the TTL: thread files inside
// per-device directories (then-empty dirs are dropped) and legacy flat files.
func (st *agentSessionStore) cleanup() {
	// Serialize with rewrite/append: deleting an empty device directory
	// between MkdirAll and WriteFile can otherwise discard a new history.
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-st.ttl)
	// Orphaned rewrite temp files (crash between WriteFile and Rename) are
	// dropped much sooner than session data.
	tmpCutoff := time.Now().Add(-time.Hour)
	removed := 0
	removeIfStale := func(path string, info os.FileInfo) {
		if info.ModTime().After(cutoff) {
			return
		}
		deviceID := filepath.Base(filepath.Dir(path))
		threadID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if filepath.Dir(path) == st.dir {
			deviceID, threadID = threadID, "t_1"
		}
		key := deviceID + "/" + threadID
		if _, faulted := st.faults[key]; faulted {
			return
		}
		st.loadPathLocked(deviceID, threadID, path)
		if _, faulted := st.faults[key]; faulted {
			return
		}
		if err := os.Remove(path); err == nil {
			delete(st.saved, key)
			delete(st.loaded, key)
			delete(st.extras, key)
			_ = os.Remove(st.transcriptPath(deviceID, threadID))
			delete(st.transcripts, key)
			st.recentTranscripts.Delete(key)
			_ = os.RemoveAll(st.childTranscriptsPath(deviceID, threadID))
			if strings.HasSuffix(path, ".jsonl") {
				_ = os.Remove(strings.TrimSuffix(path, ".jsonl") + ".jobs.json")
			}
			removed++
		}
	}
	removeTmpIfStale := func(path string, info os.FileInfo) {
		if info.ModTime().After(tmpCutoff) {
			return
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}

	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(st.dir, e.Name())
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if strings.HasSuffix(e.Name(), ".jsonl.tmp") {
				removeTmpIfStale(full, info)
			} else if strings.HasSuffix(e.Name(), ".jsonl") {
				removeIfStale(full, info)
			}
			continue
		}
		// Per-device thread directory.
		sub, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() {
				continue
			}
			info, err := se.Info()
			if err != nil {
				continue
			}
			if strings.HasSuffix(se.Name(), ".jsonl.tmp") || strings.HasSuffix(se.Name(), ".jobs.json.tmp") {
				removeTmpIfStale(filepath.Join(full, se.Name()), info)
			} else if strings.HasSuffix(se.Name(), ".jsonl") {
				removeIfStale(filepath.Join(full, se.Name()), info)
			} else if strings.HasSuffix(se.Name(), ".jobs.json") {
				history := strings.TrimSuffix(filepath.Join(full, se.Name()), ".jobs.json") + ".jsonl"
				if _, err := os.Stat(history); os.IsNotExist(err) {
					_ = os.Remove(filepath.Join(full, se.Name()))
				}
			}
		}
		// Drop the device dir once nothing is left in it.
		if rest, err := os.ReadDir(full); err == nil && len(rest) == 0 {
			_ = os.Remove(full)
		}
	}
	if removed > 0 {
		log.Printf("agent history: removed %d expired session file(s)", removed)
	}
}

// startJanitor runs one cleanup pass immediately, then hourly.
func (st *agentSessionStore) startJanitor() {
	go func() {
		st.cleanup()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			st.cleanup()
		}
	}()
}
