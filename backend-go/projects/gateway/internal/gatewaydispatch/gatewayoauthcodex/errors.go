package gatewayoauthcodex

import (
	"errors"
)

// OpenAI OAuth Codex adapter error taxonomy, migrated from
// adapters/gpt-codex/oauth-errors.ts (REFACTOR-0006 阶段 A 自 gatewaydispatch
// errors.go 随 oauth/codex 族迁入；dispatch 门面经桥 alias/转发保持消费方零改动).

// OpenAIOAuthCodexAdapterError mirrors adapters/gpt-codex/oauth-errors.ts.
type OpenAIOAuthCodexAdapterError struct {
	Message       string
	Code          string
	StatusCode    int
	Type          string
	AccountScoped bool
}

// NewOpenAIOAuthCodexAdapterError mirrors the constructor defaults.
func NewOpenAIOAuthCodexAdapterError(message string, options ...CodexAdapterErrorOption) *OpenAIOAuthCodexAdapterError {
	err := &OpenAIOAuthCodexAdapterError{
		Message:    message,
		Code:       "invalid_openai_oauth_codex_request",
		StatusCode: 400,
		Type:       "invalid_request_error",
	}
	for _, option := range options {
		option(err)
	}
	return err
}

// CodexAdapterErrorOption mirrors the constructor options bag.
type CodexAdapterErrorOption func(*OpenAIOAuthCodexAdapterError)

// WithCodexAdapterCode overrides the error code.
func WithCodexAdapterCode(code string) CodexAdapterErrorOption {
	return func(e *OpenAIOAuthCodexAdapterError) { e.Code = code }
}

// WithCodexAdapterStatus overrides the HTTP status.
func WithCodexAdapterStatus(statusCode int) CodexAdapterErrorOption {
	return func(e *OpenAIOAuthCodexAdapterError) { e.StatusCode = statusCode }
}

// WithCodexAdapterType overrides the error type.
func WithCodexAdapterType(errorType string) CodexAdapterErrorOption {
	return func(e *OpenAIOAuthCodexAdapterError) { e.Type = errorType }
}

// WithCodexAdapterAccountScoped mirrors options.accountScoped === true.
func WithCodexAdapterAccountScoped() CodexAdapterErrorOption {
	return func(e *OpenAIOAuthCodexAdapterError) { e.AccountScoped = true }
}

func (e *OpenAIOAuthCodexAdapterError) Error() string { return e.Message }

// IsOpenAIOAuthCodexAdapterError mirrors the Node instanceof checks.
func IsOpenAIOAuthCodexAdapterError(err error) bool {
	var target *OpenAIOAuthCodexAdapterError
	return errors.As(err, &target)
}
