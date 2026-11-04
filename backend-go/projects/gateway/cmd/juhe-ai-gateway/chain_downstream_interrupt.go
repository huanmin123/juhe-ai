package main

// 下游网络级中断装配（chain 面生产构造点：chain_v1.go handleUpstreamResponse
// 的 StreamDownstream{...} 字面量）。
//
// 2026-10-23 契约收口（internal/gatewayresponse 内核同步改造）：下游提交后的
// 流式失败对所有客户端一律中断连接——本 helper 把「中断」接成真·网络级断开：
// Hijack 底层 TCP 连接后直接 Close（FIN、无 chunked 终止帧），客户端 HTTP 栈
// 以 unexpected EOF 等网络级读错误收场，走它自身的网络错误自动重试（重试请求
// 重新走完整调度链，间接完成切号/会话黏连）。客户端不再收到「帧完整但缺协议
// 终止事件」的优雅收尾形态。
//
// 断开前的 End()+SetDestroyed() 使链面既有守卫立即生效：
//   - settleResponseStreamServerRetry 的 writableEndedOf 门（chain_v1_loop.go）
//     阻止向死连接换号重试；
//   - writePreCommitStreamFailureToClient（gatewayresponse/finalize.go，守卫
//     SemanticCommitted || WritableEnded || DestroyedNow）不向死连接补写；
//   - SendGatewayErrorResponse（gatewaypreauth/responses.go，守卫
//     WritableEnded || destroyed）不向死连接补写。
//
// Hijack 失败（底层不支持 http.Hijacker 或已被劫持）时降级为优雅收尾：仅记录
// 告警并保持已标记状态，不 panic、不重试——销毁语义（守卫生效）不受影响。

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// newDownstreamInterrupt 为流式响应构造下游中断与销毁视图：interrupt 幂等
// （首次调用生效），destroyed 返回当前销毁标记。首次调用依次：置位销毁标记 →
// res.End() + res.SetDestroyed()（守卫立即生效）→ res.Hijack() 拿底层连接 →
// 冲净 bufio 写侧残留 → conn.Close()。
func newDownstreamInterrupt(res *gatewaypreauth.TrackingWriter) (interrupt func(), destroyed func() bool) {
	var once sync.Once
	destroyedFlag := &atomic.Bool{}
	interrupt = func() {
		once.Do(func() {
			destroyedFlag.Store(true)
			res.End()
			res.SetDestroyed()
			conn, rw, err := res.Hijack()
			if err != nil {
				slog.Warn("gateway_downstream_interrupt_hijack_failed",
					"error", err, "degrade", "优雅收尾（底层连接不受控，销毁标记保持生效）")
				return
			}
			if rw != nil {
				_ = rw.Flush()
			}
			_ = conn.Close()
		})
	}
	return interrupt, func() bool { return destroyedFlag.Load() }
}

// interruptDownstreamConnection 对 pre-commit 的非流式客户端交接执行真·网络级
// 断开：先提交 200 + JSON 传输头并 flush，再 Hijack 底层连接并 Close（不发
// 完整响应）。这样反向代理（Caddy）看到的是「已开始响应但正文截断」，把截断
// 透传给客户端，客户端以网络级读错误收场并自行重试；若不在断连前提交响应头，
// 代理会把 pre-commit 断联合成为 502，客户端收到的仍是错误响应而不是断连。
//
// 返回 (interrupted, headersCommitted)：interrupted=true 表示连接已断开；
// headersCommitted=true 但 interrupted=false 表示 200 头已提交而 Hijack 被
// 底层拒绝——已无法回退 503，只能以截断正文收场，审计须记录该降级。
func interruptDownstreamConnection(res *gatewaypreauth.TrackingWriter) (bool, bool) {
	if res == nil || res.WritableEnded() || res.HeadersSent() {
		return false, false
	}
	hijacker, ok := res.ResponseWriter.(http.Hijacker)
	if !ok {
		return false, false
	}
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(http.StatusOK)
	if flusher, ok := res.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		slog.Warn("gateway_downstream_handoff_hijack_failed",
			"error", err, "degrade", "200 头已提交，客户端以截断正文收场")
		return false, true
	}
	res.End()
	res.SetDestroyed()
	if rw != nil {
		_ = rw.Flush()
	}
	if conn != nil {
		_ = conn.Close()
	}
	return true, true
}

// interruptStreamingHandoff 对 pre-commit 的流式交接执行真·网络级断开：
// 响应尚未开始时先提交 200/`text/event-stream` 头并 flush（避免反向代理把
// pre-commit 断开合成 502），再 Hijack+Close；若等待期保活心跳已提交同样的
// SSE 传输头（语义未提交），不再重复写头，直接在已提交的流上 Hijack+Close，
// 客户端看到截断的流并走网络错误自动重试。
//
// 返回 (interrupted, headersCommitted)：interrupted=true 表示连接已断开；
// headersCommitted=true 但 interrupted=false 表示 SSE 头已提交而 Hijack
// 被底层拒绝——此时已无法回退 503（调用方只能正常收尾，审计须记录该降级，
// 不能声称写入了 503）。两者都为 false 时响应头尚未写，可安全回退 503。
func interruptStreamingHandoff(res *gatewaypreauth.TrackingWriter, req *gatewaypreauth.GatewayRequest) (bool, bool) {
	if res == nil || req == nil || !gatewaypreauth.RequestStream(req) || res.WritableEnded() {
		return false, false
	}
	hijacker, ok := res.ResponseWriter.(http.Hijacker)
	if !ok {
		return false, false
	}
	headersCommitted := false
	if res.HeadersSent() {
		// 只有等待心跳提交的 SSE 传输头允许在已写头后断连；其他已写响应
		// （JSON 等）不得截断成"正常 EOF"。
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(res.Header().Get("Content-Type"))), "text/event-stream") {
			return false, false
		}
		headersCommitted = true
	} else {
		res.Header().Set("Content-Type", "text/event-stream")
		res.Header().Set("Cache-Control", "no-cache")
		res.Header().Set("X-Accel-Buffering", "no")
		res.WriteHeader(http.StatusOK)
		if flusher, ok := res.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		headersCommitted = true
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		slog.Warn("gateway_stream_handoff_hijack_failed",
			"error", err, "degrade", "SSE 传输头已提交，客户端以正常 EOF 收场")
		return false, headersCommitted
	}
	res.End()
	res.SetDestroyed()
	if rw != nil {
		_ = rw.Flush()
	}
	if conn != nil {
		_ = conn.Close()
	}
	return true, headersCommitted
}
