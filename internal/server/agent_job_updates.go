package server

import "sync"

// Job updates invalidate snapshots, so changes can be merged while a browser
// write is in flight. One worker and one pending bit bound the work per manager;
// callers never wait for socket writes or sidebar history scans.
type agentJobUpdates struct {
	mu      sync.Mutex
	running bool
	pending bool
	launch  func(func())
	publish func()
}

func (u *agentJobUpdates) Notify() {
	u.mu.Lock()
	u.pending = true
	if u.running {
		u.mu.Unlock()
		return
	}
	u.running = true
	u.mu.Unlock()
	u.launch(u.run)
}

func (u *agentJobUpdates) run() {
	for {
		u.mu.Lock()
		if !u.pending {
			u.running = false
			u.mu.Unlock()
			return
		}
		u.pending = false
		u.mu.Unlock()
		u.publish()
	}
}
