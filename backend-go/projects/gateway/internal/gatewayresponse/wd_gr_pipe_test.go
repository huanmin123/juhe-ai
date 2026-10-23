package gatewayresponse

// gatewayresponse 流管道场景补充（wd_ 前缀，独占新增）：语义提交后的协议失败
// 对全部客户端（含 ClientRetryEnabled 的精确客户端）一律中断连接，不再补发
// 脱敏协议失败事件（2026-10-23 契约收口）。分片序列固定，无真实网络与时钟
// 依赖。

import (
	"strings"
	"sync"
	"testing"
)

const wdResponsesDelta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"你好\"}\n\n"

const wdResponsesFailed = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"provider_error\",\"message\":\"供应商失败\"}}}\n\n"

// TestWdCommittedFailureAlwaysInterrupts：精确客户端（ClientRetryEnabled +
// responses_sse 下游）语义提交后解析到协议失败时，处置恒为中断连接 ——
// InterruptNow 恰好一次、下游无新增失败事件字节、失败仍进入
// HandleStreamFailure 且管道不向上层抛错。
func TestWdCommittedFailureAlwaysInterrupts(t *testing.T) {
	recorder := &failureRecorder{}
	var mu sync.Mutex
	interrupts := 0
	var written []byte
	_, downstream := newDownstream()
	body := NewSliceUpstreamBody([]byte(wdResponsesDelta), []byte(wdResponsesFailed))
	result, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream: StreamDownstream{
			Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
				mu.Lock()
				written = append(written, data...)
				mu.Unlock()
			}},
			Interrupt: func() {
				mu.Lock()
				interrupts++
				mu.Unlock()
			},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options: StreamPipeOptions{
			ClientRetryEnabled: true,
			DownstreamProtocol: "responses_sse",
			NowMs:              func() int64 { return 1000 },
		},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("提交后失败不得按完成收尾: %+v", result)
	}
	if !result.SemanticCommitted {
		t.Fatalf("delta 应已语义提交下游: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if interrupts != 1 {
		t.Fatalf("提交后失败应恰好中断一次连接，interrupts = %d", interrupts)
	}
	if !strings.Contains(string(written), "你好") {
		t.Fatalf("delta 应已到达下游: %q", written)
	}
	if strings.Contains(string(written), "供应商失败") || strings.Contains(string(written), "response.failed") {
		t.Fatalf("中断处置不得向下游补发失败事件: %q", written)
	}
	if recorder.count() != 1 {
		t.Fatalf("提交后失败仍应记录一次失败: %d %v", recorder.count(), result)
	}
	if last := recorder.last(); last.errorCode != "provider_error" || last.message != "供应商失败" {
		t.Fatalf("失败记录错误: %+v", last)
	}
}
