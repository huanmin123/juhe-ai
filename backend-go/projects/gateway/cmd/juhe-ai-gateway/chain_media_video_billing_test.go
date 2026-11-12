package main

// 视频任务终态计费纯函数单测（chainMediaJobTerminalCostUsd 及其助手，契约
// §2.8 网关自算口径 + 2026-10-05 按次计费批）：按次行、请求参数自算计量、
// usage_missing 三级裁决与来源优先级。静态目录价即快照（glm cogvideox-3
// 1 元/次 ÷7.0、minimax MiniMax-H3 768P 0.50 元/s ÷7.0、gemini veo-3.0
// $0.40/s，2026-10-17 口径批国内面回正），不依赖 mock 上游与 spool 链。
import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
)

// billingTestSeconds 分配可空秒数（表驱动助手）。
func billingTestSeconds(v float64) *float64 { return &v }

// TestChainMediaJobTerminalCostUsdVideoPerCall 锁定按次行结算（契约 §2.8
// 按次计费）：目录行有 VideoOutputCostPerCall（cogvideox-3 国内 1 元/次
// ÷7.0）→ 一次任务 = 一次调用，成本 = PerCall × 1，不依赖秒计量（glm 轮询
// 无时长回报，快照请求秒数也不进秒价链）。
func TestChainMediaJobTerminalCostUsdVideoPerCall(t *testing.T) {
	// cogvideox 载体已随 2025 前模型清理批移除，按次机制载体换现役
	// MiniMax-Hailuo-2.3（国内 768P-6s 主档 2.00 元/条 ÷7.0）。
	// B4①：计价键取水合账户 vendor（media_jobs 不再持久化 provider_code）。
	account := &chainMediaJobAccount{ProviderCode: "minimax"}
	record := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:   "MiniMax-Hailuo-2.3",
			Seconds: billingTestSeconds(6),
		},
		ParamsApplied: []string{"model", "prompt", "seconds"},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0.2857142857 {
		t.Fatalf("Hailuo-2.3 per-call cost = %v, want 0.2857142857（roundCost(2/7)，PerCall × 1）", cost)
	}
	// 2026-11-12 时长档批：billingSeconds=10 命中 10s 档（4.00 元/条 ÷7.0）。
	tenSeconds := *record
	tenSeconds.RequestSnapshot.BillingSeconds = billingTestSeconds(10)
	ir.Status = gatewaymedia.JobStatusCompleted
	if cost := chainMediaJobTerminalCostUsd(account, &tenSeconds, ir); cost != 0.5714285714 {
		t.Fatalf("Hailuo-2.3 10s cost = %v, want 0.5714285714（roundCost(4/7)）", cost)
	}
	// failed 不虚计（按次行同样只在 completed 计费）。
	ir.Status = gatewaymedia.JobStatusFailed
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 0 {
		t.Fatalf("failed per-call cost = %v, want 0", cost)
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
			Model:          "MiniMax-H3", // 768P 0.50 元/s ÷ 7.0
			Seconds:        billingTestSeconds(4),
			BillingSeconds: billingTestSeconds(4),
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
// （不猜测；2026-11-12 厂商默认时长批）：创建时未冻结计费秒数基源且上游
// 无回报 → 0；只要创建时冻结了基源（生效请求值或厂商默认时长）就按基源
// 计费。ignored 的 seconds 本身不构成基源——Veo 行按厂商默认 8 秒冻结，
// 而不是按用户被忽略的请求值。
func TestChainMediaJobTerminalCostUsdUsageMissing(t *testing.T) {
	// 无回报且创建时没有冻结任何计费秒数基源 → usage_missing（0）。
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
	// Veo：请求 seconds 进 ignored，但创建时按厂商默认 8 秒冻结基源 →
	// 8 × $0.10 = $0.80（veo-3.1-fast-generate-preview 现役秒价；veo-3.0
	// 目录行 shutdownDate 已过、不可达，不用作计费断言），不按用户被忽略
	// 的值、也不再落 0。
	veo := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:          "veo-3.1-fast-generate-preview",
			Seconds:        billingTestSeconds(4),
			BillingSeconds: billingTestSeconds(8),
		},
		ParamsApplied: []string{"model", "prompt", "size", "negative_prompt"},
		ParamsIgnored: []string{"seconds"},
	}
	if cost := chainMediaJobTerminalCostUsd(&chainMediaJobAccount{ProviderCode: "gemini"}, veo, ir); cost != 0.8 {
		t.Fatalf("veo default-seconds cost = %v, want 0.8（8 × 0.1）", cost)
	}
	// 助手级断言：基源在场时 enqueue 侧拿到 8 秒，不再落 usage_missing。
	if seconds := chainMediaJobVideoUsageSecondsOf(veo, ir); seconds == nil || *seconds != 8 {
		t.Fatalf("veo billing seconds = %v, want 8", seconds)
	}
}

// TestChainMediaJobTerminalCostUsdVideoResolutionTiers 锁定分辨率档结算
// （2026-11-12 分辨率档批）：xAI 官方 1080p $0.25/s × 10s = $2.50；创建时
// 冻结的 billingResolution 为空时回落 720p 主档。
func TestChainMediaJobTerminalCostUsdVideoResolutionTiers(t *testing.T) {
	account := &chainMediaJobAccount{ProviderCode: "xai"}
	record := &gatewaymedia.MediaJobRecord{
		Kind: gatewaymedia.JobKindVideo,
		RequestSnapshot: gatewaymedia.MediaJobRequestSnapshot{
			Model:             "grok-imagine-video-1.5",
			BillingSeconds:    billingTestSeconds(10),
			BillingResolution: "1080p",
		},
	}
	ir := &gatewaymedia.MediaJobIR{Status: gatewaymedia.JobStatusCompleted}
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 2.5 {
		t.Fatalf("xai 1080p cost = %v, want 2.5（10s × $0.25/s）", cost)
	}
	record.RequestSnapshot.BillingResolution = ""
	if cost := chainMediaJobTerminalCostUsd(account, record, ir); cost != 1.4 {
		t.Fatalf("xai main-tier cost = %v, want 1.4（10s × $0.14/s）", cost)
	}
}

// TestChainMinimaxV2UpstreamURL 锁定 minimax H3 v2 版本根特例（契约 §8.1）：
// 账户 base_url 承载 Hailuo /v1 版本根，H3 的 /v2 操作路径必须落在 host 根；
// 已实测 /v1/v2/... 为 404、/v2/... 命中鉴权信封。
func TestChainMinimaxV2UpstreamURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://api.minimax.chat/v1", "/v2/video_generation", "https://api.minimax.chat/v2/video_generation"},
		{"https://api.minimax.chat/v1/", "/v2/query/video_generation/task", "https://api.minimax.chat/v2/query/video_generation/task"},
		{"https://api.minimax.chat", "/v2/video_generation", "https://api.minimax.chat/v2/video_generation"},
	}
	for _, tc := range cases {
		got, err := chainMinimaxV2UpstreamURL(tc.base, tc.path)
		if err != nil || got != tc.want {
			t.Fatalf("chainMinimaxV2UpstreamURL(%q,%q) = %q/%v, want %q", tc.base, tc.path, got, err, tc.want)
		}
	}
}

// TestChainVideoJobTaskAdapterShellModelRouting 锁定任务面按冻结模型分派：
// H3 走 v2 路径参数轮询，Hailuo 保持 v1 query 形态。
func TestChainVideoJobTaskAdapterShellModelRouting(t *testing.T) {
	video := gatewaymedia.VideoAdapterForProvider("minimax")
	h3 := chainVideoJobTaskAdapterShell{inner: video, model: "MiniMax-H3"}
	if _, path, _, _ := h3.BuildPollRequest("task-1"); path != "/v2/query/video_generation/task-1" {
		t.Fatalf("H3 poll path = %q", path)
	}
	hailuo := chainVideoJobTaskAdapterShell{inner: video, model: "MiniMax-Hailuo-2.3"}
	if _, path, _, _ := hailuo.BuildPollRequest("task-1"); path != "/query/video_generation?task_id=task-1" {
		t.Fatalf("Hailuo poll path = %q", path)
	}
}
