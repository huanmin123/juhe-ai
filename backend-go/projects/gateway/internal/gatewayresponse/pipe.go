package gatewayresponse

import (
	"errors"
	"io"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// streamPipe 承载 pipeUpstreamStream 的全部状态（Node 闭包变量的结构化版本）。
type streamPipe struct {
	input       PipeUpstreamStreamInput
	body        UpstreamBody
	downstream  StreamDownstream
	profile     TimeoutProfile
	startedAt   int64
	options     StreamPipeOptions
	driver      StreamDriver
	inspector   gatewayproto.StreamInspector
	interceptor StreamInterceptor
	logger      StreamLogger
	nowMs       func() int64

	interpretProtocolFailures  bool
	captureSuccessPayloads     bool
	passthroughUpstreamFailure bool
	// suppressInjectedUsageChunk：网关注入 include_usage 的流（见
	// StreamPipeOptions.SuppressGatewayInjectedUsageChunk），写下游前剥除
	// usage-only SSE data 帧。
	suppressInjectedUsageChunk bool

	completed                          bool
	parserSkipLogged                   bool
	responseInspectionParserSkipLogged bool
	firstTokenMs                       *int64
	firstByteDeadlineObserved          bool
	pendingReadDecision                *streamFirstByteDeadlineReadDecision
	waitingForFirstChunk               bool
	lastUpstreamActivityAt             int64
	lastSseEventActivityAt             *int64
	lastSseEventCount                  int
	upstreamChunkReceived              bool
	semanticResultReceived             bool
	responseResourceId                 string
	pendingProtocolEvent               bool
	streamParserSkipped                bool
	protocolTerminalReceived           bool
	chunkIndex                         int
	totalUpstreamBytes                 int64
	totalResponseBytes                 int64
	lastProgressLogAt                  int64
	terminalEventWritten               bool
	bodyCaptureOmitted                 bool
	downstreamPrepared                 bool
	downstreamCommit                   *DownstreamCommitState
	downstreamCommitPrepared           bool
	preCommitBuffer                    *PreCommitBufferState
	preCommitSseEvidence               *StreamPreCommitSseEvidence
	observations                       []ResponseInspectionDecision
	observationOmittedCount            int

	responseCapture   *LimitedCapture
	upstreamCapture   *LimitedCapture
	diagnosticCapture *LimitedCapture

	// 受限重放窗口状态（流式提交边界受限重放与异常终局处置设计 §3.1）：窗口
	// 开启后语义 chunk 只进缓冲不写下游，关闭条件四项先到先关——①终态事件置位
	// （终态 chunk 不入缓冲，先关窗按既有语义处置）②缓冲达 B（追加前判定）
	// ③真实截止 T（replayWindowTimer 到点强制关窗 flush）④上游 EOF/失败信号。
	// 关闭即一次性 flush + MarkSemanticCommitted（flushPreCommitChunks 既有
	// semantic 判定承接）。replayWindowTimeoutMs 自包内常量初始化，包内测试
	// 可覆写为短值验证真实截止。
	replayWindowEligible   bool
	replayWindowOpen       bool
	replayWindowClosed     bool
	replayWindowTimeoutMs  int64
	replayWindowTimer      *time.Timer
	replayWindowTimerCh    <-chan time.Time
	replayWindowDurationMs *int64
	// downstreamFirstWriteMs 是首个下游字节写出时刻 − 请求开始（§3.3：客户端
	// 可感知首字延迟的真口径，含窗口停留）。
	downstreamFirstWriteMs *int64
	// gateway_stream_event_parse_error 每请求首个 WARN 的去重位。
	parseErrorWarnLogged bool
	// parseErrorIsolator 是 ParseError 事件字节隔离器（§3.2.0：分类为解析失败
	// 的事件不写下游，与失败帧拦截同一「事件级分类先于写入」时序保证）。仅在
	// gatewayopenai inspector 驱动的直通管道装配（非 openai 协议自有
	// summary 机制，不在本设计范围）。
	parseErrorIsolator *parseErrorEventIsolator

	clientClosed bool
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
	// 请求身份线索（第二批日志补齐 1）：TraceID/AccountID 经 logger 适配器
	// 统一注入全部管道日志 fields（空值省略键），pipefinal 等日志点零改动。
	if options.TraceID != "" || options.AccountID != "" {
		logger = identityStreamLogger{inner: logger, traceID: options.TraceID, accountID: options.AccountID}
	}
	nowMs := options.NowMs
	if nowMs == nil {
		nowMs = defaultNowMs
	}
	driver := options.Driver
	if driver == nil {
		driver = DefaultOpenAIStreamDriver()
	}
	// Node pipeUpstreamStream 在管道内装配检查拦截器（stream.ts:215-233）：
	// 调用方未显式注入时按策略/重试条件装配，策略流式拦截接入生产管线。
	if options.Interceptor == nil {
		options.Interceptor = mountResponseInspectionInterceptor(options, driver)
	}
	commit := options.DownstreamCommitState
	if commit == nil {
		commit = &DownstreamCommitState{}
	}
	captureLimit := -1
	if options.CaptureSuccessPayloads {
		captureLimit = StreamAuditCaptureBytes
	}
	return &streamPipe{
		input:                      input,
		body:                       input.UpstreamBody,
		downstream:                 input.Downstream,
		profile:                    input.TimeoutProfile,
		startedAt:                  input.StartedAtMs,
		options:                    options,
		driver:                     driver,
		inspector:                  driver.NewStreamInspector(),
		interceptor:                options.Interceptor,
		logger:                     logger,
		nowMs:                      nowMs,
		interpretProtocolFailures:  options.InterpretProtocolFailures,
		captureSuccessPayloads:     options.CaptureSuccessPayloads,
		suppressInjectedUsageChunk: options.SuppressGatewayInjectedUsageChunk,
		// 窗口资格缺省启用（文本主路径），显式排除面关闭（设计 §3.1）。
		replayWindowEligible:   !options.PreCommitReplayWindowDisabled,
		replayWindowTimeoutMs:  StreamReplayWindowTimeoutMs,
		parseErrorIsolator:     newParseErrorEventIsolatorForDriver(driver),
		waitingForFirstChunk:   true,
		lastUpstreamActivityAt: input.StartedAtMs,
		lastProgressLogAt:      input.StartedAtMs,
		downstreamCommit:       commit,
		preCommitBuffer:        NewPreCommitBufferState(options.RetryBeforeDownstreamWriteUntilOutput),
		preCommitSseEvidence:   NewStreamPreCommitSseEvidence(),
		responseCapture:        NewLimitedCapture(captureLimit),
		upstreamCapture:        NewLimitedCapture(captureLimit),
		diagnosticCapture:      NewLimitedCapture(StreamDiagnosticCaptureBytes),
	}
}

// run 对齐 Node 的 try → catch → finally → 后置收尾顺序。
// loop 返回 (result, nil) 表示 try 块内直接 return；返回 (nil, err) 表示异常。
func (p *streamPipe) run() (StreamPipeResult, error) {
	// 受限重放窗口 Timer 的管道结束兜底释放（设计 §3.1 条件③：窗口因其他条件
	// 关闭或管道结束时 Stop，防泄漏）；窗口已关闭时为幂等 no-op。
	defer p.closeReplayWindow()
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
		// 该快照只有拦截决策路径消费（handleInterceptedBeforeWrite 对 nil 决策
		// 直接返回）；无决策时出站循环内每个 chunk 还会各出一次快照，这里省去
		// 每读一次的整状态拷贝。Chunks 为空且无决策时检查器状态未变，后续读到
		// 的旧快照与现值等价。
		if interceptResult.Intercepted != nil {
			latestInspection = p.inspector.Snapshot()
		}
		if len(interceptResult.Chunks) == 0 && interceptResult.Intercepted == nil {
			p.settleStreamFirstByteDeadlineReadDecision(false)
		}
		if returnResult, err, handled := p.handleInterceptedBeforeWrite(interceptResult.Intercepted, latestInspection, false); handled {
			return returnResult, err
		}

		chunkWroteDownstream := false
		chunkCanEndAfterTerminal := false
		for outboundIndex, outbound := range interceptResult.Chunks {
			// 下游可见字节：网关注入 include_usage 的流（suppressInjectedUsageChunk）
			// 在 inspector 消费原始分片之后剥除 usage-only SSE data 帧——计量提取
			// 照常，仅下游字节不含该帧。整片为 usage-only 帧时下游零写入、不进
			// 预提交缓冲（下方 continue 点）；部分命中时写下游的是剥除后字节。
			downstreamChunk := outbound
			if p.suppressInjectedUsageChunk {
				if filtered, dropped := filterGatewayInjectedUsageOnlySseFrames(outbound); dropped {
					downstreamChunk = filtered
				}
			}
			// ParseError 事件字节隔离（设计 §3.2.0）：inspector 下方消费原始分片
			// 照常计数，解析失败事件的字节不进入下游可见字节。
			if p.parseErrorIsolator != nil && len(downstreamChunk) > 0 {
				downstreamChunk = p.parseErrorIsolator.push(downstreamChunk)
			}
			if !p.preCommitSseEvidence.DataEventObserved && !p.downstreamCommit.SemanticCommitted && len(downstreamChunk) > 0 {
				p.preCommitSseEvidence.Push(downstreamChunk)
			}
			p.inspector.PushChunk(outbound)
			latestInspection = p.publishInspection(p.inspector.Snapshot())
			p.updateStreamInspectionProgress(latestInspection)
			// 受限重放窗口开启点（设计 §3.1）：首个语义输出已识别、仍处提交前
			// 缓冲时置位窗口，此后语义 chunk 只进缓冲不写下游，直至窗口关闭。
			p.maybeOpenReplayWindow(latestInspection)
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
			if len(downstreamChunk) == 0 && len(outbound) > 0 {
				// 整片为被剥除的 usage-only 帧：inspector 已消费计量（上方
				// PushChunk 原始分片），下游零写入、不进预提交缓冲。
				continue
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
					if p.canKeepPreCommitBuffered(latestInspection, downstreamChunk) {
						p.appendPreCommitChunk(downstreamChunk)
					} else if p.shouldRejectOversizedUncommittedSseFraming(latestInspection, downstreamChunk) {
						return nil, &StreamPreCommitBufferExceededError{}
					} else if p.shouldKeepNonSemanticSseFramingPrivate() {
						ClearStreamPreCommitChunks(p.preCommitBuffer)
					} else {
						return nil, &StreamPreCommitBufferExceededError{}
					}
					continue
				}
			}
			if p.canKeepPreCommitBuffered(latestInspection, downstreamChunk) {
				p.appendPreCommitChunk(downstreamChunk)
				continue
			}
			if p.shouldRejectOversizedUncommittedSseFraming(latestInspection, downstreamChunk) {
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
			if _, err := p.writeDownstreamChunk(downstreamChunk, streamSemanticResultReceived(latestInspection) || p.preCommitSseEvidence.DataPayloadStarted); err != nil {
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
