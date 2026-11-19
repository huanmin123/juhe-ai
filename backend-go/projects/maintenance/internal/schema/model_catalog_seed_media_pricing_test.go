package schema

// P3 种子门禁：model_catalog_data_gen.go 必须携带 catalogdata 迁移后的 66
// 条 media_pricing_json，且 13 个 realtime、3 个 gpt-audio、1 个无价 video
// 保持 NULL。与 gateway pricing 的 catalogdata 门禁互为两端（数据源与种子）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

func seedRowIsRealtime(row modelCatalogSeedRow) bool {
	var protocols []string
	if err := json.Unmarshal([]byte(row.SupportedAPIProtocolsJSON), &protocols); err != nil {
		return false
	}
	for _, protocol := range protocols {
		if protocol == "realtime" {
			return true
		}
	}
	return false
}

func TestModelCatalogSeedMediaPricingRows(t *testing.T) {
	migrated := 0
	legacy := 0
	unitCounts := map[mediapricing.Unit]int{}
	for _, row := range modelCatalogSeedRows {
		mode := ""
		if row.Mode != nil {
			mode = *row.Mode
		}
		_, mediaMode := mediapricing.KindForMode(mode)
		if row.MediaPricingJSON == nil {
			if !mediaMode {
				continue
			}
			if seedRowIsRealtime(row) || strings.HasPrefix(row.Model, "gpt-audio") ||
				(row.ProviderCode == "volcengine" && row.Model == "doubao-seedance-1-0-pro-250528") {
				legacy++
				continue
			}
			t.Errorf("%s/%s（%s）缺 MediaPricingJSON，也不在保持 legacy 清单内", row.ProviderCode, row.Model, mode)
			continue
		}
		if !mediaMode {
			t.Errorf("%s/%s（%s）非媒体 mode 不得带 MediaPricingJSON", row.ProviderCode, row.Model, mode)
			continue
		}
		pricing, err := mediapricing.Parse([]byte(*row.MediaPricingJSON))
		if err != nil {
			t.Errorf("%s/%s MediaPricingJSON 非法: %v", row.ProviderCode, row.Model, err)
			continue
		}
		kind, _ := mediapricing.KindForMode(mode)
		if err := pricing.ValidateFor(kind); err != nil {
			t.Errorf("%s/%s MediaPricingJSON 与 mode 不匹配: %v", row.ProviderCode, row.Model, err)
			continue
		}
		migrated++
		unitCounts[pricing.Unit]++
	}
	if migrated != 66 {
		t.Errorf("种子 media_pricing_json 行 = %d, want 66", migrated)
	}
	if legacy != 17 {
		t.Errorf("种子保持 NULL 的媒体行 = %d, want 17", legacy)
	}
	want := map[mediapricing.Unit]int{
		mediapricing.UnitSecond: 46,
		mediapricing.UnitImage:  19,
		mediapricing.UnitCall:   1,
	}
	for unit, count := range want {
		if unitCounts[unit] != count {
			t.Errorf("种子 unit=%s 行 = %d, want %d", unit, unitCounts[unit], count)
		}
	}
}

func TestSeedMediaPricingJSONLookup(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		wantBase float64
	}{
		{provider: "gemini", model: "gemini-3.5-transcribe", wantBase: 0.00005},
		{provider: "gpt", model: "gpt-4o-transcribe", wantBase: 0.0001},
		{provider: "gpt", model: "gpt-4o-mini-transcribe", wantBase: 0.00005},
	}
	for _, testCase := range cases {
		raw, ok := SeedMediaPricingJSON(testCase.provider, testCase.model)
		if !ok {
			t.Fatalf("SeedMediaPricingJSON(%q,%q) not found", testCase.provider, testCase.model)
		}
		pricing, err := mediapricing.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s/%s: %v", testCase.provider, testCase.model, err)
		}
		if pricing.Unit != mediapricing.UnitSecond || pricing.BaseUsd != testCase.wantBase || len(pricing.TierMultipliers) != 0 {
			t.Fatalf("%s/%s pricing = %+v, want second/%v no tiers", testCase.provider, testCase.model, pricing, testCase.wantBase)
		}
	}
	if _, ok := SeedMediaPricingJSON("gpt", "not-in-catalog"); ok {
		t.Fatal("unknown model must not resolve")
	}
}
