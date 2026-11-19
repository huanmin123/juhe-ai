package pricing

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing/golden"
)

// 两引擎黄金对齐（非对话媒体统一计费设计 AC-015）：与
// shared/platform/usagepricing/catalog_media_test.go 消费同一组
// mediapricing/golden.Cases，逐项断言 cost、行项 kind/tier/multiplier 与
// billingPolicy。
func TestMediaUnifiedGoldenAlignment(t *testing.T) {
	for _, tc := range golden.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			breakdown := BuildCostBreakdown(
				&Pricing{ProviderCode: "golden", Model: "golden", Mode: tc.Mode, MediaPricing: tc.Template},
				CostInput{
					ProviderCode:       "golden",
					Model:              "golden",
					OutputVideoSeconds: tc.OutputVideoSeconds,
					VideoCalls:         tc.VideoCalls,
					AudioInputSeconds:  tc.AudioInputSeconds,
					OutputImageCount:   mediaGoldenIntToFloat(tc.OutputImageCount),
					ResolutionTier:     tc.ResolutionTier,
					QuantitySource:     tc.QuantitySource,
				},
			)
			if tc.Want.Nil {
				if breakdown != nil {
					t.Fatalf("want nil breakdown, got %#v", breakdown)
				}
				return
			}
			if breakdown == nil {
				t.Fatal("want breakdown, got nil")
			}
			if breakdown.BillingPolicy != tc.Want.BillingPolicy {
				t.Fatalf("billingPolicy = %q, want %q", breakdown.BillingPolicy, tc.Want.BillingPolicy)
			}
			if len(breakdown.LineItems) != tc.Want.LineCount {
				t.Fatalf("lineItems = %d, want %d", len(breakdown.LineItems), tc.Want.LineCount)
			}
			if tc.Want.LineCount == 0 {
				if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != *tc.Want.AccountChargeUsd {
					t.Fatalf("accountChargeUsd = %v, want %v", breakdown.AccountChargeUsd, *tc.Want.AccountChargeUsd)
				}
				return
			}
			line := breakdown.LineItems[0]
			if line.Key != tc.Want.Key || string(line.Kind) != tc.Want.Kind || line.Unit != tc.Want.Unit {
				t.Fatalf("line = %s/%s/%s, want %s/%s/%s", line.Key, line.Kind, line.Unit, tc.Want.Key, tc.Want.Kind, tc.Want.Unit)
			}
			if line.Tier != tc.Want.Tier {
				t.Fatalf("tier = %q, want %q", line.Tier, tc.Want.Tier)
			}
			if line.Multiplier == nil || *line.Multiplier != *tc.Want.Multiplier {
				t.Fatalf("multiplier = %v, want %v", line.Multiplier, *tc.Want.Multiplier)
			}
			if line.BaseUnitPriceUsd == nil || *line.BaseUnitPriceUsd != *tc.Want.BaseUnitPriceUsd {
				t.Fatalf("baseUnitPriceUsd = %v, want %v", line.BaseUnitPriceUsd, *tc.Want.BaseUnitPriceUsd)
			}
			if line.CostUsd != *tc.Want.CostUsd {
				t.Fatalf("line cost = %v, want %v", line.CostUsd, *tc.Want.CostUsd)
			}
			if line.QuantitySource != tc.Want.QuantitySource {
				t.Fatalf("quantitySource = %q, want %q", line.QuantitySource, tc.Want.QuantitySource)
			}
			if breakdown.AccountChargeUsd == nil || *breakdown.AccountChargeUsd != *tc.Want.AccountChargeUsd {
				t.Fatalf("accountChargeUsd = %v, want %v", breakdown.AccountChargeUsd, *tc.Want.AccountChargeUsd)
			}
		})
	}
}

func mediaGoldenIntToFloat(value *int) *float64 {
	if value == nil {
		return nil
	}
	out := float64(*value)
	return &out
}
