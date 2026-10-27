package server

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// originOK guards WebSocket upgrades against cross-site WebSocket hijacking
// (browsers send Origin but do not enforce CORS on WS upgrades). Non-browser
// clients (CLI, curl) send no Origin header and are allowed; when Origin is
// present its host must match the request Host exactly.
func originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// requireOrigin rejects WebSocket upgrade requests with a foreign Origin.
func requireOrigin(w http.ResponseWriter, r *http.Request) bool {
	if originOK(r) {
		return true
	}
	http.Error(w, "forbidden origin", http.StatusForbidden)
	return false
}

// authRateLimiter throttles per-device password guessing. Failures impose an
// exponentially growing cooldown (capped at authFailMaxDelay) during which new
// attempts are rejected without even comparing the password; a successful auth
// clears the state.
type authRateLimiter struct {
	mu     sync.Mutex
	states map[string]*authFailState
	now    func() time.Time // injectable for tests
}

type authFailState struct {
	failures int
	nextAt   time.Time
}

const (
	authFailBaseDelay = 200 * time.Millisecond
	authFailMaxDelay  = 5 * time.Second
)

func newAuthRateLimiter() *authRateLimiter {
	return &authRateLimiter{states: make(map[string]*authFailState), now: time.Now}
}

// wait returns the remaining cooldown before the next auth attempt for
// deviceID is allowed; 0 means an attempt may proceed.
func (l *authRateLimiter) wait(deviceID string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.states[deviceID]
	if st == nil {
		return 0
	}
	if d := st.nextAt.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

// fail records a failed auth attempt and returns the cooldown now in effect.
func (l *authRateLimiter) fail(deviceID string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.states[deviceID]
	if st == nil {
		st = &authFailState{}
		l.states[deviceID] = st
	}
	st.failures++
	shift := st.failures - 1
	if shift > 5 { // 200ms << 5 already exceeds the cap; avoid int64 overflow
		shift = 5
	}
	delay := authFailBaseDelay << shift
	if delay > authFailMaxDelay {
		delay = authFailMaxDelay
	}
	st.nextAt = l.now().Add(delay)
	return delay
}

// success clears the failure state for deviceID after a successful auth.
func (l *authRateLimiter) success(deviceID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.states, deviceID)
}
