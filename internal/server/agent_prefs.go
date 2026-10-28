package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Per-device agent UI preferences, persisted as one small JSON file at
// <dataDir>/agent/prefs/<device>.json (unlike history, prefs are kept
// regardless of AgentSessionTTL). Writes are atomic (tmp + rename).
type agentPrefs struct {
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	ReasoningLevel string `json:"reasoningLevel,omitempty"`
	CurrentThread  string `json:"currentThread,omitempty"`
	// OS is the last known device OS, kept so the global thread list can
	// show an OS icon for devices that are currently offline.
	OS string `json:"os,omitempty"`
}

func (s *Server) agentPrefsPath(deviceID string) string {
	return filepath.Join(s.DataDir, "agent", "prefs", sanitizeAgentHistoryName(deviceID)+".json")
}

// loadAgentPrefs reads the persisted prefs for a device; any error yields
// zero prefs (fresh defaults).
func (s *Server) loadAgentPrefs(deviceID string) agentPrefs {
	var p agentPrefs
	if s.DataDir == "" {
		return p
	}
	data, err := os.ReadFile(s.agentPrefsPath(deviceID))
	if err != nil {
		return p
	}
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("agent prefs: parse %s: %v", deviceID, err)
		return agentPrefs{}
	}
	return p
}

// saveAgentPrefs atomically persists prefs for a device.
func (s *Server) saveAgentPrefs(deviceID string, p agentPrefs) error {
	if s.DataDir == "" {
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := s.agentPrefsPath(deviceID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeAgentPrefs snapshots the entry's current prefs and persists them.
// Callers must NOT hold agentSessionsMu.
func (s *Server) writeAgentPrefs(entry *agentSessionEntry) {
	if s.DataDir == "" || entry == nil {
		return
	}
	s.agentSessionsMu.Lock()
	p := agentPrefs{
		Model:          entry.model,
		PermissionMode: entry.permMode,
		ReasoningLevel: entry.reasoningLevel,
		CurrentThread:  entry.current,
		OS:             entry.os,
	}
	deviceID := entry.deviceID
	s.agentSessionsMu.Unlock()
	if err := s.saveAgentPrefs(deviceID, p); err != nil {
		log.Printf("agent prefs: save %s: %v", deviceID, err)
	}
}
