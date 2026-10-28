package server

import (
	"strings"
	"testing"
)

func TestUnifiedDiffSimpleReplace(t *testing.T) {
	diff := unifiedDiff("old line\n", "new line\n", "f.txt")
	for _, want := range []string{"--- a/f.txt\n", "+++ b/f.txt\n", "@@ -1,1 +1,1 @@\n", "-old line\n", "+new line\n"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestUnifiedDiffIdentical(t *testing.T) {
	if got := unifiedDiff("same\n", "same\n", "f"); got != "" {
		t.Fatalf("identical texts produced %q", got)
	}
}

func TestUnifiedDiffSplitsFarHunks(t *testing.T) {
	var oldLines, newLines []string
	oldLines = append(oldLines, "change-me-1")
	for i := range 10 {
		oldLines = append(oldLines, string(rune('a'+i)))
	}
	oldLines = append(oldLines, "change-me-2")
	newLines = append(newLines, "changed-1")
	for i := range 10 {
		newLines = append(newLines, string(rune('a'+i)))
	}
	newLines = append(newLines, "changed-2")

	diff := unifiedDiff(strings.Join(oldLines, "\n")+"\n", strings.Join(newLines, "\n")+"\n", "f")
	if got := strings.Count(diff, "@@ "); got != 2 {
		t.Fatalf("hunk count = %d, want 2:\n%s", got, diff)
	}
	if !strings.Contains(diff, "-change-me-1\n+changed-1\n") || !strings.Contains(diff, "-change-me-2\n+changed-2\n") {
		t.Fatalf("changes missing:\n%s", diff)
	}
	// First hunk: old starts at line 1 with 1+3 context lines.
	if !strings.Contains(diff, "@@ -1,4 +1,4 @@\n") {
		t.Fatalf("first hunk header wrong:\n%s", diff)
	}
}

func TestUnifiedDiffNoTrailingNewline(t *testing.T) {
	diff := unifiedDiff("a\nb", "a\nc", "f")
	if strings.Count(diff, "\\ No newline at end of file") != 2 {
		t.Fatalf("missing-newline markers wrong:\n%s", diff)
	}
}

func TestUnifiedDiffPureInsertion(t *testing.T) {
	diff := unifiedDiff("", "x\ny\n", "f")
	if !strings.Contains(diff, "@@ -0,0 +1,2 @@\n") {
		t.Fatalf("insertion header wrong:\n%s", diff)
	}
	if !strings.Contains(diff, "+x\n+y\n") {
		t.Fatalf("inserted lines missing:\n%s", diff)
	}
}

func TestApplyEdit(t *testing.T) {
	if _, _, err := applyEdit("hello world", "missing", "x", false); err == nil {
		t.Fatal("0 matches must error")
	}
	if _, _, err := applyEdit("foo foo", "foo", "bar", false); err == nil ||
		!strings.Contains(err.Error(), "2 times") {
		t.Fatalf("multi-match error = %v", err)
	}
	out, n, err := applyEdit("foo foo", "foo", "bar", true)
	if err != nil || n != 2 || out != "bar bar" {
		t.Fatalf("replace_all = %q, %d, %v", out, n, err)
	}
	out, n, err = applyEdit("hello world", "world", "there", false)
	if err != nil || n != 1 || out != "hello there" {
		t.Fatalf("single replace = %q, %d, %v", out, n, err)
	}
	if _, _, err := applyEdit("abc", "", "x", false); err == nil {
		t.Fatal("empty old_string must error")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("short = %q", got)
	}
	if got := truncateRunes("0123456789abc", 5); got != "01234…" {
		t.Fatalf("cut = %q", got)
	}
	// Multibyte runes count once each.
	if got := truncateRunes("界面测试abc", 4); got != "界面测试…" {
		t.Fatalf("runes = %q", got)
	}
}
