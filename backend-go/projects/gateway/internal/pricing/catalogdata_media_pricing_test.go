package pricing

// P3 媒体目录迁移门禁（设计 §9.2/§9.3）：catalogdata 中 67 个可迁移媒体行
// 必须带合法 mediaPricing，13 个 realtime 行、3 个 gpt-audio 对话音频行与
// 1 个无价 video 行保持 legacy/NULL；并锁定 Hailuo 套餐折算、字符/token
// 换算、图片 token 估价与官方分辨率倍率等关键锚点。

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

type catalogMediaEntry struct {
	Provider  string
	Model     string
	Mode      string
	Protocols []string
	Pricing   *mediapricing.Pricing
}

func loadCatalogMediaEntries(t *testing.T) []catalogMediaEntry {
	t.Helper()
	var entries []catalogMediaEntry
	for _, provider := range seedGenProviders {
		raw, err := catalogDataFS.ReadFile("catalogdata/" + provider + ".json")
		if err != nil {
			t.Fatalf("read %s catalogdata: %v", provider, err)
		}
		var doc struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s catalogdata: %v", provider, err)
		}
		for _, entry := range doc.Models {
			model, _ := entry["model"].(string)
			mode, _ := entry["mode"].(string)
			protocols := make([]string, 0)
			if list, ok := entry["supportedAPIProtocols"].([]any); ok {
				for _, item := range list {
					if text, ok := item.(string); ok {
						protocols = append(protocols, text)
					}
				}
			}
			item := catalogMediaEntry{Provider: provider, Model: model, Mode: mode, Protocols: protocols}
			if rawPricing, ok := entry["mediaPricing"]; ok && rawPricing != nil {
				encoded, err := json.Marshal(rawPricing)
				if err != nil {
					t.Fatalf("%s/%s mediaPricing marshal: %v", provider, model, err)
				}
				parsed, err := mediapricing.Parse(encoded)
				if err != nil {
					t.Fatalf("%s/%s mediaPricing parse: %v", provider, model, err)
				}
				item.Pricing = parsed
			}
			entries = append(entries, item)
		}
	}
	return entries
}

func catalogMediaExpectedLegacy(t *testing.T, entry catalogMediaEntry) bool {
	t.Helper()
	for _, protocol := range entry.Protocols {
		if protocol == "realtime" {
			return true
		}
	}
	if strings.HasPrefix(entry.Model, "gpt-audio") {
		return true
	}
	if entry.Provider == "volcengine" && entry.Model == "doubao-seedance-1-0-pro-250528" {
		return true
	}
	return false
}

// TestCatalogDataGenerateContentImageModeProjection 钉住 Gemini 原生
// generate_content 图像行的读侧回归：seed 只按协议写 endpoint modes，
// generate_content 投影为 chat；读侧必须用 ValidateForMappedMode 保留图像
// 模板，不能按 ValidateForMode("chat") 误判为非法价（否则成功图片请求
// 0 成本 + usage_missing、账户绑定被拒）。
func TestCatalogDataGenerateContentImageModeProjection(t *testing.T) {
	seen := 0
	for _, entry := range loadCatalogMediaEntries(t) {
		if entry.Pricing == nil || entry.Mode != "image_generation" {
			continue
		}
		generateContent := false
		for _, protocol := range entry.Protocols {
			if protocol == "generate_content" || protocol == "stream_generate_content" {
				generateContent = true
				break
			}
		}
		if !generateContent {
			continue
		}
		seen++
		if err := entry.Pricing.ValidateForMappedMode("chat"); err != nil {
			t.Errorf("%s/%s: chat 投影下的合法图像模板被误判: %v", entry.Provider, entry.Model, err)
		}
		if err := entry.Pricing.ValidateFor(mediapricing.KindImage); err != nil {
			t.Errorf("%s/%s: 图像模板结构非法: %v", entry.Provider, entry.Model, err)
		}
	}
	if seen != 3 {
		t.Fatalf("generate_content 图像行 = %d, want 3（gemini 3.1 flash/pro/lite image）", seen)
	}
}

func TestCatalogDataMediaPricingMigration(t *testing.T) {
	entries := loadCatalogMediaEntries(t)
	migrated, keptLegacy := 0, 0
	unitCounts := map[mediapricing.Unit]int{}
	for _, entry := range entries {
		kind, mediaMode := mediapricing.KindForMode(entry.Mode)
		legacy := catalogMediaExpectedLegacy(t, entry)
		if entry.Pricing == nil {
			if mediaMode && !legacy {
				t.Errorf("%s/%s（%s, %v）缺 mediaPricing，也不在保持 legacy 清单内", entry.Provider, entry.Model, entry.Mode, entry.Protocols)
			}
			if legacy {
				keptLegacy++
			}
			continue
		}
		if !mediaMode {
			t.Errorf("%s/%s（%s）非媒体 mode 不得配置 mediaPricing", entry.Provider, entry.Model, entry.Mode)
			continue
		}
		if legacy {
			t.Errorf("%s/%s（%v）属于保持 legacy 清单，不得迁移 mediaPricing", entry.Provider, entry.Model, entry.Protocols)
		}
		if err := entry.Pricing.ValidateFor(kind); err != nil {
			t.Errorf("%s/%s（%s）mediaPricing 非法: %v", entry.Provider, entry.Model, entry.Mode, err)
			continue
		}
		migrated++
		unitCounts[entry.Pricing.Unit]++
	}
	if migrated != 67 {
		t.Errorf("迁移行 = %d, want 67（video 22 + call 1 + image 19 + TTS 17 + STT 8）", migrated)
	}
	if keptLegacy != 17 {
		t.Errorf("保持 legacy 行 = %d, want 17（realtime 13 + gpt-audio 3 + 无价 video 1）", keptLegacy)
	}
	wantUnits := map[mediapricing.Unit]int{
		mediapricing.UnitSecond: 47, // video 22 + TTS 17 + STT 8
		mediapricing.UnitImage:  19,
		mediapricing.UnitCall:   1,
	}
	for unit, want := range wantUnits {
		if unitCounts[unit] != want {
			t.Errorf("unit=%s 行数 = %d, want %d", unit, unitCounts[unit], want)
		}
	}
}

func catalogPricingForTest(t *testing.T, provider, model string) *mediapricing.Pricing {
	t.Helper()
	for _, entry := range loadCatalogMediaEntries(t) {
		if entry.Provider == provider && entry.Model == model {
			if entry.Pricing == nil {
				t.Fatalf("%s/%s 无 mediaPricing", provider, model)
			}
			return entry.Pricing
		}
	}
	t.Fatalf("catalogdata 未找到 %s/%s", provider, model)
	return nil
}

func assertPricingFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("%s = %.17g, want %.17g", label, got, want)
	}
}

func TestCatalogDataMediaPricingAnchors(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		unit     mediapricing.Unit
		baseUsd  float64
		baseTier string
		tiers    map[string]float64
	}{
		// Hailuo 套餐折秒与官方 1080P 倍率（设计 §9.3/§14.4 #8）。
		{"minimax", "MiniMax-Hailuo-2.3", mediapricing.UnitSecond, 4.00 / 7.0 / 10.0, "720p", map[string]float64{
			"720p": 1.0, "1080p": (3.50 / 6.0) / (4.00 / 10.0),
		}},
		{"minimax", "MiniMax-Hailuo-2.3-Fast", mediapricing.UnitSecond, 2.25 / 7.0 / 10.0, "720p", map[string]float64{
			"720p": 1.0, "1080p": (2.31 / 6.0) / (2.25 / 10.0),
		}},
		{"minimax", "MiniMax-H3", mediapricing.UnitSecond, 0.50 / 7.0, "720p", map[string]float64{
			"720p": 1.0, "1080p": 1.3, "1440p": 1.6,
		}},
		{"minimax", "MiniMax-H3-Max", mediapricing.UnitSecond, 0.50 / 7.0, "720p", map[string]float64{
			"720p": 1.0, "480p": 0.33 / 0.50,
		}},
		// qwen 官方档位与 1080p 基准档。
		{"qwen", "wan2.2-t2v-plus", mediapricing.UnitSecond, 0.70 / 7.0, "1080p", map[string]float64{
			"1080p": 1.0, "480p": 0.14 / 0.70, "720p": 0.5, "1440p": 1.5, "2160p": 2.5,
		}},
		{"qwen", "wan2.7-t2v", mediapricing.UnitSecond, 0.60 / 7.0, "720p", map[string]float64{
			"720p": 1.0, "1080p": 1.0 / 0.60,
		}},
		{"qwen", "wan3.0-video", mediapricing.UnitSecond, 0.60 / 7.0, "720p", map[string]float64{
			"720p": 1.0, "480p": 0.30 / 0.60, "1080p": 1.20 / 0.60,
		}},
		// xAI 官方三档；lite 的 1080p 高于默认 2.0，1440p/2160p 等比放大。
		{"xai", "grok-imagine-video-1.5", mediapricing.UnitSecond, 0.14, "720p", map[string]float64{
			"720p": 1.0, "480p": 0.08 / 0.14, "1080p": 0.25 / 0.14,
		}},
		{"xai", "grok-imagine-video-1.5-lite", mediapricing.UnitSecond, 0.03, "720p", map[string]float64{
			"720p": 1.0, "480p": 0.02 / 0.03, "1080p": 0.14 / 0.03,
			"1440p": 3.0 * (0.14 / 0.03) / 2.0,
			"2160p": 5.0 * (0.14 / 0.03) / 2.0,
		}},
		// 无价 video 之外的按次行。
		{"volcengine", "doubao-seed3d-2-0-260328", mediapricing.UnitCall, 2.40 / 7.0, "", nil},
		// TTS：字符价 × 4；seed-tts-2.0 只用字符价，不复用同行秒价。
		{"minimax", "speech-2.8-turbo", mediapricing.UnitSecond, 0.000028571428571428574 * 4, "", nil},
		{"volcengine", "seed-tts-2.0", mediapricing.UnitSecond, 0.000042857142857142856 * 4, "", nil},
		{"volcengine", "seed-audio-1.0", mediapricing.UnitSecond, 0.00238095, "", nil},
		{"gpt", "gpt-4o-mini-tts", mediapricing.UnitSecond, 0.000012 * 25, "", nil},
		{"gemini", "gemini-2.5-flash-preview-tts", mediapricing.UnitSecond, 0.00001 * 25, "", nil},
		// STT：现有秒价直接迁移。
		{"qwen", "qwen3-asr-flash", mediapricing.UnitSecond, 0.00022 / 7.0, "", nil},
		{"qwen", "paraformer-v2", mediapricing.UnitSecond, 0.00008 / 7.0, "", nil},
		{"gpt", "gpt-4o-transcribe", mediapricing.UnitSecond, 0.0001, "", nil},
		// 图像：按张直接迁移；token 行按每张 token 估算 1K 价。
		{"xai", "grok-imagine-image-2.0", mediapricing.UnitImage, 0.04, "1k", map[string]float64{"1k": 1.0, "2k": 0.06 / 0.04}},
		{"qwen", "qwen-image-3.0", mediapricing.UnitImage, 0.18 / 7.0, "1k", map[string]float64{"1k": 1.0, "2k": 1.0}},
		{"gemini", "gemini-3-pro-image", mediapricing.UnitImage, 0.00012 * 1120, "1k", nil},
		{"gpt", "gpt-image-2", mediapricing.UnitImage, 0.00003 * 1056, "1k", nil},
	}
	for _, tc := range cases {
		pricing := catalogPricingForTest(t, tc.provider, tc.model)
		label := tc.provider + "/" + tc.model
		if pricing.Unit != tc.unit {
			t.Errorf("%s unit = %s, want %s", label, pricing.Unit, tc.unit)
		}
		assertPricingFloat(t, label+" baseUsd", pricing.BaseUsd, tc.baseUsd)
		if pricing.BaseTier != tc.baseTier {
			t.Errorf("%s baseTier = %q, want %q", label, pricing.BaseTier, tc.baseTier)
		}
		for tier, want := range tc.tiers {
			got, ok := pricing.TierMultipliers[tier]
			if !ok {
				t.Errorf("%s 缺档位 %q", label, tier)
				continue
			}
			assertPricingFloat(t, label+" "+tier, got, want)
		}
	}
}

// TestCatalogDataSeedTTSSpecialCase 单独钉住 seed-tts-2.0 不得把同行
// audioInputCostPerSecond 当 baseUsd（否则字符+秒双计费，约高 13.9 倍）。
func TestCatalogDataSeedTTSSpecialCase(t *testing.T) {
	pricing := catalogPricingForTest(t, "volcengine", "seed-tts-2.0")
	assertPricingFloat(t, "seed-tts-2.0 baseUsd", pricing.BaseUsd, 0.000042857142857142856*4)
	if math.Abs(pricing.BaseUsd-0.00238095) < 1e-9 {
		t.Fatalf("seed-tts-2.0 baseUsd 复用了同行 legacy 秒价: %v", pricing.BaseUsd)
	}
	legacy := mustFindPricingForTest(t, "volcengine", "seed-tts-2.0")
	wantLegacyPer1M := 0.000042857142857142856 * 1_000_000
	if legacy.TtsInputUsdPer1MChars == nil || math.Abs(*legacy.TtsInputUsdPer1MChars-wantLegacyPer1M) > 1e-6 {
		got := 0.0
		if legacy.TtsInputUsdPer1MChars != nil {
			got = *legacy.TtsInputUsdPer1MChars
		}
		t.Fatalf("seed-tts-2.0 legacy 字符价 = %v, want %v/1M chars", got, wantLegacyPer1M)
	}
}
