package client

import (
	"bytes"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"rdev/internal/protocol"
)

type clientLogCollector struct {
	mu            sync.Mutex
	ring          []protocol.LogEntry
	enabled       bool
	level         string
	maxLineBytes  int
	flushInterval time.Duration
	pending       chan protocol.LogEntry
	client        *Client
}

func newClientLogCollector() *clientLogCollector {
	return &clientLogCollector{level: "info", maxLineBytes: 8192, flushInterval: time.Second, pending: make(chan protocol.LogEntry, 2048)}
}

func (c *clientLogCollector) install(client *Client) {
	c.client = client
	log.SetOutput(io.MultiWriter(os.Stderr, c))
	go c.loop()
}

func (c *clientLogCollector) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}
	entry := protocol.LogEntry{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Level: "info", Target: "go", Module: "client", Message: sanitizeClientLog(msg)}
	c.mu.Lock()
	if c.maxLineBytes > 0 && len(entry.Message) > c.maxLineBytes {
		entry.Message = entry.Message[:c.maxLineBytes]
	}
	c.ring = append(c.ring, entry)
	if len(c.ring) > 1000 {
		copy(c.ring, c.ring[len(c.ring)-1000:])
		c.ring = c.ring[:1000]
	}
	enabled := c.enabled && logLevelAllowed(entry.Level, c.level)
	c.mu.Unlock()
	if enabled {
		select {
		case c.pending <- entry:
		default:
		}
	}
	return len(p), nil
}

func (c *clientLogCollector) configure(msg *protocol.Message) {
	c.mu.Lock()
	c.enabled = msg.LogEnabled
	c.level = normalizeClientLogLevel(msg.LogLevel)
	if msg.MaxLineBytes > 0 {
		c.maxLineBytes = msg.MaxLineBytes
	}
	if msg.FlushIntervalMs > 0 {
		c.flushInterval = time.Duration(msg.FlushIntervalMs) * time.Millisecond
	}
	snapshot := append([]protocol.LogEntry(nil), c.ring...)
	c.mu.Unlock()
	if msg.LogEnabled {
		go c.sendBatch(snapshot)
	}
}

func (c *clientLogCollector) loop() {
	currentInterval := c.currentFlushInterval()
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()
	var batch []protocol.LogEntry
	for {
		select {
		case e := <-c.pending:
			batch = append(batch, e)
			if len(batch) >= 50 {
				c.sendBatch(batch)
				batch = nil
			}
		case <-ticker.C:
			if len(batch) > 0 {
				c.sendBatch(batch)
				batch = nil
			}
			if next := c.currentFlushInterval(); next != currentInterval {
				currentInterval = next
				ticker.Reset(currentInterval)
			}
		}
	}
}

func (c *clientLogCollector) currentFlushInterval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flushInterval <= 0 {
		return time.Second
	}
	return c.flushInterval
}

func (c *clientLogCollector) sendBatch(entries []protocol.LogEntry) {
	if len(entries) == 0 || c.client == nil {
		return
	}
	c.mu.Lock()
	enabled := c.enabled
	level := c.level
	c.mu.Unlock()
	if !enabled {
		return
	}
	filtered := entries[:0]
	for _, e := range entries {
		if logLevelAllowed(e.Level, level) {
			filtered = append(filtered, e)
		}
	}
	if len(filtered) == 0 {
		return
	}
	_ = c.client.send(&protocol.Message{Type: protocol.MsgLogBatch, Logs: filtered})
}

func normalizeClientLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func logLevelAllowed(level, min string) bool { return logRank(level) >= logRank(min) }
func logRank(level string) int {
	switch normalizeClientLogLevel(level) {
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

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|token|authorization|cookie|secret|key)=([^\s,;]+)`),
	regexp.MustCompile(`(?i)(--password|-p)\s+([^\s]+)`),
}

func sanitizeClientLog(s string) string {
	s = strings.TrimSpace(s)
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, `$1=[redacted]`)
	}
	s = strings.ReplaceAll(s, "\x00", "")
	return string(bytes.TrimSpace([]byte(s)))
}
