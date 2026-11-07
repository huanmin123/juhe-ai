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

// GLM chat-only 账户退役裁决测试（2026-11-07 跨协议 Responses→Chat 桥退役）。
//
// 原本文件钉住 responses -> chat_completions 桥的端到端裁决（响应回转 + URL
// 改写）；桥整体退役后改钉反向不变量：配置了已退役跨协议映射的 chat-only
// 账户不再承接 Responses 请求——URL 不改写（打到原生 /responses），响应不
// 回转（chat 上游字节原样透传），映射在运行时解析即被拒绝。

func glmBridgeAdjudicationAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:                        "acc_glm_bridge_adjudication",
		VendorCode:                "glm",
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

func glmResponsesRequestForMappingProbe(t *testing.T) *gatewaypreauth.GatewayRequest {
	t.Helper()
	body := `{"model":"client-model","stream":false,"input":"hi"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: adjMustJSONMap(t, body)}
	return req
}

// TestAdjudicateGLMResponsesToChatBridgeRetired 钉住退役不变量：已退役的
// responses→chat 映射不再驱动任何桥行为——URL 保持客户端原始 /responses
// 路径（不改写到 /chat/completions），chat 上游响应原样透传（不回转成
// Responses 形态），映射在运行时解析即被拒绝。
func TestAdjudicateGLMResponsesToChatBridgeRetired(t *testing.T) {
	t.Run("URL不改写", func(t *testing.T) {
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
		if urls[0] != "https://open.bigmodel.cn/api/coding/paas/v4/responses" {
			t.Fatalf("上游 URL = %q，期望保持 /responses 直通（桥改写已退役）", urls[0])
		}
	})

	t.Run("响应不回转", func(t *testing.T) {
		transformer := newChainBridgeResponseTransformer()
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
		if strings.Contains(text, "response.") || strings.Contains(text, `"object":"response"`) {
			t.Fatalf("退役映射不得触发桥回转，实际内容: %s", text)
		}
		if !strings.Contains(text, `"choices"`) {
			t.Fatalf("chat 上游响应应原样透传，实际内容: %s", text)
		}
	})

	t.Run("映射运行时解析被拒绝", func(t *testing.T) {
		// 运行时映射解析（与 chain_bridge_response.go 同一入口）对已退役
		// 跨协议组合返回 nil——即账户对该请求不构成映射候选。
		if mapping := chainBridgeResponseMappingOf(
			glmResponsesRequestForMappingProbe(t), glmBridgeAdjudicationAccount()); mapping != nil {
			t.Fatalf("跨协议映射必须被运行时拒绝，得到 %+v", mapping)
		}
	})
}

// TestAdjudicateGLMResponsesToChatBridgeNativeResponsesAccount 守护原生直通
// 不变量（BUG-0178 守卫的退役后形态）：持有原生 Responses 端点模式的账户对
// /v1/responses 恒按原生直通——URL 不改写、响应不回转。
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
