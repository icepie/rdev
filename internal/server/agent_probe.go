package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"rdev/internal/protocol"
)

// scheduleThreadMetaProbe probes a thread's git branch in the background
// (no locks held while the device round-trip runs) and broadcasts the
// thread_meta snapshot once done, followed by a changes push with the
// working-tree status.
func (s *Server) scheduleThreadMetaProbe(entry *agentSessionEntry, threadID string) {
	s.goAgent(func() {
		s.agentSessionsMu.Lock()
		initial := entry.threads[threadID]
		needsDefault := initial != nil && agentEffectiveCwdLocked(entry, initial) == ""
		s.agentSessionsMu.Unlock()
		if needsDefault {
			s.awaitAgentDefaultCwd(entry)
		}
		s.agentSessionsMu.Lock()
		th := entry.threads[threadID]
		if th == nil {
			s.agentSessionsMu.Unlock()
			return
		}
		cwd := agentEffectiveCwdLocked(entry, th)
		th.metaProbeSeq++
		probeSeq := th.metaProbeSeq
		s.agentSessionsMu.Unlock()

		branch := ""
		if cwd != "" {
			branch = s.probeGitBranch(entry.deviceID, cwd)
		}

		s.agentSessionsMu.Lock()
		th = entry.threads[threadID]
		if th == nil || th.metaProbeSeq != probeSeq || agentEffectiveCwdLocked(entry, th) != cwd {
			s.agentSessionsMu.Unlock()
			return
		}
		th.branch = branch
		s.agentSessionsMu.Unlock()
		s.agentBroadcast(entry, agentMsg{Op: "thread_meta", Thread: threadID, Cwd: cwd, Branch: branch})

		// Push the working-tree status along with every meta refresh (turn
		// done, set_cwd, switch_thread, auth_ok) so the changes panel stays
		// current without polling.
		changes := agentChangesMsg{Op: "changes", Thread: threadID, Cwd: cwd, Branch: branch, Files: []agentChangeFile{}}
		if cwd == "" {
			changes.Error = "no working directory"
		} else if files, err := s.probeGitChanges(entry.deviceID, cwd); err != nil {
			changes.Error = err.Error()
		} else {
			changes.Files = files
		}
		s.agentSessionsMu.Lock()
		th = entry.threads[threadID]
		current := th != nil && th.metaProbeSeq == probeSeq && agentEffectiveCwdLocked(entry, th) == cwd
		s.agentSessionsMu.Unlock()
		if !current {
			return
		}
		if data, err := json.Marshal(changes); err == nil {
			s.agentBroadcastData(entry, data)
		}
	})
}

// probeAgentDefaultCwd discovers the device client's own working directory
// (pwd / cd) once per entry and backfills thread_meta for the current
// thread. Runs off-lock; failures leave defaultCwd empty.
func (s *Server) probeAgentDefaultCwd(entry *agentSessionEntry) {
	if s.awaitAgentDefaultCwd(entry) == "" {
		return
	}
	s.agentSessionsMu.Lock()
	current := entry.current
	s.agentSessionsMu.Unlock()
	s.scheduleThreadMetaProbe(entry, current)
}

// awaitAgentDefaultCwd runs off-lock and shares one bounded probe among cold
// panel requests. Failures are not cached, so reconnects can retry without a
// model turn. A failed command's stdout must never become a working directory.
func (s *Server) awaitAgentDefaultCwd(entry *agentSessionEntry) string {
	s.agentSessionsMu.Lock()
	if entry.defaultCwd != "" || entry.deviceID == agentServerDeviceID {
		cwd := entry.defaultCwd
		s.agentSessionsMu.Unlock()
		return cwd
	}
	if pending := entry.defaultCwdProbe; pending != nil {
		s.agentSessionsMu.Unlock()
		<-pending
		s.agentSessionsMu.Lock()
		cwd := entry.defaultCwd
		s.agentSessionsMu.Unlock()
		return cwd
	}
	pending := make(chan struct{})
	entry.defaultCwdProbe = pending
	s.agentSessionsMu.Unlock()
	cwd := ""
	if client, ok := s.GetClient(entry.deviceID); ok {
		command := "pwd"
		if clientOS(client) == "windows" {
			command = "cd"
		}
		out, code, err := s.runDeviceProbe(client, command, 5*time.Second, 4096)
		if err == nil && code == 0 {
			cwd, _, _ = strings.Cut(out, "\n")
			cwd = strings.TrimSpace(cwd)
		}
	}
	s.agentSessionsMu.Lock()
	if entry.defaultCwd == "" {
		entry.defaultCwd = cwd
	}
	cwd = entry.defaultCwd
	entry.defaultCwdProbe = nil
	close(pending)
	s.agentSessionsMu.Unlock()
	return cwd
}

// probeGitBranch asks a device for the current git branch of cwd; "" when
// offline, not a repo, or the probe fails. The client connection is always
// fetched fresh (entry.client goes stale on device reconnects).
func (s *Server) probeGitBranch(deviceID, cwd string) string {
	client, ok := s.GetClient(deviceID)
	if !ok || cwd == "" {
		return ""
	}
	out, _, err := s.runDeviceProbe(client, gitBranchProbeCommand(clientOS(client), cwd), agentGitProbeTimeout, 4096)
	if err != nil {
		return ""
	}
	branch, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(branch)
}

// Git probe tuning: branch/status probes are quick, diffs may be large and
// are capped (truncation marker included by cappedBuffer).
const (
	agentGitProbeTimeout = 5 * time.Second
	agentGitDiffTimeout  = 30 * time.Second
	agentGitStatusCap    = 64 * 1024
	agentGitDiffCap      = 200 * 1024
)

// errGitNotRepo marks "cwd is not a git repository" (or the status/diff
// command failed); errGitUntracked marks a diff requested for a file git
// does not track.
var (
	errGitNotRepo   = errors.New("not a git repository")
	errGitUntracked = errors.New("untracked")
)

// probeGitChanges asks a device for the working-tree status of cwd and
// parses the porcelain output. The client connection is always fetched
// fresh (entry.client goes stale on device reconnects); runs off-lock.
func (s *Server) probeGitChanges(deviceID, cwd string) ([]agentChangeFile, error) {
	if cwd == "" {
		return nil, fmt.Errorf("no working directory")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return nil, fmt.Errorf("device offline")
	}
	goos := clientOS(client)
	out, code, err := s.runDeviceProbe(client, gitStatusProbeCommand(goos, cwd), agentGitProbeTimeout, agentGitStatusCap)
	if err != nil {
		return nil, err
	}
	// code -1 means the device never reported one (older client); keep the
	// soft-failure behavior and parse whatever stdout produced.
	if code > 0 {
		return nil, errGitNotRepo
	}
	files := parseGitStatusPorcelain(out)
	// Line counts ride along via a numstat probe (HEAD, falling back to the
	// unstaged diff when the repo has no commits yet). Failure is soft: the
	// panel keeps working, just without the +/- tallies.
	if ns, nsCode, nsErr := s.runDeviceProbe(client, gitNumstatProbeCommand(goos, cwd, true), agentGitProbeTimeout, agentGitStatusCap); nsErr == nil {
		if nsCode > 0 {
			// No HEAD yet (fresh repo): count the unstaged diff instead.
			ns, _, _ = s.runDeviceProbe(client, gitNumstatProbeCommand(goos, cwd, false), agentGitProbeTimeout, agentGitStatusCap)
		}
		mergeGitNumstat(files, parseGitNumstat(ns))
	}
	return files, nil
}

// probeGitDiff asks a device for the unified diff of path in cwd. Repos
// without commits have no HEAD, so the HEAD diff falls back to a plain
// unstaged diff; a path with no diff output at all is checked against the
// index to tell untracked files (error "untracked") from unchanged ones
// (empty diff).
func (s *Server) probeGitDiff(deviceID, cwd, path string) (string, error) {
	if cwd == "" || path == "" {
		return "", fmt.Errorf("missing cwd or path")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", fmt.Errorf("device offline")
	}
	goos := clientOS(client)
	out, code, err := s.runDeviceProbe(client, gitDiffProbeCommand(goos, cwd, path, true), agentGitDiffTimeout, agentGitDiffCap)
	if err != nil {
		return "", err
	}
	if code > 0 {
		out, code, err = s.runDeviceProbe(client, gitDiffProbeCommand(goos, cwd, path, false), agentGitDiffTimeout, agentGitDiffCap)
		if err != nil {
			return "", err
		}
		if code > 0 {
			return "", errGitNotRepo
		}
	}
	if out == "" {
		_, lsCode, lsErr := s.runDeviceProbe(client, gitLsFilesProbeCommand(goos, cwd, path), agentGitProbeTimeout, 4096)
		if lsErr == nil && lsCode > 0 {
			return "", errGitUntracked
		}
	}
	return out, nil
}

// File read probe tuning: previews are capped at 256 KiB; the probe asks for
// one extra byte to detect truncation.
const (
	agentFileReadCap     = 256 * 1024
	agentFileReadTimeout = 15 * time.Second
)

// probeReadFile reads a file on a device (relative paths resolve against
// cwd) and returns its content plus a truncation flag. Binary files (NUL in
// the first 8 KiB) are rejected. Runs off-lock.
func (s *Server) probeReadFile(deviceID, cwd, path string) (string, bool, error) {
	if cwd == "" || path == "" {
		return "", false, fmt.Errorf("missing cwd or path")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", false, fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, fileReadProbeCommand(clientOS(client), cwd, path, agentFileReadCap), agentFileReadTimeout, agentFileReadCap+1024)
	if err != nil {
		return "", false, err
	}
	if code > 0 {
		return "", false, fmt.Errorf("cannot read %s", path)
	}
	truncated := len(out) > agentFileReadCap
	if truncated {
		out = out[:agentFileReadCap]
	}
	probe := out
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if strings.IndexByte(probe, 0) >= 0 {
		return "", false, fmt.Errorf("binary file")
	}
	return out, truncated, nil
}

// fileReadProbeCommand builds the read probe for the device OS, emitting at
// most cap+1 bytes so the caller can tell truncation apart from an exact
// fit. POSIX uses head(1); Windows uses PowerShell byte reads.
func fileReadProbeCommand(goos, cwd, path string, cap int) string {
	if goos == "windows" {
		q := func(s string) string { return strings.ReplaceAll(s, `'`, `''`) }
		return fmt.Sprintf(`powershell -NoProfile -Command "Set-Location -LiteralPath '%s'; $b=[IO.File]::ReadAllBytes('%s'); $n=[Math]::Min($b.Length,%d); $s=[Console]::OpenStandardOutput(); $s.Write($b,0,$n)"`, q(cwd), q(path), cap+1)
	}
	return "cd " + shellQuote(cwd) + " && head -c " + strconv.Itoa(cap+1) + " -- " + shellQuote(path)
}

// Dir listing probe tuning: at most 2000 entries per directory, 15s.
const (
	agentDirListMaxEntries = 2000
	agentDirListTimeout    = 15 * time.Second
)

// probeListDir lists one directory on a device: child entries (dirs first
// ordering is the caller's concern), the parent path, and filesystem roots
// (["/"] on POSIX, drive letters on Windows). Runs off-lock.
func (s *Server) probeListDir(deviceID, path string) (string, []agentDirEntry, []string, error) {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", nil, nil, fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, dirListProbeCommand(clientOS(client), path), agentDirListTimeout, agentFileReadCap+1024)
	if err != nil {
		return "", nil, nil, err
	}
	if code > 0 {
		return "", nil, nil, fmt.Errorf("cannot read %s", path)
	}
	parent := ""
	entries := []agentDirEntry{}
	roots := []string{}
	sep := "/"
	if clientOS(client) == "windows" {
		sep = `\`
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "__RDEV_PARENT__"):
			parent = strings.TrimPrefix(line, "__RDEV_PARENT__")
		case strings.HasPrefix(line, "__RDEV_ROOT__"):
			roots = append(roots, strings.TrimPrefix(line, "__RDEV_ROOT__"))
		case len(line) > 2 && (strings.HasPrefix(line, "D;") || strings.HasPrefix(line, "F;")):
			name := line[2:]
			if name == "" {
				continue
			}
			entries = append(entries, agentDirEntry{
				Name: name,
				Path: strings.TrimSuffix(path, sep) + sep + name,
				Dir:  line[0] == 'D',
			})
		}
	}
	if parent == "" && len(entries) == 0 && len(roots) == 0 {
		return "", nil, nil, fmt.Errorf("cannot read %s", path)
	}
	if len(roots) == 0 && sep == "/" {
		roots = []string{"/"}
	}
	return parent, entries, roots, nil
}

// dirListProbeCommand builds the listing probe for the device OS. Output
// protocol: a __RDEV_PARENT__ line, one "D;name"/"F;name" line per entry
// (parsed on the first ";" only, so names may contain ";"), and
// __RDEV_ROOT__ lines for Windows drives. Non-directory → non-zero exit.
func dirListProbeCommand(goos, path string) string {
	if goos == "windows" {
		q := strings.ReplaceAll(path, `'`, `''`)
		return fmt.Sprintf(`powershell -NoProfile -Command "$p='%s'; if(-not (Test-Path -LiteralPath $p -PathType Container)){ exit 1 }; Write-Output ('__RDEV_PARENT__' + (Split-Path -LiteralPath $p -Parent)); Get-PSDrive -PSProvider FileSystem | ForEach-Object { Write-Output ('__RDEV_ROOT__' + $_.Name + ':\') }; Get-ChildItem -LiteralPath $p -Force -Name | Select-Object -First %d | ForEach-Object { if(Test-Path -LiteralPath (Join-Path $p $_) -PathType Container){ Write-Output ('D;' + $_) } else { Write-Output ('F;' + $_) } }"`, q, agentDirListMaxEntries)
	}
	return "d=" + shellQuote(path) + ` && [ -d "$d" ] && { echo "__RDEV_PARENT__$(dirname -- "$d")"; ls -1Ap -- "$d" 2>/dev/null | head -n ` + strconv.Itoa(agentDirListMaxEntries) + ` | while IFS= read -r e; do case "$e" in */) printf 'D;%s\n' "${e%/}";; *) printf 'F;%s\n' "$e";; esac; done; }`
}

// probeMkdir creates a directory (with parents) on a device. Runs off-lock.
func (s *Server) probeMkdir(deviceID, path string) error {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return fmt.Errorf("device offline")
	}
	_, code, err := s.runDeviceProbe(client, mkdirProbeCommand(clientOS(client), path), agentDirListTimeout, 4096)
	if err != nil {
		return err
	}
	if code > 0 {
		return fmt.Errorf("cannot create %s", path)
	}
	return nil
}

// mkdirProbeCommand builds the mkdir probe for the device OS: POSIX mkdir
// -p, Windows PowerShell New-Item -Force.
func mkdirProbeCommand(goos, path string) string {
	if goos == "windows" {
		q := strings.ReplaceAll(path, `'`, `''`)
		return fmt.Sprintf(`powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path '%s' | Out-Null"`, q)
	}
	return "mkdir -p -- " + shellQuote(path)
}

// parseGitStatusPorcelain parses `git status --porcelain=v1 -uall` output
// into path/status pairs. Rename/copy entries ("R  old -> new") report the
// new path; C-quoted paths are unquoted. Malformed lines are skipped.
func parseGitStatusPorcelain(out string) []agentChangeFile {
	files := []agentChangeFile{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 || line[2] != ' ' {
			continue
		}
		status := porcelainStatusLetter(line[0], line[1])
		if status == "" {
			continue
		}
		path := line[3:]
		if status == "R" || status == "C" {
			if i := strings.LastIndex(path, " -> "); i >= 0 {
				path = path[i+len(" -> "):]
			}
		}
		files = append(files, agentChangeFile{Path: unquoteGitPath(path), Status: status})
	}
	return files
}

// porcelainStatusLetter reduces the two porcelain columns to one status
// letter: "??" for untracked, "" for ignored/blank, else the index column
// when set, falling back to the worktree column (" M" → "M").
func porcelainStatusLetter(x, y byte) string {
	if x == '?' && y == '?' {
		return "??"
	}
	if x == '!' || y == '!' {
		return ""
	}
	c := x
	if c == ' ' {
		c = y
	}
	if c == ' ' {
		return ""
	}
	return string(c)
}

// unquoteGitPath undoes the C-style quoting git applies to paths with
// special characters in porcelain output.
func unquoteGitPath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if unquoted, err := strconv.Unquote(p); err == nil {
			return unquoted
		}
	}
	return p
}

// gitProbeQuote quotes one argument for the device shell: POSIX single
// quotes, or double quotes with \" escaping on Windows cmd.
func gitProbeQuote(goos, s string) string {
	if goos == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return shellQuote(s)
}

// gitBranchProbeCommand builds the branch-probe command for the device OS,
// quoting cwd for the respective shell.
func gitBranchProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " branch --show-current"
}

// gitStatusProbeCommand builds the working-tree status probe.
func gitStatusProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " status --porcelain=v1 -uall"
}

// gitNumstatProbeCommand builds the whole-tree numstat probe, against HEAD
// when withHead is set, else unstaged-only.
func gitNumstatProbeCommand(goos, cwd string, withHead bool) string {
	cmd := "git -C " + gitProbeQuote(goos, cwd) + " diff --numstat"
	if withHead {
		cmd += " HEAD"
	}
	return cmd + " --"
}

// parseGitNumstat parses `git diff --numstat` output into per-path line
// counts. Binary files ("-\t-\tpath") yield zeros. Rename compaction
// ("a/{old => new}/b", "old => new") resolves to the new path so counts
// match the porcelain path; C-quoted paths are unquoted. Malformed lines
// are skipped.
func parseGitNumstat(out string) map[string][2]int {
	counts := make(map[string][2]int)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		adds, errA := strconv.Atoi(fields[0])
		dels, errD := strconv.Atoi(fields[1])
		if errA != nil || errD != nil {
			continue // binary or malformed
		}
		counts[numstatNewPath(fields[2])] = [2]int{adds, dels}
	}
	return counts
}

// numstatNewPath extracts the post-rename path from numstat's path field,
// undoing git's brace compaction and C-quoting.
func numstatNewPath(p string) string {
	if i := strings.Index(p, " => "); i >= 0 {
		if open := strings.LastIndex(p[:i], "{"); open >= 0 {
			if close := strings.Index(p[i:], "}"); close >= 0 {
				// "a/{old => new}/b" → "a/" + "new" + "/b"
				p = p[:open] + p[i+len(" => "):i+close] + p[i+close+1:]
			}
		} else {
			// "old => new" → "new"
			p = p[i+len(" => "):]
		}
	}
	return unquoteGitPath(p)
}

// mergeGitNumstat fills Adds/Dels on each changed file from the numstat
// counts; paths missing from the map (untracked files) stay zero.
func mergeGitNumstat(files []agentChangeFile, counts map[string][2]int) {
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Adds = c[0]
			files[i].Dels = c[1]
		}
	}
}

// gitDiffProbeCommand builds the diff probe for one path, against HEAD when
// withHead is set, else unstaged-only.
func gitDiffProbeCommand(goos, cwd, path string, withHead bool) string {
	cmd := "git -C " + gitProbeQuote(goos, cwd) + " diff"
	if withHead {
		cmd += " HEAD"
	}
	return cmd + " -- " + gitProbeQuote(goos, path)
}

// gitLsFilesAllProbeCommand builds the file listing probe used by
// search_files: tracked files plus untracked-but-not-ignored files.
func gitLsFilesAllProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " ls-files --cached --others --exclude-standard"
}

// gitLsFilesProbeCommand builds the tracked-file check for one path (exit
// code 0 = tracked).
func gitLsFilesProbeCommand(goos, cwd, path string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " ls-files --error-unmatch -- " + gitProbeQuote(goos, path)
}

// runDeviceProbe executes a short non-PTY command on a device and returns
// its stdout (capped) and exit code (-1 when the device never reported
// one). Soft-failure helper for cwd/branch/status/diff probing.
func (s *Server) runDeviceProbe(client *ClientConn, command string, timeout time.Duration, outputCap int) (string, int, error) {
	sessionID := generateID()
	sess := &ProxySession{
		ID:       sessionID,
		ClientID: client.ID,
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		exitDone: make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}
	sess.SetSessionMeta(false, "", command, "", 0, 0)
	if !s.RegisterSession(sess, client) {
		return "", -1, fmt.Errorf("too many active sessions on device")
	}
	defer func() {
		s.removeSession(sessionID)
		client.mu.Lock()
		delete(client.Sessions, sessionID)
		client.mu.Unlock()
	}()

	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  client.ID,
		SessionID: sessionID,
		Command:   command,
		Pty:       false,
	}); err != nil {
		return "", -1, fmt.Errorf("failed to reach device: %v", err)
	}

	stdout := &cappedBuffer{cap: outputCap}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case data, ok := <-sess.WriteCh:
			if !ok {
				continue
			}
			stdout.Write(data)
		case <-sess.StderrCh: // drain
		case <-sess.CloseCh:
			sess.NotifyObserversClose()
			sess.CloseOutput()
			// The device reports the exit code just before closing; wait
			// briefly for the message to arrive.
			return stdout.String(), sess.WaitExitCode(500 * time.Millisecond), nil
		case <-timer.C:
			_ = client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
			select {
			case <-sess.CloseCh:
				sess.NotifyObserversClose()
				sess.CloseOutput()
			case <-time.After(500 * time.Millisecond):
			}
			return "", -1, fmt.Errorf("probe timed out after %s", timeout)
		}
	}
}
