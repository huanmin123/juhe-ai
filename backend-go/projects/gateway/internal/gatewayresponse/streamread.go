package gatewayresponse

import (
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 读取循环：对齐 readNextStreamChunk / raceStreamReadWithDeadlines /
// decideFirstByteDeadlineAfterPendingRead。

type streamFirstByteDeadlineReadDecision struct {
	action        FirstByteDeadlineAction
	hasAction     bool
	decisionError error
}

type streamChunkReadResult struct {
	chunk                     ChunkResult
	firstByteDeadlineObserved bool
	decision                  *streamFirstByteDeadlineReadDecision
	// clientGoneDetected 表示原始客户端请求已断开：调用方应标记 clientGone
	// 并从断连时刻起算 drain 预算（不中止上游读取）。
	clientGoneDetected bool
}

// pendingRead 对齐 ObservedFirstBytePendingRead：单写入 goroutine 桥接上游
// future，先记录 settle 时刻再发布结果；即使被放弃也不阻塞、不丢结果。
type pendingRead struct {
	ch        chan ChunkResult
	settled   atomic.Bool
	settledAt atomic.Int64
	closeOnce sync.Once
	stop      chan struct{}
}

func newPendingRead(next <-chan ChunkResult, nowMs func() int64) *pendingRead {
	read := &pendingRead{ch: make(chan ChunkResult, 1), stop: make(chan struct{})}
	go func() {
		defer read.closeOnce.Do(func() { close(read.ch) })
		result, ok := <-next
		if !ok {
			// 先记 settledAt 再置 settled：读取方以 settled 为可见门槛，
			// 顺序相反会让读取方观察到 settled=true 而 settledAt 尚为 0
			//（TestW9CPendingReadLifecycle 偶发失败根因）。
			read.settledAt.Store(nowMs())
			read.settled.Store(true)
			read.ch <- ChunkResult{Err: io.ErrUnexpectedEOF}
			return
		}
		read.settledAt.Store(nowMs())
		read.settled.Store(true)
		read.ch <- result
	}()
	return read
}

func (r *pendingRead) isSettled() bool { return r.settled.Load() }

func (r *pendingRead) settledAtMs() (int64, bool) {
	if !r.settled.Load() {
		return 0, false
	}
	return r.settledAt.Load(), true
}

// receive 阻塞取回结果（写入方保证恰好一个值后关闭）。
func (r *pendingRead) receive() (ChunkResult, bool) {
	result, ok := <-r.ch
	return result, ok
}

// readStatus 对齐 readNextStreamChunk 的 status 入参（由 pipe 状态推导）。
type readStatus struct {
	waitingForFirstChunk      bool
	lastUpstreamActivityAt    int64
	lastSseEventActivityAt    *int64
	upstreamChunkReceived     bool
	semanticResultReceived    bool
	pendingProtocolEvent      bool
	parserSkipped             bool
	waitingForFirstOutput     bool
	firstByteDeadlineObserved bool
}

func (p *streamPipe) readNextStreamChunk() (streamChunkReadResult, error) {
	pending := p.pendingClientGoneRead
	p.pendingClientGoneRead = nil
	if pending == nil {
		pending = newPendingRead(p.body.Next(), p.nowMs)
	}
	firstByteDeadlineObserved := p.firstByteDeadlineObserved

	for {
		now := p.nowMs()
		firstByteDeadlineMs := p.options.FirstByteDeadlineMs
		var responsePrecommitRemainingMs *int64
		if !p.semanticResultReceived && p.options.ResponsePrecommitDeadlineAtMs != nil {
			remaining := *p.options.ResponsePrecommitDeadlineAtMs - now
			responsePrecommitRemainingMs = &remaining
		}
		if responsePrecommitRemainingMs != nil && *responsePrecommitRemainingMs <= 0 {
			return streamChunkReadResult{}, &ResponsePrecommitDeadlineError{DeadlineAtMs: *p.options.ResponsePrecommitDeadlineAtMs}
		}
		var firstByteRemainingMs *int64
		if p.waitingForFirstOutputStatus() &&
			!p.streamParserSkipped &&
			!firstByteDeadlineObserved &&
			firstByteDeadlineMs != nil {
			remaining := p.startedAt + *firstByteDeadlineMs - now
			firstByteRemainingMs = &remaining
		}
		if firstByteRemainingMs != nil && *firstByteRemainingMs <= 0 {
			firstByteDeadlineObserved = true
			deadlineMs := *firstByteDeadlineMs
			decision := p.decideFirstByteDeadlineAfterPendingRead(pending, FirstByteDeadlineInput{
				ElapsedMs: p.nowMs() - p.startedAt,
				TimeoutMs: deadlineMs,
				Transport: "stream",
			})
			if decision.precommitDeadline {
				return streamChunkReadResult{}, decision.deadlineError
			}
			if decision.read {
				return streamChunkReadResult{
					chunk:                     decision.chunk,
					firstByteDeadlineObserved: firstByteDeadlineObserved,
					decision: &streamFirstByteDeadlineReadDecision{
						action:        decision.action,
						hasAction:     decision.hasAction,
						decisionError: decision.decisionError,
					},
				}, nil
			}
			if decision.action == FirstByteDeadlineAbort {
				return streamChunkReadResult{}, &FirstByteTimeoutError{
					Message:   "上游流式响应 " + itoa(ceilDiv(deadlineMs, 1000)) + "s 后仍未返回首个有效输出",
					TimeoutMs: deadlineMs,
					Source:    "configured_deadline",
				}
			}
			continue
		}

		readPlan := BuildGatewayStreamReadPlan(p.profile, p.startedAt, p.readPlanStatus(), now)
		if readPlan != nil && readPlan.TimeoutMs <= 0 {
			return streamChunkReadResult{}, p.streamReadPlanTimeoutError(readPlan)
		}
		// 客户端断开后的 drain 预算参与读等待竞速：单次计时器在
		// markClientGone 时创建，跨 chunk 复用；上游长时间无新数据时预算
		// 到点也必须收口。
		var clientGoneTimerCh <-chan time.Time
		if p.clientGone {
			clientGoneTimerCh = p.clientGoneTimerCh
		}
		race := raceStreamReadWithDeadlinesClientGone(pending, p.signalChannel(), firstByteRemainingMs, planTimeoutMs(readPlan), responsePrecommitRemainingMs, p.replayWindowTimerCh, clientGoneTimerCh, p.clientSignalChannel())
		switch race.kind {
		case raceRead:
			if p.options.ResponsePrecommitDeadlineAtMs != nil && !p.semanticResultReceived {
				settledAt, ok := pending.settledAtMs()
				effective := settledAt
				if !ok {
					effective = p.nowMs()
				}
				if effective > *p.options.ResponsePrecommitDeadlineAtMs {
					return streamChunkReadResult{}, &ResponsePrecommitDeadlineError{DeadlineAtMs: *p.options.ResponsePrecommitDeadlineAtMs}
				}
			}
			return streamChunkReadResult{chunk: race.chunk, firstByteDeadlineObserved: firstByteDeadlineObserved}, nil
		case raceClientGoneSignal:
			// 保留 pending：客户端断开只改变“是否写下游”，不改变上游读取
			// 归属；下一轮继续消费同一 future。
			p.pendingClientGoneRead = pending
			return streamChunkReadResult{clientGoneDetected: true}, nil
		case raceClientGoneBudget:
			return streamChunkReadResult{}, errClientGoneDrainBudget
		case raceWindowDeadline:
			// 受限重放窗口真实截止到点（设计 §3.1 条件③）：强制关窗并 flush
			// 窗口缓冲。pending 读取保持归属——已 settle 则随本结果带回（窗口
			// 已关，按直通 chunk 走既有写路径）；未 settle 则继续等待（下一轮
			// 窗口已关，不再参与竞速）。
			if err := p.flushReplayWindowAtDeadline(); err != nil {
				return streamChunkReadResult{}, err
			}
			if pending.isSettled() {
				chunk, ok := pending.receive()
				if !ok {
					return streamChunkReadResult{}, errors.New("上游流式响应已中断")
				}
				return streamChunkReadResult{chunk: chunk, firstByteDeadlineObserved: firstByteDeadlineObserved}, nil
			}
			continue
		case raceAbort:
			return streamChunkReadResult{}, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
		case racePlanTimeout:
			if readPlan == nil {
				return streamChunkReadResult{}, errors.New("网关流读取计时器状态无效")
			}
			return streamChunkReadResult{}, p.streamReadPlanTimeoutError(readPlan)
		case raceResponsePrecommitTimeout:
			return streamChunkReadResult{}, &ResponsePrecommitDeadlineError{DeadlineAtMs: derefInt64(p.options.ResponsePrecommitDeadlineAtMs)}
		}

		firstByteDeadlineObserved = true
		decision := p.decideFirstByteDeadlineAfterPendingRead(pending, FirstByteDeadlineInput{
			ElapsedMs: p.nowMs() - p.startedAt,
			TimeoutMs: derefInt64(p.options.FirstByteDeadlineMs),
			Transport: "stream",
		})
		if decision.precommitDeadline {
			return streamChunkReadResult{}, decision.deadlineError
		}
		if decision.read {
			return streamChunkReadResult{
				chunk:                     decision.chunk,
				firstByteDeadlineObserved: firstByteDeadlineObserved,
				decision: &streamFirstByteDeadlineReadDecision{
					action:        decision.action,
					hasAction:     decision.hasAction,
					decisionError: decision.decisionError,
				},
			}, nil
		}
		if decision.action == FirstByteDeadlineAbort {
			deadlineMs := derefInt64(p.options.FirstByteDeadlineMs)
			return streamChunkReadResult{}, &FirstByteTimeoutError{
				Message:   "上游流式响应 " + itoa(ceilDiv(deadlineMs, 1000)) + "s 后仍未返回首个有效输出",
				TimeoutMs: deadlineMs,
				Source:    "configured_deadline",
			}
		}
	}
}

func (p *streamPipe) waitingForFirstOutputStatus() bool {
	return p.options.FirstByteDeadlineMs != nil &&
		p.firstTokenMs == nil &&
		p.totalResponseBytes == 0 &&
		!p.downstreamCommit.SemanticCommitted
}

func (p *streamPipe) readPlanStatus() StreamReadPlanStatus {
	return StreamReadPlanStatus{
		WaitingForFirstChunk:   p.waitingForFirstChunk,
		LastUpstreamActivityAt: p.lastUpstreamActivityAt,
		LastSseEventActivityAt: derefInt64Zero(p.lastSseEventActivityAt),
		UpstreamChunkReceived:  p.upstreamChunkReceived,
		SemanticResultReceived: p.semanticResultReceived,
		PendingProtocolEvent:   p.pendingProtocolEvent,
		ParserSkipped:          p.streamParserSkipped,
	}
}

func (p *streamPipe) streamReadPlanTimeoutError(plan *StreamReadPlan) error {
	if plan.TimeoutKind == "stream_lifetime" {
		return &StreamReadPlanTimeoutError{Message: plan.TimeoutMessage, TimeoutKind: "stream_lifetime"}
	}
	return &StreamReadPlanTimeoutError{Message: plan.TimeoutMessage, TimeoutKind: plan.TimeoutKind}
}

type deadlineDecision struct {
	read              bool
	chunk             ChunkResult
	action            FirstByteDeadlineAction
	hasAction         bool
	decisionError     error
	precommitDeadline bool
	deadlineError     error
}

// decideFirstByteDeadlineAfterPendingRead 对齐同名函数：Go 侧 handler 同步完成，
// 决策立即可用；唯一竞速对象是 response precommit 墙钟。
func (p *streamPipe) decideFirstByteDeadlineAfterPendingRead(pending *pendingRead, input FirstByteDeadlineInput) deadlineDecision {
	var action FirstByteDeadlineAction
	var handlerErr error
	hasHandler := p.options.OnFirstByteDeadline != nil
	if hasHandler {
		action, handlerErr = p.options.OnFirstByteDeadline(input)
	} else {
		action = FirstByteDeadlineAbort
	}
	if handlerErr != nil && !pending.isSettled() {
		return deadlineDecision{decisionError: handlerErr}
	}

	responsePrecommitDeadlineAtMs := p.options.ResponsePrecommitDeadlineAtMs
	if responsePrecommitDeadlineAtMs != nil {
		now := p.nowMs()
		if now < *responsePrecommitDeadlineAtMs {
			// 决策即时可用；竞速对象只剩 pending 读取与未到点的墙钟。
			timer := timeAfter(*responsePrecommitDeadlineAtMs - now)
			select {
			case chunk, ok := <-pending.ch:
				if ok {
					if settledAt, hasSettled := pending.settledAtMs(); hasSettled && settledAt <= *responsePrecommitDeadlineAtMs {
						return deadlineDecision{read: true, chunk: chunk, decisionError: handlerErr}
					}
				}
				// 读取在墙钟后 settle：墙钟胜出。
				if p.options.OnFirstByteDeadlineSuperseded != nil {
					p.options.OnFirstByteDeadlineSuperseded()
				}
				return deadlineDecision{precommitDeadline: true, deadlineError: &ResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}}
			case <-timer:
				// 墙钟胜出：通知后按 deadline 处理。
				if p.options.OnFirstByteDeadlineSuperseded != nil {
					p.options.OnFirstByteDeadlineSuperseded()
				}
				deadlineError := &ResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}
				if pending.isSettled() {
					if settledAt, hasSettled := pending.settledAtMs(); hasSettled && settledAt <= *responsePrecommitDeadlineAtMs {
						chunk, readOK := pending.receive()
						if !readOK {
							return deadlineDecision{precommitDeadline: true, deadlineError: deadlineError}
						}
						return deadlineDecision{read: true, chunk: chunk, decisionError: deadlineError}
					}
				}
				return deadlineDecision{precommitDeadline: true, deadlineError: deadlineError}
			}
		}
		// 墙钟已到（fired）：pending 已 settle 且 settle 时刻在 deadline 前的
		// 读取仍以 read + decisionError 返回。
		supersededNotified := false
		if pending.isSettled() {
			if settledAt, ok := pending.settledAtMs(); ok && settledAt <= *responsePrecommitDeadlineAtMs {
				supersededNotified = true
				if p.options.OnFirstByteDeadlineSuperseded != nil {
					p.options.OnFirstByteDeadlineSuperseded()
				}
				chunk, readOK := pending.receive()
				if !readOK {
					return deadlineDecision{precommitDeadline: true, deadlineError: &ResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}}
				}
				return deadlineDecision{read: true, chunk: chunk, decisionError: &ResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}}
			}
		}
		_ = supersededNotified
		if p.options.OnFirstByteDeadlineSuperseded != nil {
			p.options.OnFirstByteDeadlineSuperseded()
		}
		return deadlineDecision{precommitDeadline: true, deadlineError: &ResponsePrecommitDeadlineError{DeadlineAtMs: *responsePrecommitDeadlineAtMs}}
	}

	return p.resolveDeadlineOutcome(pending, action, handlerErr, true)
}

func (p *streamPipe) resolveDeadlineOutcome(pending *pendingRead, action FirstByteDeadlineAction, decisionError error, hasAction bool) deadlineDecision {
	if decisionError != nil {
		if !pending.isSettled() {
			return deadlineDecision{decisionError: decisionError}
		}
		chunk, ok := pending.receive()
		if !ok {
			return deadlineDecision{decisionError: errors.New("上游流式响应已中断")}
		}
		return deadlineDecision{read: true, chunk: chunk, decisionError: decisionError}
	}
	if !pending.isSettled() {
		return deadlineDecision{action: action, hasAction: hasAction}
	}
	chunk, ok := pending.receive()
	if !ok {
		return deadlineDecision{decisionError: errors.New("上游流式响应已中断")}
	}
	return deadlineDecision{read: true, chunk: chunk, action: action, hasAction: hasAction}
}

type raceKind int

const (
	raceRead raceKind = iota
	raceSoftTimeout
	racePlanTimeout
	raceResponsePrecommitTimeout
	raceAbort
	// raceWindowDeadline 是受限重放窗口的真实截止到点（设计 §3.1 条件③）：
	// 读取静默期间 T 到点强制关窗 flush，不依赖后续 chunk 驱动。
	raceWindowDeadline
	// raceClientGoneBudget 是客户端断开后 drain 预算到点（2026-11-12 计费
	// 漏记修复）：上游长时间无新数据时也必须按预算收口。
	raceClientGoneBudget
	// raceClientGoneSignal 是原始客户端请求断开（不等首次写失败）：标记
	// clientGone 并继续读上游，预算从断连时刻起算。
	raceClientGoneSignal
)

type raceOutcome struct {
	kind  raceKind
	chunk ChunkResult
}

type raceTimer struct {
	kind raceKind
	ch   <-chan time.Time
}

func planTimeoutMs(plan *StreamReadPlan) *int64 {
	if plan == nil {
		return nil
	}
	return &plan.TimeoutMs
}

// raceStreamReadWithDeadlines 对齐 raceStreamReadWithDeadlines：read / 软超时 /
// plan 超时 / precommit 超时 / abort / 受限重放窗口截止竞速。软超时与 precommit
// 重合时保留 precommit 归因；replayWindowCh 为 nil（窗口未开启/已关闭）时不
// 参与竞速。
func raceStreamReadWithDeadlines(pending *pendingRead, signal <-chan struct{}, softTimeoutMs *int64, planTimeout *int64, responsePrecommitTimeoutMs *int64, replayWindowCh <-chan time.Time) raceOutcome {
	return raceStreamReadWithDeadlinesClientGone(pending, signal, softTimeoutMs, planTimeout, responsePrecommitTimeoutMs, replayWindowCh, nil, nil)
}

// raceStreamReadWithDeadlinesClientGone 在既有竞速面上增加客户端断开信号与
// drain 预算：clientDone 选中表示客户端原始请求已断开（标记 clientGone 并
// 继续读取），clientGoneTimerCh 为 nil（未断连/预算关闭）时不参与竞速。
func raceStreamReadWithDeadlinesClientGone(pending *pendingRead, signal <-chan struct{}, softTimeoutMs *int64, planTimeout *int64, responsePrecommitTimeoutMs *int64, replayWindowCh <-chan time.Time, clientGoneTimerCh <-chan time.Time, clientDone <-chan struct{}) raceOutcome {
	var timers []raceTimer
	if softTimeoutMs != nil {
		timers = append(timers, raceTimer{raceSoftTimeout, timeAfter(*softTimeoutMs)})
	}
	if responsePrecommitTimeoutMs != nil {
		timers = append(timers, raceTimer{raceResponsePrecommitTimeout, timeAfter(*responsePrecommitTimeoutMs)})
	}
	if planTimeout != nil {
		timers = append(timers, raceTimer{racePlanTimeout, timeAfter(*planTimeout)})
	}
	if replayWindowCh != nil {
		timers = append(timers, raceTimer{raceWindowDeadline, replayWindowCh})
	}
	if clientGoneTimerCh != nil {
		timers = append(timers, raceTimer{raceClientGoneBudget, clientGoneTimerCh})
	}

	for {
		cases := make([]reflect.SelectCase, 0, 3+len(timers))
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(pending.ch)})
		if signal != nil {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(signal)})
		}
		if clientDone != nil {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(clientDone)})
		}
		for _, timer := range timers {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timer.ch)})
		}
		chosen, value, ok := reflect.Select(cases)
		if chosen == 0 {
			// pending future 桥：ok=false 表示上游 future 源被关闭（连接中断）。
			if !ok {
				return raceOutcome{kind: raceRead, chunk: ChunkResult{Err: io.ErrUnexpectedEOF}}
			}
			result, _ := value.Interface().(ChunkResult)
			return raceOutcome{kind: raceRead, chunk: result}
		}
		firstTimerIndex := 1
		if signal != nil {
			if chosen == firstTimerIndex {
				return raceOutcome{kind: raceAbort}
			}
			firstTimerIndex++
		}
		if clientDone != nil {
			if chosen == firstTimerIndex {
				return raceOutcome{kind: raceClientGoneSignal}
			}
			firstTimerIndex++
		}
		timer := timers[chosen-firstTimerIndex]
		if timer.kind == raceSoftTimeout &&
			responsePrecommitTimeoutMs != nil &&
			(softTimeoutMs == nil || *responsePrecommitTimeoutMs <= *softTimeoutMs) {
			// The request precommit deadline is a hard wall. Preserve that
			// attribution when it coincides with a configured speed-first
			// deadline, regardless of timer registration order.
			return raceOutcome{kind: raceResponsePrecommitTimeout}
		}
		return raceOutcome{kind: timer.kind}
	}
}

func (p *streamPipe) signalChannel() <-chan struct{} {
	if p.input.Signal == nil {
		return nil
	}
	return p.input.Signal.Done()
}

// clientSignalChannel 是原始客户端请求的 Done 通道：clientGone 后不再参与
// 竞速（避免同一断连事件重复触发）。
func (p *streamPipe) clientSignalChannel() <-chan struct{} {
	if p.clientGone || p.options.ClientSignal == nil {
		return nil
	}
	return p.options.ClientSignal.Done()
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefInt64Zero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func ceilDiv(value, divisor int64) int64 {
	if divisor <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}

// settleStreamFirstByteDeadlineReadDecision 对齐
// settleStreamFirstByteDeadlineReadDecision。
func (p *streamPipe) settleStreamFirstByteDeadlineReadDecision(semanticResultInRead bool) error {
	decision := p.pendingReadDecision
	if decision == nil {
		return nil
	}
	p.pendingReadDecision = nil
	if semanticResultInRead {
		if p.options.OnFirstByteDeadlineSuperseded != nil {
			p.options.OnFirstByteDeadlineSuperseded()
		}
		return nil
	}
	if decision.decisionError != nil {
		return decision.decisionError
	}
	if decision.hasAction && decision.action == FirstByteDeadlineAbort {
		deadlineMs := derefInt64(p.options.FirstByteDeadlineMs)
		return &FirstByteTimeoutError{
			Message:   "上游流式响应 " + itoa(ceilDiv(deadlineMs, 1000)) + "s 后仍未返回首个有效输出",
			TimeoutMs: deadlineMs,
			Source:    "configured_deadline",
		}
	}
	return nil
}

// DefaultOpenAIStreamDriver 返回 openai_v1 驱动视图（G02 装配）。
func DefaultOpenAIStreamDriver() StreamDriver { return openAIStreamDriverAdapter{} }

// openAIStreamDriverAdapter 把 gatewayopenai 驱动适配为流管道视图。
type openAIStreamDriverAdapter struct{}

func (openAIStreamDriverAdapter) ClientErrorProtocol() string { return "openai" }

func (openAIStreamDriverAdapter) NewStreamInspector() gatewayproto.StreamInspector {
	return newOpenAIInspector()
}

func (openAIStreamDriverAdapter) ResponseInspectionEndpointFamily(family gatewayproto.ResponseEndpointFamily) gatewayproto.ResponseEndpointFamily {
	// 对齐 gatewayopenai.openAIEndpointFamilyOrUnknown。
	switch family {
	case gatewayproto.EndpointFamilyChatCompletions, gatewayproto.EndpointFamilyResponses:
		return family
	default:
		return gatewayproto.EndpointFamilyUnknown
	}
}

func (openAIStreamDriverAdapter) SSEResponseInspectionFailureEvent() string { return "response.failed" }

func (openAIStreamDriverAdapter) DrainForKeepAliveAfterTerminal() bool { return true }

// newOpenAIInspector 构造 G02 的流 inspector（满足 gatewayproto.StreamInspector）。
func newOpenAIInspector() *gatewayopenai.StreamInspector {
	return gatewayopenai.NewStreamInspector()
}
