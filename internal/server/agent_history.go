package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	agentHistoryMaxLineBytes = 8 << 20
	agentHistoryMaxLoadBytes = 16 << 20
)

// AgentHistoryHealth is an in-memory snapshot; querying it never scans files.
type AgentHistoryHealth struct {
	Loaded  int                 `json:"loaded"`
	Faulted int                 `json:"faulted"`
	Faults  []AgentHistoryFault `json:"faults"`
}

type AgentHistoryFault struct {
	Device string `json:"device"`
	Thread string `json:"thread"`
	Error  string `json:"error"`
}

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
	MessageID     string                 `json:"messageId,omitempty"`
	ContextOnly   bool                   `json:"contextOnly,omitempty"`
	Goal          *agentGoal             `json:"goal,omitempty"`
	Loop          *agentLoop             `json:"loop,omitempty"`
	Anthropic     *agent.ProviderReplay  `json:"anthropic,omitempty"`
	Responses     *agent.ResponseReplay  `json:"responses,omitempty"`
	OutcomeAt     string                 `json:"outcomeAt,omitempty"`
	Outcome       string                 `json:"outcome,omitempty"`
	Display       json.RawMessage        `json:"display,omitempty"`
	ImagePreviews []string               `json:"imagePreviews,omitempty"`
	T             string                 `json:"t"`
	V             int                    `json:"v,omitempty"`
	Device        string                 `json:"device,omitempty"`
	Thread        string                 `json:"thread,omitempty"`
	Model         string                 `json:"model,omitempty"`
	Cwd           string                 `json:"cwd,omitempty"`
	Title         string                 `json:"title,omitempty"`
	Updated       string                 `json:"updated,omitempty"`
	Role          string                 `json:"role,omitempty"`
	Content       string                 `json:"content,omitempty"`
	Reasoning     string                 `json:"reasoning,omitempty"`
	ToolCalls     []agentHistoryToolCall `json:"tool_calls,omitempty"`
	ToolCallID    string                 `json:"tool_call_id,omitempty"`
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
	OutcomeAt string
	Outcome   string
	ID        string
	Title     string // explicit meta title if set, else derived from the first user message
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
	dir               string
	ttl               time.Duration
	mu                sync.Mutex
	saved             map[string]int // deviceID + "/" + threadID -> number of history messages already on disk
	extras            map[string]agentThreadExtras
	loaded            map[string]bool
	faults            map[string]AgentHistoryFault
	transcripts       map[string]*agentTranscript
	recentTranscripts sync.Map // key -> immutable agentHistoryPage; snapshot reads do not take disk mutex
}

// agentThreadExtras are the meta-line fields (besides the model) carried
// through rewrites: the thread's working directory and explicit title.
type agentThreadExtras struct {
	Goal      *agentGoal
	Loop      *agentLoop
	OutcomeAt string
	Outcome   string
	Cwd       string
	Title     string
}

func newAgentSessionStore(dir string, ttl time.Duration) *agentSessionStore {
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("agent history: mkdir %s: %v", dir, err)
	}
	return &agentSessionStore{dir: dir, ttl: ttl, saved: make(map[string]int), extras: make(map[string]agentThreadExtras), loaded: make(map[string]bool), faults: make(map[string]AgentHistoryFault)}
}

func (st *agentSessionStore) historyHealth() AgentHistoryHealth {
	st.mu.Lock()
	defer st.mu.Unlock()
	health := AgentHistoryHealth{Loaded: len(st.loaded), Faulted: len(st.faults), Faults: make([]AgentHistoryFault, 0, min(20, len(st.faults)))}
	for _, fault := range st.faults {
		if len(health.Faults) == 20 {
			last := health.Faults[19]
			if fault.Device > last.Device || fault.Device == last.Device && fault.Thread >= last.Thread {
				continue
			}
			health.Faults[19] = fault
		} else {
			health.Faults = append(health.Faults, fault)
		}
		sort.Slice(health.Faults, func(i, j int) bool {
			if health.Faults[i].Device == health.Faults[j].Device {
				return health.Faults[i].Thread < health.Faults[j].Thread
			}
			return health.Faults[i].Device < health.Faults[j].Device
		})
	}
	return health

}
func (st *agentSessionStore) historyFault(deviceID, threadID string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.faults[deviceID+"/"+threadID].Error
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
		// Never discard an unexamined legacy original in favor of another file.
		return
	}
	if err := os.Rename(legacy, dst); err != nil {
		log.Printf("agent history: migrate %s: %v", deviceID, err)
		return
	}
	key := deviceID + "/t_1"
	delete(st.saved, key)
	delete(st.loaded, key)
	delete(st.extras, key)
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
		// Append saves don't rewrite the meta line, so its Updated field
		// goes stale; the file mtime is the fresher ordering signal.
		if info, ierr := e.Info(); ierr == nil && info.ModTime().After(updated) {
			updated = info.ModTime()
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
			Outcome:   st.extras[deviceID+"/"+threadID].Outcome,
			OutcomeAt: st.extras[deviceID+"/"+threadID].OutcomeAt,
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

// load reads a thread and records any parse or I/O fault without claiming a
// partial result as fully persisted. Valid records remain available for safe
// explicit recovery.

func (st *agentSessionStore) load(deviceID, threadID string) (msgs []agent.Message, model, cwd, title string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := deviceID + "/" + threadID
	msgs, model, cwd, title, _ = st.loadLocked(deviceID, threadID)
	if _, faulted := st.faults[key]; faulted {
		delete(st.saved, key)
	} else {
		st.saved[key] = len(msgs)
		assignLegacyMessageIDs(deviceID, threadID, msgs)
		if err := st.captureTranscriptLocked(deviceID, threadID, msgs); err != nil {
			log.Printf("agent transcript: migrate %s/%s: %v", deviceID, threadID, err)
		}
	}
	return msgs, model, cwd, title
}
func (st *agentSessionStore) loadLocked(deviceID, threadID string) (msgs []agent.Message, model, cwd, title string, updated time.Time) {
	return st.loadPathLocked(deviceID, threadID, st.path(deviceID, threadID))
}

func (st *agentSessionStore) loadPathLocked(deviceID, threadID, path string) (msgs []agent.Message, model, cwd, title string, updated time.Time) {
	key := deviceID + "/" + threadID
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			st.recordFaultLocked(deviceID, threadID, fmt.Sprintf("open history: %v", err))
		} else {
			delete(st.saved, key)
			delete(st.loaded, key)
		}
		return nil, "", "", "", time.Time{}
	}
	defer f.Close()
	delete(st.extras, key)
	scanner := bufio.NewScanner(io.LimitReader(f, agentHistoryMaxLoadBytes+1))
	scanner.Buffer(make([]byte, 64*1024), agentHistoryMaxLineBytes+2)
	metaSeen := false
	var parseErr error
	if info, err := f.Stat(); err != nil {
		parseErr = fmt.Errorf("stat history: %w", err)
	} else if info.Size() > agentHistoryMaxLoadBytes {
		parseErr = fmt.Errorf("history exceeds read size limit (%d bytes)", agentHistoryMaxLoadBytes)
	}
	var readBytes int64
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		readBytes += int64(advance)
		return advance, token, err
	})
	for scanner.Scan() {
		if readBytes > agentHistoryMaxLoadBytes {
			parseErr = errors.Join(parseErr, fmt.Errorf("history exceeds read size limit (%d bytes)", agentHistoryMaxLoadBytes))
			break
		}
		if len(scanner.Bytes()) > agentHistoryMaxLineBytes {
			parseErr = errors.Join(parseErr, errors.New("record exceeds history line limit"))
			break
		}
		var line agentHistoryLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			parseErr = errors.Join(parseErr, fmt.Errorf("malformed record: %w", err))
			continue
		}
		if !metaSeen && line.T != "meta" {
			parseErr = errors.Join(parseErr, errors.New("record precedes metadata"))
		}
		switch line.T {
		case "meta":
			if metaSeen || line.V != agentHistoryVersion {
				parseErr = errors.Join(parseErr, errors.New("duplicate or unsupported metadata record"))
				continue
			}
			metaSeen = true
			model = line.Model
			cwd = line.Cwd
			title = line.Title
			updated, _ = time.Parse(time.RFC3339, line.Updated)
			st.extras[deviceID+"/"+threadID] = agentThreadExtras{Cwd: cwd, Title: title, Outcome: validatedAgentOutcome(line.Outcome), OutcomeAt: line.OutcomeAt}
			x := st.extras[deviceID+"/"+threadID]
			x.Goal, x.Loop = line.Goal, line.Loop
			st.extras[deviceID+"/"+threadID] = x
		case "autonomy":
			key := deviceID + "/" + threadID
			x := st.extras[key]
			x.Goal, x.Loop = line.Goal, line.Loop
			st.extras[key] = x
		case "outcome":
			if outcome := validatedAgentOutcome(line.Outcome); outcome != "" {
				key := deviceID + "/" + threadID
				extras := st.extras[key]
				extras.Outcome = outcome
				extras.OutcomeAt = line.OutcomeAt
				st.extras[key] = extras
			}
		case "msg":
			switch line.Role {
			case "user", "assistant", "tool":
			default:
				parseErr = errors.Join(parseErr, fmt.Errorf("unsupported message role %q", line.Role))
				continue
			}
			msg := agent.Message{MessageID: line.MessageID, ContextOnly: line.ContextOnly, Anthropic: line.Anthropic, Responses: line.Responses, Display: line.Display, Role: line.Role, Content: line.Content, ToolCallID: line.ToolCallID, Reasoning: line.Reasoning}
			for i, name := range line.Images {
				var preview string
				if i < len(line.ImagePreviews) {
					preview = line.ImagePreviews[i]
				}
				msg.Images = append(msg.Images, agent.MessageImage{Name: name, Preview: preview})
			}
			for _, tc := range line.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, agent.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff, IsError: tc.IsError})
			}
			msgs = append(msgs, msg)
		default:
			parseErr = errors.Join(parseErr, fmt.Errorf("unsupported record type %q", line.T))
		}
	}
	if err := scanner.Err(); err != nil {
		parseErr = errors.Join(parseErr, fmt.Errorf("read history: %w", err))
	}
	if !metaSeen {
		parseErr = errors.Join(parseErr, errors.New("missing valid metadata record"))
	}
	if parseErr != nil {
		st.recordFaultLocked(deviceID, threadID, parseErr.Error())
	} else if _, faulted := st.faults[key]; !faulted {
		st.loaded[key] = true
	}
	return msgs, model, cwd, title, updated
}

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
	scanner.Buffer(make([]byte, 64*1024), agentHistoryMaxLineBytes)
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
	if !st.writableLocked(deviceID, threadID) {
		return // explicit recovery is required before writing damaged history
	}
	assignLegacyMessageIDs(deviceID, threadID, history)
	if err := st.captureTranscriptLocked(deviceID, threadID, history); err != nil {
		log.Printf("agent transcript: save %s/%s: %v", deviceID, threadID, err)
		return
	}
	savedCount := st.saved[key]
	if savedCount > len(history) {
		savedCount = 0 // history shrank: rewrite from scratch
	}
	newMsgs := history[savedCount:]

	fileModel, fileSize := st.peekLocked(deviceID, threadID)
	rewrite := savedCount == 0 || fileModel != model ||
		len(history) > agentHistoryMaxMessages || fileSize > agentHistoryMaxBytes

	if rewrite {
		kept := compactAgentHistory(history)
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
	var buf bytes.Buffer
	for _, m := range newMsgs {
		b := marshalAgentHistoryMsg(m)
		if b == nil || len(b) > agentHistoryMaxLineBytes {
			st.recordFaultLocked(deviceID, threadID, "message cannot be encoded within the history line limit")
			return
		}
		buf.Write(b)
		buf.WriteByte('\n')
		if fileSize+int64(buf.Len()) > agentHistoryMaxBytes {
			break // the rewrite below serializes the complete retained history
		}
	}
	if fileSize+int64(buf.Len()) > agentHistoryMaxBytes {
		kept := compactAgentHistory(history)
		if err := st.rewriteLocked(deviceID, threadID, model, kept); err == nil {
			st.saved[key] = len(history)
		}
		return
	}
	if err := appendAgentHistory(st.path(deviceID, threadID), buf.Bytes()); err != nil {
		st.recordFaultLocked(deviceID, threadID, fmt.Sprintf("append history: %v", err))
		return
	}
	st.saved[key] = len(history)
}

func (st *agentSessionStore) recordFaultLocked(deviceID, threadID, message string) {
	key := deviceID + "/" + threadID
	st.faults[key] = AgentHistoryFault{Device: deviceID, Thread: threadID, Error: message}
	delete(st.loaded, key)
	delete(st.saved, key)
}

// Validate an existing file before the first mutation, even when the caller
// has never loaded it. Faults stay latched until explicit recovery succeeds.
func (st *agentSessionStore) writableLocked(deviceID, threadID string) bool {
	key := deviceID + "/" + threadID
	if _, faulted := st.faults[key]; faulted {
		return false
	}
	if !st.loaded[key] {
		st.loadLocked(deviceID, threadID)
	}
	_, faulted := st.faults[key]
	return !faulted
}

func compactAgentHistory(history []agent.Message) []agent.Message {
	if len(history) > agentHistoryKeepMessages {
		history = history[len(history)-agentHistoryKeepMessages:]
	}
	for len(history) > 0 && history[0].Role == "tool" {
		history = history[1:]
	}
	return history
}

// Checkpoints share the same fault, encoding and compaction rules as messages.
// Keep the session's saved cursor: compaction changes disk length, not memory.
func (st *agentSessionStore) appendCheckpointLocked(deviceID, threadID, model string, line agentHistoryLine, extras agentThreadExtras) {
	key := deviceID + "/" + threadID
	data, err := json.Marshal(line)
	if err != nil || len(data) > agentHistoryMaxLineBytes {
		st.recordFaultLocked(deviceID, threadID, "checkpoint cannot be encoded within the history line limit")
		return
	}
	path := st.path(deviceID, threadID)
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		st.recordFaultLocked(deviceID, threadID, fmt.Sprintf("stat history: %v", err))
		return
	}
	if os.IsNotExist(err) || info.Size()+int64(len(data))+1 > agentHistoryMaxBytes {
		var msgs []agent.Message
		if err == nil {
			var persistedModel string
			msgs, persistedModel, _, _, _ = st.loadLocked(deviceID, threadID)
			if _, faulted := st.faults[key]; faulted {
				return
			}
			model = persistedModel
		}
		previous := st.extras[key]
		st.extras[key] = extras
		if err := st.rewriteLocked(deviceID, threadID, model, compactAgentHistory(msgs)); err != nil {
			st.extras[key] = previous
		}
		return
	}
	err = appendAgentHistory(path, append(data, '\n'))
	if err != nil {
		st.recordFaultLocked(deviceID, threadID, fmt.Sprintf("append checkpoint: %v", err))
		return
	}
	st.extras[key] = extras
}

// A valid final JSONL record need not end in a newline. Separate it before
// appending so a successful load cannot produce concatenated JSON records.
func appendAgentHistory(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if written, err := f.Write([]byte{'\n'}); err != nil {
				return err
			} else if written != 1 {
				return io.ErrShortWrite
			}
		}
	}
	written, err := f.Write(data)
	if err == nil && written != len(data) {
		return io.ErrShortWrite
	}
	return err
}

// rewriteLocked atomically replaces a thread file: fresh meta line + msgs.
// The meta line carries the thread extras (cwd/title) recorded in st.extras.
// replace persists an explicitly rewritten conversation, including rewrites
// with the same message count. The existing file survives a failed write.
func (st *agentSessionStore) replace(deviceID, threadID, model string, history []agent.Message) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := deviceID + "/" + threadID
	if !st.writableLocked(deviceID, threadID) {
		return
	}
	assignLegacyMessageIDs(deviceID, threadID, history)
	if err := st.captureTranscriptLocked(deviceID, threadID, history); err != nil {
		log.Printf("agent transcript: replace %s/%s: %v", deviceID, threadID, err)
		return
	}
	if err := st.rewriteLocked(deviceID, threadID, model, history); err != nil {
		log.Printf("agent history: replace %s/%s: %v", deviceID, threadID, err)
		return
	}
	st.saved[key] = len(history)
}

func (st *agentSessionStore) rewriteLocked(deviceID, threadID, model string, msgs []agent.Message) (err error) {
	defer func() {
		if err != nil {
			st.recordFaultLocked(deviceID, threadID, fmt.Sprintf("rewrite history: %v", err))
		} else {
			st.loaded[deviceID+"/"+threadID] = true
		}
	}()
	extras := st.extras[deviceID+"/"+threadID]
	var buf bytes.Buffer
	meta, err := json.Marshal(agentHistoryLine{
		T: "meta", V: agentHistoryVersion, Device: deviceID, Thread: threadID, Model: model,
		Goal: extras.Goal, Loop: extras.Loop,
		Cwd: extras.Cwd, Title: extras.Title, Outcome: extras.Outcome, OutcomeAt: extras.OutcomeAt,
		Updated: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if len(meta) > agentHistoryMaxLineBytes {
		return errors.New("metadata exceeds history line limit")
	}
	buf.Write(meta)
	buf.WriteByte('\n')
	for _, m := range msgs {
		b := marshalAgentHistoryMsg(m)
		if b == nil || len(b) > agentHistoryMaxLineBytes {
			return fmt.Errorf("message exceeds history line limit (%d bytes)", agentHistoryMaxLineBytes)
		}
		buf.Write(b)
		buf.WriteByte('\n')
		if buf.Len() > agentHistoryMaxLoadBytes {
			return fmt.Errorf("history exceeds write size limit (%d bytes)", agentHistoryMaxLoadBytes)
		}
	}
	if buf.Len() > agentHistoryMaxLoadBytes {
		return fmt.Errorf("history exceeds write size limit (%d bytes)", agentHistoryMaxLoadBytes)
	}
	if err := os.MkdirAll(filepath.Dir(st.path(deviceID, threadID)), 0700); err != nil {
		return err
	}
	tmp := st.path(deviceID, threadID) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, st.path(deviceID, threadID)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// recover creates a byte-for-byte backup before promoting the valid records
// parsed from a damaged file. The original remains untouched if backup or
// atomic rewrite fails.
func (st *agentSessionStore) recover(deviceID, threadID string) (int, string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	key := deviceID + "/" + threadID
	if _, damaged := st.faults[key]; !damaged {
		return 0, "", errors.New("history has no recorded damage")
	}
	path := st.path(deviceID, threadID)
	backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	source, err := os.Open(path)
	if err != nil {
		return 0, "", fmt.Errorf("open damaged history: %w", err)
	}
	dest, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = source.Close()
		return 0, "", fmt.Errorf("create recovery backup: %w", err)
	}
	_, copyErr := io.Copy(dest, source)
	closeErr := errors.Join(source.Close(), dest.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(backup)
		return 0, "", fmt.Errorf("preserve damaged history: %w", err)
	}
	// Parse the exact bytes preserved in the backup, not a separate earlier read.
	msgs, model, _, _, _ := st.loadPathLocked(deviceID, threadID, backup)
	if err := st.rewriteLocked(deviceID, threadID, model, msgs); err != nil {
		return 0, backup, fmt.Errorf("write recovered history: %w", err)
	}
	st.saved[key] = len(msgs)
	st.loaded[key] = true
	delete(st.faults, key)
	return len(msgs), backup, nil
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
	if !st.writableLocked(deviceID, threadID) {
		return
	}
	extras := st.extras[key]
	previous := extras
	update(&extras)
	st.extras[key] = extras
	model, _ := st.peekLocked(deviceID, threadID)
	if err := st.rewriteLocked(deviceID, threadID, model, history); err != nil {
		st.extras[key] = previous
		log.Printf("agent history: meta rewrite %s/%s: %v", deviceID, threadID, err)
		return
	}
	st.saved[key] = len(history)
}

// marshalAgentHistoryMsg converts one message to its JSONL record. Only
// strings are marshaled, so an error is practically unreachable.
func marshalAgentHistoryMsg(m agent.Message) []byte {
	line := agentHistoryLine{
		MessageID: m.MessageID, ContextOnly: m.ContextOnly,
		Responses: m.Responses,
		Anthropic: m.Anthropic,
		Display:   m.Display, ImagePreviews: agentImagePreviews(m.Images),
		T: "msg", Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Reasoning: m.Reasoning,
	}
	for _, img := range m.Images {
		line.Images = append(line.Images, img.Name)
	}
	for _, tc := range m.ToolCalls {
		line.ToolCalls = append(line.ToolCalls, agentHistoryToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff, IsError: tc.IsError})
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
	if !st.writableLocked(deviceID, threadID) {
		return
	}
	if err := os.Remove(st.path(deviceID, threadID)); err != nil && !os.IsNotExist(err) {
		log.Printf("agent history: clear %s/%s: %v", deviceID, threadID, err)
		return
	}
	if err := os.Remove(st.jobsPath(deviceID, threadID)); err != nil && !os.IsNotExist(err) {
		log.Printf("agent jobs: clear %s/%s: %v", deviceID, threadID, err)
	}
	_ = os.RemoveAll(st.childTranscriptsPath(deviceID, threadID))
	key := deviceID + "/" + threadID
	_ = os.Remove(st.transcriptPath(deviceID, threadID))
	delete(st.transcripts, key)
	st.recentTranscripts.Delete(key)
	delete(st.saved, key)
	delete(st.faults, key)
	delete(st.loaded, key)
	extras := st.extras[key]
	extras.Outcome = ""
	extras.OutcomeAt = ""
	extras.Goal, extras.Loop = nil, nil
	st.extras[key] = extras
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

// renderAgentHistory converts stored messages into the auth_ok history shape:
// user messages and assistant turns, with tool results paired by tool_call_id.
func renderAgentHistory(msgs []agent.Message) []agentHistoryEntry {
	var entries []agentHistoryEntry
	for i, m := range msgs {
		switch m.Role {
		case "user":
			if m.Content == "" && len(m.Images) == 0 {
				continue
			}
			entries = append(entries, agentHistoryEntry{MessageID: m.MessageID, Display: m.Display, Role: "user", Text: m.Content, Images: agentImageNames(m.Images), ImagePreviews: agentImagePreviews(m.Images)})
		case "assistant":
			e := agentHistoryEntry{MessageID: m.MessageID, Display: m.Display, Role: "assistant", Text: m.Content, Reasoning: m.Reasoning}
			// Call IDs are scoped to this response, not the conversation.
			toolResults := make(map[string]string)
			for j := i + 1; j < len(msgs) && msgs[j].Role == "tool"; j++ {
				toolResults[msgs[j].ToolCallID] = msgs[j].Content
			}
			for _, tc := range m.ToolCalls {
				t := agentHistoryTool{Name: tc.Name, Args: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff, IsError: tc.IsError}
				if res, ok := toolResults[tc.ID]; ok {
					t.Result = res
				}
				e.Tools = append(e.Tools, t)
			}
			if e.Text == "" && e.Reasoning == "" && len(e.Tools) == 0 {
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
		store := newAgentSessionStore(filepath.Join(s.DataDir, "agent", "sessions"), s.AgentSessionTTL)
		s.agentStoreMu.Lock()
		s.agentStoreInst = store
		s.agentStoreMu.Unlock()
		if s.Agent != nil && s.Agent.Enabled() {
			store.startJanitor()
		}
	})
	s.agentStoreMu.RLock()
	defer s.agentStoreMu.RUnlock()
	return s.agentStoreInst
}

// AgentHistoryHealth returns cached diagnostics without initializing storage
// or scanning history files.
func (s *Server) AgentHistoryHealth() AgentHistoryHealth {
	s.agentStoreMu.RLock()
	store := s.agentStoreInst
	s.agentStoreMu.RUnlock()
	if store == nil {
		return AgentHistoryHealth{Faults: []AgentHistoryFault{}}
	}
	return store.historyHealth()
}

func validatedAgentOutcome(outcome string) string {
	switch outcome {
	case "done", "error", "stopped", "running":
		return outcome
	}
	return ""
}

// A small append record preserves the outcome even when a command failed
// before producing history. Normal saves remain incremental, not full rewrites.
// The caller holds the thread persistence lock to serialize reset/deletion.
func (st *agentSessionStore) saveOutcome(deviceID, threadID, model, outcome, outcomeAt string) {
	outcome = validatedAgentOutcome(outcome)
	if outcome == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.writableLocked(deviceID, threadID) {
		return
	}
	extras := st.extras[deviceID+"/"+threadID]
	if outcome == "running" {
		outcomeAt = extras.OutcomeAt
	}
	extras.Outcome, extras.OutcomeAt = outcome, outcomeAt
	st.appendCheckpointLocked(deviceID, threadID, model, agentHistoryLine{T: "outcome", Outcome: outcome, OutcomeAt: outcomeAt}, extras)
}

// A running marker read from disk has no surviving foreground process after
// restart. Report interruption instead of reviving its previous success.
func restoredAgentOutcome(outcome string) string {
	if outcome == "running" {
		return "stopped"
	}
	return validatedAgentOutcome(outcome)
}
