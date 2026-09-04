package gatewayresponse

import (
	"context"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// SSE 等待心跳，对齐 sse-wait-heartbeat.ts。

// GatewaySseWaitHeartbeatIntervalMs 对齐 gatewaySseWaitHeartbeatIntervalMs。
const GatewaySseWaitHeartbeatIntervalMs = 15_000

var (
	// gatewaySseWaitHeartbeatChunk 对齐 ': juhe-ai waiting for upstream capacity\n\n'。
	gatewaySseWaitHeartbeatChunk = []byte(": juhe-ai waiting for upstream capacity\n\n")
	// codexCompactionSseWaitHeartbeatChunk 对齐 codex 保活块。
	codexCompactionSseWaitHeartbeatChunk = []byte("data: {\"type\":\"juhe_ai.keepalive\"}\n\n")
)

// GatewaySseWaitHeartbeat 对齐 GatewaySseWaitHeartbeat。
type GatewaySseWaitHeartbeat struct {
	stopOnce sync.Once
	stop     func()
}

// Stop 对齐 stop。
func (h *GatewaySseWaitHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(h.stop)
}

// HeartbeatDeps 对齐 createGatewaySseWaitHeartbeat 的入参。
type HeartbeatDeps struct {
	Res                gatewaypreauth.GatewayResponseWriter
	DownstreamProtocol string
	DownstreamCommit   *DownstreamCommitState
	Cancel             context.CancelFunc // signal abort 的等价物；可为 nil
	IntervalMs         int64
	// EmitCodexCompactionKeepalive 对齐同名入参。
	EmitCodexCompactionKeepalive bool
	// After 注入定时器（测试）；nil 时用 time.After。
	After func(d time.Duration) <-chan time.Time
}

// CreateGatewaySseWaitHeartbeat 对齐 createGatewaySseWaitHeartbeat：下游协议
// 不使用 SSE 时返回 nil。
func CreateGatewaySseWaitHeartbeat(deps HeartbeatDeps) *GatewaySseWaitHeartbeat {
	if !GatewayDownstreamProtocolUsesSSE(deps.DownstreamProtocol) {
		return nil
	}
	heartbeatChunk := gatewaySseWaitHeartbeatChunk
	if deps.EmitCodexCompactionKeepalive && deps.DownstreamProtocol == "responses_sse" {
		heartbeatChunk = codexCompactionSseWaitHeartbeatChunk
	}
	intervalMs := deps.IntervalMs
	if intervalMs < 1000 {
		intervalMs = 1000
	}
	after := deps.After
	if after == nil {
		after = time.After
	}

	ctx, cancel := context.WithCancel(context.Background())
	heartbeat := &GatewaySseWaitHeartbeat{}
	heartbeat.stop = func() {
		cancel()
	}

	go func() {
		// 首个心跳立即写出（对齐 start() 内的首次 writeHeartbeat）。
		if !writeHeartbeatChunk(deps, heartbeatChunk) {
			cancel()
			return
		}
		ticker := after(time.Duration(intervalMs) * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker:
				if !writeHeartbeatChunk(deps, heartbeatChunk) {
					cancel()
					return
				}
				ticker = after(time.Duration(intervalMs) * time.Millisecond)
			}
		}
	}()
	return heartbeat
}

func writeHeartbeatChunk(deps HeartbeatDeps, chunk []byte) bool {
	if deps.DownstreamCommit.SemanticCommitted {
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
	deps.DownstreamCommit.MarkTransportCommitted(int64(len(chunk)))
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
