package gatewayresponse

import (
	"errors"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 管道分支的共享实现：EOF 段、检查拦截、协议失败、透传终态、
// 终态成功收尾、错误处理与结果组装。

// eofFlushPhase 对齐 Node try 块内 while 之后的 EOF transformed chunks 段。
func (p *streamPipe) eofFlushPhase() (*StreamPipeResult, error) {
	var eofTransformedChunks [][]byte
	if p.options.FlushTransformedUpstreamChunks != nil {
		eofTransformedChunks = p.options.FlushTransformedUpstreamChunks()
	}
	var eofInterceptResult StreamInterceptorSseResult
	if p.interceptor != nil {
		eofInterceptResult = mergeInterceptorResults(p.pushResponseInspectionChunks(eofTransformedChunks), p.interceptor.FlushPendingOnEOF())
	} else {
		eofInterceptResult = StreamInterceptorSseResult{Chunks: eofTransformedChunks}
	}
	p.passthroughUpstreamFailure = p.passthroughUpstreamFailure || eofInterceptResult.PassthroughUpstreamFailure
	if eofInterceptResult.ParserSkipped && !p.responseInspectionParserSkipLogged {
		p.responseInspectionParserSkipLogged = true
		p.logger.Info("gateway_response_inspection_parser_skipped", nil, "网关流式事件过大，兜底拦截停止解析并继续原样转发")
	}
	p.recordResponseInspectionObservations(eofInterceptResult.Observations)
	// EOF 阶段分片同样过 ParseError 隔离器（§3.2.0 与主循环同语义）。隔离器
	// 持有的未完结事件尾字节不回灌 inspector（其 pending 行已持有同源字节，
	// 重复推送会拼接成非法载荷），而是并入预提交缓冲走既有 flush/丢弃判定；
	// 补投证据机使非语义帧清空判定与主循环同据（非 SSE 直通体等形态保持既有
	// 提交前缓冲行为），尾字节也不随 EOF 空列表早退丢失。
	eofChunks := eofInterceptResult.Chunks
	if p.parseErrorIsolator != nil {
		for index, chunk := range eofChunks {
			if len(chunk) > 0 {
				eofChunks[index] = p.parseErrorIsolator.push(chunk)
			}
		}
		if tail := p.parseErrorIsolator.flushPending(); len(tail) > 0 {
			if !p.preCommitSseEvidence.DataEventObserved && !p.downstreamCommit.SemanticCommitted {
				p.preCommitSseEvidence.Push(tail)
			}
			p.appendPreCommitChunk(tail)
		}
	}
	if len(eofChunks) == 0 && eofInterceptResult.Intercepted == nil {
		return nil, nil
	}
	latestInspection := p.inspector.Snapshot()
	if returnResult, err, handled := p.handleInterceptedBeforeWrite(eofInterceptResult.Intercepted, latestInspection, true); handled {
		return returnResult, err
	}
	eofWroteDownstream := false
	eofCanEndAfterTerminal := false
	for _, outbound := range eofChunks {
		// 与主循环同语义：网关注入 include_usage 的流在 inspector 消费原始分片
		// 之后剥除 usage-only 帧；整片命中时下游零写入、不进预提交缓冲。
		// ParseError 隔离已在上方 eofChunks 预处理完成，此处只消费过滤结果。
		downstreamChunk := outbound
		if p.suppressInjectedUsageChunk {
			if filtered, dropped := filterGatewayInjectedUsageOnlySseFrames(outbound); dropped {
				downstreamChunk = filtered
			}
		}
		if !p.preCommitSseEvidence.DataEventObserved && !p.downstreamCommit.SemanticCommitted && len(downstreamChunk) > 0 {
			p.preCommitSseEvidence.Push(downstreamChunk)
		}
		p.inspector.PushChunk(outbound)
		latestInspection = p.publishInspection(p.inspector.Snapshot())
		p.updateStreamInspectionProgress(latestInspection)
		p.omitBodyCaptureIfImageStream(latestInspection, true)
		if latestInspection.Skipped && !p.parserSkipLogged {
			p.parserSkipLogged = true
			p.logger.Warn("gateway_stream_inspector_skipped", map[string]any{
				"reason": latestInspection.SkipReason,
			}, "网关流式解析超过上限，已停止解析并继续转发")
		}
		outboundSseEventCount := latestInspection.EventCount - p.lastSseEventCount
		eofCanEndAfterTerminal = eofCanEndAfterTerminal || p.inspector.DrainEventSummariesCanEndStream()
		p.lastSseEventCount = latestInspection.EventCount
		if latestInspection.Skipped {
			p.lastSseEventActivityAt = nil
		} else if outboundSseEventCount > 0 || latestInspection.PendingEvent {
			value := p.lastUpstreamActivityAt
			p.lastSseEventActivityAt = &value
		}
		if len(downstreamChunk) == 0 && len(outbound) > 0 {
			// 整片为被剥除的 usage-only 帧：inspector 已消费计量，下游零写入。
			continue
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
			return p.handleProtocolFailure(latestInspection, true)
		}
		// 空完成终局判定（设计 §3.2.3 L1）：EOF flush 段与主循环同语义对称位。
		if p.emptyCompletionFailureDetected(latestInspection) {
			result, finalizeErr := p.finalizeEmptyCompletion(latestInspection)
			return &result, finalizeErr
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
		eofWroteDownstream = true
	}
	if eofInterceptResult.PassthroughUpstreamFailure {
		return p.handlePassthroughTerminal(latestInspection, true)
	}
	if eofInterceptResult.Intercepted != nil {
		decision := eofInterceptResult.Intercepted
		failurePayload := ResponseInspectionFailurePayloadForDecision(decision, p.options.ClientRetryEnabled)
		if p.shouldFailBeforeDownstreamCommit() {
			p.logger.Warn("gateway_response_inspected_before_downstream_commit", map[string]any{
				"elapsedMs":          p.nowMs() - p.startedAt,
				"chunkCount":         p.chunkIndex,
				"totalUpstreamBytes": p.totalUpstreamBytes,
				"totalResponseBytes": p.totalResponseBytes,
				"action":             decision.Action,
				"reason":             decision.Reason,
				"upstreamEventType":  decision.UpstreamEventType,
				"upstreamErrorCode":  decision.UpstreamErrorCode,
				"rewriteErrorCode":   decision.RewriteErrorCode,
				"downstreamWritten":  decision.DownstreamWritten,
				"eofPendingFlush":    true,
			}, "网关在 EOF pending 事件下游提交前命中流式失败，交由上层返回当前失败")
			return p.inspectionFailureResult(decision, latestInspection), nil
		}
		if err := p.input.HandleStreamFailure(failurePayload.Message, failurePayload.ErrorCode, p.streamFailureContext(p.totalResponseBytes, latestInspection.OutputReceived, false, false)); err != nil {
			return nil, err
		}
		p.interruptResponse()
		p.logger.Warn("gateway_response_inspected", map[string]any{
			"elapsedMs":          p.nowMs() - p.startedAt,
			"chunkCount":         p.chunkIndex,
			"totalUpstreamBytes": p.totalUpstreamBytes,
			"totalResponseBytes": p.totalResponseBytes,
			"action":             decision.Action,
			"reason":             decision.Reason,
			"upstreamEventType":  decision.UpstreamEventType,
			"upstreamErrorCode":  decision.UpstreamErrorCode,
			"rewriteErrorCode":   decision.RewriteErrorCode,
			"downstreamWritten":  decision.DownstreamWritten,
			"eofPendingFlush":    true,
		}, "网关已在上游 EOF 时命中响应检查策略并中断当前流")
		return p.inspectionFailureResult(decision, latestInspection), nil
	}
	if (eofWroteDownstream || len(p.preCommitBuffer.Chunks) > 0) &&
		latestInspection.TerminalReceived && !p.interpretedProtocolFailure(latestInspection) &&
		eofCanEndAfterTerminal && !p.pendingProtocolEvent {
		// 空完成终局判定（设计 §3.2.3 L1）：EOF 段终态成功臂写出前拦截。
		if p.emptyCompletionFailureDetected(latestInspection) {
			result, finalizeErr := p.finalizeEmptyCompletion(latestInspection)
			return &result, finalizeErr
		}
		if err := p.flushPreCommitChunks(); err != nil {
			return nil, err
		}
		p.terminalEventWritten = true
		p.downstreamCommit.MarkSuccessfulProtocolTerminalReceived()
		result, err := p.finishTerminalSuccess(latestInspection, false, true)
		if err != nil {
			return nil, err
		}
		return &result, nil
	}
	return nil, nil
}

// handleInterceptedBeforeWrite 对齐主循环与 EOF 段共用的
// intercepted + downstreamWritten !== true 两个前置分支。
func (p *streamPipe) handleInterceptedBeforeWrite(decision *ResponseInspectionDecision, latestInspection gatewayproto.StreamInspection, eofPendingFlush bool) (*StreamPipeResult, error, bool) {
	if decision == nil || decision.DownstreamWritten {
		return nil, nil, false
	}
	if !ShouldReturnResponseInspectionBeforeDownstreamWrite(decision, p.responseState(), p.totalResponseBytes) {
		if !p.shouldFailBeforeDownstreamCommit() {
			return nil, nil, false
		}
		// intercepted + shouldFailBeforeDownstreamCommit 分支
		p.settleStreamFirstByteDeadlineReadDecision(true)
		p.body.Close()
		fields := map[string]any{
			"elapsedMs":          p.nowMs() - p.startedAt,
			"chunkCount":         p.chunkIndex,
			"totalUpstreamBytes": p.totalUpstreamBytes,
			"action":             decision.Action,
			"reason":             decision.Reason,
			"upstreamEventType":  decision.UpstreamEventType,
			"upstreamErrorCode":  decision.UpstreamErrorCode,
			"rewriteErrorCode":   decision.RewriteErrorCode,
			"downstreamWritten":  decision.DownstreamWritten,
		}
		if eofPendingFlush {
			fields["eofPendingFlush"] = true
		}
		p.logger.Warn("gateway_response_inspected_before_downstream_commit", fields, "网关在下游提交前命中流式失败，交由上层返回当前失败")
		return p.inspectionFailureResult(decision, latestInspection), nil, true
	}
	p.settleStreamFirstByteDeadlineReadDecision(true)
	p.body.Close()
	fields := map[string]any{
		"elapsedMs":          p.nowMs() - p.startedAt,
		"chunkCount":         p.chunkIndex,
		"totalUpstreamBytes": p.totalUpstreamBytes,
		"action":             decision.Action,
		"reason":             decision.Reason,
		"policyId":           decision.PolicyID,
		"policyName":         decision.PolicyName,
		"accountSwitch":      decision.AccountSwitch,
		"retryEnabled":       decision.RetryEnabled,
	}
	message := "网关在写入下游前命中可服务端重试的响应检查策略"
	if eofPendingFlush {
		fields["eofPendingFlush"] = true
		message = "网关在 EOF pending 事件写入下游前命中可服务端重试的响应检查策略"
	}
	p.logger.Warn("gateway_response_inspected_before_downstream_write", fields, message)
	return p.inspectionFailureResult(decision, latestInspection), nil, true
}

// handleInterceptedAfterChunks 对齐主循环 chunk 循环结束后的 intercepted 分支。
func (p *streamPipe) handleInterceptedAfterChunks(decision *ResponseInspectionDecision, latestInspection gatewayproto.StreamInspection) (*StreamPipeResult, error, bool) {
	if decision == nil {
		return nil, nil, false
	}
	p.body.Close()
	failurePayload := ResponseInspectionFailurePayloadForDecision(decision, p.options.ClientRetryEnabled)
	if p.shouldFailBeforeDownstreamCommit() {
		p.logger.Warn("gateway_response_inspected_before_downstream_commit", map[string]any{
			"elapsedMs":          p.nowMs() - p.startedAt,
			"chunkCount":         p.chunkIndex,
			"totalUpstreamBytes": p.totalUpstreamBytes,
			"totalResponseBytes": p.totalResponseBytes,
			"action":             decision.Action,
			"reason":             decision.Reason,
			"upstreamEventType":  decision.UpstreamEventType,
			"upstreamErrorCode":  decision.UpstreamErrorCode,
			"rewriteErrorCode":   decision.RewriteErrorCode,
			"downstreamWritten":  decision.DownstreamWritten,
		}, "网关在下游提交前命中流式失败，交由上层返回当前失败")
		return p.inspectionFailureResult(decision, latestInspection), nil, true
	}
	if err := p.input.HandleStreamFailure(failurePayload.Message, failurePayload.ErrorCode, p.streamFailureContext(p.totalResponseBytes, latestInspection.OutputReceived, false, false)); err != nil {
		return nil, err, true
	}
	p.interruptResponse()
	p.logger.Warn("gateway_response_inspected", map[string]any{
		"elapsedMs":          p.nowMs() - p.startedAt,
		"chunkCount":         p.chunkIndex,
		"totalUpstreamBytes": p.totalUpstreamBytes,
		"totalResponseBytes": p.totalResponseBytes,
		"action":             decision.Action,
		"reason":             decision.Reason,
		"upstreamEventType":  decision.UpstreamEventType,
		"upstreamErrorCode":  decision.UpstreamErrorCode,
		"rewriteErrorCode":   decision.RewriteErrorCode,
		"downstreamWritten":  decision.DownstreamWritten,
	}, "网关已命中响应检查策略并中断当前流")
	return p.inspectionFailureResult(decision, latestInspection), nil, true
}

func (p *streamPipe) inspectionFailureResult(decision *ResponseInspectionDecision, latestInspection gatewayproto.StreamInspection) *StreamPipeResult {
	failurePayload := ResponseInspectionFailurePayloadForDecision(decision, p.options.ClientRetryEnabled)
	result := p.finishStreamResult(finishArgs{
		message: failurePayload.Message, errorCode: failurePayload.ErrorCode,
		usage:                 latestInspection.Usage,
		outputReceived:        latestInspection.OutputReceived,
		estimatedOutputTokens: latestInspection.EstimatedOutputTokens,
		imageOutputReceived:   latestInspection.ImageOutputReceived,
		responseInspection:    decision,
	})
	return &result
}

// handleProtocolFailure 对齐 interpretedProtocolFailure 主分支（主循环 + EOF 段
// 共用）；eofPendingFlush 只影响日志。
func (p *streamPipe) handleProtocolFailure(latestInspection gatewayproto.StreamInspection, eofPendingFlush bool) (*StreamPipeResult, error) {
	// 失败帧/失败终态是窗口关闭条件④（设计 §3.1 条件④ + §3.1.2 不变量 3）：
	// 失败帧字节不经窗口 flush 透传，先关窗再按既有语义处置。
	p.closeReplayWindow()
	beforeDownstreamCommit := p.shouldFailBeforeDownstreamCommit()
	if beforeDownstreamCommit {
		TakeStreamPreCommitChunks(p.preCommitBuffer)
	}
	message := orDefault(latestInspection.ErrorMessage, "上游流式响应返回失败终态")
	errorCode := ""
	if p.optionsInterpretDisabled() {
		errorCode = "upstream_protocol_failure"
	} else if latestInspection.ErrorCode != "" {
		errorCode = latestInspection.ErrorCode
	} else {
		errorCode = "upstream_protocol_failure"
	}
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, latestInspection.OutputReceived, p.interpretedProtocolFailure(latestInspection), p.interpretedProtocolFailure(latestInspection))); err != nil {
		return nil, err
	}
	p.body.Close()
	if !beforeDownstreamCommit {
		if err := p.signalCommittedStreamFailure(); err != nil {
			return nil, err
		}
	}
	event := "gateway_stream_failure_after_downstream_commit_interrupted"
	logMessage := "网关在下游提交后解析到流式失败，已丢弃供应商失败原文并中断连接"
	if beforeDownstreamCommit {
		event = "gateway_stream_failure_before_downstream_commit"
		logMessage = "网关在下游提交前解析到流式失败，交由上层返回当前失败"
	}
	fields := map[string]any{
		"message":             message,
		"errorCode":           errorCode,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"chunkIndex":          p.chunkIndex,
		"sseEventCount":       latestInspection.EventCount,
		"recentSseEventTypes": latestInspection.RecentEventTypes,
	}
	if eofPendingFlush {
		fields["eofPendingFlush"] = true
		if !beforeDownstreamCommit {
			logMessage = "网关在 EOF pending 下游提交后解析到流式失败，已中断当前连接"
		} else {
			logMessage = "网关在 EOF pending 下游提交前解析到流式失败，交由上层返回当前失败"
		}
	}
	p.logger.Warn(event, fields, logMessage)
	result := p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: latestInspection.Usage,
		outputReceived:        latestInspection.OutputReceived,
		estimatedOutputTokens: latestInspection.EstimatedOutputTokens,
		imageOutputReceived:   latestInspection.ImageOutputReceived,
	})
	return &result, nil
}

// handlePassthroughTerminal 对齐 passthroughUpstreamFailure 终态分支。
func (p *streamPipe) handlePassthroughTerminal(latestInspection gatewayproto.StreamInspection, eofPendingFlush bool) (*StreamPipeResult, error) {
	if !eofPendingFlush {
		p.body.Close()
	}
	p.endResponse()
	fields := map[string]any{
		"elapsedMs":          p.nowMs() - p.startedAt,
		"chunkCount":         p.chunkIndex,
		"totalUpstreamBytes": p.totalUpstreamBytes,
		"totalResponseBytes": p.totalResponseBytes,
		"upstreamEventType":  latestInspection.LastEventType,
		"upstreamErrorCode":  latestInspection.ErrorCode,
	}
	message := "网关已原样转发 Codex 上游失败终态并结束当前流"
	if eofPendingFlush {
		fields["eofPendingFlush"] = true
		message = "网关已在上游 EOF 时原样转发 Codex 失败终态"
	}
	p.logger.Info("gateway_stream_passthrough_upstream_failure_terminal", fields, message)
	result := p.finishStreamResult(finishArgs{
		completed: true, message: "已原样转发上游失败终态", usage: latestInspection.Usage,
		outputReceived:             latestInspection.OutputReceived,
		estimatedOutputTokens:      latestInspection.EstimatedOutputTokens,
		imageOutputReceived:        latestInspection.ImageOutputReceived,
		passthroughUpstreamFailure: true,
	})
	return &result, nil
}

// finalizeAfterLoop 对齐 try 块之后（无异常、无提前 return）的终态判定。
func (p *streamPipe) finalizeAfterLoop() (StreamPipeResult, error) {
	// 上游 EOF 是窗口关闭条件④（设计 §3.1）：先关窗（时长与 Timer 收口），
	// 已缓冲内容随后按各判定臂的既有 flush 语义处置。
	p.closeReplayWindow()
	p.preCommitSseEvidence.Finish()
	if p.shouldKeepNonSemanticSseFramingPrivate() {
		ClearStreamPreCommitChunks(p.preCommitBuffer)
	}
	inspection := p.publishInspection(p.inspector.Finish())
	p.omitBodyCaptureIfImageStream(inspection, true)

	// 异常终局判定（设计 §3.2.1 判定 2+3，互斥序先于 generic EOF 收敛臂与
	// finalizeParserSkipped）：generic 客户端的流以干净 EOF 收尾，且——
	// ① EOF 前存在解析失败事件（判定 3，要求非真终止：终止事件已到的流
	// 属上游自愈完成仍 success）；② Finish 冲出未收尾失败帧（判定 2：失败
	// 帧缺终止空行时 FailedReceived 隐含 Terminal 置位，若不在此截流会漏进
	// generic 收敛臂判 success，绕过失败归因）——均不再收敛 success，改按
	// 失败结算；先 ParseError 后 Skipped 的流同样归因 abnormal_eof。精确画像
	//（interpretProtocolFailures）不落入本臂。
	if !p.interpretProtocolFailures && p.completed &&
		(inspection.FailedReceived ||
			(!inspection.TerminalReceived && inspection.ParseErrorEventCount > 0)) {
		return p.finalizeAbnormalStreamEOF(inspection)
	}

	// 空完成终局判定（设计 §3.2.3 L1，v2.3）：协议终态已到且全程零语义输出
	// → 不收敛 success，改按 upstream_stream_empty_completion 失败结算。放在
	// generic 收敛臂（下方）与精确 success 臂（本函数末臂）之前——控制流必然
	// 先经过本判定，一处插入同时覆盖两个 success 臂，覆盖「Finish 冲刷才置位
	// Terminal」的残片形态（此时缓冲未 flush、totalResponseBytes==0，仍走提交
	// 前分支）。判定 4（无终止事件干净 EOF 零输出）不受影响：谓词要求
	// TerminalReceived。
	if p.emptyCompletionFailureDetected(inspection) {
		return p.finalizeEmptyCompletion(inspection)
	}

	// Generic clients keep opaque upstream SSE semantics: once at least one
	// real SSE data event was observed, a clean transport EOF is sufficient
	// even when the protocol driver does not recognize a provider-specific
	// terminal. Comments/heartbeats are transport-only and must remain
	// pre-commit so an empty or keep-alive-only stream can still be retried by
	// the server. Precise clients use interpretProtocolFailures and still
	// require framing.
	if !p.interpretProtocolFailures && p.completed && p.preCommitSseEvidence.DataEventObserved {
		if err := p.flushPreCommitChunks(); err != nil {
			return StreamPipeResult{}, err
		}
		p.endResponse()
		p.logger.Info("gateway_stream_finished_success", map[string]any{
			"elapsedMs":                 p.nowMs() - p.startedAt,
			"chunkCount":                p.chunkIndex,
			"totalUpstreamBytes":        p.totalUpstreamBytes,
			"totalResponseBytes":        p.totalResponseBytes,
			"firstTokenMs":              int64PtrValue(p.firstTokenMs),
			"sseEventCount":             inspection.EventCount,
			"sseEventTypeCounts":        inspection.EventTypeCounts,
			"recentSseEventTypes":       inspection.RecentEventTypes,
			"outputReceived":            inspection.OutputReceived,
			"outputEventCount":          inspection.OutputEventCount,
			"parseErrorEventCount":      inspection.ParseErrorEventCount,
			"invalidProtocolEventCount": inspection.InvalidProtocolEventCount,
			"windowDurationMs":          int64PtrValue(p.replayWindowDurationMs),
			"downstreamFirstWriteMs":    int64PtrValue(p.downstreamFirstWriteMs),
		}, "网关流式响应已成功结束")
		return p.finishStreamResult(finishArgs{
			completed: true, message: "已完成", usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}
	if inspection.Skipped && p.preCommitSseEvidence.DataEventObserved {
		return p.finalizeParserSkipped(inspection)
	}
	if !inspection.TerminalReceived {
		return p.finalizeMissingTerminal(inspection)
	}
	if !p.completed || p.interpretedProtocolFailure(inspection) {
		return p.finalizeIncompleteOrFailed(inspection)
	}
	if err := p.flushPreCommitChunks(); err != nil {
		return StreamPipeResult{}, err
	}
	p.endResponse()
	p.logger.Info("gateway_stream_finished_success", map[string]any{
		"elapsedMs":                 p.nowMs() - p.startedAt,
		"chunkCount":                p.chunkIndex,
		"totalUpstreamBytes":        p.totalUpstreamBytes,
		"totalResponseBytes":        p.totalResponseBytes,
		"firstTokenMs":              int64PtrValue(p.firstTokenMs),
		"sseEventCount":             inspection.EventCount,
		"sseEventTypeCounts":        inspection.EventTypeCounts,
		"recentSseEventTypes":       inspection.RecentEventTypes,
		"outputReceived":            inspection.OutputReceived,
		"outputEventCount":          inspection.OutputEventCount,
		"parseErrorEventCount":      inspection.ParseErrorEventCount,
		"invalidProtocolEventCount": inspection.InvalidProtocolEventCount,
		"windowDurationMs":          int64PtrValue(p.replayWindowDurationMs),
		"downstreamFirstWriteMs":    int64PtrValue(p.downstreamFirstWriteMs),
	}, "网关流式响应已成功结束")
	return p.finishStreamResult(finishArgs{
		completed: true, message: "已完成", usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

// finalizeAbnormalStreamEOF 是 §3.2.1 判定 3 的异常终局失败结算（§3.2.2）：
// generic 客户端的流在协议终止事件前以干净 EOF 收尾、且 EOF 前存在解析失败
// 事件——不再收敛 success。复用 finalizeMissingTerminal 的提交前/提交后两
// 分支骨架：提交前交由上层丢弃缓冲换号重放（客户端零字节）；提交后按
// 2026-10-23 契约真·网络级断开（客户端以其自身重试语义恢复）。error_code
// 固定 upstream_stream_abnormal_eof——它是 chain 面 classifyPostVerdictOutcome
// 失败归因三面的判别键，不按客户端重试文案改写（StreamClientFailureCode 的
// 客户端改写臂不适用）；探针不派发（availabilityProbeEligible=false：低质量
// 渠道高频断流时避免探针风暴，失败统计由账户锁承担）；usage 仍按可见输出
// 估算入账但 Success=false（finalizeStreamFailure 既有口径）。
func (p *streamPipe) finalizeAbnormalStreamEOF(inspection gatewayproto.StreamInspection) (StreamPipeResult, error) {
	message := "上游流在协议终止事件前以无法解析的异常事件收尾"
	errorCode := UpstreamStreamAbnormalEOFCode
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, false, false)); err != nil {
		return StreamPipeResult{}, err
	}
	fields := map[string]any{
		"elapsedMs":                 p.nowMs() - p.startedAt,
		"chunkCount":                p.chunkIndex,
		"totalUpstreamBytes":        p.totalUpstreamBytes,
		"totalResponseBytes":        p.totalResponseBytes,
		"firstTokenMs":              int64PtrValue(p.firstTokenMs),
		"sseEventCount":             inspection.EventCount,
		"sseEventTypeCounts":        inspection.EventTypeCounts,
		"recentSseEventTypes":       inspection.RecentEventTypes,
		"parseErrorEventCount":      inspection.ParseErrorEventCount,
		"invalidProtocolEventCount": inspection.InvalidProtocolEventCount,
		"windowDurationMs":          int64PtrValue(p.replayWindowDurationMs),
		"downstreamFirstWriteMs":    int64PtrValue(p.downstreamFirstWriteMs),
	}
	if p.shouldFailBeforeDownstreamCommit() {
		p.logger.Warn("gateway_stream_abnormal_eof_before_downstream_commit", fields, "网关在下游提交前识别到流异常终局，丢弃缓冲交由上层换号重放")
		return p.finishStreamResult(finishArgs{
			message: message, errorCode: errorCode, usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}
	if err := p.signalCommittedStreamFailure(); err != nil {
		return StreamPipeResult{}, err
	}
	p.logger.Warn("gateway_stream_abnormal_eof_interrupted", fields, "网关已因流异常终局中断连接，客户端以网络级断开触发自身重试")
	return p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

// emptyCompletionFailureDetected 报告当前流是否构成空完成终局（设计 §3.2.3
// L1，v2.3 增量）：协议终态已到（TerminalReceived）且全程零语义输出
// （!OutputReceived && !ImageOutputReceived）。现有 inspector 计数语义已保证
// tool_calls / reasoning_content / text / audio / refusal / responses delta /
// image 事件都置位 Output（usage-only 帧 choices 空数组不算输出），本谓词不
// 改分类器。**作用域限定 openai 驱动域**（与 §3.2.0 隔离器同款
// driver==openai 门）：生产实证空壳流在 openai 域；gemini 域存在合法的
// 零输出终态面——interactions create 的 interaction.completed 经
// firstGeminiFinishReason 投影为终态而天然零输出（实施验证反例，见设计文档
// §3.2.3 豁免面登记），全域生效会误伤该合法面，故 anthropic/gemini 域本批
// 不启用。豁免面：Codex passthroughUpstreamFailure 透传模式不判（同
// interpretedProtocolFailure 口径）；失败帧证据在场时交由既有协议失败路径
// 先行（各挂点序在协议失败判定之后，谓词再显式含 !FailedReceived 双保险）。
// 判定不消费状态，可被多个挂点重复询问。
func (p *streamPipe) emptyCompletionFailureDetected(inspection gatewayproto.StreamInspection) bool {
	if p.driver == nil || p.driver.ClientErrorProtocol() != "openai" {
		return false
	}
	return !p.passthroughUpstreamFailure &&
		!inspection.FailedReceived &&
		inspection.TerminalReceived &&
		!inspection.OutputReceived &&
		!inspection.ImageOutputReceived
}

// finalizeEmptyCompletion 是 §3.2.3 L1 空完成终局失败结算（v2.3）：流收到协议
// 终态且全程零语义输出（空壳成功流，生产实证 2026-10-26）不再收敛 success。
// 复用 finalizeAbnormalStreamEOF 的提交前/提交后两分支骨架：提交前（客户端零
// 字节）丢弃缓冲交由上层 RetryUpstream 换号重放；提交后（理论难达，兜底：大
// usage 帧序列冲开 256KB 预提交缓冲后终态才到）按真·网络级断开收口。
// error_code 固定 upstream_stream_empty_completion——chain 面
// classifyPostVerdictOutcome 按它做失败归因三面（经
// PreCommitStreamServerRetryErrorCode 原样透传豁免保持原样）；探针不派发
// （availabilityProbeEligible=false，同 abnormal_eof）；usage 照上游上报值入账
// 但 Success=false（finalizeStreamFailure 既有口径）。
func (p *streamPipe) finalizeEmptyCompletion(inspection gatewayproto.StreamInspection) (StreamPipeResult, error) {
	// 失败信号同属窗口关闭条件④：先关窗再处置（与 handleProtocolFailure /
	// finalizeAfterLoop 入口同序）。
	p.closeReplayWindow()
	beforeDownstreamCommit := p.shouldFailBeforeDownstreamCommit()
	if beforeDownstreamCommit {
		TakeStreamPreCommitChunks(p.preCommitBuffer)
	}
	message := "上游流式响应全程零语义输出即宣告完成"
	errorCode := UpstreamStreamEmptyCompletionCode
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, false, false)); err != nil {
		return StreamPipeResult{}, err
	}
	p.body.Close()
	if !beforeDownstreamCommit {
		if err := p.signalCommittedStreamFailure(); err != nil {
			return StreamPipeResult{}, err
		}
	}
	fields := map[string]any{
		"elapsedMs":                 p.nowMs() - p.startedAt,
		"chunkCount":                p.chunkIndex,
		"totalUpstreamBytes":        p.totalUpstreamBytes,
		"totalResponseBytes":        p.totalResponseBytes,
		"firstTokenMs":              int64PtrValue(p.firstTokenMs),
		"sseEventCount":             inspection.EventCount,
		"sseEventTypeCounts":        inspection.EventTypeCounts,
		"recentSseEventTypes":       inspection.RecentEventTypes,
		"outputReceived":            inspection.OutputReceived,
		"outputEventCount":          inspection.OutputEventCount,
		"parseErrorEventCount":      inspection.ParseErrorEventCount,
		"invalidProtocolEventCount": inspection.InvalidProtocolEventCount,
		"windowDurationMs":          int64PtrValue(p.replayWindowDurationMs),
		"downstreamFirstWriteMs":    int64PtrValue(p.downstreamFirstWriteMs),
	}
	event := "gateway_stream_empty_completion_before_downstream_commit"
	logMessage := "网关在下游提交前识别到上游空完成，丢弃缓冲交由上层换号重放"
	if !beforeDownstreamCommit {
		event = "gateway_stream_empty_completion_interrupted"
		logMessage = "网关已识别到提交后的上游空完成并中断连接，客户端以网络级断开触发自身重试"
	}
	p.logger.Warn(event, fields, logMessage)
	return p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

func (p *streamPipe) finalizeParserSkipped(inspection gatewayproto.StreamInspection) (StreamPipeResult, error) {
	success := p.completed && !p.interpretedProtocolFailure(inspection)
	message := orDefault(inspection.ErrorMessage, "上游流式响应返回失败终态")
	var errorCode string
	if success {
		message = "已完成"
	} else if p.optionsInterpretDisabled() && p.interpretedProtocolFailure(inspection) {
		errorCode = "upstream_protocol_failure"
	} else if inspection.ErrorCode != "" {
		errorCode = inspection.ErrorCode
	} else {
		errorCode = StreamClientFailureCode("upstream_protocol_failure", inspection.OutputReceived, p.options.ClientRetryEnabled, p.totalResponseBytes)
	}
	if !success {
		if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, p.interpretedProtocolFailure(inspection), !p.completed || p.interpretedProtocolFailure(inspection))); err != nil {
			return StreamPipeResult{}, err
		}
		if err := p.signalCommittedStreamFailure(); err != nil {
			return StreamPipeResult{}, err
		}
	} else {
		p.endResponse()
	}
	p.logger.Warn("gateway_stream_completed_with_parser_skipped", map[string]any{
		"completed":          p.completed,
		"success":            success,
		"elapsedMs":          p.nowMs() - p.startedAt,
		"chunkCount":         p.chunkIndex,
		"totalUpstreamBytes": p.totalUpstreamBytes,
		"totalResponseBytes": p.totalResponseBytes,
		"terminalReceived":   inspection.TerminalReceived,
		"failedReceived":     inspection.FailedReceived,
		"skipReason":         inspection.SkipReason,
	}, "网关流式解析已跳过，按原始转发结果结束")
	return p.finishStreamResult(finishArgs{
		completed: success, message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

func (p *streamPipe) finalizeMissingTerminal(inspection gatewayproto.StreamInspection) (StreamPipeResult, error) {
	message := "上游流在协议终止事件前结束"
	errorCode := StreamClientFailureCode(gatewaypreauth.GatewayStreamFailureCode(message), inspection.OutputReceived, p.options.ClientRetryEnabled, p.totalResponseBytes)
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, false, true)); err != nil {
		return StreamPipeResult{}, err
	}
	p.logger.Warn("gateway_stream_missing_terminal", map[string]any{
		"elapsedMs":           p.nowMs() - p.startedAt,
		"chunkCount":          p.chunkIndex,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"sseEventCount":       inspection.EventCount,
		"sseEventTypeCounts":  inspection.EventTypeCounts,
		"recentSseEventTypes": inspection.RecentEventTypes,
	}, "上游 EOF 前未收到协议终止事件")
	if p.shouldFailBeforeDownstreamCommit() {
		p.logger.Warn("gateway_stream_missing_terminal_before_downstream_commit", map[string]any{
			"errorCode":          errorCode,
			"totalUpstreamBytes": p.totalUpstreamBytes,
			"totalResponseBytes": p.totalResponseBytes,
		}, "网关在下游提交前发现上游缺少终止事件，交由上层返回当前失败")
		return p.finishStreamResult(finishArgs{
			message: message, errorCode: errorCode, usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}
	if err := p.signalCommittedStreamFailure(); err != nil {
		return StreamPipeResult{}, err
	}
	p.logger.Warn("gateway_stream_missing_terminal_interrupted", map[string]any{
		"errorCode":           errorCode,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"sseEventCount":       inspection.EventCount,
		"recentSseEventTypes": inspection.RecentEventTypes,
	}, "网关已因缺少终止事件中断连接")
	return p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

func (p *streamPipe) finalizeIncompleteOrFailed(inspection gatewayproto.StreamInspection) (StreamPipeResult, error) {
	message := "上游流式响应已中断"
	errorCode := ""
	if p.interpretedProtocolFailure(inspection) {
		message = orDefault(inspection.ErrorMessage, "上游流式响应返回失败终态")
		errorCode = "upstream_protocol_failure"
	} else if inspection.ErrorCode != "" {
		errorCode = inspection.ErrorCode
	} else {
		errorCode = StreamClientFailureCode(gatewaypreauth.GatewayStreamFailureCode(message), inspection.OutputReceived, p.options.ClientRetryEnabled, p.totalResponseBytes)
	}
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, p.interpretedProtocolFailure(inspection), !p.completed || p.interpretedProtocolFailure(inspection))); err != nil {
		return StreamPipeResult{}, err
	}
	if p.shouldFailBeforeDownstreamCommit() {
		p.logger.Warn("gateway_stream_finished_failed_before_downstream_commit", map[string]any{
			"completed":           p.completed,
			"errorCode":           errorCode,
			"totalUpstreamBytes":  p.totalUpstreamBytes,
			"totalResponseBytes":  p.totalResponseBytes,
			"sseEventCount":       inspection.EventCount,
			"recentSseEventTypes": inspection.RecentEventTypes,
		}, "网关在 EOF pending 收尾后识别到失败，交由上层返回当前失败")
		return p.finishStreamResult(finishArgs{
			message: message, errorCode: errorCode, usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}
	if err := p.signalCommittedStreamFailure(); err != nil {
		return StreamPipeResult{}, err
	}
	p.logger.Warn("gateway_stream_finished_failed_interrupted", map[string]any{
		"completed":           p.completed,
		"elapsedMs":           p.nowMs() - p.startedAt,
		"chunkCount":          p.chunkIndex,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"message":             message,
		"terminalReceived":    inspection.TerminalReceived,
		"failedReceived":      inspection.FailedReceived,
		"sseEventCount":       inspection.EventCount,
		"sseEventTypeCounts":  inspection.EventTypeCounts,
		"recentSseEventTypes": inspection.RecentEventTypes,
	}, "网关已中断失败流")
	return p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
	}), nil
}

// handlePipeError 对齐 catch 块。
func (p *streamPipe) handlePipeError(loopErr error) (StreamPipeResult, error) {
	if beforeCommitErr := (*StreamBeforeDownstreamCommitError)(nil); errors.As(loopErr, &beforeCommitErr) {
		return StreamPipeResult{}, beforeCommitErr.OriginalError
	}
	aborted := IsUpstreamRequestAbortedError(loopErr) || p.signalAborted()
	if aborted {
		inspection := p.publishInspection(p.inspector.Finish())
		p.omitBodyCaptureIfImageStream(inspection, true)
		if (p.terminalEventWritten || (inspection.TerminalReceived && p.downstreamCommit.SemanticCommitted)) && !p.interpretedProtocolFailure(inspection) {
			p.endResponse()
			p.logger.Info("gateway_stream_client_closed_after_terminal", map[string]any{
				"elapsedMs":            p.nowMs() - p.startedAt,
				"chunkCount":           p.chunkIndex,
				"totalUpstreamBytes":   p.totalUpstreamBytes,
				"totalResponseBytes":   p.totalResponseBytes,
				"signalAborted":        p.signalAborted(),
				"terminalEventWritten": p.terminalEventWritten,
				"outputReceived":       inspection.OutputReceived,
				"outputEventCount":     inspection.OutputEventCount,
				"sseEventCount":        inspection.EventCount,
				"sseEventTypeCounts":   inspection.EventTypeCounts,
				"recentSseEventTypes":  inspection.RecentEventTypes,
				"parserSkipped":        inspection.Skipped,
				"skipReason":           inspection.SkipReason,
			}, "客户端在协议终止事件后关闭连接，按成功流式响应收尾")
			return p.finishStreamResult(finishArgs{
				completed: true, message: "已完成", usage: inspection.Usage,
				outputReceived:        inspection.OutputReceived,
				estimatedOutputTokens: inspection.EstimatedOutputTokens,
				imageOutputReceived:   inspection.ImageOutputReceived,
			}), nil
		}
		// 不完整客户端断流的回调（Codex turn 状态记录）。
		if p.options.OnIncompleteClientAbort != nil &&
			p.downstreamCommit.SemanticCommitted &&
			p.totalResponseBytes > 0 &&
			inspection.OutputReceived &&
			!inspection.TerminalReceived &&
			!inspection.FailedReceived &&
			!inspection.Skipped &&
			!p.terminalEventWritten {
			context := IncompleteClientAbortContext{
				StreamFailureContext: p.streamFailureContext(p.totalResponseBytes, inspection.OutputReceived, false, false),
				SemanticCommitted:    true,
				TerminalReceived:     false,
				FailedReceived:       false,
				ParserSkipped:        false,
			}
			if err := p.options.OnIncompleteClientAbort(context); err != nil {
				p.logger.Warn("gateway_stream_incomplete_client_abort_callback_failed", nil, "Codex turn 不完整下游断流状态记录失败，按 downstream_closed 继续")
			}
		}
		p.logger.Warn("gateway_stream_aborted", map[string]any{
			"elapsedMs":           p.nowMs() - p.startedAt,
			"chunkCount":          p.chunkIndex,
			"totalUpstreamBytes":  p.totalUpstreamBytes,
			"totalResponseBytes":  p.totalResponseBytes,
			"signalAborted":       p.signalAborted(),
			"terminalReceived":    inspection.TerminalReceived,
			"failedReceived":      inspection.FailedReceived,
			"outputReceived":      inspection.OutputReceived,
			"outputEventCount":    inspection.OutputEventCount,
			"sseEventCount":       inspection.EventCount,
			"sseEventTypeCounts":  inspection.EventTypeCounts,
			"recentSseEventTypes": inspection.RecentEventTypes,
			"parserSkipped":       inspection.Skipped,
			"skipReason":          inspection.SkipReason,
			"errorMessage":        DownstreamConnectionClosedMessage,
		}, "网关流式转发因下游连接关闭而结束")
		p.endResponse()
		return StreamPipeResult{}, loopErr
	}
	if deadlineErr := asResponsePrecommitDeadline(loopErr); deadlineErr != nil {
		inspection := p.publishInspection(p.inspector.Finish())
		message := deadlineErr.Error()
		upstreamResponseCommitted := p.totalResponseBytes > 0
		p.logger.Warn("gateway_stream_response_precommit_deadline_exceeded", map[string]any{
			"elapsedMs":                 p.nowMs() - p.startedAt,
			"deadlineAtMs":              deadlineErr.DeadlineAtMs,
			"chunkCount":                p.chunkIndex,
			"totalUpstreamBytes":        p.totalUpstreamBytes,
			"totalResponseBytes":        p.totalResponseBytes,
			"upstreamResponseCommitted": upstreamResponseCommitted,
			"outputReceived":            inspection.OutputReceived,
			"terminalReceived":          inspection.TerminalReceived,
		}, "网关请求墙钟到达时流式响应仍未产生可提交语义结果")
		if upstreamResponseCommitted {
			p.interruptResponse()
		}
		return p.finishStreamResult(finishArgs{
			message: message, errorCode: deadlineErr.Code(), usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}

	inspection := p.publishInspection(p.inspector.Finish())
	p.omitBodyCaptureIfImageStream(inspection, true)
	// 管道错误路径同样收口窗口（关闭条件④：读错误/失败信号），Timer 与时长
	// 一并释放。
	p.closeReplayWindow()
	p.logger.Warn("gateway_stream_pipe_error", map[string]any{
		"elapsedMs":           p.nowMs() - p.startedAt,
		"chunkCount":          p.chunkIndex,
		"totalUpstreamBytes":  p.totalUpstreamBytes,
		"totalResponseBytes":  p.totalResponseBytes,
		"terminalReceived":    inspection.TerminalReceived,
		"failedReceived":      inspection.FailedReceived,
		"outputReceived":      inspection.OutputReceived,
		"outputEventCount":    inspection.OutputEventCount,
		"sseEventCount":       inspection.EventCount,
		"sseEventTypeCounts":  inspection.EventTypeCounts,
		"recentSseEventTypes": inspection.RecentEventTypes,
		"parserSkipped":       inspection.Skipped,
		"skipReason":          inspection.SkipReason,
	}, "网关流式转发捕获异常")
	if (p.terminalEventWritten || (inspection.TerminalReceived && p.downstreamCommit.SemanticCommitted)) && !p.interpretedProtocolFailure(inspection) {
		p.endResponse()
		p.logger.Info("gateway_stream_error_ignored_after_terminal", map[string]any{
			"elapsedMs": p.nowMs() - p.startedAt,
		}, "网关已收到终止事件，忽略终止后的流式异常")
		return p.finishStreamResult(finishArgs{
			completed: true, message: "已完成", usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
		}), nil
	}
	rawMessage := errorDiagnosticMessage(loopErr)
	transportFailure := StreamTransportFailureForError(loopErr, rawMessage)
	gatewayLocalFailure := IsGatewayLocalStreamFailure(loopErr, p.interpretedProtocolFailure(inspection), transportFailure)
	message := PublicStreamFailureMessage(loopErr, p.interpretedProtocolFailure(inspection), transportFailure)
	clientErrorCode := gatewayStreamFailureCodeForError(loopErr, message)
	errorCode := StreamClientFailureCode(clientErrorCode, inspection.OutputReceived, p.options.ClientRetryEnabled, p.totalResponseBytes)
	if firstByteErr := asFirstByteTimeout(loopErr); firstByteErr != nil && firstByteErr.Source == "configured_deadline" {
		p.body.Close()
	}
	failureBeforeDownstreamCommit := p.shouldFailBeforeDownstreamCommit()
	if failureBeforeDownstreamCommit &&
		(p.interpretedProtocolFailure(inspection) || isStreamPreCommitBufferExceeded(loopErr)) {
		TakeStreamPreCommitChunks(p.preCommitBuffer)
	}
	if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(
		p.totalResponseBytes,
		inspection.OutputReceived,
		p.interpretedProtocolFailure(inspection),
		!gatewayLocalFailure && (transportFailure != nil || p.interpretedProtocolFailure(inspection)),
	)); err != nil {
		return StreamPipeResult{}, err
	}
	if failureBeforeDownstreamCommit {
		p.logger.Warn("gateway_stream_failure_before_downstream_commit", map[string]any{
			"message":            message,
			"errorCode":          errorCode,
			"totalUpstreamBytes": p.totalUpstreamBytes,
			"totalResponseBytes": p.totalResponseBytes,
		}, "网关在下游提交前捕获流式失败，交由上层返回当前失败")
		result := p.finishStreamResult(finishArgs{
			message: message, errorCode: errorCode, usage: inspection.Usage,
			outputReceived:        inspection.OutputReceived,
			estimatedOutputTokens: inspection.EstimatedOutputTokens,
			imageOutputReceived:   inspection.ImageOutputReceived,
			gatewayLocalFailure:   gatewayLocalFailure,
			transportFailure:      transportFailure,
		})
		return result, nil
	}
	if err := p.signalCommittedStreamFailure(); err != nil {
		return StreamPipeResult{}, err
	}
	p.logger.Warn("gateway_stream_failure_after_downstream_commit_interrupted", map[string]any{
		"message":              message,
		"errorCode":            errorCode,
		"totalUpstreamBytes":   p.totalUpstreamBytes,
		"totalResponseBytes":   p.totalResponseBytes,
		"transportFailureKind": transportFailureKindOrNull(transportFailure),
	}, "网关已中断提交后的失败流")
	result := p.finishStreamResult(finishArgs{
		message: message, errorCode: errorCode, usage: inspection.Usage,
		outputReceived:        inspection.OutputReceived,
		estimatedOutputTokens: inspection.EstimatedOutputTokens,
		imageOutputReceived:   inspection.ImageOutputReceived,
		gatewayLocalFailure:   gatewayLocalFailure,
		transportFailure:      transportFailure,
	})
	return result, nil
}

// ---- finishTerminalSuccess ----

// finishTerminalSuccess 对齐 finishTerminalSuccess。
func (p *streamPipe) finishTerminalSuccess(inspection gatewayproto.StreamInspection, drainForKeepAlive bool, eofPendingFlush bool) (StreamPipeResult, error) {
	finalInspection := inspection
	closeIteratorAfterEnd := false
	p.omitBodyCaptureIfImageStream(finalInspection, eofPendingFlush)
	if drainForKeepAlive && !p.interpretedProtocolFailure(finalInspection) {
		finalInspection = p.publishInspection(drainIteratorAfterTerminalForInspection(p.body, p.inspector))
		p.updateStreamInspectionProgress(finalInspection)
		p.omitBodyCaptureIfImageStream(finalInspection, true)
	} else {
		closeIteratorAfterEnd = true
	}
	if p.interpretedProtocolFailure(finalInspection) {
		message := orDefault(finalInspection.ErrorMessage, "上游流式响应在成功终态后返回矛盾失败终态")
		errorCode := "upstream_protocol_failure"
		if err := p.input.HandleStreamFailure(message, errorCode, p.streamFailureContext(p.totalResponseBytes, finalInspection.OutputReceived, p.interpretedProtocolFailure(finalInspection), p.interpretedProtocolFailure(finalInspection))); err != nil {
			return StreamPipeResult{}, err
		}
		p.interruptResponse()
		if closeIteratorAfterEnd {
			p.body.Close()
		}
		p.logger.Warn("gateway_stream_failed_after_terminal", map[string]any{
			"elapsedMs":           p.nowMs() - p.startedAt,
			"chunkCount":          p.chunkIndex,
			"totalUpstreamBytes":  p.totalUpstreamBytes,
			"totalResponseBytes":  p.totalResponseBytes,
			"firstTokenMs":        int64PtrValue(p.firstTokenMs),
			"message":             message,
			"errorCode":           errorCode,
			"sseEventCount":       finalInspection.EventCount,
			"sseEventTypeCounts":  finalInspection.EventTypeCounts,
			"recentSseEventTypes": finalInspection.RecentEventTypes,
			"outputReceived":      finalInspection.OutputReceived,
			"outputEventCount":    finalInspection.OutputEventCount,
			"eofPendingFlush":     boolOrNull(eofPendingFlush),
		}, "网关在终止事件后解析到失败事件，按失败流式响应收尾")
		return p.finishStreamResult(finishArgs{
			message: message, errorCode: errorCode, usage: finalInspection.Usage,
			outputReceived:        finalInspection.OutputReceived,
			estimatedOutputTokens: finalInspection.EstimatedOutputTokens,
			imageOutputReceived:   finalInspection.ImageOutputReceived,
		}), nil
	}
	if err := p.ensureBeforeDownstreamCommit(); err != nil {
		return StreamPipeResult{}, err
	}
	p.endResponse()
	if closeIteratorAfterEnd {
		p.body.Close()
	}
	p.logger.Debug("gateway_stream_finished_success_after_terminal", map[string]any{
		"elapsedMs":                          p.nowMs() - p.startedAt,
		"chunkCount":                         p.chunkIndex,
		"totalUpstreamBytes":                 p.totalUpstreamBytes,
		"totalResponseBytes":                 p.totalResponseBytes,
		"firstTokenMs":                       int64PtrValue(p.firstTokenMs),
		"sseEventCount":                      finalInspection.EventCount,
		"sseEventTypeCounts":                 finalInspection.EventTypeCounts,
		"recentSseEventTypes":                finalInspection.RecentEventTypes,
		"outputReceived":                     finalInspection.OutputReceived,
		"outputEventCount":                   finalInspection.OutputEventCount,
		"upstreamDrainScheduledForKeepAlive": boolOrNil(drainForKeepAlive),
		"eofPendingFlush":                    boolOrNil(eofPendingFlush),
	}, "网关已收到协议终止事件并成功结束流式响应")
	return p.finishStreamResult(finishArgs{
		completed: true, message: "已完成", usage: finalInspection.Usage,
		outputReceived:        finalInspection.OutputReceived,
		estimatedOutputTokens: finalInspection.EstimatedOutputTokens,
		imageOutputReceived:   finalInspection.ImageOutputReceived,
	}), nil
}

// drainIteratorAfterTerminalForInspection 对齐 drainIteratorAfterTerminalForInspection。
func drainIteratorAfterTerminalForInspection(body UpstreamBody, inspector gatewayproto.StreamInspector) gatewayproto.StreamInspection {
	deadline := defaultNowMs() + StreamTerminalKeepAliveDrainMs
	for {
		remaining := deadline - defaultNowMs()
		if remaining <= 0 {
			body.Close()
			return inspector.Finish()
		}
		ch := body.Next()
		select {
		case result, ok := <-ch:
			if !ok {
				return inspector.Finish()
			}
			if result.Err != nil {
				body.Close()
				return inspector.Finish()
			}
			inspector.PushChunk(result.Data)
		case <-timeAfter(remaining):
			body.Close()
			return inspector.Finish()
		}
	}
}
