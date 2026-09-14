package main

// w1: chain_driver.go 收割——上游 URL 构建三协议、能力过滤、账户归一模型
// 与纯谓词（code assist / codex OAuth 判定）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func w1GatewayRequest(path string) *gatewaypreauth.GatewayRequest {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
	request.Header.Set("Content-Type", "application/json")
	return gatewaypreauth.NewGatewayRequest(request)
}

func TestW1BuildUpstreamURLsForAccount(t *testing.T) {
	driver := newChainProviderDriver()
	// nil 请求 / 空 baseUrl：报错。
	if _, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), gatewaydispatch.AccountCandidate{ID: "a"}, nil); err == nil {
		t.Fatal("nil 请求必须报错")
	}
	request := w1GatewayRequest("/v1/chat/completions")
	if _, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), gatewaydispatch.AccountCandidate{ID: "acc_1"}, request); err == nil {
		t.Fatal("空 baseUrl 必须报错")
	}
	// openai 兼容：base + 客户端路径。
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), gatewaydispatch.AccountCandidate{
		ID: "acc_1", BaseURL: "https://up.example", ProtocolCode: "openai",
	}, request)
	if err != nil || len(urls) != 1 || !strings.Contains(urls[0], "up.example") || !strings.Contains(urls[0], "/chat/completions") {
		t.Fatalf("openai urls = %v, %v", urls, err)
	}
	// anthropic：/v1/messages 路径。
	anthropicReq := w1GatewayRequest("/v1/messages")
	anthropicURLs, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), gatewaydispatch.AccountCandidate{
		ID: "acc_2", BaseURL: "https://anthropic.example", Type: "api_key", ProtocolCode: "anthropic",
	}, anthropicReq)
	if err != nil || len(anthropicURLs) == 0 || !strings.Contains(anthropicURLs[0], "anthropic.example") {
		t.Fatalf("anthropic urls = %v, %v", anthropicURLs, err)
	}
	// gemini 原生：generateContent 路径。
	geminiReq := w1GatewayRequest("/v1beta/models/gemini-2.5:generateContent")
	geminiURLs, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), gatewaydispatch.AccountCandidate{
		ID: "acc_3", BaseURL: "https://gemini.example", Type: "api_key", ProtocolCode: "gemini",
	}, geminiReq)
	if err != nil || len(geminiURLs) == 0 || !strings.Contains(geminiURLs[0], "gemini.example") {
		t.Fatalf("gemini urls = %v, %v", geminiURLs, err)
	}
	// gemini code assist（google_oauth）：仅生成类路径放行。
	codeAssist := gatewaydispatch.AccountCandidate{
		ID: "acc_4", BaseURL: "https://ca.example", Type: "google_oauth", ProtocolCode: "gemini",
		Credentials: map[string]any{"oauth_type": "code_assist"},
	}
	if _, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), codeAssist, geminiReq); err != nil {
		t.Fatalf("code assist urls = %v", err)
	}
	nonGeneration := w1GatewayRequest("/v1beta/models/gemini-2.5")
	if _, err := driver.BuildGatewayUpstreamURLsForAccount(t.Context(), codeAssist, nonGeneration); err == nil {
		t.Fatal("非生成路径必须拒绝")
	}
}

func TestW1DriverPredicatesAndPrepare(t *testing.T) {
	// Prepare：恒等透传。
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{ID: "acc_1", BaseURL: "https://u"}
	passthrough, err := driver.PrepareGatewayUpstreamAccount(t.Context(), account)
	if err != nil || passthrough.ID != "acc_1" {
		t.Fatalf("prepare = %+v, %v", passthrough, err)
	}
	// 带缓存构造：nil cache 不挂目录。
	if newChainProviderDriverWithCache(nil).gptOverrideCatalog != nil {
		t.Fatal("nil cache 不得挂目录")
	}
	if newChainProviderDriverWithCache(nil).openai == nil {
		t.Fatal("openai 驱动必须装配")
	}
	// 谓词：codex OAuth = oauth + openai 协议。
	if isCodexOAuthAccount(gatewaydispatch.AccountCandidate{Type: "oauth", ProtocolCode: "OpenAI"}) != true {
		t.Fatal("oauth+openai 必须 codex")
	}
	if isCodexOAuthAccount(gatewaydispatch.AccountCandidate{Type: "api_key"}) {
		t.Fatal("api_key 不得 codex")
	}
	// gemini code assist：凭据缺 project_id → false。
	if geminiAccountUsesCodeAssistRuntime(gatewaydispatch.AccountCandidate{Type: "google_oauth"}) {
		t.Fatal("无 oauth_type 且无 project_id 必须 false")
	}
	withProject := gatewaydispatch.AccountCandidate{
		Type:        "google_oauth",
		Credentials: map[string]any{"project_id": "proj"},
	}
	if !geminiAccountUsesCodeAssistRuntime(withProject) {
		t.Fatal("有 project_id 必须 code assist")
	}
	// 归一账户模型：支持列表命中（trim）；请求体未解析模型时为空。
	withModels := gatewaydispatch.AccountCandidate{SupportedModels: []string{" gpt-5 ", "o4-mini"}}
	modelRequest := w1GatewayRequest("/v1/chat/completions")
	modelName := "gpt-5"
	modelRequest.Body = &gatewaybody.Request{State: &gatewaybody.BodyState{Model: &modelName}}
	if got := canonicalAccountModel(modelRequest, withModels); got != "gpt-5" {
		t.Fatalf("canonical = %q", got)
	}
	if got := canonicalAccountModel(modelRequest, gatewaydispatch.AccountCandidate{}); got != "" {
		t.Fatalf("miss = %q", got)
	}
	if got := canonicalAccountModel(nil, withModels); got != "" {
		t.Fatalf("nil req = %q", got)
	}
}
