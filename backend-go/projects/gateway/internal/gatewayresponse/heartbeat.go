package gatewayresponse

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// SSE 等待心跳，对齐 sse-wait-heartbeat.ts。create/start 分离：构造不写出
//（Node createGatewaySseWaitHeartbeat 返回 {start, stop}，由等待观察者在
// 预算进入等待时 start，暂停时 stop；D-120 装配面）。

// GatewaySseWaitHeartbeatIntervalMs 对齐 gatewaySseWaitHeartbeatIntervalMs。
const GatewaySseWaitHeartbeatIntervalMs = 15_000

var (
	// gatewaySseWaitHeartbeatChunk 对齐 ': juhe-ai waiting for upstream capacity\n\n'。
	gatewaySseWaitHeartbeatChunk = []byte(": juhe-ai waiting for upstream capacity\n\n")
	// codexCompactionSseWaitHeartbeatChunk 对齐 codex 保活块。
	codexCompactionSseWaitHeartbeatChunk = []byte("data: {\"type\":\"juhe_ai.keepalive\"}\n\n")
)

// GatewaySseWaitHeartbeat 对齐 GatewaySseWaitHeartbeat：Start/Stop 可重复
// 配对（Node 等待预算的 pause/resume 边沿），Stop 幂等。
type GatewaySseWaitHeartbeat struct {
	mu   sync.Mutex
	run  *heartbeatRun
	deps HeartbeatDeps
}

// heartbeatRun 承载单轮心跳循环的取消柄。
type heartbeatRun struct {
	cancel context.CancelFunc
}

// Start 对齐 start()：已在运行时保持，否则进入新一轮心跳循环。
func (h *GatewaySseWaitHeartbeat) Start() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.run != nil {
		h.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &heartbeatRun{cancel: cancel}
	h.run = run
	deps := h.deps
	h.mu.Unlock()
	go func() {
		defer func() {
			h.mu.Lock()
			if h.run == run {
				h.run = nil
			}
			h.mu.Unlock()
		}()
		runHeartbeatLoop(deps, heartbeatChunkOf(deps), ctx)
	}()
}

// Stop 对齐 stopAndDetach：取消当前心跳循环（幂等）。
func (h *GatewaySseWaitHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	run := h.run
	h.run = nil
	h.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// HeartbeatDeps 对齐 createGatewaySseWaitHeartbeat 的入参。
type HeartbeatDeps struct {
	Res                gatewaypreauth.GatewayResponseWriter
	DownstreamProtocol string
	DownstreamCommit   *DownstreamCommitState
	// Signal 携带请求中断面：请求结束/中断即停（对齐 signal abort listener）。
	Signal     context.Context
	IntervalMs int64
	// EmitCodexCompactionKeepalive 对齐同名入参。
	EmitCodexCompactionKeepalive bool
	// After 注入定时器（测试）；nil 时用 time.After。
	After func(d time.Duration) <-chan time.Time
}

// CreateGatewaySseWaitHeartbeat 对齐 createGatewaySseWaitHeartbeat：下游协议
// 不使用 SSE 时返回 nil；构造本身不写出，等待开始由 Start 触发。
func CreateGatewaySseWaitHeartbeat(deps HeartbeatDeps) *GatewaySseWaitHeartbeat {
	if !GatewayDownstreamProtocolUsesSSE(deps.DownstreamProtocol) {
		return nil
	}
	return &GatewaySseWaitHeartbeat{deps: deps}
}

// CreateGatewaySseWaitHeartbeatObserver 对齐
// createGatewaySseWaitHeartbeatObserver：非 SSE 协议返回 nil（观察者缺省），
// 等待预算在 Begin/Pause 边沿回调 Start/Stop。
func CreateGatewaySseWaitHeartbeatObserver(deps HeartbeatDeps) *gatewaypreauth.ServerRetryBudgetWaitObserver {
	heartbeat := CreateGatewaySseWaitHeartbeat(deps)
	if heartbeat == nil {
		return nil
	}
	return &gatewaypreauth.ServerRetryBudgetWaitObserver{
		OnWaitStarted: heartbeat.Start,
		OnWaitPaused:  heartbeat.Stop,
	}
}

// ---- 内部循环 ----

func heartbeatChunkOf(deps HeartbeatDeps) []byte {
	if deps.EmitCodexCompactionKeepalive && deps.DownstreamProtocol == "responses_sse" {
		return codexCompactionSseWaitHeartbeatChunk
	}
	return gatewaySseWaitHeartbeatChunk
}

// runHeartbeatLoop 对齐 start() 内的首次 writeHeartbeat + setInterval：
// 首个心跳立即写出，之后按 interval 重复，直到取消/终止条件成立。
func runHeartbeatLoop(deps HeartbeatDeps, chunk []byte, ctx context.Context) {
	intervalMs := deps.IntervalMs
	if intervalMs < 1000 {
		intervalMs = 1000
	}
	after := deps.After
	if after == nil {
		after = time.After
	}
	if heartbeatAborted(deps, ctx) {
		return
	}
	if !writeHeartbeatChunk(deps, chunk) {
		return
	}
	for {
		timer := after(time.Duration(intervalMs) * time.Millisecond)
		select {
		case <-ctx.Done():
			return
		case <-deps.signalDone():
			return
		case <-timer:
			if heartbeatAborted(deps, ctx) || !writeHeartbeatChunk(deps, chunk) {
				return
			}
		}
	}
}

// heartbeatAborted 对齐 writeHeartbeat/start 的终止检查：请求中断、下游终止
// 或语义已提交后不再写出。
func heartbeatAborted(deps HeartbeatDeps, ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	if deps.Signal != nil && deps.Signal.Err() != nil {
		return true
	}
	if deps.DownstreamCommit != nil && deps.DownstreamCommit.SemanticCommitted {
		return true
	}
	return false
}

func (d HeartbeatDeps) signalDone() <-chan struct{} {
	if d.Signal == nil {
		return nil
	}
	return d.Signal.Done()
}

func writeHeartbeatChunk(deps HeartbeatDeps, chunk []byte) bool {
	if deps.DownstreamCommit != nil && deps.DownstreamCommit.SemanticCommitted {
		return false
	}
	if tracking, ok := deps.Res.(*gatewaypreauth.TrackingWriter); ok {
		if tracking.WritableEnded() {
			return false
		}
	}
	if !deps.Res.HeadersSent() {
		header := deps.Res.Header()
		header.Set("content-type", "text/event-stream; charset=utf-8")
		header.Set("cache-control", "no-cache, no-transform")
		header.Set("x-accel-buffering", "no")
		deps.Res.WriteHeader(200)
	}
	if _, err := deps.Res.Write(chunk); err != nil {
		return false
	}
	FlushGateway(deps.Res)
	if deps.DownstreamCommit != nil {
		deps.DownstreamCommit.MarkTransportCommitted(int64(len(chunk)))
	}
	return true
}

// GatewayDownstreamProtocolUsesSSE 对齐 gatewayDownstreamProtocolUsesSse。
func GatewayDownstreamProtocolUsesSSE(protocol string) bool {
	switch protocol {
	case "responses_sse", "chat_completions_sse", "messages_sse",
		"gemini_stream_generate_content_sse", "unknown_stream":
		return true
	}
	return false
}
