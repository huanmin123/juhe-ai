package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// bodyAttachedRequest 构造带 body 捕获态的 GatewayRequest（对齐
// chain_driver_gemini_mapped_url_test 的构造方式；ParsedJSONObjectBody 依赖
// body 管道捕获态）。
func bodyAttachedRequest(t *testing.T, method, target, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

// upstreamModelObserverEngine 装配与组合根一致的引擎：真实桥响应转换器 +
// gatewayobs 观察钩子（经 newChainUpstreamResponseModelObserver）。
func upstreamModelObserverEngine() *gatewaydispatch.Engine {
	engine := gatewaydispatch.NewEngine(nil, nil)
	engine.ResponseTransformer = newChainBridgeResponseTransformer()
	engine.ObserveUpstreamResponseModel = newChainUpstreamResponseModelObserver()
	return engine
}

func sseModelUpstream(t *testing.T, contentType, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestUpstreamModelObservedGeminiNativeRegression 钉住 gemini 原生路径回归：
// 无映射的 gemini 账户仍按 gemini 协议观察 modelVersion（2026-11-07 桥退役：
// 原桥路径观察归因用例随桥删除；gemini 映射路径的观察由引擎直通承载）。
func TestUpstreamModelObservedGeminiNativeRegression(t *testing.T) {
	server := sseModelUpstream(t, "text/event-stream",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}],\"modelVersion\":\"gemini-native-obs-v3\"}\n\n")
	engine := upstreamModelObserverEngine()

	request := bodyAttachedRequest(t, http.MethodPost, "/v1beta/models/gem-1:streamGenerateContent?alt=sse",
		`{"contents":[{"parts":[{"text":"ping"}]}]}`)
	slot := &gatewaydispatch.UpstreamResponseModelSlot{}
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), gatewaydispatch.AttemptInput{
		Req: request,
		Account: gatewaydispatch.AccountCandidate{
			ID: "acc_gemini_native", VendorCode: "google", ProtocolCode: "gemini", Type: "api_key",
		},
		UpstreamURL:               server.URL + "/v1beta/models/gem-1:streamGenerateContent?alt=sse",
		Headers:                   http.Header{},
		Body:                      []byte(`{"contents":[{"parts":[{"text":"ping"}]}]}`),
		Signal:                    context.Background(),
		TimeoutProfile:            gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
		UpstreamResponseModelSlot: slot,
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "modelVersion") {
		t.Fatalf("原生透传必须保留 modelVersion 载荷")
	}
	if slot.Get() != "gemini-native-obs-v3" {
		t.Fatalf("gemini 原生路径必须观察 modelVersion, got %q", slot.Get())
	}
}

// TestUpstreamModelObservedOpenAIRegression 钉住 openai 原生路径回归：
// chat completions 上游仍按 openai 协议观察 model 字段。
func TestUpstreamModelObservedOpenAIRegression(t *testing.T) {
	server := sseModelUpstream(t, "text/event-stream",
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-real-9\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"+
			"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-real-9\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	engine := upstreamModelObserverEngine()

	request := bodyAttachedRequest(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"client-chat","stream":true}`)
	slot := &gatewaydispatch.UpstreamResponseModelSlot{}
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), gatewaydispatch.AttemptInput{
		Req: request,
		Account: gatewaydispatch.AccountCandidate{
			ID: "acc_openai_native", VendorCode: "gpt", ProtocolCode: "openai", Type: "api_key",
		},
		UpstreamURL:               server.URL + "/v1/chat/completions",
		Headers:                   http.Header{},
		Body:                      []byte(`{"model":"client-chat","stream":true}`),
		Signal:                    context.Background(),
		TimeoutProfile:            gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
		UpstreamResponseModelSlot: slot,
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if slot.Get() != "gpt-real-9" {
		t.Fatalf("openai 原生路径必须观察 model, got %q", slot.Get())
	}
}
