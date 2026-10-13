package accountstest

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// v8-C 对话域纯粹化（模型选择器按域过滤根治）定向测试：
// 对话协议测试面（chat_completions/responses/messages/generate_content 族）排除
// 目录 mode ∈ {image_generation, video} 的行；mode=audio 整体排除保持不变；
// images_json 测试面的图像模型分支（testModelSupportsImagesProtocol）不受影响。
func TestV8CManualTestModelChatFaceExcludesImageVideoRows(t *testing.T) {
	openAIAPIKey := ManualTestModeSource{
		ProviderCode: "gpt",
		ProtocolCode: accountscore.OpenAIProtocolCode,
		// OpenAI v1 档案（IsOpenAIProtocolProfileOf 按 code+version 判定）。
		ProtocolVersion: "v1",
		AccountType:     "api_key",
	}
	ctx := context.Background()
	cache := map[string]*TestCatalogItem{}

	t.Run("图像行经 images 分支保留 eligible，但测试形态只余 images_json", func(t *testing.T) {
		item := TestCatalogItem{Model: "gpt-image-2", Mode: "image_generation", SupportedAPIProtocols: []string{"images"}}
		if !IsAccountManualTestModel(item, openAIAPIKey) {
			t.Fatal("images_json 测试面的图像模型分支必须保留（api_key + images 协议）")
		}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, item, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(modes) != 1 || modes[0] != "images_json" {
			t.Fatalf("图像行测试形态应只余 images_json：%v", modes)
		}
	})

	t.Run("声明 responses 的图像行不得再获得对话测试形态", func(t *testing.T) {
		item := TestCatalogItem{Model: "gpt-image-multi", Mode: "image_generation", SupportedAPIProtocols: []string{"images", "responses"}}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, item, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(modes) != 1 || modes[0] != "images_json" {
			t.Fatalf("image_generation 行不得携带 responses_json 等对话形态：%v", modes)
		}
	})

	t.Run("视频行整体不可测", func(t *testing.T) {
		if IsAccountManualTestModel(TestCatalogItem{Model: "sora-2", Mode: "video", SupportedAPIProtocols: []string{"video"}}, openAIAPIKey) {
			t.Fatal("video 行不得进入对话测试候选")
		}
		if IsAccountManualTestModel(TestCatalogItem{Model: "sora-2", Mode: "video"}, openAIAPIKey) {
			t.Fatal("video 行空协议默认放行必须排除")
		}
	})

	t.Run("gemini 声明 generate_content 的图像行被对话面排除", func(t *testing.T) {
		gemini := ManualTestModeSource{ProviderCode: "gemini", ProtocolCode: accountscore.GeminiProtocolCode, ProtocolVersion: "v1beta"}
		if IsAccountManualTestModel(TestCatalogItem{Model: "gemini-image", Mode: "image_generation", SupportedAPIProtocols: []string{"generate_content", "stream_generate_content"}}, gemini) {
			t.Fatal("gemini 图像行不得经 generate_content 进入对话测试候选")
		}
	})

	t.Run("audio 行整体排除与 chat 行放行保持不变", func(t *testing.T) {
		if IsAccountManualTestModel(TestCatalogItem{Model: "tts-1", Mode: "audio", SupportedAPIProtocols: []string{"audio_speech"}}, openAIAPIKey) {
			t.Fatal("audio 行保持整体排除")
		}
		chat := TestCatalogItem{Model: "gpt-4o", Mode: "chat", SupportedAPIProtocols: []string{"chat_completions", "responses"}}
		if !IsAccountManualTestModel(chat, openAIAPIKey) {
			t.Fatal("chat 行保持放行")
		}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, chat, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(modes) == 0 {
			t.Fatal("chat 行必须保留对话测试形态")
		}
	})

	t.Run("启用的模型映射继续放行图像行（hybrid 媒体映射面不受影响）", func(t *testing.T) {
		source := openAIAPIKey
		enabled := true
		source.ModelMappings = []accountscore.ModelMapping{{SourceModel: "mapped-image", Enabled: &enabled}}
		if !IsAccountManualTestModel(TestCatalogItem{Model: "mapped-image", Mode: "image_generation", SupportedAPIProtocols: []string{"images"}}, source) {
			t.Fatal("启用映射的模型应保持可测")
		}
	})
}
