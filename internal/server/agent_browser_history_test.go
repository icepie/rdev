package server

import (
	"fmt"
	"testing"
)

func TestBrowserHistoryBoundedImmutable(t *testing.T) {
	var history []browserActionRecord
	for i := 0; i < 250; i++ {
		history = appendBrowserRecord(history, browserActionRecord{ID: fmt.Sprint(i), Thumbnail: "frame"})
	}
	if len(history) != 200 || history[0].ID != "50" {
		t.Fatalf("incorrect record bound: %d", len(history))
	}
	thumbnails := 0
	for _, r := range history {
		if r.Thumbnail != "" {
			thumbnails++
		}
	}
	if thumbnails != 50 {
		t.Fatalf("thumbnail count = %d", thumbnails)
	}
	previous := history[len(history)-50]
	_ = appendBrowserRecord(history, browserActionRecord{ID: "new", Thumbnail: "new frame"})
	if history[len(history)-50] != previous {
		t.Fatal("append modified an in-flight snapshot")
	}
}

func TestBrowserHistoryRecordsOnlyInteractions(t *testing.T) {
	for _, action := range []string{"snapshot", "screenshot", "status", "wait_for", "unknown"} {
		if browserRecordable(action) {
			t.Fatalf("recorded %s", action)
		}
	}
	for _, action := range []string{"open", "navigate", "click", "fill", "select", "hover", "scroll", "key", "close"} {
		if !browserRecordable(action) {
			t.Fatalf("missing %s", action)
		}
	}
}
