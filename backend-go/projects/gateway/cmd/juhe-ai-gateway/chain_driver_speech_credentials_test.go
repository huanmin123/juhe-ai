package main

// 2026-11-06 深挖复审 F2：缺 speech_token 的火山语音账户不得作为语音请求
// 候选（否则候选循环在派发后以终态 400 阻断整组）；带凭据账户与非语音请求
// 不受该门影响。
import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func TestChainDriverVolcengineSpeechCredentialCandidateGate(t *testing.T) {
	driver := newChainProviderDriver()
	base := gatewaydispatch.AccountCandidate{
		ID:                     "acc_volc_speech",
		VendorCode:             "volcengine",
		ProtocolCode:           "openai",
		Type:                   "api_key",
		BaseURL:                "https://openspeech.example",
		SupportedEndpointModes: []string{"audio_speech", "audio_job_create"},
		SupportedModels:        []string{"seed-tts-2.0", "volc.seedasr.auc"},
	}

	speechReq := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"seed-tts-2.0","input":"hi","voice":"v"}`)
	if got := driver.gatewayRequestCapabilityMismatchReasonFor(speechReq, base, ""); got != "speech_credentials_missing" {
		t.Fatalf("缺 speech_token 的语音候选 reason = %q, want speech_credentials_missing", got)
	}

	jobsReq := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/jobs",
		`{"model":"volc.seedasr.auc","input_url":"https://example.com/a.wav"}`)
	if got := driver.gatewayRequestCapabilityMismatchReasonFor(jobsReq, base, ""); got != "speech_credentials_missing" {
		t.Fatalf("缺 speech_token 的长音频候选 reason = %q, want speech_credentials_missing", got)
	}

	withToken := base
	withToken.Credentials = map[string]any{"speech_token": "tok-1"}
	if got := driver.gatewayRequestCapabilityMismatchReasonFor(speechReq, withToken, ""); got != "" {
		t.Fatalf("带 speech_token 的账户不得被凭据门淘汰, got %q", got)
	}

	// 非语音请求不消费 speech_token：即使缺凭据也不得返回该 reason。
	chatReq := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"seed-tts-2.0","messages":[{"role":"user","content":"hi"}]}`)
	if got := driver.gatewayRequestCapabilityMismatchReasonFor(chatReq, base, ""); got == "speech_credentials_missing" {
		t.Fatal("chat 请求不应被语音凭据门淘汰")
	}
}
