package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanvasValidatesContentWithoutExecutingIt(t *testing.T) {
	tool := &canvasTool{}
	source := `<button onclick="this.textContent='done'">Try</button>`
	raw, _ := json.Marshal(map[string]string{"title": "Device report", "format": "html", "content": source})
	result, err := tool.Execute(context.Background(), raw)
	if err != nil || !strings.Contains(result, `"status":"ready"`) || strings.Contains(result, source) {
		t.Fatalf("unexpected canvas result %q: %v", result, err)
	}
	for _, content := range []string{"", strings.Repeat("x", 65537)} {
		raw, _ := json.Marshal(map[string]string{"title": "Report", "format": "html", "content": content})
		if _, err := tool.Execute(context.Background(), raw); err == nil {
			t.Fatal("invalid content accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, raw); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}
