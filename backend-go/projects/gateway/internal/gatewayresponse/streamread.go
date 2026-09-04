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
}

// pendingRead 对齐 ObservedFirstBytePendingRead：单写入 goroutine 桥接上游
// future，先记录 settle 时刻再发布结果；即使被放弃也不阻塞、不丢结果。
type pendingRead struct {
	ch                chan ChunkResult
	settled           atomic.Bool
	settledAt         atomic.Int64
	closeOnce         sync.Once
	stop              chan struct{}
}

func newPendingRead(next <-chan ChunkResult, nowMs func() int64) *pendingRead {
	read := &pendingRead{ch: make(chan ChunkResult, 1), stop: make(chan struct{})}
	go func() {
		defer read.closeOnce.Do(func() { close(read.ch) })
		result, ok := <-next
		if !ok {
			read.settled.Store(true)
			read.settledAt.Store(nowMs())
			read.ch <- ChunkResult{Err: io.ErrUnexpectedEOF}
			return
		}
		read.settled.Store(true)
		read.settledAt.Store(nowMs())
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
	pending := newPendingRead(p.body.Next(), p.nowMs)
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
		race := raceStreamReadWithDeadlines(pending, p.signalChannel(), firstByteRemainingMs, planTimeoutMs(readPlan), responsePrecommitRemainingMs)
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
// plan 超时 / precommit 超时 / abort 竞速。软超时与 precommit 重合时保留
// precommit 归因。
func raceStreamReadWithDeadlines(pending *pendingRead, signal <-chan struct{}, softTimeoutMs *int64, planTimeout *int64, responsePrecommitTimeoutMs *int64) raceOutcome {
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

	for {
		cases := make([]reflect.SelectCase, 0, 2+len(timers))
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(pending.ch)})
		if signal != nil {
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(signal)})
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
		if signal != nil && chosen == 1 {
			return raceOutcome{kind: raceAbort}
		}
		timerIndex := chosen - 1
		if signal != nil {
			timerIndex--
		}
		timer := timers[timerIndex]
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
