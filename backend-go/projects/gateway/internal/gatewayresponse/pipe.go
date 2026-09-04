package gatewayresponse

import (
	"errors"
	"io"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// streamPipe 承载 pipeUpstreamStream 的全部状态（Node 闭包变量的结构化版本）。
type streamPipe struct {
	input      PipeUpstreamStreamInput
	body       UpstreamBody
	downstream StreamDownstream
	profile    TimeoutProfile
	startedAt  int64
	options    StreamPipeOptions
	driver     StreamDriver
	inspector  gatewayproto.StreamInspector
	interceptor StreamInterceptor
	logger     StreamLogger
	nowMs      func() int64

	committedProtocolFailureEventEnabled bool
	interpretProtocolFailures            bool
	captureSuccessPayloads               bool
	passthroughUpstreamFailure           bool

	completed           bool
	parserSkipLogged    bool
	responseInspectionParserSkipLogged bool
	firstTokenMs        *int64
	firstByteDeadlineObserved bool
	pendingReadDecision *streamFirstByteDeadlineReadDecision
	waitingForFirstChunk bool
	lastUpstreamActivityAt int64
	lastSseEventActivityAt *int64
	lastSseEventCount   int
	upstreamChunkReceived bool
	semanticResultReceived bool
	responseResourceId  string
	pendingProtocolEvent bool
	streamParserSkipped bool
	protocolTerminalReceived bool
	chunkIndex          int
	totalUpstreamBytes  int64
	totalResponseBytes  int64
	lastProgressLogAt   int64
	lastBackpressureLogAt int64
	terminalEventWritten bool
	bodyCaptureOmitted  bool
	downstreamPrepared  bool
	downstreamCommit    *DownstreamCommitState
	downstreamCommitPrepared bool
	preCommitBuffer     *PreCommitBufferState
	preCommitSseEvidence *StreamPreCommitSseEvidence
	observations        []ResponseInspectionDecision
	observationOmittedCount int

	responseCapture   *LimitedCapture
	upstreamCapture   *LimitedCapture
	diagnosticCapture *LimitedCapture

	clientClosed bool
	// lastCommittedDisposition 记录最近一次 signalCommittedStreamFailure 结果
	//（Node 局部返回值；日志分支需要）。
	lastCommittedDisposition string
	// lastInspection 保留最近一次 inspector 快照（body omission 汇总用）。
	lastInspection gatewayproto.StreamInspection
}

func newStreamPipe(input PipeUpstreamStreamInput) *streamPipe {
	options := input.Options
	if !options.InterpretProtocolFailuresSet {
		options.InterpretProtocolFailures = true
	}
	if !options.CaptureSuccessPayloadsSet {
		options.CaptureSuccessPayloads = true
	}
	logger := options.Logger
	if logger == nil {
		logger = nopStreamLogger{}
	}
	nowMs := options.NowMs
	if nowMs == nil {
		nowMs = defaultNowMs
	}
	driver := options.Driver
	if driver == nil {
		driver = DefaultOpenAIStreamDriver()
	}
	commit := options.DownstreamCommitState
	if commit == nil {
		commit = &DownstreamCommitState{}
	}
	signalProtocolEvent := options.ClientRetryEnabled
	if options.CommittedFailureSignalProtocolEvent != nil {
		signalProtocolEvent = *options.CommittedFailureSignalProtocolEvent
	}
	captureLimit := -1
	if options.CaptureSuccessPayloads {
		captureLimit = StreamAuditCaptureBytes
	}
	return &streamPipe{
		input:                input,
		body:                 input.UpstreamBody,
		downstream:           input.Downstream,
		profile:              input.TimeoutProfile,
		startedAt:            input.StartedAtMs,
		options:              options,
		driver:               driver,
		inspector:            driver.NewStreamInspector(),
		interceptor:          options.Interceptor,
		logger:               logger,
		nowMs:                nowMs,
		committedProtocolFailureEventEnabled: signalProtocolEvent,
		interpretProtocolFailures:            options.InterpretProtocolFailures,
		captureSuccessPayloads:               options.CaptureSuccessPayloads,
		waitingForFirstChunk:                 true,
		lastUpstreamActivityAt:               input.StartedAtMs,
		lastProgressLogAt:                    input.StartedAtMs,
		downstreamCommit:                     commit,
		preCommitBuffer:                      NewPreCommitBufferState(options.RetryBeforeDownstreamWriteUntilOutput),
		preCommitSseEvidence:                 NewStreamPreCommitSseEvidence(),
		responseCapture:                      NewLimitedCapture(captureLimit),
		upstreamCapture:                      NewLimitedCapture(captureLimit),
		diagnosticCapture:                    NewLimitedCapture(StreamDiagnosticCaptureBytes),
	}
}

// run 对齐 Node 的 try → catch → finally → 后置收尾顺序。
// loop 返回 (result, nil) 表示 try 块内直接 return；返回 (nil, err) 表示异常。
func (p *streamPipe) run() (StreamPipeResult, error) {
	p.logger.Debug("gateway_stream_pipe_started", map[string]any{
		"timeoutsDisabled":                boolOrNull(p.profile.TimeoutsDisabled),
		"firstResponseTimeoutMs":          p.profile.FirstResponseTimeoutMs,
		"idleTimeoutMs":                   p.profile.IdleTimeoutMs,
		"uncommittedAttemptMaxLifetimeMs": p.profile.UncommittedAttemptMaxLifetimeMs,
		"startedAt":                       p.startedAt,
	}, "网关开始转发上游流式响应")

	result, loopErr := p.loop()
	if loopErr != nil {
		return p.handlePipeError(loopErr)
	}
	if result != nil {
		return *result, nil
	}

	// ---- try 块正常走完后的收尾（Node EOF 后置段）----
	return p.finalizeAfterLoop()
}

// loop 对齐 while(true) 主循环 + EOF transformed chunks 段。
func (p *streamPipe) loop() (*StreamPipeResult, error) {
	var latestInspection gatewayproto.StreamInspection
	for {
		if p.clientClosed || p.downstream.DestroyedNow() {
			return nil, errors.New("客户端连接已断开")
		}
		readResult, err := p.readNextStreamChunk()
		if err != nil {
			return nil, err
		}
		p.firstByteDeadlineObserved = readResult.firstByteDeadlineObserved
		p.pendingReadDecision = readResult.decision
		result := readResult.chunk

		if result.Err == io.EOF {
			p.settleStreamFirstByteDeadlineReadDecision(false)
			p.completed = true
			break
		}
		if result.Err != nil {
			return nil, result.Err
		}

		buffer := result.Data
		p.chunkIndex++
		p.totalUpstreamBytes += int64(len(buffer))
		p.upstreamChunkReceived = true
		p.waitingForFirstChunk = false
		p.lastUpstreamActivityAt = p.nowMs()
		if !p.bodyCaptureOmitted {
			p.upstreamCapture.Push(buffer)
		}
		transformedChunks := [][]byte{buffer}
		if p.options.TransformUpstreamChunk != nil {
			transformedChunks = p.options.TransformUpstreamChunk(buffer)
		}
		interceptResult := StreamInterceptorSseResult{Chunks: transformedChunks}
		if p.interceptor != nil {
			interceptResult = p.pushResponseInspectionChunks(transformedChunks)
		}
		p.passthroughUpstreamFailure = p.passthroughUpstreamFailure || interceptResult.PassthroughUpstreamFailure
		p.pendingProtocolEvent = interceptResult.PendingEvent
		if interceptResult.PendingEvent {
			value := p.lastUpstreamActivityAt
			p.lastSseEventActivityAt = &value
		}
		if interceptResult.ParserSkipped && !p.responseInspectionParserSkipLogged {
			p.responseInspectionParserSkipLogged = true
			p.logger.Info("gateway_response_inspection_parser_skipped", nil, "网关流式事件过大，兜底拦截停止解析并继续原样转发")
		}
		p.recordResponseInspectionObservations(interceptResult.Observations)
		latestInspection = p.inspector.Snapshot()
		if len(interceptResult.Chunks) == 0 && interceptResult.Intercepted == nil {
			p.settleStreamFirstByteDeadlineReadDecision(false)
		}
		if returnResult, err, handled := p.handleInterceptedBeforeWrite(interceptResult.Intercepted, latestInspection, false); handled {
			return returnResult, err
		}

		chunkWroteDownstream := false
		chunkCanEndAfterTerminal := false
		for outboundIndex, outbound := range interceptResult.Chunks {
			if !p.preCommitSseEvidence.DataEventObserved && !p.downstreamCommit.SemanticCommitted {
				p.preCommitSseEvidence.Push(outbound)
			}
			p.inspector.PushChunk(outbound)
			latestInspection = p.publishInspection(p.inspector.Snapshot())
			p.updateStreamInspectionProgress(latestInspection)
			p.omitBodyCaptureIfImageStream(latestInspection, false)
			if latestInspection.Skipped && !p.parserSkipLogged {
				p.parserSkipLogged = true
				p.logger.Warn("gateway_stream_inspector_skipped", map[string]any{
					"reason": latestInspection.SkipReason,
				}, "网关流式解析超过上限，已停止解析并继续转发")
			}
			outboundSseEventCount := latestInspection.EventCount - p.lastSseEventCount
			chunkCanEndAfterTerminal = chunkCanEndAfterTerminal || p.inspector.DrainEventSummariesCanEndStream()
			p.lastSseEventCount = latestInspection.EventCount
			if latestInspection.Skipped {
				p.lastSseEventActivityAt = nil
			} else if outboundSseEventCount > 0 || latestInspection.PendingEvent {
				value := p.lastUpstreamActivityAt
				p.lastSseEventActivityAt = &value
			}
			if p.pendingReadDecision != nil {
				semanticResultInRead := streamSemanticResultReceived(latestInspection) || p.preCommitSseEvidence.DataPayloadStarted
				lastOutboundInRead := outboundIndex == len(interceptResult.Chunks)-1
				if semanticResultInRead || lastOutboundInRead {
					if err := p.settleStreamFirstByteDeadlineReadDecision(semanticResultInRead); err != nil {
						return nil, err
					}
				} else {
					// Keep all transformed fragments from the same raw read
					// private until we know whether that read contains a
					// semantic result.
					if p.canKeepPreCommitBuffered(latestInspection, outbound) {
						p.appendPreCommitChunk(outbound)
					} else if p.shouldRejectOversizedUncommittedSseFraming(latestInspection, outbound) {
						return nil, &StreamPreCommitBufferExceededError{}
					} else if p.shouldKeepNonSemanticSseFramingPrivate() {
						ClearStreamPreCommitChunks(p.preCommitBuffer)
					} else {
						return nil, &StreamPreCommitBufferExceededError{}
					}
					continue
				}
			}
			if p.canKeepPreCommitBuffered(latestInspection, outbound) {
				p.appendPreCommitChunk(outbound)
				continue
			}
			if p.shouldRejectOversizedUncommittedSseFraming(latestInspection, outbound) {
				return nil, &StreamPreCommitBufferExceededError{}
			}
			if p.shouldKeepNonSemanticSseFramingPrivate() {
				ClearStreamPreCommitChunks(p.preCommitBuffer)
				continue
			}
			if p.interpretedProtocolFailure(latestInspection) {
				return p.handleProtocolFailure(latestInspection, false)
			}
			if err := p.flushPreCommitChunks(); err != nil {
				return nil, err
			}
			if _, err := p.writeDownstreamChunk(outbound, streamSemanticResultReceived(latestInspection) || p.preCommitSseEvidence.DataPayloadStarted); err != nil {
				return nil, err
			}
			if latestInspection.TerminalReceived && !p.interpretedProtocolFailure(latestInspection) {
				p.terminalEventWritten = true
				p.downstreamCommit.MarkSuccessfulProtocolTerminalReceived()
			}
			chunkWroteDownstream = true
		}
		if !latestInspection.Skipped && p.lastSseEventActivityAt == nil {
			value := p.lastUpstreamActivityAt
			p.lastSseEventActivityAt = &value
		}
		if returnResult, err, handled := p.handleInterceptedAfterChunks(interceptResult.Intercepted, latestInspection); handled {
			return returnResult, err
		}
		if interceptResult.PassthroughUpstreamFailure {
			return p.handlePassthroughTerminal(latestInspection, false)
		}
		if p.interceptor == nil && (chunkWroteDownstream || len(p.preCommitBuffer.Chunks) > 0) &&
			latestInspection.TerminalReceived && !p.interpretedProtocolFailure(latestInspection) &&
			chunkCanEndAfterTerminal && !p.pendingProtocolEvent {
			if err := p.flushPreCommitChunks(); err != nil {
				return nil, err
			}
			p.terminalEventWritten = true
			p.downstreamCommit.MarkSuccessfulProtocolTerminalReceived()
			finalInspection := p.publishInspection(p.inspector.Finish())
			result, err := p.finishTerminalSuccess(finalInspection, p.driver.DrainForKeepAliveAfterTerminal(), true)
			if err != nil {
				return nil, err
			}
			return &result, nil
		}
	}

	// ---- EOF：transformed chunks 收尾段（Node try 块内 while 之后）----
	return p.eofFlushPhase()
}

func (p *streamPipe) optionsInterpretDisabled() bool {
	return p.options.InterpretProtocolFailuresSet && !p.options.InterpretProtocolFailures
}

// interpretedProtocolFailure 对齐 interpretedProtocolFailure 闭包。
func (p *streamPipe) interpretedProtocolFailure(inspection gatewayproto.StreamInspection) bool {
	return inspection.FailedReceived && !p.passthroughUpstreamFailure
}
