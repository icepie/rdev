package client

import (
	"os"
	"path/filepath"
	"rdev/internal/protocol"
	"sync"
	"time"
)

const directoryCacheBytes = 32 << 20
const directoryCacheTTL = 10 * time.Second

type directorySnapshot struct {
	entries      []protocol.FileEntry
	modified, at time.Time
	bytes        int
}
type fileListingCache struct {
	mu         sync.Mutex
	entries    map[string]directorySnapshot
	generation uint64
}

func (c *fileListingCache) invalidate() { c.mu.Lock(); c.entries = nil; c.generation++; c.mu.Unlock() }
func (c *fileListingCache) list(path string, offset int64, limit int) ([]protocol.FileEntry, int, error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, 0, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	c.mu.Lock()
	generation := c.generation
	for key, entry := range c.entries {
		if now.Sub(entry.at) >= directoryCacheTTL {
			delete(c.entries, key)
		}
	}
	snapshot, ok := c.entries[key]
	c.mu.Unlock()
	if offset <= 0 || !ok || !snapshot.modified.Equal(stat.ModTime()) {
		entries, _, err := listFileEntries(path, 0, 0)
		if err != nil {
			return nil, 0, err
		}
		bytes := 0
		for _, entry := range entries {
			bytes += len(entry.Path) + len(entry.Name) + len(entry.ModTime) + 80
		}
		snapshot = directorySnapshot{entries: entries, modified: stat.ModTime(), at: now, bytes: bytes}
		c.mu.Lock()
		if c.generation == generation {
			delete(c.entries, key)
			if bytes <= directoryCacheBytes {
				if c.entries == nil {
					c.entries = make(map[string]directorySnapshot)
				}
				for {
					total := bytes
					oldestKey := ""
					oldest := now
					for k, e := range c.entries {
						total += e.bytes
						if oldestKey == "" || e.at.Before(oldest) {
							oldestKey = k
							oldest = e.at
						}
					}
					if len(c.entries) < 8 && total <= directoryCacheBytes {
						break
					}
					delete(c.entries, oldestKey)
				}
				c.entries[key] = snapshot
			}
		}
		c.mu.Unlock()
	}
	total := len(snapshot.entries)
	start := int(min(max(offset, 0), int64(total)))
	end := total
	if limit > 0 && limit < total-start {
		end = start + limit
	}
	return append([]protocol.FileEntry(nil), snapshot.entries[start:end]...), total, nil
}
