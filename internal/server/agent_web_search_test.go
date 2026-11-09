package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchReturnsAvailableBackendAndCancelsSlowOne(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(started)
			<-r.Context().Done()
			close(stopped)
			return
		}
		<-started
		if r.URL.Query().Get("q") != "中文 查询" {
			t.Error("query not preserved")
		}
		w.Write([]byte("<rss><channel><item><title>招聘</title><link>https://example.com/job</link><description>详情</description></item></channel></rss>"))
	}))
	defer srv.Close()
	tool := webSearchTool{client: srv.Client(), backends: []webSearchBackend{
		{"slow", srv.URL + "/slow?q=", parseDDGResults},
		{"rss", srv.URL + "/rss?q=", parseBingRSS},
	}}
	hits, source, err := tool.search(context.Background(), "中文 查询", 1)
	if err != nil || len(hits) != 1 || source != "rss" {
		t.Fatalf("%v %s %v", hits, source, err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("losing request not canceled")
	}
}

func TestSearchRejectsChallengeAndHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			w.WriteHeader(429)
			return
		}
		w.Write([]byte("<html>Verify you are human</html>"))
	}))
	defer srv.Close()
	tool := webSearchTool{client: srv.Client(), backends: []webSearchBackend{
		{"blocked", srv.URL + "/blocked?q=", parseBingRSS},
		{"challenge", srv.URL + "/challenge?q=", parseDDGResults},
	}}
	_, _, err := tool.search(context.Background(), "query", 10)
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "no usable results") {
		t.Fatalf("missing backend diagnostics: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := tool.search(ctx, "query", 10); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestBingRSSParsing(t *testing.T) {
	doc := "<rss><channel><item><title>A &amp; B</title><link>https://example.com</link><description>&lt;b&gt;snippet&lt;/b&gt;</description></item><item><title>second</title></item></channel></rss>"
	hits := parseBingRSS(doc, 1)
	if len(hits) != 1 || hits[0].title != "A & B" || hits[0].snippet != "snippet" {
		t.Fatalf("%+v", hits)
	}
	if hits := parseBingRSS("<html>challenge</html>", 10); len(hits) != 0 {
		t.Fatalf("%+v", hits)
	}
}
