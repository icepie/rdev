package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAgentAskToolRoundTrip drives a full turn against a fake provider: the
// model calls the ask tool, the browser answer (injected via
// handleAskResponse) becomes the tool result the model sees on the next
// round, and the turn completes.
func TestAgentAskToolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var toolResultSeen string
	var firstTools []string
	var hits atomic.Int32

	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)

		mu.Lock()
		if hits.Load() == 0 {
			for _, tl := range req.Tools {
				firstTools = append(firstTools, tl.Function.Name)
			}
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				toolResultSeen = m.Content
			}
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if hits.Add(1) == 1 {
			// Round 1: call the ask tool with two options.
			fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ask","arguments":"{\"question\":\"Pick one\",\"options\":[{\"label\":\"A\"},{\"label\":\"B\"}]}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		// Round 2: final answer.
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"done"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer prov.Close()

	srv := &Server{
		Agent: &AgentConfig{Providers: []AgentProvider{{
			ID:      "p",
			BaseURL: prov.URL,
			APIKey:  "k",
			Models:  []AgentModelConfig{{ID: "m"}},
		}}},
		AgentMaxTurns: 4,
	}
	entry, err := srv.agentSessionFor("dev1", &ClientConn{ID: "dev1"})
	if err != nil {
		t.Fatalf("agentSessionFor: %v", err)
	}
	h := &agentWSHandler{srv: srv}
	ac := &agentConn{deviceID: "dev1", entry: entry, current: entry.current}

	h.handleChat(ac, "hello", "", "", nil)

	// Wait for the ask question to go pending.
	var askID string
	waitFor(t, "ask question pending", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		th := entry.threads[entry.current]
		for id := range th.questions {
			askID = id
			return true
		}
		return false
	})

	// The browser answers "A".
	h.handleAskResponse(ac, askID, "A")

	waitFor(t, "turn to finish", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		th := entry.threads[entry.current]
		return !th.running
	})

	mu.Lock()
	defer mu.Unlock()
	if toolResultSeen != "A" {
		t.Fatalf("tool result seen by model = %q, want %q", toolResultSeen, "A")
	}
	for _, want := range []string{"ask", "web_fetch", "web_search", "edit", "read_file"} {
		if !slicesContains(firstTools, want) {
			t.Fatalf("provider tool list %v missing %q", firstTools, want)
		}
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestAgentUltrathinkEffort verifies the magic keyword: a chat message
// containing standalone "ultrathink" raises this turn's reasoning_effort to
// high; a message quoting it inside code keeps the configured level.
func TestAgentUltrathinkEffort(t *testing.T) {
	var mu sync.Mutex
	var efforts []string

	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		efforts = append(efforts, req.ReasoningEffort)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer prov.Close()

	srv := &Server{
		Agent: &AgentConfig{Providers: []AgentProvider{{
			ID:      "p",
			BaseURL: prov.URL,
			APIKey:  "k",
			Models:  []AgentModelConfig{{ID: "m"}},
		}}},
		AgentMaxTurns: 4,
	}
	entry, err := srv.agentSessionFor("dev1", &ClientConn{ID: "dev1"})
	if err != nil {
		t.Fatalf("agentSessionFor: %v", err)
	}
	h := &agentWSHandler{srv: srv}
	ac := &agentConn{deviceID: "dev1", entry: entry, current: entry.current}

	waitTurn := func() {
		waitFor(t, "turn to finish", func() bool {
			srv.agentSessionsMu.Lock()
			defer srv.agentSessionsMu.Unlock()
			th := entry.threads[entry.current]
			return !th.running
		})
	}

	h.handleChat(ac, "ultrathink: design the schema", "", "", nil)
	waitTurn()
	h.handleChat(ac, "run `ultrathink` for me", "", "", nil)
	waitTurn()

	mu.Lock()
	defer mu.Unlock()
	if len(efforts) != 2 {
		t.Fatalf("provider hits = %d, want 2", len(efforts))
	}
	if efforts[0] != "high" {
		t.Fatalf("ultrathink turn effort = %q, want high", efforts[0])
	}
	// The configured level (default "medium") survives on the plain turn.
	if efforts[1] != "medium" {
		t.Fatalf("quoted-keyword turn effort = %q, want medium (configured)", efforts[1])
	}
}

// TestWebFetchToolConversion fetches an HTML fixture through web_fetch and
// checks the markdown-ish conversion (headings, links, script stripping).
func TestWebFetchToolConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>t</title><script>var x=1;</script></head>
<body><h1>Hello</h1><p>see <a href="/about">the about page</a> now</p></body></html>`)
	}))
	defer srv.Close()

	out, err := (&webFetchTool{}).Execute(t.Context(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "# Hello") {
		t.Fatalf("missing heading, out = %q", out)
	}
	if !strings.Contains(out, "[the about page]("+srv.URL+"/about)") {
		t.Fatalf("missing resolved link, out = %q", out)
	}
	if strings.Contains(out, "var x=1") {
		t.Fatalf("script leaked into output")
	}
}

// TestWebSearchParse checks the DuckDuckGo HTML result extraction against a
// fixture shaped like the real endpoint.
func TestWebSearchParse(t *testing.T) {
	fixture := `<html><body>
<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fone">First result</a>
<a class="result__snippet">snippet one</a></div>
<div class="result"><a class="result__a" href="https://example.com/two">Second result</a>
<a class="result__snippet">snippet two</a></div>
</body></html>`
	hits := parseDDGResults(fixture, 10)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].title != "First result" || hits[0].url != "https://example.com/one" || hits[0].snippet != "snippet one" {
		t.Fatalf("hit0 = %+v", hits[0])
	}
	if hits[1].url != "https://example.com/two" {
		t.Fatalf("hit1 = %+v", hits[1])
	}
	if capped := parseDDGResults(fixture, 1); len(capped) != 1 {
		t.Fatalf("limit not applied: %d", len(capped))
	}
}
