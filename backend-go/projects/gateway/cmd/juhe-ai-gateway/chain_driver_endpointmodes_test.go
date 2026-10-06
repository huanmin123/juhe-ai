package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// BUG-0175 W4-F：D-155（SupportedEndpointModes 派发消费）与 D-99
// （api-key-client-compatibility 组合入口）的 chain_driver 语义测试。

func newEndpointGateRequest(t *testing.T, method, path, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	}
	return req
}

func mustJSONMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("test body must be json: %v", err)
	}
	return parsed
}

func TestEndpointModeMismatchReasonConsumesSupportedEndpointModes(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	chatReq := newEndpointGateRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"glm-4.6"}`)
	responsesReq := newEndpointGateRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3","input":"hi"}`)

	t.Run("no constraint keeps account eligible", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "a1", ProtocolCode: "openai", ProviderCode: "glm", Type: "api_key"}
		if reason := driver.endpointModeMismatchReason(chatReq, account, ""); reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
	})

	t.Run("chat account rejected on responses shape", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "a2", ProtocolCode: "openai", ProviderCode: "glm", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse"},
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, ""); reason != "endpoint_mode_unsupported" {
			t.Fatalf("reason = %q, want endpoint_mode_unsupported", reason)
		}
		if reason := driver.endpointModeMismatchReason(chatReq, account, ""); reason != "" {
			t.Fatalf("chat reason = %q, want empty", reason)
		}
	})

	t.Run("codex responses client forces responses_sse", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "a3", ProtocolCode: "openai", ProviderCode: "gpt", Type: "api_key",
			SupportedEndpointModes: []string{"responses_sse"},
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, "codex_responses"); reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
		chatOnly := gatewaydispatch.AccountCandidate{
			ID: "a4", ProtocolCode: "openai", ProviderCode: "glm", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse"},
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, chatOnly, "codex_responses"); reason != "endpoint_mode_unsupported" {
			t.Fatalf("reason = %q, want endpoint_mode_unsupported", reason)
		}
	})

	t.Run("oauth gpt account eliminated on chat shape", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "a5", ProtocolCode: "openai", ProviderCode: "gpt", Type: "oauth",
			SupportedEndpointModes: []string{"responses_json", "responses_sse"},
		}
		// Node accountSupportsOpenAIEndpointMode：chat 形态对 responses-only
		// 模式集产生淘汰（chat_json 不在账户模式集内）。
		mode, required := driver.requiredSupportedEndpointMode(chatReq, account, "")
		if !required || mode != gatewaypreauth.EndpointModeChatJSON {
			t.Fatalf("mode=%q required=%v, want chat_json gated", mode, required)
		}
		if verdict := driver.endpointModeMismatchReason(chatReq, account, ""); verdict != "endpoint_mode_unsupported" {
			t.Fatalf("verdict = %q, want endpoint_mode_unsupported", verdict)
		}
	})

	t.Run("mapping bridge remaps mode vocabulary to upstream family", func(t *testing.T) {
		// hybrid 账户持有 messages→chat_completions 跨协议映射（映射许可表放
		// 行的组合），要求 chat_json 模式。
		messagesReq := newEndpointGateRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x","max_tokens":128}`)
		anthropicUpstream := gatewaydispatch.AccountCandidate{
			ID: "a6", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse", "messages_json", "messages_sse"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{
				{SourceModel: "claude-x", SourceEndpointFamily: "messages", UpstreamModel: "deepseek-v3", UpstreamEndpointFamily: "chat_completions", Enabled: true},
			},
		}
		if reason := driver.endpointModeMismatchReason(messagesReq, anthropicUpstream, ""); reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
		messagesOnly := gatewaydispatch.AccountCandidate{
			ID: "a7", ProtocolCode: "anthropic", ProviderCode: "anthropic", Type: "api_key",
			SupportedEndpointModes: []string{"messages_json", "messages_sse"},
		}
		if reason := driver.endpointModeMismatchReason(messagesReq, messagesOnly, ""); reason != "" {
			t.Fatalf("native anthropic reason = %q, want empty", reason)
		}
		_ = ctx
	})
}

// TestEndpointModeCodexResponsesMappingBranchPriority 锁 BUG-0179 修复语义：
// 跨族映射分支先于 codex_responses 首分支（Node 各 driver 映射分支先行），
// OAuth 账户经 account.type !== 'oauth' 守卫仍走 codex_responses 强制分支。
func TestEndpointModeCodexResponsesMappingBranchPriority(t *testing.T) {
	driver := newChainProviderDriver()
	responsesReq := newEndpointGateRequest(t, http.MethodPost, "/v1/responses", `{"model":"glm-4.6","input":"hi"}`)
	responsesToChatMapping := []gatewayruntimecache.AccountModelMapping{{
		SourceModel:            "glm-4.6",
		SourceEndpointFamily:   "responses",
		UpstreamModel:          "glm-4.6-air",
		UpstreamEndpointFamily: "chat_completions",
		Enabled:                true,
	}}

	t.Run("codex_responses passes mapped chat-only account with chat_sse", func(t *testing.T) {
		// 主回归：Codex CLI 请求 + chat-only 桥账户（显式 responses ->
		// chat_completions 映射）不再被 responses_sse 强制淘汰，改闸 chat_sse。
		account := gatewaydispatch.AccountCandidate{
			ID: "bug0179-1", ProtocolCode: "openai", ProtocolVersion: "v1", ProviderCode: "glm", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse"},
			ModelMappings:          responsesToChatMapping,
		}
		mode, required := driver.requiredSupportedEndpointMode(responsesReq, account, "codex_responses")
		if !required || mode != gatewaypreauth.EndpointModeChatSSE {
			t.Fatalf("mode=%q required=%v, want chat_sse", mode, required)
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, "codex_responses"); reason != "" {
			t.Fatalf("reason = %q, want empty (mapped chat account must serve codex_responses)", reason)
		}
	})

	t.Run("codex_responses still eliminates unmapped chat-only account", func(t *testing.T) {
		// 无映射 chat-only 账户仍被 codex_responses 强制分支淘汰（a3/a4 语义
		// 保留，顺序调整未放松强制）。
		account := gatewaydispatch.AccountCandidate{
			ID: "bug0179-2", ProtocolCode: "openai", ProviderCode: "glm", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse"},
		}
		mode, required := driver.requiredSupportedEndpointMode(responsesReq, account, "codex_responses")
		if !required || mode != gatewaypreauth.EndpointModeResponsesSSE {
			t.Fatalf("mode=%q required=%v, want responses_sse", mode, required)
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, "codex_responses"); reason != "endpoint_mode_unsupported" {
			t.Fatalf("reason = %q, want endpoint_mode_unsupported", reason)
		}
	})

	t.Run("oauth account keeps codex_responses force despite mapping", func(t *testing.T) {
		// Node gpt driver account.type !== 'oauth' 守卫：OAuth + 映射不走映射
		// 分支，裁决来自 codex_responses 强制分支（responses_sse 而非 chat_sse）。
		account := gatewaydispatch.AccountCandidate{
			ID: "bug0179-3", ProtocolCode: "openai", ProtocolVersion: "v1", ProviderCode: "gpt", Type: "oauth",
			SupportedEndpointModes: []string{"responses_json", "responses_sse"},
			ModelMappings:          responsesToChatMapping,
		}
		mode, required := driver.requiredSupportedEndpointMode(responsesReq, account, "codex_responses")
		if !required || mode != gatewaypreauth.EndpointModeResponsesSSE {
			t.Fatalf("mode=%q required=%v, want responses_sse (oauth guard keeps force branch)", mode, required)
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, "codex_responses"); reason != "" {
			t.Fatalf("reason = %q, want empty for responses-mode oauth account", reason)
		}
		// 守卫判别：同账户若只持 chat 模式集，codex_responses 下必须被淘汰
		// （证明要求的是 responses_sse 而非映射分支的 chat_sse）。
		chatOnlyOAuth := account
		chatOnlyOAuth.ID = "bug0179-3b"
		chatOnlyOAuth.SupportedEndpointModes = []string{"chat_json", "chat_sse"}
		if reason := driver.endpointModeMismatchReason(responsesReq, chatOnlyOAuth, "codex_responses"); reason != "endpoint_mode_unsupported" {
			t.Fatalf("chat-only oauth reason = %q, want endpoint_mode_unsupported", reason)
		}
	})

	t.Run("openai_standard mapped account unchanged", func(t *testing.T) {
		// 普通 Responses 客户端 + 映射账户：映射分支放行 chat_sse（确认顺序
		// 调整不改变既有 openai_standard 语义）。
		account := gatewaydispatch.AccountCandidate{
			ID: "bug0179-4", ProtocolCode: "openai", ProtocolVersion: "v1", ProviderCode: "glm", Type: "api_key",
			SupportedEndpointModes: []string{"chat_json", "chat_sse"},
			ModelMappings:          responsesToChatMapping,
		}
		mode, required := driver.requiredSupportedEndpointMode(responsesReq, account, "openai_standard")
		if !required || mode != gatewaypreauth.EndpointModeChatSSE {
			t.Fatalf("mode=%q required=%v, want chat_sse", mode, required)
		}
		if reason := driver.endpointModeMismatchReason(responsesReq, account, "openai_standard"); reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
	})
}

func TestGatewayRequestCapabilityMismatchReasonForClientCompatibility(t *testing.T) {
	driver := newChainProviderDriver()
	oauthReq := newEndpointGateRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3","input":"hi"}`)
	oauthAccount := gatewaydispatch.AccountCandidate{
		ID: "oauth-1", ProtocolCode: "openai", ProviderCode: "gpt", Type: "oauth",
		SupportedEndpointModes: []string{"responses_json", "responses_sse"},
	}
	// D-99 eliminated 裁决：oauth 账户不服务非 codex_responses 客户端形态。
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(oauthReq, oauthAccount, "openai_standard"); reason == "" {
		t.Fatal("oauth account must be eliminated for openai_standard compatibility")
	}
	if reason := driver.gatewayRequestCapabilityMismatchReasonFor(oauthReq, oauthAccount, "codex_responses"); reason != "" {
		t.Fatalf("codex_responses reason = %q, want empty", reason)
	}
}

func TestBuildOpenAIClientCompatibilityBodyNormalizesCodexResponses(t *testing.T) {
	driver := newChainProviderDriver()
	req := newEndpointGateRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3","input":"hello","temperature":0.5,"max_output_tokens":256}`)

	// 非 codex_responses 客户端：入口保持关闭。
	body, err := driver.buildOpenAIClientCompatibilityBody(req, "openai_standard")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != nil {
		t.Fatalf("compat body must be nil for non codex_responses requests, got %s", body)
	}

	body, err = driver.buildOpenAIClientCompatibilityBody(req, "codex_responses")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body == nil {
		t.Fatal("compat body required for codex_responses /responses POST")
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("compat body not json: %v", err)
	}
	if parsed["stream"] != true || parsed["store"] != false {
		t.Fatalf("stream/store = %v/%v, want true/false", parsed["stream"], parsed["store"])
	}
	if _, hasTemperature := parsed["temperature"]; hasTemperature {
		t.Fatalf("temperature must be stripped, got %v", parsed["temperature"])
	}
	if _, hasMaxOutput := parsed["max_output_tokens"]; hasMaxOutput {
		t.Fatalf("max_output_tokens must be stripped")
	}
	if _, hasInstructions := parsed["instructions"]; !hasInstructions {
		t.Fatalf("instructions default missing")
	}
	input, ok := parsed["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("string input must normalize to message items, got %v", parsed["input"])
	}
}

func TestApplyOpenAIClientCompatibilityHeadersForcesSSE(t *testing.T) {
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	headers := request.HTTP.Header
	headers.Set("Accept", "application/json")

	applyOpenAIClientCompatibilityHeaders(headers, request, "gpt-5.3", true)
	if got := headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", got)
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
}
