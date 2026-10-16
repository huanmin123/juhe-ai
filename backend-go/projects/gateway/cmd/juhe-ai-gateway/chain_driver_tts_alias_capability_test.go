package main

import (
	"testing"

	gatewaybody "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	gatewaydispatch "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	gatewaypreauth "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// volcengine TTS 占位名↔现役名等价组在 driver 能力判定链的接入钉子（契约
// §9.2「派发匹配等价」）：声明 doubao-tts 的账户对 seed-audio-1.0 请求在
// gatewayRequestCapabilityMismatchReasonFor 放行（真实复现断层修复——缺本
// 判定时该组合以 model_unsupported 被能力链拒绝，整组候选清空后表现为 503
// endpoint_mode_unsupported 通用文案，2026-10-17 实测回填批）。
func TestChainDriverVolcengineTTSAliasSpeechCapabilityPassthrough(t *testing.T) {
	d := &chainProviderDriver{}
	req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		RawBody: []byte(`{"model":"seed-audio-1.0","input":"hi","voice":"v1"}`),
	}}
	account := gatewaydispatch.AccountCandidate{
		ID:                     "acc_test",
		ProviderCode:           "volcengine",
		ProtocolCode:           "openai",
		SupportedModels:        []string{"doubao-tts"},
		SupportedEndpointModes: []string{"audio_speech"},
	}
	reason := d.gatewayRequestCapabilityMismatchReasonFor(req, account, "")
	if reason != "" {
		t.Fatalf("expected empty mismatch reason, got %q", reason)
	}
}
