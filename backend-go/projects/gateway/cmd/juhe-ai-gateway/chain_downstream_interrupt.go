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
