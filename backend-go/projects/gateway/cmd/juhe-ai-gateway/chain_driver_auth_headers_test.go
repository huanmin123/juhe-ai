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
)

// BUG-0174 M-4/M-5：主链上游认证头矩阵，对齐 Node driver.ts + 探针
// probe.go:413-461 的已对齐写法。

func newAuthMatrixRequest(t *testing.T, method, path, body string, header map[string]string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range header {
		request.Header.Set(name, value)
	}
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(body)}
	}
	return req
}

func TestChainProviderDriverAnthropicAuthHeaderMatrix(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("api_key uses x-api-key and default version", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x"}`, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_a1", ProtocolCode: "anthropic", ProviderCode: "anthropic",
			Type: "api_key", APIKey: "sk-ant-1", BaseURL: "https://anthropic.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("X-Api-Key"); got != "sk-ant-1" {
			t.Fatalf("x-api-key = %q, want sk-ant-1", got)
		}
		if got := parts.Headers.Get("Authorization"); got != "" {
			t.Fatalf("authorization must stay empty for api_key, got %q", got)
		}
		// M-4: "v1" 是协议段常量，头值必须默认 2023-06-01。
		if got := parts.Headers.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		if got := parts.Headers.Get("Anthropic-Beta"); got != "" {
			t.Fatalf("anthropic-beta must stay unset for api_key without client beta, got %q", got)
		}
	})

	t.Run("client anthropic-version passes through", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x"}`, map[string]string{"Anthropic-Version": "2025-01-01"})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_a2", ProtocolCode: "anthropic", ProviderCode: "anthropic",
			Type: "api_key", APIKey: "sk-ant-2", BaseURL: "https://anthropic.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Anthropic-Version"); got != "2025-01-01" {
			t.Fatalf("anthropic-version = %q, want client value 2025-01-01", got)
		}
	})

	t.Run("oauth uses bearer access token with merged beta and cli identity", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x"}`, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_a3", ProtocolCode: "anthropic", ProviderCode: "anthropic",
			Type:        "oauth",
			APIKey:      "not-the-access-token",
			Credentials: map[string]any{"access_token": "oauth-token-1"},
			BaseURL:     "https://anthropic.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer oauth-token-1" {
			t.Fatalf("authorization = %q, want Bearer oauth-token-1", got)
		}
		if got := parts.Headers.Get("X-Api-Key"); got != "" {
			t.Fatalf("x-api-key must stay empty for oauth, got %q", got)
		}
		wantBeta := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
		if got := parts.Headers.Get("Anthropic-Beta"); got != wantBeta {
			t.Fatalf("anthropic-beta = %q, want %q", got, wantBeta)
		}
		if got := parts.Headers.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		identity := map[string]string{
			"User-Agent":                                "claude-cli/2.1.161 (external, cli)",
			"X-Stainless-Lang":                          "js",
			"X-Stainless-Package-Version":               "0.94.0",
			"X-Stainless-Os":                            "Linux",
			"X-Stainless-Arch":                          "arm64",
			"X-Stainless-Runtime":                       "node",
			"X-Stainless-Runtime-Version":               "v24.3.0",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Timeout":                       "600",
			"X-App":                                     "cli",
			"Anthropic-Dangerous-Direct-Browser-Access": "true",
		}
		for name, want := range identity {
			if got := parts.Headers.Get(name); got != want {
				t.Fatalf("%s = %q, want %q", name, got, want)
			}
		}
	})

	t.Run("oauth merges client beta without duplicates", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x"}`,
			map[string]string{"Anthropic-Beta": "oauth-2025-04-20, Custom-Beta"})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_a4", ProtocolCode: "anthropic", ProviderCode: "anthropic",
			Type:        "oauth",
			Credentials: map[string]any{"access_token": "oauth-token-2"},
			BaseURL:     "https://anthropic.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		// Node splitAnthropicBetaHeader 对每项 trim 后以裸逗号 join，
		// 客户端项内的空格被规范化掉。
		want := "oauth-2025-04-20,Custom-Beta,claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
		if got := parts.Headers.Get("Anthropic-Beta"); got != want {
			t.Fatalf("anthropic-beta = %q, want %q", got, want)
		}
	})

	t.Run("glm coding profile uses bearer api key", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1/messages", `{"model":"glm-x"}`, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_a5", ProtocolCode: "anthropic", ProviderCode: "glm",
			ProviderProtocolProfileID: "profile_glm_coding_anthropic_v1",
			Type:                      "api_key", APIKey: "glm-key-1", BaseURL: "https://glm.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer glm-key-1" {
			t.Fatalf("authorization = %q, want Bearer glm-key-1", got)
		}
		if got := parts.Headers.Get("X-Api-Key"); got != "" {
			t.Fatalf("x-api-key must stay empty for glm profile, got %q", got)
		}
	})
}

func geminiGenerateContentPath(model string) string {
	return "/v1beta/models/" + model + ":generateContent"
}

func TestChainProviderDriverGeminiAuthHeaderMatrix(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()
	body := `{"contents":[{"parts":[{"text":"hi"}]}]}`

	t.Run("api_key uses x-goog-api-key", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, geminiGenerateContentPath("gemini-2.5-flash"), body, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g1", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "api_key", APIKey: "gem-key-1", BaseURL: "https://gem.example",
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("X-Goog-Api-Key"); got != "gem-key-1" {
			t.Fatalf("x-goog-api-key = %q, want gem-key-1", got)
		}
		if got := parts.Headers.Get("Authorization"); got != "" {
			t.Fatalf("authorization must stay empty for api_key, got %q", got)
		}
	})

	t.Run("google_oauth ai studio uses bearer with allowlist and quota project", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, geminiGenerateContentPath("gemini-2.5-flash"), body, map[string]string{
			"X-Goog-Api-Client": "keep-me",
			"X-Custom-Trace":    "drop-me",
			"Content-Type":      "application/json",
		})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g2", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "google_oauth", APIKey: "oa-token-1",
			Credentials: map[string]any{"oauth_type": "ai_studio", "quota_project_id": "proj-quota"},
			BaseURL:     "https://gem.example",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || urls[0] != "https://gem.example/v1beta/models/gemini-2.5-flash:generateContent" {
			t.Fatalf("ai_studio urls = %#v, want native v1beta url", urls)
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer oa-token-1" {
			t.Fatalf("authorization = %q, want Bearer oa-token-1", got)
		}
		if got := parts.Headers.Get("X-Goog-Api-Key"); got != "" {
			t.Fatalf("x-goog-api-key must be dropped for google_oauth, got %q", got)
		}
		if got := parts.Headers.Get("X-Goog-User-Project"); got != "proj-quota" {
			t.Fatalf("x-goog-user-project = %q, want proj-quota", got)
		}
		if got := parts.Headers.Get("X-Goog-Api-Client"); got != "keep-me" {
			t.Fatalf("x-goog-api-client = %q, want allowlisted keep-me", got)
		}
		if got := parts.Headers.Get("X-Custom-Trace"); got != "" {
			t.Fatalf("x-custom-trace must be dropped by gemini_cli allowlist, got %q", got)
		}
		if got := parts.Headers.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want allowlisted application/json", got)
		}
	})

	t.Run("code assist wraps body onto v1internal stream endpoint", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", body, map[string]string{
			"X-Custom-Trace": "drop-me",
		})
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g3", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "google_oauth", APIKey: "oa-token-2",
			Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-9"},
			BaseURL:     "https://cloudcode.example",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || urls[0] != "https://cloudcode.example/v1internal:streamGenerateContent?alt=sse" {
			t.Fatalf("code assist urls = %#v, want v1internal stream endpoint", urls)
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer oa-token-2" {
			t.Fatalf("authorization = %q, want Bearer oa-token-2", got)
		}
		if got := parts.Headers.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if got := parts.Headers.Get("User-Agent"); got != "GeminiCLI/0.1.5 (Windows; AMD64)" {
			t.Fatalf("user-agent = %q, want GeminiCLI/0.1.5 (Windows; AMD64)", got)
		}
		if got := parts.Headers.Get("X-Custom-Trace"); got != "" {
			t.Fatalf("code assist headers must be a fresh set, got x-custom-trace %q", got)
		}
		var wrapped map[string]any
		if err := json.Unmarshal(parts.Body, &wrapped); err != nil {
			t.Fatalf("wrapped body invalid json: %v", err)
		}
		if wrapped["model"] != "gemini-2.5-flash" || wrapped["project"] != "proj-9" {
			t.Fatalf("wrapped body model/project wrong: %#v", wrapped)
		}
		requestObject, ok := wrapped["request"].(map[string]any)
		if !ok || len(requestObject) == 0 {
			t.Fatalf("wrapped body must nest the original request object: %#v", wrapped)
		}
	})

	t.Run("code assist legacy credentials without oauth type use project id", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, geminiGenerateContentPath("gemini-2.5-flash"), body, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g4", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "google_oauth", APIKey: "oa-token-3",
			Credentials: map[string]any{"project_id": "proj-legacy"},
			BaseURL:     "https://cloudcode.example",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || !strings.HasSuffix(urls[0], "/v1internal:streamGenerateContent?alt=sse") {
			t.Fatalf("legacy code assist urls = %#v", urls)
		}
	})

	t.Run("explicit ai studio with project id stays native", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, geminiGenerateContentPath("gemini-2.5-flash"), body, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g5", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "google_oauth", APIKey: "oa-token-4",
			Credentials: map[string]any{"oauth_type": "ai_studio", "project_id": "proj-ignored"},
			BaseURL:     "https://gem.example",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || !strings.Contains(urls[0], "/v1beta/models/") {
			t.Fatalf("ai_studio urls = %#v, want native v1beta path", urls)
		}
	})

	t.Run("code assist rejects non generation endpoints", func(t *testing.T) {
		req := newAuthMatrixRequest(t, http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens", body, nil)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_g6", ProtocolCode: "gemini", ProviderCode: "gemini",
			Type: "google_oauth", APIKey: "oa-token-5",
			Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-9"},
			BaseURL:     "https://cloudcode.example",
		}
		if _, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req); err == nil {
			t.Fatal("code assist countTokens must fail URL build")
		}
		if reason := driver.GatewayRequestCapabilityMismatchReason(req, []gatewaydispatch.AccountCandidate{account}); reason != "gemini_code_assist_unsupported_endpoint" {
			t.Fatalf("mismatch reason = %q, want gemini_code_assist_unsupported_endpoint", reason)
		}
		if driver.AccountSupportsGatewayRequest(req, account, "") {
			t.Fatal("code assist account must not support countTokens")
		}
	})
}
