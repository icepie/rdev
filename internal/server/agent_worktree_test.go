package server

import (
	"testing"
	"time"
)

func TestValidateAgentWorktreeBaseRejectsRevisionAndPaths(t *testing.T) {
	for _, base := range []string{"../main", "/tmp/repo", "main..old", "-x", "main;touch /tmp/pwn"} {
		if err := validateAgentWorktreeBase(base); err == nil {
			t.Fatalf("base %q was accepted", base)
		}
	}
	for _, base := range []string{"main", "feature/review-1", "release_2026.1"} {
		if err := validateAgentWorktreeBase(base); err != nil {
			t.Fatalf("base %q rejected: %v", base, err)
		}
	}
}

func TestWorktreePathOwnershipRejectsArbitraryPaths(t *testing.T) {
	record := AgentWorktreeRecord{Device: "device-1", Thread: "t_1", Repository: "/srv/project", Path: "/srv/project.rdev-worktrees/owned"}
	record.Branch = worktreeBranchName(record.Device, record.Thread)
	if !worktreePathOwned(record) {
		t.Fatal("generated sibling path was not accepted")
	}
	for _, path := range []string{"/srv/project/file", "/srv/project.rdev-worktrees/owned/nested", "/tmp/owned"} {
		record.Path = path
		if worktreePathOwned(record) {
			t.Fatalf("arbitrary path %q was accepted", path)
		}
	}
}

func TestWorktreeRefsRequireNonManagedPreserver(t *testing.T) {
	managed := "refs/heads/rdev/worktree/a/b\n"
	if agentWorktreeRefsPreserve(managed, "rdev/worktree/a/b", "deadbeef") {
		t.Fatal("managed branch alone was treated as preserved")
	}
	if !agentWorktreeRefsPreserve(managed+"refs/heads/main\n", "rdev/worktree/a/b", "deadbeef") {
		t.Fatal("main branch did not preserve merge base")
	}
}

func TestAgentWorktreeStateRoundtripIsOwnedAndAtomic(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	item := AgentWorktreeRecord{Device: "device-1", Thread: "t_1", Repository: "/repo", Path: "/repo.rdev-worktrees/x", Branch: "rdev/worktree/a/b", State: AgentWorktreeReady, CreatedAt: now, UpdatedAt: now}
	if err := saveAgentWorktreeRecords(dir, map[string]AgentWorktreeRecord{worktreeOwnerKey(item.Device, item.Thread): item}); err != nil {
		t.Fatal(err)
	}
	got, err := loadAgentWorktreeRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got[worktreeOwnerKey(item.Device, item.Thread)].Path != item.Path {
		t.Fatalf("roundtrip path = %q", got[worktreeOwnerKey(item.Device, item.Thread)].Path)
	}
}
