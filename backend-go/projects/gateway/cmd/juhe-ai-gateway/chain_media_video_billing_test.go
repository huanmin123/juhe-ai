package main

// 视频任务终态计费纯函数单测（chainMediaJobTerminalCostUsd 及其助手，契约
// §2.8 网关自算口径 + 2026-10-05 按次计费批）：按次行、请求参数自算计量、
// usage_missing 三级裁决与来源优先级。静态目录价即快照（glm cogvideox-3
// $0.2/次、minimax MiniMax-H3 $0.08/s、gemini veo-3.0 $0.40/s），不依赖
// mock 上游与 spool 链。
import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// billingTestSeconds 分配可空秒数（表驱动助手）。
func billingTestSeconds(v float64) *float64 { return &v }

// TestChainMediaJobTerminalCostUsdVideoPerCall 锁定按次行结算（契约 §2.8
// 按次计费）：目录行有 VideoOutputCostPerCall（cogvideox-3 $0.2/次）→ 一次
// 任务 = 一次调用，成本 = PerCall × 1，不依赖秒计量（glm 轮询无时长回报，
// 快照请求秒数也不进秒价链）。
func TestChainMediaJobTerminalCostUsdVideoPerCall(t *testing.T) {
	record := &gatewaymedia.MediaJobRecord{
		Kind:         gatewaymedia.JobKindVideo,
		ProviderCode: "glm",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "cogvideox-3",
			Seconds: billingTestSeconds(6),
		},
		ParamsApplied: []string{"model", "prompt", "seconds"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0.2 {
		t.Fatalf("cogvideox-3 per-call cost = %v, want 0.2（PerCall × 1）", cost)
	}
	// cogvideox-2：0.5 元/次 ÷ 7.0 = $0.0714（换算快照字面量）。
	record.RequestSnapshot.Model = "cogvideox-2"
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0.0714 {
		t.Fatalf("cogvideox-2 per-call cost = %v, want 0.0714", cost)
	}
	// failed 不虚计（按次行同样只在 completed 计费）。
	ir.Status = gatewaymedia.JobStatusFailed
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0 {
		t.Fatalf("failed per-call cost = %v, want 0", cost)
	}
}

// TestChainMediaJobTerminalCostUsdRequestSelfComputedSeconds 锁定请求参数
// 自算计量（契约 §2.8 网关自算口径，2026-10-05 批）：秒价模型无上游 usage
// 回报、创建请求 seconds 生效（ParamsApplied 含 seconds）→ 按快照请求秒数
// × 目录秒价计费（usage_source=request 语义）；上游回报优先于请求自算。
func TestChainMediaJobTerminalCostUsdRequestSelfComputedSeconds(t *testing.T) {
	record := &gatewaymedia.MediaJobRecord{
		Kind:         gatewaymedia.JobKindVideo,
		ProviderCode: "minimax",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "MiniMax-H3", // $0.08/s
			Seconds: billingTestSeconds(4),
		},
		ParamsApplied: []string{"model", "prompt", "seconds", "input_reference"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0.32 {
		t.Fatalf("self-computed cost = %v, want 0.32（4s × $0.08/s）", cost)
	}
	// 上游回报优先：轮询响应带秒计量时以回报为准（2s → 0.16），请求值不覆盖。
	ir.Usage.OutputVideoSeconds = billingTestSeconds(2)
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0.16 {
		t.Fatalf("upstream usage wins = %v, want 0.16（2s × $0.08/s）", cost)
	}
}

// TestChainMediaJobTerminalCostUsdUsageMissing 锁定 usage_missing 边界
// （不猜测）：无回报且无生效请求参数 → 0；ignored 的 seconds 不构成计量
// 基源（§2.4 规则 1/3：用户值不生效、厂商用默认时长，网关不猜测默认值
// ——Veo 先例，即便模型落了秒价也不按 ignored 请求值计）。
func TestChainMediaJobTerminalCostUsdUsageMissing(t *testing.T) {
	// 无回报且请求未带 seconds → usage_missing（0）。
	record := &gatewaymedia.MediaJobRecord{
		Kind:            gatewaymedia.JobKindVideo,
		ProviderCode:    "minimax",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "MiniMax-H3"},
		ParamsApplied:   []string{"model", "prompt"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(record, ir); cost != 0 {
		t.Fatalf("no-basis cost = %v, want 0（usage_missing 不猜测）", cost)
	}
	// seconds 进了请求但属 ignored（Veo：SupportsSeconds=false）→ 不自算，
	// 目录秒价（$0.40/s）不按未生效的用户值连乘。
	veo := &gatewaymedia.MediaJobRecord{
		Kind:         gatewaymedia.JobKindVideo,
		ProviderCode: "gemini",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "veo-3.0-generate-preview",
			Seconds: billingTestSeconds(8),
		},
		ParamsApplied: []string{"model", "prompt", "size", "negative_prompt"},
		ParamsIgnored: []string{"seconds"},
	}
	if cost := chainMediaJobTerminalCostUsd(veo, ir); cost != 0 {
		t.Fatalf("ignored seconds cost = %v, want 0（用户值不生效不构成计量基源）", cost)
	}
	// 助手级断言：ignored 场景 chainMediaJobVideoUsageSecondsOf 返回 nil
	//（enqueue 侧据此落 usage_missing，而非自算秒数）。
	if seconds := chainMediaJobVideoUsageSecondsOf(veo, ir); seconds != nil {
		t.Fatalf("ignored seconds metering = %v, want nil", *seconds)
	}
}
