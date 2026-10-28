package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// --- fake SSE provider ---

// sseChunk builds one `data:` line carrying a delta.
func sseChunk(content, reasoning string, toolCalls ...map[string]any) string {
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	}
	chunk := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}
	data, _ := json.Marshal(chunk)
	return "data: " + string(data) + "\n\n"
}

func sseToolCall(id, name, args string) map[string]any {
	return map[string]any{
		"index":    0,
		"id":       id,
		"type":     "function",
		"function": map[string]any{"name": name, "arguments": args},
	}
}

const sseDone = "data: [DONE]\n\n"

// fakeProvider serves one scripted response per request; requests beyond the
// script fail the test.
type fakeProvider struct {
	t       *testing.T
	hits    atomic.Int32
	handler func(w http.ResponseWriter, round int)
}

func newFakeProvider(t *testing.T, rounds ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round := int(hits.Add(1)) - 1
		if round >= len(rounds) {
			t.Errorf("unexpected extra provider request %d", round+1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, rounds[round])
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// collectEvents returns an emit that records events plus the collected slice.
func collectEvents() (func(Event), *[]Event) {
	events := []Event{}
	return func(e Event) { events = append(events, e) }, &events
}

func eventsOfType(events []Event, typ EventType) []Event {
	var out []Event
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// --- fake tools ---

type fakeTool struct {
	name      string
	dangerous bool
	ran       bool
}

func (t *fakeTool) Name() string            { return t.name }
func (t *fakeTool) Description() string     { return "fake " + t.name }
func (t *fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Dangerous() bool         { return t.dangerous }
func (t *fakeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	t.ran = true
	return "tool output", nil
}

func testConfig(baseURL string, tools []Tool) Config {
	return Config{
		BaseURL:      baseURL,
		APIKey:       "test",
		Model:        "test-model",
		SystemPrompt: "sys",
		Tools:        tools,
		MaxTurns:     8,
	}
}

// --- approval ---

func TestSessionApproveDenySkipsExecution(t *testing.T) {
	srv, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "shell", `{"command":"rm -rf /"}`))+sseDone,
		sseChunk("done", "")+sseDone,
	)
	tool := &fakeTool{name: "shell", dangerous: true}
	cfg := testConfig(srv.URL, []Tool{tool})
	var approveCalls int
	cfg.Approve = func(tc ToolCall, args json.RawMessage) (bool, error) {
		approveCalls++
		if tc.Name != "shell" {
			t.Errorf("unexpected approval request for %q", tc.Name)
		}
		return false, nil
	}
	sess := New(cfg)
	emit, events := collectEvents()
	text, err := sess.Run(context.Background(), "wipe it", emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if text != "done" {
		t.Fatalf("final text = %q", text)
	}
	if approveCalls != 1 {
		t.Fatalf("approve calls = %d, want 1", approveCalls)
	}
	if tool.ran {
		t.Fatal("denied tool must not execute")
	}
	ends := eventsOfType(*events, EventToolEnd)
	if len(ends) != 1 || !ends[0].IsError || ends[0].ToolResult != "error: denied by user" {
		t.Fatalf("tool_end events = %+v", ends)
	}
	// The denial is fed back to the model as a tool message.
	var found bool
	for _, m := range sess.History() {
		if m.Role == "tool" && m.Content == "error: denied by user" {
			found = true
		}
	}
	if !found {
		t.Fatal("denial tool message missing from history")
	}
}

func TestSessionApproveAllowExecutes(t *testing.T) {
	srv, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "shell", `{"command":"ls"}`))+sseDone,
		sseChunk("listed", "")+sseDone,
	)
	tool := &fakeTool{name: "shell", dangerous: true}
	cfg := testConfig(srv.URL, []Tool{tool})
	cfg.Approve = func(tc ToolCall, args json.RawMessage) (bool, error) { return true, nil }
	sess := New(cfg)
	emit, events := collectEvents()
	if _, err := sess.Run(context.Background(), "list", emit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tool.ran {
		t.Fatal("approved tool was not executed")
	}
	ends := eventsOfType(*events, EventToolEnd)
	if len(ends) != 1 || ends[0].IsError || ends[0].ToolResult != "tool output" {
		t.Fatalf("tool_end events = %+v", ends)
	}
}

func TestSessionApproveSkippedForSafeTool(t *testing.T) {
	srv, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "list_dir", `{"path":"/"}`))+sseDone,
		sseChunk("ok", "")+sseDone,
	)
	tool := &fakeTool{name: "list_dir"} // not dangerous
	cfg := testConfig(srv.URL, []Tool{tool})
	cfg.Approve = func(tc ToolCall, args json.RawMessage) (bool, error) {
		t.Errorf("Approve called for non-dangerous tool %q", tc.Name)
		return false, nil
	}
	sess := New(cfg)
	if _, err := sess.Run(context.Background(), "ls", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tool.ran {
		t.Fatal("safe tool must run without approval")
	}
}

// --- todo_write ---

func TestSessionTodoWriteEmitsEvent(t *testing.T) {
	args := `{"items":[{"content":"plan","status":"in_progress"},{"content":"do","status":"pending"}]}`
	srv, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "todo_write", args))+sseDone,
		sseChunk("planned", "")+sseDone,
	)
	tool := &fakeTool{name: "todo_write"}
	cfg := testConfig(srv.URL, []Tool{tool})
	sess := New(cfg)
	emit, events := collectEvents()
	if _, err := sess.Run(context.Background(), "plan it", emit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	todos := eventsOfType(*events, EventTodo)
	if len(todos) != 1 {
		t.Fatalf("todo events = %d, want 1", len(todos))
	}
	var items []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(todos[0].ToolResult), &items); err != nil {
		t.Fatalf("todo items JSON: %v", err)
	}
	if len(items) != 2 || items[0].Content != "plan" || items[0].Status != "in_progress" {
		t.Fatalf("todo items = %+v", items)
	}
}

// --- reasoning ---

func TestSessionReasoningEventsAndDone(t *testing.T) {
	srv, _ := newFakeProvider(t,
		sseChunk("", "thinking ")+sseChunk("", "hard")+sseChunk("answer", "")+sseDone,
	)
	cfg := testConfig(srv.URL, nil)
	sess := New(cfg)
	emit, events := collectEvents()
	if _, err := sess.Run(context.Background(), "hi", emit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reasons := eventsOfType(*events, EventReasoning)
	if len(reasons) != 2 || reasons[0].Text != "thinking " || reasons[1].Text != "hard" {
		t.Fatalf("reasoning events = %+v", reasons)
	}
	dones := eventsOfType(*events, EventDone)
	if len(dones) != 1 || dones[0].Reasoning != "thinking hard" {
		t.Fatalf("done events = %+v", dones)
	}
	// Reasoning rides the assistant message for persistence.
	hist := sess.History()
	if last := hist[len(hist)-1]; last.Role != "assistant" || last.Reasoning != "thinking hard" {
		t.Fatalf("last history message = %+v", last)
	}
}

// --- retry ---

func TestStreamChatRetriesOnceWithoutProgress(t *testing.T) {
	srv, hits := newFakeProvider(t,
		"", // first attempt: 200 with an empty body would not error; use close below instead
		sseChunk("recovered", "")+sseDone,
	)
	// Override: make the first request fail with a 500 (zero progress).
	var hits2 atomic.Int32
	srv.Close()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits2.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "boom")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunk("recovered", "")+sseDone)
	}))
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL, nil)
	sess := New(cfg)
	text, err := sess.Run(context.Background(), "hi", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("text = %q", text)
	}
	if hits.Load() != 0 {
		t.Fatalf("unexpected hits on first server: %d", hits.Load())
	}
	if got := hits2.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2 (one retry)", got)
	}
}

func TestStreamChatGivesUpAfterOneRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL, nil)
	sess := New(cfg)
	if _, err := sess.Run(context.Background(), "hi", nil); err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want exactly 2", got)
	}
}

// --- truncation / compaction ---

func TestTruncateToolOutputKeepsHeadAndTail(t *testing.T) {
	long := strings.Repeat("a", toolOutputMax+5000)
	out := truncateToolOutput(long)
	if len(out) >= toolOutputMax {
		t.Fatalf("truncated length = %d", len(out))
	}
	if !strings.HasPrefix(out, strings.Repeat("a", 100)) {
		t.Fatal("head lost")
	}
	if !strings.Contains(out, "...[truncated ") || !strings.Contains(out, " chars]...") {
		t.Fatalf("marker missing: %q", out[6000:6100])
	}
	if !strings.HasSuffix(out, strings.Repeat("a", 100)) {
		t.Fatal("tail lost")
	}
	// Short output passes through.
	if got := truncateToolOutput("short"); got != "short" {
		t.Fatalf("short output mangled: %q", got)
	}
}

func TestTruncateToolOutputRuneSafe(t *testing.T) {
	long := strings.Repeat("界", toolOutputMax+100)
	out := truncateToolOutput(long)
	for _, r := range out {
		if r == '\uFFFD' {
			t.Fatal("replacement char: truncation split a rune")
		}
	}
}

func TestCompactToolOutputsOnlyTouchesOldLargeToolMessages(t *testing.T) {
	cfg := testConfig("http://unused", nil)
	sess := New(cfg)
	msgs := []Message{{Role: "user", Content: "start"}}
	for i := range 9 {
		msgs = append(msgs,
			Message{Role: "assistant", ToolCalls: []ToolCall{{ID: fmt.Sprint(i), Name: "shell", Arguments: "{}"}}},
			Message{Role: "tool", ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 3000)},
		)
	}
	sess.SetHistory(msgs)
	sess.compactToolOutputs()
	hist := sess.History()

	cutoff := len(hist) - compactKeepRecent
	toolIdx := 0
	for i, m := range hist {
		if m.Role != "tool" {
			continue
		}
		toolIdx++
		old := i < cutoff
		truncated := strings.Contains(m.Content, compactMarker)
		if old && !truncated {
			t.Errorf("old tool message %d (idx %d) not compacted", toolIdx, i)
		}
		if !old && truncated {
			t.Errorf("recent tool message %d (idx %d) must stay intact", toolIdx, i)
		}
		if truncated {
			if len(m.Content) > compactHeadLen+len(compactMarker)+16 {
				t.Errorf("compacted length = %d", len(m.Content))
			}
		}
	}

	// Idempotent: a second pass changes nothing.
	before, _ := json.Marshal(sess.History())
	sess.compactToolOutputs()
	after, _ := json.Marshal(sess.History())
	if string(before) != string(after) {
		t.Error("compaction is not idempotent")
	}
}

func TestHistoryToolMessagesTruncatedBeforeFeed(t *testing.T) {
	big := strings.Repeat("y", toolOutputMax+100)
	srv, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "echo", `{}`))+sseDone,
		sseChunk("fin", "")+sseDone,
	)
	tool := &fakeTool{name: "echo"}
	cfg := testConfig(srv.URL, []Tool{tool})
	sess := New(cfg)
	if _, err := sess.Run(context.Background(), "go", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_ = big
	// The tool result in history must be the (short) output, and tool_end
	// results are capped: run a second tool producing huge output.
	srv2, _ := newFakeProvider(t,
		sseChunk("", "", sseToolCall("c1", "echo", `{}`))+sseDone,
		sseChunk("fin", "")+sseDone,
	)
	bigTool := &bigOutputTool{name: "echo", out: big}
	cfg2 := testConfig(srv2.URL, []Tool{bigTool})
	sess2 := New(cfg2)
	emit, events := collectEvents()
	if _, err := sess2.Run(context.Background(), "go", emit); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ends := eventsOfType(*events, EventToolEnd)
	if len(ends) != 1 || len(ends[0].ToolResult) >= toolOutputMax {
		t.Fatalf("tool_end result not truncated: %+v", ends)
	}
	for _, m := range sess2.History() {
		if m.Role == "tool" && len(m.Content) >= toolOutputMax {
			t.Fatalf("history tool message not truncated: %d bytes", len(m.Content))
		}
	}
}

type bigOutputTool struct {
	name string
	out  string
}

func (t *bigOutputTool) Name() string            { return t.name }
func (t *bigOutputTool) Description() string     { return "big" }
func (t *bigOutputTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *bigOutputTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.out, nil
}

// --- permission modes (ApproveAll gate) ---

// TestSessionApproveAllGate covers the session-side approval gate: without
// ApproveAll only DangerousTool calls prompt; with ApproveAll every tool
// call prompts. The full/write/ask mode mapping itself is tested in the
// server package (agentPermNeedsApproval).
func TestSessionApproveAllGate(t *testing.T) {
	cases := []struct {
		name       string
		approveAll bool
		dangerous  bool
		wantAsk    bool
	}{
		{"dangerous prompts", false, true, true},
		{"readonly skipped", false, false, false},
		{"ask mode prompts readonly", true, false, true},
		{"ask mode prompts dangerous", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newFakeProvider(t,
				sseChunk("", "", sseToolCall("c1", "tool", `{}`))+sseDone,
				sseChunk("done", "")+sseDone,
			)
			tool := &fakeTool{name: "tool", dangerous: c.dangerous}
			cfg := testConfig(srv.URL, []Tool{tool})
			asked := 0
			cfg.ApproveAll = func() bool { return c.approveAll }
			cfg.Approve = func(tc ToolCall, args json.RawMessage) (bool, error) {
				asked++
				return true, nil
			}
			sess := New(cfg)
			if _, err := sess.Run(context.Background(), "go", nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !tool.ran {
				t.Fatal("tool must run when approval is granted")
			}
			if got := asked > 0; got != c.wantAsk {
				t.Fatalf("approveAll=%v dangerous=%v: prompted=%v, want %v", c.approveAll, c.dangerous, got, c.wantAsk)
			}
		})
	}
}

// --- reasoning effort ---

func TestMarshalChatRequestReasoningEffort(t *testing.T) {
	cases := []struct {
		effort string
		want   string // "" means the field must be absent
	}{
		{"", ""},
		{"off", ""},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
	}
	for _, c := range cases {
		cfg := testConfig("http://unused", nil)
		cfg.ReasoningEffort = c.effort
		payload, err := marshalChatRequest(cfg, []Message{{Role: "user", Content: "hi"}})
		if err != nil {
			t.Fatalf("marshalChatRequest(%q): %v", c.effort, err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		raw, ok := body["reasoning_effort"]
		if c.want == "" {
			if ok {
				t.Errorf("effort %q: reasoning_effort must be omitted, got %s", c.effort, raw)
			}
			continue
		}
		if !ok {
			t.Errorf("effort %q: reasoning_effort missing", c.effort)
			continue
		}
		var got string
		if err := json.Unmarshal(raw, &got); err != nil || got != c.want {
			t.Errorf("effort %q: reasoning_effort = %q, want %q", c.effort, got, c.want)
		}
	}
}

// --- image attachments ---

func TestMessageMarshalJSONImageParts(t *testing.T) {
	m := Message{
		Role:    "user",
		Content: "what is this?",
		Images: []MessageImage{
			{Name: "shot.png", Mime: "image/png", Data: "QUJD"},
			{Name: "pic.jpg", Mime: "image/jpeg", Data: "REVG"},
		},
	}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("content is not a parts array: %v (%s)", err, payload)
	}
	if wire.Role != "user" {
		t.Fatalf("role = %q", wire.Role)
	}
	if len(wire.Content) != 3 {
		t.Fatalf("parts = %d, want 3: %s", len(wire.Content), payload)
	}
	if wire.Content[0].Type != "text" || wire.Content[0].Text != "what is this?" {
		t.Fatalf("text part = %+v", wire.Content[0])
	}
	if wire.Content[1].Type != "image_url" || wire.Content[1].ImageURL == nil ||
		wire.Content[1].ImageURL.URL != "data:image/png;base64,QUJD" {
		t.Fatalf("image part 1 = %+v", wire.Content[1])
	}
	if wire.Content[2].Type != "image_url" || wire.Content[2].ImageURL == nil ||
		wire.Content[2].ImageURL.URL != "data:image/jpeg;base64,REVG" {
		t.Fatalf("image part 2 = %+v", wire.Content[2])
	}
}

func TestMessageMarshalJSONStringContent(t *testing.T) {
	for _, m := range []Message{
		{Role: "user", Content: "hi"},
		// Names-only images (history reloads) must not produce parts.
		{Role: "user", Content: "hi", Images: []MessageImage{{Name: "shot.png"}}},
		{Role: "assistant", Content: "answer"},
		{Role: "tool", Content: "out", ToolCallID: "c1"},
	} {
		payload, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", m, err)
		}
		var wire struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(payload, &wire); err != nil {
			t.Fatalf("%+v: content must stay a string: %v (%s)", m, err, payload)
		}
		if wire.Content != m.Content {
			t.Fatalf("%+v: content = %q", m, wire.Content)
		}
	}
}

// TestRunWithImagesSendsParts drives a full Run with an image attachment
// and checks the provider receives the parts array.
func TestRunWithImagesSendsParts(t *testing.T) {
	var sawParts atomic.Bool
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err == nil {
			for _, m := range req.Messages {
				if m.Role == "user" && len(m.Content) > 0 && m.Content[0] == '[' {
					sawParts.Store(true)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunk("I see an image", "")+sseDone)
	}))
	defer prov.Close()

	cfg := testConfig(prov.URL, nil)
	sess := New(cfg)
	images := []MessageImage{{Name: "shot.png", Mime: "image/png", Data: "QUJD"}}
	if _, err := sess.RunWithImages(context.Background(), "look", images, nil); err != nil {
		t.Fatalf("RunWithImages: %v", err)
	}
	if !sawParts.Load() {
		t.Fatal("provider did not receive a parts-array user message")
	}
	// The history keeps the attachment for turn_start / persistence.
	hist := sess.History()
	if len(hist) == 0 || len(hist[0].Images) != 1 || hist[0].Images[0].Name != "shot.png" {
		t.Fatalf("history images = %+v", hist)
	}
}
