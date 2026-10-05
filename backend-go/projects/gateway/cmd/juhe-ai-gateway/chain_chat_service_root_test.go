package main

// 对话批（火山方舟/通义百炼对话承接）chat 出站 URL 归一钉值：官方 OpenAI
// 兼容端点服务根（ark /api/v3、百炼 compatible-mode /compatible-mode/v1）
// 插入与去重、非 chat 路径维持 openai /v1 归一、模型映射改写后路径一并覆盖。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func TestChainChatCompletionsPathDetection(t *testing.T) {
	cases := []struct {
		pathAndQuery string
		want         bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/chat/completions?api-version=1", true},
		{"/chat/completions", true},
		{"/V1/Chat/Completions", true},
		{"/v1/responses", false},
		{"/v1/models", false},
		{"/v1/videos", false},
		{"/v1/audio/speech", false},
		{"/api/v3/chat/completions", true},
	}
	for _, tc := range cases {
		if got := chainIsChatCompletionsPathAndQuery(tc.pathAndQuery); got != tc.want {
			t.Fatalf("chainIsChatCompletionsPathAndQuery(%q) = %v, want %v", tc.pathAndQuery, got, tc.want)
		}
	}
}

func TestChainVolcengineChatUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "official root inserts api v3",
			base:         "https://ark.cn-beijing.volces.com",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:         "base with api v3 dedups",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:         "trailing slash base with api v3 dedups",
			base:         "https://ark.cn-beijing.volces.com/api/v3/",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:         "query preserved",
			base:         "https://ark.cn-beijing.volces.com",
			pathAndQuery: "/v1/chat/completions?beta=true",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions?beta=true",
		},
		{
			name:         "already-stripped client path",
			base:         "https://ark.cn-beijing.volces.com",
			pathAndQuery: "/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainVolcengineChatUpstreamURL(tc.base, tc.pathAndQuery)
			if err != nil {
				t.Fatalf("chainVolcengineChatUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("chainVolcengineChatUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.pathAndQuery, got, tc.want)
			}
		})
	}
	if _, err := chainVolcengineChatUpstreamURL("://bad", "/v1/chat/completions"); err == nil || !strings.Contains(err.Error(), "base_url 无效") {
		t.Fatalf("无效 base_url 应报错：%v", err)
	}
}

func TestChainQwenChatUpstreamURL(t *testing.T) {
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "official root inserts compatible-mode v1",
			base:         "https://dashscope.aliyuncs.com",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name:         "base with compatible-mode dedups",
			base:         "https://dashscope.aliyuncs.com/compatible-mode/v1",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name:         "media service root base is not deduped",
			base:         "https://dashscope.aliyuncs.com/api/v1",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://dashscope.aliyuncs.com/api/v1/compatible-mode/v1/chat/completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainQwenChatUpstreamURL(tc.base, tc.pathAndQuery)
			if err != nil {
				t.Fatalf("chainQwenChatUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("chainQwenChatUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.pathAndQuery, got, tc.want)
			}
		})
	}
	if _, err := chainQwenChatUpstreamURL("https://[bad", "/v1/chat/completions"); err == nil || !strings.Contains(err.Error(), "base_url 无效") {
		t.Fatalf("无效 base_url 应报错：%v", err)
	}
}

func TestChainDriverChatURLForVolcengineQwenAccounts(t *testing.T) {
	driver := newChainProviderDriver()
	newChatRequest := func(t *testing.T, model string) *gatewaypreauth.GatewayRequest {
		t.Helper()
		body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req := gatewaypreauth.NewGatewayRequest(request)
		req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
		return req
	}

	// volcengine：官方根 chat 请求按 /api/v3 服务根归一。
	volcengineAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_volc_chat",
		ProviderCode:    "volcengine",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         "https://ark.cn-beijing.volces.com",
		APIKey:          "vk-1",
	}
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), volcengineAccount, newChatRequest(t, "doubao-seed-1-6-flash-250815"))
	if err != nil {
		t.Fatalf("volcengine chat build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://ark.cn-beijing.volces.com/api/v3/chat/completions" {
		t.Fatalf("volcengine chat url = %#v", urls)
	}

	// qwen：官方根 chat 请求按 /compatible-mode/v1 服务根归一。
	qwenAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_qwen_chat",
		ProviderCode:    "qwen",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         "https://dashscope.aliyuncs.com",
		APIKey:          "qk-1",
	}
	urls, err = driver.BuildGatewayUpstreamURLsForAccount(context.Background(), qwenAccount, newChatRequest(t, "qwen-max"))
	if err != nil {
		t.Fatalf("qwen chat build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" {
		t.Fatalf("qwen chat url = %#v", urls)
	}

	// 模型映射改写（responses→chat bridge）后的 /chat/completions 路径一并
	// 走服务根归一。
	qwenAccount.ModelMappings = []gatewayruntimecache.AccountModelMapping{{
		SourceModel:            "ext-model",
		SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
		UpstreamModel:          "qwen-max",
		UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
		Enabled:                true,
	}}
	mappedBody := `{"model":"ext-model","input":"hi"}`
	mappedRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(mappedBody))
	req := gatewaypreauth.NewGatewayRequest(mappedRequest)
	req.Body = &gatewaybody.Request{RawBody: []byte(mappedBody), Body: mustJSONMap(t, mappedBody)}
	urls, err = driver.BuildGatewayUpstreamURLsForAccount(context.Background(), qwenAccount, req)
	if err != nil {
		t.Fatalf("mapped chat build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" {
		t.Fatalf("mapped chat url = %#v", urls)
	}

	// 非 chat 路径维持 openai 归一（零回归钉值）：/v1/responses 无映射时按
	// openai /v1 归一拼接。
	plainAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_volc_plain",
		ProviderCode:    "volcengine",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         "https://ark.cn-beijing.volces.com",
		APIKey:          "vk-1",
	}
	responsesBody := `{"model":"m","input":"hi"}`
	responsesRequest := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(responsesBody))
	req = gatewaypreauth.NewGatewayRequest(responsesRequest)
	req.Body = &gatewaybody.Request{RawBody: []byte(responsesBody), Body: mustJSONMap(t, responsesBody)}
	urls, err = driver.BuildGatewayUpstreamURLsForAccount(context.Background(), plainAccount, req)
	if err != nil {
		t.Fatalf("non-chat build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://ark.cn-beijing.volces.com/v1/responses" {
		t.Fatalf("non-chat url = %#v", urls)
	}
}
