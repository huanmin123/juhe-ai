package providers

// 目录展示完整性门禁（计划-20261005T230500000Z 步骤 6 的轻量替代）：
// 逐 mode × 价组合断言 buildProviderCatalogDisplay 的必需 section 矩阵，
// 钉住「投影缺场景分支」类回归（实证：glm 投影曾缺 image 分支导致
// glm-image/cogview-4 每张价整行不显示；USD 行曾无来源披露出口）。
// 全量 220 行的逐行断言由 catalog-lint（pricing 层）与 UI/API 验收覆盖，
// 此处用合成行锁规则本身。
import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

func sectionKeysOf(item *ModelCatalogItem) map[string]bool {
	keys := map[string]bool{}
	for _, section := range buildProviderCatalogDisplay(item) {
		keys[section.Key] = true
	}
	return keys
}

func catalogItemOfMode(mode string) *ModelCatalogItem {
	return &ModelCatalogItem{ProviderCode: "glm", Model: "lint-probe", Mode: &mode}
}

func TestCatalogDisplayRequiredSectionsByMode(t *testing.T) {
	imageMode := "image_generation"
	imageLegacy := "image"
	audioMode := "audio"
	videoMode := "video"
	chatMode := "chat"
	price := 1.0

	cases := []struct {
		name     string
		item     *ModelCatalogItem
		mustHave []string
	}{
		{
			name:     "image 行带每张价必须渲染 image_generation section（glm 缺分支回归钉）",
			item:     func() *ModelCatalogItem { it := catalogItemOfMode(imageMode); it.OutputUsdPerImage = &price; return it }(),
			mustHave: []string{"image_generation"},
		},
		{
			name: "image 旧词表行同规则（双轨兼容）",
			item: func() *ModelCatalogItem {
				it := catalogItemOfMode(imageLegacy)
				it.OutputUsdPerImage = &price
				return it
			}(),
			mustHave: []string{"image_generation"},
		},
		{
			name: "video 行带秒价必须渲染 media_pricing",
			item: func() *ModelCatalogItem {
				it := catalogItemOfMode(videoMode)
				it.VideoOutputUsdPerSecond = &price
				return it
			}(),
			mustHave: []string{"media_pricing"},
		},
		{
			name: "video 行带按次价必须渲染 media_pricing",
			item: func() *ModelCatalogItem {
				it := catalogItemOfMode(videoMode)
				it.VideoOutputUsdPerCall = &price
				return it
			}(),
			mustHave: []string{"media_pricing"},
		},
		{
			name: "audio 行带 ASR 秒价必须渲染 media_pricing",
			item: func() *ModelCatalogItem {
				it := catalogItemOfMode(audioMode)
				it.AudioInputUsdPerSecond = &price
				return it
			}(),
			mustHave: []string{"media_pricing"},
		},
		{
			name: "audio 行带 TTS 字符价必须渲染 media_pricing",
			item: func() *ModelCatalogItem {
				it := catalogItemOfMode(audioMode)
				it.TtsInputUsdPer1MChars = &price
				return it
			}(),
			mustHave: []string{"media_pricing"},
		},
		{
			name:     "chat 行带 token 价必须渲染 token_pricing",
			item:     func() *ModelCatalogItem { it := catalogItemOfMode(chatMode); it.InputUsdPer1M = &price; return it }(),
			mustHave: []string{"token_pricing"},
		},
	}
	for _, tc := range cases {
		keys := sectionKeysOf(tc.item)
		for _, must := range tc.mustHave {
			if !keys[must] {
				t.Errorf("%s: 缺必需 section %q（实际 keys %v）", tc.name, must, keys)
			}
		}
	}
}

// TestCatalogDisplaySourceDisclosureNotGenerated 钉住披露 section 停生成契约
// （供应商独立计费与模型目录展示设计 §3.2）：系统统一美元计价，币种/汇率/
// 来源文本不进客户可见目录；CNY 行、USD 行、未声明币种行都不再渲染
// currency_conversion / source_disclosure，原始来源留档仍在 catalogdata
// JSON 的 sourcePricingNote 字段。
func TestCatalogDisplaySourceDisclosureNotGenerated(t *testing.T) {
	cases := []struct {
		name string
		item ModelCatalogItem
	}{
		{
			name: "CNY 行",
			item: ModelCatalogItem{
				ProviderCode: "volcengine", SourcePricingCurrency: "CNY",
				SourceExchangeRateToUsd: ptrFloat64(7.0), SourceExchangeRateDate: "2026-10-05",
				SourcePricingNote: "官方人民币价",
			},
		},
		{
			name: "USD 行",
			item: ModelCatalogItem{
				ProviderCode: "glm", SourcePricingCurrency: "USD",
				SourcePricingNote: "官方国际站 USD 明文价",
			},
		},
		{
			name: "未声明币种但有 note 的行",
			item: ModelCatalogItem{
				ProviderCode: "unknown", SourcePricingNote: "官方明文价",
			},
		},
	}
	for _, tc := range cases {
		item := tc.item
		keys := sectionKeysOf(&item)
		for _, forbidden := range []string{"currency_conversion", "source_disclosure"} {
			if keys[forbidden] {
				t.Errorf("%s: 不应再生成 %q（实际 keys %v）", tc.name, forbidden, keys)
			}
		}
	}
}

// TestCatalogDisplayDeepSeekOffPeakOnlyForDeepSeek 钉住 offpeak_pricing 的
// 供应商边界：仅 deepseek 投影生成「空闲时段参考价」，key/label 与
// token_pricing 一一对应、值为高峰价 × deepSeekOffPeakRatio；其余供应商
// 同形状行不生成（半价规则不散落复制）。
func TestCatalogDisplayDeepSeekOffPeakOnlyForDeepSeek(t *testing.T) {
	price := 1.14285714
	cacheRead := 0.28571429
	deepseek := &ModelCatalogItem{
		ProviderCode:        "deepseek",
		Model:               "deepseek-chat",
		InputUsdPer1M:       &price,
		CachedInputUsdPer1M: &cacheRead,
	}
	sections := map[string]catalogDisplaySection{}
	for _, section := range buildProviderCatalogDisplay(deepseek) {
		sections[section.Key] = section
	}
	offpeak, ok := sections["offpeak_pricing"]
	if !ok {
		t.Fatalf("deepseek 缺少 offpeak_pricing: %+v", sections)
	}
	token, ok := sections["token_pricing"]
	if !ok {
		t.Fatalf("deepseek 缺少 token_pricing: %+v", sections)
	}
	peakByKey := map[string]catalogDisplayItem{}
	for _, entry := range token.Items {
		peakByKey[entry.Key] = entry
	}
	want := map[string]float64{"cache_miss": price * 0.5, "cache_hit": cacheRead * 0.5}
	for _, entry := range offpeak.Items {
		expect, ok := want[entry.Key]
		if !ok {
			t.Fatalf("offpeak 条目 %q 不在预期 key 集", entry.Key)
		}
		got, ok := entry.Value.(float64)
		if !ok || got != expect {
			t.Fatalf("offpeak 条目 %q = %v, want %v", entry.Key, entry.Value, expect)
		}
	}
	if _, exists := sections["peak_schedule"]; exists {
		t.Fatalf("deepseek 不应再生成 peak_schedule（时段口径唯一落点为价格表说明）: %+v", sections)
	}

	glm := &ModelCatalogItem{
		ProviderCode:  "glm",
		Model:         "glm-5",
		InputUsdPer1M: &price,
	}
	for _, section := range buildProviderCatalogDisplay(glm) {
		if section.Key == "offpeak_pricing" {
			t.Fatalf("非 deepseek 投影不应生成 offpeak_pricing: %+v", section)
		}
		if section.Key == "peak_schedule" {
			t.Fatalf("非 deepseek 投影不应生成 peak_schedule: %+v", section)
		}
	}
}

// TestCatalogDisplayMediaPriceLabelsNonEmpty 防媒体计费 section 出现空标签
// 或空值条目（priceSection 已滤 nil；此处锁格式函数回归）。
func TestCatalogDisplayMediaPriceLabelsNonEmpty(t *testing.T) {
	video, call, chars, perSec := 0.14, 0.2, 4.2857, 0.000012
	item := &ModelCatalogItem{
		ProviderCode:            "volcengine",
		Mode:                    func() *string { v := "video"; return &v }(),
		VideoOutputUsdPerSecond: &video,
		VideoOutputUsdPerCall:   &call,
		TtsInputUsdPer1MChars:   &chars,
		AudioInputUsdPerSecond:  &perSec,
	}
	sections := buildProviderCatalogDisplay(item)
	joined := ""
	for _, section := range sections {
		if section.Key == "media_pricing" {
			for _, entry := range section.Items {
				joined += entry.Label + "|" + strings.TrimSpace(fmtString(entry.Value)) + "\n"
			}
		}
	}
	for _, must := range []string{"视频（每秒）", "视频（每次）", "语音合成（每百万字符）", "语音识别（每秒）"} {
		if !strings.Contains(joined, must) {
			t.Errorf("media_pricing 缺标签 %q:\n%s", must, joined)
		}
	}
}

func fmtString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// TestCatalogDisplayMediaTierEntries 钉住按厂商档位价的展示（2026-11-12
// 档位批）：分辨率秒价按 480p→1080p 升序、按次时长价按秒数升序、TTS 按次价
// 各落 media_pricing 条目；有档表时主档不再重复展示。
func TestCatalogDisplayMediaTierEntries(t *testing.T) {
	videoMode := "video"
	labelsAndValues := func(item *ModelCatalogItem) map[string]string {
		out := map[string]string{}
		for _, section := range buildProviderCatalogDisplay(item) {
			if section.Key != "media_pricing" {
				continue
			}
			for _, entry := range section.Items {
				out[entry.Label] = entry.Value.(string)
			}
		}
		return out
	}

	// 分辨率秒价：升序展示，主档 0.14 不重复出现。
	video := &ModelCatalogItem{
		ProviderCode:            "xai",
		Model:                   "grok-imagine-video-1.5",
		Mode:                    &videoMode,
		VideoOutputUsdPerSecond: ptrFloat64(0.14),
		VideoOutputUsdPerSecondByResolution: map[string]float64{
			"1080p": 0.25, "480p": 0.08, "720p": 0.14,
		},
	}
	got := labelsAndValues(video)
	if got["视频 480P（每秒）"] != "$0.08/s" ||
		got["视频 720P（每秒）"] != "$0.14/s" ||
		got["视频 1080P（每秒）"] != "$0.25/s" {
		t.Fatalf("分辨率档展示 = %v", got)
	}
	if _, duplicated := got["视频（每秒）"]; duplicated {
		t.Fatalf("有档表时不得重复展示主档: %v", got)
	}
	// H3 官方 768P/2K 档：排序落在 720P 与 1080P 之间（2026-11-13）。
	if keys := sortedResolutionKeys(map[string]float64{"2k": 1, "480p": 1, "768p": 1, "1080p": 1, "720p": 1}); strings.Join(keys, ",") != "480p,720p,768p,1080p,2k" {
		t.Fatalf("分辨率档排序 = %v", keys)
	}

	// 按次时长档：6s 在 10s 前，主档 0.2857 不重复出现。
	perCall := &ModelCatalogItem{
		ProviderCode:          "minimax",
		Model:                 "MiniMax-Hailuo-2.3",
		Mode:                  &videoMode,
		VideoOutputUsdPerCall: ptrFloat64(0.2857),
		VideoOutputUsdPerCallBySeconds: map[string]float64{
			"10": 0.5714, "6": 0.2857,
		},
	}
	got = labelsAndValues(perCall)
	if got["视频 6s（每次）"] != "$0.2857/video" || got["视频 10s（每次）"] != "$0.5714/video" {
		t.Fatalf("时长档展示 = %v", got)
	}
	if _, duplicated := got["视频（每次）"]; duplicated {
		t.Fatalf("有档表时不得重复展示主档: %v", got)
	}

	// TTS token 口径无 usage 的按次固定价。
	audioMode := "audio_speech"
	tts := &ModelCatalogItem{
		ProviderCode:        "gpt",
		Model:               "gpt-4o-mini-tts",
		Mode:                &audioMode,
		TtsOutputUsdPerCall: ptrFloat64(0.3),
	}
	if got := labelsAndValues(tts); got["语音合成（每次）"] != "$0.3/次" {
		t.Fatalf("TTS 按次价展示 = %v", got)
	}
}

// TestCatalogDisplayImagePixelTiers 钉住按输出像素分档的按张价展示
// （2026-11-12 档位批）：Seedream 5.0 Pro 两档分别显示 ≤2.61M / >2.61M，
// 有档表时不再重复主档「每张」。
func TestCatalogDisplayImagePixelTiers(t *testing.T) {
	imageMode := "image_generation"
	item := &ModelCatalogItem{
		ProviderCode:      "volcengine",
		Model:             "doubao-seedream-5-0-pro-260628",
		Mode:              &imageMode,
		OutputUsdPerImage: ptrFloat64(0.04286),
		OutputUsdPerImageTiers: []pricing.ImagePriceTier{
			{MaxPixels: 2610000, UsdPerImage: 0.04285714285714286},
			{MaxPixels: 0, UsdPerImage: 0.08571428571428572},
		},
		InputUsdPerImage:    ptrFloat64(0.002857142857142857),
		InputImageFreeCount: 1,
	}
	var labels []string
	values := map[string]float64{}
	for _, section := range buildProviderCatalogDisplay(item) {
		if section.Key != "image_generation" {
			continue
		}
		for _, entry := range section.Items {
			labels = append(labels, entry.Label)
			if value, ok := entry.Value.(float64); ok {
				values[entry.Label] = value
			}
		}
	}
	if len(labels) != 3 || labels[0] != "每张（≤2.61M 像素）" || labels[1] != "每张（>2.61M 像素）" || labels[2] != "输入图（首张免费）" {
		t.Fatalf("image tier labels = %v", labels)
	}
	if values[labels[0]] != 0.04285714285714286 || values[labels[1]] != 0.08571428571428572 ||
		values[labels[2]] != 0.002857142857142857 {
		t.Fatalf("image tier values = %v", values)
	}
}

// TestCatalogDisplayVideoCallResolutionTiers 钉住 Hailuo“分辨率×时长”按次
// 组合档展示（2026-11-13）：768P 6s/10s、1080P 6s 逐档展示，排序先分辨率
// 后秒数，且不再重复纯秒数档。
func TestCatalogDisplayVideoCallResolutionTiers(t *testing.T) {
	videoMode := "video"
	item := &ModelCatalogItem{
		ProviderCode: "minimax",
		Model:        "MiniMax-Hailuo-2.3",
		Mode:         &videoMode,
		VideoOutputUsdPerCallByResolutionSeconds: map[string]float64{
			"1080p-6": 0.5, "768p-10": 0.5714285714285714, "768p-6": 0.2857142857142857,
		},
	}
	var labels []string
	values := map[string]float64{}
	for _, section := range buildProviderCatalogDisplay(item) {
		for _, entry := range section.Items {
			if value, ok := entry.Value.(string); ok && strings.Contains(entry.Label, "每次") {
				labels = append(labels, entry.Label)
				_ = value
			}
			if number, ok := entry.Value.(float64); ok {
				values[entry.Label] = number
			}
		}
	}
	want := []string{"视频 768P 6s（每次）", "视频 768P 10s（每次）", "视频 1080P 6s（每次）"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("组合档展示 = %v want %v", labels, want)
	}
	if _, dup := values["视频 6s（每次）"]; dup {
		t.Fatalf("有组合档时不得重复纯秒数档: %v", values)
	}
}

// TestBuiltInStaticDerivedTierFields 钉住 staticPricingFor 必须把档位表从
// 静态定价层透传到目录展示（2026-11-13 修复：此前只拷贝主档价，真实目录
// 行的像素档/分辨率档/输入图价全部丢失，仅合成测试能通过）。
func TestBuiltInStaticDerivedTierFields(t *testing.T) {
	seedream := ResolveBuiltInStaticDerivedCapabilities("volcengine", "doubao-seedream-5-0-pro-260628", nil, "")
	if len(seedream.OutputUsdPerImageTiers) != 2 || seedream.InputUsdPerImage == nil || seedream.InputImageFreeCount != 1 {
		t.Fatalf("Seedream 静态档位未透传: tiers=%+v input=%v free=%d",
			seedream.OutputUsdPerImageTiers, seedream.InputUsdPerImage, seedream.InputImageFreeCount)
	}
	hailuo := ResolveBuiltInStaticDerivedCapabilities("minimax", "MiniMax-Hailuo-2.3", nil, "")
	if got := hailuo.VideoOutputUsdPerCallByResolutionSeconds["1080p-6"]; got != 0.5 {
		t.Fatalf("Hailuo 1080P 组合档未透传: %#v", hailuo.VideoOutputUsdPerCallByResolutionSeconds)
	}
	h3 := ResolveBuiltInStaticDerivedCapabilities("minimax", "MiniMax-H3", nil, "")
	if len(h3.VideoOutputUsdPerSecondByResolution) != 2 {
		t.Fatalf("H3 分辨率秒价档未透传: %#v", h3.VideoOutputUsdPerSecondByResolution)
	}
	qwenPro := ResolveBuiltInStaticDerivedCapabilities("qwen", "qwen-image-3.0-pro", nil, "")
	if len(qwenPro.OutputUsdPerImageTiers) != 2 {
		t.Fatalf("qwen-image-3.0-pro 像素档未透传: %#v", qwenPro.OutputUsdPerImageTiers)
	}
}
