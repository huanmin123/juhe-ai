package pricing

// M2 视频计费行项测试（媒体设计 §10 / 契约 §2.8）：
//   - video_output_seconds：数量 = 输出秒，成本 = seconds × VideoOutputUsdPerSecond；
//   - 无价格或无秒不产行项（失败任务不虚计）；
//   - EstimateProviderCostUsd 对静态快照 sora-2 系可直接估出终态成本
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
}
