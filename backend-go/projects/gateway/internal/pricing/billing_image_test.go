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

func TestBuildCostBreakdownTokenPricedImageRowBillsImageTokens(t *testing.T) {
	// gpt-image 族（2026-11-06 深挖复审 F1）：目录只有 outputCostPerImageToken，
	// 上游 images 响应根带 data[]（张数推导=1）且 usage 报 image tokens。按张价
	// 缺失不得把整单判成 nil；应走 image token 行项计费。
	breakdown := BuildCostBreakdown(&Pricing{
		ProviderCode: "gpt",
		Model:        "gpt-image-2",
		Mode:         "image_generation",
		PriceSet:     PriceSet{ImageOutputUsdPer1M: imageTestFloat(30)},
	}, CostInput{
		OutputTokens:     imageTestCount(343),
		OutputImageCount: imageTestCount(1),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil（token 价图像行 + data[] 张数不得被判为未定价）")
	}
	var imageLine *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == LineImageOutput {
			imageLine = &breakdown.LineItems[index]
		}
		if breakdown.LineItems[index].Kind == LineImageOutUnit {
			t.Fatalf("无按张价时不得出按张行: %+v", breakdown.LineItems[index])
		}
	}
	if imageLine == nil || imageLine.Quantity != 343 {
		t.Fatalf("image_output line = %+v", imageLine)
	}
	want := 343.0 * 30 / 1_000_000
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != want {
		t.Fatalf("account charge = %v, want %v", breakdown.AccountChargeUsd, want)
	}
}

// TestEstimateProviderCostUsdImagePixelTiers 锁定按输出像素分档的按张价
// （2026-11-12 档位批）：Seedream 5.0 Pro 官方 ≤2.61M 像素 0.30 元/张、
// >2.61M 0.60 元/张（÷7.0）；请求未带 size 时回落主档价。
func TestEstimateProviderCostUsdImagePixelTiers(t *testing.T) {
	smallPixels := int64(1024 * 1024)
	largePixels := int64(2048 * 2048)
	small := EstimateProviderCostUsd(CostInput{
		ProviderCode: "volcengine", Model: "doubao-seedream-5-0-pro-260628",
		OutputImageCount: audioTestFloat(1), OutputImagePixels: &smallPixels,
	})
	if small == nil || *small != 0.0428571429 {
		t.Fatalf("small image cost = %v want 0.0428571429（0.30/7）", small)
	}
	large := EstimateProviderCostUsd(CostInput{
		ProviderCode: "volcengine", Model: "doubao-seedream-5-0-pro-260628",
		OutputImageCount: audioTestFloat(1), OutputImagePixels: &largePixels,
	})
	if large == nil || *large != 0.0857142857 {
		t.Fatalf("large image cost = %v want 0.0857142857（0.60/7）", large)
	}
	noPixels := EstimateProviderCostUsd(CostInput{
		ProviderCode: "volcengine", Model: "doubao-seedream-5-0-pro-260628",
		OutputImageCount: audioTestFloat(1),
	})
	if noPixels == nil || *noPixels != 0.04286 {
		t.Fatalf("no-size image cost = %v want 0.04286（main tier）", noPixels)
	}
}

// TestEstimateProviderCostUsdImageInputImages 锁定输入图按张价与免费额度
// （2026-11-12 输入素材批）：Seedream 5.0 Pro 首张免费、后续 0.02 元/张
// （÷7.0）；只传 1 张不产生输入图行项。
func TestEstimateProviderCostUsdImageInputImages(t *testing.T) {
	two := 2.0
	one := 1.0
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "volcengine", Model: "doubao-seedream-5-0-pro-260628",
		InputImageCount: &two,
	})
	if cost == nil || *cost != 0.0028571429 {
		t.Fatalf("2 input images cost = %v want 0.0028571429（1 张计费 × 0.02/7）", cost)
	}
	free := EstimateProviderCostUsd(CostInput{
		ProviderCode: "volcengine", Model: "doubao-seedream-5-0-pro-260628",
		InputImageCount: &one,
	})
	if free == nil || *free != 0 {
		t.Fatalf("1 input image must be free, cost = %v want 0", free)
	}
}

// TestEstimateProviderCostUsdQwenImageInputImages 锁定 qwen-image-3.0 输入图
// 0.02 元/张（÷7.0，无每请求免费张数；官方免费额度是账号级“输入输出共
// 10 张”，不进单请求计费）：输出按张 + 输入图按张同时成行。
func TestEstimateProviderCostUsdQwenImageInputImages(t *testing.T) {
	one := 1.0
	inputOnly := EstimateProviderCostUsd(CostInput{
		ProviderCode: "qwen", Model: "qwen-image-3.0",
		InputImageCount: &one,
	})
	if inputOnly == nil || *inputOnly != 0.0028571429 {
		t.Fatalf("qwen 1 input image cost = %v want 0.0028571429", inputOnly)
	}
	combined := EstimateProviderCostUsd(CostInput{
		ProviderCode: "qwen", Model: "qwen-image-3.0",
		InputImageCount:  &one,
		OutputImageCount: &one,
	})
	if combined == nil || *combined != 0.0285714286 {
		t.Fatalf("qwen output+input cost = %v want 0.0285714286", combined)
	}
}

// TestEstimateProviderCostUsdQwenImageProPixelTiers 锁定 qwen-image-3.0-pro
// 1K/2K 输出档（2026-11-13）：≤1024×1024 按 1K 0.25 元/张，>1024×1024 按
// 2K 0.50 元/张（÷7.0）；输入图 0.02 元/张另计。
func TestEstimateProviderCostUsdQwenImageProPixelTiers(t *testing.T) {
	one := 1.0
	small := int64(1024 * 1024)
	large := int64(2048 * 2048)
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode: "qwen", Model: "qwen-image-3.0-pro",
		OutputImageCount: &one, OutputImagePixels: &small, InputImageCount: &one,
	})
	if cost == nil || *cost != 0.0385714286 {
		t.Fatalf("qwen pro 1K + 1 input = %v want 0.0385714286", cost)
	}
	cost = EstimateProviderCostUsd(CostInput{
		ProviderCode: "qwen", Model: "qwen-image-3.0-pro",
		OutputImageCount: &one, OutputImagePixels: &large, InputImageCount: &one,
	})
	if cost == nil || *cost != 0.0742857143 {
		t.Fatalf("qwen pro 2K + 1 input = %v want 0.0742857143", cost)
	}
	justAbove := int64(1024*1024 + 1)
	cost = EstimateProviderCostUsd(CostInput{
		ProviderCode: "qwen", Model: "qwen-image-3.0-pro",
		OutputImageCount: &one, OutputImagePixels: &justAbove,
	})
	if cost == nil || *cost != 0.0714285714 {
		t.Fatalf("qwen pro >1K 像素应落 2K 档: %v want 0.0714285714", cost)
	}
}
