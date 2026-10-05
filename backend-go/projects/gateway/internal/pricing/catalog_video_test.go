package pricing

// M2 视频静态快照测试（媒体设计 §10 / 任务书 §4）：sora-2 / sora-2-pro 收录
// 进 openai 静态目录（mode=video、协议 ["video"]、OutputModalities
// ["video"]），720p 基准秒价经 PriceSet.VideoOutputUsdPerSecond 呈现；
// 秒价是唯一单价（纯视频模型，hasAnyRate 口径）。行项生成（CostLineKind
// video_output_seconds 的计费引擎消费）由后续终态计费任务交付，此处只锁
// 目录结构。
// 2026-10-05 全厂商补全批：sora-2/sora-2-pro 官方 deprecations.md 明文
// 2026-09-24 关停——目录断言改经 FindProviderModelPricingAsOf 关停日前
// 回溯查询（快照行仍在，断言语义不变）。
import "testing"

// soraPreShutdownAsOf 是 sora-2 系关停日（2026-09-24）前一天的回溯截止。
const soraPreShutdownAsOf = "2026-09-23"

func mustFindSoraPricingForTest(t *testing.T, providerCode, model string) *Pricing {
	t.Helper()
	pricing := FindProviderModelPricingAsOf(providerCode, model, soraPreShutdownAsOf)
	if pricing == nil {
		t.Fatalf("pricing not found: %s/%s (as of %s)", providerCode, model, soraPreShutdownAsOf)
	}
	return pricing
}

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
		got := mustFindSoraPricingForTest(t, "gpt", tc.model)
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
	// 2026-10-05 新事实：现行 lookup（asOf=今天）对关停后的 sora-2 系
	// 返回 nil（deprecations 2026-09-24 关停生效，不泄漏进现役解析）。
	if FindProviderModelPricing("gpt", "sora-2") != nil || FindProviderModelPricing("openai", "sora-2-pro") != nil {
		t.Fatal("shutdown sora-2 rows must not resolve in the live lookup")
	}
}

// TestGlmCogVideoSnapshotPricing 锁定 glm 视频目录行（M3 收录 + 2026-10-05
// 按次计费批落价）：cogvideox-2/3 收录进 glm 静态目录（mode=video、协议
// ["video"]、OutputModalities ["video"]）。官方按次计费（z.ai 国际站
// cogvideox-3 $0.2/video 明文；cogvideox-2 国内 0.5 元/次 ÷ 7.0 = $0.0714
// 换算）落 VideoOutputCostPerCall（2026-10-05 用户裁决：无法返回 usage 的
// 上游按官网计费规则、按输入计）；秒价槽位保持 nil（按次 ≠ 按秒，互斥），
// 不得携带 token/图像价。cogvideox-2 挂 CNY 换算四件套；cogvideox-3 为官方
// USD 明文，只留 SourcePricingNote 不挂换算四件套（同 cogview-4 先例）。
func TestGlmCogVideoSnapshotPricing(t *testing.T) {
	cases := []struct {
		model          string
		usdPerCall     float64
		wantCNYSources bool
	}{
		{"cogvideox-2", 0.0714, true},
		{"cogvideox-3", 0.2, false},
	}
	for _, tc := range cases {
		got := mustFindPricingForTest(t, "glm", tc.model)
		if got.Mode != "video" {
			t.Fatalf("%s mode = %q want video", tc.model, got.Mode)
		}
		if len(got.SupportedAPIProtocols) != 1 || got.SupportedAPIProtocols[0] != "video" {
			t.Fatalf("%s protocols = %v want [video]", tc.model, got.SupportedAPIProtocols)
		}
		if len(got.OutputModalities) != 1 || got.OutputModalities[0] != "video" {
			t.Fatalf("%s outputModalities = %v want [video]", tc.model, got.OutputModalities)
		}
		if len(got.InputModalities) != 2 {
			t.Fatalf("%s inputModalities = %v want [text image]（文生/图生视频）", tc.model, got.InputModalities)
		}
		// 按次计费批落价：VideoOutputCostPerCall 承接官方按次口径。
		if got.VideoOutputUsdPerCall == nil || *got.VideoOutputUsdPerCall != tc.usdPerCall {
			t.Fatalf("%s videoOutputUsdPerCall = %v want %v", tc.model, got.VideoOutputUsdPerCall, tc.usdPerCall)
		}
		// 按次 ≠ 按秒：秒价槽位保持 nil（互斥）。
		if got.VideoOutputUsdPerSecond != nil {
			t.Fatalf("%s videoOutputUsdPerSecond = %v want nil（按次计费不得落秒价槽位）", tc.model, *got.VideoOutputUsdPerSecond)
		}
		if got.InputUsdPer1M != nil || got.OutputUsdPer1M != nil || got.OutputUsdPerImage != nil {
			t.Fatalf("%s must not carry token/image prices (video model)", tc.model)
		}
		// 按次价即单价：hasAnyRate 必须为真（纯视频行不被目录过滤）。
		if !hasAnyRate(got.PriceSet) {
			t.Fatalf("%s must carry a rate（按次计费批落价）", tc.model)
		}
		// 来源元信息：换算行挂 CNY 四件套，官方 USD 明文行只留 SourcePricingNote。
		if tc.wantCNYSources {
			if got.SourcePricingCurrency != "CNY" || got.SourceExchangeRateToUsd == nil || *got.SourceExchangeRateToUsd != 7.0 || got.SourceExchangeRateDate != "2026-10-05" {
				t.Fatalf("%s source meta = %s/%v/%s want CNY/7.0/2026-10-05（0.5 元/次 ÷ 7.0 换算四件套）", tc.model, got.SourcePricingCurrency, got.SourceExchangeRateToUsd, got.SourceExchangeRateDate)
			}
		} else {
			if got.SourcePricingCurrency != "" || got.SourceExchangeRateToUsd != nil || got.SourceExchangeRateDate != "" {
				t.Fatalf("%s source meta = %s/%v/%s want empty（官方 USD 明文不挂换算四件套）", tc.model, got.SourcePricingCurrency, got.SourceExchangeRateToUsd, got.SourceExchangeRateDate)
			}
			if got.SourcePricingNote == "" {
				t.Fatalf("%s sourcePricingNote = \"\" want 官方明文来源留档（z.ai $0.2/video + 国内 1 元/次原句）", tc.model)
			}
		}
	}
}
