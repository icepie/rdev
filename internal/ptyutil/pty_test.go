package ptyutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitialSizeDefaults(t *testing.T) {
	for _, tc := range []struct {
		rows, cols         uint16
		wantRows, wantCols uint16
	}{
		{0, 0, 24, 80},
		{30, 0, 30, 80},
		{0, 132, 24, 132},
		{40, 100, 40, 100},
	} {
		rows, cols := initialSize(&Config{Rows: tc.rows, Cols: tc.cols})
		if rows != tc.wantRows || cols != tc.wantCols {
			t.Errorf("initialSize(%d,%d) = %d,%d, want %d,%d", tc.rows, tc.cols, rows, cols, tc.wantRows, tc.wantCols)
		}
	}
}

func TestShellFlagMatchesPlatform(t *testing.T) {
	want := "-c"
	if runtime.GOOS == "windows" {
		want = "/c"
	}
	if got := shellFlag(); got != want {
		t.Fatalf("shellFlag() = %q, want %q", got, want)
	}
}

func TestDetectShellPrefersEnvironment(t *testing.T) {
	t.Setenv("SHELL", "/custom/sh")
	t.Setenv("COMSPEC", `C:\cmd.exe`)
	if got := detectShell(); got != "/custom/sh" {
		t.Fatalf("detectShell() = %q, want $SHELL", got)
	}
	t.Setenv("SHELL", "")
	if got := detectShell(); got != `C:\cmd.exe` {
		t.Fatalf("detectShell() = %q, want $COMSPEC", got)
	}
}

func TestShellInPrefixPrefersBashThenSh(t *testing.T) {
	prefix := t.TempDir()
	if got := shellInPrefix(prefix); got != "" {
		t.Fatalf("empty prefix = %q, want none", got)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := shellInPrefix(prefix), filepath.Join(prefix, "bin", "sh"); got != want {
		t.Fatalf("sh only = %q, want %q", got, want)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "bash"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := shellInPrefix(prefix), filepath.Join(prefix, "bin", "bash"); got != want {
		t.Fatalf("bash preferred = %q, want %q", got, want)
	}
	if got := shellInPrefix(""); got != "" {
		t.Fatalf("empty prefix = %q, want none", got)
	}
}
