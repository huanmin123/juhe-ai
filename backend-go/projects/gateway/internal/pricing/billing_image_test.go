package pricing

// 按张计价图像行计费测试（2026-10-17 实弹取证回填）：seedream/image-01/
// cogview/grok-imagine-image 族目录槽位 OutputUsdPerImage，上游 usage 只报
// output_tokens（图像内部 token 核算，无独立文本输出 token 价）——引擎必须
// 按张出线项计费，不得以「未定价输出 token」拒绝整单（回归前行为：breakdown
// nil → usage 冻结落 0 成本）；也不得把图像内部 token 虚计为文本输出。
import "testing"

func imageTestCount(v float64) *float64 { return &v }

func imageTestFloat(v float64) *float64 { return &v }

func TestBuildCostBreakdownPerImagePricedRowIgnoresImageInternalTokens(t *testing.T) {
	pricingRow := &Pricing{
		ProviderCode: "volcengine",
		Model:        "doubao-seedream-5-0-flash-260915",
		Mode:         "image_generation",
		PriceSet:     PriceSet{OutputUsdPerImage: imageTestFloat(0.01714)},
	}
	breakdown := BuildCostBreakdown(pricingRow, CostInput{
		OutputTokens:     imageTestCount(4096),
		OutputImageCount: imageTestCount(1),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil（按张行 + 图像内部 token 不得被未定价判定拒绝）")
	}
	var unitLine *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == LineImageOutUnit {
			unitLine = &breakdown.LineItems[index]
		}
	}
	if unitLine == nil {
		t.Fatalf("image_output_unit line missing: %+v", breakdown.LineItems)
	}
	if unitLine.Quantity != 1 || unitLine.UnitPriceUsd != 0.01714 || unitLine.CostUsd != 0.01714 {
		t.Fatalf("unit line = %+v", unitLine)
	}
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == LineOutput {
			t.Fatalf("图像内部 token 不得虚计为文本输出行: %+v", breakdown.LineItems[index])
		}
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.01714 {
		t.Fatalf("account charge = %v, want 0.01714", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownPerImagePricedRowWithoutCountNoCharge(t *testing.T) {
	// 张数计量缺失：不虚计（无按张行项），图像内部 token 也不得以未定价拒绝
	// 之外的形态出账——charge 0（计量照落成本不虚计，契约 §2.8）。
	breakdown := BuildCostBreakdown(&Pricing{
		ProviderCode: "volcengine",
		Mode:         "image_generation",
		PriceSet:     PriceSet{OutputUsdPerImage: imageTestFloat(0.01714)},
	}, CostInput{
		OutputTokens: imageTestCount(4096),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil（缺张数应收敛为 0 计费而非 nil 拒绝）")
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("account charge = %v, want 0", breakdown.AccountChargeUsd)
	}
}
