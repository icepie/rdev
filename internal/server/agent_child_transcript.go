package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rdev/internal/agent"
)

// Child transcripts are append-only and independent of both model context and
// the 32KiB output ring. They survive job-summary eviction and are removed only
// with their owning thread (explicit reset/delete or configured session TTL).
// Quotas stop execution with an error, never trim an existing transcript.
const (
	agentChildTranscriptLimit  = 64 << 20
	agentChildThreadLimit      = 256 << 20
	agentChildThreadCountLimit = 4096
	agentChildQuotaReserve     = 64 << 10
	agentChildPageEntries      = 100
	agentChildPageBytes        = 1 << 20
)

type agentChildJobKey struct{}

type agentChildTranscript struct {
	data  []byte // memory-only sessions; durable sessions read from their file
	size  int64
	seen  map[string][32]byte
	fault error
}

type agentChildEntry struct {
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"`
	Message    json.RawMessage `json:"message,omitempty"`
	State      string          `json:"state,omitempty"`
	Finished   *time.Time      `json:"finished,omitempty"`
	Result     string          `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Output     string          `json:"output,omitempty"`
	OutputBase int64           `json:"outputBase,omitempty"`
	OutputNext int64           `json:"outputNext,omitempty"`
}

func (st *agentSessionStore) childTranscriptsPath(device, thread string) string {
	return strings.TrimSuffix(st.path(device, thread), ".jsonl") + ".children"
}

func validAgentChildID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// The jobs mutex covers archive I/O as well as Close. Once Close returns,
// delayed provider callbacks cannot recreate a reset/deleted thread's files.
func (m *agentJobs) createChildTranscriptLocked(id string) error {
	if !validAgentChildID(id) {
		return fmt.Errorf("invalid child transcript ID")
	}
	if m.children == nil {
		m.children = make(map[string]*agentChildTranscript)
	}
	count := len(m.children)
	if m.childTranscriptDir != "" {
		files, err := os.ReadDir(m.childTranscriptDir)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read child transcript directory: %w", err)
		}
		count = len(files)
	}
	if count >= agentChildThreadCountLimit {
		return fmt.Errorf("child transcript retention quota reached (%d children); export or explicitly reset/delete the thread", agentChildThreadCountLimit)
	}
	transcript := &agentChildTranscript{seen: make(map[string][32]byte)}
	m.children[id] = transcript
	if err := m.appendChildEntriesLocked(id, []agentChildEntry{{Type: "session", ID: id}}); err != nil {
		delete(m.children, id)
		return err
	}
	return nil
}

func (m *agentJobs) childBytesLocked() (int64, error) {
	var total int64
	if m.childTranscriptDir == "" {
		for _, transcript := range m.children {
			total += transcript.size
		}
		return total, nil
	}
	files, err := os.ReadDir(m.childTranscriptDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func (m *agentJobs) appendChildEntriesLocked(id string, entries []agentChildEntry) error {
	transcript := m.children[id]
	if transcript == nil {
		return fmt.Errorf("child transcript unavailable")
	}
	if transcript.fault != nil {
		return transcript.fault
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	if buffer.Len() == 0 {
		return nil
	}
	childLimit, threadLimit := int64(agentChildTranscriptLimit), int64(agentChildThreadLimit)
	for _, entry := range entries {
		if entry.Type == "message" {
			// Leave room to durably report quota failure instead of silently
			// dropping the final status when a tool result fills the archive.
			childLimit -= agentChildQuotaReserve
			threadLimit -= agentMaxRunningJobs * agentChildQuotaReserve
			break
		}
	}
	if transcript.size+int64(buffer.Len()) > childLimit {
		return fmt.Errorf("child transcript quota exceeded (%d bytes); existing records retained", childLimit)
	}
	total, err := m.childBytesLocked()
	if err != nil {
		return fmt.Errorf("inspect child transcript quota: %w", err)
	}
	if total+int64(buffer.Len()) > threadLimit {
		return fmt.Errorf("thread child transcript quota exceeded (%d bytes); existing records retained", threadLimit)
	}
	if m.childTranscriptDir == "" {
		transcript.data = append(transcript.data, buffer.Bytes()...)
	} else {
		if err := os.MkdirAll(m.childTranscriptDir, 0700); err != nil {
			return err
		}
		path := filepath.Join(m.childTranscriptDir, id+".jsonl")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("open child transcript: %w", err)
		}
		_, err = file.Write(buffer.Bytes())
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			// A failed write/sync may have committed a prefix. Do not retry it
			// as though it were atomic or hide that the durable state is unsure.
			transcript.fault = fmt.Errorf("persist child transcript: %w", err)
			return transcript.fault
		}
	}
	transcript.size += int64(buffer.Len())
	return nil
}

func (m *agentJobs) captureChildHistory(id string, history []agent.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("child transcript owner is closed")
	}
	transcript := m.children[id]
	if transcript == nil {
		return fmt.Errorf("unknown child transcript %q", id)
	}
	var entries []agentChildEntry
	changed := make(map[string][32]byte)
	for _, message := range history {
		if message.ContextOnly {
			continue
		}
		if message.MessageID == "" {
			return fmt.Errorf("child transcript message has no stable ID")
		}
		if message.ArchiveContent != "" {
			message.Content = message.ArchiveContent
		}
		raw := marshalAgentHistoryMsg(message)
		if raw == nil {
			return fmt.Errorf("encode child transcript message %q", message.MessageID)
		}
		digest := sha256.Sum256(raw)
		if prior, ok := transcript.seen[message.MessageID]; ok && prior == digest {
			continue
		}
		entries = append(entries, agentChildEntry{Type: "message", Message: raw})
		changed[message.MessageID] = digest
	}
	if err := m.appendChildEntriesLocked(id, entries); err != nil {
		return err
	}
	for id, digest := range changed {
		transcript.seen[id] = digest
	}
	return nil
}

// Cursors count actual JSONL bytes. Only complete records are returned, so a
// page cursor remains stable across restart, compaction and result transitions.
// An arbitrary interior/stale cursor resets explicitly, never silently skips.
func (m *agentJobs) readChildTranscript(id string, from int64) (ompSubagentMessages, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	page := ompSubagentMessages{
		SessionFile: "rdev://subagent/" + id,
		Entries:     []json.RawMessage{}, Messages: []json.RawMessage{},
		Retention:       "until thread reset/delete or configured session TTL",
		TranscriptLimit: agentChildTranscriptLimit, ThreadTranscriptLimit: agentChildThreadLimit,
	}
	if m.childTranscriptDir == "" {
		page.Retention = "memory only (persistence disabled); until thread reset/delete, session eviction or server restart"
	}
	if m.closed || !validAgentChildID(id) {
		return page, fmt.Errorf("unknown subagent")
	}
	if job := m.jobs[id]; job != nil && job.snapshot.Kind != "subagent" {
		return page, fmt.Errorf("unknown subagent")
	}
	var source io.ReadSeeker
	var size int64
	if m.childTranscriptDir != "" {
		file, err := os.Open(filepath.Join(m.childTranscriptDir, id+".jsonl"))
		if err != nil {
			return page, fmt.Errorf("child transcript unavailable: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return page, err
		}
		source, size = file, info.Size()
	} else {
		transcript := m.children[id]
		if transcript == nil {
			return page, fmt.Errorf("child transcript unavailable (legacy jobs contain only bounded output)")
		}
		source, size = bytes.NewReader(transcript.data), transcript.size
	}
	if size > agentChildTranscriptLimit {
		return page, fmt.Errorf("child transcript exceeds retention quota")
	}
	page.Reset = from < 0 || from > size
	if !page.Reset && from > 0 {
		if _, err := source.Seek(from-1, io.SeekStart); err != nil {
			return page, err
		}
		var previous [1]byte
		if _, err := io.ReadFull(source, previous[:]); err != nil {
			return page, err
		}
		page.Reset = previous[0] != '\n'
	}
	if page.Reset {
		from = 0
	}
	page.FromByte, page.NextByte = from, from
	if _, err := source.Seek(from, io.SeekStart); err != nil {
		return page, err
	}
	reader := bufio.NewReader(io.LimitReader(source, agentChildTranscriptLimit+1))
	for len(page.Entries) < agentChildPageEntries && page.NextByte-from < agentChildPageBytes {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return page, fmt.Errorf("incomplete child transcript at byte %d: %w", page.NextByte, err)
		}
		var entry agentChildEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return page, fmt.Errorf("invalid child transcript at byte %d: %w", page.NextByte, err)
		}
		page.Entries = append(page.Entries, json.RawMessage(bytes.TrimSuffix(line, []byte{'\n'})))
		if entry.Type == "message" {
			page.Messages = append(page.Messages, entry.Message)
		}
		if entry.Type == "completion" {
			page.Output, page.OutputBase, page.OutputNext = entry.Output, entry.OutputBase, entry.OutputNext
		}
		page.NextByte += int64(len(line))
	}
	page.HasMore = page.NextByte < size
	return page, nil
}

// A durable completion wins over an older running job snapshot. Otherwise
// append an explicit interruption record exactly once; never resume tools.
func (m *agentJobs) restoreChildTranscriptLocked(snapshot *agentJobSnapshot) error {
	if m.childTranscriptDir == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(m.childTranscriptDir, snapshot.ID+".jsonl"))
	if os.IsNotExist(err) {
		return nil // Legacy job snapshots have no fabricated transcript.
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > agentChildTranscriptLimit {
		return fmt.Errorf("child transcript exceeds retention quota")
	}
	decoder := json.NewDecoder(io.LimitReader(file, agentChildTranscriptLimit+1))
	var last agentChildEntry
	for {
		var entry agentChildEntry
		if err := decoder.Decode(&entry); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("read interrupted child transcript: %w", err)
		}
		last = entry
	}
	if last.Type == "completion" {
		snapshot.State, snapshot.Result, snapshot.Error = last.State, agentJobTail(last.Result), agentJobTail(last.Error)
		if last.OutputNext > 0 {
			snapshot.Output, snapshot.OutputBase, snapshot.OutputNext = last.Output, last.OutputBase, last.OutputNext
		}
		if last.Finished != nil {
			snapshot.Finished = last.Finished
		}
		return nil
	}
	if m.children == nil {
		m.children = make(map[string]*agentChildTranscript)
	}
	m.children[snapshot.ID] = &agentChildTranscript{size: info.Size()}
	return m.appendChildEntriesLocked(snapshot.ID, []agentChildEntry{{Type: "completion", State: snapshot.State, Error: snapshot.Error, Finished: snapshot.Finished, Output: snapshot.Output, OutputBase: snapshot.OutputBase, OutputNext: snapshot.OutputNext}})
}
