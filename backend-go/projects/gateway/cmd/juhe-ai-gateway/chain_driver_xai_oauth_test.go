package main

// xAI（Grok）OAuth 账户的透传口径（docs/functions/xAI账号接入.md）：
// 不套用 GPT/Codex 归一化（无 Codex 头装配、body 无字段删改、不强改
// store/stream），并在目标主机精确为 cli-chat-proxy.grok.com 时补充 Grok
// CLI 身份三头（api.x.ai 与自定义主机不携带）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// xaiOAuthBody 构造一个「若进 Codex 归一化必被改写」的请求体：store=true
// （Codex 分支强改 false）、temperature 等归一化删除面字段、嵌套 reasoning
// （responses 族 + 无扁平 reasoning_effort 时透传不改）。
const xaiOAuthBody = `{"model":"grok-4.7","input":"hi","store":true,"temperature":0.7,"reasoning":{"effort":"low"}}`

func xaiOAuthAccount(baseURL string) gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID: "acc_x1", ProtocolCode: "openai", VendorCode: "xai", Type: "oauth",
		APIKey:  "xai-oauth-token",
		BaseURL: baseURL,
	}
}

func TestChainProviderDriverXaiOAuthPassthroughParts(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("grok cli host injects identity trio without codex shaping", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", xaiOAuthBody, nil)
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, xaiOAuthAccount("https://cli-chat-proxy.grok.com/v1"), gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		// Grok CLI 身份三头（版本取 upstreamidentity 生效推导，不锚字面量）。
		wantVersion := upstreamidentity.EffectiveGrokCLIVersion()
		if got := parts.Headers.Get("User-Agent"); got != "xai-grok-workspace/"+wantVersion {
			t.Fatalf("user-agent = %q, want xai-grok-workspace/%s", got, wantVersion)
		}
		if got := parts.Headers.Get("X-Xai-Token-Auth"); got != "xai-grok-cli" {
			t.Fatalf("x-xai-token-auth = %q, want xai-grok-cli", got)
		}
		if got := parts.Headers.Get("X-Grok-Client-Version"); got != wantVersion {
			t.Fatalf("x-grok-client-version = %q, want %s", got, wantVersion)
		}
		assertNoCodexShaping(t, parts)
	})

	t.Run("api.x.ai host keeps client identity and no trio", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", xaiOAuthBody, map[string]string{"User-Agent": "xai-grok-workspace-test"})
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, xaiOAuthAccount("https://api.x.ai/v1"), gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("X-Xai-Token-Auth"); got != "" {
			t.Fatalf("x-xai-token-auth = %q, want 空（api.x.ai 不携带）", got)
		}
		if got := parts.Headers.Get("X-Grok-Client-Version"); got != "" {
			t.Fatalf("x-grok-client-version = %q, want 空（api.x.ai 不携带）", got)
		}
		if got := parts.Headers.Get("User-Agent"); got != "xai-grok-workspace-test" {
			t.Fatalf("user-agent = %q, want 客户端原值不被覆盖", got)
		}
		assertNoCodexShaping(t, parts)
	})

	t.Run("custom host keeps no trio", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/responses", xaiOAuthBody, nil)
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, xaiOAuthAccount("https://grok-relay.example/v1"), gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("X-Xai-Token-Auth"); got != "" {
			t.Fatalf("x-xai-token-auth = %q, want 空（自定义主机不携带）", got)
		}
		if got := parts.Headers.Get("X-Grok-Client-Version"); got != "" {
			t.Fatalf("x-grok-client-version = %q, want 空（自定义主机不携带）", got)
		}
		assertNoCodexShaping(t, parts)
	})
}

// assertNoCodexShaping 钉住 xAI OAuth 透传分支的两条硬口径：无 Codex 头装
// 配（Originator/attestation/ChatGPT 账户头/openai-beta，UA 非 codex 形态）
// 且 body 字节级原样（store 不被强改、字段不被删、非流式不注入 stream 字段）。
func assertNoCodexShaping(t *testing.T, parts gatewaydispatch.PreparedRequestParts) {
	t.Helper()
	for name, want := range map[string]string{
		"Originator":                             "",
		"X-Oai-Attestation":                      "",
		"Chatgpt-Account-Id":                     "",
		"Openai-Beta":                            "",
		"X-Openai-Internal-Codex-Responses-Lite": "",
	} {
		if got := parts.Headers.Get(name); got != want {
			t.Fatalf("%s = %q, want %q（无 Codex 头装配）", name, got, want)
		}
	}
	if got := parts.Headers.Get("Authorization"); got != "Bearer xai-oauth-token" {
		t.Fatalf("authorization = %q, want Bearer xai-oauth-token", got)
	}
	if !bytes.Equal(parts.Body, []byte(xaiOAuthBody)) {
		t.Fatalf("body 必须字节级原样透传，got %s", parts.Body)
	}
}

// TestChainProviderDriverXaiOAuthChatStreamUsageInjectStillApplies 钉住费用
// 口径：xAI OAuth 走通用透传后，chat completions 流式出站的
// stream_options.include_usage 注入（流式零计费修复）照常生效——本批只收窄
// Codex 归一化边界，不改账务路径。
func TestChainProviderDriverXaiOAuthChatStreamUsageInjectStillApplies(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	// w1gNewDriverRequest 携带 body 解析产物（真实链路由 ingress 解析），
	// RequestStream 判定依赖该产物。
	req := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"grok-4.7","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, xaiOAuthAccount("https://cli-chat-proxy.grok.com/v1"), gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(parts.Body, &decoded); err != nil {
		t.Fatalf("body 不是合法 JSON: %v", err)
	}
	options, ok := decoded["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage:true 注入保持", decoded["stream_options"])
	}
	if decoded["model"] != "grok-4.7" {
		t.Fatalf("model = %v, want grok-4.7（其余字段保留）", decoded["model"])
	}
}
