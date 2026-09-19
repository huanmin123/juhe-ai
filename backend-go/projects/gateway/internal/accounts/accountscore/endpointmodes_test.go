package accountscore

// endpointmodes_test.go 钉住 images_json 能力修复的词表契约：images_json 是
// openai 族的合法模式（图文问答 generate_image 工具回环 /v1/images 依赖它），
// 但默认集仍只有四个 chat/responses 模式（images_json 只能显式开启）。

import "testing"

func TestOpenAIEndpointModeValuesIncludeImages(t *testing.T) {
	if !IsOpenAIEndpointMode("images_json") {
		t.Fatal("images_json 应是 openai 族合法上游接口能力")
	}
	want := []string{"images_json", "chat_json", "chat_sse", "responses_json", "responses_sse"}
	if len(OpenAIEndpointModeValues) != len(want) {
		t.Fatalf("openai 模式表长度不一致：%v", OpenAIEndpointModeValues)
	}
	for index, mode := range want {
		if OpenAIEndpointModeValues[index] != mode {
			t.Fatalf("openai 模式表顺序不一致（images_json 居首，镜像健康检查顺序）：%v", OpenAIEndpointModeValues)
		}
	}
}

func TestOpenAIDefaultEndpointModesExcludeImages(t *testing.T) {
	cases := []ModeDefaultContext{
		{ProviderCode: GptVendorCode, AccountType: "api_key"},
		{ProviderCode: DeepSeekProviderCode, AccountType: "api_key"},
		{}, // 未知供应商回落分支。
	}
	for _, input := range cases {
		defaults := DefaultOpenAIEndpointModes(input)
		if len(defaults) != 4 {
			t.Fatalf("新账户默认集应保持四个 chat/responses 模式：%v", defaults)
		}
		for _, mode := range defaults {
			if mode == "images_json" {
				t.Fatalf("默认集不得包含 images_json（只能显式开启）：%v", defaults)
			}
		}
	}
	oauth := DefaultOpenAIEndpointModes(ModeDefaultContext{AccountType: "oauth"})
	if len(oauth) != 2 || oauth[0] != "responses_json" {
		t.Fatalf("oauth 默认仍是 responses 对：%v", oauth)
	}
}

func TestHybridEndpointModeValuesFollowOpenAIUnion(t *testing.T) {
	if !IsHybridEndpointMode("images_json") {
		t.Fatal("hybrid 词表是三族并集，应随 openai 族扩展包含 images_json")
	}
	if HybridEndpointModeValues[0] != "images_json" {
		t.Fatalf("hybrid 并集中 images_json 应随 openai 族居首：%v", HybridEndpointModeValues)
	}
}
