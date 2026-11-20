package pricing

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// 统一媒体模板（media_unified_v1）行项单测：非对话媒体统一计费设计
// §6.1/§6.2、AC-001/002/006/007/017。

func mediaTestFloat(value float64) *float64 { return &value }

func mediaVideoPricing() *mediapricing.Pricing {
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

func mediaImagePricing() *mediapricing.Pricing {
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

func mediaAudioPricing() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.0002,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}

func mediaCallPricing() *mediapricing.Pricing {
	return &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitCall,
		BaseUsd:         0.3429,
		BaseTier:        "",
		TierMultipliers: map[string]float64{},
	}
}

func TestMediaUnifiedInvalidTemplateRowIsUnpriced(t *testing.T) {
	// AC-016：media_pricing_json 非法时整行按无价处理，即使模板对象与 legacy
	// 价同时在场也不得产出任何行项（不回落旧价、不把非法值当免费）。
	breakdown := BuildCostBreakdown(
		&Pricing{
			ProviderCode:        "minimax",
			Model:               "MiniMax-Hailuo-2.3",
			Mode:                "video",
			MediaPricing:        mediaVideoPricing(),
			MediaPricingInvalid: true,
			PriceSet:            PriceSet{VideoOutputUsdPerSecond: mediaTestFloat(0.05)},
		},
		CostInput{
			ProviderCode:       "minimax",
			Model:              "MiniMax-Hailuo-2.3",
			OutputVideoSeconds: mediaTestFloat(10),
			ResolutionTier:     "720p",
		},
	)
	if breakdown != nil {
		t.Fatalf("非法媒体模板不得产出计费行项: %#v", breakdown)
	}
}

func TestMediaUnifiedVideoSecondsTierAndMultiplier(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3", Mode: "video", MediaPricing: mediaVideoPricing()},
		CostInput{
			ProviderCode:       "minimax",
			Model:              "MiniMax-Hailuo-2.3",
			OutputVideoSeconds: mediaTestFloat(10),
			ResolutionTier:     "1080p",
			QuantitySource:     "upstream",
			// token 计量同时在场时必须被统一模板忽略（AC-007）。
			InputTokens:  mediaTestFloat(1000),
			OutputTokens: mediaTestFloat(2000),
		},
	)
	if breakdown == nil {
		t.Fatal("统一媒体模板应产出 breakdown")
	}
	if breakdown.BillingPolicy != BillingPolicyMediaUnified {
		t.Fatalf("billingPolicy = %s, want %s", breakdown.BillingPolicy, BillingPolicyMediaUnified)
	}
	if len(breakdown.LineItems) != 1 {
		t.Fatalf("lineItems = %d, want 1（不得混产 token 行项）", len(breakdown.LineItems))
	}
	line := breakdown.LineItems[0]
	if line.Key != "video_output_seconds" || line.Kind != CostLineKindVideoOutputSeconds {
		t.Fatalf("line = %s/%s, want video_output_seconds", line.Key, line.Kind)
	}
	if line.Quantity != 10 || line.Unit != LineUnitSecond {
		t.Fatalf("quantity/unit = %v/%s, want 10/second", line.Quantity, line.Unit)
	}
	if line.Tier != "1080p" {
		t.Fatalf("tier = %q, want 1080p", line.Tier)
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

func TestMediaUnifiedVideoCall(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3", Mode: "video", MediaPricing: mediaCallPricing()},
		CostInput{ProviderCode: "minimax", Model: "MiniMax-Hailuo-2.3", VideoCalls: mediaTestFloat(1), QuantitySource: "upstream"},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("call 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "video_output_calls" || line.Kind != CostLineKindVideoOutputCalls || line.Unit != LineUnitCall {
		t.Fatalf("line = %s/%s/%s, want video_output_calls", line.Key, line.Kind, line.Unit)
	}
	if line.Tier != "" {
		t.Fatalf("call tier = %q, want empty", line.Tier)
	}
	if line.Multiplier == nil || *line.Multiplier != 1.0 {
		t.Fatalf("call multiplier = %v, want 1.0", line.Multiplier)
	}
	if line.CostUsd != 0.3429 || breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.3429 {
		t.Fatalf("call cost = %v/%v, want 0.3429", line.CostUsd, breakdown.AccountChargeUsd)
	}
}

func TestMediaUnifiedAudioSeconds(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "volcengine", Model: "seed-audio-1.0", Mode: "audio", MediaPricing: mediaAudioPricing()},
		CostInput{ProviderCode: "volcengine", Model: "seed-audio-1.0", AudioInputSeconds: mediaTestFloat(12.5), QuantitySource: "gateway_parse"},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("audio 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "audio_input_seconds" || line.Kind != CostLineKindAudioInputSeconds {
		t.Fatalf("line = %s/%s, want audio_input_seconds", line.Key, line.Kind)
	}
	if line.Tier != "" || line.Multiplier == nil || *line.Multiplier != 1.0 {
		t.Fatalf("audio tier/multiplier = %q/%v, want empty/1.0", line.Tier, line.Multiplier)
	}
	if line.CostUsd != 0.0025 {
		t.Fatalf("audio cost = %v, want 0.0025", line.CostUsd)
	}
}

func TestMediaUnifiedImage(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "openai", Model: "gpt-image-2", Mode: "image_generation", MediaPricing: mediaImagePricing()},
		CostInput{ProviderCode: "openai", Model: "gpt-image-2", OutputImageCount: mediaTestFloat(2), ResolutionTier: "2k", QuantitySource: "upstream"},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("image 模板应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "image_output_unit" || line.Kind != LineImageOutUnit || line.Unit != LineUnitImage {
		t.Fatalf("line = %s/%s/%s, want image_output_unit", line.Key, line.Kind, line.Unit)
	}
	if line.Tier != "2k" || line.Multiplier == nil || *line.Multiplier != 1.6 {
		t.Fatalf("image tier/multiplier = %q/%v, want 2k/1.6", line.Tier, line.Multiplier)
	}
	if line.CostUsd != 0.128 {
		t.Fatalf("image cost = %v, want 0.128", line.CostUsd)
	}
	if breakdown.OutputUsdPerImage == nil || *breakdown.OutputUsdPerImage != 0.064 {
		t.Fatalf("outputUsdPerImage = %v, want 0.064", breakdown.OutputUsdPerImage)
	}
}

func TestMediaUnifiedImageWithChatProjectedMode(t *testing.T) {
	// Gemini generate_content 图像行的目录 mode 投影为 chat；kind 不可映射时
	// 必须按实际计量维度（OutputImageCount）兜底为 image，不能因 mode=chat
	// 产出 nil 或回落 token 计费。
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "gemini", Model: "gemini-3.1-flash-image", Mode: "chat", MediaPricing: mediaImagePricing()},
		CostInput{ProviderCode: "gemini", Model: "gemini-3.1-flash-image",
			OutputImageCount: mediaTestFloat(1), QuantitySource: "upstream"},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("chat 投影图像行应产出唯一行项: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.Key != "image_output_unit" || line.Kind != LineImageOutUnit {
		t.Fatalf("line = %s/%s, want image_output_unit", line.Key, line.Kind)
	}
	if line.Tier != "1k" || line.Multiplier == nil || *line.Multiplier != 1.0 {
		t.Fatalf("image tier/multiplier = %q/%v, want 1k/1.0", line.Tier, line.Multiplier)
	}
	if line.CostUsd != 0.04 {
		t.Fatalf("image cost = %v, want 0.04", line.CostUsd)
	}
}

func TestMediaUnifiedInvalidTierDoesNotFallback(t *testing.T) {
	// 档位表完整合法，但请求档位不在表中：PriceForTier 必须报错，
	// 不回落 baseTier 猜测（设计 §6.1 第 2 条）。
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "m", Mode: "video", MediaPricing: mediaVideoPricing()},
		CostInput{ProviderCode: "minimax", Model: "m", OutputVideoSeconds: mediaTestFloat(10), ResolutionTier: "800p"},
	)
	if breakdown != nil {
		t.Fatalf("非法档位必须返回 nil，不得回落 baseTier: %#v", breakdown)
	}
}

func TestMediaUnifiedAudioRejectsTier(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "volcengine", Model: "m", Mode: "audio", MediaPricing: mediaAudioPricing()},
		CostInput{ProviderCode: "volcengine", Model: "m", AudioInputSeconds: mediaTestFloat(1), ResolutionTier: "720p"},
	)
	if breakdown != nil {
		t.Fatalf("audio unit=second 带档位必须拒绝: %#v", breakdown)
	}
}

func TestMediaUnifiedFreePriceIsValid(t *testing.T) {
	template := mediaAudioPricing()
	template.BaseUsd = 0
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "volcengine", Model: "free-tts", Mode: "audio", MediaPricing: template},
		CostInput{ProviderCode: "volcengine", Model: "free-tts", AudioInputSeconds: mediaTestFloat(3)},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("免费模板必须是有价（非 nil）: %#v", breakdown)
	}
	line := breakdown.LineItems[0]
	if line.CostUsd != 0 || line.BaseUnitPriceUsd == nil || *line.BaseUnitPriceUsd != 0 {
		t.Fatalf("免费行项 = cost %v base %v, want 0/0", line.CostUsd, line.BaseUnitPriceUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("accountChargeUsd = %v, want 0", breakdown.AccountChargeUsd)
	}
}

func TestMediaUnifiedCostOverride(t *testing.T) {
	override := 0.5
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "m", Mode: "video", MediaPricing: mediaVideoPricing()},
		CostInput{ProviderCode: "minimax", Model: "m", OutputVideoSeconds: mediaTestFloat(10), ResolutionTier: "1080p", CostUsd: &override},
	)
	if breakdown == nil || breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0.5 {
		t.Fatalf("costUsd override 未生效: %#v", breakdown)
	}
	if breakdown.LineItems[0].CostUsd != 1.0 {
		t.Fatalf("行项金额不应被覆盖: %v", breakdown.LineItems[0].CostUsd)
	}
}

func TestMediaUnifiedInputTemplateOverridesCatalog(t *testing.T) {
	frozen := mediaCallPricing()
	frozen.BaseUsd = 1.25
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "m", Mode: "video", MediaPricing: mediaVideoPricing()},
		CostInput{ProviderCode: "minimax", Model: "m", VideoCalls: mediaTestFloat(1), MediaPricing: frozen, HasMediaPricing: true},
	)
	if breakdown == nil || len(breakdown.LineItems) != 1 {
		t.Fatalf("冻结模板覆盖失败: %#v", breakdown)
	}
	if breakdown.LineItems[0].Key != "video_output_calls" || breakdown.LineItems[0].CostUsd != 1.25 {
		t.Fatalf("冻结模板行项 = %#v, want 1.25 按次", breakdown.LineItems[0])
	}
}

func TestMediaUnifiedMissingQuantityProducesNoLine(t *testing.T) {
	breakdown := BuildCostBreakdown(
		&Pricing{ProviderCode: "minimax", Model: "m", Mode: "video", MediaPricing: mediaVideoPricing()},
		CostInput{ProviderCode: "minimax", Model: "m"},
	)
	if breakdown == nil {
		t.Fatal("有效模板 + 缺失数量应返回 0 成本 breakdown，由调用方标 usage_missing")
	}
	if len(breakdown.LineItems) != 0 {
		t.Fatalf("缺失数量不得产行项: %#v", breakdown.LineItems)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("accountChargeUsd = %v, want 0", breakdown.AccountChargeUsd)
	}
}
