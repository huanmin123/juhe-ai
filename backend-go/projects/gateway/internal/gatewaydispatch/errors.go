package gatewaydispatch

import (
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// Error taxonomy of the upstream/dispatch layers, migrated from
// upstream/request.ts, upstream/first-byte-timeout.ts,
// upstream/body.ts, upstream/first-byte-deadline.ts and
// dispatch/upstream-dispatch.ts.
//
// Node tracks "the request reached the upstream" with WeakSets attached to
// error objects. Go errors are values, so the same facts are carried as
// wrapper error types (StartedTransportError / body transport marker) and
// inspected with errors.As — the predicates keep the Node names.

// UpstreamRequestTimeoutError mirrors upstream/request.ts
// UpstreamRequestTimeoutError.
type UpstreamRequestTimeoutError struct{ Message string }

func (e *UpstreamRequestTimeoutError) Error() string { return e.Message }

// UpstreamRequestAbortedError mirrors UpstreamRequestAbortedError.
// UpstreamRequestStarted mirrors the constructor flag: the abort happened
// after the request reached the upstream.
type UpstreamRequestAbortedError struct {
	Message                string
	UpstreamRequestStarted bool
}

func (e *UpstreamRequestAbortedError) Error() string { return e.Message }

// FirstByteTimeoutSource mirrors the source union.
type FirstByteTimeoutSource string

const (
	FirstByteTimeoutSourceHardTimeout       FirstByteTimeoutSource = "hard_timeout"
	FirstByteTimeoutSourceConfiguredDeadline FirstByteTimeoutSource = "configured_deadline"
)

// GatewayFirstByteTimeoutError mirrors upstream/first-byte-timeout.ts.
type GatewayFirstByteTimeoutError struct {
	Message  string
	TimeoutMs int64
	Source   FirstByteTimeoutSource
}

func (e *GatewayFirstByteTimeoutError) Error() string { return e.Message }

// Code mirrors the readonly code property.
func (e *GatewayFirstByteTimeoutError) Code() string { return "first_byte_timeout" }

// IsGatewayFirstByteTimeoutError mirrors isGatewayFirstByteTimeoutError.
func IsGatewayFirstByteTimeoutError(err error) bool {
	var target *GatewayFirstByteTimeoutError
	return errors.As(err, &target)
}

// GatewayResponsePrecommitDeadlineError mirrors
// upstream/first-byte-deadline.ts GatewayResponsePrecommitDeadlineError.
type GatewayResponsePrecommitDeadlineError struct {
	DeadlineAtMs int64
}

func (e *GatewayResponsePrecommitDeadlineError) Error() string {
	return "网关请求墙钟已到，响应尚未产生可提交的语义结果"
}

// Code mirrors the readonly code property.
func (e *GatewayResponsePrecommitDeadlineError) Code() string {
	return "gateway_request_wall_budget_exhausted"
}

// IsGatewayResponsePrecommitDeadlineError mirrors the Node predicate.
func IsGatewayResponsePrecommitDeadlineError(err error) bool {
	var target *GatewayResponsePrecommitDeadlineError
	return errors.As(err, &target)
}

// StartedTransportError marks a transport failure observed after the request
// reached the upstream (Node: startedUpstreamTransportErrors WeakSet).
type StartedTransportError struct{ Err error }

func (e *StartedTransportError) Error() string { return e.Err.Error() }
func (e *StartedTransportError) Unwrap() error { return e.Err }

// IsStartedUpstreamTransportError mirrors isStartedUpstreamTransportError.
// Locally terminated requests (client abort, configured first-byte deadline)
// are never marked started, mirroring markLocallyTerminatedUpstreamRequestError.
func IsStartedUpstreamTransportError(err error) bool {
	var target *StartedTransportError
	return errors.As(err, &target)
}

// StartedBodyTransportError marks a transport failure observed while
// consuming an already-started upstream response body (Node:
// startedUpstreamBodyTransportErrors WeakSet minus unsupported-encoding and
// locally terminated errors).
type StartedBodyTransportError struct{ Err error }

func (e *StartedBodyTransportError) Error() string { return e.Err.Error() }
func (e *StartedBodyTransportError) Unwrap() error { return e.Err }

// PrimaryStartedGatewayTransportError mirrors the secondary WeakSet in
// dispatch/upstream-attempts.ts: a started transport failure that escaped
// performUpstreamRequestAttempt.
type PrimaryStartedGatewayTransportError struct{ Err error }

func (e *PrimaryStartedGatewayTransportError) Error() string { return e.Err.Error() }
func (e *PrimaryStartedGatewayTransportError) Unwrap() error { return e.Err }

// IsPrimaryStartedGatewayTransportError mirrors
// isPrimaryStartedGatewayTransportError.
func IsPrimaryStartedGatewayTransportError(err error) bool {
	var target *PrimaryStartedGatewayTransportError
	return errors.As(err, &target)
}

// UpstreamBodyReadIncompleteError mirrors upstream/body.ts.
type UpstreamBodyReadIncompleteError struct{ Cause error }

func (e *UpstreamBodyReadIncompleteError) Error() string {
	if e.Cause != nil && timeoutLikeText(e.Cause.Error()) {
		return "上游响应正文读取超时"
	}
	return "上游响应正文读取未完成"
}

func (e *UpstreamBodyReadIncompleteError) Code() string { return "UPSTREAM_BODY_READ_INCOMPLETE" }
func (e *UpstreamBodyReadIncompleteError) Unwrap() error { return e.Cause }

// UpstreamBodyReadMaxLifetimeError mirrors upstream/body.ts.
type UpstreamBodyReadMaxLifetimeError struct{ TimeoutMs int64 }

func (e *UpstreamBodyReadMaxLifetimeError) Error() string {
	return fmt.Sprintf("上游非流式响应正文读取超时（绝对上限 %ds）", int64CeilDiv(e.TimeoutMs, 1000))
}

func (e *UpstreamBodyReadMaxLifetimeError) Code() string { return "UPSTREAM_BODY_READ_MAX_LIFETIME" }

// NonStreamUpstreamBodyPipeError mirrors upstream/body.ts.
type NonStreamUpstreamBodyPipeError struct {
	Message       string
	PartialResult NonStreamPipeResult
	OriginalError error
}

func (e *NonStreamUpstreamBodyPipeError) Error() string { return e.Message }
func (e *NonStreamUpstreamBodyPipeError) Unwrap() error { return e.OriginalError }

// IsProvenUpstreamBodyTransportError mirrors isProvenUpstreamBodyTransportError:
// only failures proven to have happened while consuming an already-started
// upstream response body count.
func IsProvenUpstreamBodyTransportError(err error) bool {
	var started *StartedBodyTransportError
	if errors.As(err, &started) {
		return true
	}
	var incomplete *UpstreamBodyReadIncompleteError
	if errors.As(err, &incomplete) {
		return IsProvenUpstreamBodyTransportError(incomplete.Cause)
	}
	var pipe *NonStreamUpstreamBodyPipeError
	if errors.As(err, &pipe) {
		return IsProvenUpstreamBodyTransportError(pipe.OriginalError)
	}
	return false
}

// UnsupportedUpstreamResponseEncodingError mirrors upstream/request.ts.
type UnsupportedUpstreamResponseEncodingError struct{ Message string }

func (e *UnsupportedUpstreamResponseEncodingError) Error() string { return e.Message }

// UpstreamAttemptError mirrors dispatch/upstream-dispatch.ts
// UpstreamAttemptError: the engine exhausted every candidate.
type UpstreamAttemptError struct {
	Message                 string
	LastAttempt             *UpstreamAttempt
	FailedAccountIDs        []string
	AgentGuidanceResponse   *gatewaypreauth.GatewayAgentGuidanceResponse
	RecoverableAccountIDs   []string
	TerminalUpstreamFailure bool
}

func (e *UpstreamAttemptError) Error() string { return e.Message }

// NormalRouteFirstByteCutoverError mirrors
// dispatch/upstream-dispatch.ts NormalRouteFirstByteCutoverError.
type NormalRouteFirstByteCutoverError struct {
	AccountID          string
	AccountName        string
	Deadline           gatewayrouting.NormalRouteAttemptFirstByteDeadline
	Message            string
	CutoverReservation any
}

func (e *NormalRouteFirstByteCutoverError) Error() string { return e.Message }

// Code mirrors the readonly code property.
func (e *NormalRouteFirstByteCutoverError) Code() string { return "normal_route_first_byte_timeout" }

// GatewayRequestWallBudgetExhaustedError mirrors
// dispatch/upstream-dispatch.ts GatewayRequestWallBudgetExhaustedError.
type GatewayRequestWallBudgetExhaustedError struct {
	WallRemainingMs           int64
	MinimumMeaningfulAttemptMs int64
	BudgetKind                string // 'wall' | 'coordination'
}

func (e *GatewayRequestWallBudgetExhaustedError) Error() string {
	if e.BudgetKind == WallBudgetKindCoordination {
		return "网关请求协调等待预算已耗尽，需要交接客户端重试"
	}
	return "网关请求墙钟预算已进入最终响应预留区"
}

// Code mirrors the readonly code property.
func (e *GatewayRequestWallBudgetExhaustedError) Code() string {
	if e.BudgetKind == WallBudgetKindCoordination {
		return "gateway_request_coordination_budget_exhausted"
	}
	return "gateway_request_wall_budget_exhausted"
}

// Wall budget kinds.
const (
	WallBudgetKindWall         = "wall"
	WallBudgetKindCoordination = "coordination"
)

// UnsafeResolvedUpstreamURLError mirrors shared/upstream-url-policy.ts
// UnsafeResolvedUpstreamUrlError.
type UnsafeResolvedUpstreamURLError struct{ Message string }

func (e *UnsafeResolvedUpstreamURLError) Error() string { return e.Message }

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

// timeoutLikeText mirrors the /timeout|timedout|timed out|etimedout|超时/i
// probe shared by body.ts and upstream-dispatch.ts.
func timeoutLikeText(text string) bool {
	if text == "" {
		return false
	}
	lowered := strings.ToLower(text)
	for _, marker := range []string{"timeout", "timedout", "timed out", "etimedout", "超时"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func int64CeilDiv(value, divisor int64) int64 {
	if divisor <= 0 {
		return value
	}
	return (value + divisor - 1) / divisor
}
