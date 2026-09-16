package modelcheckprofile

import (
	"reflect"
	"testing"
)

// w12a_profile_test.go 补齐 Find 未命中、PairedModel 回退与 SourceEndpointFamilies
// 的 default 分支。

func TestW12aFindUnknownProviderAndProfile(t *testing.T) {
	if _, ok := Find("w12a-unknown", "profile_x"); ok {
		t.Fatalf("未知 provider 不应命中")
	}
	if _, ok := Find("gpt", "profile_unknown"); ok {
		t.Fatalf("未知 profile 不应命中")
	}
	profile, ok := Find("  GPT  ", " PROFILE_GPT_OPENAI_V1 ")
	if !ok || profile.ID != "openai_responses_strong" {
		t.Fatalf("大小写与空白应被归一化: %#v %v", profile, ok)
	}
	if _, ok := FindForModel("gpt", "profile_x", DefaultModel); ok {
		t.Fatalf("未知 profile 的模型查找不应命中")
	}
	if _, ok := FindForModel("gpt", "profile_gpt_openai_v1", "not-a-model"); ok {
		t.Fatalf("不在档案内模型不应命中")
	}
}

func TestW12aPairedModelFallbacks(t *testing.T) {
	profile := ProtocolProfile{Models: []string{"m-a", "m-b"}}
	// 无配对表项时回退到第一个不同模型。
	if got := PairedModel(profile, "m-a"); got != "m-b" {
		t.Fatalf("应回退到另一模型: %q", got)
	}
	// 单模型档案 → 空串。
	single := ProtocolProfile{Models: []string{"only"}}
	if got := PairedModel(single, "only"); got != "" {
		t.Fatalf("单模型应返回空: %q", got)
	}
	// 配对表项存在且在档案内 → 直接返回配对。
	pairedProfile := ProtocolProfile{Models: []string{"gpt-5.6-sol", "gpt-5.6-terra"}}
	if got := PairedModel(pairedProfile, "gpt-5.6-sol"); got != "gpt-5.6-terra" {
		t.Fatalf("配对模型应优先: %q", got)
	}
	// 配对表项存在但不在档案内 → 回退。
	mixed := ProtocolProfile{Models: []string{"glm-5.2", "glm-5.1"}}
	if got := PairedModel(mixed, "gpt-5.6-sol"); got != "glm-5.2" {
		t.Fatalf("配对不在档案内应回退: %q", got)
	}
}

func TestW12aSourceEndpointFamiliesDefault(t *testing.T) {
	if got := SourceEndpointFamilies(ProtocolProfile{Protocol: "w12a-unknown"}); got != nil {
		t.Fatalf("未知协议应返回 nil: %#v", got)
	}
	gemini := SourceEndpointFamilies(ProtocolProfile{Protocol: ProtocolGeminiNative})
	if !reflect.DeepEqual(gemini, []EndpointFamily{EndpointGenerateContent, EndpointStreamGenerate}) {
		t.Fatalf("gemini 家族不符: %#v", gemini)
	}
}

func TestW12aProfilesReturnsClones(t *testing.T) {
	first := Profiles()
	first[0].Models[0] = "w12a-mutated"
	first[0].ProviderProtocolProfileIDs[0] = "w12a-mutated"
	second := Profiles()
	if second[0].Models[0] == "w12a-mutated" || second[0].ProviderProtocolProfileIDs[0] == "w12a-mutated" {
		t.Fatalf("Profiles 应返回深拷贝: %#v", second[0])
	}
	if SupportedModels() == nil || len(SupportedModels()) < 10 {
		t.Fatalf("SupportedModels 应聚合目录模型")
	}
}
