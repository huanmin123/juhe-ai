package main

// w1（单元层）：B1 回归——usageDispatchAdapter 入队失败采样计数在值接收者下
// 失效：每次经接口调用都复制接收者副本，enqueueFailures 永远从 0 加到 1 后随
// 副本丢弃，"前 10 条逐条、之后每 100 条一条"的采样永不生效、逐条刷 Warn。
// 指针接收者 + 指针字面量构造后，计数必须跨接口调用累计、采样阈值必须生效。

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// alwaysFailingUsageRecorder 是恒失败的 UsageRecorder 测试替身：每次入队返回
// 固定错误，驱动 usageDispatchAdapter 的采样计数路径。
type alwaysFailingUsageRecorder struct{}

func (alwaysFailingUsageRecorder) EnqueueUsageRecord(ctx gatewayusage.Ctx, input gatewayusage.UsageRecordInput) error {
	return errors.New("enqueue unavailable (w1 sampling test)")
}

// TestW1UsageDispatchAdapterEnqueueFailureSampling：适配器经接口连续失败 110
// 次，断言 enqueueFailures 跨接口调用累计（值接收者缺陷下每次调用后恒为 1）
// 且采样节奏正确（1-10 逐条、11-99 静默、第 100 条一条、101-110 静默）。
func TestW1UsageDispatchAdapterEnqueueFailureSampling(t *testing.T) {
	adapter := &usageDispatchAdapter{recorder: alwaysFailingUsageRecorder{}}
	// 生产组合根（chain_compose.go）以指针字面量存入 gatewayresponse 接口
	// 字段；这里同样经接口调用，值接收者缺陷会在接口分派上暴露。
	var port gatewayresponse.UsageDispatcher = adapter

	// 适配器经包级 slog 输出采样告警（入队失败=记录彻底丢失，级别 Error，
	// 与异步 spool 写失败 Error 对齐；LevelWarn 过滤器同时容纳两级别）。
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(previous)

	input := gatewayresponse.ModelsUsageDispatchInput{
		UsageContext: gatewaypreauth.GatewayFailureUsageContext{TraceID: "trace_w1_sampling"},
	}
	warnCount := func() int { return strings.Count(buf.String(), "gateway_usage_enqueue_failed") }

	// 前 10 次失败：逐条告警，计数逐次累计。
	for i := 0; i < 10; i++ {
		port.DispatchUsageRecord(input)
	}
	if got := atomic.LoadInt64(&adapter.enqueueFailures); got != 10 {
		t.Fatalf("10 次失败后 enqueueFailures = %d, want 10（计数必须跨接口调用累计）", got)
	}
	if got := warnCount(); got != 10 {
		t.Fatalf("前 10 次失败应逐条告警，实际 %d 条: %s", got, buf.String())
	}

	// 第 11 次：计数到达 11；采样阈值（>10 且非 100 倍数）内不再逐条告警。
	port.DispatchUsageRecord(input)
	if got := atomic.LoadInt64(&adapter.enqueueFailures); got != 11 {
		t.Fatalf("11 次失败后 enqueueFailures = %d, want 11", got)
	}
	if got := warnCount(); got != 10 {
		t.Fatalf("第 11 次失败起应进入采样（不再逐条告警），实际 %d 条: %s", got, buf.String())
	}

	// 第 12-99 次：每条失败仍计数，但不再产生日志。
	for i := 0; i < 88; i++ {
		port.DispatchUsageRecord(input)
	}
	if got := warnCount(); got != 10 {
		t.Fatalf("第 12-99 次失败不应产生日志，实际 %d 条: %s", got, buf.String())
	}

	// 第 100 次：每 100 条一条的采样告警。
	port.DispatchUsageRecord(input)
	if got := atomic.LoadInt64(&adapter.enqueueFailures); got != 100 {
		t.Fatalf("100 次失败后 enqueueFailures = %d, want 100", got)
	}
	if got := warnCount(); got != 11 {
		t.Fatalf("第 100 次失败应产生每百条一条的采样告警，实际 %d 条: %s", got, buf.String())
	}

	// 第 101-110 次：回到静默采样，计数继续累计。
	for i := 0; i < 10; i++ {
		port.DispatchUsageRecord(input)
	}
	if got := atomic.LoadInt64(&adapter.enqueueFailures); got != 110 {
		t.Fatalf("110 次失败后 enqueueFailures = %d, want 110", got)
	}
	if got := warnCount(); got != 11 {
		t.Fatalf("第 101-110 次失败不应产生日志，实际 %d 条: %s", got, buf.String())
	}
}
