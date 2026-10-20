package pricing

import (
	"reflect"
	"testing"
)

// T18：覆盖能力解析的 gpt 候选链必须与价格目录的 OpenAI builder 单一同源，
// 同时保留「请求模型本体在前」的解析契约。
func TestModelCandidatesForRequestOverrideOpenAIGolden(t *testing.T) {
	cases := []string{
		"gpt-5.6",
		"gpt-5.6-sol-2026-01-02",
		"gpt-5.4-mini-20260102",
		"gpt-4.1-nano-preview",
		"gpt-image-2-2026-03-01",
		"gpt-5.3-codex",
		"unknown-model",
	}
	for _, model := range cases {
		got := ModelCandidatesForRequestOverride("gpt", model)
		want := []string{model}
		seen := map[string]bool{model: true}
		for _, candidate := range buildOpenAIModelCandidates(model) {
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			want = append(want, candidate)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("model %q candidates = %v, want %v", model, got, want)
		}
	}
}

func TestModelCandidatesForRequestOverrideNonGPTKeepsIdentityContract(t *testing.T) {
	if got := ModelCandidatesForRequestOverride("anthropic", " claude-x "); !reflect.DeepEqual(got, []string{" claude-x ", "claude-x"}) {
		t.Fatalf("anthropic candidates = %v", got)
	}
	if got := ModelCandidatesForRequestOverride("deepseek", "deepseek-v4"); !reflect.DeepEqual(got, []string{"deepseek-v4"}) {
		t.Fatalf("deepseek candidates = %v", got)
	}
}
