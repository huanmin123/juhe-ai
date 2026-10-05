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
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "openai", "gpt-4o"), CostInput{
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
	// 现役视频行的估算消费面由 MiniMax-H3 承接（$0.08/s，4s = $0.32），
	// 语义与原 sora-2（4s × $0.10/s = $0.40）钉值一致：静态快照直接估出
	// 终态成本（任务终态计费回填链的消费面）。
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-H3", OutputVideoSeconds: audioTestFloat(4),
	})
	if cost == nil || *cost != 0.32 {
		t.Fatalf("MiniMax-H3 cost = %v want 0.32", cost)
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
// 计费批，契约 §2.8 按次计费）：cogvideox-3 静态快照 $0.2/次（z.ai 官方
// USD 明文，按次计费每条=每次）；一次任务 = 一次调用（结算链按任务粒度填
// VideoCalls=1），行项 video_output_calls 直乘。
func TestBuildCostBreakdownVideoOutputCallsLine(t *testing.T) {
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "glm", "cogvideox-3"), CostInput{
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
	if line.UnitPriceUsd != 0.2 {
		t.Fatalf("unit price = %v want 0.2", line.UnitPriceUsd)
	}
	if line.CostUsd != 0.2 {
		t.Fatalf("cost = %v want 0.2", line.CostUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.2 {
		t.Fatalf("account charge = %v want 0.2", breakdown.AccountChargeUsd)
	}
	// 次价行无秒价槽位（互斥）：秒计量在场也不产生秒行项。
	withoutSeconds := BuildCostBreakdown(mustFindPricingForTest(t, "glm", "cogvideox-3"), CostInput{
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
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "glm", "cogvideox-3"), CostInput{
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
	// cogvideox-2：国内 0.5 元/次 ÷ 7.0 = $0.0714（快照字面量位精确）。
	cogvideox2 := EstimateProviderCostUsd(CostInput{
		ProviderCode: "glm", Model: "cogvideox-2", VideoCalls: audioTestFloat(1),
	})
	if cogvideox2 == nil || *cogvideox2 != 0.0714 {
		t.Fatalf("cogvideox-2 cost = %v want 0.0714", cogvideox2)
	}
	// MiniMax-Hailuo-2.3：国际站按条明文主档 768P-6s $0.28/条。
	hailuo := EstimateProviderCostUsd(CostInput{
		ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3", VideoCalls: audioTestFloat(1),
	})
	if hailuo == nil || *hailuo != 0.28 {
		t.Fatalf("MiniMax-Hailuo-2.3 cost = %v want 0.28", hailuo)
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
