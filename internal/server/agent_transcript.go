package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rdev/internal/agent"
)

// Transcripts retain all original records until explicit reset/deletion or the
// session TTL. Quotas refuse new writes; they never evict old conversation data.
const (
	agentTranscriptMaxBytes    = 256 << 20
	agentTranscriptMaxEntries  = 100000
	agentTranscriptRecordBytes = 32 << 20 // rendered entry plus fork-safe raw messages
	agentTranscriptPageBytes   = 32 << 20
)

type agentTranscriptRecord struct {
	Entry    agentHistoryEntry  `json:"entry"`
	Messages []agentHistoryLine `json:"messages"`
}
type agentTranscriptRef struct {
	id     string
	offset int64
	size   int
	hash   [32]byte
}
type agentTranscript struct {
	refs   []agentTranscriptRef
	byID   map[string]int
	size   int64
	recent []agentHistoryEntry
}
type agentHistoryPage struct {
	History []agentHistoryEntry
	HasMore bool
	Before  string
}

func (st *agentSessionStore) transcriptPath(device, thread string) string {
	return strings.TrimSuffix(st.path(device, thread), ".jsonl") + ".transcript"
}

// Deterministic migration survives a crash between archive creation and context
// rewrite. Existing IDs are preserved, including fork ancestry.
func assignLegacyMessageIDs(device, thread string, messages []agent.Message) {
	for i := range messages {
		if messages[i].MessageID != "" {
			continue
		}
		data := marshalAgentHistoryMsg(messages[i])
		sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00", device, thread, i)), data...))
		messages[i].MessageID = "m_" + hex.EncodeToString(sum[:16])
	}
}

func (st *agentSessionStore) transcriptLocked(device, thread string) (*agentTranscript, error) {
	key := device + "/" + thread
	if t := st.transcripts[key]; t != nil {
		return t, nil
	}
	t := &agentTranscript{byID: make(map[string]int)}
	f, err := os.Open(st.transcriptPath(device, thread))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if info.Size() > agentTranscriptMaxBytes {
			return nil, errors.New("transcript exceeds 256 MiB quota")
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), agentTranscriptRecordBytes+1)
		for scanner.Scan() {
			data := scanner.Bytes()
			var record agentTranscriptRecord
			if err := json.Unmarshal(data, &record); err != nil {
				return nil, fmt.Errorf("damaged transcript: %w", err)
			}
			if record.Entry.MessageID == "" {
				return nil, errors.New("transcript record has no identity")
			}
			ref := agentTranscriptRef{id: record.Entry.MessageID, offset: t.size, size: len(data), hash: sha256.Sum256(data)}
			if at, ok := t.byID[ref.id]; ok {
				t.refs[at] = ref
			} else {
				if len(t.refs) >= agentTranscriptMaxEntries {
					return nil, errors.New("transcript exceeds 100000 entry quota")
				}
				t.byID[ref.id] = len(t.refs)
				t.refs = append(t.refs, ref)
			}
			t.size += int64(len(data) + 1)
			t.cacheEntry(record.Entry)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		// An incomplete append must never be treated as a valid archive boundary.
		if t.size != info.Size() {
			return nil, errors.New("transcript has an incomplete final record")
		}
	}
	st.publishTranscriptPage(device, thread, t)
	if st.transcripts == nil {
		st.transcripts = make(map[string]*agentTranscript)
	}
	st.transcripts[key] = t
	return t, nil
}

func (st *agentSessionStore) captureTranscript(device, thread string, messages []agent.Message) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.captureTranscriptLocked(device, thread, messages)
}

func (st *agentSessionStore) captureTranscriptLocked(device, thread string, messages []agent.Message) error {
	t, err := st.transcriptLocked(device, thread)
	if err != nil {
		return err
	}
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for i := range messages {
		m := messages[i]
		if m.ContextOnly || m.Role == "tool" {
			continue
		}
		end := i + 1
		for end < len(messages) && messages[end].Role == "tool" {
			end++
		}
		group := append([]agent.Message(nil), messages[i:end]...)
		// Context reloads contain shortened tool results. An annotation/re-save may
		// update presentation, but must never replace the archived original output.
		if at, ok := t.byID[m.MessageID]; ok {
			oldFile, err := os.Open(st.transcriptPath(device, thread))
			if err != nil {
				return err
			}
			old, err := readTranscriptRecord(oldFile, t.refs[at])
			oldFile.Close()
			if err != nil {
				return err
			}
			for j := range group {
				if j < len(old.Messages) && group[j].Role == "tool" && group[j].ArchiveContent == "" {
					group[j].Content = old.Messages[j].Content
				}
			}
		}
		for j := range group {
			if group[j].ArchiveContent != "" {
				group[j].Content = group[j].ArchiveContent
			}
		}
		rendered := renderAgentHistory(group)
		if len(rendered) == 0 {
			continue
		}
		if m.MessageID == "" {
			return errors.New("cannot archive message without identity")
		}
		record := agentTranscriptRecord{Entry: rendered[0]}
		for _, msg := range group {
			var line agentHistoryLine
			if err := json.Unmarshal(marshalAgentHistoryMsg(msg), &line); err != nil {
				return err
			}
			record.Messages = append(record.Messages, line)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(data) > agentTranscriptRecordBytes {
			return errors.New("transcript record exceeds 32 MiB quota")
		}
		hash := sha256.Sum256(data)
		at, exists := t.byID[m.MessageID]
		if exists && t.refs[at].hash == hash {
			continue
		}
		if t.size+int64(len(data)+1) > agentTranscriptMaxBytes || (!exists && len(t.refs) >= agentTranscriptMaxEntries) {
			return errors.New("durable transcript quota reached (256 MiB / 100000 entries); export or delete this thread before continuing")
		}
		if f == nil {
			path := st.transcriptPath(device, thread)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return err
			}
		}
		data = append(data, '\n')
		n, err := f.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			delete(st.transcripts, device+"/"+thread)
			return err
		}
		ref := agentTranscriptRef{id: m.MessageID, offset: t.size, size: len(data) - 1, hash: hash}
		if exists {
			t.refs[at] = ref
		} else {
			t.byID[m.MessageID] = len(t.refs)
			t.refs = append(t.refs, ref)
		}
		t.size += int64(len(data))
		t.cacheEntry(record.Entry)
	}
	if f != nil {
		if err := f.Sync(); err != nil {
			delete(st.transcripts, device+"/"+thread)
			return err
		}
	}
	st.publishTranscriptPage(device, thread, t)
	return nil
}

func readTranscriptRecord(f *os.File, ref agentTranscriptRef) (agentTranscriptRecord, error) {
	var record agentTranscriptRecord
	data := make([]byte, ref.size)
	if _, err := f.ReadAt(data, ref.offset); err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, nil
}

func (st *agentSessionStore) transcriptPage(device, thread, before, around string, limit int) (agentHistoryPage, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.transcriptPageLocked(device, thread, before, around, limit)
}
func (st *agentSessionStore) transcriptPageLocked(device, thread, before, around string, limit int) (agentHistoryPage, error) {
	page := agentHistoryPage{History: []agentHistoryEntry{}}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	t, err := st.transcriptLocked(device, thread)
	if err != nil {
		return page, err
	}
	end := len(t.refs)
	if before != "" {
		at, ok := t.byID[before]
		if !ok {
			return page, errors.New("unknown history cursor")
		}
		end = at
	}
	target := -1
	if around != "" {
		at, ok := t.byID[around]
		if !ok {
			return page, errors.New("message not found")
		}
		target = at
		end = min(len(t.refs), at+1+limit/2)
	}
	start := max(0, end-limit)
	// Keep the requested target, even when neighboring records exceed a page's byte budget.
	if target >= 0 {
		start = target
		end = target + 1
	}
	used := 0
	for i := end - 1; i >= start; i-- {
		if used+t.refs[i].size > agentTranscriptPageBytes {
			start = i + 1
			break
		}
		used += t.refs[i].size
	}
	if target >= 0 {
		used = t.refs[target].size
		for start > 0 && end-start < limit && used+t.refs[start-1].size <= agentTranscriptPageBytes {
			start--
			used += t.refs[start].size
		}
		for end < len(t.refs) && end-start < limit && used+t.refs[end].size <= agentTranscriptPageBytes {
			used += t.refs[end].size
			end++
		}
	}
	if start == end {
		return page, nil
	}
	f, err := os.Open(st.transcriptPath(device, thread))
	if err != nil {
		return page, err
	}
	defer f.Close()
	for _, ref := range t.refs[start:end] {
		record, err := readTranscriptRecord(f, ref)
		if err != nil {
			return page, err
		}
		page.History = append(page.History, record.Entry)
	}
	page.HasMore = start > 0
	page.Before = t.refs[start].id
	return page, nil
}

func messageFromTranscriptLine(line agentHistoryLine) agent.Message {
	m := agent.Message{MessageID: line.MessageID, ContextOnly: line.ContextOnly, Role: line.Role, Content: line.Content, Reasoning: line.Reasoning, ToolCallID: line.ToolCallID, Display: line.Display, Responses: line.Responses, Anthropic: line.Anthropic}
	for i, name := range line.Images {
		image := agent.MessageImage{Name: name}
		if i < len(line.ImagePreviews) {
			image.Preview = line.ImagePreviews[i]
		}
		m.Images = append(m.Images, image)
	}
	for _, tc := range line.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, agent.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments, Summary: tc.Summary, Diff: tc.Diff, IsError: tc.IsError})
	}
	return m
}

func (st *agentSessionStore) forkTranscript(device, source, target, messageID string, edit bool) ([]agent.Message, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, err := st.transcriptLocked(device, source)
	if err != nil {
		return nil, err
	}
	at, ok := t.byID[messageID]
	if !ok {
		return nil, errors.New("message not found")
	}
	f, err := os.Open(st.transcriptPath(device, source))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	selected, err := readTranscriptRecord(f, t.refs[at])
	if err != nil {
		return nil, err
	}
	if edit && selected.Entry.Role != "user" {
		return nil, errors.New("editText is allowed only for a user message")
	}
	end := at + 1
	if edit {
		end = at
	}
	var context []agent.Message
	var total int
	for _, ref := range t.refs[:end] {
		record, err := readTranscriptRecord(f, ref)
		if err != nil {
			return nil, err
		}
		group := make([]agent.Message, 0, len(record.Messages))
		for _, line := range record.Messages {
			group = append(group, messageFromTranscriptLine(line))
		}
		if err := validateTranscriptBoundary(group); err != nil {
			return nil, err
		}
		if err := st.captureTranscriptLocked(device, target, group); err != nil {
			return nil, err
		}
		// Model context is bounded independently; the new archive retains the full prefix.
		context = append(context, group...)
		total += ref.size
		for len(context) > agentHistoryKeepMessages || total > agentHistoryMaxBytes {
			if len(context) <= len(group) {
				break
			}
			cut := 1
			for cut < len(context) && context[cut].Role == "tool" {
				cut++
			}
			context = context[cut:]
			total = 0
			for _, m := range context {
				total += len(marshalAgentHistoryMsg(m))
			}
		}
	}
	return context, nil
}
func validateTranscriptBoundary(group []agent.Message) error {
	if len(group) == 0 {
		return nil
	}
	if group[0].Role == "tool" {
		return errors.New("cannot fork at an orphan tool result")
	}
	expected := make(map[string]bool)
	for _, tc := range group[0].ToolCalls {
		expected[tc.ID] = true
	}
	for _, m := range group[1:] {
		if m.Role != "tool" || !expected[m.ToolCallID] {
			return errors.New("incomplete tool boundary")
		}
		delete(expected, m.ToolCallID)
	}
	if len(expected) != 0 {
		return errors.New("cannot fork before all tool results are complete")
	}
	return nil
}

func (t *agentTranscript) cacheEntry(entry agentHistoryEntry) {
	found := false
	for i, e := range t.recent {
		if e.MessageID == entry.MessageID {
			t.recent[i] = entry
			found = true
			break
		}
	}
	if !found {
		if t.byID[entry.MessageID] != len(t.refs)-1 {
			return
		}
		t.recent = append(t.recent, entry)
	}
	if len(t.recent) > 50 {
		t.recent = append([]agentHistoryEntry(nil), t.recent[len(t.recent)-50:]...)
	}
	// Record sizes conservatively bound rendered cache memory without re-encoding.
	total := 0
	for i := len(t.recent) - 1; i >= 0; i-- {
		total += t.refs[t.byID[t.recent[i].MessageID]].size
		if total > agentTranscriptPageBytes {
			t.recent = append([]agentHistoryEntry(nil), t.recent[i+1:]...)
			break
		}
	}
}
func (st *agentSessionStore) publishTranscriptPage(device, thread string, t *agentTranscript) {
	page := agentHistoryPage{History: append([]agentHistoryEntry(nil), t.recent...), HasMore: len(t.refs) > len(t.recent)}
	if len(page.History) > 0 {
		page.Before = page.History[0].MessageID
	}
	st.recentTranscripts.Store(device+"/"+thread, page)
}
func (st *agentSessionStore) recentTranscriptPage(device, thread string) agentHistoryPage {
	if value, ok := st.recentTranscripts.Load(device + "/" + thread); ok {
		return value.(agentHistoryPage)
	}
	return agentHistoryPage{History: []agentHistoryEntry{}}
}
