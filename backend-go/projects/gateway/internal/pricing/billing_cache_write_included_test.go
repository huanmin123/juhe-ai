package pricing

// BUG-0347 回归：上游把 cache_write_tokens 计入 input_tokens 总额时，普通
// input 行项必须扣除缓存写入再计价；Anthropic 的 cache_creation_input_tokens
// 独立于 input_tokens，不得扣除。
//
// 取证样本（生产 trace 9da21540-a1fc-4b71-b03f-00b03d818bd8，gpt-6.1-sol）：
// 上游 usage {"input_tokens":13196,"input_tokens_details":{"cached_tokens":0,
// "cache_write_tokens":13193}} —— 修复前 input 行项 13196 与 cache_write
// 行项 13193 并存，同一批 token 按输入价与缓存写入价各计一次。

import (
	"testing"
)

func TestBuildCostBreakdownCacheWriteIncludedInInput(t *testing.T) {
	// 形态 1：cache_write ≈ input（生产异常形态）。input 行项应收敛到差额 3，
	// 而不是全量 13196。gpt-5.6-sol 价：input 4 / cache_write 5 per 1M。
	nearEqual := billingBreakdown(t, "openai", "gpt-5.6-sol", CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.6-sol",
		InputTokens:      f64p(13_196),
		CacheWriteTokens: f64p(13_193),
		OutputTokens:     f64p(167),
	})
	if nearEqual == nil {
		t.Fatal("near-equal cache write breakdown must build")
	}
	wantFloat(t, "input 行项数量", lineQuantity(t, nearEqual, LineInput), 3)
	wantFloat(t, "inputCostUsd", nearEqual.InputCostUsd, 0.000012)           // 3 * 4/1M
	wantFloat(t, "cacheWriteCostUsd", nearEqual.CacheWriteCostUsd, 0.065965) // 13193 * 5/1M
	wantFloat(t, "outputCostUsd", nearEqual.OutputCostUsd, 0.00334)          // 167 * 20/1M
	wantFloat(t, "accountChargeUsd", nearEqual.AccountChargeUsd, 0.069317)

	// 形态 2：cache_write ≪ input（正常形态）。只有写入部分被扣除，其余输入
	// 仍按普通输入价计费。
	small := billingBreakdown(t, "openai", "gpt-5.6-sol", CostInput{
		ProviderCode:     "openai",
		Model:            "gpt-5.6-sol",
		InputTokens:      f64p(18_993),
		CacheWriteTokens: f64p(4_215),
	})
	wantFloat(t, "normal 形态 input 行项", lineQuantity(t, small, LineInput), 14_778)
	wantFloat(t, "normal 形态 inputCostUsd", small.InputCostUsd, 0.059112) // 14778 * 4/1M
	wantFloat(t, "normal 形态 cacheWriteCostUsd", small.CacheWriteCostUsd, 0.021075)
	wantFloat(t, "normal 形态 accountChargeUsd", small.AccountChargeUsd, 0.080187)

	// 形态 3：写入全部落在 1h 槽位（cacheWriteTokens 为空，仅
	// cacheWrite1hTokens 有值）。扣除量取两档之和，不能只扣标准档。
	only1h := billingBreakdown(t, "openai", "gpt-5.6-sol", CostInput{
		ProviderCode:       "openai",
		Model:              "gpt-5.6-sol",
		InputTokens:        f64p(100_000),
		CacheWrite1hTokens: f64p(100_000),
	})
	wantFloat(t, "1h-only input 行项", lineQuantity(t, only1h, LineInput), 0)
	if only1h.InputCostUsd != nil {
		t.Fatalf("1h-only inputCostUsd = %v, want nil (0 quantity yields no line)", *only1h.InputCostUsd)
	}
	wantFloat(t, "1h-only cacheWrite1hCostUsd", only1h.CacheWrite1hCostUsd, 0.5) // 100k * 5/1M
	wantFloat(t, "1h-only accountChargeUsd", only1h.AccountChargeUsd, 0.5)
}

func TestBuildCostBreakdownCacheWriteNotDeductedForAnthropic(t *testing.T) {
	// Anthropic 的 cache_creation_input_tokens 独立于 input_tokens：缓存写入
	// 不得从 input 中扣除，否则会漏计真实输入。
	got := billingBreakdown(t, "anthropic", "claude-sonnet-5", CostInput{
		ProviderCode:     "anthropic",
		Model:            "claude-sonnet-5",
		InputTokens:      f64p(1_000_000),
		CacheWriteTokens: f64p(1_000_000),
	})
	wantFloat(t, "anthropic input 行项", lineQuantity(t, got, LineInput), 1_000_000)
	wantFloat(t, "anthropic inputCostUsd", got.InputCostUsd, 2) // 全量 1M * 2/1M
}

// lineQuantity 取指定行项的数量；行项缺失即视为 0。
func lineQuantity(t *testing.T, breakdown *CostBreakdown, kind CostLineKind) *float64 {
	t.Helper()
	for index := range breakdown.LineItems {
		if breakdown.LineItems[index].Kind == kind {
			value := breakdown.LineItems[index].Quantity
			return &value
		}
	}
	return f64p(0)
}
