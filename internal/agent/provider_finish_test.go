package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilteredResponseNeverRetriesOrExecutesTools(t *testing.T) {
	for _, reason := range []string{"content_filter", "CONTENT_FILTER", "content_filte"} {
		t.Run(reason, func(t *testing.T) {
			provider, hits := newFakeProvider(t, finishChunk(reason)+sseDone)
			tool := &fakeTool{name: "probe"}
			s := New(testConfig(provider.URL, []Tool{tool}))
			_, err := s.Run(context.Background(), "request", nil)
			if !errors.Is(err, ErrContentFiltered) || hits.Load() != 1 || tool.ran {
				t.Fatalf("err=%v requests=%d ran=%v", err, hits.Load(), tool.ran)
			}
			if history := s.History(); len(history) != 1 || history[0].Content != "request" {
				t.Fatalf("filtered user request lost: %+v", history)
			}
		})
	}
}
func TestNativeFinishFailureCannotBeOverwrittenByCleanStop(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"partial"},"finish_reason":"stop","native_finish_reason":"MALFORMED_FUNCTION_CALL"}]}` + "\n\n" + finishChunk("stop") + sseDone
	message, _, err := readStream(strings.NewReader(stream), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "MALFORMED_FUNCTION_CALL") || message.Content != "partial" {
		t.Fatalf("%+v %v", message, err)
	}
	for _, native := range []string{"STOP", "FUTURE_VENDOR_REASON"} {
		if err := nativeFinishReasonError(native); err != nil {
			t.Fatalf("unrecognized native reason must not imply failure: %v", err)
		}
	}
}
func TestReasoningAliasesAndRefusalText(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning_content":"first","reasoning":"duplicate","reasoning_text":"duplicate"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_text":" second","refusal":"Cannot provide this response."},"finish_reason":"content_filter"}]}` + "\n\n" + sseDone
	message, _, err := readStream(strings.NewReader(stream), nil, nil, nil)
	if !errors.Is(err, ErrContentFiltered) || message.Reasoning != "first second" || message.Content != "Cannot provide this response." {
		t.Fatalf("%+v %v", message, err)
	}
}
func TestPermanentHTTPFailureIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(401)
		fmt.Fprint(w, "invalid API key")
	}))
	defer provider.Close()
	_, err := New(testConfig(provider.URL, nil)).Run(context.Background(), "request", nil)
	if err == nil || hits.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, hits.Load())
	}
}
func TestCompletedStreamDoesNotWaitForGatewayEOF(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunk("complete", "")+finishChunk("stop")+`data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	message, usage, err := streamChat(ctx, New(testConfig(provider.URL, nil)).cfg, nil, nil, nil)
	if err != nil || message.Content != "complete" || usage.InputTokens != 7 || usage.OutputTokens != 2 || ctx.Err() != nil {
		t.Fatalf("message=%+v usage=%+v err=%v", message, usage, err)
	}
}
