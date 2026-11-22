package main

// 视频任务终态计费纯函数单测（chainMediaJobTerminalCostUsd 及其助手，契约
// §2.8 网关自算口径 + 2026-10-05 按次计费批）：按次行、请求参数自算计量、
// usage_missing 三级裁决与来源优先级。静态目录价即快照（glm cogvideox-3
// 1 元/次 ÷7.0、minimax MiniMax-H3 768P 0.50 元/s ÷7.0、gemini veo-3.0
// $0.40/s，2026-10-17 口径批国内面回正），不依赖 mock 上游与 spool 链。
import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// billingTestSeconds 分配可空秒数（表驱动助手）。
func billingTestSeconds(v float64) *float64 { return &v }

// TestChainMediaJobTerminalCostUsdVideoPerCall 锁定按次行结算（契约 §2.8
// 按次计费）：P3 迁移后唯一 unit=call 视频行是 doubao-seed3d（2.40 元/次
// ÷7.0）→ 一次任务 = 一次调用，成本 = baseUsd × 1，不依赖秒计量。
func TestChainMediaJobTerminalCostUsdVideoPerCall(t *testing.T) {
	// B4①：计价键取水合账户 vendor（media_jobs 不再持久化 provider_code）。
	account := &chainMediaJobAccount{ProviderCode: "volcengine"}
	record := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "doubao-seed3d-2-0-260328",
		},
		ParamsApplied: []string{"model", "prompt"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0.3428571429 {
		t.Fatalf("doubao-seed3d per-call cost = %v, want 0.3428571429（roundCost(2.4/7)）", cost)
	}
	// failed 不虚计（按次行同样只在 completed 计费）。
	ir.Status = gatewaymedia.JobStatusFailed
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0 {
		t.Fatalf("failed per-call cost = %v, want 0", cost)
	}
}

// TestChainMediaJobTerminalCostUsdHailuoMigratedSeconds 锁定 P3 迁移后的
// Hailuo 2.3 按秒结算：10s × 10s 套餐折算价 0.057142857/s（不再按条）。
func TestChainMediaJobTerminalCostUsdHailuoMigratedSeconds(t *testing.T) {
	account := &chainMediaJobAccount{ProviderCode: "minimax"}
	record := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "MiniMax-Hailuo-2.3",
			Seconds: billingTestSeconds(10),
		},
		ParamsApplied: []string{"model", "prompt", "seconds"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0.5714285714 {
		t.Fatalf("Hailuo-2.3 10s cost = %v, want 0.5714285714（roundCost(10×4/7/10)）", cost)
	}
}

// TestChainMediaJobTerminalCostUsdRequestSelfComputedSeconds 锁定请求参数
// 自算计量（契约 §2.8 网关自算口径，2026-10-05 批）：秒价模型无上游 usage
// 回报、创建请求 seconds 生效（ParamsApplied 含 seconds）→ 按快照请求秒数
// × 目录秒价计费（usage_source=request 语义）；上游回报优先于请求自算。
func TestChainMediaJobTerminalCostUsdRequestSelfComputedSeconds(t *testing.T) {
	account := &chainMediaJobAccount{ProviderCode: "minimax"}
	record := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "MiniMax-H3", // 768P 0.50 元/s ÷ 7.0
			Seconds: billingTestSeconds(4),
		},
		ParamsApplied: []string{"model", "prompt", "seconds", "input_reference"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0.2857142857 {
		t.Fatalf("self-computed cost = %v, want 0.2857142857（roundCost(4s × 0.5/7)）", cost)
	}
	// 上游回报优先：轮询响应带秒计量时以回报为准，请求值不覆盖。
	ir.Usage.OutputVideoSeconds = billingTestSeconds(2)
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0.1428571429 {
		t.Fatalf("upstream usage wins = %v, want 0.1428571429（roundCost(2s × 0.5/7)）", cost)
	}
}

// TestChainMediaJobTerminalCostUsdUsageMissing 锁定 usage_missing 边界
// （不猜测）：无回报且无生效请求参数 → 0；ignored 的 seconds 不构成计量
// 基源（§2.4 规则 1/3：用户值不生效、厂商用默认时长，网关不猜测默认值
// ——Veo 先例，即便模型落了秒价也不按 ignored 请求值计）。
func TestChainMediaJobTerminalCostUsdUsageMissing(t *testing.T) {
	// 无回报且请求未带 seconds → usage_missing（0）。
	account := &chainMediaJobAccount{ProviderCode: "minimax"}
	record := &gatewaymedia.MediaJobRecord{
		Kind:            gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{Model: "MiniMax-H3"},
		ParamsApplied:   []string{"model", "prompt"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0 {
		t.Fatalf("no-basis cost = %v, want 0（usage_missing 不猜测）", cost)
	}
	// seconds 进了请求但属 ignored（Veo：SupportsSeconds=false）→ 不自算，
	// 目录秒价（$0.40/s）不按未生效的用户值连乘。
	veo := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "veo-3.0-generate-preview",
			Seconds: billingTestSeconds(8),
		},
		ParamsApplied: []string{"model", "prompt", "size", "negative_prompt"},
		ParamsIgnored: []string{"seconds"},
	}
	if cost := chainMediaJobTerminalCostUsd(&chainMediaJobAccount{ProviderCode: "gemini"}, veo, ir); cost != 0 {
		t.Fatalf("ignored seconds cost = %v, want 0（用户值不生效不构成计量基源）", cost)
	}
	// 助手级断言：ignored 场景 chainMediaJobVideoUsageSecondsOf 返回 nil
	//（enqueue 侧据此落 usage_missing，而非自算秒数）。
	if seconds := chainMediaJobVideoUsageSecondsOf(veo, ir); seconds != nil {
		t.Fatalf("ignored seconds metering = %v, want nil", *seconds)
	}
}

// TestEnqueueMediaJobTerminalUsageInvalidMediaPricingZeroCost 钉住 AC-016 异步
// 路径：非法 media_pricing_json 的终态任务必须向 usage 行写显式 0 成本 +
// usage_missing，不得因 breakdown=nil 留下 cost_usd=NULL（同步路径已在
// chain_usage.go 同口径处理）。
func TestEnqueueMediaJobTerminalUsageInvalidMediaPricingZeroCost(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	chain := &gatewayChain{finalizationUsage: recorder}
	record := &gatewaymedia.MediaJobRecord{
		ID:        "job-invalid-pricing",
		Kind:      gatewaymedia.JobKindVideo,
		APIKeyID:  "key-1",
		AccountID: "acc-1",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "doubao-seedance-1-0-pro-250528",
		},
	}
	account := &chainMediaJobAccount{
		OwnerSystemAccountID: "owner-1",
		ProviderCode:         "volcengine",
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	settlement := chainMediaJobSettlement{
		usageMissing:        true,
		media:               true,
		mediaPricingInvalid: true,
	}
	chain.enqueueMediaJobTerminalUsage(context.Background(), "trace-1", "api-owner-1", record, account, ir, settlement)

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(records))
	}
	if records[0].CostUsd == nil || *records[0].CostUsd != 0 {
		t.Fatalf("CostUsd = %v, want 显式 0（AC-016）", records[0].CostUsd)
	}
	if !records[0].UsageMissing {
		t.Fatal("非法 media_pricing_json 必须标 usage_missing")
	}
}

// TestEnqueueMediaJobTerminalUsageMediaMeteringMissingZeroCost 钉住统一媒体行
// 「有效模板但计量缺失」的终态口径（设计 §5.2/§7.1）：settlement.media=true、
// breakdown=nil、mediaPricingInvalid=false 时同样必须显式落 0 + usage_missing，
// 不能留 cost_usd=NULL；legacy 无价行（media=false）保持原 NULL 语义。
func TestEnqueueMediaJobTerminalUsageMediaMeteringMissingZeroCost(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	chain := &gatewayChain{finalizationUsage: recorder}
	record := &gatewaymedia.MediaJobRecord{
		ID:        "job-metering-missing",
		Kind:      gatewaymedia.JobKindVideo,
		APIKeyID:  "key-1",
		AccountID: "acc-1",
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model: "veo-3.0-generate-preview",
		},
	}
	account := &chainMediaJobAccount{
		OwnerSystemAccountID: "owner-1",
		ProviderCode:         "gemini",
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	settlement := chainMediaJobSettlement{
		usageMissing: true,
		media:        true,
	}
	chain.enqueueMediaJobTerminalUsage(context.Background(), "trace-1", "api-owner-1", record, account, ir, settlement)

	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1", len(records))
	}
	if records[0].CostUsd == nil || *records[0].CostUsd != 0 {
		t.Fatalf("CostUsd = %v, want 显式 0（统一媒体行计量缺失）", records[0].CostUsd)
	}
	if !records[0].UsageMissing {
		t.Fatal("统一媒体行计量缺失必须标 usage_missing")
	}
}
