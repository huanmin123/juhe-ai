package accountstest

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

func TestResolveAccountConnectivitySelection(t *testing.T) {
	service := &Service{}
	cases := []struct {
		name      string
		context   *ManualTestContext
		wantModel string
		wantMode  string
		wantErr   string
	}{
		{
			name: "video uses health check mode",
			context: &ManualTestContext{
				CapabilityDomain:        accountscore.CapabilityDomainVideo,
				HealthCheckModel:        "video-model",
				HealthCheckEndpointMode: "video_create",
			},
			wantModel: "video-model",
			wantMode:  "video_create",
		},
		{
			name: "image falls back to images_json",
			context: &ManualTestContext{
				CapabilityDomain:        accountscore.CapabilityDomainImage,
				HealthCheckModel:        "image-model",
				HealthCheckEndpointMode: "chat_sse",
			},
			wantModel: "image-model",
			wantMode:  "images_json",
		},
		{
			name: "audio job mode stays audio_job_get",
			context: &ManualTestContext{
				CapabilityDomain:        accountscore.CapabilityDomainAudio,
				HealthCheckModel:        "audio-model",
				HealthCheckEndpointMode: "audio_job_get",
			},
			wantModel: "audio-model",
			wantMode:  "audio_job_get",
		},
		{
			name: "chat is rejected",
			context: &ManualTestContext{
				CapabilityDomain: accountscore.CapabilityDomainChat,
				HealthCheckModel: "gpt-model",
			},
			wantErr: "对话账户请使用模型测试",
		},
		{
			name: "missing model is rejected",
			context: &ManualTestContext{
				CapabilityDomain: accountscore.CapabilityDomainVideo,
			},
			wantErr: "账户缺少检查模型",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, mode, err := service.ResolveAccountConnectivitySelection(tc.context)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAccountConnectivitySelection: %v", err)
			}
			if model != tc.wantModel || mode != tc.wantMode {
				t.Fatalf("got (%q,%q), want (%q,%q)", model, mode, tc.wantModel, tc.wantMode)
			}
		})
	}
}

func TestIsAccountManualTestModelAllowsAudioOnlyForAudioDomain(t *testing.T) {
	item := TestCatalogItem{Mode: "audio", Model: "tts-1", SupportedAPIProtocols: []string{"audio"}}
	audioSource := ManualTestModeSource{
		CapabilityDomain:       accountscore.CapabilityDomainAudio,
		SupportedEndpointModes: []string{"audio_speech"},
	}
	if !IsAccountManualTestModel(item, audioSource) {
		t.Fatal("audio domain with audio_speech must be testable")
	}
	chatSource := ManualTestModeSource{
		CapabilityDomain:       accountscore.CapabilityDomainChat,
		SupportedEndpointModes: []string{"audio_speech"},
	}
	if IsAccountManualTestModel(item, chatSource) {
		t.Fatal("chat domain must not accept audio catalog rows")
	}
}
