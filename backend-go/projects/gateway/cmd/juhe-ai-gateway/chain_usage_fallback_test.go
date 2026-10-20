package main

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
)

// T9c：组合根 hook 必须从客户端请求体取估算事实并按协议驱动分派。
func TestStreamUsageFallbackHookUsesClientRequestBody(t *testing.T) {
	body := map[string]any{
		"model": "mock-model",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello world, please estimate me"},
		},
	}
	req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		RawBody: []byte(`{"model":"mock-model","messages":[{"role":"user","content":"hello world, please estimate me"}]}`),
		Body:    body,
	}}
	hook := streamUsageFallbackHookOf(req)
	if hook == nil {
		t.Fatal("hook must be wired for a request with body")
	}
	next, estimated, estimatedInput, estimatedOutput := hook(
		gatewayresponse.NewOpenAIResponseDriver(),
		gatewayproto.EmptyUsage(),
		gatewayresponse.UsageFallbackInput{
			Completed:             true,
			OutputReceived:        true,
			EstimatedOutputTokens: 7,
			HasEstimatedOutput:    true,
		},
	)
	if !estimated {
		t.Fatal("fallback did not estimate missing usage")
	}
	if estimatedInput == nil || *estimatedInput <= 0 {
		t.Fatalf("estimated input = %v, want positive", estimatedInput)
	}
	if estimatedOutput == nil || *estimatedOutput != 7 {
		t.Fatalf("estimated output = %v, want 7", estimatedOutput)
	}
	if next.InputTokens == nil || *next.InputTokens != *estimatedInput {
		t.Fatalf("usage input = %v, want %d", next.InputTokens, *estimatedInput)
	}
}

// req 缺失时 hook 保持「缺省恒等」，不得因 nil 解引用阻断响应收尾。
func TestStreamUsageFallbackHookNilRequest(t *testing.T) {
	if hook := streamUsageFallbackHookOf(nil); hook != nil {
		t.Fatal("nil request must yield nil hook")
	}
}
