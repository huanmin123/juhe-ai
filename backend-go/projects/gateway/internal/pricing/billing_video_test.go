package pricing

// M2 视频计费行项测试（媒体设计 §10 / 契约 §2.8）：
//   - video_output_seconds：数量 = 输出秒，成本 = seconds × VideoOutputUsdPerSecond；
//   - 无价格或无秒不产行项（失败任务不虚计）；
//   - EstimateProviderCostUsd 对静态快照 sora-2 系可直接估出终态成本
//     （任务终态计费回填链的消费面）。
import "testing"

func TestBuildCostBreakdownVideoOutputSecondsLine(t *testing.T) {
	// sora-2 静态快照：$0.10/s。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "openai", "sora-2"), CostInput{
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
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "openai", "sora-2"), CostInput{
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
	// 终态计费回填消费面：EstimateProviderCostUsd 静态快照直接估出 sora-2
	//（4s × $0.10/s = $0.40）与 sora-2-pro（4s × $0.30/s = $1.20）。
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "openai", Model: "sora-2", OutputVideoSeconds: audioTestFloat(4),
	})
	if cost == nil || *cost != 0.4 {
		t.Fatalf("sora-2 cost = %v want 0.4", cost)
	}
	proCost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "openai", Model: "sora-2-pro", OutputVideoSeconds: audioTestFloat(4),
	})
	if proCost == nil || *proCost != 1.2 {
		t.Fatalf("sora-2-pro cost = %v want 1.2", proCost)
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
		ProviderCode: "openai", Model: "sora-2",
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
