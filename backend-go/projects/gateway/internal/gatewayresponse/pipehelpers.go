package gatewayresponse

import (
	"errors"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 管道辅助：下游写、pre-commit、终态信号、结果组装与工具函数。

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
	if p.downstreamFirstWriteMs == nil {
		// 首个下游字节写出时刻（§3.3）：含窗口停留的客户端可感知首字延迟口径。
		value := p.nowMs() - p.startedAt
		p.downstreamFirstWriteMs = &value
	}
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
	// 窗口关闭点（设计 §3.1）：任何 flush 都意味着提交发生（一次性 flush +
	// MarkSemanticCommitted），窗口使命结束；后续 chunk 按既有写路径直通。
	p.closeReplayWindow()
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

// maybeOpenReplayWindow 在首个语义输出识别点开启受限重放窗口（设计 §3.1）：
// 资格启用、仍处提交前缓冲、未见终态语义事件且未解析跳过时置位窗口并启动
// 真实截止 Timer（T 到点强制关窗 flush，保证首字延迟最多 +T）。
func (p *streamPipe) maybeOpenReplayWindow(inspection gatewayproto.StreamInspection) {
	if !p.replayWindowEligible || p.replayWindowOpen || p.replayWindowClosed {
		return
	}
	if !streamOutputReceived(inspection) {
		return
	}
	if p.downstreamCommit.SemanticCommitted || p.totalResponseBytes > 0 || !p.preCommitBuffer.Buffering {
		return
	}
	if inspection.TerminalReceived || inspection.FailedReceived || inspection.Skipped {
		// 终态语义事件与解析跳过不入窗（§3.1.2 不变量 1），保持既有立即处置。
		return
	}
	p.replayWindowOpen = true
	p.preCommitBuffer.Windowing = true
	p.preCommitBuffer.WindowStartMs = p.nowMs()
	p.preCommitBuffer.WindowBytes = 0
	timer := time.NewTimer(time.Duration(p.replayWindowTimeoutMs) * time.Millisecond)
	p.replayWindowTimer = timer
	p.replayWindowTimerCh = timer.C
}

// closeReplayWindow 关闭受限重放窗口（幂等）：记录窗口开启到关闭的时长
// （§3.3 windowDurationMs），Stop 释放真实截止 Timer。
func (p *streamPipe) closeReplayWindow() {
	if p.replayWindowOpen {
		duration := p.nowMs() - p.preCommitBuffer.WindowStartMs
		if duration < 0 {
			duration = 0
		}
		p.replayWindowDurationMs = &duration
	}
	p.replayWindowOpen = false
	p.replayWindowClosed = true
	p.preCommitBuffer.Windowing = false
	p.preCommitBuffer.WindowBytes = 0
	if p.replayWindowTimer != nil {
		p.replayWindowTimer.Stop()
		p.replayWindowTimer = nil
	}
	p.replayWindowTimerCh = nil
}

// flushReplayWindowAtDeadline 是窗口真实截止到点的强制关窗 flush（设计 §3.1
// 条件③）：静默/慢滴流不依赖后续 chunk 驱动，T 到点即把已缓冲内容按既有
// semantic 判定下发，其后 chunk 直接透传。
func (p *streamPipe) flushReplayWindowAtDeadline() error {
	if !p.replayWindowOpen {
		return nil
	}
	p.logger.Info("gateway_stream_replay_window_deadline_flush", map[string]any{
		"elapsedMs":     p.nowMs() - p.startedAt,
		"windowBytes":   p.preCommitBuffer.WindowBytes,
		"windowStartMs": p.preCommitBuffer.WindowStartMs,
	}, "受限重放窗口到达真实截止，已把窗口缓冲内容提交下发")
	p.closeReplayWindow()
	return p.flushPreCommitChunks()
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
	// Gemini interactions 创建流的资源 ID 回带（对齐 Node stream.ts:389
	// updateStreamInspectionProgress 的 responseResourceId ??= inspection.responseResourceId；
	// Go 放在快照唯一漏斗处，额外覆盖 Finish 冲刷快照）。首个非空值落
	// p.responseResourceId，供提交前亲和写入（BeforeDownstreamCommit 钩子）
	// 与 StreamPipeResult 结果回带消费。
	if p.responseResourceId == "" && inspection.ResponseResourceID != "" {
		p.responseResourceId = inspection.ResponseResourceID
	}
	p.lastInspection = inspection
	p.warnFirstParseErrorEvent(inspection)
	return inspection
}

// warnFirstParseErrorEvent 在首个解析失败事件上发每请求一条 WARN（设计 §3.2.0
// / §3.3）：计数与事件序号来自 inspector 快照，原始字节摘要由隔离器记录；
// accountId/traceId 经 identityStreamLogger 统一注入，非 openai 直通管道
// （无隔离器）只留计数证据。
func (p *streamPipe) warnFirstParseErrorEvent(inspection gatewayproto.StreamInspection) {
	if p.parseErrorWarnLogged || inspection.ParseErrorEventCount == 0 {
		return
	}
	p.parseErrorWarnLogged = true
	fields := map[string]any{
		"endpoint":             string(p.options.EndpointFamily),
		"eventIndex":           inspection.EventCount,
		"parseErrorEventCount": inspection.ParseErrorEventCount,
	}
	if p.parseErrorIsolator != nil {
		snippet, snippetIndex := p.parseErrorIsolator.firstDropped()
		if snippet != "" {
			fields["parseErrorSnippet"] = snippet
		}
		if snippetIndex > 0 {
			fields["parseErrorEventIndex"] = snippetIndex
		}
	}
	p.logger.Warn("gateway_stream_event_parse_error", fields, "上游流式事件无法 JSON 解析，已字节级隔离不向下游透传")
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
		"event":               "gateway_stream_body_capture_omitted",
		"reason":              "image_stream_payload",
		"elapsedMs":           p.nowMs() - p.startedAt,
		"chunkIndex":          p.chunkIndex,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"sseEventCount":       inspection.EventCount,
		"lastSseEventType":    inspection.LastEventType,
		"recentSseEventTypes": inspection.RecentEventTypes,
		"eofPendingFlush":     boolOrNil(eofPendingFlush),
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
	completed                  bool
	message                    string
	errorCode                  string
	usage                      gatewayproto.ParsedUsage
	outputReceived             bool
	estimatedOutputTokens      int
	imageOutputReceived        bool
	responseInspection         *ResponseInspectionDecision
	passthroughUpstreamFailure bool
	gatewayLocalFailure        bool
	transportFailure           *StreamTransportFailure
}

func (p *streamPipe) finishStreamResult(args finishArgs) StreamPipeResult {
	result := StreamResult(StreamResultInput{
		Completed:                       args.completed,
		Message:                         args.message,
		ErrorCode:                       args.errorCode,
		FirstTokenMs:                    p.firstTokenMs,
		Usage:                           args.usage,
		ResponseCapture:                 p.responseCapture,
		UpstreamCapture:                 p.upstreamCapture,
		DiagnosticCapture:               p.diagnosticCapture,
		ResponseInspection:              args.responseInspection,
		OutputReceived:                  args.outputReceived,
		EstimatedOutputTokens:           args.estimatedOutputTokens,
		ImageOutputReceived:             args.imageOutputReceived,
		CaptureSuccessPayloads:          p.captureSuccessPayloads,
		BodyOmission:                    p.bodyOmissionFor(gatewayproto.StreamInspection{}),
		Observations:                    p.observations,
		ObservationOmittedCount:         p.observationOmittedCount,
		DownstreamBytesWritten:          p.downstreamCommit.DownstreamBytesWritten,
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
	// 结算观测面（设计 §3.3）：解析失败事件计数取最终 inspector 快照；
	// windowDurationMs 未开窗流省略（nil）；downstreamFirstWriteMs 是首个下游
	// 字节写出时刻 − 请求开始。
	result.ParseErrorEventCount = p.lastInspection.ParseErrorEventCount
	result.WindowDurationMs = p.replayWindowDurationMs
	result.DownstreamFirstWriteMs = p.downstreamFirstWriteMs
	return result
}

// signalCommittedStreamFailure 对齐 signalCommittedStreamFailure。
//
// 2026-10-23 契约收口：下游提交后的流式失败对全部客户端（generic 与精确画像）
// 一律中断连接（真·网络级断开），由客户端以网络级错误自行重试；不再按客户端
// 画像补发脱敏协议失败事件（原 committedFailureSignal=protocol_error_event
// 的 signaled 分支已删除）。提交前（未向客户端写任何字节）的失败行为不变，
// 仍走 writePreCommitStreamFailureToClient / RetryUpstream 换号路径。
func (p *streamPipe) signalCommittedStreamFailure() error {
	p.interruptResponse()
	return nil
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
