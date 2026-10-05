package pricing

// M5b realtime 静态快照门禁（Realtime 设计 §5 / 契约 §4.4）：gpt-realtime
// 收录进 openai 静态目录（mode=audio、协议 ["realtime"]、双向 text+audio
// 模态）。audio token 价 $32/$64 per 1M；2026-10-05 遗留取证批以
// pricing.md 官方表为唯一真值补齐 text $4/$16、模态无关 cached $0.40
//（text/audio cached 同值）与 image 输入 $5.00——原「text 价多源分歧
// 不落」口径废止（依据见 data_openai.go 行内注释）。会话级 usage 终态
// 计费（audio token 行项）由 M5b2 计费面交付，此处锁目录结构与单价。
import "testing"

func TestGptRealtimeSnapshotPricing(t *testing.T) {
	got := mustFindPricingForTest(t, "gpt", "gpt-realtime")
	if got.Mode != "audio" {
		t.Fatalf("gpt-realtime mode = %q want audio", got.Mode)
	}
	if len(got.SupportedAPIProtocols) != 1 || got.SupportedAPIProtocols[0] != "realtime" {
		t.Fatalf("gpt-realtime protocols = %v want [realtime]", got.SupportedAPIProtocols)
	}
	if got.ReleaseDate != "2025-08-28" {
		t.Fatalf("gpt-realtime releaseDate = %q want 2025-08-28 (Realtime API GA)", got.ReleaseDate)
	}
	for _, pair := range []struct {
		name  string
		want  float64
		value *float64
	}{
		{"audioInputUsdPer1M", 32, got.AudioInputUsdPer1M},
		{"audioOutputUsdPer1M", 64, got.AudioOutputUsdPer1M},
	} {
		if pair.value == nil || *pair.value != pair.want {
			t.Fatalf("gpt-realtime %s = %v want %v", pair.name, pair.value, pair.want)
		}
	}
	// text/cached/image 价已按 pricing.md 官方表明文落值（遗留取证批：
	// text $4/$16、cached $0.40 同值、image $5.00），逐项钉住防止回退为
	// 「不落」旧口径。
	for _, pair := range []struct {
		name  string
		want  float64
		value *float64
	}{
		{"inputUsdPer1M", 4, got.InputUsdPer1M},
		{"outputUsdPer1M", 16, got.OutputUsdPer1M},
		{"cachedInputUsdPer1M", 0.40, got.CachedInputUsdPer1M},
		{"imageInputUsdPer1M", 5, got.ImageInputUsdPer1M},
	} {
		if pair.value == nil || *pair.value != pair.want {
			t.Fatalf("gpt-realtime %s = %v want %v（pricing.md 官方明文）", pair.name, pair.value, pair.want)
		}
	}
	// 模态无关 cached 通道只落 text/audio 同值 $0.40；image cached $0.50
	// 与之异值，目录无 per-modality cached 通道，不得误落。
	if got.ImageOutputUsdPer1M != nil || got.OutputUsdPerImage != nil {
		t.Fatalf("gpt-realtime 不应有 image 输出价: %+v", got.PriceSet)
	}
	// audio 价是已落单价：hasAnyRate 必须为真（否则纯 audio 行被目录过滤）。
	if !hasAnyRate(got.PriceSet) {
		t.Fatal("gpt-realtime must carry a rate (audio token prices only)")
	}
}
