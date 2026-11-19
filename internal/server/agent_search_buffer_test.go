package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWindowsSearchTreatsArgumentsAsData(t *testing.T) {
	powerShell, err := exec.LookPath("pwsh")
	if err != nil {
		powerShell, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell is required for the Windows search integration test")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "目录 [ab] & percent% ' smart’ `tick")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-exist")
	payload := "'; [IO.File]::WriteAllText('" + strings.ReplaceAll(marker, "'", "''") + "','unsafe'); #"
	file := filepath.Join(dir, "result space.txt")
	if err := os.WriteFile(file, []byte("first\n"+payload+"\nlast\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		pattern  string
		contents bool
		want     string
		exit     int
	}{
		{"grep", regexp.QuoteMeta(payload), true, file + ":2:" + payload, 0},
		{"glob", "*.txt", false, file, 0},
		{"no-match", "absent", true, "", 1},
		{"invalid-regex", "[", true, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := strings.Fields(windowsSearchCommand(tc.pattern, dir, tc.contents))[1:]
			cmd := exec.CommandContext(ctx, powerShell, args...)
			output, err := cmd.CombinedOutput()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.exit {
				t.Fatalf("exit: %v, output: %s", err, output)
			}
			if tc.exit != 2 && strings.TrimSpace(string(output)) != tc.want {
				t.Fatalf("got %q, want %q", output, tc.want)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("argument executed as code: %v", err)
			}
		})
	}
}

func TestCappedBufferRetainsOrderedTail(t *testing.T) {
	for _, capacity := range []int{0, 1, 7, 16} {
		var streamed strings.Builder
		buffer := cappedBuffer{cap: capacity, onWrite: func(s string) { streamed.WriteString(s) }}
		var all string
		for _, chunk := range []string{"", "abc", "defgh", "i", "0123456789abcdef0123456789", "yz", "!", ""} {
			buffer.Write([]byte(chunk))
			all += chunk
			want := all
			if len(want) > capacity {
				want = want[len(want)-capacity:]
			}
			got := buffer.String()
			if len(all) > capacity {
				_, got, _ = strings.Cut(got, "\n")
			}
			if got != want || streamed.String() != all {
				t.Fatalf("cap=%d chunk=%q tail=%q want=%q streamed=%q", capacity, chunk, got, want, streamed.String())
			}
		}
	}
}
