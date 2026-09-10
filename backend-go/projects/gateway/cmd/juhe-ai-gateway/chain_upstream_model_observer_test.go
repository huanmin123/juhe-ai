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

// TestUpstreamModelObservedBeforeBridgeTransformReachesSlot 钉住桥路径归因
// （P2 核心）：responses -> gemini 桥请求下，观察挂在桥转换之前的原始上游流
// 上——slot 归因的是上游原生 modelVersion，而不是空值、映射名或转换后的
// 客户端形态；发布值经 slot Bind 进入响应快照（usage/audit 的
// UpstreamResponseModel 数据源）。
func TestUpstreamModelObservedBeforeBridgeTransformReachesSlot(t *testing.T) {
	server := sseModelUpstream(t, "text/event-stream",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}],\"modelVersion\":\"real-upstream-model-v1\"}\n\n")
	engine := upstreamModelObserverEngine()

	request := bodyAttachedRequest(t, http.MethodPost, "/v1/responses",
		`{"model":"client-model","stream":true,"input":"hi"}`)
	slot := &gatewaydispatch.UpstreamResponseModelSlot{}
	response, err := engine.PerformUpstreamRequestAttempt(context.Background(), gatewaydispatch.AttemptInput{
		Req:                       request,
		Account:                   bridgeStreamMappingAccount(),
		UpstreamURL:               server.URL + "/v1/responses",
		Headers:                   http.Header{},
		Body:                      []byte(`{"model":"client-model","stream":true,"input":"hi"}`),
		Signal:                    context.Background(),
		TimeoutProfile:            gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
		UpstreamResponseModelSlot: slot,
	})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}

	// 排空转换后的下游流（驱动 pump 消费原始上游体至 EOF，发布先于下游
	// EOF）。
	transformed, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read transformed body: %v", err)
	}
	text := string(transformed)
	if !strings.Contains(text, "event: response.completed") {
		t.Fatalf("桥转换必须产出 Responses SSE, got %q", text)
	}
	// 归因不来自转换后的客户端形态：下游载荷不含上游原生 modelVersion。
	if strings.Contains(text, "real-upstream-model-v1") {
		t.Fatalf("转换后的客户端载荷不应携带上游原生 modelVersion")
	}
	if slot.Get() != "real-upstream-model-v1" {
		t.Fatalf("slot 必须归因上游原始 modelVersion, got %q", slot.Get())
	}

	// chain 响应面消费契约：Bind 把发布接入响应快照（发布先于绑定也要能看到）。
	snapshotModel := ""
	slot.Bind(func(model string) { snapshotModel = model })
	if snapshotModel != "real-upstream-model-v1" {
		t.Fatalf("响应快照必须拿到上游原始模型, got %q", snapshotModel)
	}
}

// TestUpstreamModelObservedGeminiNativeRegression 钉住 gemini 原生路径回归：
// 无桥映射的 gemini 账户仍按 gemini 协议观察 modelVersion。
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
			ID: "acc_gemini_native", ProviderCode: "google", ProtocolCode: "gemini", Type: "api_key",
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
			ID: "acc_openai_native", ProviderCode: "gpt", ProtocolCode: "openai", Type: "api_key",
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
