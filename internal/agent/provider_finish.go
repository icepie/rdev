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
	Code      string `json:"code"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
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

func providerStreamError(label string, detail providerErrorDetail) (err error) {
	defer func() {
		if detail.RequestID != "" {
			d := Diagnostic(err)
			d.RequestID = detail.RequestID
			err = &diagnosticError{cause: err, diagnostic: *d.Safe()}
		}
	}()
	if detail.contextOverflow() {
		return finishDiagnostic(fmt.Errorf("%w: %s: %s", ErrContextLengthExceeded, label, detail.Message), detail.Code, "")
	}
	for _, reason := range []string{detail.Code, detail.Type, detail.Message} {
		if strings.EqualFold(reason, "content_filter") || strings.EqualFold(reason, "content_filte") {
			return finishDiagnostic(ErrContentFiltered, reason, "")
		}
	}
	return finishDiagnostic(fmt.Errorf("agent: %s (%s): %s", label, detail.Type, detail.Message), detail.Code, "")
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
func finishReasonError(reason string) (err error) {
	defer func() { err = finishDiagnostic(err, reason, "") }()
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "", "stop", "end", "tool_calls", "function_call":
		return nil
	case "context_length_exceeded", "context_window_exceeded":
		return ErrContextLengthExceeded
	case "length", "max_tokens", "max_output_tokens":
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
func nativeFinishReasonError(reason string) (err error) {
	defer func() { err = finishDiagnostic(err, "", reason) }()
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
	if target != ErrContextLengthExceeded && target != ErrContentFiltered {
		return false
	}
	if target == ErrContextLengthExceeded && e.status == 413 {
		message := strings.ToLower(e.detail)
		if strings.Contains(message, "failed to buffer the request body") && strings.Contains(message, "length limit exceeded") {
			return true
		}
	}
	var envelope struct {
		Error providerErrorDetail `json:"error"`
		providerErrorDetail
	}
	if json.Unmarshal([]byte(e.detail), &envelope) != nil {
		return false
	}
	if target == ErrContentFiltered {
		for _, code := range []string{envelope.Error.Code, envelope.Error.Type, envelope.Code, envelope.Type} {
			if strings.EqualFold(code, "content_filter") || strings.EqualFold(code, "content_filte") {
				return true
			}
		}
		return false
	}
	return envelope.Error.contextOverflow() || envelope.providerErrorDetail.contextOverflow()
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
