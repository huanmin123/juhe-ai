package pricing

// M1 同步音频计费行项测试（音频设计 §10 / 契约 §2.8）：
//   - tts_input_chars：数量 = 请求字符数，成本 = chars/1M × TtsInputUsdPer1MChars；
//   - audio_input_seconds：数量 = 秒，成本 = seconds × AudioInputUsdPerSecond；
//   - 无价格或无计量不产行项（0 计费不产生行）。
import "testing"

func audioTestFloat(v float64) *float64 { return &v }

func TestBuildCostBreakdownTtsInputCharsLine(t *testing.T) {
	// minimax speech-2.8-turbo（2026-10-17 国内口径批）：2.00 元/万字符 ÷7.0
	// = $28.5714/1M chars，无 token 单价（tts-1 等旧行已按 2025 前模型清理批移除）。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "minimax", "speech-2.8-turbo"), CostInput{
		TtsInputChars: audioTestFloat(2_000_000),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	var line *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == CostLineKindTtsInputChars {
			line = &breakdown.LineItems[index]
		}
	}
	if line == nil {
		t.Fatalf("tts_input_chars line missing: %+v", breakdown.LineItems)
	}
	if line.Quantity != 2_000_000 || line.Unit != LineUnitChar || line.UnitSize != 1_000_000 {
		t.Fatalf("line = %+v", line)
	}
	if line.UnitPriceUsd != 28.57142857 {
		t.Fatalf("unit price = %v want 28.57142857", line.UnitPriceUsd)
	}
	if line.CostUsd != 57.14285714 {
		t.Fatalf("cost = %v want 57.14285714（2M chars × $28.57142857/1M）", line.CostUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 57.14285714 {
		t.Fatalf("account charge = %v want 57.14285714", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownTtsInputCharsNoMeteringNoLine(t *testing.T) {
	// 无计量（nil）：纯 TTS 模型也不产生行项，charge 回落 0。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "minimax", "speech-2.8-turbo"), CostInput{
		TtsInputChars: audioTestFloat(0),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	for _, item := range breakdown.LineItems {
		if item.Kind == CostLineKindTtsInputChars {
			t.Fatalf("zero metering must not emit a line: %+v", breakdown.LineItems)
		}
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 0 {
		t.Fatalf("account charge = %v want 0", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownTtsNoPriceNoLine(t *testing.T) {
	// 无字符价的模型（gpt-4o-mini-tts 按 token 计价）携带字符计量：
	// 不产行项、不虚计。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "gpt", "gpt-4o-mini-tts"), CostInput{
		TtsInputChars: audioTestFloat(1000),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	for _, item := range breakdown.LineItems {
		if item.Kind == CostLineKindTtsInputChars {
			t.Fatalf("unpriced metering must not emit a line: %+v", breakdown.LineItems)
		}
	}
}

func TestBuildCostBreakdownAudioInputSecondsLine(t *testing.T) {
	// qwen3-asr-flash（2026-10-17 国内口径批）：0.00022 元/秒 ÷7.0 折秒
	// （minimax asr-1.0 已随 2025 前模型清理批移除）。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "qwen", "qwen3-asr-flash"), CostInput{
		AudioInputSeconds: audioTestFloat(60),
	})
	if breakdown == nil {
		t.Fatal("breakdown = nil")
	}
	var line *CostLineItem
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == CostLineKindAudioInputSeconds {
			line = &breakdown.LineItems[index]
		}
	}
	if line == nil {
		t.Fatalf("audio_input_seconds line missing: %+v", breakdown.LineItems)
	}
	if line.Quantity != 60 || line.Unit != LineUnitSecond || line.UnitSize != 1 {
		t.Fatalf("line = %+v", line)
	}
	// 60s × $3.1429e-05/s = $0.0018857143（roundCost）。
	if line.CostUsd != 0.0018857143 {
		t.Fatalf("cost = %v want 0.0018857143", line.CostUsd)
	}
}

func TestHasAnyCostDimensionAudioMetering(t *testing.T) {
	input := CostInput{TtsInputChars: audioTestFloat(1)}
	if !hasAnyCostDimension(input) {
		t.Fatal("TtsInputChars alone must count as a cost dimension")
	}
	if !hasAnyCostDimension(CostInput{AudioInputSeconds: audioTestFloat(1)}) {
		t.Fatal("AudioInputSeconds alone must count as a cost dimension")
	}
}

// mustFindPricingForTest 经静态快照解析模型（providerToken 按 data.go 注册表）。
func mustFindPricingForTest(t *testing.T, providerCode, model string) *Pricing {
	t.Helper()
	pricing := FindProviderModelPricing(providerCode, model)
	if pricing == nil {
		t.Fatalf("pricing not found: %s/%s", providerCode, model)
	}
	return pricing
}
