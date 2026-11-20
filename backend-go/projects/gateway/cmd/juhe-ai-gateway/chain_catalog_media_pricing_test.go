package main

// BUG-0368 回归：models.media_pricing_json 为 SQL NULL 的文本模型，目录扫描
// 曾把 NULL 经行 map 序列化成 JSON "null"，再被 mediapricing.Parse 判为
// ErrEmpty 后置 MediaPricingInvalid，导致 BuildCostBreakdown 整行拒绝 token
// 计价（所有文本请求 cost_usd=0 + usage_missing）。本测试锁定两条语义：
// SQL NULL 是「无模板」而不是「非法模板」；无模板的文本行仍按 token 价结算。

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

func TestCatalogRowFromValuesDropsSQLNull(t *testing.T) {
	columns := [][2]string{
		{"COALESCE(vendor_code, '') AS provider_code", "providerCode"},
		{"public_model", "model"},
		{"media_pricing_json", "mediaPricing"},
	}
	row := catalogRowFromValues([]any{"gpt", "gpt-6.2-astra", nil}, columns, nil)
	if _, ok := row["mediaPricing"]; ok {
		t.Fatalf("SQL NULL 列不得写入行对象（会序列化为 JSON null）: %#v", row["mediaPricing"])
	}
	if row["providerCode"] != "gpt" || row["model"] != "gpt-6.2-astra" {
		t.Fatalf("非 NULL 列丢失: %#v", row)
	}
}

func TestCatalogItemFromRowNullMediaPricingKeepsTokenPricing(t *testing.T) {
	tests := []struct {
		name            string
		mediaPricing    any
		wantInvalid     bool
		wantHasTemplate bool
	}{
		{name: "SQL NULL（键缺失）", mediaPricing: nil},
		{name: "JSON null", mediaPricing: json.RawMessage("null")},
		{name: "空串", mediaPricing: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := map[string]any{
				"providerCode":   "gpt",
				"model":          "gpt-6.2-astra",
				"scope":          "built_in",
				"status":         "active",
				"mode":           "responses",
				"inputUsdPer1M":  8.0,
				"outputUsdPer1M": 40.0,
			}
			if tt.mediaPricing != nil {
				row["mediaPricing"] = tt.mediaPricing
			}
			item, err := catalogItemFromRow(row)
			if err != nil {
				t.Fatalf("catalogItemFromRow: %v", err)
			}
			if item.MediaPricingInvalid != tt.wantInvalid {
				t.Fatalf("MediaPricingInvalid = %v, want %v", item.MediaPricingInvalid, tt.wantInvalid)
			}
			if item.HasMediaPricing != tt.wantHasTemplate {
				t.Fatalf("HasMediaPricing = %v, want %v", item.HasMediaPricing, tt.wantHasTemplate)
			}
			breakdown := pricing.BuildCostBreakdown(chainCatalogPricing(&item), pricing.CostInput{
				ProviderCode: "gpt",
				Model:        "gpt-6.2-astra",
				ServiceTier:  "default",
				InputTokens:  float64Ptr(5374),
				OutputTokens: float64Ptr(5),
			})
			if breakdown == nil || breakdown.AccountChargeUsd == nil {
				t.Fatalf("无媒体模板的文本行必须按 token 计价，breakdown = %#v", breakdown)
			}
			if got := *breakdown.AccountChargeUsd; got != 0.043192 {
				t.Fatalf("AccountChargeUsd = %v, want 0.043192", got)
			}
		})
	}
}

func TestCatalogItemFromRowMalformedMediaPricingStaysInvalid(t *testing.T) {
	row := map[string]any{
		"providerCode":      "volcengine",
		"model":             "doubao-seedream-5-0-flash-260915",
		"scope":             "built_in",
		"status":            "active",
		"mode":              "image_generation",
		"outputUsdPerImage": 0.01714,
		// 缺少 version/unit/baseUsd：结构非法，必须维持 AC-016 的
		// MediaPricingInvalid 并拒绝回落 legacy 价。
		"mediaPricing": map[string]any{"baseUsd": 0.01714},
	}
	item, err := catalogItemFromRow(row)
	if err != nil {
		t.Fatalf("catalogItemFromRow: %v", err)
	}
	if !item.MediaPricingInvalid || item.HasMediaPricing || len(item.MediaPricing) != 0 {
		t.Fatalf("非法模板必须按无价处理: invalid=%v has=%v raw=%s",
			item.MediaPricingInvalid, item.HasMediaPricing, string(item.MediaPricing))
	}
}

func float64Ptr(value float64) *float64 { return &value }
