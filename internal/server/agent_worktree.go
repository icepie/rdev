package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type AgentWorktreeState string

const (
	AgentWorktreeNone     AgentWorktreeState = "none"
	AgentWorktreeCreating AgentWorktreeState = "creating"
	AgentWorktreeReady    AgentWorktreeState = "ready"
	AgentWorktreeRemoving AgentWorktreeState = "removing"
	AgentWorktreeError    AgentWorktreeState = "error"
)

type AgentWorktreeRecord struct {
	Device         string             `json:"device"`
	Thread         string             `json:"thread"`
	Repository     string             `json:"repository"`
	Path           string             `json:"path"`
	Branch         string             `json:"branch"`
	Base           string             `json:"base,omitempty"`
	PreviousCwd    string             `json:"previousCwd,omitempty"`
	PreviousBranch string             `json:"previousBranch,omitempty"`
	PreviousCommit string             `json:"previousCommit,omitempty"`
	State          AgentWorktreeState `json:"state"`
	Error          string             `json:"error,omitempty"`
	CreatedAt      time.Time          `json:"createdAt"`
	UpdatedAt      time.Time          `json:"updatedAt"`
}
type agentWorktreeDisk struct {
	Version int                   `json:"version"`
	Items   []AgentWorktreeRecord `json:"items"`
}

var (
	worktreeFileLocks   sync.Map
	worktreeBaseRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	worktreeDrivePathRE = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

func worktreeStatePath(d string) string   { return filepath.Join(d, "agent", "worktrees.json") }
func worktreeOwnerKey(d, t string) string { return d + "\x00" + t }
func worktreeFileLock(p string) *sync.Mutex {
	v, _ := worktreeFileLocks.LoadOrStore(p, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func loadAgentWorktreeRecords(d string) (map[string]AgentWorktreeRecord, error) {
	m := map[string]AgentWorktreeRecord{}
	if d == "" {
		return m, nil
	}
	b, e := os.ReadFile(worktreeStatePath(d))
	if errors.Is(e, os.ErrNotExist) {
		return m, nil
	}
	if e != nil {
		return nil, e
	}
	var x agentWorktreeDisk
	if e = json.Unmarshal(b, &x); e != nil {
		return nil, fmt.Errorf("decode worktree state: %w", e)
	}
	if x.Version != 0 && x.Version != 1 {
		return nil, fmt.Errorf("unsupported worktree state version %d", x.Version)
	}
	for _, r := range x.Items {
		if r.Device == "" || r.Thread == "" {
			return nil, fmt.Errorf("invalid worktree state owner")
		}
		m[worktreeOwnerKey(r.Device, r.Thread)] = r
	}
	return m, nil
}
func saveAgentWorktreeRecords(d string, m map[string]AgentWorktreeRecord) error {
	if d == "" {
		return fmt.Errorf("worktree persistence unavailable: server data directory is not configured")
	}
	dir := filepath.Join(d, "agent")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	x := agentWorktreeDisk{Version: 1}
	for _, r := range m {
		x.Items = append(x.Items, r)
	}
	b, e := json.MarshalIndent(x, "", "\t")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".worktrees-*.tmp")
	if e != nil {
		return e
	}
	n := f.Name()
	defer os.Remove(n)
	f.Chmod(0600)
	if _, e = f.Write(append(b, '\n')); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(n, worktreeStatePath(d))
}
func (s *Server) agentWorktreeRecords() (map[string]AgentWorktreeRecord, error) {
	p := worktreeStatePath(s.DataDir)
	l := worktreeFileLock(p)
	l.Lock()
	defer l.Unlock()
	return loadAgentWorktreeRecords(s.DataDir)
}
func (s *Server) saveAgentWorktreeRecord(r AgentWorktreeRecord) error {
	p := worktreeStatePath(s.DataDir)
	l := worktreeFileLock(p)
	l.Lock()
	defer l.Unlock()
	m, e := loadAgentWorktreeRecords(s.DataDir)
	if e != nil {
		return e
	}
	m[worktreeOwnerKey(r.Device, r.Thread)] = r
	return saveAgentWorktreeRecords(s.DataDir, m)
}
func (s *Server) deleteAgentWorktreeRecord(d, t string) error {
	p := worktreeStatePath(s.DataDir)
	l := worktreeFileLock(p)
	l.Lock()
	defer l.Unlock()
	m, e := loadAgentWorktreeRecords(s.DataDir)
	if e != nil {
		return e
	}
	delete(m, worktreeOwnerKey(d, t))
	return saveAgentWorktreeRecords(s.DataDir, m)
}
func (s *Server) AgentWorktreeRecord(d, t string) (AgentWorktreeRecord, bool, error) {
	m, e := s.agentWorktreeRecords()
	if e != nil {
		return AgentWorktreeRecord{}, false, e
	}
	r, ok := m[worktreeOwnerKey(d, t)]
	return r, ok, nil
}
func worktreeName(d, t string) string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		copy(b, []byte(fmt.Sprintf("%024x", time.Now().UnixNano())))
	}
	h := sha256.Sum256([]byte(d + "\x00" + t))
	return hex.EncodeToString(h[:6]) + "-" + hex.EncodeToString(b)
}
func worktreeBranchName(d, t string) string {
	h := sha256.Sum256([]byte(d + "\x00" + t))
	return "rdev/worktree/" + hex.EncodeToString(h[:8]) + "/" + hex.EncodeToString(h[8:16])
}
func validateAgentWorktreeBase(b string) error {
	b = strings.TrimSpace(b)
	if b == "" {
		return nil
	}
	if len(b) > 200 || strings.ContainsAny(b, "\x00\r\n") || !worktreeBaseRE.MatchString(b) || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.Contains(b, "..") {
		return fmt.Errorf("invalid base branch %q", b)
	}
	return nil
}
func validateAgentWorktreeCwd(c string) error {
	if c == "" {
		return fmt.Errorf("a working directory is required")
	}
	if strings.ContainsAny(c, "\x00\r\n") || (!strings.HasPrefix(c, "/") && !worktreeDrivePathRE.MatchString(c)) {
		return fmt.Errorf("working directory must be an absolute device path")
	}
	return nil
}
func worktreePathOwned(r AgentWorktreeRecord) bool {
	p := strings.TrimRight(r.Repository, "/") + ".rdev-worktrees/"
	x := strings.TrimPrefix(r.Path, p)
	return r.Device != "" && r.Thread != "" && r.Repository != "" && r.Path != "" && r.Branch == worktreeBranchName(r.Device, r.Thread) && strings.HasPrefix(r.Path, p) && x != "" && x != "." && x != ".." && !strings.ContainsAny(x, "/\\\x00\r\n")
}

type agentGitResult struct {
	stdout, stderr string
	exit           int
}

func splitAgentShellOutput(o string) (string, string) {
	a := "<stdout>\n"
	i := strings.Index(o, a)
	if i < 0 {
		return "", strings.TrimSpace(o)
	}
	i += len(a)
	j := strings.Index(o[i:], "\n</stdout>")
	if j < 0 {
		return strings.TrimSuffix(o[i:], "\n"), ""
	}
	j += i
	k := strings.Index(o[j+len("\n</stdout>"):], "<stderr>\n")
	if k < 0 {
		return strings.TrimSuffix(o[i:j], "\n"), ""
	}
	k += j + len("\n</stdout>") + len("<stderr>\n")
	l := strings.Index(o[k:], "\n</stderr>")
	if l < 0 {
		l = len(o) - k
	}
	return strings.TrimSuffix(o[i:j], "\n"), strings.TrimSuffix(o[k:k+l], "\n")
}
func windowsAgentGitCommand(script string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$git=(Get-Command git -ErrorAction SilentlyContinue).Source; if (-not $git) { exit 127 }; $root=Split-Path (Split-Path $git -Parent) -Parent; $bash=Join-Path $root 'bin\\bash.exe'; if (-not (Test-Path $bash)) { exit 127 }; $s=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + encoded + `')); & $bash -lc $s; exit $LASTEXITCODE"`
}
func worktreeWindowsClient(c *ClientConn) bool {
	return strings.Contains(strings.ToLower(clientOS(c)), "windows")
}
func (s *Server) runAgentGit(ctx context.Context, c *ClientConn, cwd, cmd string) (agentGitResult, error) {
	if c == nil {
		return agentGitResult{}, fmt.Errorf("device is offline: selected device has no active session")
	}
	if e := validateAgentWorktreeCwd(cwd); e != nil {
		return agentGitResult{}, e
	}
	// Cwd is advisory on older clients; enforce it in the actual shell command.
	deviceCwd := cwd
	if worktreeWindowsClient(c) {
		deviceCwd = strings.ReplaceAll(cwd, "\\", "/")
	}
	cmd = "cd -- " + shellQuote(deviceCwd) + " && " + cmd
	if worktreeWindowsClient(c) {
		cmd = windowsAgentGitCommand(cmd)
	}
	r, e := (&shellTool{srv: s, client: c, Cwd: cwd}).runShellReporting(ctx, cmd, nil)
	o, er := splitAgentShellOutput(r.Output)
	if worktreeWindowsClient(c) && r.ExitCode == 127 {
		return agentGitResult{o, er, r.ExitCode}, fmt.Errorf("Windows Git execution unavailable: install Git for Windows with bundled bash.exe")
	}
	return agentGitResult{o, er, r.ExitCode}, e
}
func agentGitError(cmd string, r agentGitResult) error {
	t := strings.TrimSpace(r.stderr)
	if t == "" {
		t = strings.TrimSpace(r.stdout)
	}
	if t == "" {
		t = fmt.Sprintf("git command exited with status %d", r.exit)
	}
	return fmt.Errorf("git operation failed (%s): %s", cmd, t)
}
func (s *Server) selectedAgentWorktreeClient(e *agentSessionEntry) (*ClientConn, error) {
	if e == nil || e.deviceID == "" {
		return nil, fmt.Errorf("selected device session unavailable")
	}
	if e.deviceID == agentServerDeviceID {
		return nil, fmt.Errorf("worktrees unsupported on server device")
	}
	c, ok := s.GetClient(e.deviceID)
	if !ok || c == nil {
		return nil, fmt.Errorf("device %q is offline: reconnect selected device", e.deviceID)
	}
	return c, nil
}
func (s *Server) worktreeThreadUsable(e *agentSessionEntry, t *agentThread) error {
	if e == nil || t == nil || e.threads[t.id] != t {
		return fmt.Errorf("unknown thread")
	}
	if t.running {
		return fmt.Errorf("worktree operation refused while a turn is active; stop the turn first")
	}
	if t.scheduled != nil || t.autonomyPending {
		return fmt.Errorf("worktree operation refused while a turn is scheduled")
	}
	if t.jobs != nil {
		for _, j := range t.jobs.List() {
			if j.State == "running" {
				return fmt.Errorf("worktree operation refused while background job %q is active", j.ID)
			}
		}
	}
	return nil
}
func (s *Server) reserveAgentWorktree(e *agentSessionEntry, t *agentThread) (func(), error) {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	if e == nil || t == nil || e.threads[t.id] != t {
		return nil, fmt.Errorf("unknown thread")
	}
	if e.worktreeBusy {
		return nil, fmt.Errorf("worktree operation already active")
	}
	for _, o := range e.threads {
		if o == nil {
			continue
		}
		if o.running || o.scheduled != nil || o.autonomyPending {
			return nil, fmt.Errorf("worktree operation refused while a turn is active")
		}
		if o.jobs != nil {
			for _, j := range o.jobs.List() {
				if j.State == "running" {
					return nil, fmt.Errorf("worktree operation refused while background job %q is active", j.ID)
				}
			}
		}
	}
	e.worktreeBusy = true
	return func() {
		s.agentSessionsMu.Lock()
		e.worktreeBusy = false
		threads := make([]*agentThread, 0, len(e.threads))
		for _, thread := range e.threads {
			threads = append(threads, thread)
		}
		s.agentSessionsMu.Unlock()
		for _, thread := range threads {
			s.resumeAgentQueue(e, thread)
		}
	}, nil
}

// AgentWorktreeBusyLocked is for central turn/job admission. Caller MUST hold
// agentSessionsMu; parent admission paths should reject while this is true.
func AgentWorktreeBusyLocked(e *agentSessionEntry) bool { return e != nil && e.worktreeBusy }
func (s *Server) worktreePathInUse(e *agentSessionEntry, t *agentThread, p string) bool {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	for id, o := range e.threads {
		if id != t.id && o != nil && (o.cwd == p || strings.HasPrefix(o.cwd, strings.TrimRight(p, "/")+"/")) {
			return true
		}
	}
	return false
}
func (s *Server) sourceActive(e *agentSessionEntry, t *agentThread, p string) bool {
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	for id, o := range e.threads {
		if id == t.id || o == nil || o.cwd != p {
			continue
		}
		if o.running || o.scheduled != nil || o.autonomyPending {
			return true
		}
		if o.jobs != nil {
			for _, j := range o.jobs.List() {
				if j.State == "running" {
					return true
				}
			}
		}
	}
	return false
}
func (s *Server) generatedWorktreeRecord(e *agentSessionEntry, t *agentThread, repo, base string) AgentWorktreeRecord {
	n := time.Now().UTC()
	return AgentWorktreeRecord{Device: e.deviceID, Thread: t.id, Repository: repo, Path: strings.TrimRight(repo, "/") + ".rdev-worktrees/" + worktreeName(e.deviceID, t.id), Branch: worktreeBranchName(e.deviceID, t.id), Base: base, PreviousCwd: t.cwd, State: AgentWorktreeCreating, CreatedAt: n, UpdatedAt: n}
}
func (s *Server) discoverAgentRepository(ctx context.Context, c *ClientConn, cwd string) (string, agentGitResult, error) {
	r, e := s.runAgentGit(ctx, c, cwd, "git rev-parse --show-toplevel")
	return strings.TrimSpace(r.stdout), r, e
}

func (s *Server) switchAgentThreadCwd(e *agentSessionEntry, t *agentThread, cwd string) error {
	if er := validateAgentWorktreeCwd(cwd); er != nil {
		return er
	}
	s.agentSessionsMu.Lock()
	if e.threads[t.id] != t {
		s.agentSessionsMu.Unlock()
		return fmt.Errorf("unknown thread")
	}
	t.cwd = cwd
	if t.deviceScope != nil {
		t.deviceScope.mu.Lock()
		t.deviceScope.members[e.deviceID] = cwd
		t.deviceScope.mu.Unlock()
	}
	t.branch = ""
	t.metaProbeSeq++
	if p, m, er := s.resolveAgentModel(t.model); er == nil {
		ns := s.buildAgentSession(e, t, e.client, p, m)
		ns.SetHistory(t.session.History())
		t.session = ns
		t.sessionClient = e.client
	}
	h := t.session.History()
	d, id := e.deviceID, t.id
	s.agentSessionsMu.Unlock()
	if st := s.agentStore(); st != nil {
		st.setCwd(d, id, cwd, h)
	}
	return nil
}

func (s *Server) AgentWorktreeCreate(ctx context.Context, e *agentSessionEntry, t *agentThread, base string) (AgentWorktreeRecord, error) {
	base = strings.TrimSpace(base)
	if er := validateAgentWorktreeBase(base); er != nil {
		return AgentWorktreeRecord{}, er
	}
	if e == nil || t == nil {
		return AgentWorktreeRecord{}, fmt.Errorf("unknown thread")
	}
	t.persistMu.Lock()
	defer t.persistMu.Unlock()
	s.agentSessionsMu.Lock()
	if er := s.worktreeThreadUsable(e, t); er != nil {
		s.agentSessionsMu.Unlock()
		return AgentWorktreeRecord{}, er
	}
	cwd, dev := agentEffectiveCwdLocked(e, t), e.deviceID
	s.agentSessionsMu.Unlock()
	if er := validateAgentWorktreeCwd(cwd); er != nil {
		return AgentWorktreeRecord{}, er
	}
	c, er := s.selectedAgentWorktreeClient(e)
	if er != nil {
		return AgentWorktreeRecord{}, er
	}
	if old, ok, er := s.AgentWorktreeRecord(dev, t.id); er != nil {
		return AgentWorktreeRecord{}, er
	} else if ok && old.State != AgentWorktreeNone {
		return AgentWorktreeRecord{}, fmt.Errorf("thread already owns worktree %q", old.Path)
	}
	root, r, er := s.discoverAgentRepository(ctx, c, cwd)
	if er != nil {
		return AgentWorktreeRecord{}, er
	}
	root = strings.TrimSpace(root)
	if r.exit != 0 || root == "" {
		return AgentWorktreeRecord{}, agentGitError("discover repository", r)
	}
	br, be := s.runAgentGit(ctx, c, root, "git -C "+shellQuote(root)+" symbolic-ref --quiet --short HEAD")
	if be != nil || br.exit != 0 || strings.TrimSpace(br.stdout) == "" {
		return AgentWorktreeRecord{}, fmt.Errorf("managed worktree requires an attached source branch")
	}
	cr, ce := s.runAgentGit(ctx, c, root, "git -C "+shellQuote(root)+" rev-parse --verify HEAD^{commit}")
	if ce != nil || cr.exit != 0 {
		return AgentWorktreeRecord{}, fmt.Errorf("cannot determine source branch commit")
	}
	rec := s.generatedWorktreeRecord(e, t, root, base)
	rec.PreviousCwd = cwd
	rec.PreviousBranch = strings.TrimSpace(br.stdout)
	rec.PreviousCommit = strings.TrimSpace(cr.stdout)
	baseArg := "HEAD"
	if base != "" {
		baseArg = "refs/heads/" + base
		v, ve := s.runAgentGit(ctx, c, cwd, "git -C "+shellQuote(root)+" check-ref-format --branch "+shellQuote(base)+" && git -C "+shellQuote(root)+" rev-parse --verify "+shellQuote(baseArg)+"^{commit}")
		if ve != nil || v.exit != 0 {
			if ve != nil {
				return rec, ve
			}
			return rec, agentGitError("validate base branch", v)
		}
	}
	if er = s.saveAgentWorktreeRecord(rec); er != nil {
		return AgentWorktreeRecord{}, er
	}
	cmd := "git -C " + shellQuote(root) + " worktree add -b " + shellQuote(rec.Branch) + " " + shellQuote(rec.Path) + " " + shellQuote(baseArg)
	r, er = s.runAgentGit(ctx, c, cwd, cmd)
	if er != nil || r.exit != 0 {
		rec.State, rec.Error, rec.UpdatedAt = AgentWorktreeError, "unable to create managed worktree", time.Now().UTC()
		_ = s.saveAgentWorktreeRecord(rec)
		if er != nil {
			return rec, er
		}
		return rec, agentGitError("create worktree", r)
	}
	rec.State, rec.Error, rec.UpdatedAt = AgentWorktreeReady, "", time.Now().UTC()
	if er = s.saveAgentWorktreeRecord(rec); er != nil {
		return rec, er
	}
	if er = s.switchAgentThreadCwd(e, t, rec.Path); er != nil {
		return rec, er
	}
	return rec, nil
}

func (s *Server) AgentWorktreeStatus(ctx context.Context, e *agentSessionEntry, t *agentThread) (AgentWorktreeRecord, error) {
	if e == nil || t == nil {
		return AgentWorktreeRecord{}, fmt.Errorf("unknown thread")
	}
	r, ok, er := s.AgentWorktreeRecord(e.deviceID, t.id)
	if er != nil {
		return AgentWorktreeRecord{}, er
	}
	if !ok {
		return AgentWorktreeRecord{Device: e.deviceID, Thread: t.id, State: AgentWorktreeNone}, nil
	}
	if r.State == AgentWorktreeCreating || r.State == AgentWorktreeRemoving || r.State == AgentWorktreeError {
		return s.AgentWorktreeRecover(ctx, e, t)
	}
	if r.State != AgentWorktreeReady {
		return r, nil
	}
	if !worktreePathOwned(r) {
		return r, fmt.Errorf("managed worktree state has an unsafe path")
	}
	c, er := s.selectedAgentWorktreeClient(e)
	if er != nil {
		return r, er
	}
	v, ve := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" worktree list --porcelain")
	if ve != nil {
		return r, ve
	}
	if v.exit != 0 {
		return r, agentGitError("status worktree", v)
	}
	if !strings.Contains(v.stdout, "worktree "+r.Path+"\n") {
		return r, fmt.Errorf("managed worktree %q is no longer registered", r.Path)
	}
	return r, nil
}

func agentWorktreeRefsPreserve(out, branch, commit string) bool {
	managed := "refs/heads/" + branch
	for _, x := range strings.Split(out, "\n") {
		x = strings.TrimSpace(x)
		if x != "" && x != managed && x != commit {
			return true
		}
	}
	return false
}
func (s *Server) AgentWorktreeRemove(ctx context.Context, e *agentSessionEntry, t *agentThread) (AgentWorktreeRecord, error) {
	if e == nil || t == nil {
		return AgentWorktreeRecord{}, fmt.Errorf("unknown thread")
	}
	t.persistMu.Lock()
	defer t.persistMu.Unlock()
	s.agentSessionsMu.Lock()
	if er := s.worktreeThreadUsable(e, t); er != nil {
		s.agentSessionsMu.Unlock()
		return AgentWorktreeRecord{}, er
	}
	d, id := e.deviceID, t.id
	s.agentSessionsMu.Unlock()
	r, ok, er := s.AgentWorktreeRecord(d, id)
	if er != nil {
		return AgentWorktreeRecord{}, er
	}
	if !ok || r.State == AgentWorktreeNone {
		return AgentWorktreeRecord{Device: d, Thread: id, State: AgentWorktreeNone}, nil
	}
	if r.State != AgentWorktreeReady {
		return r, fmt.Errorf("worktree is not ready (state %s)", r.State)
	}
	if !worktreePathOwned(r) {
		return r, fmt.Errorf("managed worktree state has an unsafe path")
	}
	if s.worktreePathInUse(e, t, r.Path) {
		return r, fmt.Errorf("refusing to remove worktree while another thread uses its working directory")
	}
	c, er := s.selectedAgentWorktreeClient(e)
	if er != nil {
		return r, er
	}
	branch, branchErr := s.runAgentGit(ctx, c, r.Path, "git symbolic-ref --quiet --short HEAD")
	if branchErr != nil || branch.exit != 0 || strings.TrimSpace(branch.stdout) != r.Branch {
		return r, fmt.Errorf("refusing to remove: managed checkout branch changed")
	}
	st, se := s.runAgentGit(ctx, c, r.Path, "git status --porcelain=v1 --untracked-files=all")
	if se != nil {
		return r, se
	}
	if st.exit != 0 {
		return r, agentGitError("inspect worktree", st)
	}
	if strings.TrimSpace(st.stdout) != "" {
		return r, fmt.Errorf("refusing to remove dirty or untracked worktree")
	}
	cm, ce := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" rev-parse --verify "+shellQuote(r.Branch)+"^{commit}")
	if ce != nil {
		return r, ce
	}
	if cm.exit != 0 {
		return r, agentGitError("inspect managed branch", cm)
	}
	commit := strings.TrimSpace(cm.stdout)
	refs, re := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" for-each-ref --contains "+shellQuote(commit)+" --format='%(refname)'")
	if re != nil {
		return r, re
	}
	if refs.exit != 0 {
		return r, agentGitError("inspect preserving refs", refs)
	}
	if !agentWorktreeRefsPreserve(refs.stdout, r.Branch, commit) {
		return r, fmt.Errorf("refusing to remove commit %s: no non-managed ref preserves it", commit)
	}
	r.State, r.UpdatedAt = AgentWorktreeRemoving, time.Now().UTC()
	if er = s.saveAgentWorktreeRecord(r); er != nil {
		return r, er
	}
	rm, re := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" worktree remove -- "+shellQuote(r.Path))
	if re != nil || rm.exit != 0 {
		r.State, r.Error, r.UpdatedAt = AgentWorktreeError, "unable to remove managed worktree", time.Now().UTC()
		_ = s.saveAgentWorktreeRecord(r)
		if re != nil {
			return r, re
		}
		return r, agentGitError("remove worktree", rm)
	}
	bd, re := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" branch -d -- "+shellQuote(r.Branch))
	if re != nil || bd.exit != 0 {
		r.State, r.Error, r.UpdatedAt = AgentWorktreeError, "worktree removed but managed branch remains", time.Now().UTC()
		_ = s.saveAgentWorktreeRecord(r)
		if re != nil {
			return r, re
		}
		return r, agentGitError("remove managed branch", bd)
	}
	prev := r.PreviousCwd
	if prev != "" {
		if er = s.switchAgentThreadCwd(e, t, prev); er != nil {
			return r, er
		}
	}
	if er = s.deleteAgentWorktreeRecord(d, id); er != nil {
		return r, er
	}
	r.State, r.Error = AgentWorktreeNone, ""
	return r, nil
}
func (s *Server) AgentWorktreeMerge(ctx context.Context, e *agentSessionEntry, t *agentThread) (AgentWorktreeRecord, error) {
	if e == nil || t == nil {
		return AgentWorktreeRecord{}, fmt.Errorf("unknown thread")
	}
	t.persistMu.Lock()
	defer t.persistMu.Unlock()
	s.agentSessionsMu.Lock()
	if er := s.worktreeThreadUsable(e, t); er != nil {
		s.agentSessionsMu.Unlock()
		return AgentWorktreeRecord{}, er
	}
	d, id := e.deviceID, t.id
	s.agentSessionsMu.Unlock()
	r, ok, er := s.AgentWorktreeRecord(d, id)
	if er != nil {
		return AgentWorktreeRecord{}, er
	}
	if !ok || r.State != AgentWorktreeReady {
		return r, fmt.Errorf("thread has no ready managed worktree")
	}
	if !worktreePathOwned(r) || r.PreviousCwd == "" || r.PreviousBranch == "" || r.PreviousCommit == "" {
		return r, fmt.Errorf("managed worktree metadata is incomplete or unsafe")
	}
	if s.sourceActive(e, t, r.PreviousCwd) {
		return r, fmt.Errorf("refusing to merge while another thread has active work in original checkout")
	}
	c, er := s.selectedAgentWorktreeClient(e)
	if er != nil {
		return r, er
	}
	managedBranch, branchErr := s.runAgentGit(ctx, c, r.Path, "git symbolic-ref --quiet --short HEAD")
	if branchErr != nil || managedBranch.exit != 0 || strings.TrimSpace(managedBranch.stdout) != r.Branch {
		return r, fmt.Errorf("refusing to merge: managed checkout branch changed")
	}
	st, se := s.runAgentGit(ctx, c, r.PreviousCwd, "git status --porcelain=v1 --untracked-files=all")
	if se != nil {
		return r, se
	}
	if st.exit != 0 {
		return r, agentGitError("inspect original checkout", st)
	}
	if strings.TrimSpace(st.stdout) != "" {
		return r, fmt.Errorf("refusing to merge: original checkout has dirty or untracked files")
	}
	br, be := s.runAgentGit(ctx, c, r.PreviousCwd, "git -C "+shellQuote(r.Repository)+" symbolic-ref --quiet --short HEAD")
	if be != nil || br.exit != 0 || strings.TrimSpace(br.stdout) != r.PreviousBranch {
		return r, fmt.Errorf("refusing to merge: original checkout branch changed")
	}
	cm, ce := s.runAgentGit(ctx, c, r.PreviousCwd, "git -C "+shellQuote(r.Repository)+" rev-parse --verify HEAD^{commit}")
	if ce != nil || cm.exit != 0 || strings.TrimSpace(cm.stdout) != r.PreviousCommit {
		return r, fmt.Errorf("refusing to merge: original branch advanced or rewound")
	}
	ms, me := s.runAgentGit(ctx, c, r.Path, "git status --porcelain=v1 --untracked-files=all")
	if me != nil {
		return r, me
	}
	if ms.exit != 0 {
		return r, agentGitError("inspect managed checkout", ms)
	}
	if strings.TrimSpace(ms.stdout) != "" {
		return r, fmt.Errorf("refusing to merge: managed worktree has dirty or untracked files")
	}
	mg, mge := s.runAgentGit(ctx, c, r.PreviousCwd, "git -C "+shellQuote(r.Repository)+" merge --ff-only -- "+shellQuote(r.Branch))
	if mge != nil {
		return r, mge
	}
	if mg.exit != 0 {
		return r, fmt.Errorf("fast-forward merge refused: %s", strings.TrimSpace(mg.stderr))
	}
	// The managed checkout remains usable after a merge. Advance the source
	// checkpoint so a later batch of commits can be merged without treating
	// this successful merge as an external source-branch mutation.
	cm, ce = s.runAgentGit(ctx, c, r.PreviousCwd, "git -C "+shellQuote(r.Repository)+" rev-parse --verify HEAD^{commit}")
	if ce != nil || cm.exit != 0 || strings.TrimSpace(cm.stdout) == "" {
		return r, fmt.Errorf("merge completed but source checkpoint failed")
	}
	r.PreviousCommit = strings.TrimSpace(cm.stdout)
	r.UpdatedAt = time.Now().UTC()
	if er := s.saveAgentWorktreeRecord(r); er != nil {
		return r, fmt.Errorf("merge completed but worktree checkpoint failed: %w", er)
	}
	return r, nil
}
func (s *Server) AgentWorktreeRecover(ctx context.Context, e *agentSessionEntry, t *agentThread) (AgentWorktreeRecord, error) {
	if e == nil || t == nil {
		return AgentWorktreeRecord{}, fmt.Errorf("unknown thread")
	}
	t.persistMu.Lock()
	defer t.persistMu.Unlock()
	r, ok, er := s.AgentWorktreeRecord(e.deviceID, t.id)
	if er != nil || !ok {
		return AgentWorktreeRecord{Device: e.deviceID, Thread: t.id, State: AgentWorktreeNone}, er
	}
	if r.State == AgentWorktreeReady {
		return r, nil
	}
	if r.State != AgentWorktreeCreating && r.State != AgentWorktreeRemoving && r.State != AgentWorktreeError {
		return r, nil
	}
	if !worktreePathOwned(r) {
		return r, fmt.Errorf("persisted worktree has unsafe path")
	}
	c, er := s.selectedAgentWorktreeClient(e)
	if er != nil {
		return r, er
	}
	v, ve := s.runAgentGit(ctx, c, r.Repository, "git -C "+shellQuote(r.Repository)+" worktree list --porcelain")
	if ve != nil {
		return r, ve
	}
	if v.exit != 0 {
		return r, agentGitError("recover worktree", v)
	}
	if strings.Contains(v.stdout, "worktree "+r.Path+"\n") {
		wasCreating := r.State == AgentWorktreeCreating
		r.State, r.Error = AgentWorktreeReady, ""
		r.UpdatedAt = time.Now().UTC()
		if er := s.saveAgentWorktreeRecord(r); er != nil {
			return r, er
		}
		if wasCreating {
			return r, s.switchAgentThreadCwd(e, t, r.Path)
		}
		return r, nil
	}
	// Never remove an unregistered directory or force-delete an unmerged ref.
	// A crash after worktree removal may leave only its branch and ownership.
	cleanup := "if test -e " + shellQuote(r.Path) + " || test -L " + shellQuote(r.Path) +
		"; then echo 'unregistered worktree path still exists' >&2; exit 1; fi; " +
		"git show-ref --verify --quiet " + shellQuote("refs/heads/"+r.Branch) +
		"; status=$?; if test $status -eq 0; then git branch -d -- " + shellQuote(r.Branch) +
		"; elif test $status -ne 1; then exit $status; fi"
	cleaned, err := s.runAgentGit(ctx, c, r.Repository, cleanup)
	if err != nil {
		return r, err
	}
	if cleaned.exit != 0 {
		return r, agentGitError("recover managed worktree", cleaned)
	}
	s.agentSessionsMu.Lock()
	restoreCwd := t.cwd == r.Path
	s.agentSessionsMu.Unlock()
	if restoreCwd {
		if err := s.switchAgentThreadCwd(e, t, r.PreviousCwd); err != nil {
			return r, err
		}
	}
	if err := s.deleteAgentWorktreeRecord(e.deviceID, t.id); err != nil {
		return r, err
	}
	r.State, r.Error = AgentWorktreeNone, ""
	return r, nil
}
func (s *Server) AgentWorktreeDeletionGuard(ctx context.Context, e *agentSessionEntry, t *agentThread) error {
	if e == nil || t == nil {
		return fmt.Errorf("unknown thread")
	}
	r, ok, er := s.AgentWorktreeRecord(e.deviceID, t.id)
	if er != nil {
		return er
	}
	if ok && r.State != AgentWorktreeNone {
		return fmt.Errorf("thread owns managed worktree %q; remove it explicitly before deleting the thread", r.Path)
	}
	return nil
}
func (h *agentWSHandler) handleWorktree(ac *agentConn, msg agentMsg) {
	thread := msg.Thread
	if thread == "" {
		thread = ac.current
	}
	out := agentMsg{Op: "worktree", ID: msg.ID, Thread: thread, State: string(AgentWorktreeError)}
	if ac == nil || ac.entry == nil {
		out.Error = "agent session is not ready"
		if ac != nil {
			ac.writeJSON(out)
		}
		return
	}
	s := h.srv
	s.agentSessionsMu.Lock()
	t := ac.entry.threads[thread]
	s.agentSessionsMu.Unlock()
	if t == nil {
		out.Error = "unknown thread"
		ac.writeJSON(out)
		return
	}
	if msg.Action != "create" && msg.Action != "status" && msg.Action != "remove" && msg.Action != "merge" {
		out.Error = "unknown worktree action"
		ac.writeJSON(out)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s.goAgent(func() {
		defer cancel()
		release, er := s.reserveAgentWorktree(ac.entry, t)
		if er != nil {
			out.Error = er.Error()
			ac.writeJSON(out)
			return
		}
		defer release()
		var r AgentWorktreeRecord
		switch msg.Action {
		case "create":
			r, er = s.AgentWorktreeCreate(ctx, ac.entry, t, msg.Base)
		case "status":
			r, er = s.AgentWorktreeStatus(ctx, ac.entry, t)
		case "remove":
			r, er = s.AgentWorktreeRemove(ctx, ac.entry, t)
		case "merge":
			r, er = s.AgentWorktreeMerge(ctx, ac.entry, t)
		}
		reply := agentMsg{Op: "worktree", ID: msg.ID, Thread: thread, State: string(r.State), Path: r.Path, Branch: r.Branch, Repository: r.Repository}
		if reply.State == "" {
			reply.State = string(AgentWorktreeError)
		}
		if er != nil {
			reply.Error = er.Error()
		}
		if msg.Action == "merge" && er == nil {
			reply.Message = "fast-forward merge completed; managed worktree retained"
		}
		ac.writeJSON(reply)
		if er == nil {
			s.agentSessionsMu.Lock()
			cwd, branch := agentEffectiveCwdLocked(ac.entry, t), t.branch
			s.agentSessionsMu.Unlock()
			s.agentBroadcast(ac.entry, agentMsg{Op: "thread_meta", Thread: thread, Cwd: cwd, Branch: branch})
			s.scheduleThreadMetaProbe(ac.entry, thread)
		}
	})
}
