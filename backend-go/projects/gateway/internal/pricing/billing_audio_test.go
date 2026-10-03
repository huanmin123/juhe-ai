package pricing

// M1 同步音频计费行项测试（音频设计 §10 / 契约 §2.8）：
//   - tts_input_chars：数量 = 请求字符数，成本 = chars/1M × TtsInputUsdPer1MChars；
//   - audio_input_seconds：数量 = 秒，成本 = seconds × AudioInputUsdPerSecond；
//   - 无价格或无计量不产行项（0 计费不产生行）。
import "testing"

func audioTestFloat(v float64) *float64 { return &v }

func TestBuildCostBreakdownTtsInputCharsLine(t *testing.T) {
	// tts-1 静态快照：$15.00/1M chars，无 token 单价。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "gpt", "tts-1"), CostInput{
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
	if line.UnitPriceUsd != 15 {
		t.Fatalf("unit price = %v want 15", line.UnitPriceUsd)
	}
	if line.CostUsd != 30 {
		t.Fatalf("cost = %v want 30", line.CostUsd)
	}
	if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != 30 {
		t.Fatalf("account charge = %v want 30", breakdown.AccountChargeUsd)
	}
}

func TestBuildCostBreakdownTtsInputCharsNoMeteringNoLine(t *testing.T) {
	// 无计量（nil）：纯 TTS 模型也不产生行项，charge 回落 0。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "gpt", "tts-1"), CostInput{
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
	// whisper-1 静态快照：$0.006/min 折算 $0.0001/s。
	breakdown := BuildCostBreakdown(mustFindPricingForTest(t, "gpt", "whisper-1"), CostInput{
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
	// 60s × $0.0001/s = $0.006（官方每分钟价回转无损）。
	if line.CostUsd != 0.006 {
		t.Fatalf("cost = %v want 0.006", line.CostUsd)
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
