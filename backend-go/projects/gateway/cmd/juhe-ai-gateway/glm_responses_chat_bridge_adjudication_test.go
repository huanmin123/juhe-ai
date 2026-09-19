package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// GLM 三协议适配方案 P1 裁决测试（响应侧）。
//
// 背景：GLM chat 账户（openai 协议档案）显式配置 responses -> chat_completions
// 模型映射后，链上响应变换器（chain_bridge_response.go）是 chat 上游回转
// Responses 客户端的唯一出口。本测试用运行时证据裁决：chat SSE/JSON 上游
// 响应是否真的被回转成 Responses 形态。若原样透传 choices/[DONE]，说明
// 响应侧门控未放行该组合，桥端到端断裂。
func glmBridgeAdjudicationAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:                        "acc_glm_bridge_adjudication",
		ProviderCode:              "glm",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		ProviderProtocolProfileID: "profile_glm_coding_openai_v1",
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "client-model",
			SourceEndpointFamily:   "responses",
			UpstreamModel:          "glm-upstream",
			UpstreamEndpointFamily: "chat_completions",
			Enabled:                true,
		}},
	}
}

func adjMustJSONMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("测试 body 非法: %v", err)
	}
	return parsed
}

func adjUpstreamResponse(contentType string, body io.ReadCloser) *gatewaydispatch.GatewayUpstreamResponse {
	header := http.Header{}
	header.Set("Content-Type", contentType)
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK, header, body)
}

func glmBridgeAdjudicationInput(t *testing.T, stream bool, response *gatewaydispatch.GatewayUpstreamResponse) gatewaydispatch.UpstreamResponseTransformInput {
	t.Helper()
	streamText := "false"
	if stream {
		streamText = "true"
	}
	body := `{"model":"client-model","stream":` + streamText + `,"input":"hi"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: adjMustJSONMap(t, body)}
	return gatewaydispatch.UpstreamResponseTransformInput{
		Req:      req,
		Account:  glmBridgeAdjudicationAccount(),
		Response: response,
	}
}

func TestAdjudicateGLMResponsesToChatBridgeResponseConversion(t *testing.T) {
	// 守护 responses -> chat_completions 响应侧回转契约（BUG-0178 修复回归）。
	transformer := newChainBridgeResponseTransformer()

	t.Run("流式", func(t *testing.T) {
		upstreamSSE := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
		input := glmBridgeAdjudicationInput(t, true, adjUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(upstreamSSE))))
		out, err := transformer.TransformUpstreamResponseForAccount(input)
		if err != nil {
			t.Fatalf("TransformUpstreamResponseForAccount error: %v", err)
		}
		clientBody, readErr := io.ReadAll(out.Body)
		if readErr != nil {
			t.Fatalf("读取客户端响应失败: %v", readErr)
		}
		text := string(clientBody)
		if !strings.Contains(text, "response.") {
			t.Fatalf("客户端未收到 Responses SSE（响应未被桥回转，chat SSE 原样透传），实际内容: %s", text)
		}
		t.Logf("裁决通过：客户端收到 Responses SSE，长度 %d", len(text))
	})

	t.Run("缓冲", func(t *testing.T) {
		upstreamJSON := `{"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"model":"glm-upstream"}`
		input := glmBridgeAdjudicationInput(t, false, adjUpstreamResponse("application/json", io.NopCloser(strings.NewReader(upstreamJSON))))
		out, err := transformer.TransformUpstreamResponseForAccount(input)
		if err != nil {
			t.Fatalf("TransformUpstreamResponseForAccount error: %v", err)
		}
		clientBody, readErr := io.ReadAll(out.Body)
		if readErr != nil {
			t.Fatalf("读取客户端响应失败: %v", readErr)
		}
		text := string(clientBody)
		if !strings.Contains(text, `"object":"response"`) {
			t.Fatalf("客户端未收到 Responses JSON（响应未被桥回转，chat JSON 原样透传），实际内容: %s", text)
		}
		t.Logf("裁决通过：客户端收到 Responses JSON，长度 %d", len(text))
	})
}

// TestAdjudicateGLMResponsesToChatBridgeUpstreamURL 守护链上 URL 构建的映射
// 路径改写（BUG-0178 第二层修复回归）：responses -> chat_completions 映射
// 账户的 /v1/responses 请求必须打到上游 /chat/completions，而非原生
// /responses（真机验收发现：URL 构建器曾用客户端原始路径，桥体转换后仍打
// 到上游 Responses 端点被拒）。
func TestAdjudicateGLMResponsesToChatBridgeUpstreamURL(t *testing.T) {
	driver := newChainProviderDriver()
	body := `{"model":"client-model","stream":false,"input":"hi"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: adjMustJSONMap(t, body)}
	account := glmBridgeAdjudicationAccount()
	account.BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("BuildGatewayUpstreamURLsForAccount error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("urls = %#v", urls)
	}
	want := "https://open.bigmodel.cn/api/coding/paas/v4/v1/chat/completions"
	// 与全部既有 GLM chat 流量同一 BuildUpstreamURL 契约：base 无 /v1 结尾时
	// 补 /v1（endpoint_test.go 期望表钉住的行为），改写仅替换版本后的路径段。
	if urls[0] != want {
		t.Fatalf("上游 URL = %q，期望 %q（映射路径改写未生效）", urls[0], want)
	}
	t.Logf("裁决通过：上游 URL = %s", urls[0])
}

// TestAdjudicateGLMResponsesToChatBridgeNativeResponsesAccount 守护能力不变量
// （BUG-0178 守卫回归）：持有原生 Responses 端点模式的账户即使配置了
// responses -> chat_completions 映射，/v1/responses 仍按原生直通——URL 不
// 改写、响应不回转（codex OAuth 与 responses 模式 api_key 账户的既有语义）。
func TestAdjudicateGLMResponsesToChatBridgeNativeResponsesAccount(t *testing.T) {
	account := glmBridgeAdjudicationAccount()
	account.BaseURL = "https://resp.example/v1"
	account.SupportedEndpointModes = []string{"responses_json", "responses_sse"}

	t.Run("URL不改写", func(t *testing.T) {
		driver := newChainProviderDriver()
		body := `{"model":"client-model","stream":false,"input":"hi"}`
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: adjMustJSONMap(t, body)}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
		if err != nil {
			t.Fatalf("BuildGatewayUpstreamURLsForAccount error: %v", err)
		}
		if len(urls) != 1 || urls[0] != "https://resp.example/v1/responses" {
			t.Fatalf("原生 Responses 账户 URL = %#v，期望保持 /responses 直通", urls)
		}
	})

	t.Run("响应不回转", func(t *testing.T) {
		transformer := newChainBridgeResponseTransformer()
		upstreamSSE := "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"
		input := glmBridgeAdjudicationInput(t, true, adjUpstreamResponse("text/event-stream", io.NopCloser(strings.NewReader(upstreamSSE))))
		input.Account = account
		out, err := transformer.TransformUpstreamResponseForAccount(input)
		if err != nil {
			t.Fatalf("TransformUpstreamResponseForAccount error: %v", err)
		}
		clientBody, readErr := io.ReadAll(out.Body)
		if readErr != nil {
			t.Fatalf("读取客户端响应失败: %v", readErr)
		}
		if !strings.Contains(string(clientBody), `"type":"response.completed"`) {
			t.Fatalf("原生 Responses 响应应原样透传，实际: %.200s", string(clientBody))
		}
	})
}
