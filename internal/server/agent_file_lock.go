package server

import (
	"context"
	"path"
	"strings"
	"sync"
)

// agentFileMutations serializes Agent read/modify/write transactions on the
// same device path, while unrelated files and devices remain independent.
// Waits are cancelable; idle keys are removed to bound the registry's lifetime.
type agentFileMutations struct {
	mu    sync.Mutex
	paths map[string]*agentFileMutation
}
type agentFileMutation struct {
	token chan struct{}
	refs  int
}

func agentMutationKey(device, goos, file string) string {
	if goos == "windows" {
		file = strings.ToLower(strings.ReplaceAll(file, `\`, "/"))
	}
	return device + "\x00" + path.Clean(file)
}

func (m *agentFileMutations) Acquire(ctx context.Context, device, goos, file string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := agentMutationKey(device, goos, file)
	m.mu.Lock()
	if m.paths == nil {
		m.paths = make(map[string]*agentFileMutation)
	}
	entry := m.paths[key]
	if entry == nil {
		entry = &agentFileMutation{token: make(chan struct{}, 1)}
		m.paths[key] = entry
	}
	entry.refs++
	m.mu.Unlock()
	dropRef := func() {
		m.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(m.paths, key)
		}
		m.mu.Unlock()
	}
	select {
	case entry.token <- struct{}{}:
		var once sync.Once
		release := func() { once.Do(func() { <-entry.token; dropRef() }) }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		dropRef()
		return nil, ctx.Err()
	}
}
