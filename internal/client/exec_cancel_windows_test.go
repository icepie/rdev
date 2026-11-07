//go:build windows

package client

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"rdev/internal/protocol"
)

func TestWindowsExecHelper(t *testing.T) {
	mode := os.Getenv("RDEV_EXEC_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsExecHelper$")
		child.Env = append(os.Environ(), "RDEV_EXEC_HELPER=child")
		if err := child.Run(); err != nil {
			os.Exit(1)
		}
	case "child":
		if err := os.WriteFile(os.Getenv("RDEV_EXEC_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
	case "keep":
		time.Sleep(300 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("RDEV_EXEC_MARKER"), []byte("kept"), 0600); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func TestWindowsExecCancelStopsDescendantsAndIsolatesSessions(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient("", "test", "", "cmd.exe")
	dir := t.TempDir()
	// Exercise cmd.exe quoting with a real executable path containing spaces.
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	executable = filepath.Join(dir, "quoted helper.exe")
	if err := os.WriteFile(executable, binary, 0700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child-pid")
	command := `"` + executable + `" -test.run=^TestWindowsExecHelper$`
	canceled, err := client.startShellExecSession(&protocol.Message{SessionID: "cancel", Command: command, Env: []string{"RDEV_EXEC_HELPER=parent", "RDEV_EXEC_MARKER=" + pidFile}})
	if err != nil {
		t.Fatal(err)
	}
	defer canceled.close()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild did not start")
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	keptFile := filepath.Join(dir, "kept")
	kept, err := client.startShellExecSession(&protocol.Message{SessionID: "keep", Command: command, Env: []string{"RDEV_EXEC_HELPER=keep", "RDEV_EXEC_MARKER=" + keptFile}})
	if err != nil {
		t.Fatal(err)
	}
	defer kept.close()
	canceled.close()
	canceled.close()
	if result, err := windows.WaitForSingleObject(child, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("grandchild survived: %d %v", result, err)
	}
	for name, session := range map[string]*clientSession{"canceled": canceled, "kept": kept} {
		select {
		case <-session.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s session did not finish", name)
		}
	}
	if data, err := os.ReadFile(keptFile); err != nil || string(data) != "kept" {
		t.Fatalf("other session was interrupted: %s %v", data, err)
	}
}

func TestWindowsExecCanceledStartupAndFailedStart(t *testing.T) {
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "exit 0")
		cancel()
		release, err := startExecCommand(cmd)
		if err == nil {
			release()
			t.Fatal("canceled command started")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing.exe"))
	if release, err := startExecCommand(cmd); err == nil {
		release()
		t.Fatal("missing executable started")
	}
}

func TestWindowsExecCancelDuringStart(t *testing.T) {
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Millisecond, cancel)
		cmd := exec.CommandContext(ctx, "cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
		release, err := startExecCommand(cmd)
		if err != nil {
			timer.Stop()
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("startup cancellation: %v", err)
			}
			continue
		}
		done := make(chan error, 1)
		go func() { err := cmd.Wait(); release(); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("long-running command escaped cancellation")
			}
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("startup cancellation left a running/suspended command")
		}
		timer.Stop()
		cancel()
	}
}
