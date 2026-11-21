package pricing

import (
	"math"
	"testing"
)

// 2026-11-20 向量/重排协议补齐批：目录行的 mode 是 embedding/rerank，
// 只有输入价、没有输出 token 维度。这里直接走真实目录查找与结算路径，
// 防止后续把 rerank 行误判成无价/媒体行或要求输出价。
func TestBuildCostBreakdownVectorRowsBillInputTokensOnly(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		// 0.8 元/百万 tokens ÷ 7.0
		{model: "gte-rerank-v2", want: 0.11428571},
		// 0.5 元/百万 tokens ÷ 7.0
		{model: "text-embedding-v4", want: 0.07142857},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			row := FindProviderModelPricing("qwen", tc.model)
			if row == nil {
				t.Fatalf("qwen %s pricing row missing", tc.model)
			}
			tokens := 1_000_000.0
			breakdown := BuildCostBreakdown(row, CostInput{
				ProviderCode: "qwen",
				Model:        tc.model,
				InputTokens:  &tokens,
			})
			if breakdown == nil || breakdown.AccountChargeUsd == nil {
				t.Fatalf("qwen %s breakdown = %+v, want account charge", tc.model, breakdown)
			}
			if got := *breakdown.AccountChargeUsd; math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("qwen %s account charge = %v, want %v", tc.model, got, tc.want)
			}
			if breakdown.InputCostUsd == nil {
				t.Fatalf("qwen %s input line missing", tc.model)
			}
			if breakdown.OutputCostUsd != nil {
				t.Fatalf("qwen %s output cost = %v, want nil (no output token dimension)", tc.model, *breakdown.OutputCostUsd)
			}
		})
	}
}
