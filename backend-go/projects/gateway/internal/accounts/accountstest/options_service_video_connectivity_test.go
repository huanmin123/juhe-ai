package accountstest

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// 媒体域连通性探测（2026-11-01 用户裁决）的人工测试选项断言：视频目录行
// （mode=video / 协议 video）经 video_create 进入人工测试选项的可执行形态
// （testEndpointModes），此前「账户测试参数无效」的词表拦截随之解除；
// audio 行整体排除与图像行 images_json 形态保持既有契约不变。
func TestVideoConnectivityManualTestOptions(t *testing.T) {
	openAIAPIKey := ManualTestModeSource{
		ProviderCode:    "volcengine",
		ProtocolCode:    accountscore.OpenAIProtocolCode,
		ProtocolVersion: "v1",
		AccountType:     "api_key",
	}
	ctx := context.Background()
	cache := map[string]*TestCatalogItem{}

	t.Run("视频目录行的 testEndpointModes 携带 video_create", func(t *testing.T) {
		item := TestCatalogItem{Model: "doubao-seedance-1-0", Mode: "video", SupportedAPIProtocols: []string{"video"}}
		if !IsAccountManualTestModel(item, openAIAPIKey) {
			t.Fatal("视频行必须进入人工测试候选")
		}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, item, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(modes) != 1 || modes[0] != "video_create" {
			t.Fatalf("视频行 testEndpointModes 应只含 video_create：%v", modes)
		}
	})

	t.Run("声明对话与视频协议的视频行只产出 video_create（对话面排除）", func(t *testing.T) {
		item := TestCatalogItem{Model: "video-multi", Mode: "video", SupportedAPIProtocols: []string{"video", "chat_completions"}}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, item, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		for _, mode := range modes {
			if mode != "video_create" {
				t.Fatalf("视频行不得携带对话族测试形态：%v", modes)
			}
		}
		if len(modes) != 1 {
			t.Fatalf("视频行测试形态应只余 video_create：%v", modes)
		}
	})

	t.Run("图像行 images_json 与 audio 行排除契约不变", func(t *testing.T) {
		image := TestCatalogItem{Model: "seedream", Mode: "image_generation", SupportedAPIProtocols: []string{"images"}}
		modes, err := (&Service{}).manualTestEndpointModesForTargetModel(ctx, openAIAPIKey, image, "", cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(modes) != 1 || modes[0] != "images_json" {
			t.Fatalf("图像行测试形态应只余 images_json：%v", modes)
		}
		audio := TestCatalogItem{Model: "doubao-tts", Mode: "audio", SupportedAPIProtocols: []string{"audio_speech"}}
		if IsAccountManualTestModel(audio, openAIAPIKey) {
			t.Fatal("audio 行保持整体排除（既有契约）")
		}
	})
}
