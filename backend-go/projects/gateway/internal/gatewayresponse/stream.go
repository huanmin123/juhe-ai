package gatewayresponse

import (
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 流式管道日志接缝：Node 的 getRequestLogger() 的 Debug/Info/Warn 面。
type StreamLogger interface {
	Debug(event string, fields map[string]any, message string)
	Info(event string, fields map[string]any, message string)
	Warn(event string, fields map[string]any, message string)
}

type nopStreamLogger struct{}

func (nopStreamLogger) Debug(string, map[string]any, string) {}
func (nopStreamLogger) Info(string, map[string]any, string)  {}
func (nopStreamLogger) Warn(string, map[string]any, string)  {}

// StreamFailureContext 对齐 StreamFailureContext。
type StreamFailureContext struct {
	DownstreamBytesWritten int64
	OutputReceived         bool
	// ProtocolFailureEventReceived 三态：Set=false 表示 Node undefined。
	ProtocolFailureEventReceived    bool
	ProtocolFailureEventReceivedSet bool
	AvailabilityProbeEligible       bool
	AvailabilityProbeEligibleSet    bool
}

// CommittedStreamFailureSignalContext 对齐 CommittedStreamFailureSignalContext。
type CommittedStreamFailureSignalContext struct {
	StreamFailureContext
	Message              string
	ErrorCode            string
	SemanticCommitted    bool
	AccountFailureEligible bool
}

// IncompleteClientAbortContext 对齐 IncompleteClientAbortContext。
type IncompleteClientAbortContext struct {
	StreamFailureContext
	SemanticCommitted bool
	TerminalReceived  bool
	FailedReceived    bool
	ParserSkipped     bool
}

// FirstByteDeadlineInput 对齐 FirstByteDeadlineDecisionInput。
type FirstByteDeadlineInput struct {
	ElapsedMs int64
	TimeoutMs int64
	Transport string // 'stream' | 'non_stream'
}

// FirstByteDeadlineAction 对齐 FirstByteDeadlineAction。
type FirstByteDeadlineAction string

const (
	FirstByteDeadlineContinue FirstByteDeadlineAction = "continue"
	FirstByteDeadlineAbort    FirstByteDeadlineAction = "abort"
)

// FirstByteDeadlineHandler 对齐 FirstByteDeadlineHandler；error 对齐 handler
// throw（decisionError 语义）。
type FirstByteDeadlineHandler func(input FirstByteDeadlineInput) (FirstByteDeadlineAction, error)

// StreamInterceptorSseResult 对齐 ResponseInspectionSseResult（含完整决策）。
type StreamInterceptorSseResult struct {
	Chunks        [][]byte
	Intercepted   *ResponseInspectionDecision
	Observations  []ResponseInspectionDecision
	// PassthroughUpstreamFailure 对齐同名字段（Codex cyber_policy 透传）。
	PassthroughUpstreamFailure bool
	PendingEvent               bool
	ParserSkipped              bool
}

// StreamInterceptor 对齐 pipeUpstreamStream 消费的检查拦截器面。
type StreamInterceptor interface {
	PushChunk(chunk []byte) StreamInterceptorSseResult
	FlushPendingOnEOF() StreamInterceptorSseResult
	MarkDownstreamWrite()
}

// StreamDriver 是流管道消费的协议驱动视图（G02-G04 装配面）。
type StreamDriver interface {
	// ClientErrorProtocol 对齐 protocolDriver.clientErrorProtocol。
	ClientErrorProtocol() string
	// NewStreamInspector 对齐 createStreamInspector。
	NewStreamInspector() gatewayproto.StreamInspector
	// ResponseInspectionEndpointFamily 对齐 responseInspectionEndpointFamily。
	ResponseInspectionEndpointFamily(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily
	// SSEResponseInspectionFailureEvent 对齐 sseResponseInspectionFailureEvent；
	// 'none' 时检查拦截器不构建失败事件。
	SSEResponseInspectionFailureEvent() string
	// DrainForKeepAliveAfterTerminal 对齐 drainForKeepAliveAfterTerminal。
	DrainForKeepAliveAfterTerminal() bool
}

// StreamDownstream 是下游响应视图：GatewayResponseWriter + destroyed/interrupt。
type StreamDownstream struct {
	Res gatewaypreauth.GatewayResponseWriter
	// Destroyed 对齐 res.destroyed；nil 恒为 false。
	Destroyed func() bool
	// Interrupt 对齐 destroyResponseForUpstreamBodyError(res)：标记强制关闭并
	// 中断下游连接；nil 时仅记录内部状态。
	Interrupt func()
	// WritableEndedOverride 在 Res 非 *gatewaypreauth.TrackingWriter 时提供
	// res.writableEnded；nil 时回退 TrackingWriter（若有）。
	WritableEndedOverride func() bool
}

// WritableEnded 返回 res.writableEnded 语义值。
func (d StreamDownstream) WritableEnded() bool {
	if d.WritableEndedOverride != nil {
		return d.WritableEndedOverride()
	}
	if tracking, ok := d.Res.(*gatewaypreauth.TrackingWriter); ok {
		return tracking.WritableEnded()
	}
	return false
}

// DestroyedNow 返回 res.destroyed 语义值。
func (d StreamDownstream) DestroyedNow() bool {
	if d.Destroyed != nil {
		return d.Destroyed()
	}
	return false
}

// InterruptNow 执行强制下游关闭。
func (d StreamDownstream) InterruptNow() {
	if d.Interrupt != nil {
		d.Interrupt()
	}
}

// End 转发 TrackingWriter 的 res.end() 语义（非 TrackingWriter 时为 no-op）。
func (d StreamDownstream) End() {
	if tracking, ok := d.Res.(*gatewaypreauth.TrackingWriter); ok {
		tracking.End()
	}
}

// FlushGateway 对 GatewayResponseWriter 做类型断言后 flush（SSE 旁路）。
func FlushGateway(res gatewaypreauth.GatewayResponseWriter) {
	if flusher, ok := res.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}

// StreamPipeOptions 对齐 StreamPipeOptions。
type StreamPipeOptions struct {
	ClientRetryEnabled bool
	// CommittedFailureSignalProtocolEvent 对齐 committedFailureSignal ===
	// 'protocol_error_event'；nil 表示按 clientRetryEnabled 推导。
	CommittedFailureSignalProtocolEvent *bool
	InterpretProtocolFailures           bool // 默认 true（构造时取反入参）
	// InterpretProtocolFailuresSet=false 表示调用方未显式传 false。
	InterpretProtocolFailuresSet bool
	OnFirstOutput                func()
	CaptureSuccessPayloads       bool // 默认 true
	// CaptureSuccessPayloadsSet=false 表示调用方未显式传 false。
	CaptureSuccessPayloadsSet        bool
	RetryBeforeDownstreamWriteUntilOutput bool
	ResponseInspectionPolicies       []RuntimeResponseInspectionPolicy
	ResponseInspectionContext        *ResponseInspectionRuntimeContext
	DownstreamProtocol               string
	ResponseProtocol                 string
	EndpointFamily                   gatewayproto.ResponseEndpointFamily
	FirstByteTimeoutMs               *int64
	FirstByteDeadlineMs              *int64
	ResponsePrecommitDeadlineAtMs    *int64
	OnFirstByteDeadline              FirstByteDeadlineHandler
	OnFirstByteDeadlineSuperseded    func()
	PrepareDownstream                func()
	BeforeDownstreamCommit           func(responseResourceId string) error
	TransformUpstreamChunk           func(chunk []byte) [][]byte
	FlushTransformedUpstreamChunks   func() [][]byte
	DownstreamCommitState            *DownstreamCommitState
	BeforeCommittedFailureSignal     func(context CommittedStreamFailureSignalContext) error
	OnIncompleteClientAbort          func(context IncompleteClientAbortContext) error
	// Driver 缺省为 openai 视图（Node responseProtocol ?? 'openai_v1'）。
	Driver      StreamDriver
	Interceptor StreamInterceptor
	Logger      StreamLogger
	// NowMs 注入时钟（Date.now 等价物）；nil 用真实时间。
	NowMs func() int64
}

// PipeUpstreamStreamInput 对齐 pipeUpstreamStream 的位置参数。
type PipeUpstreamStreamInput struct {
	UpstreamBody      UpstreamBody
	Downstream        StreamDownstream
	TimeoutProfile    TimeoutProfile
	StartedAtMs       int64
	HandleStreamFailure func(message string, errorCode string, context StreamFailureContext) error
	// Signal 对齐 AbortSignal：取消即客户端断开/请求中止。
	Signal interface {
		Done() <-chan struct{}
		Err() error
	}
	Options StreamPipeOptions
}

// PipeUpstreamStream 对齐 pipeUpstreamStream。
func PipeUpstreamStream(input PipeUpstreamStreamInput) (StreamPipeResult, error) {
	pipe := newStreamPipe(input)
	return pipe.run()
}

func defaultNowMs() int64 { return time.Now().UnixMilli() }
