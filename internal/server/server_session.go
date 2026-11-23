package server

import (
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// ProxySession represents a proxied SSH session (shell/exec/sftp)
type ProxySession struct {
	ID         string
	ClientID   string
	terminal   *terminalConn
	WriteCh    chan []byte // client device -> SSH stdout / terminal
	StderrCh   chan []byte // client device -> SSH stderr
	CloseCh    chan struct{}
	Done       chan struct{}
	closeOnce  sync.Once
	outputOnce sync.Once
	exitCode   int
	exitDone   chan struct{} // closed when exit code is set
	exitMu     sync.Mutex
	CloseSSH   func()
	ExitSSH    func(code int)

	// Session management metadata
	createdAt time.Time
	pty       bool
	term      string
	rows      int
	cols      int
	command   string // original command (empty for shell)
	subsystem string // "", "sftp"

	// Recent output history for late session attach viewers.
	historyMu    sync.Mutex
	history      [][]byte
	historyBytes int

	// Observers for session attach.
	obsMu     sync.RWMutex
	observers map[string]*sessionObserver // id -> observer
}

type sessionObserver struct {
	id       string
	writeCh  chan []byte // copy of session output -> observer
	stderrCh chan []byte // copy of session stderr -> observer
	done     chan struct{}
	once     sync.Once
}

func (o *sessionObserver) close() {
	o.once.Do(func() { close(o.done) })
}

func (s *ProxySession) SignalClose() {
	s.closeOnce.Do(func() { close(s.CloseCh) })
}

func (s *ProxySession) CloseOutput() {
	s.outputOnce.Do(func() {
		close(s.WriteCh)
		close(s.StderrCh)
	})
}

func (s *ProxySession) SetExitCode(code int) {
	s.exitMu.Lock()
	s.exitCode = code
	s.exitMu.Unlock()
	// Signal that exit code is available
	select {
	case <-s.exitDone:
		// already closed
	default:
		close(s.exitDone)
	}
}

func (s *ProxySession) GetExitCode() int {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitCode
}

// WaitExitCode blocks until an exit code is set or timeout expires
func (s *ProxySession) WaitExitCode(timeout time.Duration) int {
	select {
	case <-s.exitDone:
		s.exitMu.Lock()
		code := s.exitCode
		s.exitMu.Unlock()
		return code
	case <-time.After(timeout):
		return -1
	}
}

// --- Observer (session attach) support ---

// AddObserver registers an observer that receives a copy of all session output.
func (s *ProxySession) AddObserver(id string) (history [][]byte, writeCh, stderrCh <-chan []byte, done <-chan struct{}) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	if s.observers == nil {
		s.observers = make(map[string]*sessionObserver)
	}
	obs := &sessionObserver{
		id:       id,
		writeCh:  make(chan []byte, 4096),
		stderrCh: make(chan []byte, 1024),
		done:     make(chan struct{}),
	}
	s.observers[id] = obs
	if len(s.history) > 0 {
		history = make([][]byte, len(s.history))
		copy(history, s.history)
	}
	return history, obs.writeCh, obs.stderrCh, obs.done
}

func (s *ProxySession) recordHistoryLocked(data []byte) {
	if len(data) == 0 || sessionHistoryLimit <= 0 {
		return
	}
	chunk := data
	if len(chunk) > sessionHistoryLimit {
		chunk = chunk[len(chunk)-sessionHistoryLimit:]
	}
	copyChunk := make([]byte, len(chunk))
	copy(copyChunk, chunk)

	s.history = append(s.history, copyChunk)
	s.historyBytes += len(copyChunk)
	for s.historyBytes > sessionHistoryLimit && len(s.history) > 0 {
		s.historyBytes -= len(s.history[0])
		s.history[0] = nil
		s.history = s.history[1:]
	}
}

func (s *ProxySession) HistorySnapshot() [][]byte {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if len(s.history) == 0 {
		return nil
	}
	snapshot := make([][]byte, len(s.history))
	copy(snapshot, s.history)
	return snapshot
}

// RemoveObserver unregisters an observer.
func (s *ProxySession) RemoveObserver(id string) {
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	if obs, ok := s.observers[id]; ok {
		obs.close()
		close(obs.writeCh)
		close(obs.stderrCh)
		delete(s.observers, id)
	}
}

// BroadcastOutput sends session output to all observers.
func (s *ProxySession) BroadcastOutput(data []byte) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.recordHistoryLocked(data)
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		select {
		case obs.writeCh <- data:
		default:
			// drop if observer is slow
		}
	}
}

// BroadcastStderr sends session stderr to all observers.
func (s *ProxySession) BroadcastStderr(data []byte) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.recordHistoryLocked(data)
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		select {
		case obs.stderrCh <- data:
		default:
		}
	}
}

// NotifyObserversClose signals all observers that the session is closing.
func (s *ProxySession) NotifyObserversClose() {
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	for _, obs := range s.observers {
		obs.close()
	}
}

// ObserverCount returns the number of observers.
func (s *ProxySession) ObserverCount() int {
	s.obsMu.RLock()
	defer s.obsMu.RUnlock()
	return len(s.observers)
}

// SetSessionMeta stores session creation metadata.
func (s *ProxySession) SetSessionMeta(pty bool, term, command, subsystem string, rows, cols int) {
	s.pty = pty
	s.term = term
	s.command = command
	s.subsystem = subsystem
	s.rows = rows
	s.cols = cols
	s.createdAt = time.Now()
}

// SessionMeta returns session metadata for API listing.
func (s *ProxySession) SessionMeta() (pty bool, term, command, subsystem string, rows, cols int, createdAt time.Time) {
	return s.pty, s.term, s.command, s.subsystem, s.rows, s.cols, s.createdAt
}

// ProxyForward represents a proxied TCP connection (port forwarding)
type ProxyForward struct {
	ID         string
	ClientID   string
	WriteCh    chan []byte
	CloseCh    chan struct{}
	OpenCh     chan struct{}
	Done       chan struct{}
	closeOnce  sync.Once
	outputOnce sync.Once
	openOnce   sync.Once
	failMu     sync.Mutex
	failErr    string
	CloseSSH   func()
}

func (f *ProxyForward) SignalOpen() {
	if f.OpenCh == nil {
		return
	}
	f.openOnce.Do(func() { close(f.OpenCh) })
}

func (f *ProxyForward) SignalFail(errText string) {
	f.failMu.Lock()
	f.failErr = errText
	f.failMu.Unlock()
	f.SignalOpen()
}

func (f *ProxyForward) FailError() string {
	f.failMu.Lock()
	defer f.failMu.Unlock()
	return f.failErr
}

func (f *ProxyForward) SignalClose() {
	f.closeOnce.Do(func() { close(f.CloseCh) })
}

func (f *ProxyForward) CloseOutput() {
	f.outputOnce.Do(func() { close(f.WriteCh) })
}

type ReverseForward struct {
	ID       string
	ClientID string
	BindAddr string
	BindPort uint32
	SSHConn  *gossh.ServerConn
	OpenCh   chan struct{}
	Cancel   func()

	mu      sync.Mutex
	port    uint32
	errText string
	once    sync.Once
}

func (f *ReverseForward) SignalOpen(port uint32) {
	f.mu.Lock()
	f.port = port
	f.mu.Unlock()
	f.once.Do(func() { close(f.OpenCh) })
}

func (f *ReverseForward) SignalFail(errText string) {
	f.mu.Lock()
	f.errText = errText
	f.mu.Unlock()
	f.once.Do(func() { close(f.OpenCh) })
}

func (f *ReverseForward) Result() (uint32, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.port, f.errText
}
