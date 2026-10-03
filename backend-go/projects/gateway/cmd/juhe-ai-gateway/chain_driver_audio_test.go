package main

// M1 同步音频 chain 侧接线测试（音频设计 §5/契约 §5.1）：gemini 账户的
// speech 请求经 gatewaymedia adapter 改写 URL/body；openai 族账户保持直连
// 透传（/v1 前缀 + 原样 body）；capability gate 对 gemini speech 放行、对
// gemini transcription（M1 无 adapter）仍按原生面淘汰。
import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestChainDriverGeminiSpeechAdapterDispatch(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_gem_tts",
		ProviderCode: "gemini",
		ProtocolCode: "gemini",
		Type:         "api_key",
		BaseURL:      "https://gem.example",
		APIKey:       "gk-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"gemini-2.5-flash-preview-tts","input":"Say cheerfully: hi","voice":"Kore","response_format":"pcm"}`)

	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://gem.example/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("gemini speech url = %#v", urls)
	}

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if !strings.Contains(string(parts.Body), `"responseModalities":["AUDIO"]`) {
		t.Fatalf("body must carry AUDIO modality: %s", parts.Body)
	}
	if !strings.Contains(string(parts.Body), `"voiceName":"Kore"`) {
		t.Fatalf("body must carry voiceName: %s", parts.Body)
	}
	if got := parts.Headers.Get("X-Goog-Api-Key"); got != "gk-1" {
		t.Fatalf("gemini auth header = %q", got)
	}

	// capability：gemini speech 放行（后续 endpoint mode / model 门继续生效）。
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason == "gemini_native_unsupported" {
		t.Fatal("speech shape must not be rejected by the native-path gate")
	}
	// gemini 的非 audio_speech mode 过滤沿既有语义（空集不受限）。
	if reason := driver.endpointModeMismatchReason(req, account, ""); reason != "" {
		t.Fatalf("empty mode set must stay unconstrained, got %q", reason)
	}
}

func TestChainDriverGeminiSpeechFormatGate(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_gem_tts", ProviderCode: "gemini", ProtocolCode: "gemini",
		Type: "api_key", BaseURL: "https://gem.example", APIKey: "gk-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"Kore","response_format":"mp3"}`)
	if _, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req); err == nil ||
		!strings.Contains(err.Error(), "pcm") {
		t.Fatalf("non-pcm format must be rejected with the pcm boundary, got %v", err)
	}
}

func TestChainDriverGeminiTranscriptionStillNativeGated(t *testing.T) {
	// M1 无 gemini STT adapter：/v1/audio/transcriptions 对 gemini 账户仍按
	// 原生面淘汰（gemini_native_unsupported）。
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_gem", ProviderCode: "gemini", ProtocolCode: "gemini",
		Type: "api_key", BaseURL: "https://gem.example", APIKey: "gk-1",
	}
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil))
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, account, ""); reason != "gemini_native_unsupported" {
		t.Fatalf("transcription on gemini must stay native-gated, got %q", reason)
	}
}

func TestChainDriverOpenAISpeechStaysPassthrough(t *testing.T) {
	// openai 族：/v1-suffixed base + 原样客户端 body（无 adapter 改写）。
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_oai", ProviderCode: "gpt", ProtocolCode: "openai",
		Type: "api_key", BaseURL: "https://api.openai.example", APIKey: "sk-1",
	}
	body := `{"model":"tts-1","input":"hi","voice":"alloy","response_format":"wav"}`
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech", body)

	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://api.openai.example/v1/audio/speech" {
		t.Fatalf("openai speech url = %#v", urls)
	}
	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if string(parts.Body) != body {
		t.Fatalf("openai speech body must pass through verbatim, got %s", parts.Body)
	}
}
