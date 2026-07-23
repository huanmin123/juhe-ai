// Package gatewaystreamrelay provides the HTTP-independent, bounded streaming
// transport core used by the Go gateway. Protocol parsing, upstream dialing,
// HTTP header commitment, persistence, and queue publication remain separate
// owners.
package gatewaystreamrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"juhe-ai/backend-go/internal/gatewayaudit"
	"juhe-ai/backend-go/internal/modules/gatewayusage"
)

const (
	MaxStreamBytes      int64 = 64 * 1024 * 1024
	maxBufferBytes            = 1024 * 1024
	defaultBufferBytes        = 32 * 1024
	defaultIdleTimeout        = 60 * time.Second
	defaultTotalTimeout       = 10 * time.Minute
)

var (
	ErrInvalidLimits          = errors.New("网关流式中转限制无效")
	ErrStreamTooLarge         = errors.New("网关流式响应超过 64 MiB 上限")
	ErrIdleDeadline           = errors.New("网关流式中转空闲超时")
	ErrTotalDeadline          = errors.New("网关流式中转总时长超时")
	ErrClientCanceled         = errors.New("客户端已取消流式请求")
	ErrSourceRead             = errors.New("读取上游流式响应失败")
	ErrDestinationWrite       = errors.New("写入客户端流式响应失败")
	ErrInspector              = errors.New("流式协议检查失败")
	ErrMissingTerminal        = errors.New("流式响应缺少协议终止事件")
	ErrProtocolTerminalFailed = errors.New("上游流式协议报告失败终态")
)

// Source and Sink make cancellation an explicit part of the transport
// contract. Adapters must stop their operation when ctx is done. This keeps the
// core free of detached read/write goroutines and lets backpressure remain a
// synchronous, bounded operation.
type Source interface {
	Read(ctx context.Context, p []byte) (int, error)
}

type Sink interface {
	Write(ctx context.Context, p []byte) (int, error)
}

type SourceFunc func(context.Context, []byte) (int, error)

func (fn SourceFunc) Read(ctx context.Context, p []byte) (int, error) { return fn(ctx, p) }

type SinkFunc func(context.Context, []byte) (int, error)

func (fn SinkFunc) Write(ctx context.Context, p []byte) (int, error) { return fn(ctx, p) }

// TerminalInspector observes bytes before they are committed downstream. It
// must retain only bounded state. Finish is called only after clean transport
// EOF; Snapshot is called for every outcome so partial usage and terminal facts
// can still be handed off.
type TerminalInspector interface {
	Observe(p []byte) error
	Finish() error
	Snapshot() Inspection
}

// CommitObserver is optional. Inspectors that classify protocol violations at
// the downstream commit boundary can receive this notification without making
// transport relay depend on a protocol package.
type CommitObserver interface {
	ObserveCommit(transportCommitted, semanticCommitted bool, downstreamBytes int64)
}

type Inspection struct {
	TerminalRequired bool
	TerminalReceived bool
	SemanticOutput   bool
	Failed           bool
	Usage            gatewayusage.UsageFacts
	ErrorCode        string
	ErrorMessage     string
	ResponseSnapshot any
}

type Limits struct {
	MaxBytes     int64
	BufferBytes  int
	IdleTimeout  time.Duration
	TotalTimeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxBytes:     MaxStreamBytes,
		BufferBytes:  defaultBufferBytes,
		IdleTimeout:  defaultIdleTimeout,
		TotalTimeout: defaultTotalTimeout,
	}
}

type Options struct {
	Limits           Limits
	Inspector        TerminalInspector
	StartedAt        time.Time
	StatusCode       int
	HadFailedAttempt bool
	Now              func() time.Time
}

type State string

const (
	StateCompleted             State = "completed"
	StateFailedBeforeFirstByte State = "failed_before_first_byte"
	StateFailedAfterFirstByte  State = "failed_after_first_byte"
)

type Handoff struct {
	Usage gatewayusage.TerminalFacts
	Audit gatewayaudit.TerminalInput
}

type Result struct {
	State               State
	BytesRead           int64
	BytesWritten        int64
	TransportCommitted  bool
	FirstByteSent       bool
	FirstByteAt         time.Time
	SemanticCommitted   bool
	SemanticFirstByteAt time.Time
	CompletedAt         time.Time
	RetryAllowed        bool
	Inspection          Inspection
	Handoff             Handoff
}

type failure struct {
	err         error
	code        string
	message     string
	phase       string
	attribution gatewayusage.FailureAttribution
	audit       gatewayaudit.Outcome
	clientAbort bool
}

// Relay synchronously moves one upstream stream to one downstream sink. A
// successful write must complete before the next upstream read, which is the
// backpressure boundary. A failure can be retried only when no downstream byte
// has been written.
func Relay(parent context.Context, source Source, sink Sink, options Options) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	if source == nil {
		return Result{}, fmt.Errorf("%w: source is required", ErrInvalidLimits)
	}
	if sink == nil {
		return Result{}, fmt.Errorf("%w: sink is required", ErrInvalidLimits)
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return Result{}, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	startedAt := options.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}

	totalCtx, cancelTotal := context.WithTimeoutCause(parent, limits.TotalTimeout, ErrTotalDeadline)
	defer cancelTotal()

	buffer := make([]byte, limits.BufferBytes)
	result := Result{}
	for {
		readLimit := len(buffer)
		remaining := limits.MaxBytes - result.BytesRead
		if remaining < int64(readLimit) {
			readLimit = int(remaining + 1)
		}
		readCtx, cancelRead := context.WithTimeoutCause(totalCtx, limits.IdleTimeout, ErrIdleDeadline)
		n, readErr := source.Read(readCtx, buffer[:readLimit])
		readCause := context.Cause(readCtx)
		cancelRead()
		if n < 0 || n > readLimit {
			return finalizeFailure(result, options, startedAt, now, failure{
				err:  fmt.Errorf("%w: source returned invalid byte count %d", ErrSourceRead, n),
				code: "invalid_source_read", message: "上游流式读取返回了无效字节数", phase: "upstream",
				attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
			})
		}
		if readCause != nil {
			return finalizeFailure(result, options, startedAt, now, classifyContextFailure(readCause))
		}
		if n > 0 {
			if int64(n) > remaining {
				return finalizeFailure(result, options, startedAt, now, failure{
					err: ErrStreamTooLarge, code: "stream_too_large", message: "上游流式响应超过 64 MiB 上限", phase: "relay",
					attribution: gatewayusage.FailureAttributionGatewayCapacity, audit: gatewayaudit.OutcomeGatewayFailed,
				})
			}
			chunk := buffer[:n]
			result.BytesRead += int64(n)
			if options.Inspector != nil {
				if inspectErr := options.Inspector.Observe(chunk); inspectErr != nil {
					return finalizeFailure(result, options, startedAt, now, failure{
						err: fmt.Errorf("%w: %w", ErrInspector, inspectErr), code: "stream_inspection_failed",
						message: "流式协议检查失败", phase: "stream",
						attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
					})
				}
				result.Inspection = options.Inspector.Snapshot()
				if result.Inspection.Failed {
					return finalizeProtocolFailure(result, options, startedAt, now)
				}
			}
			semanticOutput := options.Inspector == nil || result.Inspection.SemanticOutput
			writeFailure := writeChunk(totalCtx, sink, chunk, limits.IdleTimeout, semanticOutput, &result, now)
			if observer, ok := options.Inspector.(CommitObserver); ok {
				observer.ObserveCommit(result.TransportCommitted, result.SemanticCommitted, result.BytesWritten)
			}
			if writeFailure != nil {
				return finalizeFailure(result, options, startedAt, now, *writeFailure)
			}
			if result.Inspection.TerminalReceived {
				return finalizeTerminal(result, options, startedAt, now)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if cause := context.Cause(totalCtx); cause != nil {
					return finalizeFailure(result, options, startedAt, now, classifyContextFailure(cause))
				}
				return finalizeEOF(result, options, startedAt, now)
			}
			return finalizeFailure(result, options, startedAt, now, failure{
				err: fmt.Errorf("%w: %w", ErrSourceRead, readErr), code: "upstream_stream_read_failed",
				message: "读取上游流式响应失败", phase: "upstream",
				attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
			})
		}
		if n == 0 {
			return finalizeFailure(result, options, startedAt, now, failure{
				err: fmt.Errorf("%w: %w", ErrSourceRead, io.ErrNoProgress), code: "upstream_stream_no_progress",
				message: "上游流式读取未产生进展", phase: "upstream",
				attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
			})
		}
	}
}

func writeChunk(parent context.Context, sink Sink, chunk []byte, idleTimeout time.Duration, semanticOutput bool, result *Result, now func() time.Time) *failure {
	for offset := 0; offset < len(chunk); {
		writeCtx, cancelWrite := context.WithTimeoutCause(parent, idleTimeout, ErrIdleDeadline)
		n, err := sink.Write(writeCtx, chunk[offset:])
		writeCause := context.Cause(writeCtx)
		cancelWrite()
		if n < 0 || n > len(chunk)-offset {
			return &failure{
				err:  fmt.Errorf("%w: sink returned invalid byte count %d", ErrDestinationWrite, n),
				code: "invalid_destination_write", message: "客户端流式写入返回了无效字节数", phase: "client",
				attribution: gatewayusage.FailureAttributionClientLifecycle, audit: gatewayaudit.OutcomeClientAborted, clientAbort: true,
			}
		}
		if n > 0 {
			var committedAt time.Time
			if !result.TransportCommitted || (semanticOutput && !result.SemanticCommitted) {
				committedAt = now()
			}
			if !result.TransportCommitted {
				result.TransportCommitted = true
				result.FirstByteSent = true
				result.FirstByteAt = committedAt
			}
			if semanticOutput && !result.SemanticCommitted {
				result.SemanticCommitted = true
				result.SemanticFirstByteAt = committedAt
			}
			result.BytesWritten += int64(n)
			offset += n
		}
		if writeCause != nil {
			classified := classifyWriteContextFailure(writeCause)
			return &classified
		}
		if err != nil {
			return &failure{
				err: fmt.Errorf("%w: %w", ErrDestinationWrite, err), code: "client_stream_write_failed",
				message: "写入客户端流式响应失败", phase: "client",
				attribution: gatewayusage.FailureAttributionClientLifecycle, audit: gatewayaudit.OutcomeClientAborted, clientAbort: true,
			}
		}
		if n == 0 {
			return &failure{
				err: fmt.Errorf("%w: %w", ErrDestinationWrite, io.ErrShortWrite), code: "client_stream_no_progress",
				message: "客户端流式写入未产生进展", phase: "client",
				attribution: gatewayusage.FailureAttributionClientLifecycle, audit: gatewayaudit.OutcomeClientAborted, clientAbort: true,
			}
		}
	}
	return nil
}

func finalizeEOF(result Result, options Options, startedAt time.Time, now func() time.Time) (Result, error) {
	if options.Inspector != nil {
		if err := options.Inspector.Finish(); err != nil {
			return finalizeFailure(result, options, startedAt, now, failure{
				err: fmt.Errorf("%w: %w", ErrInspector, err), code: "stream_inspection_finish_failed",
				message: "流式协议终态检查失败", phase: "stream",
				attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
			})
		}
		result.Inspection = options.Inspector.Snapshot()
	}
	if result.Inspection.Failed {
		return finalizeProtocolFailure(result, options, startedAt, now)
	}
	if result.Inspection.TerminalRequired && !result.Inspection.TerminalReceived {
		return finalizeFailureWithInspection(result, options, startedAt, now, failure{
			err: ErrMissingTerminal, code: gatewayaudit.ErrorCodeMissingStreamTerminal,
			message: "流式响应缺少协议终止事件", phase: "stream",
			attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
		})
	}
	return finalizeSuccess(result, options, startedAt, now)
}

func finalizeTerminal(result Result, options Options, startedAt time.Time, now func() time.Time) (Result, error) {
	if options.Inspector != nil {
		if err := options.Inspector.Finish(); err != nil {
			return finalizeFailure(result, options, startedAt, now, failure{
				err: fmt.Errorf("%w: %w", ErrInspector, err), code: "stream_inspection_finish_failed",
				message: "流式协议终态检查失败", phase: "stream",
				attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
			})
		}
		result.Inspection = options.Inspector.Snapshot()
	}
	if result.Inspection.Failed {
		return finalizeProtocolFailure(result, options, startedAt, now)
	}
	return finalizeSuccess(result, options, startedAt, now)
}

func finalizeSuccess(result Result, options Options, startedAt time.Time, now func() time.Time) (Result, error) {
	result.CompletedAt = now()
	result.State = StateCompleted
	result.RetryAllowed = false
	result.Handoff = makeHandoff(result, options, startedAt, true, failure{})
	return result, nil
}

func finalizeProtocolFailure(result Result, options Options, startedAt time.Time, now func() time.Time) (Result, error) {
	message := result.Inspection.ErrorMessage
	if message == "" {
		message = "上游流式协议报告失败终态"
	}
	code := result.Inspection.ErrorCode
	if code == "" {
		code = "upstream_stream_terminal_failed"
	}
	return finalizeFailureWithInspection(result, options, startedAt, now, failure{
		err: ErrProtocolTerminalFailed, code: code, message: message, phase: "stream",
		attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
	})
}

func finalizeFailure(result Result, options Options, startedAt time.Time, now func() time.Time, value failure) (Result, error) {
	if options.Inspector != nil {
		result.Inspection = options.Inspector.Snapshot()
	}
	return finalizeFailureWithInspection(result, options, startedAt, now, value)
}

func finalizeFailureWithInspection(result Result, options Options, startedAt time.Time, now func() time.Time, value failure) (Result, error) {
	result.CompletedAt = now()
	if result.FirstByteSent {
		result.State = StateFailedAfterFirstByte
		result.RetryAllowed = false
	} else {
		result.State = StateFailedBeforeFirstByte
		result.RetryAllowed = !value.clientAbort && value.attribution != gatewayusage.FailureAttributionClientLifecycle
	}
	result.Handoff = makeHandoff(result, options, startedAt, false, value)
	return result, value.err
}

func makeHandoff(result Result, options Options, startedAt time.Time, success bool, value failure) Handoff {
	var statusCode *int
	if options.StatusCode >= 100 && options.StatusCode <= 599 {
		status := options.StatusCode
		statusCode = &status
	}
	var firstToken *time.Duration
	if result.SemanticCommitted {
		duration := result.SemanticFirstByteAt.Sub(startedAt)
		if duration < 0 {
			duration = 0
		}
		firstToken = &duration
	}
	usage := gatewayusage.TerminalFacts{
		CompletedAt:      result.CompletedAt,
		StatusCode:       statusCode,
		FirstToken:       firstToken,
		Usage:            result.Inspection.Usage,
		ResponseSnapshot: result.Inspection.ResponseSnapshot,
	}
	audit := gatewayaudit.TerminalInput{
		Stream:           true,
		TerminalRequired: result.Inspection.TerminalRequired,
		TerminalReceived: result.Inspection.TerminalReceived,
		HadFailedAttempt: options.HadFailedAttempt,
	}
	if success {
		usage.Outcome = gatewayusage.OutcomeSucceeded
		audit.Success = true
		return Handoff{Usage: usage, Audit: audit}
	}
	usage.Outcome = gatewayusage.OutcomeFailed
	usage.FailureAttribution = value.attribution
	usage.ErrorCode = value.code
	usage.ErrorMessage = value.message
	audit.RequestedOutcome = value.audit
	audit.ClientAborted = value.clientAbort
	audit.ErrorPhase = value.phase
	audit.ErrorCode = value.code
	audit.ErrorMessage = value.message
	return Handoff{Usage: usage, Audit: audit}
}

func classifyContextFailure(cause error) failure {
	switch {
	case errors.Is(cause, ErrIdleDeadline):
		return failure{
			err: ErrIdleDeadline, code: "stream_idle_timeout", message: "网关流式中转空闲超时", phase: "stream",
			attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
		}
	case errors.Is(cause, ErrTotalDeadline):
		return failure{
			err: ErrTotalDeadline, code: "stream_total_timeout", message: "网关流式中转总时长超时", phase: "stream",
			attribution: gatewayusage.FailureAttributionAccountUpstream, audit: gatewayaudit.OutcomeStreamFailed,
		}
	default:
		return failure{
			err: fmt.Errorf("%w: %w", ErrClientCanceled, cause), code: "client_canceled",
			message: "客户端已取消流式请求", phase: "client",
			attribution: gatewayusage.FailureAttributionClientLifecycle, audit: gatewayaudit.OutcomeClientAborted, clientAbort: true,
		}
	}
}

func classifyWriteContextFailure(cause error) failure {
	if !errors.Is(cause, ErrIdleDeadline) && !errors.Is(cause, ErrTotalDeadline) {
		return classifyContextFailure(cause)
	}
	code := "client_stream_idle_timeout"
	message := "客户端流式写入空闲超时"
	err := ErrIdleDeadline
	if errors.Is(cause, ErrTotalDeadline) {
		code = "client_stream_total_timeout"
		message = "客户端流式写入超过请求总时长"
		err = ErrTotalDeadline
	}
	return failure{
		err: err, code: code, message: message, phase: "client",
		attribution: gatewayusage.FailureAttributionClientLifecycle,
		audit:       gatewayaudit.OutcomeClientAborted,
		clientAbort: true,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxBytes == 0 {
		value.MaxBytes = defaults.MaxBytes
	}
	if value.BufferBytes == 0 {
		value.BufferBytes = defaults.BufferBytes
	}
	if value.IdleTimeout == 0 {
		value.IdleTimeout = defaults.IdleTimeout
	}
	if value.TotalTimeout == 0 {
		value.TotalTimeout = defaults.TotalTimeout
	}
	if value.MaxBytes <= 0 || value.MaxBytes > MaxStreamBytes ||
		value.BufferBytes <= 0 || value.BufferBytes > maxBufferBytes ||
		value.IdleTimeout <= 0 || value.TotalTimeout <= 0 {
		return Limits{}, fmt.Errorf("%w: %#v", ErrInvalidLimits, value)
	}
	return value, nil
}
