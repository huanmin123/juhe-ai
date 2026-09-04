package gatewayresponse

import (
	"errors"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 管道辅助：下游写、pre-commit、终态信号、结果组装与工具函数。

// lastCommittedDisposition 记录最近一次 committed failure signal 结果。
// （Node 用局部返回值；这里提到结构上以便日志分支读取。）

func (p *streamPipe) responseState() PreCommitResponseState {
	return PreCommitResponseState{
		HeadersSent:   p.downstream.Res.HeadersSent(),
		WritableEnded: p.downstream.WritableEnded(),
		Destroyed:     p.downstream.DestroyedNow(),
	}
}

func (p *streamPipe) prepareDownstreamForWrite() {
	if p.downstreamPrepared {
		return
	}
	p.downstreamPrepared = true
	if p.options.PrepareDownstream != nil {
		p.options.PrepareDownstream()
	}
}

func (p *streamPipe) ensureBeforeDownstreamCommit() error {
	if p.downstreamCommitPrepared || p.options.BeforeDownstreamCommit == nil {
		return nil
	}
	if err := p.options.BeforeDownstreamCommit(p.responseResourceId); err != nil {
		return &StreamBeforeDownstreamCommitError{OriginalError: err}
	}
	p.downstreamCommitPrepared = true
	return nil
}

// writeDownstreamChunk 对齐 writeDownstreamChunk：Go 的响应写是同步的，
// 背压由底层 Write 阻塞承担；每个分片后 flush 保持事件边界可见。
func (p *streamPipe) writeDownstreamChunk(chunk []byte, semantic bool) (struct{ WriteMs int64 }, error) {
	var out struct{ WriteMs int64 }
	if err := p.ensureBeforeDownstreamCommit(); err != nil {
		return out, err
	}
	p.captureDownstreamChunk(chunk)
	p.prepareDownstreamForWrite()
	started := p.nowMs()
	if _, err := p.downstream.Res.Write(chunk); err != nil {
		return out, err
	}
	FlushGateway(p.downstream.Res)
	out.WriteMs = p.nowMs() - started
	if p.interceptor != nil {
		p.interceptor.MarkDownstreamWrite()
	}
	p.totalResponseBytes += int64(len(chunk))
	if semantic {
		p.downstreamCommit.MarkSemanticCommitted(int64(len(chunk)))
	} else {
		p.downstreamCommit.MarkTransportCommitted(int64(len(chunk)))
	}
	return out, nil
}

func (p *streamPipe) captureDownstreamChunk(chunk []byte) {
	if p.bodyCaptureOmitted {
		return
	}
	p.responseCapture.Push(chunk)
	p.diagnosticCapture.Push(chunk)
}

func (p *streamPipe) canKeepPreCommitBuffered(inspection gatewayproto.StreamInspection, chunk []byte) bool {
	return CanKeepStreamPreCommitChunk(p.preCommitBuffer, PreCommitInspectionState{
		OutputReceived:   inspection.OutputReceived,
		TerminalReceived: inspection.TerminalReceived,
		FailedReceived:   inspection.FailedReceived,
		Skipped:          inspection.Skipped,
	}, chunk, p.totalResponseBytes, p.responseState())
}

func (p *streamPipe) appendPreCommitChunk(chunk []byte) {
	if p.preCommitSseEvidence.OnlyNonSemanticFramingObserved {
		ClearStreamPreCommitChunks(p.preCommitBuffer)
		return
	}
	AppendStreamPreCommitChunk(p.preCommitBuffer, chunk)
}

func (p *streamPipe) flushPreCommitChunks() error {
	chunks := TakeStreamPreCommitChunks(p.preCommitBuffer)
	if len(chunks) == 0 {
		return nil
	}
	semantic := p.preCommitSseEvidence.DataPayloadStarted || p.preCommitSseEvidence.DataEventObserved
	for index, chunk := range chunks {
		if _, err := p.writeDownstreamChunk(chunk, semantic && index == len(chunks)-1); err != nil {
			return err
		}
	}
	return nil
}

func (p *streamPipe) shouldFailBeforeDownstreamCommit() bool {
	return ShouldFailBeforeStreamDownstreamCommit(p.preCommitBuffer, p.totalResponseBytes, p.responseState())
}

func (p *streamPipe) shouldKeepNonSemanticSseFramingPrivate() bool {
	return p.preCommitBuffer.Buffering &&
		p.preCommitSseEvidence.OnlyNonSemanticFramingObserved &&
		p.totalResponseBytes == 0 &&
		!p.downstream.WritableEnded() &&
		!p.downstream.DestroyedNow() &&
		!p.downstreamCommit.SemanticCommitted
}

func (p *streamPipe) shouldRejectOversizedUncommittedSseFraming(inspection gatewayproto.StreamInspection, chunk []byte) bool {
	return p.preCommitBuffer.Buffering &&
		p.totalResponseBytes == 0 &&
		!p.downstream.WritableEnded() &&
		!p.downstream.DestroyedNow() &&
		!p.downstreamCommit.SemanticCommitted &&
		!inspection.OutputReceived &&
		!inspection.TerminalReceived &&
		!inspection.FailedReceived &&
		!p.preCommitSseEvidence.OnlyNonSemanticFramingObserved &&
		!p.preCommitSseEvidence.DataPayloadStarted &&
		!p.preCommitSseEvidence.DataEventObserved &&
		(inspection.Skipped || WouldExceedStreamPreCommitBuffer(p.preCommitBuffer, chunk))
}

func (p *streamPipe) recordResponseInspectionObservations(observations []ResponseInspectionDecision) {
	if len(observations) == 0 {
		return
	}
	for _, observation := range observations {
		if len(p.observations) < maxResponseInspectionObservationCount {
			p.observations = append(p.observations, observation)
		} else {
			p.observationOmittedCount++
		}
	}
}

// maxResponseInspectionObservationCount 对齐同名常量。
const maxResponseInspectionObservationCount = 20

func (p *streamPipe) markFirstSemanticOutput(inspection gatewayproto.StreamInspection) {
	if p.firstTokenMs != nil || !streamOutputReceived(inspection) {
		return
	}
	value := p.nowMs() - p.startedAt
	p.firstTokenMs = &value
	if p.options.OnFirstOutput != nil {
		p.options.OnFirstOutput()
	}
}

func (p *streamPipe) updateStreamInspectionProgress(inspection gatewayproto.StreamInspection) {
	p.markFirstSemanticOutput(inspection)
	p.semanticResultReceived = p.semanticResultReceived || streamSemanticResultReceived(inspection)
	p.pendingProtocolEvent = inspection.PendingEvent
	p.streamParserSkipped = inspection.Skipped
	p.protocolTerminalReceived = p.protocolTerminalReceived || inspection.TerminalReceived
	p.lastInspection = inspection
	// Node 另在此调用 markRequestProtocolTerminalOutcome('failure'|'success')；
	// Go 侧由请求上下文（G15/G17）承接。
}

func (p *streamPipe) publishInspection(inspection gatewayproto.StreamInspection) gatewayproto.StreamInspection {
	p.lastInspection = inspection
	return inspection
}

func (p *streamPipe) omitBodyCaptureIfImageStream(inspection gatewayproto.StreamInspection, eofPendingFlush bool) {
	if !inspection.ImageOutputReceived || p.bodyCaptureOmitted {
		return
	}
	p.bodyCaptureOmitted = true
	p.upstreamCapture.Clear()
	p.responseCapture.Clear()
	p.diagnosticCapture.Clear()
	fields := map[string]any{
		"event":              "gateway_stream_body_capture_omitted",
		"reason":             "image_stream_payload",
		"elapsedMs":          p.nowMs() - p.startedAt,
		"chunkIndex":         p.chunkIndex,
		"totalUpstreamBytes": p.totalUpstreamBytes,
		"totalResponseBytes": p.totalResponseBytes,
		"sseEventCount":      inspection.EventCount,
		"lastSseEventType":   inspection.LastEventType,
		"recentSseEventTypes": inspection.RecentEventTypes,
		"eofPendingFlush":    boolOrNil(eofPendingFlush),
	}
	p.logger.Info("gateway_stream_body_capture_omitted", fields, "网关识别到图像流输出，已省略流式响应正文捕获，仅保留元信息")
}

func (p *streamPipe) bodyOmissionFor(inspection gatewayproto.StreamInspection) *StreamBodyOmissionSummary {
	if !p.bodyCaptureOmitted {
		return nil
	}
	target := inspection
	if target.EventCount == 0 && target.LastEventType == "" && len(target.RecentEventTypes) == 0 {
		target = p.lastInspection
	}
	return StreamBodyOmissionSummaryOf(StreamInspectionSummaryInput{
		EventCount:          target.EventCount,
		LastEventType:       target.LastEventType,
		RecentEventTypes:    target.RecentEventTypes,
		ImageOutputReceived: target.ImageOutputReceived,
		TerminalReceived:    target.TerminalReceived,
		FailedReceived:      target.FailedReceived,
	}, p.totalUpstreamBytes, p.totalResponseBytes)
}

// finishArgs 汇总 finishStreamResult 的分支入参。
type finishArgs struct {
	completed              bool
	message                string
	errorCode              string
	usage                  gatewayproto.ParsedUsage
	outputReceived         bool
	estimatedOutputTokens  int
	imageOutputReceived    bool
	responseInspection     *ResponseInspectionDecision
	passthroughUpstreamFailure bool
	gatewayLocalFailure    bool
	transportFailure       *StreamTransportFailure
}

func (p *streamPipe) finishStreamResult(args finishArgs) StreamPipeResult {
	result := StreamResult(StreamResultInput{
		Completed:               args.completed,
		Message:                 args.message,
		ErrorCode:               args.errorCode,
		FirstTokenMs:            p.firstTokenMs,
		Usage:                   args.usage,
		ResponseCapture:         p.responseCapture,
		UpstreamCapture:         p.upstreamCapture,
		DiagnosticCapture:       p.diagnosticCapture,
		ResponseInspection:      args.responseInspection,
		OutputReceived:          args.outputReceived,
		EstimatedOutputTokens:   args.estimatedOutputTokens,
		ImageOutputReceived:     args.imageOutputReceived,
		CaptureSuccessPayloads:  p.captureSuccessPayloads,
		BodyOmission:            p.bodyOmissionFor(gatewayproto.StreamInspection{}),
		Observations:            p.observations,
		ObservationOmittedCount: p.observationOmittedCount,
		DownstreamBytesWritten:  p.downstreamCommit.DownstreamBytesWritten,
		UpstreamResponseBytesWrittenSet: true,
		UpstreamResponseBytesWritten:    p.totalResponseBytes,
		TransportCommittedSet:           true,
		TransportCommitted:              p.downstreamCommit.TransportCommitted && p.downstreamCommit.DownstreamBytesWritten > 0,
		SemanticCommittedSet:            true,
		SemanticCommitted:               p.downstreamCommit.SemanticCommitted,
		UncommittedResponseBody:         UncommittedStreamResponseBody(p.preCommitBuffer),
		ResponseResourceId:              p.responseResourceId,
		ProtocolValidated:               args.completed && p.protocolTerminalReceived && !p.streamParserSkipped,
		PassthroughUpstreamFailure:      args.passthroughUpstreamFailure || p.passthroughUpstreamFailure,
	})
	result.GatewayLocalFailure = args.gatewayLocalFailure
	result.TransportFailure = args.transportFailure
	return result
}

// signalCommittedStreamFailure 对齐 signalCommittedStreamFailure。
func (p *streamPipe) signalCommittedStreamFailure(inspection gatewayproto.StreamInspection, accountFailureEligible bool) (string, error) {
	canSignalCommittedImageFailure := inspection.ImageOutputReceived && p.options.DownstreamProtocol == "responses_sse"
	if p.terminalEventWritten || (!p.committedProtocolFailureEventEnabled && !canSignalCommittedImageFailure) {
		p.interruptResponse()
		p.lastCommittedDisposition = "interrupted"
		return "interrupted", nil
	}
	// The stream has already crossed the downstream commit boundary. It
	// cannot be replaced by another account, so expose the parsed upstream
	// failure when available and otherwise identify the interruption itself.
	clientMessage := "上游流式响应中断"
	if inspection.OutputReceived {
		clientMessage = "上游流式响应在输出后中断"
	} else if inspection.ErrorMessage != "" {
		clientMessage = inspection.ErrorMessage
	}
	clientErrorCode := inspection.ErrorCode
	if clientErrorCode == "" {
		clientErrorCode = "upstream_stream_interrupted"
	}
	if p.committedProtocolFailureEventEnabled {
		clientErrorCode = gatewaypreauth.GatewayStreamClientRetryErrorCode
	}
	if p.options.BeforeCommittedFailureSignal != nil {
		context := CommittedStreamFailureSignalContext{
			StreamFailureContext:   p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, p.interpretedProtocolFailure(inspection), p.interpretedProtocolFailure(inspection)),
			Message:                clientMessage,
			ErrorCode:              clientErrorCode,
			SemanticCommitted:      p.downstreamCommit.SemanticCommitted,
			AccountFailureEligible: accountFailureEligible,
		}
		if err := p.options.BeforeCommittedFailureSignal(context); err != nil {
			p.logger.Warn("gateway_stream_committed_failure_signal_callback_failed", nil, "Codex turn 提交后失败状态记录失败，继续发送协议终态")
		}
	}
	p.prepareDownstreamForWrite()
	failureEvent := p.writeGatewayStreamFailureEventWithBackpressure(clientMessage, clientErrorCode)
	if failureEvent == nil {
		p.interruptResponse()
		p.lastCommittedDisposition = "interrupted"
		return "interrupted", nil
	}
	if !p.bodyCaptureOmitted {
		p.responseCapture.Push(failureEvent)
		p.diagnosticCapture.Push(failureEvent)
	}
	p.totalResponseBytes += int64(len(failureEvent))
	p.downstreamCommit.MarkSemanticCommitted(int64(len(failureEvent)))
	p.terminalEventWritten = true
	p.endResponse()
	p.lastCommittedDisposition = "signaled"
	return "signaled", nil
}

func (p *streamPipe) writeGatewayStreamFailureEventWithBackpressure(message string, code string) []byte {
	buffer := gatewaypreauth.BuildGatewayStreamFailureEventForProtocol(
		message, code,
		gatewaypreauth.GatewayErrorProtocol(p.driver.ClientErrorProtocol()),
		gatewaypreauth.OpenAIGatewayDownstreamProtocol(p.options.DownstreamProtocol),
	)
	if buffer == nil {
		return nil
	}
	if _, err := p.downstream.Res.Write(buffer); err != nil {
		return nil
	}
	FlushGateway(p.downstream.Res)
	return buffer
}

func (p *streamPipe) endResponse() {
	if p.downstream.WritableEnded() || p.downstream.DestroyedNow() {
		return
	}
	if tracking, ok := p.downstream.Res.(*gatewaypreauth.TrackingWriter); ok {
		tracking.End()
	}
}

func (p *streamPipe) interruptResponse() {
	if p.downstream.WritableEnded() || p.downstream.DestroyedNow() {
		return
	}
	p.downstream.InterruptNow()
}

func (p *streamPipe) signalAborted() bool {
	if p.input.Signal == nil {
		return false
	}
	select {
	case <-p.input.Signal.Done():
		return true
	default:
		return false
	}
}

func (p *streamPipe) streamFailureContext(downstreamBytesWritten int64, outputReceived bool, protocolFailureEventReceived bool, availabilityProbeEligible ...bool) StreamFailureContext {
	context := StreamFailureContext{
		DownstreamBytesWritten:          downstreamBytesWritten,
		OutputReceived:                  outputReceived,
		ProtocolFailureEventReceived:    protocolFailureEventReceived,
		ProtocolFailureEventReceivedSet: true,
	}
	if len(availabilityProbeEligible) > 0 {
		context.AvailabilityProbeEligible = availabilityProbeEligible[0]
		context.AvailabilityProbeEligibleSet = true
	} else {
		context.AvailabilityProbeEligible = protocolFailureEventReceived
		context.AvailabilityProbeEligibleSet = true
	}
	return context
}

func (p *streamPipe) pushResponseInspectionChunks(chunks [][]byte) StreamInterceptorSseResult {
	result := StreamInterceptorSseResult{}
	for _, chunk := range chunks {
		result = mergeInterceptorResults(result, p.interceptor.PushChunk(chunk))
		if result.Intercepted != nil {
			break
		}
	}
	return result
}

func mergeInterceptorResults(left, right StreamInterceptorSseResult) StreamInterceptorSseResult {
	merged := StreamInterceptorSseResult{
		Chunks:                     append(append([][]byte(nil), left.Chunks...), right.Chunks...),
		Intercepted:                left.Intercepted,
		PassthroughUpstreamFailure: left.PassthroughUpstreamFailure || right.PassthroughUpstreamFailure,
		PendingEvent:               right.PendingEvent || (left.Intercepted == nil && right.Intercepted == nil && left.PendingEvent),
		ParserSkipped:              left.ParserSkipped || right.ParserSkipped,
	}
	if merged.Intercepted == nil {
		merged.Intercepted = right.Intercepted
	}
	merged.Observations = append(append([]ResponseInspectionDecision(nil), left.Observations...), right.Observations...)
	return merged
}

func streamOutputReceived(inspection gatewayproto.StreamInspection) bool {
	return inspection.OutputReceived || inspection.ImageOutputReceived
}

func streamSemanticResultReceived(inspection gatewayproto.StreamInspection) bool {
	return inspection.OutputReceived ||
		inspection.ImageOutputReceived ||
		inspection.TerminalReceived ||
		inspection.FailedReceived
}

func timeAfter(ms int64) <-chan time.Time {
	if ms < 1 {
		ms = 1
	}
	return time.After(time.Duration(ms) * time.Millisecond)
}

func boolOrNull(value bool) any {
	if value {
		return true
	}
	return nil
}

func boolOrNil(value bool) any {
	if value {
		return true
	}
	return nil
}

func int64PtrValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func orDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func errorDiagnosticMessage(err error) string {
	if err == nil {
		return "上游流式响应已中断"
	}
	return err.Error()
}

func isStreamPreCommitBufferExceeded(err error) bool {
	var target *StreamPreCommitBufferExceededError
	return errors.As(err, &target)
}

func asFirstByteTimeout(err error) *FirstByteTimeoutError {
	var target *FirstByteTimeoutError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func asResponsePrecommitDeadline(err error) *ResponsePrecommitDeadlineError {
	var target *ResponsePrecommitDeadlineError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func gatewayStreamFailureCodeForError(err error, message string) string {
	switch {
	case p0IsFirstByteTimeout(err):
		return FirstByteTimeoutErrorCode
	case isStreamPreCommitBufferExceeded(err):
		return StreamPreCommitBufferExceededCode
	default:
		return gatewaypreauth.GatewayStreamFailureCode(message)
	}
}

func p0IsFirstByteTimeout(err error) bool { return asFirstByteTimeout(err) != nil }

func transportFailureKindOrNull(failure *StreamTransportFailure) any {
	if failure == nil {
		return nil
	}
	return failure.Kind
}

// lastCommittedDisposition 存放最近一次 signal 结果（日志分支用）。
