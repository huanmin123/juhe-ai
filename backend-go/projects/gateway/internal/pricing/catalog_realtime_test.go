package pricing

// M5b realtime 静态快照门禁（Realtime 设计 §5 / 契约 §4.4）：gpt-realtime
// 收录进 openai 静态目录（mode=audio、协议 ["realtime"]、双向 text+audio
// 模态），audio token 价 $32/$64 per 1M（来源与不落 text 价/缓存价的依据
// 见 data_openai.go 行内注释）。会话级 usage 终态计费（audio token 行项）
// 由 M5b2 计费面交付，此处只锁目录结构与单价。
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
	// text token 价多源分歧，按「不得编造」不落（data_openai.go 注释）。
	if got.InputUsdPer1M != nil || got.OutputUsdPer1M != nil || got.CachedInputUsdPer1M != nil {
		t.Fatalf("gpt-realtime must not carry unverified text token prices: %+v", got.PriceSet)
	}
	// audio 价是已落单价：hasAnyRate 必须为真（否则纯 audio 行被目录过滤）。
	if !hasAnyRate(got.PriceSet) {
		t.Fatal("gpt-realtime must carry a rate (audio token prices only)")
	}
}
