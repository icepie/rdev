package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
)

const browserHistoryLimit = 200
const browserThumbnailLimit = 50

type browserActionRecord struct {
	ID         string `json:"id"`
	Device     string `json:"device"`
	Target     string `json:"target,omitempty"`
	Action     string `json:"action"`
	StartedAt  int64  `json:"startedAt"`
	DurationMS int64  `json:"durationMs"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Thumbnail  string `json:"thumbnail,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

func browserRecordable(action string) bool {
	switch action {
	case "open", "navigate", "click", "fill", "select", "hover", "scroll", "key", "close":
		return true
	}
	return false
}

// Called while the conversation operation lock is held: the thumbnail cannot
// accidentally come from the next Agent action. A failed capture never fails
// the action. Inputs/URLs/errors are not copied into the replay metadata.
func (t *browserTool) recordBrowserAction(ctx context.Context, c *browserCDP, a browserAction, target string, started time.Time, actionErr error) {
	record := browserActionRecord{ID: generateID(), Device: a.Device, Target: target, Action: a.Action, StartedAt: started.UnixMilli(), DurationMS: time.Since(started).Milliseconds(), OK: actionErr == nil}
	if actionErr != nil {
		record.Error = "Browser action failed; see the tool result for details."
	}
	if c != nil && ctx.Err() == nil && a.Action != "close" {
		captureCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		var size struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if value, err := browserEvaluate(captureCtx, c, `JSON.stringify({width:innerWidth,height:innerHeight})`); err == nil {
			_ = json.Unmarshal([]byte(value), &size)
		}
		record.Width, record.Height = size.Width, size.Height
		var shot struct {
			Data string `json:"data"`
		}
		if err := c.call(captureCtx, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": 50, "captureBeyondViewport": false}, &shot); err == nil {
			if data, err := base64.StdEncoding.DecodeString(shot.Data); err == nil {
				record.Thumbnail = boundedAgentImagePreview(data, 12*1024)
			}
		}
	}
	t.state.mu.Lock()
	t.state.browserHistory = appendBrowserRecord(t.state.browserHistory, record)
	t.state.mu.Unlock()
	// Replay data has its own bounded ring. Do not duplicate images in the LLM
	// transcript or unbounded live turn frames.
	t.srv.agentSessionsMu.Lock()
	current := t.entry.threads[t.thread.id] == t.thread && t.thread.automation == t.state
	t.srv.agentSessionsMu.Unlock()
	if current {
		t.srv.agentBroadcast(t.entry, agentMsg{Op: "browser_action", Thread: t.thread.id, BrowserAction: &record})
	}
}

func appendBrowserRecord(records []browserActionRecord, record browserActionRecord) []browserActionRecord {
	records = append(append([]browserActionRecord(nil), records...), record)
	if len(records) > browserHistoryLimit {
		records = records[len(records)-browserHistoryLimit:]
	}
	thumbs := 0
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Thumbnail != "" {
			thumbs++
			if thumbs > browserThumbnailLimit {
				records[i].Thumbnail = ""
			}
		}
	}
	return records
}
