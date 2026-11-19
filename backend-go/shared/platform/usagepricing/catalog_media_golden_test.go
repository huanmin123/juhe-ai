package usagepricing

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing/golden"
	"github.com/huanminabc/juhe-ai/backend-go-platform/usagewriter"
)

// 两引擎黄金对齐（非对话媒体统一计费设计 AC-015）：与
// gateway/internal/pricing/billing_media_golden_test.go 消费同一组
// mediapricing/golden.Cases，防止冻结路径与网关估算各写一套预期。
func TestUsageCatalogMediaGoldenAlignment(t *testing.T) {
	for _, tc := range golden.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			row := usageCatalogPricingRow{
				model:           "golden",
				mode:            tc.Mode,
				mediaPricing:    tc.Template,
				hasMediaPricing: true,
			}
			breakdown := buildUsageCatalogCostBreakdown(row, "golden", "", usagewriter.UsageRecordInput{
				OutputVideoSeconds: tc.OutputVideoSeconds,
				VideoCalls:         tc.VideoCalls,
				AudioInputSeconds:  tc.AudioInputSeconds,
				OutputImageCount:   tc.OutputImageCount,
				ResolutionTier:     tc.ResolutionTier,
				QuantitySource:     tc.QuantitySource,
			})
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
			if line.Key != tc.Want.Key || line.Kind != tc.Want.Kind || line.Unit != tc.Want.Unit {
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
