package gatewayupstream

import (
	"errors"
	"fmt"
	"strings"
)

// Error taxonomy of the upstream transport layer, migrated from
// upstream/request.ts, upstream/first-byte-timeout.ts, upstream/body.ts and
// upstream/first-byte-deadline.ts (REFACTOR-0006 阶段 A 自 gatewaydispatch
// errors.go 随传输族迁入；dispatch 侧错误类型留守根包).
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
	FirstByteTimeoutSourceHardTimeout        FirstByteTimeoutSource = "hard_timeout"
	FirstByteTimeoutSourceConfiguredDeadline FirstByteTimeoutSource = "configured_deadline"
)

// GatewayFirstByteTimeoutError mirrors upstream/first-byte-timeout.ts.
type GatewayFirstByteTimeoutError struct {
	Message   string
	TimeoutMs int64
	Source    FirstByteTimeoutSource
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

// UpstreamBodyReadIncompleteError mirrors upstream/body.ts.
type UpstreamBodyReadIncompleteError struct{ Cause error }

func (e *UpstreamBodyReadIncompleteError) Error() string {
	if e.Cause != nil && timeoutLikeText(e.Cause.Error()) {
		return "上游响应正文读取超时"
	}
	return "上游响应正文读取未完成"
}

func (e *UpstreamBodyReadIncompleteError) Code() string  { return "UPSTREAM_BODY_READ_INCOMPLETE" }
func (e *UpstreamBodyReadIncompleteError) Unwrap() error { return e.Cause }

// UpstreamBodyReadMaxLifetimeError mirrors upstream/body.ts.
type UpstreamBodyReadMaxLifetimeError struct{ TimeoutMs int64 }

func (e *UpstreamBodyReadMaxLifetimeError) Error() string {
	return fmt.Sprintf("上游非流式响应正文读取超时（绝对上限 %ds）", Int64CeilDiv(e.TimeoutMs, 1000))
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

// UnsupportedUpstreamResponseEncodingError mirrors upstream/request.ts.
type UnsupportedUpstreamResponseEncodingError struct{ Message string }

func (e *UnsupportedUpstreamResponseEncodingError) Error() string { return e.Message }

// UnsafeResolvedUpstreamURLError mirrors shared/upstream-url-policy.ts
// UnsafeResolvedUpstreamUrlError.
type UnsafeResolvedUpstreamURLError struct{ Message string }

func (e *UnsafeResolvedUpstreamURLError) Error() string { return e.Message }

// TimeoutLikeText mirrors the /timeout|timedout|timed out|etimedout|超时/i
// probe shared by body.ts and upstream-dispatch.ts（原包内私有名
// timeoutLikeText，随传输族迁出后导出供 dispatch 门面转发）.
func TimeoutLikeText(text string) bool {
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

// timeoutLikeText keeps the in-package call sites byte-identical.
func timeoutLikeText(text string) bool { return TimeoutLikeText(text) }

func Int64CeilDiv(value, divisor int64) int64 {
	if divisor <= 0 {
		return value
	}
	return (value + divisor - 1) / divisor
}
