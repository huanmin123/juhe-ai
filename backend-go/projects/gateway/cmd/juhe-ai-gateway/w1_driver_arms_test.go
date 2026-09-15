package main

// chain_driver.go 纯请求构建臂的补充单元测试（w1g 前缀，TestW1G 入口）。
// 增量聚焦：端点模式矩阵、client-compatibility 变换、codex Responses 归一化、
// Gemini Code Assist 包装与映射/凭据等纯 helper；auth 头矩阵与既有
// chain_driver_auth_headers_test.go / chain_driver_endpointmodes_test.go 不重复。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func w1gJSONPtr(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("w1g 测试体必须是合法 JSON: %v", err)
	}
	return parsed
}

// w1gNewDriverRequest 构造带 JSON body 的 GatewayRequest。
func w1gNewDriverRequest(t *testing.T, method, target, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	if body != "" {
		req.Body = &gatewaybody.Request{
			RawBody: []byte(body),
			Body:    w1gJSONPtr(t, body),
			State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusParsed},
		}
	}
	return req
}

func w1gStrPtr(value string) *string { return &value }

func TestW1GBuildGatewayUpstreamURLsForAccountArms(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("nil request errors", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "acc_u1", ProtocolCode: "openai", BaseURL: "https://up.example"}
		if _, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, nil); err == nil {
			t.Fatal("nil 请求必须报错")
		} else if !strings.Contains(err.Error(), "构建上游地址缺少请求或账户 baseUrl") {
			t.Fatalf("err = %v, want 构建上游地址缺少请求或账户 baseUrl", err)
		}
	})

	t.Run("nil http request errors", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "acc_u2", ProtocolCode: "openai", BaseURL: "https://up.example"}
		req := &gatewaypreauth.GatewayRequest{}
		if _, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req); err == nil {
			t.Fatal("HTTP 为 nil 的请求必须报错")
		}
	})

	t.Run("empty base url errors", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)
		account := gatewaydispatch.AccountCandidate{ID: "acc_u3", ProtocolCode: "openai"}
		if _, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req); err == nil {
			t.Fatal("缺少 baseUrl 必须报错")
		}
	})

	t.Run("anthropic native messages builds base v1 url", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x","max_tokens":16}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_u4", ProtocolCode: "anthropic", ProviderCode: "anthropic",
			Type: "api_key", BaseURL: "https://ant.example",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || urls[0] != "https://ant.example/v1/messages" {
			t.Fatalf("anthropic urls = %#v, want https://ant.example/v1/messages", urls)
		}
	})

	t.Run("anthropic unsupported account type errors", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/messages", `{"model":"claude-x","max_tokens":16}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_u5", ProtocolCode: "anthropic", Type: "google_oauth",
			BaseURL: "https://ant.example",
		}
		_, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err == nil || !strings.Contains(err.Error(), "不支持当前 Anthropic 请求路径") {
			t.Fatalf("err = %v, want 不支持当前 Anthropic 请求路径", err)
		}
	})

	t.Run("openai default keeps version stripped path", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-x"}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_u6", ProtocolCode: "openai", Type: "api_key",
			BaseURL: "https://up.example", APIKey: "sk-1",
		}
		urls, err := driver.BuildGatewayUpstreamURLsForAccount(ctx, account, req)
		if err != nil {
			t.Fatalf("build urls: %v", err)
		}
		if len(urls) != 1 || urls[0] != "https://up.example/v1/chat/completions" {
			t.Fatalf("openai urls = %#v, want https://up.example/v1/chat/completions", urls)
		}
	})
}

func TestW1GBuildGatewayUpstreamRequestPartsCodexOAuth(t *testing.T) {
	driver := newChainProviderDriver()
	ctx := context.Background()

	t.Run("nil request errors", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "acc_c0", ProtocolCode: "openai", Type: "oauth"}
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, nil, account, gatewaydispatch.UsageIdentity{}, ""); err == nil {
			t.Fatal("nil 请求必须报错")
		} else if !strings.Contains(err.Error(), "构建上游请求缺少请求上下文") {
			t.Fatalf("err = %v, want 构建上游请求缺少请求上下文", err)
		}
	})

	t.Run("oauth codex account builds bearer parts with account id", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3","input":"hello"}`)
		req.HTTP.Header.Set("Session-Id", "sess-1")
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_c1", ProtocolCode: "openai", ProviderCode: "gpt", Type: "oauth",
			APIKey:          "codex-oauth-token",
			Credentials:     map[string]any{"account_id": "acct-77"},
			BaseURL:         "https://chatgpt.example",
			SupportedModels: []string{"gpt-5.3"},
		}
		parts, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses")
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer codex-oauth-token" {
			t.Fatalf("authorization = %q, want Bearer codex-oauth-token", got)
		}
		if got := parts.Headers.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if got := parts.Headers.Get("Chatgpt-Account-Id"); got != "acct-77" {
			t.Fatalf("chatgpt-account-id = %q, want acct-77", got)
		}
		if got := parts.Headers.Get("Openai-Beta"); got != "responses=experimental" {
			t.Fatalf("openai-beta = %q, want responses=experimental", got)
		}
		var decoded map[string]any
		if err := json.Unmarshal(parts.Body, &decoded); err != nil {
			t.Fatalf("codex body 不是合法 JSON: %v", err)
		}
		if decoded["model"] != "gpt-5.3" {
			t.Fatalf("model = %v, want gpt-5.3（canonicalAccountModel 命中 SupportedModels）", decoded["model"])
		}
		if decoded["stream"] != true || decoded["store"] != false {
			t.Fatalf("stream/store = %v/%v, want true/false", decoded["stream"], decoded["store"])
		}
	})

	t.Run("oauth codex invalid body errors", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{not-json"))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{
			RawBody: []byte("{not-json"),
			State:   &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusInvalidJSON},
		}
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_c2", ProtocolCode: "openai", ProviderCode: "gpt", Type: "oauth",
			APIKey: "token-2",
		}
		if _, err := driver.BuildGatewayUpstreamRequestParts(ctx, req, account, gatewaydispatch.UsageIdentity{}, "codex_responses"); err == nil {
			t.Fatal("非法 JSON 请求体必须报错")
		}
	})
}

func TestW1GBuildGeminiCodeAssistRequestPartsArms(t *testing.T) {
	driver := newChainProviderDriver()
	nonGeneration := w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:countTokens", `{"contents":[]}`)
	codeAssistAccount := gatewaydispatch.AccountCandidate{
		ID: "acc_ga1", ProtocolCode: "gemini", ProviderCode: "gemini", Type: "google_oauth",
		Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-1"},
		BaseURL:     "https://cloudcode.example",
	}

	t.Run("non generation endpoint errors", func(t *testing.T) {
		_, err := driver.buildGeminiCodeAssistRequestParts(nonGeneration, codeAssistAccount)
		if err == nil || !strings.Contains(err.Error(), "仅支持 generateContent 与 streamGenerateContent") {
			t.Fatalf("err = %v, want 仅支持 generateContent 与 streamGenerateContent", err)
		}
	})

	t.Run("missing project id errors", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:generateContent", `{"contents":[]}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_ga2", ProtocolCode: "gemini", Type: "google_oauth",
			Credentials: map[string]any{"oauth_type": "code_assist"},
		}
		_, err := driver.buildGeminiCodeAssistRequestParts(req, account)
		if err == nil || !strings.Contains(err.Error(), "缺少 project_id") {
			t.Fatalf("err = %v, want 缺少 project_id", err)
		}
	})

	t.Run("empty body errors", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:generateContent", "")
		account := codeAssistAccount
		_, err := driver.buildGeminiCodeAssistRequestParts(req, account)
		if err == nil || !strings.Contains(err.Error(), "请求体不能为空") {
			t.Fatalf("err = %v, want 请求体不能为空", err)
		}
	})

	t.Run("invalid json body errors", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-x:generateContent", strings.NewReader("{bad"))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte("{bad")}
		_, err := driver.buildGeminiCodeAssistRequestParts(req, codeAssistAccount)
		if err == nil || !strings.Contains(err.Error(), "必须是有效的 JSON 对象") {
			t.Fatalf("err = %v, want 必须是有效的 JSON 对象", err)
		}
	})

	t.Run("non object body errors", func(t *testing.T) {
		req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-x:generateContent", nil))
		req.Body = &gatewaybody.Request{RawBody: []byte(`[1,2,3]`)}
		_, err := driver.buildGeminiCodeAssistRequestParts(req, codeAssistAccount)
		if err == nil || !strings.Contains(err.Error(), "必须是 JSON 对象") {
			t.Fatalf("err = %v, want 必须是 JSON 对象", err)
		}
	})

	t.Run("mapping upstream model wins and api keys fallback", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`)
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_ga3", ProtocolCode: "gemini", Type: "google_oauth",
			Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "proj-2"},
			APIKeys:     []string{"key-from-pool"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "gemini-2.5-flash", SourceEndpointFamily: "generate_content",
				UpstreamModel: "gemini-2.5-pro", UpstreamEndpointFamily: "generate_content", Enabled: true,
			}},
		}
		parts, err := driver.buildGeminiCodeAssistRequestParts(req, account)
		if err != nil {
			t.Fatalf("build parts: %v", err)
		}
		if got := parts.Headers.Get("Authorization"); got != "Bearer key-from-pool" {
			t.Fatalf("authorization = %q, want APIKeys[0] 回退 Bearer key-from-pool", got)
		}
		var wrapped map[string]any
		if err := json.Unmarshal(parts.Body, &wrapped); err != nil {
			t.Fatalf("wrapped body invalid: %v", err)
		}
		if wrapped["model"] != "gemini-2.5-pro" || wrapped["project"] != "proj-2" {
			t.Fatalf("model/project = %v/%v, want gemini-2.5-pro/proj-2", wrapped["model"], wrapped["project"])
		}
	})
}

func TestW1GClientUpstreamBody(t *testing.T) {
	t.Run("upstream cache wins over raw body", func(t *testing.T) {
		req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
			RawBody:           []byte("raw"),
			UpstreamBodyCache: &gatewaybody.UpstreamBodyCache{PassthroughBody: []byte("cached")},
		}}
		if got := string(clientUpstreamBody(req)); got != "cached" {
			t.Fatalf("clientUpstreamBody = %q, want cached", got)
		}
	})

	t.Run("raw body used without cache", func(t *testing.T) {
		req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{RawBody: []byte("raw")}}
		if got := string(clientUpstreamBody(req)); got != "raw" {
			t.Fatalf("clientUpstreamBody = %q, want raw", got)
		}
	})

	t.Run("nil body and empty cache return nil", func(t *testing.T) {
		if got := clientUpstreamBody(&gatewaypreauth.GatewayRequest{}); got != nil {
			t.Fatalf("clientUpstreamBody = %v, want nil", got)
		}
		empty := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
			UpstreamBodyCache: &gatewaybody.UpstreamBodyCache{},
		}}
		if got := clientUpstreamBody(empty); got != nil {
			t.Fatalf("clientUpstreamBody = %v, want nil", got)
		}
	})
}

func TestW1GIsGeminiCodeAssistGenerationRequest(t *testing.T) {
	cases := []struct {
		name string
		req  *gatewaypreauth.GatewayRequest
		want bool
	}{
		{"nil request", nil, false},
		{"generateContent", w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:generateContent", `{}`), true},
		{"streamGenerateContent", w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:streamGenerateContent?alt=sse", `{}`), true},
		{"countTokens", w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:countTokens", `{}`), false},
		{"non native path", w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{}`), false},
	}
	for _, testCase := range cases {
		if got := isGeminiCodeAssistGenerationRequest(testCase.req); got != testCase.want {
			t.Fatalf("%s: isGeminiCodeAssistGenerationRequest = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestW1GGeminiCodeAssistUpstreamURL(t *testing.T) {
	if got := geminiCodeAssistUpstreamURL(" https://cc.example/ "); got != "https://cc.example/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("geminiCodeAssistUpstreamURL = %q", got)
	}
	if got := geminiCodeAssistUpstreamURL(""); got != "/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("empty base url = %q", got)
	}
}

func TestW1GRequestMappingSourceFamilyOf(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		target   string
		withBody bool
		want     string
	}{
		{"nil request", "", "", false, gatewayopenai.FamilyChatCompletions},
		{"chat completions", http.MethodPost, "/v1/chat/completions", true, "chat_completions"},
		{"responses", http.MethodPost, "/v1/responses", true, "responses"},
		{"gemini generate content", http.MethodPost, "/v1beta/models/x:generateContent", true, "generate_content"},
		{"gemini stream generate", http.MethodPost, "/v1beta/models/x:streamGenerateContent?alt=sse", true, "stream_generate_content"},
		{"gemini count tokens maps messages", http.MethodPost, "/v1beta/models/x:countTokens", true, "messages"},
		{"anthropic messages post", http.MethodPost, "/v1/messages", true, "messages"},
		{"anthropic messages get falls back", http.MethodGet, "/v1/messages", false, "chat_completions"},
		{"unknown path falls back", http.MethodGet, "/v1/models", false, "chat_completions"},
	}
	for _, testCase := range cases {
		var req *gatewaypreauth.GatewayRequest
		if testCase.name == "nil request" {
			req = nil
		} else {
			body := ""
			if testCase.withBody {
				body = `{"model":"m"}`
			}
			req = w1gNewDriverRequest(t, testCase.method, testCase.target, body)
		}
		if got := requestMappingSourceFamilyOf(req); got != testCase.want {
			t.Fatalf("%s: requestMappingSourceFamilyOf = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestW1GRequestEndpointFamilyOf(t *testing.T) {
	cases := []struct {
		pathAndQuery string
		want         string
	}{
		{"/v1/chat/completions?api-key=x", "chat_completions"},
		{"/v1/responses", "responses"},
		{"/V1/RESPONSES", "responses"},
		{"/chat/completions", "chat_completions"},
		{"/v1/embeddings", "chat_completions"},
	}
	for _, testCase := range cases {
		if got := requestEndpointFamilyOf(testCase.pathAndQuery); got != testCase.want {
			t.Fatalf("requestEndpointFamilyOf(%q) = %q, want %q", testCase.pathAndQuery, got, testCase.want)
		}
	}
}

func TestW1GGatewayRequestCapabilityMismatchReasonArms(t *testing.T) {
	driver := newChainProviderDriver()
	chatReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"beta-model"}`)

	t.Run("gemini native unsupported", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{ID: "acc_m1", ProtocolCode: "gemini", Type: "api_key"}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(chatReq, account, ""); reason != "gemini_native_unsupported" {
			t.Fatalf("reason = %q, want gemini_native_unsupported", reason)
		}
	})

	t.Run("model unsupported without constraint relief", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_m2", ProtocolCode: "openai", Type: "api_key",
			SupportedModels: []string{" alpha-model ", "beta-model"},
		}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(chatReq, account, ""); reason != "" {
			t.Fatalf("reason = %q, want 空（trim 命中）", reason)
		}
		other := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gamma-model"}`)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(other, account, ""); reason != "model_unsupported" {
			t.Fatalf("reason = %q, want model_unsupported", reason)
		}
	})

	t.Run("mapping admits unsupported model", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_m3", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key",
			SupportedModels: []string{"beta-model"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "gamma-model", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "beta-model", UpstreamEndpointFamily: "chat_completions", Enabled: true,
			}},
		}
		other := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gamma-model"}`)
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(other, account, ""); reason != "" {
			t.Fatalf("reason = %q, want 空（映射放行）", reason)
		}
	})

	t.Run("no request model passes", func(t *testing.T) {
		modelsReq := w1gNewDriverRequest(t, http.MethodGet, "/v1/models", "")
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_m4", ProtocolCode: "openai", Type: "api_key",
			SupportedModels: []string{"beta-model"},
		}
		if reason := driver.gatewayRequestCapabilityMismatchReasonFor(modelsReq, account, ""); reason != "" {
			t.Fatalf("reason = %q, want 空（无请求模型）", reason)
		}
	})

	t.Run("candidate set returns first mismatch", func(t *testing.T) {
		gemini := gatewaydispatch.AccountCandidate{ID: "acc_m5", ProtocolCode: "gemini", Type: "api_key"}
		ok := gatewaydispatch.AccountCandidate{ID: "acc_m6", ProtocolCode: "openai", Type: "api_key"}
		if reason := driver.GatewayRequestCapabilityMismatchReason(chatReq, []gatewaydispatch.AccountCandidate{ok, gemini}); reason != "gemini_native_unsupported" {
			t.Fatalf("reason = %q, want gemini_native_unsupported", reason)
		}
		if reason := driver.GatewayRequestCapabilityMismatchReason(chatReq, []gatewaydispatch.AccountCandidate{ok}); reason != "" {
			t.Fatalf("reason = %q, want 空", reason)
		}
	})
}

func TestW1GChainStripGatewayVersionPrefix(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/v1/responses", "/responses"},
		{"/v1beta/models/x:generateContent", "/models/x:generateContent"},
		{"/v1", "/"},
		{"/v1beta", "/"},
		{"/v2/responses", "/v2/responses"},
		{"/responses", "/responses"},
	}
	for _, testCase := range cases {
		if got := chainStripGatewayVersionPrefix(testCase.path); got != testCase.want {
			t.Fatalf("chainStripGatewayVersionPrefix(%q) = %q, want %q", testCase.path, got, testCase.want)
		}
	}
}

func TestW1GIsOpenAIResponsesPostRequestForCompatibility(t *testing.T) {
	if isOpenAIResponsesPostRequestForCompatibility(nil) {
		t.Fatal("nil 请求必须返回 false")
	}
	cases := []struct {
		name   string
		method string
		target string
		want   bool
	}{
		{"responses post", http.MethodPost, "/v1/responses", true},
		{"stripped responses post", http.MethodPost, "/responses", true},
		{"get responses", http.MethodGet, "/v1/responses", false},
		{"responses compact", http.MethodPost, "/v1/responses/compact", false},
		{"chat completions", http.MethodPost, "/v1/chat/completions", false},
	}
	for _, testCase := range cases {
		req := w1gNewDriverRequest(t, testCase.method, testCase.target, `{}`)
		if got := isOpenAIResponsesPostRequestForCompatibility(req); got != testCase.want {
			t.Fatalf("%s: isOpenAIResponsesPostRequestForCompatibility = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestW1GApplyCodexResponsesCompatibility(t *testing.T) {
	t.Run("string input normalizes onto message items", func(t *testing.T) {
		body := map[string]any{
			"model": "gpt-5.3", "input": "hello", "temperature": 0.5,
			"max_output_tokens": 256.0, "top_p": 0.9, "truncation": "auto",
			"user": "u-1", "context_management": map[string]any{}, "max_completion_tokens": 128.0,
		}
		applyCodexResponsesCompatibility(body)
		items, ok := body["input"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("input = %#v, want 单条 message item", body["input"])
		}
		message, _ := items[0].(map[string]any)
		if message["role"] != "user" || message["type"] != "message" {
			t.Fatalf("message = %#v, want user message", message)
		}
		for _, key := range []string{"temperature", "max_output_tokens", "top_p", "truncation", "user", "context_management", "max_completion_tokens"} {
			if _, exists := body[key]; exists {
				t.Fatalf("%s 必须被删除", key)
			}
		}
		if body["stream"] != true || body["store"] != false {
			t.Fatalf("stream/store = %v/%v, want true/false", body["stream"], body["store"])
		}
		if body["instructions"] != "" || body["tool_choice"] != "auto" || body["parallel_tool_calls"] != true {
			t.Fatalf("defaults = %#v", body)
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 0 {
			t.Fatalf("tools = %#v, want 空数组", body["tools"])
		}
		include, ok := body["include"].([]any)
		if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("include = %#v, want reasoning.encrypted_content", body["include"])
		}
	})

	t.Run("array input converts system roles to developer", func(t *testing.T) {
		body := map[string]any{"input": []any{
			map[string]any{"type": "message", "role": "system", "content": "be nice"},
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			"raw-string",
		}}
		applyCodexResponsesCompatibility(body)
		items := body["input"].([]any)
		first, _ := items[0].(map[string]any)
		if first["role"] != "developer" {
			t.Fatalf("system role = %v, want developer", first["role"])
		}
		if first["content"] != "be nice" {
			t.Fatalf("原字段必须保留, got %#v", first)
		}
		second, _ := items[1].(map[string]any)
		if second["role"] != "user" {
			t.Fatalf("user role 必须保持, got %v", second["role"])
		}
		if items[2] != "raw-string" {
			t.Fatalf("非对象 item 必须原样保留, got %#v", items[2])
		}
	})

	t.Run("input with additional tools keeps tools unset", func(t *testing.T) {
		body := map[string]any{"input": []any{
			map[string]any{"type": "additional_tools"},
		}}
		applyCodexResponsesCompatibility(body)
		if _, exists := body["tools"]; exists {
			t.Fatalf("additional_tools 输入不得注入空 tools, got %#v", body["tools"])
		}
	})

	t.Run("existing tool choice object preserved", func(t *testing.T) {
		choice := map[string]any{"type": "function", "name": "f"}
		body := map[string]any{"input": "hi", "tool_choice": choice, "parallel_tool_calls": false}
		applyCodexResponsesCompatibility(body)
		if _, ok := body["tool_choice"].(map[string]any); !ok {
			t.Fatalf("tool_choice 对象必须保留, got %#v", body["tool_choice"])
		}
		if body["parallel_tool_calls"] != false {
			t.Fatalf("显式 parallel_tool_calls 必须保留, got %v", body["parallel_tool_calls"])
		}
	})
}

func TestW1GCodexResponsesCompatibilityHelpers(t *testing.T) {
	t.Run("codexResponsesInputHasAdditionalTools", func(t *testing.T) {
		if codexResponsesInputHasAdditionalTools("not-array") {
			t.Fatal("非数组必须返回 false")
		}
		if codexResponsesInputHasAdditionalTools([]any{map[string]any{"type": "message"}}) {
			t.Fatal("无 additional_tools 必须返回 false")
		}
		if !codexResponsesInputHasAdditionalTools([]any{map[string]any{"type": "additional_tools"}}) {
			t.Fatal("命中 additional_tools 必须返回 true")
		}
	})

	t.Run("ensureCodexResponsesReasoningEncryptedContent", func(t *testing.T) {
		if got := ensureCodexResponsesReasoningEncryptedContent(nil); len(got) != 1 || got[0] != "reasoning.encrypted_content" {
			t.Fatalf("nil include = %#v", got)
		}
		got := ensureCodexResponsesReasoningEncryptedContent([]any{"a", "  ", 12})
		if len(got) != 2 || got[0] != "a" || got[1] != "reasoning.encrypted_content" {
			t.Fatalf("include = %#v, want [a reasoning.encrypted_content]", got)
		}
		got = ensureCodexResponsesReasoningEncryptedContent([]any{"reasoning.encrypted_content"})
		if len(got) != 1 {
			t.Fatalf("重复 include = %#v, want 不追加", got)
		}
	})

	t.Run("normalizeCodexResponsesInputItems", func(t *testing.T) {
		out := normalizeCodexResponsesInputItems([]any{
			map[string]any{"role": "system", "content": "c", "extra": 1},
			map[string]any{"role": "user"},
			"keep",
		})
		if len(out) != 3 {
			t.Fatalf("len = %d, want 3", len(out))
		}
		converted, _ := out[0].(map[string]any)
		if converted["role"] != "developer" || converted["extra"] != 1 {
			t.Fatalf("converted = %#v", converted)
		}
		original := map[string]any{"role": "system"}
		_ = normalizeCodexResponsesInputItems([]any{original})
		if original["role"] != "system" {
			t.Fatalf("原对象不得被就地改写, got %#v", original)
		}
	})
}

func TestW1GParsingOpenAIClientCompatibilityJSONObject(t *testing.T) {
	t.Run("nil request returns empty object", func(t *testing.T) {
		parsed, err := parseOpenAIClientCompatibilityJSONObject(nil)
		if err != nil || len(parsed) != 0 {
			t.Fatalf("parsed=%#v err=%v, want 空 map", parsed, err)
		}
	})

	t.Run("parsed body map is copied", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3"}`)
		parsed, err := parseOpenAIClientCompatibilityJSONObject(req)
		if err != nil || parsed["model"] != "gpt-5.3" {
			t.Fatalf("parsed=%#v err=%v", parsed, err)
		}
		parsed["model"] = "mutated"
		if got := req.ParsedJSONObjectBody()["model"]; got != "gpt-5.3" {
			t.Fatalf("原 body 被改写: %v", got)
		}
	})

	t.Run("invalid json state errors", func(t *testing.T) {
		req := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
			State: &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusInvalidJSON},
		}}
		if _, err := parseOpenAIClientCompatibilityJSONObject(req); err == nil {
			t.Fatal("invalid_json 状态必须报错")
		}
	})

	t.Run("raw body parsed and validated", func(t *testing.T) {
		objectReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		objectReq.Body = &gatewaybody.Request{RawBody: []byte(`{"model":"gpt-5.3"}`)}
		parsed, err := parseOpenAIClientCompatibilityJSONObject(objectReq)
		if err != nil || parsed["model"] != "gpt-5.3" {
			t.Fatalf("parsed=%#v err=%v", parsed, err)
		}
		arrayReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		arrayReq.Body = &gatewaybody.Request{RawBody: []byte(`[1]`)}
		if _, err := parseOpenAIClientCompatibilityJSONObject(arrayReq); err == nil || !strings.Contains(err.Error(), "是 JSON 对象") {
			t.Fatalf("err = %v, want 是 JSON 对象", err)
		}
		brokenReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		brokenReq.Body = &gatewaybody.Request{RawBody: []byte("{oops")}
		if _, err := parseOpenAIClientCompatibilityJSONObject(brokenReq); err == nil {
			t.Fatal("非法 JSON 必须报错")
		}
		emptyReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		emptyReq.Body = &gatewaybody.Request{}
		parsed, err = parseOpenAIClientCompatibilityJSONObject(emptyReq)
		if err != nil || len(parsed) != 0 {
			t.Fatalf("空 body parsed=%#v err=%v, want 空 map", parsed, err)
		}
	})
}

func TestW1GBuildOpenAIClientCompatibilityBodyArms(t *testing.T) {
	driver := newChainProviderDriver()

	t.Run("invalid raw body surfaces error", func(t *testing.T) {
		req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		req.Body = &gatewaybody.Request{RawBody: []byte("{bad")}
		if _, err := driver.buildOpenAIClientCompatibilityBody(req, "codex_responses"); err == nil {
			t.Fatal("非法 JSON body 必须报错")
		}
	})

	t.Run("request model overrides compatibility body model", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3","input":"hi"}`)
		body, err := driver.buildOpenAIClientCompatibilityBody(req, "codex_responses")
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("body invalid: %v", err)
		}
		if parsed["model"] != "gpt-5.3" {
			t.Fatalf("model = %v, want gpt-5.3", parsed["model"])
		}
	})

	t.Run("non responses post keeps entry closed", func(t *testing.T) {
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-5.3"}`)
		body, err := driver.buildOpenAIClientCompatibilityBody(req, "codex_responses")
		if err != nil || body != nil {
			t.Fatalf("body=%v err=%v, want nil/nil", body, err)
		}
	})
}

func TestW1GApplyOpenAIClientCompatibilityHeadersArms(t *testing.T) {
	t.Run("not forced keeps headers untouched", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Accept", "application/json")
		applyOpenAIClientCompatibilityHeaders(headers, nil, "", false)
		if headers.Get("Accept") != "application/json" || headers.Get("Originator") != "" {
			t.Fatalf("headers = %#v, want 保持原样", headers)
		}
	})

	t.Run("nil headers does not panic", func(t *testing.T) {
		applyOpenAIClientCompatibilityHeaders(nil, nil, "", true)
	})

	t.Run("codex client headers skip normalization", func(t *testing.T) {
		headers := http.Header{}
		headers.Set("Originator", "Codex Desktop")
		applyOpenAIClientCompatibilityHeaders(headers, nil, "", true)
		if headers.Get("Accept") != "" || headers.Get("Content-Type") != "" {
			t.Fatalf("codex 客户端头不得改写, got accept=%q content-type=%q", headers.Get("Accept"), headers.Get("Content-Type"))
		}
	})

	t.Run("forced non codex headers normalized with fallback model", func(t *testing.T) {
		request := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-5.3"}`)
		headers := request.HTTP.Header
		applyOpenAIClientCompatibilityHeaders(headers, request, "", true)
		if headers.Get("Accept") != "text/event-stream" || headers.Get("Content-Type") != "application/json" {
			t.Fatalf("accept/content-type = %q/%q", headers.Get("Accept"), headers.Get("Content-Type"))
		}
		if headers.Get("Originator") != "Codex Desktop" {
			t.Fatalf("originator = %q, want 合成 Codex Desktop", headers.Get("Originator"))
		}
	})
}

func TestW1GGptRequestOverrideEndpointFamily(t *testing.T) {
	chatReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)
	responsesReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"m"}`)
	account := gatewaydispatch.AccountCandidate{ID: "acc_e1", ProtocolCode: "openai", Type: "api_key"}

	cases := []struct {
		name    string
		req     *gatewaypreauth.GatewayRequest
		mapping *gatewayproto.ResolvedModelMapping
		want    string
	}{
		{"nil mapping responses", responsesReq, nil, "responses"},
		{"nil mapping chat", chatReq, nil, "chat_completions"},
		{"mapping upstream responses", chatReq, w1gMapping("chat_completions", "responses"), "responses"},
		{"mapping upstream chat", responsesReq, w1gMapping("responses", "chat_completions"), "chat_completions"},
		// merge 后白名单经 openaicompat.NormalizeEndpointFamily 扩到跨协议族
		//（混合供应商桥能力）：messages 归一化为 anthropic_messages 后放行。
		{"mapping upstream messages bridged", chatReq, w1gMapping("chat_completions", "messages"), "anthropic_messages"},
	}
	for _, testCase := range cases {
		if got := gptRequestOverrideEndpointFamily(testCase.req, account, testCase.mapping); got != testCase.want {
			t.Fatalf("%s: gptRequestOverrideEndpointFamily = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func w1gMapping(sourceFamily, upstreamFamily string) *gatewayproto.ResolvedModelMapping {
	return &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: sourceFamily, UpstreamEndpointFamily: upstreamFamily}
}

func TestW1GRequiredSupportedEndpointModeArms(t *testing.T) {
	driver := newChainProviderDriver()
	chatReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`)
	chatStreamReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m","stream":true}`)
	responsesReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":"m","input":"hi"}`)
	messagesReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/messages", `{"model":"m","max_tokens":16}`)
	countTokensReq := w1gNewDriverRequest(t, http.MethodPost, "/v1/messages/count_tokens", `{"model":"m"}`)
	geminiReq := w1gNewDriverRequest(t, http.MethodPost, "/v1beta/models/gemini-x:generateContent", `{"contents":[]}`)
	modelsReq := w1gNewDriverRequest(t, http.MethodGet, "/v1/models", "")
	openAIAccount := gatewaydispatch.AccountCandidate{ID: "acc_p1", ProtocolCode: "openai", Type: "api_key"}
	anthropicAccount := gatewaydispatch.AccountCandidate{ID: "acc_p2", ProtocolCode: "anthropic", Type: "api_key"}
	geminiAccount := gatewaydispatch.AccountCandidate{ID: "acc_p3", ProtocolCode: "gemini", Type: "api_key"}
	codeAssistAccount := gatewaydispatch.AccountCandidate{
		ID: "acc_p4", ProtocolCode: "gemini", Type: "google_oauth",
		Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "p"},
	}

	cases := []struct {
		name     string
		req      *gatewaypreauth.GatewayRequest
		account  gatewaydispatch.AccountCandidate
		compat   string
		wantMode string
		wantReq  bool
	}{
		{"nil request", nil, openAIAccount, "", "", false},
		{"codex responses post", responsesReq, openAIAccount, "codex_responses", gatewaypreauth.EndpointModeResponsesSSE, true},
		{"ungated models shape", modelsReq, openAIAccount, "", "", true},
		{"responses json", responsesReq, openAIAccount, "", gatewaypreauth.EndpointModeResponsesJSON, true},
		{"chat json", chatReq, openAIAccount, "", gatewaypreauth.EndpointModeChatJSON, true},
		{"anthropic messages json", messagesReq, anthropicAccount, "", gatewaypreauth.EndpointModeMessagesJSON, true},
		{"anthropic count tokens", countTokensReq, anthropicAccount, "", gatewaypreauth.EndpointModeMessageTokenCounting, true},
		{"gemini native json", geminiReq, geminiAccount, "", gatewaypreauth.EndpointModeGenerateContentJSON, true},
		{"gemini code assist always sse", geminiReq, codeAssistAccount, "", gatewaypreauth.EndpointModeGenerateContentSSE, true},
	}
	for _, testCase := range cases {
		mode, required := driver.requiredSupportedEndpointMode(testCase.req, testCase.account, testCase.compat)
		if mode != testCase.wantMode || required != testCase.wantReq {
			t.Fatalf("%s: mode=%q required=%v, want %q/%v", testCase.name, mode, required, testCase.wantMode, testCase.wantReq)
		}
	}

	t.Run("bridge mapping remaps to chat sse on stream", func(t *testing.T) {
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_p5", ProtocolCode: "openai", Type: "api_key",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "m", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "u", UpstreamEndpointFamily: "chat_completions", Enabled: true,
			}},
		}
		// 同族映射不切词汇表：回落 openai 协议面。
		mode, required := driver.requiredSupportedEndpointMode(chatStreamReq, account, "")
		if !required || mode != gatewaypreauth.EndpointModeChatSSE {
			t.Fatalf("same-family mode=%q required=%v, want chat_sse", mode, required)
		}
	})

	t.Run("bridge mapping to anthropic messages and gemini", func(t *testing.T) {
		// 跨协议桥映射只有 hybrid 供应商准入（isOpenAIModelMappingRuntimeConversionSupported），
		// 存储词汇是 messages / generate_content，NormalizeEndpointFamily 再切到
		// anthropic_messages / gemini_generate_content。
		responsesSource := gatewaydispatch.AccountCandidate{
			ID: "acc_p6", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "m", SourceEndpointFamily: "responses",
				UpstreamModel: "u", UpstreamEndpointFamily: "messages", Enabled: true,
			}},
		}
		mode, required := driver.requiredSupportedEndpointMode(responsesReq, responsesSource, "")
		if !required || mode != gatewaypreauth.EndpointModeMessagesJSON {
			t.Fatalf("anthropic bridge mode=%q required=%v, want messages_json", mode, required)
		}
		chatToGemini := gatewaydispatch.AccountCandidate{
			ID: "acc_p7", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "m", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "u", UpstreamEndpointFamily: "generate_content", Enabled: true,
			}},
		}
		mode, required = driver.requiredSupportedEndpointMode(chatReq, chatToGemini, "")
		if !required || mode != gatewaypreauth.EndpointModeGenerateContentJSON {
			t.Fatalf("gemini bridge mode=%q required=%v, want generate_content_json", mode, required)
		}
		unknownUpstream := gatewaydispatch.AccountCandidate{
			ID: "acc_p8", ProtocolCode: "openai", ProviderCode: "hybrid", Type: "api_key",
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel: "m", SourceEndpointFamily: "chat_completions",
				UpstreamModel: "u", UpstreamEndpointFamily: "embeddings", Enabled: true,
			}},
		}
		// 转换支持层拒绝 embeddings 上游 → 映射不解析，回落 openai 协议面 chat 形态。
		mode, required = driver.requiredSupportedEndpointMode(chatReq, unknownUpstream, "")
		if !required || mode != gatewaypreauth.EndpointModeChatJSON {
			t.Fatalf("未知上游族 mode=%q required=%v, want chat_json 回落", mode, required)
		}
	})
}

func TestW1GPureDriverHelpers(t *testing.T) {
	t.Run("canonicalAccountModel", func(t *testing.T) {
		if canonicalAccountModel(nil, gatewaydispatch.AccountCandidate{}) != "" {
			t.Fatal("nil 请求必须返回空")
		}
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":" m1 "}`)
		account := gatewaydispatch.AccountCandidate{SupportedModels: []string{"m1 ", "m2"}}
		if got := canonicalAccountModel(req, account); got != "m1" {
			t.Fatalf("canonicalAccountModel = %q, want m1（trim 后命中）", got)
		}
		miss := w1gNewDriverRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"m9"}`)
		if got := canonicalAccountModel(miss, account); got != "" {
			t.Fatalf("canonicalAccountModel = %q, want 空", got)
		}
	})

	t.Run("accountCredentialText", func(t *testing.T) {
		if got := accountCredentialText(gatewaydispatch.AccountCandidate{}, "k"); got != "" {
			t.Fatalf("nil credentials = %q, want 空", got)
		}
		account := gatewaydispatch.AccountCandidate{Credentials: map[string]any{
			"project_id": "  proj-1  ", "count": 3,
		}}
		if got := accountCredentialText(account, "project_id"); got != "proj-1" {
			t.Fatalf("project_id = %q, want proj-1", got)
		}
		if got := accountCredentialText(account, "count"); got != "" {
			t.Fatalf("非字符串值 = %q, want 空", got)
		}
	})

	t.Run("codexAccountOf and codexIdentityOf", func(t *testing.T) {
		group := "group-1"
		account := gatewaydispatch.AccountCandidate{
			ID: "acc_x1", APIKey: "sk-1", SystemAccountID: "sys-1", BoundGroupID: &group,
			Credentials: map[string]any{"account_id": "a"},
		}
		codex := codexAccountOf(account)
		if codex.ID != "acc_x1" || codex.APIKey != "sk-1" || codex.Credentials["account_id"] != "a" {
			t.Fatalf("codexAccountOf = %#v", codex)
		}
		identity := codexIdentityOf(account)
		if identity.SystemAccountID != "sys-1" || identity.APIKeyID != "acc_x1" || identity.GroupID != "group-1" {
			t.Fatalf("codexIdentityOf = %#v", identity)
		}
		noGroup := codexIdentityOf(gatewaydispatch.AccountCandidate{ID: "acc_x2", SystemAccountID: "sys-2"})
		if noGroup.GroupID != "" {
			t.Fatalf("nil BoundGroupID = %q, want 空", noGroup.GroupID)
		}
	})

	t.Run("containsTrimmed", func(t *testing.T) {
		values := []string{" a ", "b"}
		if !containsTrimmed(values, "a") || !containsTrimmed(values, " b ") {
			t.Fatal("containsTrimmed 必须 trim 比较")
		}
		if containsTrimmed(values, "c") {
			t.Fatal("未命中必须返回 false")
		}
	})

	t.Run("stringValueOrEmpty and compatibilityModelOverride", func(t *testing.T) {
		if got := stringValueOrEmpty(" v "); got != "v" {
			t.Fatalf("stringValueOrEmpty = %q, want v", got)
		}
		if got := stringValueOrEmpty(12); got != "" {
			t.Fatalf("非字符串 = %q, want 空", got)
		}
		driver := &chainProviderDriver{}
		if got := driver.compatibilityModelOverride(nil); got != "" {
			t.Fatalf("nil 请求 = %q, want 空", got)
		}
		req := w1gNewDriverRequest(t, http.MethodPost, "/v1/responses", `{"model":" gpt-x "}`)
		if got := driver.compatibilityModelOverride(req); got != "gpt-x" {
			t.Fatalf("compatibilityModelOverride = %q, want gpt-x", got)
		}
	})

	t.Run("mergeAnthropicBetaHeader", func(t *testing.T) {
		if got := mergeAnthropicBetaHeader(" Beta-A ,beta-a", false); got != "Beta-A" {
			t.Fatalf("非 oauth 合并 = %q, want Beta-A（大小写不敏感去重）", got)
		}
		got := mergeAnthropicBetaHeader("", true)
		want := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
		if got != want {
			t.Fatalf("oauth 默认合并 = %q, want %q", got, want)
		}
		got = mergeAnthropicBetaHeader("OAUTH-2025-04-20", true)
		if got != "OAUTH-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14" {
			t.Fatalf("大小写去重合并 = %q", got)
		}
	})

	t.Run("normalizeProtocol and openAIModelMappingsOf", func(t *testing.T) {
		if got := normalizeProtocol(" OpenAI "); got != "openai" {
			t.Fatalf("normalizeProtocol = %q, want openai", got)
		}
		routeRule := "rule-1"
		mappings := openAIModelMappingsOf([]gatewayruntimecache.AccountModelMapping{
			{SourceModel: "a", UpstreamModel: "b", Enabled: true, RuntimeRouteRuleID: &routeRule},
			{SourceModel: "c", UpstreamModel: "d", Enabled: false},
		})
		if len(mappings) != 2 || mappings[0].Enabled == nil || !*mappings[0].Enabled || mappings[1].Enabled == nil || *mappings[1].Enabled {
			t.Fatalf("openAIModelMappingsOf = %#v", mappings)
		}
		if mappings[0].RuntimeRouteRuleID != "rule-1" || mappings[0].RuntimeSource != "" {
			t.Fatalf("runtime 字段透传错误: %#v", mappings[0])
		}
		if got := openAIModelMappingsOf(nil); len(got) != 0 {
			t.Fatalf("nil mappings = %#v", got)
		}
	})

	t.Run("upstreamHeadersOf drops hop by hop headers", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set("Cookie", "sid=1")
		request.Header.Set("Connection", "keep-alive")
		request.Header.Set("Authorization", "Bearer client")
		req := gatewaypreauth.NewGatewayRequest(request)
		headers := upstreamHeadersOf(req, gatewaydispatch.AccountCandidate{
			ProtocolCode: "openai", Type: "api_key", APIKey: "sk-up",
		})
		if headers.Get("Cookie") != "" || headers.Get("Connection") != "" {
			t.Fatalf("hop-by-hop 头必须删除, cookie=%q connection=%q", headers.Get("Cookie"), headers.Get("Connection"))
		}
		if headers.Get("Authorization") != "Bearer sk-up" {
			t.Fatalf("authorization = %q, want Bearer sk-up", headers.Get("Authorization"))
		}
	})
}
