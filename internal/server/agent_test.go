package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAgentQueueSteerOrdering drives the full server-side turn pipeline with
// a fake SSE provider: a chat that blocks mid-turn, a queued follow-up, and
// a steer that must cancel the running turn and jump the queue.
func TestAgentQueueSteerOrdering(t *testing.T) {
	var mu sync.Mutex
	var seen []string // last user message of every provider request
	var hits atomic.Int32
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		lastUser := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}
		mu.Lock()
		seen = append(seen, lastUser)
		mu.Unlock()

		if hits.Add(1) == 1 {
			// First turn: hang until the client cancels (steer/stop), then
			// return nothing.
			<-r.Context().Done()
			return
		}
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

	h.handleChat(ac, "zero", "", "", nil)

	// Wait until the first turn's provider request is actually in flight.
	waitFor(t, "first provider request", func() bool { return hits.Load() == 1 })

	h.handleChat(ac, "one", "", "", nil) // queued behind the running turn
	h.handleSteer(ac, "two", "")         // cancels the turn, jumps the queue

	// Wait for all three turns to settle.
	waitFor(t, "turns to finish", func() bool {
		srv.agentSessionsMu.Lock()
		defer srv.agentSessionsMu.Unlock()
		th := entry.threads[entry.current]
		return !th.running && len(th.queue) == 0 && hits.Load() == 3
	})

	mu.Lock()
	defer mu.Unlock()
	want := []string{"zero", "two", "one"}
	if !slices.Equal(seen, want) {
		t.Fatalf("provider saw user messages %v, want %v", seen, want)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestAgentPermNeedsApprovalMatrix is the three-mode approval matrix:
// full auto-approves everything, write asks only for dangerous tools, ask
// asks for every tool.
func TestAgentPermNeedsApprovalMatrix(t *testing.T) {
	cases := []struct {
		mode      string
		dangerous bool
		want      bool
	}{
		{agentPermFull, true, false},
		{agentPermFull, false, false},
		{agentPermWrite, true, true},
		{agentPermWrite, false, false},
		{agentPermAsk, true, true},
		{agentPermAsk, false, true},
	}
	for _, c := range cases {
		if got := agentPermNeedsApproval(c.mode, c.dangerous); got != c.want {
			t.Errorf("agentPermNeedsApproval(%q, dangerous=%v) = %v, want %v", c.mode, c.dangerous, got, c.want)
		}
	}
}

func TestParseAgentImages(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	mk := func(n int, data string) json.RawMessage {
		imgs := make([]agentImage, n)
		for i := range imgs {
			imgs[i] = agentImage{Name: fmt.Sprintf("img%d.png", i), Mime: "image/png", Data: data}
		}
		raw, _ := json.Marshal(imgs)
		return raw
	}

	t.Run("no images", func(t *testing.T) {
		imgs, err := parseAgentImages(nil)
		if err != nil || imgs != nil {
			t.Fatalf("parseAgentImages(nil) = %v, %v", imgs, err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		imgs, err := parseAgentImages(mk(2, png))
		if err != nil {
			t.Fatalf("parseAgentImages: %v", err)
		}
		if len(imgs) != 2 || imgs[0].Name != "img0.png" || imgs[0].Mime != "image/png" || imgs[0].Data != png {
			t.Fatalf("imgs = %+v", imgs)
		}
		if names := agentImageNames(imgs); !slices.Equal(names, []string{"img0.png", "img1.png"}) {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("too many", func(t *testing.T) {
		if _, err := parseAgentImages(mk(agentMaxImages+1, png)); err == nil {
			t.Fatal("expected error for more than 4 images")
		}
	})

	t.Run("too large", func(t *testing.T) {
		big := base64.StdEncoding.EncodeToString(make([]byte, agentMaxImageBytes+1))
		if _, err := parseAgentImages(mk(1, big)); err == nil {
			t.Fatal("expected error for image over 4MB decoded")
		}
	})

	t.Run("at the limit", func(t *testing.T) {
		max := base64.StdEncoding.EncodeToString(make([]byte, agentMaxImageBytes))
		if _, err := parseAgentImages(mk(agentMaxImages, max)); err != nil {
			t.Fatalf("4 images of exactly 4MB must pass: %v", err)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		if _, err := parseAgentImages(mk(1, "not!base64!")); err == nil {
			t.Fatal("expected error for invalid base64")
		}
	})
}
