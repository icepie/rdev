package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"rdev/internal/protocol"
)

const (
	defaultClientLogDir                   = "data/client-logs"
	defaultClientLogRetention             = 7 * 24 * time.Hour
	defaultClientLogMaxFileSize     int64 = 32 * 1024 * 1024
	defaultClientLogMaxLineBytes          = 8 * 1024
	defaultClientLogFlushIntervalMs       = 1000
)

type clientLogConfig struct {
	Enabled         bool      `json:"enabled"`
	Level           string    `json:"level"`
	SampleRate      float64   `json:"sampleRate"`
	MaxLineBytes    int       `json:"maxLineBytes"`
	FlushIntervalMs int       `json:"flushIntervalMs"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ClientLogManager struct {
	mu            sync.RWMutex
	dir           string
	retention     time.Duration
	maxFileSize   int64
	deviceConfigs map[string]clientLogConfig
}

func NewClientLogManager(dir string, retention time.Duration, maxFileSize int64) *ClientLogManager {
	if strings.TrimSpace(dir) == "" {
		dir = defaultClientLogDir
	}
	if retention <= 0 {
		retention = defaultClientLogRetention
	}
	if maxFileSize <= 0 {
		maxFileSize = defaultClientLogMaxFileSize
	}
	return &ClientLogManager{dir: dir, retention: retention, maxFileSize: maxFileSize, deviceConfigs: make(map[string]clientLogConfig)}
}

func (m *ClientLogManager) StartCleanup() {
	go func() {
		m.cleanupOnce()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			m.cleanupOnce()
		}
	}()
}

func (m *ClientLogManager) configFor(device string) clientLogConfig {
	m.mu.RLock()
	cfg, ok := m.deviceConfigs[device]
	m.mu.RUnlock()
	if !ok {
		return clientLogConfig{Level: "info", SampleRate: 1, MaxLineBytes: defaultClientLogMaxLineBytes, FlushIntervalMs: defaultClientLogFlushIntervalMs}
	}
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 1
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = defaultClientLogMaxLineBytes
	}
	if cfg.FlushIntervalMs <= 0 {
		cfg.FlushIntervalMs = defaultClientLogFlushIntervalMs
	}
	return cfg
}

func (m *ClientLogManager) setConfig(device string, cfg clientLogConfig) clientLogConfig {
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	cfg.Level = normalizeLogLevel(cfg.Level)
	if cfg.SampleRate <= 0 || cfg.SampleRate > 1 {
		cfg.SampleRate = 1
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = defaultClientLogMaxLineBytes
	}
	if cfg.FlushIntervalMs <= 0 {
		cfg.FlushIntervalMs = defaultClientLogFlushIntervalMs
	}
	cfg.UpdatedAt = time.Now()
	m.mu.Lock()
	m.deviceConfigs[device] = cfg
	m.mu.Unlock()
	return cfg
}

func normalizeLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func logLevelRank(level string) int {
	switch normalizeLogLevel(level) {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn":
		return 3
	case "error":
		return 4
	default:
		return 2
	}
}

func serverLogLevelAllowed(level, min string) bool {
	return logLevelRank(level) >= logLevelRank(min)
}

func safeClientLogDeviceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "device"
	}
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" || out == "." || out == ".." {
		return "device"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func (m *ClientLogManager) appendBatch(device, version string, entries []protocol.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	safe := safeClientLogDeviceID(device)
	dir := filepath.Join(m.dir, safe)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	day := time.Now().Format("2006-01-02")
	path, err := m.nextWritablePath(dir, day)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if e.Timestamp == "" {
			e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
		}
		e.Level = normalizeLogLevel(e.Level)
		if e.Message == "" {
			continue
		}
		rec := map[string]interface{}{"ts": e.Timestamp, "device": device, "level": e.Level, "client": version, "module": firstNonEmpty(e.Module, e.Target), "msg": e.Message}
		if len(e.Fields) > 0 {
			rec["fields"] = e.Fields
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (m *ClientLogManager) nextWritablePath(dir, day string) (string, error) {
	for i := 0; i < 1000; i++ {
		name := day + ".log"
		if i > 0 {
			name = fmt.Sprintf("%s.%d.log", day, i)
		}
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return path, nil
			}
			return "", err
		}
		if st.Size() < m.maxFileSize {
			return path, nil
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s.%d.log", day, time.Now().UnixNano())), nil
}

func (m *ClientLogManager) cleanupOnce() {
	cutoff := time.Now().Add(-m.retention)
	filepath.WalkDir(m.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == m.dir {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				log.Printf("client log cleanup remove %s: %v", path, err)
			}
		}
		return nil
	})
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
		}
	}
}

func (m *ClientLogManager) logDeviceIDs() []string {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func (m *ClientLogManager) deviceStats(device string) (size int64, latest time.Time) {
	dir := filepath.Join(m.dir, safeClientLogDeviceID(device))
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		size += st.Size()
		if st.ModTime().After(latest) {
			latest = st.ModTime()
		}
		return nil
	})
	return
}

func (m *ClientLogManager) tail(device string, lines int) ([]string, error) {
	if lines <= 0 || lines > 5000 {
		lines = 300
	}
	files, err := m.logFiles(device, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(files) - 1; i >= 0 && len(out) < lines; i-- {
		chunk, _ := readLastLines(files[i], lines-len(out))
		out = append(chunk, out...)
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}

func (m *ClientLogManager) logFiles(device, date string) ([]string, error) {
	dir := filepath.Join(m.dir, safeClientLogDeviceID(device))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if date == "" || strings.HasPrefix(name, date+".") || name == date+".log" {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

func readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	ring := make([]string, 0, n)
	for scanner.Scan() {
		if len(ring) == n {
			copy(ring, ring[1:])
			ring[n-1] = scanner.Text()
		} else {
			ring = append(ring, scanner.Text())
		}
	}
	return ring, scanner.Err()
}

func (s *Server) sendClientLogConfig(client *ClientConn) {
	if client == nil || !client.LogSupported || s.ClientLogs == nil {
		return
	}
	cfg := s.ClientLogs.configFor(client.ID)
	_ = client.Send(&protocol.Message{Type: protocol.MsgLogConfig, LogEnabled: cfg.Enabled, LogLevel: cfg.Level, SampleRate: cfg.SampleRate, MaxLineBytes: cfg.MaxLineBytes, FlushIntervalMs: cfg.FlushIntervalMs})
}

func (s *Server) handleClientLogBatch(client *ClientConn, msg *protocol.Message) {
	if s.ClientLogs == nil || client == nil {
		return
	}
	cfg := s.ClientLogs.configFor(client.ID)
	if !cfg.Enabled {
		return
	}
	entries := make([]protocol.LogEntry, 0, len(msg.Logs))
	for _, e := range msg.Logs {
		if !serverLogLevelAllowed(e.Level, cfg.Level) {
			continue
		}
		if cfg.MaxLineBytes > 0 && len(e.Message) > cfg.MaxLineBytes {
			e.Message = e.Message[:cfg.MaxLineBytes]
		}
		entries = append(entries, e)
	}
	if err := s.ClientLogs.appendBatch(client.ID, client.Version, entries); err != nil {
		log.Printf("client log append failed device=%s: %v", client.ID, err)
	}
}

func (s *Server) HandleClientLogsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if s.ClientLogs == nil {
		s.ClientLogs = NewClientLogManager("", 0, 0)
	}
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/client-logs")
	if path == "/config" && r.Method == http.MethodGet {
		s.handleClientLogsConfig(w, r)
		return
	}
	if path == "/devices" && r.Method == http.MethodGet {
		s.handleClientLogsDevices(w, r)
		return
	}
	if strings.HasPrefix(path, "/devices/") {
		s.handleClientLogsDevice(w, r, strings.TrimPrefix(path, "/devices/"))
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleClientLogsConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"dir": s.ClientLogs.dir, "retentionSeconds": int(s.ClientLogs.retention.Seconds()), "maxFileSize": s.ClientLogs.maxFileSize, "defaultLevel": "info"})
}

func (s *Server) handleClientLogsDevices(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID        string `json:"id"`
		Version   string `json:"version,omitempty"`
		Connected bool   `json:"connected"`
		Supported bool   `json:"supported"`
		Enabled   bool   `json:"enabled"`
		Level     string `json:"level"`
		Size      int64  `json:"size"`
		Latest    string `json:"latest,omitempty"`
	}
	seen := map[string]bool{}
	var out []item
	s.mu.RLock()
	for _, c := range s.clients {
		cfg := s.ClientLogs.configFor(c.ID)
		size, latest := s.ClientLogs.deviceStats(c.ID)
		it := item{ID: c.ID, Version: c.Version, Connected: true, Supported: c.LogSupported, Enabled: cfg.Enabled, Level: cfg.Level, Size: size}
		if !latest.IsZero() {
			it.Latest = latest.Format(time.RFC3339)
		}
		out = append(out, it)
		seen[c.ID] = true
	}
	s.mu.RUnlock()
	s.ClientLogs.mu.RLock()
	for id, cfg := range s.ClientLogs.deviceConfigs {
		if !seen[id] {
			size, latest := s.ClientLogs.deviceStats(id)
			it := item{ID: id, Enabled: cfg.Enabled, Level: cfg.Level, Size: size}
			if !latest.IsZero() {
				it.Latest = latest.Format(time.RFC3339)
			}
			out = append(out, it)
		}
	}
	s.ClientLogs.mu.RUnlock()
	for _, id := range s.ClientLogs.logDeviceIDs() {
		if !seen[id] {
			size, latest := s.ClientLogs.deviceStats(id)
			it := item{ID: id, Level: "info", Size: size}
			if !latest.IsZero() {
				it.Latest = latest.Format(time.RFC3339)
			}
			out = append(out, it)
			seen[id] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleClientLogsDevice(w http.ResponseWriter, r *http.Request, rest string) {
	device, action, _ := strings.Cut(rest, "/")
	device, _ = url.PathUnescape(device)
	switch {
	case action == "config" && r.Method == http.MethodPost:
		var req clientLogConfig
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
		cfg := s.ClientLogs.setConfig(device, req)
		if c := s.clientByID(device); c != nil {
			s.sendClientLogConfig(c)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	case action == "tail" && r.Method == http.MethodGet:
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		lines, err := s.ClientLogs.tail(device, n)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"device": device, "lines": lines})
	case action == "download" && r.Method == http.MethodGet:
		date := r.URL.Query().Get("date")
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		files, err := s.ClientLogs.logFiles(device, date)
		if err != nil || len(files) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s.log\"", safeClientLogDeviceID(device), date))
		for _, f := range files {
			in, _ := os.Open(f)
			if in != nil {
				_, _ = io.Copy(w, in)
				in.Close()
			}
		}
	case action == "" && r.Method == http.MethodDelete:
		err := os.RemoveAll(filepath.Join(s.ClientLogs.dir, safeClientLogDeviceID(device)))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}
