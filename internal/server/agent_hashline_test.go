package server

import (
	"strings"
	"testing"
)

func anchorOf(t *testing.T, n int, line string) string {
	t.Helper()
	return formatAnchor(n, line)
}

func TestApplyHashlineEditsSingleLine(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	out, n, err := applyHashlineEdits(content, []lineEditArg{
		{Start: anchorOf(t, 2, "beta"), NewString: "BETA"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	if out != "alpha\nBETA\ngamma\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestApplyHashlineEditsRangeAndBottomUp(t *testing.T) {
	content := "l1\nl2\nl3\nl4\nl5\n"
	out, _, err := applyHashlineEdits(content, []lineEditArg{
		// Listed in ascending order; applied bottom-up so anchors stay valid.
		{Start: anchorOf(t, 2, "l2"), End: anchorOf(t, 3, "l3"), NewString: "x\ny"},
		{Start: anchorOf(t, 5, "l5"), NewString: ""}, // deletion
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out != "l1\nx\ny\nl4\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestApplyHashlineEditsStaleAnchorRejected(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"
	_, _, err := applyHashlineEdits(content, []lineEditArg{
		{Start: anchorOf(t, 2, "WRONG"), NewString: "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "stale anchor") {
		t.Fatalf("err = %v, want stale anchor", err)
	}
}

func TestApplyHashlineEditsOverlapRejected(t *testing.T) {
	content := "a\nb\nc\n"
	_, _, err := applyHashlineEdits(content, []lineEditArg{
		{Start: anchorOf(t, 1, "a"), End: anchorOf(t, 2, "b"), NewString: "x"},
		{Start: anchorOf(t, 2, "b"), NewString: "y"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("err = %v, want overlapping", err)
	}
}

func TestApplyHashlineEditsOutOfRange(t *testing.T) {
	_, _, err := applyHashlineEdits("only\n", []lineEditArg{
		{Start: anchorOf(t, 9, "only"), NewString: "x"},
	})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v, want out of range", err)
	}
}

func TestFormatReadLinesTruncationAndPaging(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 3000; i++ {
		sb.WriteString("line")
		sb.WriteString(strings.Repeat("", 0))
		sb.WriteByte(byte('0' + i%10))
		sb.WriteByte('\n')
	}
	content := sb.String()

	// Default: first 2000 lines + continuation marker.
	out := formatReadLines(content, 0, 0, false)
	if !strings.Contains(out, "[lines 1-2000 of 3000; 1000 more — continue with offset=2001]") {
		t.Fatalf("missing truncation marker, tail: %q", out[len(out)-120:])
	}

	// Paging: middle window.
	out = formatReadLines(content, 2001, 2000, false)
	if !strings.Contains(out, "[lines 2001-3000 of 3000]") {
		t.Fatalf("missing window marker, tail: %q", out[len(out)-80:])
	}

	// Past the end.
	out = formatReadLines(content, 9999, 0, false)
	if !strings.Contains(out, "past the end") {
		t.Fatalf("out = %q", out)
	}
}

func TestFormatReadLinesAnchors(t *testing.T) {
	out := formatReadLines("foo\nbar\n", 1, 0, true)
	want := formatAnchor(1, "foo") + "|foo\n" + formatAnchor(2, "bar") + "|bar\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestFormatReadLinesCRLFSharedAnchors(t *testing.T) {
	crlf := "one\r\ntwo\r\n"
	out := formatReadLines(crlf, 1, 0, true)
	// Anchors hash the \r-stripped line, so CRLF files share anchors.
	if !strings.HasPrefix(out, formatAnchor(1, "one")+"|one\r") {
		t.Fatalf("out = %q", out)
	}
}

func TestMagicTurnEffort(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"ultrathink about the design", "high"},
		{"please ULTRATHINK this", "high"},
		{"run `ultrathink` in the shell", ""},
		{"```\nultrathink\n```\njust do it", ""},
		{"the counterultrathink word", ""},
		{"no keywords here", ""},
	}
	for _, c := range cases {
		if got := magicTurnEffort(c.text); got != c.want {
			t.Errorf("magicTurnEffort(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
