package main

// BUG-0347 回归（jobs 镜像引擎）：worker_usage_pricing_catalog.go 的
// buildUsageCatalogTokenBreakdown 与 gateway pricing.buildTokenCostBreakdown
// 必须保持同一扣除语义——OpenAI 兼容族把 cache_write_tokens 计入
// input_tokens 总额，普通 input 行项要先扣缓存写入；Anthropic 的
// cache_creation_input_tokens 独立于 input，不得扣除。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/usagewriter"
)

func usageCatalogIntPtr(value int) *int { return &value }

func usageCatalogRatePtr(value float64) *float64 { return &value }

// usageCatalogBug0347Row 构造与 gpt-5.6-sol 同价的目录行（input 4 /
// cache_write 5 / cached input 0.4 / output 20 per 1M）。
func usageCatalogBug0347Row() usageCatalogPricingRow {
	return usageCatalogPricingRow{
		model:               "gpt-5.6-sol",
		upstreamModel:       "gpt-5.6-sol",
		inputUsdPer1M:       usageCatalogRatePtr(4),
		outputUsdPer1M:      usageCatalogRatePtr(20),
		cachedInputUsdPer1M: usageCatalogRatePtr(0.4),
		cacheWriteUsdPer1M:  usageCatalogRatePtr(5),
	}
}

func usageCatalogLineQuantity(breakdown *usagewriter.CostBreakdown, kind string) float64 {
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == kind {
			return breakdown.LineItems[index].Quantity
		}
	}
	return 0
}

func TestUsageCatalogTokenBreakdownDeductsIncludedCacheWrite(t *testing.T) {
	row := usageCatalogBug0347Row()

	// 形态 1：cache_write ≈ input（生产异常形态 gpt-6.1-sol 13196/13193）。
	nearEqual := buildUsageCatalogCostBreakdown(row, "openai", "", usagewriter.UsageRecordInput{
		InputTokens:      usageCatalogIntPtr(13_196),
		CacheWriteTokens: usageCatalogIntPtr(13_193),
		OutputTokens:     usageCatalogIntPtr(167),
	})
	if nearEqual == nil {
		t.Fatal("near-equal cache write breakdown must build")
	}
	if got := usageCatalogLineQuantity(nearEqual, "input"); got != 3 {
		t.Fatalf("input 行项数量 = %v, want 3（input 13196 − cache_write 13193）", got)
	}
	if nearEqual.InputCostUsd == nil || *nearEqual.InputCostUsd != 0.000012 {
		t.Fatalf("inputCostUsd = %v, want 0.000012", nearEqual.InputCostUsd)
	}
	if nearEqual.CacheWriteCostUsd == nil || *nearEqual.CacheWriteCostUsd != 0.065965 {
		t.Fatalf("cacheWriteCostUsd = %v, want 0.065965", nearEqual.CacheWriteCostUsd)
	}
	if nearEqual.AccountChargeUsd == nil || *nearEqual.AccountChargeUsd != 0.069317 {
		t.Fatalf("accountChargeUsd = %v, want 0.069317", nearEqual.AccountChargeUsd)
	}

	// 形态 2：cache_write ≪ input（正常形态），只扣写入部分。
	small := buildUsageCatalogCostBreakdown(row, "openai", "", usagewriter.UsageRecordInput{
		InputTokens:      usageCatalogIntPtr(18_993),
		CacheWriteTokens: usageCatalogIntPtr(4_215),
	})
	if small == nil {
		t.Fatal("normal-form breakdown must build")
	}
	if got := usageCatalogLineQuantity(small, "input"); got != 14_778 {
		t.Fatalf("input 行项数量 = %v, want 14778", got)
	}
	if small.InputCostUsd == nil || *small.InputCostUsd != 0.059112 {
		t.Fatalf("inputCostUsd = %v, want 0.059112", small.InputCostUsd)
	}
	if small.AccountChargeUsd == nil || *small.AccountChargeUsd != 0.080187 {
		t.Fatalf("accountChargeUsd = %v, want 0.080187", small.AccountChargeUsd)
	}

	// 形态 3：写入全部落在 1h 槽位时，扣除量取两档之和。
	only1h := buildUsageCatalogCostBreakdown(row, "openai", "", usagewriter.UsageRecordInput{
		InputTokens:        usageCatalogIntPtr(100_000),
		CacheWrite1hTokens: usageCatalogIntPtr(100_000),
	})
	if only1h == nil {
		t.Fatal("1h-only breakdown must build")
	}
	if got := usageCatalogLineQuantity(only1h, "input"); got != 0 {
		t.Fatalf("input 行项数量 = %v, want 0", got)
	}
	if only1h.CacheWrite1hCostUsd == nil || *only1h.CacheWrite1hCostUsd != 0.5 {
		t.Fatalf("cacheWrite1hCostUsd = %v, want 0.5", only1h.CacheWrite1hCostUsd)
	}
}

func TestUsageCatalogTokenBreakdownKeepsAnthropicInputWhole(t *testing.T) {
	// claude-sonnet-5 同价行：input 2 / 5m write 2.5。
	row := usageCatalogPricingRow{
		model:                "claude-sonnet-5",
		upstreamModel:        "claude-sonnet-5",
		inputUsdPer1M:        usageCatalogRatePtr(2),
		cachedInputUsdPer1M:  usageCatalogRatePtr(0.2),
		cacheWriteUsdPer1M:   usageCatalogRatePtr(2.5),
		cacheWrite1hUsdPer1M: usageCatalogRatePtr(4),
	}
	got := buildUsageCatalogCostBreakdown(row, "anthropic", "", usagewriter.UsageRecordInput{
		InputTokens:      usageCatalogIntPtr(1_000_000),
		CacheWriteTokens: usageCatalogIntPtr(1_000_000),
	})
	if got == nil {
		t.Fatal("anthropic breakdown must build")
	}
	if quantity := usageCatalogLineQuantity(got, "input"); quantity != 1_000_000 {
		t.Fatalf("anthropic input 行项 = %v, want 全量 1000000", quantity)
	}
	if got.InputCostUsd == nil || *got.InputCostUsd != 2 {
		t.Fatalf("anthropic inputCostUsd = %v, want 2", got.InputCostUsd)
	}
}
