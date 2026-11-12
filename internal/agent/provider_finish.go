package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Ported from OMP v18.18.3 openai-completions/native-finish-reason. Filtering
// remains a terminal provider outcome, never a successful or retryable answer.
var ErrContentFiltered = errors.New("provider stopped this response due to content filtering; any partial output is preserved and unfinished tool calls were not executed")
var errResponseLength = errors.New("agent: response truncated (length limit)")

// ErrContextLengthExceeded identifies rejected input, not an output-token limit
// or a generic invalid request. Callers may compact only before any progress.
var ErrContextLengthExceeded = errors.New("agent: provider context length exceeded")

type providerErrorDetail struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e providerErrorDetail) contextOverflow() bool {
	for _, code := range []string{e.Code, e.Type} {
		switch strings.ToLower(code) {
		case "context_length_exceeded", "context_window_exceeded", "prompt_too_long":
			return true
		}
	}
	// Anthropic reports this as invalid_request_error without a distinct code.
	message := strings.ToLower(strings.TrimSpace(e.Message))
	return (e.Type == "invalid_request_error" || e.Code == "invalid_request_error") &&
		(strings.HasPrefix(message, "prompt is too long") || strings.HasPrefix(message, "input is too long") || strings.HasPrefix(message, "your prompt is too long"))
}

func providerStreamError(label string, detail providerErrorDetail) error {
	if detail.contextOverflow() {
		return fmt.Errorf("%w: %s: %s", ErrContextLengthExceeded, label, detail.Message)
	}
	return fmt.Errorf("agent: %s (%s): %s", label, detail.Type, detail.Message)
}

type providerFinishError struct {
	reason    string
	transient bool
}

func (e *providerFinishError) Error() string {
	if e.transient {
		return "agent: provider returned error finish_reason: " + e.reason
	}
	return "agent: provider finish_reason: " + e.reason
}
func finishReasonError(reason string) error {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "stop", "end", "tool_calls", "function_call":
		return nil
	case "context_length_exceeded", "context_window_exceeded":
		return ErrContextLengthExceeded
	case "length", "max_tokens":
		return errResponseLength
	// Some compatible gateways truncate this spelling; it is still a refusal.
	case "content_filter", "content_filte":
		return ErrContentFiltered
	case "network_error", "error", "insufficient_system_resource", "unknown", "other", "unspecified", "finish_reason_unspecified":
		return &providerFinishError{reason: reason, transient: true}
	default:
		return &providerFinishError{reason: reason}
	}
}
func nativeFinishReasonError(reason string) error {
	switch strings.ToUpper(reason) {
	case "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "NETWORK_ERROR":
		return &providerFinishError{reason: "native_finish_reason: " + reason, transient: true}
	}
	return nil
}

type providerHTTPError struct {
	status int
	detail string
}

func (e *providerHTTPError) Error() string {
	return fmt.Sprintf("agent: chat request failed: %d: %s", e.status, e.detail)
}
func (e *providerHTTPError) Is(target error) bool {
	if target != ErrContextLengthExceeded {
		return false
	}
	var envelope struct {
		Error providerErrorDetail `json:"error"`
		providerErrorDetail
	}
	return json.Unmarshal([]byte(e.detail), &envelope) == nil &&
		(envelope.Error.contextOverflow() || envelope.providerErrorDetail.contextOverflow())
}
func retryEmptyProviderResponse(err error) bool {
	if errors.Is(err, ErrContentFiltered) || errors.Is(err, errResponseLength) || errors.Is(err, ErrContextLengthExceeded) {
		return false
	}
	var finish *providerFinishError
	if errors.As(err, &finish) {
		return finish.transient
	}
	var httpErr *providerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.status == 408 || httpErr.status == 409 || httpErr.status == 429 || httpErr.status >= 500
	}
	return true
}
