package pricing

// M2 视频静态快照测试（媒体设计 §10 / 任务书 §4）：sora-2 / sora-2-pro 收录
// 进 openai 静态目录（mode=video、协议 ["video"]、OutputModalities
// ["video"]），720p 基准秒价经 PriceSet.VideoOutputUsdPerSecond 呈现；
// 秒价是唯一单价（纯视频模型，hasAnyRate 口径）。行项生成（CostLineKind
// video_output_seconds 的计费引擎消费）由后续终态计费任务交付，此处只锁
// 目录结构。
import "testing"

func TestSoraVideoSnapshotPricing(t *testing.T) {
	cases := []struct {
		model          string
		usdPerSecond   float64
		releaseDate    string
		inputModalitie []string
	}{
		{"sora-2", 0.10, "2025-10-06", []string{"text", "image"}},
		{"sora-2-pro", 0.30, "2025-10-06", []string{"text", "image"}},
	}
	for _, tc := range cases {
		got := mustFindPricingForTest(t, "gpt", tc.model)
		if got.Mode != "video" {
			t.Fatalf("%s mode = %q want video", tc.model, got.Mode)
		}
		if len(got.SupportedAPIProtocols) != 1 || got.SupportedAPIProtocols[0] != "video" {
			t.Fatalf("%s protocols = %v want [video]", tc.model, got.SupportedAPIProtocols)
		}
		if len(got.OutputModalities) != 1 || got.OutputModalities[0] != "video" {
			t.Fatalf("%s outputModalities = %v want [video]", tc.model, got.OutputModalities)
		}
		if len(got.InputModalities) != len(tc.inputModalitie) {
			t.Fatalf("%s inputModalities = %v want %v", tc.model, got.InputModalities, tc.inputModalitie)
		}
		if got.ReleaseDate != tc.releaseDate {
			t.Fatalf("%s releaseDate = %q want %s", tc.model, got.ReleaseDate, tc.releaseDate)
		}
		if got.VideoOutputUsdPerSecond == nil || *got.VideoOutputUsdPerSecond != tc.usdPerSecond {
			t.Fatalf("%s videoOutputUsdPerSecond = %v want %v", tc.model, got.VideoOutputUsdPerSecond, tc.usdPerSecond)
		}
		// 秒价是唯一单价：hasAnyRate 必须为真（否则纯视频模型被目录过滤）。
		if !hasAnyRate(got.PriceSet) {
			t.Fatalf("%s must carry a rate (video second price only)", tc.model)
		}
		if got.InputUsdPer1M != nil || got.OutputUsdPer1M != nil {
			t.Fatalf("%s must not carry token prices (video bills per second)", tc.model)
		}
	}
	// sora-2/image-to-video 模型名含斜杠，按任务裁决不收录（待图生视频接入）。
	if FindProviderModelPricing("gpt", "sora-2/image-to-video") != nil {
		t.Fatal("sora-2/image-to-video must not be in the catalog (slash model name)")
	}
}

// TestGlmCogVideoSnapshotPricing 锁定 glm 视频目录行（M3，契约 §7.1）：
// cogvideox-3 收录进 glm 静态目录（mode=video、协议 ["video"]、
// OutputModalities ["video"]），但不得携带任何单价——CogVideoX 按次计费
// 且官方精确秒价不可查证，glm 轮询响应无时长回报，终态计费走契约 §2.8
// 兜底（0 计费 + usage_missing），不编造秒价。
func TestGlmCogVideoSnapshotPricing(t *testing.T) {
	got := mustFindPricingForTest(t, "glm", "cogvideox-3")
	if got.Mode != "video" {
		t.Fatalf("cogvideox-3 mode = %q want video", got.Mode)
	}
	if len(got.SupportedAPIProtocols) != 1 || got.SupportedAPIProtocols[0] != "video" {
		t.Fatalf("cogvideox-3 protocols = %v want [video]", got.SupportedAPIProtocols)
	}
	if len(got.OutputModalities) != 1 || got.OutputModalities[0] != "video" {
		t.Fatalf("cogvideox-3 outputModalities = %v want [video]", got.OutputModalities)
	}
	if len(got.InputModalities) != 2 {
		t.Fatalf("cogvideox-3 inputModalities = %v want [text image]（文生/图生视频）", got.InputModalities)
	}
	// 不落价：无秒价、无 token 价、无任何档价（usage_missing 兜底口径）。
	if got.VideoOutputUsdPerSecond != nil {
		t.Fatalf("cogvideox-3 must not carry a per-second rate（官方按次计价不可查证，不编造）: %v", *got.VideoOutputUsdPerSecond)
	}
	if got.InputUsdPer1M != nil || got.OutputUsdPer1M != nil || got.OutputUsdPerImage != nil {
		t.Fatalf("cogvideox-3 must not carry token/image prices (video model)")
	}
	if hasAnyRate(got.PriceSet) {
		t.Fatalf("cogvideox-3 must carry no rate at all（按次计价待官方口径回填）: %+v", got.PriceSet)
	}
}
