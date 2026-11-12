package pricing

// M2 视频计费行项测试（媒体设计 §10 / 契约 §2.8）：
//   - video_output_seconds：数量 = 输出秒，成本 = seconds × VideoOutputUsdPerSecond；
//   - video_output_calls（2026-10-05 按次计费批）：数量 = 调用次数（一次任务
//     = 一次调用），成本 = calls × VideoOutputUsdPerCall；
//   - 无价格或无计量不产行项（失败任务不虚计）；秒价与次价同行互斥（门禁）；
//   - EstimateProviderCostUsd 对静态快照可直接估出终态成本
//     （任务终态计费回填链的消费面）。
import "testing"

func TestBuildCostBreakdownVideoOutputSecondsLine(t *testing.T) {
	// sora-2 静态快照：$0.10/s（2026-09-24 官方关停，经关停日前回溯查询
	// 验证历史计费口径）。
	breakdown := BuildCostBreakdown(mustFindSoraPricingForTest(t, "openai", "sora-2"), CostInput{
		OutputVideoSeconds: audioTestFloat(4),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	var line *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == CostLineKindVideoOutputSeconds {
			line = &breakdown.LineItems[index]
		}
	}
	if line == nil {
		t.Fatalf("video_output_seconds line missing: %+v", breakdown.LineItems)
	}
	if line.Quantity != 4 || line.Unit != LineUnitSecond || line.UnitSize != 1 {
		t.Fatalf("line = %+v", line)
	}
	if line.UnitPriceUsd != 0.10 {
		t.Fatalf("unit price = %v want 0.10", line.UnitPriceUsd)
	}
	if line.CostUsd != 0.4 {
		t.Fatalf("cost = %v want 0.4", line.CostUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.4 {
		t.Fatalf("account charge = %v want 0.4", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownVideoNoSecondsNoLine(t *testing.T) {
	// 失败任务不虚计：无秒计量（0）不产行项，charge 回落 0。
	breakdown := BuildCostBreakdown(mustFindSoraPricingForTest(t, "openai", "sora-2"), CostInput{
		OutputVideoSeconds: audioTestFloat(0),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	for _, item := range breakdown.LineItems {
		if item.Kind == CostLineKindVideoOutputSeconds {
			t.Fatalf("zero seconds must not emit a line: %+v", breakdown.LineItems)
		}
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("account charge = %v want 0", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownVideoNoPriceNoLine(t *testing.T) {
	// 无秒价的模型（token 计价的对话模型）携带秒计量：不产行项、不虚计。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "glm", "glm-5.3"), CostInput{
		OutputVideoSeconds: audioTestFloat(4),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	for _, item := range breakdown.LineItems {
		if item.Kind == CostLineKindVideoOutputSeconds {
			t.Fatalf("unpriced seconds must not emit a line: %+v", breakdown.LineItems)
		}
	}
}

func TestEstimateProviderCostUsdVideoSeconds(t *testing.T) {
	// 2026-10-05 新事实：sora-2/sora-2-pro 官方 2026-09-24 关停，现行
	// lookup（asOf=今天）不再解析，估算入口同步返回 nil（关停模型不产生
	// 新计费，不回落历史价）。
	for _, model := range []string{"sora-2", "sora-2-pro"} {
		cost := EstimateProviderCostUsd(CostInput{
			ProviderCode: "openai", Model: model, OutputVideoSeconds: audioTestFloat(4),
		})
		if cost != nil {
			t.Fatalf("shutdown %s estimate = %v want nil", model, cost)
		}
	}
	// 现役视频行的估算消费面由 MiniMax-H3 承接（2026-10-17 国内口径回正：
	// 768P 0.50 元/秒 ÷7.0，4s = 2/7 美元），静态快照直接估出终态成本
	// （任务终态计费回填链的消费面）。
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-H3", OutputVideoSeconds: audioTestFloat(4),
	})
	if cost == nil || *cost != 0.2857142857 {
		t.Fatalf("MiniMax-H3 cost = %v want 0.2857142857（roundCost(4×0.5/7)）", cost)
	}
	// 目录未命中（未知模型）→ nil（0 计费 + usage_missing 标记语义，不猜测）。
	unknown := EstimateProviderCostUsd(CostInput{
		ProviderCode: "openai", Model: "sora-unknown", OutputVideoSeconds: audioTestFloat(4),
	})
	if unknown != nil {
		t.Fatalf("unknown model cost = %v want nil", unknown)
	}
	// 无计量维度 → nil。
	noDimension := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-H3",
	})
	if noDimension != nil {
		t.Fatalf("no dimension cost = %v want nil", noDimension)
	}
}

func TestHasAnyCostDimensionVideoMetering(t *testing.T) {
	if !hasAnyCostDimension(CostInput{OutputVideoSeconds: audioTestFloat(1)}) {
		t.Fatal("OutputVideoSeconds alone must count as a cost dimension")
	}
	// 按次计费批：VideoCalls 同样计入计量维度（按次任务无秒计量）。
	if !hasAnyCostDimension(CostInput{VideoCalls: audioTestFloat(1)}) {
		t.Fatal("VideoCalls alone must count as a cost dimension")
	}
}

// TestBuildCostBreakdownVideoOutputCallsLine 锁定按次行项（2026-10-05 按次
// 计费批，契约 §2.8 按次计费）：MiniMax-Hailuo-2.3 静态快照按国内明文
// 768P-6s 2.00 元/条 ÷7.0（2026-10-17 口径批回正，按次计费每条=每次；
// cogvideox 载体已随 2025 前模型清理批移除）；一次任务 = 一次调用（结算链
// 按任务粒度填 VideoCalls=1），行项 video_output_calls 直乘。
func TestBuildCostBreakdownVideoOutputCallsLine(t *testing.T) {
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "minimax", "MiniMax-Hailuo-2.3"), CostInput{
		VideoCalls: audioTestFloat(1),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	var line *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == CostLineKindVideoOutputCalls {
			line = &breakdown.LineItems[index]
		}
	}
	if line == nil {
		t.Fatalf("video_output_calls line missing: %+v", breakdown.LineItems)
	}
	if line.Quantity != 1 || line.Unit != LineUnitCall || line.UnitSize != 1 {
		t.Fatalf("line = %+v", line)
	}
	if line.UnitPriceUsd != 2.0/7.0 {
		t.Fatalf("unit price = %v want %v", line.UnitPriceUsd, 2.0/7.0)
	}
	// 成本值经 roundCost（10 位小数，契约折算口径）。
	if line.CostUsd != 0.2857142857 {
		t.Fatalf("cost = %v want 0.2857142857", line.CostUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.2857142857 {
		t.Fatalf("account charge = %v want 0.2857142857", breakdown.AccountChargeUsd)
	}
	// 次价行无秒价槽位（互斥）：秒计量在场也不产生秒行项。
	withoutSeconds := BuildCostBreakdown(mustFindPricingForTest(t, "minimax", "MiniMax-Hailuo-2.3"), CostInput{
		VideoCalls:         audioTestFloat(1),
		OutputVideoSeconds: audioTestFloat(6),
	})
	for _, item := range withoutSeconds.LineItems {
		if item.Kind == CostLineKindVideoOutputSeconds {
			t.Fatalf("per-call row must not emit a seconds line: %+v", withoutSeconds.LineItems)
		}
	}
}

func TestBuildCostBreakdownVideoNoCallsNoLine(t *testing.T) {
	// 失败任务不虚计：无次数计量（0）不产行项，charge 回落 0。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "minimax", "MiniMax-Hailuo-2.3"), CostInput{
		VideoCalls: audioTestFloat(0),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	for _, item := range breakdown.LineItems {
		if item.Kind == CostLineKindVideoOutputCalls {
			t.Fatalf("zero calls must not emit a line: %+v", breakdown.LineItems)
		}
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("account charge = %v want 0", breakdown.AccountChargeUsd)
	}
}

func TestEstimateProviderCostUsdVideoCalls(t *testing.T) {
	// MiniMax-Hailuo-2.3：国内按条明文主档 768P-6s 2.00 元/条 ÷7.0（2026-10-17 回正；
	// cogvideox 载体已随 2025 前模型清理批移除，按次机制消费面由 Hailuo 承接）。
	hailuo := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3", VideoCalls: audioTestFloat(1),
	})
	if hailuo == nil || *hailuo != 0.2857142857 {
		t.Fatalf("MiniMax-Hailuo-2.3 cost = %v want 0.2857142857（roundCost(2/7)）", hailuo)
	}
	// 秒价行（MiniMax-H3 $0.08/s）填次数计量：无 PerCall 槽位 → 不按次计，
	// 估出 0（结算链据此回退秒价链）。
	perSecond := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-H3", VideoCalls: audioTestFloat(1),
	})
	if perSecond == nil || *perSecond != 0 {
		t.Fatalf("per-second row calls estimate = %v want 0", perSecond)
	}
	// 目录未命中 → nil（0 计费 + usage_missing 标记语义，不猜测）。
	unknown := EstimateProviderCostUsd(CostInput{
		ProviderCode: "glm", Model: "cogvideox-unknown", VideoCalls: audioTestFloat(1),
	})
	if unknown != nil {
		t.Fatalf("unknown model cost = %v want nil", unknown)
	}
}

// TestVideoCallSecondPriceMutualExclusion 是按次/秒价互斥门禁（2026-10-05
// 按次计费批）：同一目录行 VideoOutputCostPerSecond 与 VideoOutputCostPerCall
// 只落其一（按次 ≠ 按秒；同行双价会让秒/次两条结算路径对同一任务重复计费）。
// 遍历全部供应商静态快照行。
func TestVideoCallSecondPriceMutualExclusion(t *testing.T) {
	for _, entry := range providerCatalog {
		for index := range entry.rawModels {
			row := &entry.rawModels[index]
			if row.VideoOutputCostPerSecond != nil && row.VideoOutputCostPerCall != nil {
				t.Fatalf("%s/%s 同时携带秒价与按次价（互斥门禁：两字段只落其一）", entry.providerID, row.Model)
			}
		}
	}
}

// TestEstimateProviderCostUsdVideoCallDurationTiers 锁定按次时长档结算
// （2026-11-12 档位批）：Hailuo-2.3 官方 768P 6s 2.00 元/条、10s 4.00 元/条
// （platform.minimax.cn，÷7.0）；结算链给出 VideoCallSeconds 时按档位价，
// 不命中档位不回退主档（宁可不计，不按错档收费）。
func TestEstimateProviderCostUsdVideoCallDurationTiers(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		seconds *float64
		want    float64
	}{
		{"Hailuo-2.3 6s", "MiniMax-Hailuo-2.3", audioTestFloat(6), 0.2857142857},
		{"Hailuo-2.3 10s", "MiniMax-Hailuo-2.3", audioTestFloat(10), 0.5714285714},
		{"Hailuo-2.3-Fast 6s", "MiniMax-Hailuo-2.3-Fast", audioTestFloat(6), 0.1928571429},
		{"Hailuo-2.3-Fast 10s", "MiniMax-Hailuo-2.3-Fast", audioTestFloat(10), 0.3214285714},
		{"Hailuo-2.3 无秒数用主档", "MiniMax-Hailuo-2.3", nil, 0.2857142857},
		{"Hailuo-2.3 非档位秒数不计", "MiniMax-Hailuo-2.3", audioTestFloat(8), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := EstimateProviderCostUsd(CostInput{
				ProviderCode:     "minimax",
				Model:            tc.model,
				VideoCalls:       audioTestFloat(1),
				VideoCallSeconds: tc.seconds,
			})
			if cost == nil {
				t.Fatalf("cost = nil want %v", tc.want)
			}
			if *cost != tc.want {
				t.Fatalf("cost = %v want %v", *cost, tc.want)
			}
		})
	}
}

// TestVideoCallTierSeconds 锁定创建时按最近档收敛（等距取小档）：请求值
// 不落在档位上时，出站与计费都用收敛后的档位。
func TestVideoCallTierSeconds(t *testing.T) {
	cases := []struct {
		seconds float64
		want    float64
	}{
		{6, 6}, {7, 6}, {8, 6}, {9, 10}, {10, 10}, {12, 10},
	}
	for _, tc := range cases {
		tier := VideoCallTierSeconds("minimax", "MiniMax-Hailuo-2.3", tc.seconds)
		if tier == nil || *tier != tc.want {
			t.Fatalf("VideoCallTierSeconds(%v) = %v want %v", tc.seconds, tier, tc.want)
		}
	}
	if tier := VideoCallTierSeconds("minimax", "MiniMax-H3", 7); tier != nil {
		t.Fatalf("per-second row must not have call tiers, got %v", *tier)
	}
	if tier := VideoCallTierSeconds("openai", "sora-2", 8); tier != nil {
		t.Fatalf("unknown row must return nil, got %v", *tier)
	}
}

// TestEstimateProviderCostUsdVideoResolutionTiers 锁定秒价分辨率档结算
// （2026-11-12 分辨率档批）：xAI 官方 480p $0.08 / 720p $0.14 / 1080p $0.25
// 每秒；Volcengine/Qwen 为国内人民币 ÷7.0。分辨率已知但无该档 → 不产行项
// （0，不回退主档）；分辨率为空 → 主档价。
func TestEstimateProviderCostUsdVideoResolutionTiers(t *testing.T) {
	cases := []struct {
		name       string
		provider   string
		model      string
		seconds    float64
		resolution string
		want       float64
	}{
		{"xai 1.5 480p", "xai", "grok-imagine-video-1.5", 10, "480p", 0.8},
		{"xai 1.5 720p", "xai", "grok-imagine-video-1.5", 10, "720p", 1.4},
		{"xai 1.5 1080p", "xai", "grok-imagine-video-1.5", 10, "1080p", 2.5},
		{"xai lite 1080p", "xai", "grok-imagine-video-1.5-lite", 10, "1080p", 1.4},
		{"xai 未知档位不计", "xai", "grok-imagine-video-1.5", 10, "2k", 0},
		{"xai 无分辨率用主档", "xai", "grok-imagine-video-1.5", 10, "", 1.4},
		{"volcengine 2.5 1080p", "volcengine", "doubao-seedance-2-5-260628", 5, "1080p", 2.6714285714},
		{"qwen wan3.0 480p", "qwen", "wan3.0-video", 5, "480p", 0.2142857143},
		{"minimax H3 2K", "minimax", "MiniMax-H3", 10, "2k", 1.1428571429},
		{"minimax H3 768P", "minimax", "MiniMax-H3", 10, "768P", 0.7142857143},
		{"minimax H3 未知档位不计", "minimax", "MiniMax-H3", 10, "1080p", 0},
		{"minimax H3 无分辨率用主档", "minimax", "MiniMax-H3", 10, "", 0.7142857143},
		{"minimax H3-Max 480P", "minimax", "MiniMax-H3-Max", 10, "480p", 0.4714285714},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := EstimateProviderCostUsd(CostInput{
				ProviderCode:          tc.provider,
				Model:                 tc.model,
				OutputVideoSeconds:    audioTestFloat(tc.seconds),
				OutputVideoResolution: tc.resolution,
			})
			if cost == nil {
				t.Fatalf("cost = nil want %v", tc.want)
			}
			if *cost != tc.want {
				t.Fatalf("cost = %v want %v", *cost, tc.want)
			}
		})
	}
}

// TestEstimateProviderCostUsdVideoCallResolutionTiers 锁定 Hailuo 2.3 的
// “分辨率 × 时长”按次组合档（2026-11-13）：768P 6s/10s 与 1080P 6s 各自
// 采用官方条价；1080P 无 10s 档 → 不产行项（不回退 768P 主档）。
func TestEstimateProviderCostUsdVideoCallResolutionTiers(t *testing.T) {
	one := 1.0
	six := 6.0
	ten := 10.0
	cases := []struct {
		name       string
		model      string
		seconds    float64
		resolution string
		want       float64
	}{
		{"hailuo 768P 6s", "MiniMax-Hailuo-2.3", six, "768P", 0.2857142857},
		{"hailuo 768P 10s", "MiniMax-Hailuo-2.3", ten, "768P", 0.5714285714},
		{"hailuo 1080P 6s", "MiniMax-Hailuo-2.3", six, "1080P", 0.5},
		{"hailuo 1080P 10s 无档", "MiniMax-Hailuo-2.3", ten, "1080P", 0},
		{"hailuo 无分辨率回主档 6s", "MiniMax-Hailuo-2.3", six, "", 0.2857142857},
		{"hailuo fast 1080P 6s", "MiniMax-Hailuo-2.3-Fast", six, "1080P", 0.33},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := EstimateProviderCostUsd(CostInput{
				ProviderCode:          "minimax",
				Model:                 tc.model,
				VideoCalls:            &one,
				VideoCallSeconds:      &tc.seconds,
				OutputVideoResolution: tc.resolution,
			})
			if tc.want == 0 {
				if cost != nil && *cost != 0 {
					t.Fatalf("cost = %v want 0（无档不回退主档）", *cost)
				}
				return
			}
			if cost == nil || *cost != tc.want {
				t.Fatalf("cost = %v want %v", cost, tc.want)
			}
		})
	}
}
