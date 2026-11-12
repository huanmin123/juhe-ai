package pricing

import "testing"

// TestEstimateProviderCostUsdMiniMaxM31Flash 锁定 M3.1-Flash 账单口径
// （2026-11-13）：tokenrhythm 真实调用 204 prompt（128 cached）/8
// completion 计费 ¥0.00012080；按官方 1/4/0.1 元每百万 ÷7.0，本引擎应得
// 同一人民币金额（$0.0000172571 × 7）。
func TestEstimateProviderCostUsdMiniMaxM31Flash(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cost := EstimateProviderCostUsd(CostInput{
		ProviderCode:    "minimax",
		Model:           "MiniMax-M3.1-Flash",
		InputTokens:     f(204),
		OutputTokens:    f(8),
		CacheReadTokens: f(128),
	})
	if cost == nil || *cost != 0.0000172571 {
		t.Fatalf("M3.1-Flash cost = %v want 0.0000172571（¥0.0001208）", cost)
	}
}
