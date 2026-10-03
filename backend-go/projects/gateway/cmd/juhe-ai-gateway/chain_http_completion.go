package main

// 每请求 HTTP 完成观测 subject（审计缺口收口 A2）：链入口在请求终态 defer
// 处 complete 一次，audit capture（gatewayusage.GatewayHTTPCompletionObserver，
// 驱动 audit http_completed_at / http_duration_ms 的 flush 等待）与响应层
// sink 失败 usage（gatewayresponse.HTTPCompletionObserver，CompletedAtMs）
// 从同一 subject 读取完成时刻。此前生产两处端口均未装配：审计两列恒 NULL，
// sink 失败 usage 的 CompletedAtMs 退化为 nowMs。
//
// 并发契约：complete 单次幂等；监听者在 complete 锁外同步回调、不起
// goroutine；observe 侧以 1 缓冲通道投递，无消费者也不阻塞、不泄漏。

import (
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// chainHTTPCompletionKey 把本请求的 subject 挂进请求上下文，sink 侧
// Observe 据此解析（对齐 D-119 chainGatewayRuntimeKey 的传递模式）。
type chainHTTPCompletionKeyType struct{}

var chainHTTPCompletionKey chainHTTPCompletionKeyType

// gatewayHTTPCompletionListener 包一层可取消句柄（函数值不可比较，cancel
// 置位后 complete 跳过回调）。
type gatewayHTTPCompletionListener struct {
	fn        func(completedAtMs int64)
	cancelled bool
}

// gatewayHTTPCompletion 是每请求单次完成信号 subject。
type gatewayHTTPCompletion struct {
	mu          sync.Mutex
	completed   bool
	completedAt int64
	listeners   []*gatewayHTTPCompletionListener
}

func newGatewayHTTPCompletion() *gatewayHTTPCompletion {
	return &gatewayHTTPCompletion{}
}

// complete 标记完成并同步通知监听者（幂等：仅首次生效；已取消的句柄跳过）。
func (s *gatewayHTTPCompletion) complete(completedAtMs int64) {
	s.mu.Lock()
	if s.completed {
		s.mu.Unlock()
		return
	}
	s.completed = true
	s.completedAt = completedAtMs
	listeners := s.listeners
	s.listeners = nil
	s.mu.Unlock()
	for _, listener := range listeners {
		if listener.cancelled {
			continue
		}
		listener.fn(completedAtMs)
	}
}

// CompletedAtMs implements gatewayusage.GatewayHTTPCompletionObserver.
func (s *gatewayHTTPCompletion) CompletedAtMs() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completedAt, s.completed
}

// OnCompleted implements gatewayusage.GatewayHTTPCompletionObserver：已完成
// 时立即同步回调一次并返回 no-op cancel；未完成时注册句柄，cancel 仅使该
// 句柄失效（不影响其他监听者）。
func (s *gatewayHTTPCompletion) OnCompleted(listener func(completedAtMs int64)) func() {
	s.mu.Lock()
	if s.completed {
		value := s.completedAt
		s.mu.Unlock()
		listener(value)
		return func() {}
	}
	handle := &gatewayHTTPCompletionListener{fn: listener}
	s.listeners = append(s.listeners, handle)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		handle.cancelled = true
		s.mu.Unlock()
	}
}

// gatewayHTTPCompletionChannel 是 gatewayresponse.HTTPCompletion 的 subject
// 订阅视图：1 缓冲通道在 complete 时投递一次完成时刻。
type gatewayHTTPCompletionChannel struct {
	ch chan int64
}

// Wait implements gatewayresponse.HTTPCompletion.
func (c gatewayHTTPCompletionChannel) Wait() <-chan int64 { return c.ch }

// observe 订阅一次完成信号：已完成立即投递，未完成注册监听（OnCompleted
// 在锁内复查完成态，注册与完成并发时不丢信号；缓冲 1 保证无人 Wait 也不
// 阻塞）。
func (s *gatewayHTTPCompletion) observe() gatewayresponse.HTTPCompletion {
	ch := make(chan int64, 1)
	if value, ok := s.CompletedAtMs(); ok {
		ch <- value
		return gatewayHTTPCompletionChannel{ch: ch}
	}
	s.OnCompleted(func(value int64) {
		select {
		case ch <- value:
		default:
		}
	})
	return gatewayHTTPCompletionChannel{ch: ch}
}

// chainHTTPCompletionObserver implements gatewayresponse.HTTPCompletionObserver：
// 按请求上下文解析 subject，缺失（无 subject 装配的路径 / 测试替身）返回
// nil，sink 侧回退 nowMs 兜底。
type chainHTTPCompletionObserver struct{}

// Observe implements gatewayresponse.HTTPCompletionObserver.
func (chainHTTPCompletionObserver) Observe(req *gatewaypreauth.GatewayRequest, _ gatewaypreauth.GatewayResponseWriter) gatewayresponse.HTTPCompletion {
	if req == nil || req.HTTP == nil {
		return nil
	}
	subject, ok := req.HTTP.Context().Value(chainHTTPCompletionKey).(*gatewayHTTPCompletion)
	if !ok || subject == nil {
		return nil
	}
	return subject.observe()
}

var (
	_ gatewayusage.GatewayHTTPCompletionObserver = (*gatewayHTTPCompletion)(nil)
	_ gatewayresponse.HTTPCompletionObserver     = chainHTTPCompletionObserver{}
)
