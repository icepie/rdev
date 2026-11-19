package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFailureDiagnosticPreservesFilteredPartialRound(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses"} {
		t.Run(api, func(t *testing.T) {
			var requests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("x-request-id", "req-diagnostic-1")
				if api == "openai-responses" {
					fmt.Fprint(w, responseSSE(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]string{"type": "function_call", "call_id": "pending", "name": "probe", "arguments": "{"}}))
					fmt.Fprint(w, responseSSE(map[string]string{"type": "response.output_text.delta", "delta": "partial"}))
					fmt.Fprint(w, responseSSE(map[string]string{"type": "response.reasoning_summary_text.delta", "delta": "thinking"}))
					fmt.Fprint(w, responseSSE(map[string]any{"type": "response.incomplete", "response": map[string]any{"incomplete_details": map[string]string{"reason": "content_filte"}}}))
				} else {
					fmt.Fprint(w, sseChunk("partial", "thinking", sseToolCall("pending", "probe", "{"))+finishChunk("content_filte")+sseDone)
				}
			}))
			defer provider.Close()
			tool := &fakeTool{name: "probe"}
			cfg := testConfig(provider.URL, []Tool{tool})
			cfg.API, cfg.ProviderID, cfg.Model = api, "fixture", "model-1"
			session := New(cfg)
			_, err := session.Run(context.Background(), "private prompt", nil)
			if !errors.Is(err, ErrContentFiltered) || requests.Load() != 1 || tool.ran {
				t.Fatalf("filter retried or executed incomplete tool: err=%v requests=%d tool=%v", err, requests.Load(), tool.ran)
			}
			d := Diagnostic(err)
			if d == nil || d.Code != "provider_content_filter" || d.Provider != "fixture" || d.Model != "model-1" || d.API != api || d.RawFinishReason != "content_filte" || d.FinishReason != "content_filter" || d.RequestID != "req-diagnostic-1" || !d.TextStarted || !d.ReasoningStarted || !d.ToolCallStarted {
				t.Fatalf("lost diagnostic: %+v", d)
			}
			history := session.History()
			if len(history) != 2 || history[1].Content != "partial" || history[1].Reasoning != "thinking" || len(history[1].ToolCalls) != 0 {
				t.Fatalf("partial history changed: %+v", history)
			}
		})
	}
}

func TestFailureDiagnosticRedactionAndBounds(t *testing.T) {
	secret := "sk-private-key"
	body := `{"error":{"message":"private prompt and tool arguments ` + secret + `"}}`
	cfg := Config{APIKey: secret, ProviderID: "provider", Model: "model", API: "openai-completions"}
	err := providerDiagnostic(&providerHTTPError{status: 401, detail: body}, cfg, Message{}, "request-"+secret)
	d := Diagnostic(err)
	if d.RequestID != "" || d.HTTPStatus != 401 || d.Code != "provider_http_error" {
		t.Fatalf("incorrect redaction: %+v", d)
	}
	raw, _ := json.Marshal(d)
	for _, forbidden := range []string{secret, "private prompt", "tool arguments", body} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, raw)
		}
	}
	d.Model = strings.Repeat("m", 10000)
	d.RawFinishReason = "prompt\nAuthorization: secret"
	d.Message = strings.Repeat("private body", 10000)
	d.RequestID = strings.Repeat("r", 10000)
	bounded := d.Safe()
	raw, _ = json.Marshal(bounded)
	if len(raw) > 2048 || bounded.Model != "" || bounded.RawFinishReason != "" || bounded.RequestID != "" || strings.Contains(string(raw), "private body") {
		t.Fatalf("unbounded diagnostic: %s", raw)
	}
	if got := Diagnostic(errors.New(body)); got.Code != "agent_error" || strings.Contains(got.Message, secret) {
		t.Fatalf("generic error copied raw body: %+v", got)
	}
}

func TestFailureDiagnosticNativeReasonAndErrorContracts(t *testing.T) {
	for _, tc := range []struct {
		reason string
		target error
	}{{"content_filter", ErrContentFiltered}, {"MAX_TOKENS", errResponseLength}, {"context_length_exceeded", ErrContextLengthExceeded}} {
		err := finishReasonError(tc.reason)
		if !errors.Is(err, tc.target) || retryEmptyProviderResponse(err) {
			t.Fatalf("changed permanent error contract: %s: %v", tc.reason, err)
		}
	}
	err := nativeFinishReasonError("MALFORMED_FUNCTION_CALL")
	var finish *providerFinishError
	if !errors.As(err, &finish) || !retryEmptyProviderResponse(err) || Diagnostic(err).NativeFinishReason != "MALFORMED_FUNCTION_CALL" {
		t.Fatalf("changed transient native contract: %v", err)
	}
}

func TestResponsesBuildersKeepPrefixesAndPartialFailures(t *testing.T) {
	var input strings.Builder
	for range 512 {
		input.WriteString(responseSSE(map[string]string{"type": "response.output_text.delta", "delta": "t"}))
		input.WriteString(responseSSE(map[string]string{"type": "response.reasoning_summary_text.delta", "delta": "r"}))
	}
	text, reasoning := strings.Repeat("t", 512), strings.Repeat("r", 512)
	for _, mismatch := range []bool{false, true} {
		finalText := text + "suffix"
		if mismatch {
			finalText = "different"
		}
		completion := responseComplete([]any{responseText(finalText), map[string]any{"type": "reasoning", "summary": []any{map[string]string{"text": reasoning + "end"}}}})
		var gotText, gotReasoning strings.Builder
		message, _, err := readResponsesStream(strings.NewReader(input.String()+completion), "scope", func(s string) { gotText.WriteString(s) }, func(s string) { gotReasoning.WriteString(s) }, nil)
		if mismatch {
			if err == nil || message.Content != text || message.Reasoning != reasoning || gotText.String() != text || gotReasoning.String() != reasoning {
				t.Fatalf("mismatched final replaced partials: %+v %v", message, err)
			}
		} else if err != nil || message.Content != finalText || message.Reasoning != reasoning+"end" || gotText.String() != message.Content || gotReasoning.String() != message.Reasoning {
			t.Fatalf("builder lost output or suffix: %+v %v", message, err)
		}
	}
}

func TestFailureDiagnosticHTTPFilterAndEnvelopeRequestID(t *testing.T) {
	err := &providerHTTPError{status: 503, detail: `{"request_id":"req-body","error":{"code":"content_filter","message":"private prompt"}}`}
	if !errors.Is(err, ErrContentFiltered) || retryEmptyProviderResponse(err) {
		t.Fatal("structured HTTP filtering must not retry even with a transient status")
	}
	d := Diagnostic(providerDiagnostic(err, Config{Model: "fixture"}, Message{}, ""))
	if d.Code != "provider_content_filter" || d.RequestID != "req-body" || d.HTTPStatus != 503 {
		t.Fatalf("lost HTTP filtering metadata: %+v", d)
	}
	streamErr := providerStreamError("provider error", providerErrorDetail{Code: "content_filter", RequestID: "req-stream", Message: "private prompt"})
	d = Diagnostic(providerDiagnostic(streamErr, Config{Model: "fixture"}, Message{}, ""))
	if d.RequestID != "req-stream" || d.Code != "provider_content_filter" || strings.Contains(d.Message, "private prompt") {
		t.Fatalf("lost stream metadata or leaked body: %+v", d)
	}
}

func TestFailureDiagnosticAnthropicToolFragment(t *testing.T) {
	input := responseSSE(map[string]any{"type": "message_start", "message": map[string]any{}}) +
		responseSSE(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "pending", "name": "probe", "input": map[string]any{}}}) +
		responseSSE(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": "{"}}) +
		responseSSE(map[string]any{"type": "error", "request_id": "req-anthropic", "error": map[string]string{"type": "overloaded_error", "message": "private failure"}})
	message, _, err := readAnthropicStream(strings.NewReader(input), "scope", nil, nil, nil)
	if err == nil || len(message.ToolCalls) != 0 {
		t.Fatalf("partial tool became executable: %+v %v", message, err)
	}
	d := Diagnostic(providerDiagnostic(err, Config{API: "anthropic-messages"}, message, ""))
	if !d.ToolCallStarted || d.RequestID != "req-anthropic" || d.TextStarted {
		t.Fatalf("lost Anthropic fragment progress: %+v", d)
	}
}
