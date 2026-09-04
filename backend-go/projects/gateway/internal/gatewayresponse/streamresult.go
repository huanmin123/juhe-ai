package gatewayresponse

import (
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// StreamTransportFailure 对齐 StreamTransportFailure。
type StreamTransportFailure struct {
	Kind   string // 'timeout' | 'read_incomplete'
	Reason string
}

// StreamBodyOmissionSummary 对齐 StreamBodyOmissionSummary。
type StreamBodyOmissionSummary struct {
	// Reason is 'image_stream_payload' | 'image_json_payload'.
	Reason             string
	Message            string
	TotalUpstreamBytes int64
	TotalResponseBytes int64
	SseEventCount      int
	LastSseEventType   string
	RecentSseEventTypes []string
	ImageOutputReceived bool
	TerminalReceived   bool
	FailedReceived     bool
}

// StreamPipeResult 对齐 StreamPipeResult。
type StreamPipeResult struct {
	Completed bool
	// ProtocolValidated 仅当协议 inspector 观察到完整且有效的帧序列时为 true。
	ProtocolValidated     bool
	Message               string
	ErrorCode             string
	FirstTokenMs          *int64
	Usage                 gatewayproto.ParsedUsage
	OutputReceived        bool
	ImageOutputReceived   bool
	EstimatedOutputTokens int
	ResponseBodyText      string
	ResponseResourceId    string
	AuditResponseBody     []byte
	AuditUpstreamBody     []byte
	DownstreamBytesWritten int64
	// UpstreamResponseBytesWritten 是本次上游响应真正转发到下游的字节数。
	UpstreamResponseBytesWritten int64
	TransportCommitted    bool
	SemanticCommitted     bool
	UncommittedResponseBody []byte
	ResponseInspection    *ResponseInspectionDecision
	// PassthroughUpstreamFailure 表示上游失败终态被刻意原样转发。
	PassthroughUpstreamFailure bool
	ResponseInspectionObservations []ResponseInspectionDecision
	ResponseInspectionObservationOmittedCount int
	BodyOmission            *StreamBodyOmissionSummary
	TransportFailure        *StreamTransportFailure
	// GatewayLocalFailure 表示网关本地处理失败且无上游传输归因。
	GatewayLocalFailure bool
}

// 审计捕获上限，对齐 stream.ts 常量。
const (
	StreamDiagnosticCaptureBytes = 256 * 1024
	StreamAuditCaptureBytes      = 1024 * 1024
	// StreamTerminalKeepAliveDrainMs 对齐 streamTerminalKeepAliveDrainMs。
	StreamTerminalKeepAliveDrainMs = 50
)

// StreamInspectionSummaryInput 对齐 StreamInspectionSummaryInput。
type StreamInspectionSummaryInput struct {
	EventCount          int
	LastEventType       string
	RecentEventTypes    []string
	ImageOutputReceived bool
	TerminalReceived    bool
	FailedReceived      bool
}

// StreamResultInput 汇总 streamResult 的全部入参（Node 用 20 个位置参数）。
type StreamResultInput struct {
	Completed               bool
	Message                 string
	ErrorCode               string
	FirstTokenMs            *int64
	Usage                   gatewayproto.ParsedUsage
	ResponseCapture         *LimitedCapture
	UpstreamCapture         *LimitedCapture
	DiagnosticCapture       *LimitedCapture
	ResponseInspection      *ResponseInspectionDecision
	OutputReceived          bool
	EstimatedOutputTokens   int
	ImageOutputReceived     bool
	CaptureSuccessPayloads  bool
	BodyOmission            *StreamBodyOmissionSummary
	Observations            []ResponseInspectionDecision
	ObservationOmittedCount int
	DownstreamBytesWritten  int64
	// 以下字段 Node 由 stream.ts 显式传入；零值时按 Node 默认推导。
	UpstreamResponseBytesWrittenSet bool
	UpstreamResponseBytesWritten    int64
	TransportCommittedSet           bool
	TransportCommitted              bool
	SemanticCommittedSet            bool
	SemanticCommitted               bool
	UncommittedResponseBody         []byte
	ResponseResourceId              string
	ProtocolValidated               bool
	PassthroughUpstreamFailure      bool
}

// StreamResult 对齐 streamResult。
func StreamResult(input StreamResultInput) StreamPipeResult {
	downstreamBytes := input.DownstreamBytesWritten
	upstreamBytes := downstreamBytes
	if input.UpstreamResponseBytesWrittenSet {
		upstreamBytes = input.UpstreamResponseBytesWritten
	}
	transportCommitted := downstreamBytes > 0
	if input.TransportCommittedSet {
		transportCommitted = input.TransportCommitted
	}
	semanticCommitted := input.OutputReceived || input.ImageOutputReceived
	if input.SemanticCommittedSet {
		semanticCommitted = input.SemanticCommitted
	}

	var responseBodyText string
	if input.BodyOmission == nil && !(input.Completed && !input.CaptureSuccessPayloads) {
		if text, ok := input.DiagnosticCapture.ToDiagnosticText(); ok {
			responseBodyText = text
		}
	}
	var auditResponseBody []byte
	if input.BodyOmission == nil {
		if input.CaptureSuccessPayloads {
			auditResponseBody = input.ResponseCapture.CompleteBuffer()
		} else if !input.Completed {
			auditResponseBody = input.DiagnosticCapture.CompleteBuffer()
		}
	}
	auditUpstreamBody := auditUpstreamBodyForResult(input.UpstreamCapture, input.Completed, input.CaptureSuccessPayloads, input.BodyOmission)

	result := StreamPipeResult{
		Completed:                input.Completed,
		ProtocolValidated:        input.ProtocolValidated,
		Message:                  input.Message,
		ErrorCode:                input.ErrorCode,
		FirstTokenMs:             input.FirstTokenMs,
		Usage:                    input.Usage,
		OutputReceived:           input.OutputReceived,
		ImageOutputReceived:      input.ImageOutputReceived,
		EstimatedOutputTokens:    input.EstimatedOutputTokens,
		ResponseBodyText:         responseBodyText,
		ResponseResourceId:       input.ResponseResourceId,
		AuditResponseBody:        auditResponseBody,
		AuditUpstreamBody:        auditUpstreamBody,
		DownstreamBytesWritten:   downstreamBytes,
		UpstreamResponseBytesWritten: upstreamBytes,
		TransportCommitted:       transportCommitted,
		SemanticCommitted:        semanticCommitted,
		UncommittedResponseBody:  input.UncommittedResponseBody,
		ResponseInspection:       input.ResponseInspection,
		BodyOmission:             input.BodyOmission,
	}
	if input.PassthroughUpstreamFailure {
		result.PassthroughUpstreamFailure = true
	}
	if len(input.Observations) > 0 {
		result.ResponseInspectionObservations = append([]ResponseInspectionDecision(nil), input.Observations...)
		result.ResponseInspectionObservationOmittedCount = input.ObservationOmittedCount
	}
	return result
}

func auditUpstreamBodyForResult(upstreamCapture *LimitedCapture, completed bool, captureSuccessPayloads bool, bodyOmission *StreamBodyOmissionSummary) []byte {
	if bodyOmission != nil {
		return nil
	}
	if captureSuccessPayloads {
		return upstreamCapture.CompleteBuffer()
	}
	if completed {
		return nil
	}
	buffer := upstreamCapture.Buffer()
	if len(buffer) > 0 {
		return buffer
	}
	return nil
}

// StreamBodyOmissionSummaryOf 对齐 streamBodyOmissionSummary。
func StreamBodyOmissionSummaryOf(inspection StreamInspectionSummaryInput, totalUpstreamBytes int64, totalResponseBytes int64) *StreamBodyOmissionSummary {
	return &StreamBodyOmissionSummary{
		Reason:              "image_stream_payload",
		Message:             "图像流正文已省略，避免在日志和审计中保存图片字节",
		TotalUpstreamBytes:  totalUpstreamBytes,
		TotalResponseBytes:  totalResponseBytes,
		SseEventCount:       inspection.EventCount,
		LastSseEventType:    inspection.LastEventType,
		RecentSseEventTypes: inspection.RecentEventTypes,
		ImageOutputReceived: inspection.ImageOutputReceived,
		TerminalReceived:    inspection.TerminalReceived,
		FailedReceived:      inspection.FailedReceived,
	}
}

// StreamTransportFailureForError 对齐 streamTransportFailureForError。
func StreamTransportFailureForError(err error, diagnosticMessage string) *StreamTransportFailure {
	if readPlanTimeout(err) {
		return &StreamTransportFailure{Kind: "timeout", Reason: "上游流式响应传输超时"}
	}
	if !IsStartedUpstreamBodyTransportError(err) {
		return nil
	}
	kind := streamTransportFailureKind(err, diagnosticMessage)
	reason := "上游流式响应读取未完成"
	if kind == "timeout" {
		reason = "上游流式响应传输超时"
	}
	return &StreamTransportFailure{Kind: kind, Reason: reason}
}

func readPlanTimeout(err error) bool {
	var target *StreamReadPlanTimeoutError
	return err != nil && asStreamReadPlanTimeout(err, &target)
}

func asStreamReadPlanTimeout(err error, target **StreamReadPlanTimeoutError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*StreamReadPlanTimeoutError); ok {
		*target = e
		return true
	}
	return asStreamReadPlanTimeout(errorsUnwrap(err), target)
}

func errorsUnwrap(err error) error {
	switch e := err.(type) {
	case interface{ Unwrap() error }:
		return e.Unwrap()
	default:
		return nil
	}
}

func streamTransportFailureKind(err error, diagnosticMessage string) string {
	name := ""
	code := ""
	var transport *StartedBodyTransportError
	if err != nil && asStartedTransport(err, &transport) {
		name = transport.Name
		code = transport.Code
	}
	diagnostic := strings.ToLower(strings.TrimSpace(strings.Join([]string{name, code, diagnosticMessage}, " ")))
	if transportTimeoutPattern(diagnostic) {
		return "timeout"
	}
	return "read_incomplete"
}

func asStartedTransport(err error, target **StartedBodyTransportError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*StartedBodyTransportError); ok {
		*target = e
		return true
	}
	return asStartedTransport(errorsUnwrap(err), target)
}

// PublicStreamFailureMessage 对齐 publicStreamFailureMessage。
func PublicStreamFailureMessage(err error, protocolFailure bool, transportFailure *StreamTransportFailure) string {
	if protocolFailure {
		return "上游流式响应返回失败终态"
	}
	if err != nil {
		switch err.(type) {
		case *StreamReadPlanTimeoutError, *FirstByteTimeoutError, *StreamPreCommitBufferExceededError:
			return err.Error()
		}
	}
	if transportFailure != nil {
		return transportFailure.Reason
	}
	return "网关处理流式响应失败"
}

// IsGatewayLocalStreamFailure 对齐 isGatewayLocalStreamFailure。
func IsGatewayLocalStreamFailure(err error, protocolFailure bool, transportFailure *StreamTransportFailure) bool {
	return !protocolFailure && transportFailure == nil && !IsFirstByteTimeoutError(err)
}

// QuoteInt 仅供测试辅助格式化（避免各处 strconv 重复导入）。
func QuoteInt(value int64) string { return strconv.FormatInt(value, 10) }
