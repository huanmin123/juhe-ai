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
//（供应商独立计费与模型目录展示设计 §3.2）：系统统一美元计价，币种/汇率/
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
		ProviderCode:       "deepseek",
		Model:              "deepseek-chat",
		InputUsdPer1M:      &price,
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
		ProviderCode: "glm",
		Model:        "glm-5",
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
