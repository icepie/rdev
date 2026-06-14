package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestIsGitSmartSSHCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "upload pack", command: "git-upload-pack '/home/dev/repo.git'", want: true},
		{name: "receive pack", command: "git-receive-pack /home/dev/repo.git", want: true},
		{name: "upload archive", command: "git-upload-archive repo.git", want: true},
		{name: "quoted command", command: `"git-upload-pack" '/tmp/repo.git'`, want: true},
		{name: "plain shell", command: "git status", want: false},
		{name: "empty", command: "   ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGitSmartSSHCommand(tt.command); got != tt.want {
				t.Fatalf("isGitSmartSSHCommand(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestParseGitSmartSSHCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		wantOK  bool
		want    gitSmartCommand
	}{
		{name: "quoted path", command: "git-upload-pack '/tmp/repo with space.git'", wantOK: true, want: gitSmartCommand{Name: "git-upload-pack", Path: "/tmp/repo with space.git"}},
		{name: "receive pack", command: `git-receive-pack "C:/repo.git"`, wantOK: true, want: gitSmartCommand{Name: "git-receive-pack", Path: "C:/repo.git"}},
		{name: "archive", command: "git-upload-archive repo.git", wantOK: true, want: gitSmartCommand{Name: "git-upload-archive", Path: "repo.git"}},
		{name: "extra arg rejected", command: "git-upload-pack repo.git --bad", wantOK: false},
		{name: "empty path rejected", command: "git-upload-pack ''", wantOK: false},
		{name: "plain git rejected", command: "git status", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGitSmartSSHCommand(tt.command)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDirectorySnapshotRepoSkipsDotGit(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "README.md"), "hello", 0644)
	mustWriteFile(t, filepath.Join(src, "sub", "tool.sh"), "#!/bin/sh\n", 0755)
	mustWriteFile(t, filepath.Join(src, ".git", "config"), "ignored", 0644)

	if err := buildDirectorySnapshotRepo(src, dst); err != nil {
		t.Fatalf("buildDirectorySnapshotRepo: %v", err)
	}

	repo, err := git.PlainOpen(dst)
	if err != nil {
		t.Fatalf("PlainOpen snapshot: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if !cfg.Core.IsBare {
		t.Fatalf("snapshot output should be bare")
	}
	ref, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if _, err := tree.File("README.md"); err != nil {
		t.Fatalf("README missing from snapshot tree: %v", err)
	}
	if _, err := tree.File(".git/config"); err == nil {
		t.Fatalf("source .git content should not be copied into snapshot tree")
	}
}

func TestReceivePackFallbackRequiresBareRepo(t *testing.T) {
	plain := t.TempDir()
	if _, err := git.PlainInit(plain, false); err != nil {
		t.Fatalf("PlainInit non-bare: %v", err)
	}
	if _, err := newGitReceivePackSession(plain); err == nil || !strings.Contains(err.Error(), "bare Git repository") {
		t.Fatalf("expected non-bare rejection, got %v", err)
	}

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "file.txt"), "plain", 0644)
	if _, err := newGitReceivePackSession(dir); err == nil || !strings.Contains(err.Error(), "bare Git repository") {
		t.Fatalf("expected plain dir rejection, got %v", err)
	}

	bare := filepath.Join(t.TempDir(), "repo.git")
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	if _, err := newGitReceivePackSession(bare); err != nil {
		t.Fatalf("expected bare receive-pack session, got %v", err)
	}
}

func TestFallbackUploadAndReceiveWorkWithGoGitClient(t *testing.T) {
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(remotePath, true); err != nil {
		t.Fatalf("PlainInit remote: %v", err)
	}

	localPath := t.TempDir()
	local, err := git.PlainInit(localPath, false)
	if err != nil {
		t.Fatalf("PlainInit local: %v", err)
	}
	mustWriteFile(t, filepath.Join(localPath, "README.md"), "hello\n", 0644)
	wt, err := local.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	hash, err := wt.Commit("initial", &git.CommitOptions{Author: testSignature()})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	if err := local.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/master"}}); err != nil {
		t.Fatalf("Push via go-git client: %v", err)
	}

	remote, err := git.PlainOpen(remotePath)
	if err != nil {
		t.Fatalf("PlainOpen remote: %v", err)
	}
	ref, err := remote.Reference(plumbing.ReferenceName("refs/heads/master"), true)
	if err != nil {
		t.Fatalf("remote ref: %v", err)
	}
	if ref.Hash() != hash {
		t.Fatalf("remote hash=%s, want %s", ref.Hash(), hash)
	}

	if _, _, err := newGitUploadPackSession(remotePath); err != nil {
		t.Fatalf("expected upload-pack session for bare repo: %v", err)
	}
	if _, err := newGitReceivePackSession(remotePath); err != nil {
		t.Fatalf("expected receive-pack session for bare repo: %v", err)
	}
}

func TestUploadPackFallbackSupportsPlainDirectorySnapshot(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "plain.txt"), "snapshot", 0644)

	session, cleanup, err := newGitUploadPackSession(dir)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("newGitUploadPackSession plain dir: %v", err)
	}
	ar, err := session.AdvertisedReferences()
	if err != nil {
		t.Fatalf("AdvertisedReferences: %v", err)
	}
	if len(ar.References) == 0 {
		t.Fatalf("snapshot should advertise at least one reference")
	}
}

func TestSnapshotLimits(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	tooLarge := make([]byte, gitSnapshotMaxFileSize+1)
	if err := os.WriteFile(filepath.Join(src, "big.bin"), tooLarge, 0644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	err := buildDirectorySnapshotRepo(src, dst)
	if err == nil || !strings.Contains(err.Error(), "exceeds snapshot file size limit") {
		t.Fatalf("expected file size limit error, got %v", err)
	}
}

func mustWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func testSignature() *object.Signature {
	return &object.Signature{Name: "test", Email: "test@example.invalid", When: time.Unix(1, 0)}
}

func TestNewGitReceivePackSessionRejectsMissingPath(t *testing.T) {
	_, err := newGitReceivePackSession(filepath.Join(t.TempDir(), "missing.git"))
	if err == nil || !errors.Is(err, git.ErrRepositoryNotExists) && !strings.Contains(err.Error(), "bare Git repository") {
		t.Fatalf("expected missing repo rejection, got %v", err)
	}
}
