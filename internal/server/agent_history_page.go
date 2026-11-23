package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rdev/internal/agent"
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
			ID:         threadID,
			Title:      title,
			MetaTitle:  metaTitle,
			Model:      model,
			Cwd:        cwd,
			UpdatedAt:  updated,
			Outcome:    st.extras[deviceID+"/"+threadID].Outcome,
			OutcomeAt:  st.extras[deviceID+"/"+threadID].OutcomeAt,
			Diagnostic: st.extras[deviceID+"/"+threadID].Diagnostic.Safe(),
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
