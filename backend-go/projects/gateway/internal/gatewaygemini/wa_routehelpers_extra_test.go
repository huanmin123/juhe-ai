package gatewaygemini

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// 端点族识别：models、generateContent 族、countTokens、embedContent。
func TestWAEndpointFamilyFromPath(t *testing.T) {
	cases := []struct {
		path   string
		family EndpointFamily
	}{
		{"/v1beta/models", EndpointFamilyModels},
		{"/models", EndpointFamilyModels},
		{"/v1beta/models/gemini-2:generatecontent", EndpointFamilyGenerateContent},
		{"/models/gemini-2:streamgeneratecontent?alt=sse", EndpointFamilyStreamGenerateContent},
		{"/v1beta/models/gemini-2:counttokens", EndpointFamilyCountTokens},
		{"/models/gemini-2:embedcontent", EndpointFamilyEmbedContent},
		{"/MODELS/GEMINI-2:GENERATECONTENT", EndpointFamilyGenerateContent}, // 小写化匹配
		{"/v1beta/models/gemini-2:unknownaction", ""},
		// 动作正则只要求最后一个冒号段匹配，模型段可含冒号（现行行为）。
		{"/models/gemini-2:extra:generatecontent", EndpointFamilyGenerateContent},
		{"/v1beta/interactions", EndpointFamilyInteractions},
		{"/other", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := EndpointFamilyFromPath(tc.path); got != tc.family {
			t.Fatalf("EndpointFamilyFromPath(%q) = %q，期望 %q", tc.path, got, tc.family)
		}
	}
}

func TestWAStripV1BetaPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/v1beta/models", "/models"},
		{"/V1Beta/models", "/models"},
		{"/v1beta", ""},
		{"/v1betaX/models", "/v1betaX/models"},
		{"/models", "/models"},
	}
	for _, tc := range cases {
		if got := stripV1BetaPrefix(tc.in); got != tc.want {
			t.Fatalf("stripV1BetaPrefix(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestWAIsModelsAndInteractionsRequest(t *testing.T) {
	models := httptest.NewRequest("GET", "/v1beta/models?pageSize=10", nil)
	if !IsModelsRequest(models) || !IsNativeRequest(models) {
		t.Fatal("GET models 应命中")
	}
	post := httptest.NewRequest("POST", "/v1beta/models", nil)
	if IsModelsRequest(post) {
		t.Fatal("POST models 不应命中 IsModelsRequest")
	}
	if IsModelsRequest(nil) {
		t.Fatal("nil 请求不应命中")
	}
	interactions := httptest.NewRequest("POST", "/v1beta/interactions", nil)
	if !IsInteractionsRequest(interactions) {
		t.Fatal("interactions 路径应命中")
	}
	other := httptest.NewRequest("GET", "/v1beta/other", nil)
	if IsInteractionsRequest(other) || IsNativeRequest(other) {
		t.Fatal("非协议路径不应命中")
	}
	if IsNativeRequest(nil) {
		t.Fatal("nil 请求不应命中 IsNativeRequest")
	}
	// embedContent/countTokens/streamGenerateContent 原生 POST。
	for _, target := range []string{
		"/v1beta/models/m:generatecontent",
		"/v1beta/models/m:streamgeneratecontent",
		"/v1beta/models/m:counttokens",
		"/v1beta/models/m:embedcontent",
		"/v1beta/interactions/abc",
	} {
		if !IsNativeRequest(httptest.NewRequest("POST", target, nil)) {
			t.Fatalf("POST %s 应为原生请求", target)
		}
	}
	if IsNativeRequest(httptest.NewRequest("GET", "/v1beta/models/m:generatecontent", nil)) {
		t.Fatal("GET generateContent 不应命中")
	}
}

func TestWAEndpointModeForRequestShapeAll(t *testing.T) {
	cases := []struct {
		endpoint string
		stream   bool
		want     string
	}{
		{"/v1beta/models/m:generatecontent", false, EndpointModeGenerateContentJSON},
		{"/v1beta/models/m:generatecontent", true, EndpointModeGenerateContentSSE},
		{"/v1beta/models/m:streamgeneratecontent", false, EndpointModeGenerateContentSSE},
		{"/v1beta/models/m:streamgeneratecontent", true, EndpointModeGenerateContentSSE},
		{"/v1beta/models/m:counttokens", false, EndpointModeCountTokens},
		{"/v1beta/models/m:embedcontent", false, EndpointModeEmbedContent},
		{"/v1beta/interactions", false, EndpointModeInteractionsJSON},
		{"/v1beta/interactions", true, EndpointModeInteractionsSSE},
		{"/v1beta/other", false, ""},
	}
	for _, tc := range cases {
		if got := EndpointModeForRequestShape(tc.endpoint, tc.stream); got != tc.want {
			t.Fatalf("EndpointModeForRequestShape(%q,%v) = %q，期望 %q", tc.endpoint, tc.stream, got, tc.want)
		}
	}
}

func TestWARequestPathAndQueryGemini(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1beta/models?alt=sse", nil)
	if got := RequestPathAndQuery(request); got != "/v1beta/models?alt=sse" {
		t.Fatalf("RequestPathAndQuery = %q", got)
	}
	if got := RequestPathAndQuery(nil); got != "/" {
		t.Fatalf("nil 请求 = %q", got)
	}
}

// SSE 判定：query stream=true、body stream 标志、Accept 头。
func TestWARequestIndicatesSSE(t *testing.T) {
	cases := []struct {
		name       string
		target     string
		accept     string
		bodyStream bool
		want       bool
	}{
		{"query stream=true", "/models/m:generatecontent?stream=true", "", false, true},
		{"query stream=TRUE 大小写", "/x?stream=TRUE", "", false, true},
		{"query stream=false", "/x?stream=false", "", false, false},
		{"body stream", "/x", "", true, true},
		{"Accept 头", "/x", "text/event-stream", false, true},
		{"Accept 混合", "/x", "application/json, text/event-stream", false, true},
		{"无信号", "/x", "application/json", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", tc.target, nil)
			if tc.accept != "" {
				request.Header.Set("Accept", tc.accept)
			}
			if got := RequestIndicatesSSE(request, tc.bodyStream); got != tc.want {
				t.Fatalf("RequestIndicatesSSE = %v，期望 %v", got, tc.want)
			}
		})
	}
}

func TestWABuildUpstreamURLGemini(t *testing.T) {
	cases := []struct {
		name, base, path string
		stream           bool
		want             string
		wantErr          bool
	}{
		{"默认 base", "", "/v1beta/models/m:generatecontent", false, "https://generativelanguage.googleapis.com/v1beta/models/m:generatecontent", false},
		{"query 合并并删 key", "https://proxy.example", "/v1beta/models/m:generatecontent?key=secret&foo=bar", false, "https://proxy.example/v1beta/models/m:generatecontent?foo=bar", false},
		{"stream 补 alt=sse", "https://proxy.example", "/v1beta/models/m:streamgeneratecontent", true, "https://proxy.example/v1beta/models/m:streamgeneratecontent?alt=sse", false},
		{"base 已含 /v1beta 去重", "https://proxy.example/v1beta/", "/v1beta/models", false, "https://proxy.example/v1beta/models", false},
		{"interactions 删除 alt", "https://proxy.example", "/v1beta/interactions?alt=sse&x=1", false, "https://proxy.example/v1beta/interactions?x=1", false},
		{"interactions stream 保留 alt=sse", "https://proxy.example", "/v1beta/interactions", true, "https://proxy.example/v1beta/interactions?alt=sse", false},
		{"非法 base 报错", "://bad", "/v1beta/models", false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildUpstreamURL(tc.base, tc.path, tc.stream)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildUpstreamURL error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("BuildUpstreamURL = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestWABuildUpstreamURLsForAccountGemini(t *testing.T) {
	generate := httptest.NewRequest("POST", "/v1beta/models/gemini-2:generatecontent", nil)
	t.Run("api_key 账户", func(t *testing.T) {
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://proxy.example"}, generate, false)
		if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://proxy.example/v1beta/models/gemini-2:generatecontent") {
			t.Fatalf("api_key = %v", urls)
		}
	})
	t.Run("google_oauth 账户", func(t *testing.T) {
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "google_oauth", BaseURL: ""}, generate, false)
		if len(urls) != 1 {
			t.Fatalf("google_oauth = %v", urls)
		}
	})
	t.Run("非支持账户类型", func(t *testing.T) {
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key_oauth"}, generate, false); urls != nil {
			t.Fatalf("非支持类型应 nil: %v", urls)
		}
	})
	t.Run("非原生请求", func(t *testing.T) {
		other := httptest.NewRequest("GET", "/v1beta/other", nil)
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://p.example"}, other, false); urls != nil {
			t.Fatalf("非原生应 nil: %v", urls)
		}
	})
	t.Run("models 仅探测请求放行", func(t *testing.T) {
		models := httptest.NewRequest("GET", "/v1beta/models", nil)
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://p.example"}, models, false); urls != nil {
			t.Fatalf("非探测 models 应 nil: %v", urls)
		}
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://p.example"}, models, true)
		if len(urls) != 1 || urls[0] != "https://p.example/v1beta/models" {
			t.Fatalf("探测 models = %v", urls)
		}
	})
	t.Run("interactions query stream=true 判定流式", func(t *testing.T) {
		sse := httptest.NewRequest("POST", "/v1beta/interactions?stream=true", nil)
		urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "https://p.example"}, sse, false)
		if len(urls) != 1 || !strings.Contains(urls[0], "alt=sse") {
			t.Fatalf("interactions SSE = %v", urls)
		}
	})
	t.Run("非法 base", func(t *testing.T) {
		if urls := BuildUpstreamURLsForAccount(UpstreamAccount{Type: "api_key", BaseURL: "://bad"}, generate, false); urls != nil {
			t.Fatalf("非法 base 应 nil: %v", urls)
		}
	})
}

// 模型目录投影：Gemini models 列表响应装配。
func TestWABuildModelsResponseGemini(t *testing.T) {
	response := BuildModelsResponse([]ModelCatalogItem{
		{Model: "gemini-2.5-pro", CapabilityNotes: "思考模型", MaxInputTokens: 100, MaxOutputTokens: 50,
			SupportedAPIProtocols: []string{"generate_content", "count_tokens", "embed_content"}},
		{Model: "models/gemini-2.5-flash", Notes: "快速", ContextWindowTokens: 200,
			SupportedAPIProtocols: []string{"stream_generate_content"}},
		{Model: "gemini-3"},
	})
	if len(response.Models) != 3 {
		t.Fatalf("模型数 = %d", len(response.Models))
	}
	first := response.Models[0]
	if first.Name != "models/gemini-2.5-pro" || first.Version != "gemini-2.5-pro" || first.DisplayName != "gemini-2.5-pro" {
		t.Fatalf("首模型 = %+v", first)
	}
	if first.Description != "思考模型" || first.InputTokenLimit != 100 || first.OutputTokenLimit != 50 {
		t.Fatalf("首模型字段 = %+v", first)
	}
	if !waSliceEqual(first.SupportedGenerationMethods, []string{"generateContent", "countTokens", "embedContent"}) {
		t.Fatalf("支持方法 = %v", first.SupportedGenerationMethods)
	}
	second := response.Models[1]
	if second.Name != "models/gemini-2.5-flash" {
		t.Fatalf("models/ 前缀不应重复: %q", second.Name)
	}
	if second.Description != "快速" || second.InputTokenLimit != 200 {
		t.Fatalf("次模型 = %+v", second)
	}
	if !waSliceEqual(second.SupportedGenerationMethods, []string{"generateContent"}) {
		t.Fatalf("stream_generate_content 映射 = %v", second.SupportedGenerationMethods)
	}
	third := response.Models[2]
	if !waSliceEqual(third.SupportedGenerationMethods, []string{"generateContent"}) {
		t.Fatalf("缺省方法 = %v", third.SupportedGenerationMethods)
	}
	// 空目录与非正数边界。
	empty := BuildModelsResponse(nil)
	if len(empty.Models) != 0 {
		t.Fatalf("空目录 = %+v", empty)
	}
	negative := BuildModelsResponse([]ModelCatalogItem{{Model: "m", MaxInputTokens: -5, MaxOutputTokens: 0}})
	if negative.Models[0].InputTokenLimit != 0 || negative.Models[0].OutputTokenLimit != 0 {
		t.Fatalf("非正数应为 0: %+v", negative.Models[0])
	}
}
