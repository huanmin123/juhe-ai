package usagepricing

import (
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

// 统一媒体模板（media_unified_v1）在 usagepricing 落库冻结路径的同源单测：
// 非对话媒体统一计费设计 §6.1/§6.2、AC-001/002/003/006/007/017/018。
// 与 gateway/internal/pricing/billing_media_unified_test.go 使用同一组
// 价格字面量与预期值（AC-015 黄金对齐）。

func usagepricingFloat(value float64) *float64 { return &value }
func usagepricingInt(value int) *int           { return &value }

func usagepricingVideoTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:  1,
		Unit:     mediapricing.UnitSecond,
		BaseUsd:  0.05,
		BaseTier: mediapricing.VideoTier720P,
		TierMultipliers: map[string]float64{
			"480p": 0.5, "720p": 1.0, "1080p": 2.0, "1440p": 3.0, "2160p": 4.0,
		},
	}
}

func usagepricingImageTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:  1,
		Unit:     mediapricing.UnitImage,
		BaseUsd:  0.04,
		BaseTier: mediapricing.ImageTier1K,
		TierMultipliers: map[string]float64{
			"1k": 1.0, "2k": 1.6, "4k": 3.0,
		},
	}
}

func usagepricingAudioTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.0002,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}

func usagepricingCallTemplate() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitCall,
		BaseUsd:         0.3429,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}

func usagepricingMediaRow(mode string, template *mediapricing.Pricing) usageCatalogPricingRow {
	return usageCatalogPricingRow{
		model:           "media-model",
		mode:            mode,
		mediaPricing:    template,
		hasMediaPricing: true,
	}
}

func TestUsageCatalogMediaVideoSeconds(t *testing.T) {
	row := usagepricingMediaRow("video", usagepricingVideoTemplate())
	breakdown := buildUsageCatalogCostBreakdown(row, "minimax", "", usagewriter.UsageRecordInput{
		OutputVideoSeconds: usagepricingFloat(10),
		ResolutionTier:     "1080p",
		QuantitySource:     "upstream",
		// AC-007：token 计量在场也不得混产 legacy 行项。
		InputTokens:  usagepricingInt(1000),
		OutputTokens: usagepricingInt(2000),
	})
	if breakdown == nil {
		t.Fatal("媒体模板应产出 breakdown")
	}
	if breakdown.BillingPolicy != usageCatalogMediaPolicy {
		t.Fatalf("billingPolicy = %s, want %s", breakdown.BillingPolicy, usageCatalogMediaPolicy)
	}
	if len(breakdown.LineItems) != 1 {
		t.Fatalf("lineItems = %d, want 1", len(breakdown.LineItems))
	}
	line := breakdown.LineItems[0]
	if line.Key != "video_output_seconds" || line.Unit != "second" {
		t.Fatalf("line = %s/%s, want video_output_seconds/second", line.Key, line.Unit)
	}
	if line.Quantity != 10 || line.Tier != "1080p" {
		t.Fatalf("quantity/tier = %v/%q, want 10/1080p", line.Quantity, line.Tier)
	}
	if line.Multiplier == nil || *line.Multiplier != 2.0 {
		t.Fatalf("multiplier = %v, want 2.0", line.Multiplier)
	}
	if line.BaseUnitPriceUsd == nil || *line.BaseUnitPriceUsd != 0.05 {
		t.Fatalf("baseUnitPriceUsd = %v, want 0.05", line.BaseUnitPriceUsd)
	}
	if line.QuantitySource != "upstream" {
		t.Fatalf("quantitySource = %q, want upstream", line.QuantitySource)
	}
	if line.CostUsd != 1.0 || breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 1.0 {
		t.Fatalf("cost = %v/%v, want 1.0", line.CostUsd, breakdown.AccountChargeUsd)
	}
}

func TestUsageCatalogMediaVideoCall(t *testing.T) {
	row := usagepricingMediaRow("video", usagepricingCallTemplate())
	breakdown := buildUsageCatalogCostBreakdown(row, "minimax", "", usagewriter.UsageRecordInput{
		VideoCalls:     usagepricingFloat(1),
		QuantitySource: "upstream",
	})
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("call 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "video_output_calls" || line.Unit != "call" || line.Tier != "" {
		t.Fatalf("line = %s/%s tier=%q, want video_output_calls/call/empty", line.Key, line.Unit, line.Tier)
	}
	if line.Multiplier == nil || *line.Multiplier != 1.0 || line.CostUsd != 0.3429 {
		t.Fatalf("call multiplier/cost = %v/%v, want 1.0/0.3429", line.Multiplier, line.CostUsd)
	}
}

func TestUsageCatalogMediaAudioSeconds(t *testing.T) {
	row := usagepricingMediaRow("audio", usagepricingAudioTemplate())
	breakdown := buildUsageCatalogCostBreakdown(row, "volcengine", "", usagewriter.UsageRecordInput{
		AudioInputSeconds: usagepricingFloat(12.5),
		QuantitySource:    "gateway_parse",
	})
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("audio 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "audio_input_seconds" || line.Tier != "" || line.QuantitySource != "gateway_parse" {
		t.Fatalf("line = %#v", line)
	}
	if line.CostUsd != 0.0025 {
		t.Fatalf("audio cost = %v, want 0.0025", line.CostUsd)
	}
}

func TestUsageCatalogMediaImage(t *testing.T) {
	row := usagepricingMediaRow("image_generation", usagepricingImageTemplate())
	breakdown := buildUsageCatalogCostBreakdown(row, "openai", "", usagewriter.UsageRecordInput{
		OutputImageCount: usagepricingInt(2),
		ResolutionTier:   "2k",
		QuantitySource:   "upstream",
	})
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("image 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "image_output_unit" || line.Unit != "image" || line.Tier != "2k" {
		t.Fatalf("line = %#v, want image_output_unit/2k", line)
	}
	if line.Multiplier == nil || *line.Multiplier != 1.6 || line.CostUsd != 0.128 {
		t.Fatalf("image multiplier/cost = %v/%v, want 1.6/0.128", line.Multiplier, line.CostUsd)
	}
	if breakdown.OutputUsdPerImage == nil || *breakdown.OutputUsdPerImage != 0.064 {
		t.Fatalf("outputUsdPerImage = %v, want 0.064", breakdown.OutputUsdPerImage)
	}
}

func TestUsageCatalogMediaInvalidJSONIsUnpriced(t *testing.T) {
	var row usageCatalogPricingRow
	applyUsageCatalogMediaPricing(&row, sql.NullString{String: `{"version":1,"unit":"second"`, Valid: true})
	if !row.mediaPricingInvalid || row.hasMediaPricing {
		t.Fatalf("非法 JSON 必须标记 invalid 且不得视为有价: %#v", row)
	}
	// 即使 legacy 价存在，非法 media_pricing_json 也不回落 legacy（AC-016）。
	row.mode = "video"
	row.inputUsdPer1M = usagepricingFloat(1)
	if breakdown := buildUsageCatalogCostBreakdown(row, "openai", "", usagewriter.UsageRecordInput{
		OutputVideoSeconds: usagepricingFloat(1),
	}); breakdown != nil {
		t.Fatalf("非法 media_pricing_json 不得回落 legacy: %#v", breakdown)
	}
}

func TestUsageCatalogMediaModeMismatchIsUnpriced(t *testing.T) {
	row := usageCatalogPricingRow{mode: "video"}
	applyUsageCatalogMediaPricing(&row, sql.NullString{
		String: `{"version":1,"unit":"image","baseUsd":0.04,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`,
		Valid:  true,
	})
	if row.hasMediaPricing || !row.mediaPricingInvalid {
		t.Fatalf("video 行 + image 模板必须标记无价/非法: %#v", row)
	}
}

func TestUsageCatalogMediaNonMediaModeKeepsPricing(t *testing.T) {
	// chat/responses/embedding/空 mode 不参与媒体 kind 推断：Gemini
	// generate_content 图像行与无 endpoint modes 的历史/迁移行必须保留模板。
	for _, mode := range []string{"chat", "responses", "embedding", ""} {
		row := usageCatalogPricingRow{mode: mode}
		applyUsageCatalogMediaPricing(&row, sql.NullString{
			String: `{"version":1,"unit":"image","baseUsd":0.04,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`,
			Valid:  true,
		})
		if !row.hasMediaPricing || row.mediaPricingInvalid {
			t.Fatalf("mode %q + image 模板应保留为有效媒体价: %#v", mode, row)
		}
	}
}

func TestUsageCatalogMediaFreeZeroPriceIsPriced(t *testing.T) {
	template := usagepricingAudioTemplate()
	template.BaseUsd = 0
	row := usagepricingMediaRow("audio", template)
	if !usageCatalogRowHasAnyRate(row) {
		t.Fatal("免费 0 价媒体行必须被识别为有价")
	}
	breakdown := buildUsageCatalogCostBreakdown(row, "volcengine", "", usagewriter.UsageRecordInput{
		AudioInputSeconds: usagepricingFloat(3),
	})
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("免费模板必须产出有效 breakdown: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.CostUsd != 0 || line.BaseUnitPriceUsd == nil || *line.BaseUnitPriceUsd != 0 {
		t.Fatalf("免费行项 = %#v, want cost/base 0", line)
	}
}

func TestUsageCatalogMediaInvalidTierDoesNotFallback(t *testing.T) {
	row := usagepricingMediaRow("video", usagepricingVideoTemplate())
	if breakdown := buildUsageCatalogCostBreakdown(row, "minimax", "", usagewriter.UsageRecordInput{
		OutputVideoSeconds: usagepricingFloat(10),
		ResolutionTier:     "800p",
	}); breakdown != nil {
		t.Fatalf("非法档位必须返回 nil: %#v", breakdown)
	}
}
