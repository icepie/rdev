package server

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"rdev/internal/agent"
)

type agentHistoryHit struct {
	Thread    string `json:"thread"`
	MessageID string `json:"messageId"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
}

func mergeAgentTranscript(existing, messages []agent.Message) []agent.Message {
	index := make(map[string]int, len(existing))
	for i, m := range existing {
		index[m.MessageID] = i
	}
	for _, m := range messages {
		if m.ContextOnly {
			continue
		}
		if m.ArchiveContent != "" {
			m.Content = m.ArchiveContent
		}
		if at, ok := index[m.MessageID]; ok {
			if m.Role == "tool" && m.ArchiveContent == "" {
				m.Content = existing[at].Content
			}
			existing[at] = m
		} else {
			index[m.MessageID] = len(existing)
			existing = append(existing, m)
		}
	}
	return existing
}
func memoryHistoryPage(messages []agent.Message, before, around string, limit int) (agentHistoryPage, error) {
	entries := renderAgentHistory(messages)
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	end := len(entries)
	cursor := before
	if around != "" {
		cursor = around
	}
	if cursor != "" {
		found := false
		for i, e := range entries {
			if e.MessageID == cursor {
				end = i
				if around != "" {
					end = min(len(entries), i+1+limit/2)
				}
				found = true
				break
			}
		}
		if !found {
			return agentHistoryPage{}, errors.New("message not found")
		}
	}
	start := max(0, end-limit)
	page := agentHistoryPage{History: entries[start:end], HasMore: start > 0}
	if len(page.History) > 0 {
		page.Before = page.History[0].MessageID
	}
	return page, nil
}

// Caller holds agentSessionsMu. The archive's current turn is excluded because
// live replay already contains its user, assistant, and tool frames.
func (s *Server) agentTranscriptPageLocked(entry *agentSessionEntry, th *agentThread, before, around string, limit int) (agentHistoryPage, error) {
	if store := s.agentStore(); store != nil {
		store.mu.Lock()
		defer store.mu.Unlock()
		t, err := store.transcriptLocked(entry.deviceID, th.id)
		if err != nil {
			return agentHistoryPage{}, err
		}
		if len(t.refs) == 0 {
			messages := th.session.History()
			if th.running {
				messages = th.turnHistory
			}
			return memoryHistoryPage(messages, before, around, limit)
		}
		if th.running && th.turnMessageID != "" {
			if boundary, ok := t.byID[th.turnMessageID]; ok {
				// Targeted pages may not cross into live replay.
				if around != "" {
					if at, ok := t.byID[around]; ok && at >= boundary {
						return agentHistoryPage{}, errors.New("message is in the running turn")
					}
				}
				if before != "" {
					if at, ok := t.byID[before]; ok && at > boundary {
						return agentHistoryPage{}, errors.New("cursor is in the running turn")
					}
				}
				if before == "" && around == "" {
					before = th.turnMessageID
				}
			}
		}
		page, err := store.transcriptPageLocked(entry.deviceID, th.id, before, around, limit)
		if th.running {
			if boundary, ok := t.byID[th.turnMessageID]; ok {
				for i, e := range page.History {
					if at := t.byID[e.MessageID]; at >= boundary {
						page.History = page.History[:i]
						break
					}
				}
			}
		}
		return page, err
	}
	messages := th.archive
	if len(messages) == 0 {
		messages = th.session.History()
	}
	if th.running {
		for i, m := range messages {
			if m.MessageID == th.turnMessageID {
				messages = messages[:i]
				break
			}
		}
	}
	return memoryHistoryPage(messages, before, around, limit)
}
func (s *Server) completedTranscriptPageLocked(entry *agentSessionEntry, th *agentThread) (agentHistoryPage, error) {
	if store := s.agentStore(); store != nil {
		return store.recentTranscriptPage(entry.deviceID, th.id), nil
	}
	th.archive = mergeAgentTranscript(th.archive, th.session.History())
	return memoryHistoryPage(th.archive, "", "", 50)
}

func (s *Server) recentTranscriptPageLocked(entry *agentSessionEntry, th *agentThread) agentHistoryPage {
	if th.running {
		return th.turnPage
	}
	if store := s.agentStore(); store != nil {
		page := store.recentTranscriptPage(entry.deviceID, th.id)
		if len(page.History) > 0 {
			return page
		}
	}
	messages := th.archive
	if len(messages) == 0 {
		messages = th.session.History()
	}
	page, _ := memoryHistoryPage(messages, "", "", 50)
	return page
}

func (h *agentWSHandler) historyThread(ac *agentConn, msg agentMsg) (*agentSessionEntry, *agentThread, error) {
	if !ac.authed || ac.entry == nil || ac.entry.deviceID != ac.deviceID {
		return nil, nil, errors.New("authenticated device session required")
	}
	thread := msg.Thread
	if thread == "" {
		thread = ac.current
	}
	th := ac.entry.threads[thread]
	if th == nil {
		return nil, nil, errors.New("unknown or inaccessible thread")
	}
	return ac.entry, th, nil
}
func (h *agentWSHandler) handleHistoryPage(ac *agentConn, msg agentMsg) {
	response := agentMsg{Op: "history_page", ID: msg.ID, Thread: msg.Thread, History: []agentHistoryEntry{}}
	h.srv.agentSessionsMu.Lock()
	entry, th, err := h.historyThread(ac, msg)
	if err != nil {
		h.srv.agentSessionsMu.Unlock()
		response.Error = err.Error()
		ac.writeJSON(response)
		return
	}
	h.srv.agentSessionsMu.Unlock()
	th.persistMu.Lock()
	h.srv.agentSessionsMu.Lock()
	valid := entry.threads[th.id] == th
	view := &agentThread{id: th.id, session: th.session, running: th.running, turnMessageID: th.turnMessageID, turnHistory: th.turnHistory, archive: th.archive}
	h.srv.agentSessionsMu.Unlock()
	response.Thread = th.id
	if valid {
		page, pageErr := h.srv.agentTranscriptPageLocked(entry, view, msg.Before, msg.Around, msg.Limit)
		err = pageErr
		response.History, response.HistoryHasMore, response.HistoryBefore = page.History, page.HasMore, page.Before
	} else {
		err = errors.New("thread was deleted")
	}
	th.persistMu.Unlock()
	if err != nil {
		response.Error = err.Error()
	}
	ac.writeJSON(response)
}

func (h *agentWSHandler) handleSearchHistory(ac *agentConn, msg agentMsg) {
	response := agentMsg{Op: "search_history", ID: msg.ID, Thread: msg.Thread, Hits: []agentHistoryHit{}}
	q := strings.TrimSpace(msg.Q)
	if q == "" || len(q) > 512 || !utf8.ValidString(q) {
		response.Error = "query must contain 1–512 UTF-8 bytes"
		ac.writeJSON(response)
		return
	}
	h.srv.agentSessionsMu.Lock()
	if !ac.authed || ac.entry == nil || ac.entry.deviceID != ac.deviceID {
		h.srv.agentSessionsMu.Unlock()
		response.Error = "authenticated device session required"
		ac.writeJSON(response)
		return
	}
	type target struct {
		id, title string
		messages  []agent.Message
	}
	var targets []target
	for _, th := range ac.entry.threads {
		if msg.Thread != "" && msg.Thread != th.id {
			continue
		}
		title := th.title
		if title == "" {
			title = threadTitle(th.session.History())
		}
		item := target{id: th.id, title: title}
		if h.srv.agentStore() == nil {
			item.messages = append([]agent.Message(nil), th.archive...)
			if len(item.messages) == 0 {
				item.messages = th.session.History()
			}
		}
		targets = append(targets, item)
	}
	h.srv.agentSessionsMu.Unlock()
	if msg.Thread != "" && len(targets) == 0 {
		response.Error = "unknown or inaccessible thread"
		ac.writeJSON(response)
		return
	}
	// Scan one bounded record at a time; stop after 50 matches. Unlike a recent
	// context search, this can find the first message of the oldest retained thread.
	for _, target := range targets {
		if len(response.Hits) >= 50 {
			break
		}
		if store := h.srv.agentStore(); store != nil {
			store.mu.Lock()
			t, err := store.transcriptLocked(ac.deviceID, target.id)
			if err != nil {
				store.mu.Unlock()
				response.Error = err.Error()
				break
			}
			f, err := os.Open(store.transcriptPath(ac.deviceID, target.id))
			if os.IsNotExist(err) {
				store.mu.Unlock()
				continue
			}
			if err != nil {
				store.mu.Unlock()
				response.Error = err.Error()
				break
			}
			refs := append([]agentTranscriptRef(nil), t.refs...)
			// The opened descriptor and copied offsets describe an immutable generation.
			// Appends, resets, and other threads may proceed during the bounded reads.
			store.mu.Unlock()
			for i := len(refs) - 1; i >= 0 && len(response.Hits) < 50; i-- {
				ref := refs[i]
				record, err := readTranscriptRecord(f, ref)
				if err != nil {
					response.Error = err.Error()
					break
				}
				if snippet, ok := historySnippet(record.Entry, q); ok {
					response.Hits = append(response.Hits, agentHistoryHit{Thread: target.id, MessageID: record.Entry.MessageID, Title: target.title, Snippet: snippet})
				}
			}
			f.Close()
		} else {
			for _, e := range renderAgentHistory(target.messages) {
				if len(response.Hits) >= 50 {
					break
				}
				if snippet, ok := historySnippet(e, q); ok {
					response.Hits = append(response.Hits, agentHistoryHit{Thread: target.id, MessageID: e.MessageID, Title: target.title, Snippet: snippet})
				}
			}
		}
	}
	ac.writeJSON(response)
}
func historySnippet(e agentHistoryEntry, q string) (string, bool) {
	parts := []string{e.Text, e.Reasoning}
	for _, tool := range e.Tools {
		parts = append(parts, tool.Name, tool.Args, tool.Result, tool.Summary, tool.Diff)
	}
	needle := strings.ToLower(q)
	for _, text := range parts {
		lower := strings.ToLower(text)
		at := strings.Index(lower, needle)
		if at < 0 {
			continue
		}
		// Lowercasing may change UTF-8 byte length; map the match by rune index.
		match := utf8.RuneCountInString(lower[:at])
		startRune, endRune := max(0, match-80), match+utf8.RuneCountInString(q)+160
		start, end, index := 0, len(text), 0
		for offset := range text {
			if index == startRune {
				start = offset
			}
			if index == endRune {
				end = offset
				break
			}
			index++
		}
		return text[start:end], true
	}
	return "", false
}

func (h *agentWSHandler) handleForkThread(ac *agentConn, msg agentMsg) {
	fail := func(err error) {
		ac.writeJSON(agentMsg{Op: "fork_thread", ID: msg.ID, Thread: msg.Thread, Error: err.Error()})
	}
	if msg.MessageID == "" {
		fail(errors.New("messageId is required"))
		return
	}
	if msg.EditText != nil && (len(*msg.EditText) > 1<<20 || !utf8.ValidString(*msg.EditText)) {
		fail(errors.New("draft exceeds 1 MiB or is not UTF-8"))
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	entry, source, err := h.historyThread(ac, msg)
	srv.agentSessionsMu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	source.persistMu.Lock()
	defer source.persistMu.Unlock()
	srv.agentSessionsMu.Lock()
	if entry.threads[source.id] != source {
		srv.agentSessionsMu.Unlock()
		fail(errors.New("source thread was deleted"))
		return
	}
	model, cwd, title := source.model, source.cwd, source.title
	// Frozen pre-turn context bounds fork eligibility while the source is live.
	boundary := source.turnMessageID
	running := source.running
	memory := append([]agent.Message(nil), source.archive...)
	if len(memory) == 0 {
		memory = source.session.History()
	}
	srv.agentSessionsMu.Unlock()
	targetID := newAgentThreadID()
	var context, archive []agent.Message
	store := srv.agentStore()
	if store != nil {
		if err := store.captureTranscript(entry.deviceID, source.id, memory); err != nil {
			fail(err)
			return
		}
	}
	if store != nil {
		if running {
			store.mu.Lock()
			t, e := store.transcriptLocked(entry.deviceID, source.id)
			if e == nil {
				if at, ok := t.byID[msg.MessageID]; ok {
					if current, ok := t.byID[boundary]; ok && at >= current {
						e = errors.New("cannot fork an unfinished turn")
					}
				}
			}
			store.mu.Unlock()
			if e != nil {
				fail(e)
				return
			}
		}
		context, err = store.forkTranscript(entry.deviceID, source.id, targetID, msg.MessageID, msg.EditText != nil)
	} else {
		end := -1
		for i, m := range memory {
			if running && m.MessageID == boundary {
				break
			}
			if m.MessageID == msg.MessageID {
				if msg.EditText != nil {
					if m.Role != "user" {
						err = errors.New("editText requires a user message")
						break
					}
					end = i
				} else {
					end = i + 1
					for end < len(memory) && memory[end].Role == "tool" {
						end++
					}
					err = validateTranscriptBoundary(memory[i:end])
				}
				break
			}
		}
		if end < 0 && err == nil {
			err = errors.New("message not found or unfinished")
		}
		if err == nil {
			archive = append([]agent.Message(nil), memory[:end]...)
			context = archive
		}
	}
	if err != nil {
		if store != nil {
			store.clear(entry.deviceID, targetID)
		}
		fail(err)
		return
	}
	if store != nil {
		store.save(entry.deviceID, targetID, model, context)
		store.setCwd(entry.deviceID, targetID, cwd, context)
		store.setTitle(entry.deviceID, targetID, title, context)
		if fault := store.historyFault(entry.deviceID, targetID); fault != "" {
			fail(fmt.Errorf("persist fork: %s", fault))
			return
		}
	}
	srv.agentSessionsMu.Lock()
	oldModel := entry.model
	entry.model = model
	th := srv.newAgentThreadLocked(entry, targetID, context, cwd, title)
	entry.model = oldModel
	th.archive = archive
	entry.threads[targetID] = th
	entry.current = targetID
	ac.current = targetID
	snap := srv.threadSnapshotLocked(entry, th)
	reply := snap.peekMessage(targetID)
	reply.Op = "thread_switched"
	reply.ID = msg.ID
	reply.Model = th.model
	reply.Draft = msg.EditText
	srv.agentSessionsMu.Unlock()
	ac.writeJSON(reply)
	srv.writeAgentPrefs(entry)
	srv.broadcastThreadList(entry)
	srv.scheduleThreadMetaProbe(entry, targetID)
}
