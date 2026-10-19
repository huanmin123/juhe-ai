package main

// C2 补修覆盖：xAI（Grok）OAuth 的客户端兼容能力门与 Codex 兼容改写边界
//（docs/functions/xAI账号接入.md）。
//
//   - OAuth 客户端兼容门（oauth_account_client_compatibility_unsupported）按
//     gatewaydispatch.CodexOAuthVendorEligible 收窄到 gpt/迁移期空 vendor：
//     xAI OAuth 对普通 openai_standard 客户端的 /v1/responses 候选保留；
//     /chat/completions 仍由账户 responses-only endpoint modes 阻断
//    （endpoint_mode_unsupported），本门不承担端点面职责。
//   - compat=codex_responses 的 Codex 客户端形态对 xAI OAuth 属「协议边界」
//     一节的合法「Codex 客户端兼容」改写触发（D-99 兼容体改写 +
//     Accept/Content-Type 强制 + 合成 Codex 客户端头），且不得吞掉 xai 账户
//     凭据（Authorization 保持 Bearer access token）。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// xaiCompatOAuthAccount 构造带 responses-only 端点模式集的 oauth 账户候选
//（Grok OAuth 固定 responses_json/responses_sse，与运行时 oauth 缺省投影
// 一致）；vendorCode 参数化以覆盖 gpt/空值对照形态。
func xaiCompatOAuthAccount(vendorCode string) gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID: "acc_xc1", ProtocolCode: "openai", VendorCode: vendorCode, Type: "oauth",
		APIKey:  "xai-oauth-token",
		BaseURL: "https://cli-chat-proxy.grok.com/v1",
		SupportedEndpointModes: []string{
			gatewaypreauth.EndpointModeResponsesJSON,
			gatewaypreauth.EndpointModeResponsesSSE,
		},
	}
}

// TestChainProviderDriverXaiOAuthClientCompatibilityGate 候选过滤级钉：兼容
// 门收窄后 xai oauth + openai_standard 的 /v1/responses 候选保留，gpt/空
// vendor 的 oauth + openai_standard 仍以原 reason 淘汰，xai oauth 的 chat 面
// 仍被 endpoint modes 阻断（阻断机制从兼容门换为端点模式门，拒绝事实不变）。
func TestChainProviderDriverXaiOAuthClientCompatibilityGate(t *testing.T) {
	driver := newChainProviderDriver()

	t.Run("xai oauth keeps openai_standard responses candidate", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", `{"model":"grok-4.7","input":"hi"}`, nil)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, xaiCompatOAuthAccount("xai"), "openai_standard"); reason != "" {
			t.Fatalf("reason = %q, want 空（openai_standard 候选保留）", reason)
		}
		if !driver.AccountSupportsGatewayRequest(req, xaiCompatOAuthAccount("xai"), "openai_standard") {
			t.Fatal("xai oauth + openai_standard 候选必须保留")
		}
	})

	t.Run("gpt oauth still eliminated for openai_standard", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5","input":"hi"}`, nil)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, xaiCompatOAuthAccount("gpt"), "openai_standard"); reason != "oauth_account_client_compatibility_unsupported" {
			t.Fatalf("reason = %q, want oauth_account_client_compatibility_unsupported（gpt 行为逐点不变）", reason)
		}
	})

	t.Run("empty vendor oauth still eliminated for openai_standard", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5","input":"hi"}`, nil)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, xaiCompatOAuthAccount(""), "openai_standard"); reason != "oauth_account_client_compatibility_unsupported" {
			t.Fatalf("reason = %q, want oauth_account_client_compatibility_unsupported（迁移期空值同 gpt）", reason)
		}
	})

	t.Run("xai oauth chat completions still blocked by endpoint modes", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"grok-4.7","messages":[{"role":"user","content":"hi"}]}`, nil)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(req, xaiCompatOAuthAccount("xai"), "openai_standard"); reason != "endpoint_mode_unsupported" {
			t.Fatalf("reason = %q, want endpoint_mode_unsupported（chat 面仍被 responses-only endpoint modes 阻断）", reason)
		}
	})
}

// TestChainProviderDriverXaiOAuthCodexResponsesCompatRewrite 钉住此前零覆盖
// 的改写形态：compat=codex_responses + POST /v1/responses 命中 D-99 兼容体
// 改写（string input → message 数组、强制 SSE、store 关闭、归一化删除面删除、
// instructions 注入）与 Accept/Content-Type 强制 + 合成 Codex 客户端头
//（Originator），同时 xai 账户凭据不被吞掉（Authorization 保持 Bearer），且
// grok 主机的 CLI 身份三头在头序末位仍生效（覆盖合成的 Codex UA）。
func TestChainProviderDriverXaiOAuthCodexResponsesCompatRewrite(t *testing.T) {
	driver := newChainProviderDriver()
	req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses",
		`{"model":"grok-4.7","input":"hi","store":true,"temperature":0.7}`, nil)
	parts, err := driver.BuildGatewayUpstreamRequestParts(
		context.Background(), req, xaiCompatOAuthAccount("xai"), gatewaydispatch.UsageIdentity{}, "codex_responses")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(parts.Body, &decoded); err != nil {
		t.Fatalf("body 不是合法 JSON: %v", err)
	}
	if decoded["stream"] != true {
		t.Fatalf("stream = %#v, want true（codex_responses 强制 SSE）", decoded["stream"])
	}
	if decoded["store"] != false {
		t.Fatalf("store = %#v, want false", decoded["store"])
	}
	if _, ok := decoded["temperature"]; ok {
		t.Fatalf("temperature = %#v, want 删除（归一化删除面）", decoded["temperature"])
	}
	if _, ok := decoded["instructions"]; !ok {
		t.Fatal("instructions 必须注入（Codex Responses 规范化）")
	}
	input, ok := decoded["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("input = %#v, want message 数组改写", decoded["input"])
	}
	first, _ := input[0].(map[string]any)
	if first == nil || first["type"] != "message" || first["role"] != "user" {
		t.Fatalf("input[0] = %#v, want message/user 形态", input[0])
	}

	if got := parts.Headers.Get("Authorization"); got != "Bearer xai-oauth-token" {
		t.Fatalf("authorization = %q, want Bearer xai-oauth-token（xai 凭据不被改写吞掉）", got)
	}
	if got := parts.Headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", got)
	}
	if got := parts.Headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if got := parts.Headers.Get("Originator"); got != "codex_exec" {
		t.Fatalf("originator = %q, want codex_exec（合成 Codex 客户端头）", got)
	}
	wantVersion := upstreamidentity.EffectiveGrokCLIVersion()
	if got := parts.Headers.Get("User-Agent"); got != "xai-grok-workspace/"+wantVersion {
		t.Fatalf("user-agent = %q, want xai-grok-workspace/%s（CLI 身份覆盖合成 Codex UA）", got, wantVersion)
	}
	if got := parts.Headers.Get("X-Xai-Token-Auth"); got != "xai-grok-cli" {
		t.Fatalf("x-xai-token-auth = %q, want xai-grok-cli", got)
	}
}
