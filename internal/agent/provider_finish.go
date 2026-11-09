package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Ported from OMP v18.18.3 openai-completions/native-finish-reason. Filtering
// remains a terminal provider outcome, never a successful or retryable answer.
var ErrContentFiltered = errors.New("provider stopped this response due to content filtering; any partial output is preserved and unfinished tool calls were not executed")
var errResponseLength = errors.New("agent: response truncated (length limit)")

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
func retryEmptyProviderResponse(err error) bool {
	if errors.Is(err, ErrContentFiltered) || errors.Is(err, errResponseLength) {
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
