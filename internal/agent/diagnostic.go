package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FailureDiagnostic is the durable, allowlisted description of a failed round.
// It deliberately has no error-body, prompt, credential or tool-argument field.
// Progress describes the failed provider round, not earlier successful rounds.
type FailureDiagnostic struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	API                string `json:"api,omitempty"`
	FinishReason       string `json:"finishReason,omitempty"`
	RawFinishReason    string `json:"rawFinishReason,omitempty"`
	NativeFinishReason string `json:"nativeFinishReason,omitempty"`
	RequestID          string `json:"requestId,omitempty"`
	HTTPStatus         int    `json:"httpStatus,omitempty"`
	TextStarted        bool   `json:"textStarted"`
	ReasoningStarted   bool   `json:"reasoningStarted"`
	ToolCallStarted    bool   `json:"toolCallStarted"`
}

// Safe returns an independent bounded snapshot, including for older/untrusted
// history records. Metadata must be an identifier, never arbitrary error prose.
func (d *FailureDiagnostic) Safe() *FailureDiagnostic {
	if d == nil {
		return nil
	}
	out := *d
	switch out.Code {
	case "provider_content_filter", "provider_context_length", "provider_output_limit", "provider_http_error", "provider_error", "agent_canceled", "agent_error":
	default:
		out.Code = "agent_error"
	}
	for _, field := range []*string{&out.Provider, &out.Model, &out.API, &out.FinishReason, &out.RawFinishReason, &out.NativeFinishReason, &out.RequestID} {
		*field = diagnosticIdentifier(*field)
	}
	if out.HTTPStatus < 100 || out.HTTPStatus > 599 {
		out.HTTPStatus = 0
	}
	out.Message = out.Summary()
	return &out
}

func diagnosticIdentifier(value string) string {
	if len(value) > 160 {
		return ""
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-_.:/", c)) {
			return ""
		}
	}
	return value
}

// Summary is safe to display on both live failures and restored conversations.
func (d *FailureDiagnostic) Summary() string {
	if d == nil {
		return "Agent execution failed."
	}
	var message string
	switch d.Code {
	case "provider_content_filter":
		message = "The provider stopped the response due to content filtering. Partial output was preserved; unfinished tool calls were not executed."
	case "provider_context_length":
		message = "The provider rejected the request because its context limit was exceeded."
	case "provider_output_limit":
		message = "The provider reached its output limit. Partial output was preserved; unfinished tool calls were not executed."
	case "agent_canceled":
		message = "Agent execution was interrupted."
	case "provider_http_error":
		message = fmt.Sprintf("The provider request failed (HTTP %d).", d.HTTPStatus)
	case "provider_error":
		message = "The provider response failed or ended before completion. Partial output was preserved; unfinished tool calls were not executed."
	default:
		message = "Agent execution failed."
	}
	return message
}

type diagnosticError struct {
	cause      error
	diagnostic FailureDiagnostic
}

func (e *diagnosticError) Error() string { return e.cause.Error() }
func (e *diagnosticError) Unwrap() error { return e.cause }

// Diagnostic extracts only structured metadata; it never copies err.Error().
func Diagnostic(err error) *FailureDiagnostic {
	if err == nil {
		return nil
	}
	var wrapped *diagnosticError
	if errors.As(err, &wrapped) {
		return wrapped.diagnostic.Safe()
	}
	d := &FailureDiagnostic{Code: "agent_error"}
	switch {
	case errors.Is(err, ErrContentFiltered):
		d.Code = "provider_content_filter"
	case errors.Is(err, ErrContextLengthExceeded):
		d.Code = "provider_context_length"
	case errors.Is(err, errResponseLength):
		d.Code = "provider_output_limit"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		d.Code = "agent_canceled"
	}
	return d.Safe()
}

func finishDiagnostic(err error, reason, native string) error {
	if err == nil {
		return nil
	}
	d := Diagnostic(err)
	if d.Code == "agent_error" {
		d.Code = "provider_error"
	}
	if reason != "" {
		d.RawFinishReason = reason
		d.FinishReason = strings.ToLower(strings.TrimSpace(reason))
		if d.FinishReason == "content_filte" {
			d.FinishReason = "content_filter"
		}
	}
	if native != "" {
		d.NativeFinishReason = native
	}
	return &diagnosticError{cause: err, diagnostic: *d.Safe()}
}

func providerDiagnostic(err error, cfg Config, assistant Message, requestID string) error {
	if err == nil {
		return nil
	}
	d := Diagnostic(err)
	if d.Code == "agent_error" {
		d.Code = "provider_error"
	}
	var httpErr *providerHTTPError
	if errors.As(err, &httpErr) {
		d.HTTPStatus = httpErr.status
		if d.Code == "provider_error" {
			d.Code = "provider_http_error"
		}
		// Some providers return the request identifier only in their error
		// envelope. Decode that field alone; never retain the body as metadata.
		var envelope struct {
			RequestID string `json:"request_id"`
			Error     struct {
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(httpErr.detail), &envelope) == nil {
			d.RequestID = envelope.RequestID
			if d.RequestID == "" {
				d.RequestID = envelope.Error.RequestID
			}
		}
	}
	d.Provider, d.Model, d.API = cfg.ProviderID, cfg.Model, cfg.API
	if requestID != "" {
		d.RequestID = requestID
	}
	if d.API == "" {
		d.API = "openai-completions"
	}
	d.TextStarted = assistant.Content != ""
	d.ReasoningStarted = assistant.Reasoning != ""
	d.ToolCallStarted = d.ToolCallStarted || len(assistant.ToolCalls) != 0
	// A gateway may echo a credential into a nominal metadata field. Remove
	// the configured secret before the allowlist/size check, not just from logs.
	if cfg.APIKey != "" {
		for _, field := range []*string{&d.Provider, &d.Model, &d.API, &d.FinishReason, &d.RawFinishReason, &d.NativeFinishReason, &d.RequestID} {
			if strings.Contains(*field, cfg.APIKey) {
				*field = ""
			}
		}
	}
	return &diagnosticError{cause: err, diagnostic: *d.Safe()}
}
