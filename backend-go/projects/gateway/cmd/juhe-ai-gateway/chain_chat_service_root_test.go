package main

// 对话批（火山方舟/通义百炼对话承接）chat 出站 URL 归一钉值：官方 OpenAI
// 兼容端点服务根（ark /api/v3、百炼 compatible-mode /compatible-mode/v1）
// 插入与去重、非 chat 路径维持 openai /v1 归一、模型映射改写后路径一并覆盖。
// 2026-10-17 补 Agent Plan 专属端点（base=/api/plan/v3 显式完整服务根直拼）
// 与 volcengine 图像面（chainVolcengineImagesUpstreamURL）、媒体任务面
//（chainVolcengineVideoUpstreamURL）的三形态钉值。

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
		{
			// Agent Plan 专属端点（2026-10-17 显式服务根语义）：非空非 /api/v3
			// 路径视为用户显式指定的完整服务根，直拼不再插入第二个服务根。
			name:         "agent plan explicit service root kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/chat/completions",
		},
		{
			name:         "agent plan trailing slash kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3/",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/chat/completions",
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

func TestChainVolcengineImagesUpstreamURL(t *testing.T) {
	// 图像出站 URL 三形态钉值（2026-10-17 图像域开通 + Agent Plan 专属端点
	// 修复）：ark 官方 OpenAI 兼容图像端点 /api/v3/images/generations 与
	// chat 共用服务根；base=/api/plan/v3 是用户显式完整服务根，直拼不插根。
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "official root inserts api v3",
			base:         "https://ark.cn-beijing.volces.com",
			pathAndQuery: "/v1/images/generations",
			want:         "https://ark.cn-beijing.volces.com/api/v3/images/generations",
		},
		{
			name:         "base with api v3 dedups",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/images/generations",
			want:         "https://ark.cn-beijing.volces.com/api/v3/images/generations",
		},
		{
			name:         "agent plan explicit service root kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/images/generations",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/images/generations",
		},
		{
			name:         "query preserved",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/images/generations?beta=true",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/images/generations?beta=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainVolcengineImagesUpstreamURL(tc.base, tc.pathAndQuery)
			if err != nil {
				t.Fatalf("chainVolcengineImagesUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("chainVolcengineImagesUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.pathAndQuery, got, tc.want)
			}
		})
	}
	if _, err := chainVolcengineImagesUpstreamURL("://bad", "/v1/images/generations"); err == nil || !strings.Contains(err.Error(), "base_url 无效") {
		t.Fatalf("无效 base_url 应报错：%v", err)
	}
}

func TestChainVolcengineVideoUpstreamURL(t *testing.T) {
	// 媒体任务面出站 URL 三形态钉值：adapter 出站路径自带 /api/v3 服务根
	//（契约 §9.1 /api/v3/contents/generations/tasks 族）；base 为官方裸域名
	// 直拼、base 已含 /api/v3 去重、base=/api/plan/v3 显式完整服务根剥前缀
	// 直拼（/api/plan/v3 + /contents/generations/tasks）。
	cases := []struct {
		name, base, path, want string
	}{
		{
			name: "official root keeps adapter service root",
			base: "https://ark.cn-beijing.volces.com",
			path: "/api/v3/contents/generations/tasks",
			want: "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks",
		},
		{
			name: "base with api v3 dedups adapter prefix",
			base: "https://ark.cn-beijing.volces.com/api/v3",
			path: "/api/v3/contents/generations/tasks",
			want: "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks",
		},
		{
			name: "agent plan explicit service root strips adapter prefix",
			base: "https://ark.cn-beijing.volces.com/api/plan/v3",
			path: "/api/v3/contents/generations/tasks",
			want: "https://ark.cn-beijing.volces.com/api/plan/v3/contents/generations/tasks",
		},
		{
			name: "agent plan poll path strips adapter prefix",
			base: "https://ark.cn-beijing.volces.com/api/plan/v3/",
			path: "/api/v3/contents/generations/tasks/job-1",
			want: "https://ark.cn-beijing.volces.com/api/plan/v3/contents/generations/tasks/job-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chainVolcengineVideoUpstreamURL(tc.base, tc.path)
			if err != nil {
				t.Fatalf("chainVolcengineVideoUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("chainVolcengineVideoUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
	if _, err := chainVolcengineVideoUpstreamURL("https://[bad", "/api/v3/contents/generations/tasks"); err == nil || !strings.Contains(err.Error(), "base_url 无效") {
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

func TestChainVolcengineEmbeddingsUpstreamURL(t *testing.T) {
	// 向量出站 URL 钉值（2026-10-17 向量承接批）：ark 官方 OpenAI 兼容向量
	// 端点 /api/v3/embeddings 与 chat 共用服务根与归一（chainVolcengineChat
	// UpstreamURL 显式路径模式）；不得走 openai BuildUpstreamURL 的 /v1 强制
	// 补缀（官方根会错拼 /v1/embeddings——上游 404 实证，vector 批接入前
	// 网关真实出站日志 chain_driver.go 向量分支注释）。
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "official root inserts api v3",
			base:         "https://ark.cn-beijing.volces.com",
			pathAndQuery: "/v1/embeddings",
			want:         "https://ark.cn-beijing.volces.com/api/v3/embeddings",
		},
		{
			name:         "base with api v3 dedups",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/embeddings",
			want:         "https://ark.cn-beijing.volces.com/api/v3/embeddings",
		},
		{
			name:         "agent plan explicit service root kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/embeddings",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/embeddings",
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
	if !chainIsEmbeddingsPathAndQuery("/v1/embeddings") || !chainIsEmbeddingsPathAndQuery("/v1/embeddings?x=1") || chainIsEmbeddingsPathAndQuery("/v1/chat/completions") {
		t.Fatalf("chainIsEmbeddingsPathAndQuery 判定不符")
	}
}
