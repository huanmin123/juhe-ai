package gatewaymedia

// 视频 adapter 实际出站计量换算测试（P1）：计费快照必须按 adapter 真实
// 出站秒数/分辨率档冻结，不能沿用请求原值或标准 size 短边归档。

import "testing"

func effectiveMeteringOf(t *testing.T, provider string, params NormalizedVideoParams) (*float64, string) {
	t.Helper()
	adapter := VideoAdapterForProvider(provider)
	effective, ok := adapter.(VideoEffectiveMeteringProvider)
	if !ok {
		t.Fatalf("%s adapter 未实现 VideoEffectiveMeteringProvider", provider)
	}
	return effective.EffectiveVideoMetering(params)
}

func float64Ptr(value float64) *float64 { return &value }

func TestGlmEffectiveVideoMeteringQuantizesDuration(t *testing.T) {
	tests := []struct {
		request float64
		want    float64
	}{
		{2, 5}, {5, 5}, {8, 10}, {10, 10},
	}
	for _, tc := range tests {
		seconds, tier := effectiveMeteringOf(t, "glm", NormalizedVideoParams{Seconds: float64Ptr(tc.request)})
		if seconds == nil || *seconds != tc.want || tier != "" {
			t.Fatalf("glm request=%vs effective=%v/%q, want %v/empty", tc.request, seconds, tier, tc.want)
		}
	}
}

func TestXAIEffectiveVideoMeteringRoundsDurationAndTier(t *testing.T) {
	seconds, tier := effectiveMeteringOf(t, "xai", NormalizedVideoParams{
		Seconds: float64Ptr(7.6),
		Size:    "1440x1440",
	})
	if seconds == nil || *seconds != 8 {
		t.Fatalf("xai seconds = %v, want 8", seconds)
	}
	// xAI 出站 resolution 最高档为 1080p；1440x1440 不能被标准归档为 1440p。
	if tier != "1080p" {
		t.Fatalf("xai tier = %q, want 1080p", tier)
	}
}

func TestVeoEffectiveVideoMeteringUsesDefaultSecondsAndTwoTiers(t *testing.T) {
	seconds, tier := effectiveMeteringOf(t, "gemini", NormalizedVideoParams{
		Model:   "veo-3.0-generate-preview",
		Seconds: float64Ptr(5), // Veo 请求面不支持 seconds，出站固定默认 8s
		Size:    "1024x1024",
	})
	if seconds == nil || *seconds != veoDefaultDurationSeconds {
		t.Fatalf("veo seconds = %v, want default %v", seconds, veoDefaultDurationSeconds)
	}
	// Veo 两档：短边 1024 < 1080 → 720p（标准归档会误判 1080p）。
	if tier != "720p" {
		t.Fatalf("veo 1024x1024 tier = %q, want 720p", tier)
	}
	if _, tier := effectiveMeteringOf(t, "gemini", NormalizedVideoParams{
		Model: "veo-3.0-generate-preview",
		Size:  "3840x2160",
	}); tier != "1080p" {
		t.Fatalf("veo 3840x2160 tier = %q, want 1080p", tier)
	}
}

func TestVolcengineEffectiveVideoMeteringCapsAt1080p(t *testing.T) {
	for _, size := range []string{"3840x2160", "2560x1440", "1440x1440"} {
		_, tier := effectiveMeteringOf(t, "volcengine", NormalizedVideoParams{Size: size})
		if tier != "1080p" {
			t.Fatalf("volcengine %s tier = %q, want 1080p", size, tier)
		}
	}
	if _, tier := effectiveMeteringOf(t, "volcengine", NormalizedVideoParams{Size: "1280x720"}); tier != "720p" {
		t.Fatalf("volcengine 1280x720 tier = %q, want 720p", tier)
	}
}
