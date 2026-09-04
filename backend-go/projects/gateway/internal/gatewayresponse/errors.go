package gatewayresponse

import (
	"errors"
	"strings"
)

// 终态错误类型，逐字对齐 Node upstream/request.ts、
// upstream/first-byte-timeout.ts、upstream/first-byte-deadline.ts 与
// response/stream.ts 的本地错误。

// ErrUpstreamRequestAbortedMessage 对齐 UpstreamRequestAbortedError 的默认
// message；isUpstreamRequestAbortedError 同时按 message 判定。
const ErrUpstreamRequestAbortedMessage = "请求已取消"

// UpstreamRequestAbortedError 对齐 UpstreamRequestAbortedError。
type UpstreamRequestAbortedError struct {
	Message                string
	UpstreamRequestStarted bool
}

func (e *UpstreamRequestAbortedError) Error() string {
	if e.Message == "" {
		return ErrUpstreamRequestAbortedMessage
	}
	return e.Message
}

// IsUpstreamRequestAbortedError 对齐 isUpstreamRequestAbortedError：类型判定
// 或 message === '请求已取消'。
func IsUpstreamRequestAbortedError(err error) bool {
	if err == nil {
		return false
	}
	var aborted *UpstreamRequestAbortedError
	if errors.As(err, &aborted) {
		return true
	}
	return err.Error() == ErrUpstreamRequestAbortedMessage
}

// FirstByteTimeoutErrorCode 对齐 GatewayFirstByteTimeoutError.code。
const FirstByteTimeoutErrorCode = "first_byte_timeout"

// FirstByteTimeoutError 对齐 GatewayFirstByteTimeoutError。
type FirstByteTimeoutError struct {
	Message   string
	TimeoutMs int64
	// Source is 'hard_timeout' | 'configured_deadline'.
	Source string
}

func (e *FirstByteTimeoutError) Error() string { return e.Message }

// Code 对齐 readonly code。
func (e *FirstByteTimeoutError) Code() string { return FirstByteTimeoutErrorCode }

// IsFirstByteTimeoutError 对齐 isGatewayFirstByteTimeoutError。
func IsFirstByteTimeoutError(err error) bool {
	var target *FirstByteTimeoutError
	return errors.As(err, &target)
}

// GatewayRequestWallBudgetExhaustedCode 对齐
// GatewayResponsePrecommitDeadlineError.code。
const GatewayRequestWallBudgetExhaustedCode = "gateway_request_wall_budget_exhausted"

// ResponsePrecommitDeadlineError 对齐 GatewayResponsePrecommitDeadlineError。
type ResponsePrecommitDeadlineError struct {
	DeadlineAtMs int64
}

func (e *ResponsePrecommitDeadlineError) Error() string {
	return "网关请求墙钟已到，响应尚未产生可提交的语义结果"
}

// Code 对齐 readonly code。
func (e *ResponsePrecommitDeadlineError) Code() string {
	return GatewayRequestWallBudgetExhaustedCode
}

// IsResponsePrecommitDeadlineError 对齐 isGatewayResponsePrecommitDeadlineError。
func IsResponsePrecommitDeadlineError(err error) bool {
	var target *ResponsePrecommitDeadlineError
	return errors.As(err, &target)
}

// ResponsePrecommitDeadlineErrorOf 对齐 responsePrecommitDeadlineErrorFor：
// 直接判定，或从 NonStreamBodyPipeError 还原原始错误。
func ResponsePrecommitDeadlineErrorOf(err error) *ResponsePrecommitDeadlineError {
	if err == nil {
		return nil
	}
	var deadline *ResponsePrecommitDeadlineError
	if errors.As(err, &deadline) {
		return deadline
	}
	var pipe *NonStreamBodyPipeError
	if errors.As(err, &pipe) && pipe.OriginalError != nil {
		var inner *ResponsePrecommitDeadlineError
		if errors.As(pipe.OriginalError, &inner) {
			return inner
		}
	}
	return nil
}

// StreamReadPlanTimeoutError 对齐 StreamReadPlanTimeoutError；TimeoutKind 是
// stream-read-plan 的 timeoutKind。
type StreamReadPlanTimeoutError struct {
	Message     string
	TimeoutKind string
}

func (e *StreamReadPlanTimeoutError) Error() string { return e.Message }

// IsStreamReadPlanTimeoutError 判定 read-plan 超时（含子类）。
func IsStreamReadPlanTimeoutError(err error) bool {
	var target *StreamReadPlanTimeoutError
	return errors.As(err, &target)
}

// StreamPreCommitBufferExceededCode 对齐 StreamPreCommitBufferExceededError.code。
const StreamPreCommitBufferExceededCode = "stream_precommit_buffer_exceeded"

// StreamPreCommitBufferExceededError 对齐 StreamPreCommitBufferExceededError。
type StreamPreCommitBufferExceededError struct{}

func (e *StreamPreCommitBufferExceededError) Error() string {
	return "流式响应在语义提交前超过安全缓冲上限"
}

// Code 对齐 readonly code。
func (e *StreamPreCommitBufferExceededError) Code() string {
	return StreamPreCommitBufferExceededCode
}

// StreamBeforeDownstreamCommitError 对齐 StreamBeforeDownstreamCommitError：
// 包装 beforeDownstreamCommit 回调错误，上层 rethrow 原始错误。
type StreamBeforeDownstreamCommitError struct {
	OriginalError error
}

func (e *StreamBeforeDownstreamCommitError) Error() string {
	return "流式响应下游提交前准备失败"
}

func (e *StreamBeforeDownstreamCommitError) Unwrap() error { return e.OriginalError }

// NonStreamBodyPipeError 对齐 NonStreamUpstreamBodyPipeError：非流式正文管道
// 中断并携带部分管道结果。
type NonStreamBodyPipeError struct {
	OriginalError error
	PartialResult NonStreamPipeResult
}

func (e *NonStreamBodyPipeError) Error() string {
	if e.OriginalError != nil {
		return e.OriginalError.Error()
	}
	return "上游非流式响应正文中断"
}

func (e *NonStreamBodyPipeError) Unwrap() error { return e.OriginalError }

// StartedBodyTransportError 标记“上游请求已开始后正文传输失败”
// （startedUpstreamBodyTransportErrors WeakSet 的 Go 等价物）。
type StartedBodyTransportError struct {
	Err error
	// Code mirrors error.code（ETIMEDOUT 等）用于 transport failure 归类。
	Code string
	// Name mirrors error.name。
	Name string
}

func (e *StartedBodyTransportError) Error() string { return e.Err.Error() }
func (e *StartedBodyTransportError) Unwrap() error { return e.Err }

// IsStartedUpstreamBodyTransportError 对齐 isStartedUpstreamBodyTransportError。
func IsStartedUpstreamBodyTransportError(err error) bool {
	var target *StartedBodyTransportError
	return errors.As(err, &target)
}

// transportTimeoutPattern 对齐 /timeout|timedout|timed out|etimedout|超时/。
func transportTimeoutPattern(diagnostic string) bool {
	lower := strings.ToLower(diagnostic)
	for _, needle := range []string{"timeout", "timedout", "timed out", "etimedout", "超时"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}
