package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
)

// agentHistoryToolCall is the on-disk shape for a tool call. agent.ToolCall's
// MarshalJSON emits the OpenAI wire shape without a matching UnmarshalJSON,
// so history uses its own plain representation.
type agentHistoryToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   string `json:"summary,omitempty"`
	Diff      string `json:"diff,omitempty"`
}

// agentHistoryLine is one JSONL record: meta (t=="meta") or message (t=="msg").
type agentHistoryLine struct {
	T          string                 `json:"t"`
	V          int                    `json:"v,omitempty"`
	Device     string                 `json:"device,omitempty"`
	Thread     string                 `json:"thread,omitempty"`
	Model      string                 `json:"model,omitempty"`
	Cwd        string                 `json:"cwd,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Updated    string                 `json:"updated,omitempty"`
	Role       string                 `json:"role,omitempty"`
	Content    string                 `json:"content,omitempty"`
	Reasoning  string                 `json:"reasoning,omitempty"`
	ToolCalls  []agentHistoryToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	// Images holds the file names of a user message's image attachments;
	// the image data itself is never persisted.
	Images []string `json:"images,omitempty"`
}

// agentHistoryEntry is one rendered conversation entry sent in auth_ok.
type agentHistoryEntry struct {
	Role      string             `json:"role"` // "user" | "assistant"
	Text      string             `json:"text,omitempty"`
	Reasoning string             `json:"reasoning,omitempty"`
	Tools     []agentHistoryTool `json:"tools,omitempty"`
	Images    []string           `json:"images,omitempty"`
}

type agentHistoryTool struct {
	Name    string `json:"name"`
	Args    string `json:"args,omitempty"`
	Result  string `json:"result,omitempty"`
	Summary string `json:"summary,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// agentThreadMeta describes one persisted thread of a device.
type agentThreadMeta struct {
	ID    string
	Title string // explicit meta title if set, else derived from the first user message
	// MetaTitle is the explicit title stored in the meta line ("" when the
	// thread was never renamed).
	MetaTitle string
	Model     string
	Cwd       string
	UpdatedAt time.Time
}

// agentSessionStore persists per-device, per-thread agent conversation
// history as JSONL under <dir>/<deviceID>/<threadID>.jsonl. A single mutex
// guards all files; volume is tiny.
type agentSessionStore struct {
	dir    string
	ttl    time.Duration
	mu     sync.Mutex
	saved  map[string]int // deviceID + "/" + threadID -> number of history messages already on disk
	extras map[string]agentThreadExtras
}

// agentThreadExtras are the meta-line fields (besides the model) carried
// through rewrites: the thread's working directory and explicit title.
type agentThreadExtras struct {
	Cwd   string
	Title string
}

func newAgentSessionStore(dir string, ttl time.Duration) *agentSessionStore {
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("agent history: mkdir %s: %v", dir, err)
	}
	return &agentSessionStore{dir: dir, ttl: ttl, saved: make(map[string]int), extras: make(map[string]agentThreadExtras)}
}

// sanitizeAgentHistoryName makes a device ID safe for use as a file name.
func sanitizeAgentHistoryName(deviceID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, deviceID)
}

func (st *agentSessionStore) path(deviceID, threadID string) string {
	return filepath.Join(st.dir, sanitizeAgentHistoryName(deviceID), sanitizeAgentHistoryName(threadID)+".jsonl")
}

// legacyPath is the pre-threads single-file layout for a device.
func (st *agentSessionStore) legacyPath(deviceID string) string {
	return filepath.Join(st.dir, sanitizeAgentHistoryName(deviceID)+".jsonl")
}

// migrate moves a legacy single-file history (<device>.jsonl) into the
// threaded layout as t_1. No-op when no legacy file exists.
func (st *agentSessionStore) migrate(deviceID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	legacy := st.legacyPath(deviceID)
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	dir := filepath.Join(st.dir, sanitizeAgentHistoryName(deviceID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("agent history: migrate %s: %v", deviceID, err)
		return
	}
	dst := st.path(deviceID, "t_1")
	if _, err := os.Stat(dst); err == nil {
		// t_1 already exists: drop the legacy file rather than merge.
		if err := os.Remove(legacy); err != nil {
			log.Printf("agent history: migrate %s: %v", deviceID, err)
		}
		return
	}
	if err := os.Rename(legacy, dst); err != nil {
		log.Printf("agent history: migrate %s: %v", deviceID, err)
	}
}

// listThreads returns the persisted threads of a device, most recently
// listDevices returns the device ids that have a session directory (any
// device that ever ran an agent thread with persistence enabled).
func (st *agentSessionStore) listDevices() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// updated first. Titles derive from the first user message.
func (st *agentSessionStore) listThreads(deviceID string) []agentThreadMeta {
	st.mu.Lock()
	defer st.mu.Unlock()
	dir := filepath.Join(st.dir, sanitizeAgentHistoryName(deviceID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var metas []agentThreadMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		threadID := strings.TrimSuffix(e.Name(), ".jsonl")
		msgs, model, cwd, metaTitle, updated := st.loadLocked(deviceID, threadID)
		if updated.IsZero() {
			if info, ierr := e.Info(); ierr == nil {
				updated = info.ModTime()
			}
		}
		title := metaTitle
		if title == "" {
			title = threadTitle(msgs)
		}
		metas = append(metas, agentThreadMeta{
			ID:        threadID,
			Title:     title,
			MetaTitle: metaTitle,
			Model:     model,
			Cwd:       cwd,
			UpdatedAt: updated,
		})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UpdatedAt.After(metas[j].UpdatedAt) })
	return metas
}

// threadTitle derives a thread's display title: the first 30 runes of the
// first user message, or "新对话" when there is none.
func threadTitle(msgs []agent.Message) string {
	for _, m := range msgs {
		if m.Role != "user" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		title := strings.TrimSpace(m.Content)
		n := 0
		for i := range title {
			if n == 30 {
				return title[:i]
			}
			n++
		}
		return title
	}
	return "新对话"
}

// load reads the stored history for one thread. Malformed lines are skipped;
// the first valid meta line provides the last-used model, working directory
// and explicit title.
func (st *agentSessionStore) load(deviceID, threadID string) (msgs []agent.Message, model, cwd, title string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	msgs, model, cwd, title, _ = st.loadLocked(deviceID, threadID)
	st.saved[deviceID+"/"+threadID] = len(msgs)
	return msgs, model, cwd, title
}

func (st *agentSessionStore) loadLocked(deviceID, threadID string) (msgs []agent.Message, model, cwd, title string, updated time.Time) {
	f, err := os.Open(st.path(deviceID, threadID))
	if err != nil {
		return nil, "", "", "", time.Time{}
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), agentHistoryMaxBytes*2)
	metaSeen := false
	for scanner.Scan() {
		var line agentHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		switch line.T {
		case "meta":
			if !metaSeen && line.V == agentHistoryVersion {
				metaSeen = true
				model = line.Model
				cwd = line.Cwd
				title = line.Title
				updated, _ = time.Parse(time.RFC3339, line.Updated)
				st.extras[deviceID+"/"+threadID] = agentThreadExtras{Cwd: cwd, Title: title}
			}
		case "msg":
			switch line.Role {
			case "user", "assistant", "tool":
			default:
				continue
			}
			msg := agent.Message{Role: line.Role, Content: line.Content, ToolCallID: line.ToolCallID, Reasoning: line.Reasoning}
			for _, name := range line.Images {
				msg.Images = append(msg.Images, agent.MessageImage{Name: name})
			}
			for _, tc := range line.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff})
			}
			msgs = append(msgs, msg)
		}
	}
	return msgs, model, cwd, title, updated
}

// peekLocked returns the meta model and size of a thread file ("", 0 when
// missing or without a valid meta line).
func (st *agentSessionStore) peekLocked(deviceID, threadID string) (model string, size int64) {
	fi, err := os.Stat(st.path(deviceID, threadID))
	if err != nil {
		return "", 0
	}
	size = fi.Size()
	f, err := os.Open(st.path(deviceID, threadID))
	if err != nil {
		return "", size
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var line agentHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err == nil && line.T == "meta" {
			return line.Model, size
		}
	}
	return "", size
}

// save persists history for one thread: new messages since the last save are
// appended; the whole file is compact-rewritten when the model changed, the
// history shrank (reset/rebuild), or the message/byte caps are exceeded.
func (st *agentSessionStore) save(deviceID, threadID, model string, history []agent.Message) {
	if len(history) == 0 {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	key := deviceID + "/" + threadID
	savedCount := st.saved[key]
	if savedCount > len(history) {
		savedCount = 0 // history shrank: rewrite from scratch
	}
	newMsgs := history[savedCount:]

	fileModel, fileSize := st.peekLocked(deviceID, threadID)
	rewrite := savedCount == 0 || fileModel != model ||
		len(history) > agentHistoryMaxMessages || fileSize > agentHistoryMaxBytes

	if rewrite {
		kept := history
		if len(kept) > agentHistoryKeepMessages {
			kept = kept[len(kept)-agentHistoryKeepMessages:]
		}
		if err := st.rewriteLocked(deviceID, threadID, model, kept); err != nil {
			log.Printf("agent history: save %s/%s: %v", deviceID, threadID, err)
			return
		}
		// Everything currently in history is represented in the file (compacted).
		st.saved[key] = len(history)
		return
	}

	if len(newMsgs) == 0 {
		return // nothing new, meta unchanged
	}
	if err := os.MkdirAll(filepath.Dir(st.path(deviceID, threadID)), 0700); err != nil {
		log.Printf("agent history: mkdir %s/%s: %v", deviceID, threadID, err)
		return
	}
	f, err := os.OpenFile(st.path(deviceID, threadID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("agent history: open %s: %v", deviceID, err)
		return
	}
	var buf bytes.Buffer
	for _, m := range newMsgs {
		if b := marshalAgentHistoryMsg(m); b != nil {
			buf.Write(b)
			buf.WriteByte('\n')
		}
	}
	_, werr := f.Write(buf.Bytes())
	f.Close()
	if werr != nil {
		log.Printf("agent history: append %s: %v", deviceID, werr)
		return
	}
	st.saved[key] = len(history)
}

// rewriteLocked atomically replaces a thread file: fresh meta line + msgs.
// The meta line carries the thread extras (cwd/title) recorded in st.extras.
func (st *agentSessionStore) rewriteLocked(deviceID, threadID, model string, msgs []agent.Message) error {
	extras := st.extras[deviceID+"/"+threadID]
	var buf bytes.Buffer
	meta, err := json.Marshal(agentHistoryLine{
		T: "meta", V: agentHistoryVersion, Device: deviceID, Thread: threadID, Model: model,
		Cwd: extras.Cwd, Title: extras.Title,
		Updated: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	buf.Write(meta)
	buf.WriteByte('\n')
	for _, m := range msgs {
		if b := marshalAgentHistoryMsg(m); b != nil {
			buf.Write(b)
			buf.WriteByte('\n')
		}
	}
	if err := os.MkdirAll(filepath.Dir(st.path(deviceID, threadID)), 0700); err != nil {
		return err
	}
	tmp := st.path(deviceID, threadID) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, st.path(deviceID, threadID))
}

// setCwd records a thread's working directory and rewrites its history file
// so the meta line carries it (cwd changes are rare; a full rewrite is
// acceptable). The on-disk model is preserved.
func (st *agentSessionStore) setCwd(deviceID, threadID, cwd string, history []agent.Message) {
	st.setExtra(deviceID, threadID, func(x *agentThreadExtras) { x.Cwd = cwd }, history)
}

// setTitle records a thread's explicit title and rewrites its history file
// so the meta line carries it.
func (st *agentSessionStore) setTitle(deviceID, threadID, title string, history []agent.Message) {
	st.setExtra(deviceID, threadID, func(x *agentThreadExtras) { x.Title = title }, history)
}

func (st *agentSessionStore) setExtra(deviceID, threadID string, update func(*agentThreadExtras), history []agent.Message) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := deviceID + "/" + threadID
	extras := st.extras[key]
	update(&extras)
	st.extras[key] = extras
	model, _ := st.peekLocked(deviceID, threadID)
	if err := st.rewriteLocked(deviceID, threadID, model, history); err != nil {
		log.Printf("agent history: meta rewrite %s/%s: %v", deviceID, threadID, err)
		return
	}
	st.saved[key] = len(history)
}

// marshalAgentHistoryMsg converts one message to its JSONL record. Only
// strings are marshaled, so an error is practically unreachable.
func marshalAgentHistoryMsg(m agent.Message) []byte {
	line := agentHistoryLine{
		T: "msg", Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Reasoning: m.Reasoning,
	}
	for _, img := range m.Images {
		line.Images = append(line.Images, img.Name)
	}
	for _, tc := range m.ToolCalls {
		line.ToolCalls = append(line.ToolCalls, agentHistoryToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff})
	}
	data, err := json.Marshal(line)
	if err != nil {
		return nil
	}
	return data
}

// clear deletes the stored history for one thread (reset/delete-thread op).
func (st *agentSessionStore) clear(deviceID, threadID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.Remove(st.path(deviceID, threadID)); err != nil && !os.IsNotExist(err) {
		log.Printf("agent history: clear %s/%s: %v", deviceID, threadID, err)
	}
	delete(st.saved, deviceID+"/"+threadID)
	// st.extras is kept: a reset thread lives on in memory and its next save
	// must re-persist cwd/title. Deleted threads drop extras via dropExtras.
}

// dropExtras forgets the recorded cwd/title of a removed thread.
func (st *agentSessionStore) dropExtras(deviceID, threadID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.extras, deviceID+"/"+threadID)
}

// cleanup deletes history files idle beyond the TTL: thread files inside
// per-device directories (then-empty dirs are dropped) and legacy flat files.
func (st *agentSessionStore) cleanup() {
	if st.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-st.ttl)
	removed := 0
	removeIfStale := func(path string, info os.FileInfo) {
		if info.ModTime().After(cutoff) {
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
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if info, err := e.Info(); err == nil {
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
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".jsonl") {
				continue
			}
			if info, err := se.Info(); err == nil {
				removeIfStale(filepath.Join(full, se.Name()), info)
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

// renderAgentHistory converts stored messages into the auth_ok history shape:
// user messages and assistant turns, with tool results paired by tool_call_id.
func renderAgentHistory(msgs []agent.Message) []agentHistoryEntry {
	toolResults := make(map[string]string)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}
	var entries []agentHistoryEntry
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if m.Content == "" && len(m.Images) == 0 {
				continue
			}
			entries = append(entries, agentHistoryEntry{Role: "user", Text: m.Content, Images: agentImageNames(m.Images)})
		case "assistant":
			e := agentHistoryEntry{Role: "assistant", Text: m.Content, Reasoning: m.Reasoning}
			for _, tc := range m.ToolCalls {
				t := agentHistoryTool{Name: tc.Name, Args: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff}
				if res, ok := toolResults[tc.ID]; ok {
					t.Result = res
				}
				e.Tools = append(e.Tools, t)
			}
			if e.Text == "" && len(e.Tools) == 0 {
				continue
			}
			entries = append(entries, e)
		}
	}
	return entries
}

// agentStore lazily creates the per-device history store. Returns nil when
// persistence is disabled (no data dir or TTL <= 0). The TTL janitor starts
// with the store when the agent is enabled.
func (s *Server) agentStore() *agentSessionStore {
	if s.DataDir == "" || s.AgentSessionTTL <= 0 {
		return nil
	}
	s.agentStoreOnce.Do(func() {
		s.agentStoreInst = newAgentSessionStore(filepath.Join(s.DataDir, "agent", "sessions"), s.AgentSessionTTL)
		if s.Agent != nil && s.Agent.Enabled() {
			s.agentStoreInst.startJanitor()
		}
	})
	return s.agentStoreInst
}
