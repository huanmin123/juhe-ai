package main

// 对话批出站 URL 归一钉值（上游 Base URL 版本根语义 v2）：base_url 即版本根
//（ark /api/v3），chat/images/embeddings 与其他 openai 族同走 openai 直拼
// 归一（入站剥 /v1 后直拼，不再有 volcengine 专用服务根插根/去重归一——
// 原 chainVolcengineChatUpstreamURL / chainVolcengineImagesUpstreamURL /
// chainVolcengineVideoUpstreamURL 与 chainIsEmbeddingsPathAndQuery 随 v2 删
// 除）；qwen chat 通用 opPath 恒拼 /compatible-mode/v1 厂商前缀（R6）后直拼
//（base=官方 origin），不再有 strict 插根/去重分支。
// 原 2026-10-17 Agent Plan 显式服务根语义随 v2 收敛：base_url 配置为哪段
// 版本根，最终 URL 就是那段版本根 + opPath，网关不改写。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
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
	// 动机（v2 直拼契约重写）：volcengine 不再有专用 chat 归一——base_url 即
	// 版本根（官方 /api/v3），经 gatewayopenai.BuildUpstreamURL 直拼；入站
	// /v1 剥离保留。原「official root inserts api v3」裸域插根用例随 v2 删除
	//（裸域 base 是 v2 契约下的错误配置，直拼得 /chat/completions）。
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "version root api v3 direct concat",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:         "trailing slash trimmed",
			base:         "https://ark.cn-beijing.volces.com/api/v3/",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:         "query preserved",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/chat/completions?beta=true",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions?beta=true",
		},
		{
			name:         "already-stripped client path",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			// Agent Plan 专属端点：用户把 base_url 配成显式版本根
			// /api/plan/v3，直拼不改写（v2：网关不再判定服务根形态）。
			name:         "agent plan explicit version root kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/chat/completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gatewayopenai.BuildUpstreamURL(tc.base, tc.pathAndQuery)
			if err != nil {
				t.Fatalf("BuildUpstreamURL(%q, %q) error: %v", tc.base, tc.pathAndQuery, err)
			}
			if got != tc.want {
				t.Fatalf("BuildUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.pathAndQuery, got, tc.want)
			}
		})
	}
}

func TestChainQwenChatUpstreamURL(t *testing.T) {
	// 动机（v2 + R6 重写）：qwen chat 通用 opPath 恒拼 chainQwenChatPathPrefix
	//（/compatible-mode/v1）后与 base_url 直拼；不再有「base 已含服务根去重」
	// 与「媒体服务根 base 插根」分支——chat lane 的 base_url 契约是官方
	// origin（DashScope 双根特例：chat 走 compatible-mode，媒体任务走
	// /api/v1 全路径）。
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "official origin gets compatible-mode prefix",
			base:         "https://dashscope.aliyuncs.com",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name:         "trailing slash trimmed",
			base:         "https://dashscope.aliyuncs.com/",
			pathAndQuery: "/chat/completions",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name:         "query preserved",
			base:         "https://dashscope.aliyuncs.com",
			pathAndQuery: "/v1/chat/completions?beta=true",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions?beta=true",
		},
		{
			// v2 契约钉值：前缀恒拼，base 配置成兼容模式全根会重复前缀——
			// chat lane 的 base_url 应配置 origin（不再做去重改写）。
			name:         "prefix always prepended (no dedup branch)",
			base:         "https://dashscope.aliyuncs.com/compatible-mode/v1",
			pathAndQuery: "/v1/chat/completions",
			want:         "https://dashscope.aliyuncs.com/compatible-mode/v1/compatible-mode/v1/chat/completions",
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

func TestChainMediaTaskJoinUpstreamURL(t *testing.T) {
	// 动机（v2 直拼契约重写）：媒体任务面不再有厂商服务根归一
	//（原 chainVolcengineVideoUpstreamURL 三形态钉值随 v2 删除），统一走共享
	// upstreamenvelope.JoinUpstreamURL：base_url 即版本根 + adapter opPath
	//（不含版本段）直拼。Agent Plan 用户把 base_url 配成 /api/plan/v3 即
	// 直拼该版本根。
	cases := []struct {
		name, base, path, want string
	}{
		{
			name: "version root api v3 + version-less adapter opPath",
			base: "https://ark.cn-beijing.volces.com/api/v3",
			path: "/contents/generations/tasks",
			want: "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks",
		},
		{
			name: "trailing slash trimmed",
			base: "https://ark.cn-beijing.volces.com/api/v3/",
			path: "/contents/generations/tasks/job-1",
			want: "https://ark.cn-beijing.volces.com/api/v3/contents/generations/tasks/job-1",
		},
		{
			name: "agent plan explicit version root kept",
			base: "https://ark.cn-beijing.volces.com/api/plan/v3",
			path: "/contents/generations/tasks",
			want: "https://ark.cn-beijing.volces.com/api/plan/v3/contents/generations/tasks",
		},
		{
			name: "qwen dual-root full opPath kept as-is",
			base: "https://dashscope.aliyuncs.com",
			path: "/api/v1/services/aigc/video-generation/video-synthesis",
			want: "https://dashscope.aliyuncs.com/api/v1/services/aigc/video-generation/video-synthesis",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upstreamenvelope.JoinUpstreamURL(tc.base, tc.path)
			if err != nil {
				t.Fatalf("upstreamenvelope.JoinUpstreamURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("upstreamenvelope.JoinUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
			}
		})
	}
	// 共享 JoinUpstreamURL 只做非空与 opPath "/" 前缀校验（URL 形态合法性
	// 下沉到出站请求构造/传输面裁决），无效 host 不再在拼接层拒绝。
	joined, err := upstreamenvelope.JoinUpstreamURL("https://[bad", "/contents/generations/tasks")
	if err != nil {
		t.Fatalf("拼接层只做非空与前缀校验，不应拒绝无效 host：%v", err)
	}
	if joined != "https://[bad/contents/generations/tasks" {
		t.Fatalf("无效 host 直拼结果 = %q", joined)
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

	// 动机（v2 直拼契约）：volcengine 账户 base_url 为版本根 /api/v3，chat 与
	// 其他 openai 族同走 openai 直拼归一。
	volcengineAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_volc_chat",
		VendorCode:      "volcengine",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         "https://ark.cn-beijing.volces.com/api/v3",
		APIKey:          "vk-1",
	}
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), volcengineAccount, newChatRequest(t, "doubao-seed-1-6-flash-250815"))
	if err != nil {
		t.Fatalf("volcengine chat build urls: %v", err)
	}
	if len(urls) != 1 || urls[0] != "https://ark.cn-beijing.volces.com/api/v3/chat/completions" {
		t.Fatalf("volcengine chat url = %#v", urls)
	}

	// qwen：官方 origin base，chat 请求恒拼 /compatible-mode/v1 前缀（R6）。
	qwenAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_qwen_chat",
		VendorCode:      "qwen",
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
	// 走前缀直拼。
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
	// v2 直拼拼接（动机：裸域不再补 /v1，base_url 为版本根时原样直拼）。
	plainAccount := gatewaydispatch.AccountCandidate{
		ID:              "acc_volc_plain",
		VendorCode:      "volcengine",
		ProtocolCode:    "openai",
		ProtocolVersion: "v1",
		Type:            "api_key",
		BaseURL:         "https://ark.cn-beijing.volces.com/api/v3",
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
	if len(urls) != 1 || urls[0] != "https://ark.cn-beijing.volces.com/api/v3/responses" {
		t.Fatalf("non-chat url = %#v", urls)
	}
}

func TestChainVolcengineEmbeddingsUpstreamURL(t *testing.T) {
	// 动机（v2 直拼契约重写）：向量面不再有专用归一（原
	// chainVolcengineChatUpstreamURL 显式路径模式与 chainIsEmbeddingsPathAndQuery
	// 判定随 v2 删除）——ark 版本根 /api/v3 由 base_url 承载，/v1/embeddings
	// 与 chat/images 同走 openai 直拼归一（入站 /v1 剥离后直拼）。
	cases := []struct {
		name, base, pathAndQuery, want string
	}{
		{
			name:         "version root api v3 direct concat",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/embeddings",
			want:         "https://ark.cn-beijing.volces.com/api/v3/embeddings",
		},
		{
			name:         "multimodal subpath direct concat",
			base:         "https://ark.cn-beijing.volces.com/api/v3",
			pathAndQuery: "/v1/embeddings/multimodal",
			want:         "https://ark.cn-beijing.volces.com/api/v3/embeddings/multimodal",
		},
		{
			name:         "agent plan explicit version root kept",
			base:         "https://ark.cn-beijing.volces.com/api/plan/v3",
			pathAndQuery: "/v1/embeddings",
			want:         "https://ark.cn-beijing.volces.com/api/plan/v3/embeddings",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gatewayopenai.BuildUpstreamURL(tc.base, tc.pathAndQuery)
			if err != nil {
				t.Fatalf("BuildUpstreamURL(%q, %q) error: %v", tc.base, tc.pathAndQuery, err)
			}
			if got != tc.want {
				t.Fatalf("BuildUpstreamURL(%q, %q) = %q, want %q", tc.base, tc.pathAndQuery, got, tc.want)
			}
		})
	}
}
