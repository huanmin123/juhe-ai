package managementmodelcheckoptions

import (
	"reflect"
	"testing"
)

func TestServiceOptionsMatchesNodeContract(t *testing.T) {
	result := NewService().Options()

	wantModels := []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
		"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "glm-5.1",
		"claude-opus-4-8", "claude-opus-4-7", "gemini-3.5-flash", "gemini-3.1-pro-preview",
	}
	gotModels := make([]string, 0, len(result.SupportedModels))
	for _, item := range result.SupportedModels {
		gotModels = append(gotModels, item.Value)
		if item.Label != item.Value || item.Description != "" {
			t.Fatalf("model option = %+v", item)
		}
	}
	if !reflect.DeepEqual(gotModels, wantModels) {
		t.Fatalf("supported models = %#v, want %#v", gotModels, wantModels)
	}
	if result.DefaultModel != wantModels[0] || result.DefaultProfile != "full" {
		t.Fatalf("defaults = %q/%q", result.DefaultModel, result.DefaultProfile)
	}
	if len(result.SupportedProfiles) != 1 {
		t.Fatalf("supported profiles = %#v", result.SupportedProfiles)
	}
	profile := result.SupportedProfiles[0]
	if profile.Value != "full" || profile.Label != "强诊断完整检测" || profile.Description != "准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针" {
		t.Fatalf("profile = %+v", profile)
	}
	if result.TrustedComparison.EnabledByDefault || !result.TrustedComparison.Available || result.TrustedComparison.UnavailableReason != "" {
		t.Fatalf("trusted comparison = %+v", result.TrustedComparison)
	}
	wantMessage := "可信对比默认关闭；选择一个你信任的可用 OpenAI Responses / OpenAI Chat Completions / Anthropic Messages / Gemini native 协议账户后，会额外消耗该账户额度"
	if result.TrustedComparison.Message != wantMessage {
		t.Fatalf("trusted comparison message = %q", result.TrustedComparison.Message)
	}
}

func TestServiceOptionsReturnsIndependentSlices(t *testing.T) {
	service := NewService()
	first := service.Options()
	first.SupportedModels[0].Value = "mutated"
	first.SupportedProfiles[0].Label = "mutated"

	second := service.Options()
	if second.SupportedModels[0].Value != "gpt-5.6-sol" || second.SupportedProfiles[0].Label != "强诊断完整检测" {
		t.Fatalf("options leaked caller mutation: %+v", second)
	}
}
