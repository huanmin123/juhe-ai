package playground

// 账户目录分类与 GET /accounts 测试（设计 §3.2）+ fetch_url 纯函数面测试
// （SSRF 拒绝表、HTML 剥取、实体解码、字节截断）。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// TestClassifyCatalogModels 账户目录分类表：chat 三协议、functionCalling 任一
// 协议声明、image 家族白名单、video/audio 供应商 × 协议 × 端点模式、generic_http
// 账户目录、白名单外供应商不产媒体行。
func TestClassifyCatalogModels(t *testing.T) {
	apiKeyView := TransportAccount{
		ID: "acc_1", Type: "api_key", ProviderCode: "gpt",
		SupportedEndpointModes: []string{"chat_sse", "video_create", "audio_speech"},
		SupportedModels:        []string{"gpt-image-2", "grok-imagine-video-1.5", "speech-2.8-hd"},
	}
	oauthView := TransportAccount{
		ID: "acc_2", Type: "oauth", ProviderCode: "xai",
		SupportedEndpointModes: []string{"chat_sse"},
	}
	cases := []struct {
		name         string
		providerCode string
		catalog      []CatalogItem
		view         *TransportAccount
		want         map[string]ModelKind // modelId → kind
	}{
		{
			name:         "chat 三协议 + function_calling",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "m-chat", SupportedAPIProtocols: []string{"chat_completions"}, SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}}},
				{Model: "m-claude", SupportedAPIProtocols: []string{"messages"}},
				{Model: "m-gemini", SupportedAPIProtocols: []string{"generate_content"}},
				{Model: "m-tts", SupportedAPIProtocols: []string{"audio_speech"}},
				{Model: "m-embed", SupportedAPIProtocols: []string{"embeddings"}},
			},
			view: &apiKeyView,
			want: map[string]ModelKind{"m-chat": KindChat, "m-claude": KindChat, "m-gemini": KindChat},
		},
		{
			name:         "functionCalling 跨协议声明（responses 行）",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "m-hybrid", SupportedAPIProtocols: []string{"chat_completions", "responses"}, SupportedToolsByProtocol: map[string][]string{"responses": {"web_search"}}},
				{Model: "m-tool", SupportedAPIProtocols: []string{"responses"}, SupportedToolsByProtocol: map[string][]string{"responses": {"function_calling"}}},
			},
			view: &apiKeyView,
			want: map[string]ModelKind{"m-hybrid": KindChat},
		},
		{
			name:         "image 家族白名单（gpt × gpt-image-2，xai × grok-imagine）",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}},
				{Model: "other-image-model", SupportedAPIProtocols: []string{"image_generation"}},
			},
			view: &apiKeyView,
			want: map[string]ModelKind{"gpt-image-2": KindImage},
		},
		{
			name:         "xai 生图家族 + 非 api_key 账户不可路由",
			providerCode: "xai",
			catalog:      []CatalogItem{{Model: "grok-imagine-image", SupportedAPIProtocols: []string{"image_generation"}}},
			view:         &oauthView,
			want:         map[string]ModelKind{},
		},
		{
			name:         "video/audio 白名单供应商 × 协议 × 端点模式",
			providerCode: "gemini",
			catalog: []CatalogItem{
				{Model: "veo-3", SupportedAPIProtocols: []string{"video", "generate_content"}},
				{Model: "tts-hd", SupportedAPIProtocols: []string{"audio_speech"}},
			},
			view: &TransportAccount{
				ID: "acc_g", Type: "api_key", ProviderCode: "gemini",
				SupportedEndpointModes: []string{"chat_sse", "video_create", "audio_speech"},
				SupportedModels:        []string{"veo-3", "tts-hd"},
			},
			want: map[string]ModelKind{"veo-3": KindVideo, "tts-hd": KindAudio, "veo-3-chat": KindChat},
		},
		{
			name:         "非白名单供应商无媒体行",
			providerCode: "anthropic",
			catalog: []CatalogItem{
				{Model: "claude-video", SupportedAPIProtocols: []string{"video"}},
			},
			view: &apiKeyView,
			want: map[string]ModelKind{},
		},
		{
			name:         "端点模式缺失不列媒体行",
			providerCode: "minimax",
			catalog: []CatalogItem{
				{Model: "video-no-mode", SupportedAPIProtocols: []string{"video"}},
			},
			view: &TransportAccount{ID: "acc_9", Type: "api_key", ProviderCode: "minimax", SupportedEndpointModes: []string{"chat_sse"}, SupportedModels: []string{"video-no-mode"}},
			want: map[string]ModelKind{},
		},
		{
			name:         "传输视图缺失仅产 chat 目录分类",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "chat-only", SupportedAPIProtocols: []string{"chat_completions"}},
				{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}},
			},
			view: nil,
			want: map[string]ModelKind{"chat-only": KindChat},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyCatalogModels(testCase.providerCode, testCase.catalog, testCase.view)
			gotMap := map[string]ModelKind{}
			for _, option := range got {
				gotMap[option.ModelID] = option.Kind
			}
			for modelID, kind := range testCase.want {
				if modelID == "veo-3-chat" {
					continue // 特殊断言键在下方单独处理
				}
				if gotMap[modelID] != kind {
					t.Fatalf("model %s kind = %v，期望 %v（全部结果 %v）", modelID, gotMap[modelID], kind, got)
				}
			}
			for modelID := range gotMap {
				if _, expected := testCase.want[modelID]; !expected {
					t.Fatalf("意外的分类结果 %s=%v（期望集 %v）", modelID, gotMap[modelID], testCase.want)
				}
			}
			// 特殊键：veo-3 行同时声明 video 与 generate_content → chat+video 双行。
			if _, expected := testCase.want["veo-3-chat"]; expected {
				kinds := map[ModelKind]bool{}
				for _, option := range got {
					if option.ModelID == "veo-3" {
						kinds[option.Kind] = true
					}
				}
				if !kinds[KindChat] || !kinds[KindVideo] {
					t.Fatalf("veo-3 应同时产出 chat 与 video 行: %v", got)
				}
			}
		})
	}
}

// TestFunctionCallingAnyProtocol functionCalling = 任一协议行声明 function_calling
// （chat supportsTool 口径：仅声明在 messages 行也判真）。
func TestFunctionCallingAnyProtocol(t *testing.T) {
	view := TransportAccount{ID: "a", Type: "api_key", ProviderCode: "gpt", SupportedEndpointModes: []string{"chat_sse"}}
	catalog := []CatalogItem{{
		Model:                    "m",
		SupportedAPIProtocols:    []string{"chat_completions", "messages"},
		SupportedToolsByProtocol: map[string][]string{"messages": {"function_calling"}},
	}}
	options := classifyCatalogModels("gpt", catalog, &view)
	if len(options) != 1 || options[0].Kind != KindChat || !options[0].FunctionCalling {
		t.Fatalf("functionCalling 判定不符: %+v", options)
	}
}

// TestAccountsEndpoint GET /accounts 集成：active 且启用才入列（inactive 剔除、
// 快照缺失剔除），{data} 信封内响应形状 accountId/accountName/providerCode/models。
func TestAccountsEndpoint(t *testing.T) {
	deps := &Deps{
		Accounts: &staticAccounts{options: []AccountOption{
			{ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Status: "active"},
			{ID: "acc_2", Name: "停用账户", ProviderCode: "gpt", Status: "disabled"},
			{ID: "acc_3", Name: "无快照账户", ProviderCode: "gpt", Status: "active"},
		}},
		AccountLookup: &staticLookup{refs: map[string]*AccountRef{
			"acc_1": {ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp_1"}},
			"acc_3": {ID: "acc_3", Name: "无快照账户", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp_missing"}},
		}},
		TransportAccounts: &staticTransport{views: map[string][]TransportAccount{
			"acc_1": {{ID: "acc_1", Type: "api_key", ProviderCode: "gpt", SupportedEndpointModes: []string{"chat_sse"}}},
		}},
		ProviderCatalog: &staticCatalog{byProvider: map[string][]CatalogItem{
			"gpt": {
				{Model: "gpt-5.4", SupportedAPIProtocols: []string{"chat_completions"}, SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}}},
				{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}},
			},
		}},
		AccountCatalog: &staticCatalog{},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/accounts", nil)
	if recorder.Code != 200 {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Accounts []struct {
				AccountID    string `json:"accountId"`
				AccountName  string `json:"accountName"`
				ProviderCode string `json:"providerCode"`
				Models       []struct {
					ModelID         string `json:"modelId"`
					Kind            string `json:"kind"`
					FunctionCalling bool   `json:"functionCalling"`
				} `json:"models"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(payload.Data.Accounts) != 1 || payload.Data.Accounts[0].AccountID != "acc_1" {
		t.Fatalf("accounts = %+v（应只含 acc_1：disabled 与无快照剔除）", payload.Data.Accounts)
	}
	entry := payload.Data.Accounts[0]
	if entry.AccountName != "神影-克隆" || entry.ProviderCode != "gpt" {
		t.Fatalf("账户投影不符: %+v", entry)
	}
	if len(entry.Models) != 2 {
		t.Fatalf("模型行数量不符: %+v（chat gpt-5.4 + image gpt-image-2，api_key 账户模型清单空 = 不限制）", entry.Models)
	}
	if entry.Models[0].ModelID != "gpt-5.4" || entry.Models[0].Kind != "chat" || !entry.Models[0].FunctionCalling {
		t.Fatalf("chat 行不符: %+v", entry.Models[0])
	}
	if entry.Models[1].ModelID != "gpt-image-2" || entry.Models[1].Kind != "image" {
		t.Fatalf("image 行不符: %+v", entry.Models[1])
	}
}

// TestAccountsEndpointEmptyList 信封恒为数组：无可见账户时 {data:{accounts:[]}}
// （非 null）。
func TestAccountsEndpointEmptyList(t *testing.T) {
	deps := &Deps{
		Accounts:          &staticAccounts{options: []AccountOption{}},
		AccountLookup:     &staticLookup{refs: map[string]*AccountRef{}},
		TransportAccounts: &staticTransport{},
		ProviderCatalog:   &staticCatalog{},
		AccountCatalog:    &staticCatalog{},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/accounts", nil)
	body := strings.TrimSpace(recorder.Body.String())
	if !strings.HasPrefix(body, `{"data":{"accounts":[`) {
		t.Fatalf("空列表信封不符: %s", body)
	}
}

// TestAccountsEndpointEnvelopeContract 成功载荷信封钉测：JSON 成功端点统一以
// {data:{...}} 包裹（chat 面 okEnvelope / shared/http.ts ok(data) 同款）；此处
// 以 accounts 端点做字节级前缀/后缀断言并复核载荷可解析。
func TestAccountsEndpointEnvelopeContract(t *testing.T) {
	deps := &Deps{
		Accounts: &staticAccounts{options: []AccountOption{
			{ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Status: "active"},
		}},
		AccountLookup: &staticLookup{refs: map[string]*AccountRef{
			"acc_1": {ID: "acc_1", Name: "神影-克隆", ProviderCode: "gpt", Enabled: true, EnabledGroupIDs: []string{"grp_1"}},
		}},
		TransportAccounts: &staticTransport{views: map[string][]TransportAccount{
			"acc_1": {{ID: "acc_1", Type: "api_key", ProviderCode: "gpt", SupportedEndpointModes: []string{"chat_sse"}}},
		}},
		ProviderCatalog: &staticCatalog{byProvider: map[string][]CatalogItem{
			"gpt": {{Model: "gpt-5.4", SupportedAPIProtocols: []string{"chat_completions"}}},
		}},
		AccountCatalog: &staticCatalog{},
	}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/accounts", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	body := strings.TrimSpace(recorder.Body.String())
	if !strings.HasPrefix(body, `{"data":{"accounts":[`) || !strings.HasSuffix(body, `]}}`) {
		t.Fatalf("成功载荷必须以 {data:...} 信封包裹: %s", body)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("信封解析失败: %v", err)
	}
	var inner accountsResponse
	if err := json.Unmarshal(envelope.Data, &inner); err != nil {
		t.Fatalf("信封内载荷解析失败: %v", err)
	}
	if len(inner.Accounts) != 1 || inner.Accounts[0].AccountID != "acc_1" {
		t.Fatalf("信封内载荷不符: %+v", inner.Accounts)
	}
}

// TestIPForbiddenTable SSRF 拒绝面表：私网/环回/链路本地/组播/未指定全部拒绝，
// 公网 IP（v4/v6）放行；IPv4-mapped IPv6 还原后判定。
func TestIPForbiddenTable(t *testing.T) {
	cases := []struct {
		ip        string
		forbidden bool
	}{
		{"127.0.0.1", true},
		{"10.1.2.3", true},
		{"172.16.0.9", true},
		{"172.31.255.1", true},
		{"192.168.1.1", true},
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"224.0.0.5", true},
		{"239.255.255.250", true},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"ff02::1", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:192.168.0.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700::1111", false},
	}
	for _, testCase := range cases {
		ip := net.ParseIP(testCase.ip)
		if ip == nil {
			t.Fatalf("IP 解析失败: %s", testCase.ip)
		}
		if got := ipForbidden(ip); got != testCase.forbidden {
			t.Fatalf("ipForbidden(%s) = %v，期望 %v", testCase.ip, got, testCase.forbidden)
		}
	}
}

// TestValidateFetchURLScheme 表：仅 http/https；IP 字面量直接判定（不触 DNS）。
func TestValidateFetchURLScheme(t *testing.T) {
	cases := []struct {
		rawURL   string
		wantFail bool
		contains string
	}{
		{"ftp://example.com/file", true, "http/https"},
		{"file:///etc/passwd", true, "http/https"},
		{"data:text/html,hi", true, "http/https"},
		{"http:///no-host", true, "主机名"},
		{"http://127.0.0.1/x", true, "内网"},
		{"https://192.168.1.1/x", true, "内网"},
		{"http://[::1]/x", true, "内网"},
		{"http://169.254.169.254/latest/meta-data", true, "内网"},
		{"https://8.8.8.8/x", false, ""},
		{"https://example.com/page", false, ""},
	}
	for _, testCase := range cases {
		_, err := validateFetchURL(testCase.rawURL)
		if testCase.wantFail && err == nil {
			t.Fatalf("%s 应被拒绝", testCase.rawURL)
		}
		if testCase.wantFail && testCase.contains != "" && !strings.Contains(err.Error(), testCase.contains) {
			t.Fatalf("%s 拒绝语义不符: %v", testCase.rawURL, err)
		}
		if !testCase.wantFail && err != nil {
			t.Fatalf("%s 不应被拒绝: %v", testCase.rawURL, err)
		}
	}
}

// TestStripHTMLToText HTML 剥取：脚本/样式块整块移除、块级标签折行、实体解码、
// 空白收敛、结果 ≤50KB。
func TestStripHTMLToText(t *testing.T) {
	html := []byte(`<!DOCTYPE html><html><head><style>.x{color:red}</style>` +
		`<script>var a=1;</script></head><body><!-- 注释 -->` +
		`<h1>标题&amp;副题</h1><p>第一段&#65;<br>换行</p>` +
		`<div>  多   空格 </div><a href="x">链接</a></body></html>`)
	text := stripHTMLToText(html)
	for _, banned := range []string{"var a=1", "color:red", "注释", "<"} {
		if strings.Contains(text, banned) {
			t.Fatalf("剥取结果残留 %q: %q", banned, text)
		}
	}
	for _, wanted := range []string{"标题&副题", "第一段A", "换行", "链接"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("剥取结果缺少 %q: %q", wanted, text)
		}
	}
}

// TestClampTextBytes 截断不劈开多字节字符。
func TestClampTextBytes(t *testing.T) {
	text := strings.Repeat("汉", 100) // 每字 3 字节
	cut := clampTextBytes(text, 100)
	if len(cut) != 99 { // 3*33=99，第 34 字被截断回退
		t.Fatalf("截断长度 = %d，期望 99（UTF-8 边界回退）", len(cut))
	}
	if clampTextBytes("short", 100) != "short" {
		t.Fatalf("短文本不应截断")
	}
}

// TestFetchFailureToolContent ensure executeFetchTool 的失败文案契约（「抓取
// 失败:<原因>」）在参数与执行两层都成立。
func TestFetchFailureToolContent(t *testing.T) {
	deps := &Deps{FetchURL: func(context.Context, string) (FetchResult, error) {
		return FetchResult{}, errors.New("连接超时")
	}}
	routes := &playgroundRoutes{deps: deps}
	text, ok := routes.executeFetchTool(context.Background(), chatToolCall{
		CallID: "c", ToolName: fetchURLToolName, ArgumentsJSON: `{"url":"https://example.com/x"}`,
	})
	if ok || !strings.Contains(text, "抓取失败: 连接超时") {
		t.Fatalf("执行失败文案不符: ok=%v text=%q", ok, text)
	}
	text, ok = routes.executeFetchTool(context.Background(), chatToolCall{
		CallID: "c", ToolName: fetchURLToolName, ArgumentsJSON: `not-json`,
	})
	if ok || !strings.Contains(text, "抓取失败") {
		t.Fatalf("参数失败文案不符: ok=%v text=%q", ok, text)
	}
}

// brokenReader 是恒返回读取错误的 Body（readErr 包装路径回放用）。
type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("故意读失败") }

// TestFetchURLReadErrorWrapsCause 响应体读取失败时错误必须包装 readErr 本身
// （文案携带真实原因，不得出现 %!w(<nil>) 之类的格式化残留）。
func TestFetchURLReadErrorWrapsCause(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(brokenReader{}),
		}, nil
	})}
	_, err := newDefaultFetchURL(client)(context.Background(), "https://93.184.216.34/page")
	if err == nil {
		t.Fatalf("响应体读取失败应返回错误")
	}
	if strings.Contains(err.Error(), "%!w") {
		t.Fatalf("错误文案包含格式化残留 %%!w: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "响应读取失败") || !strings.Contains(err.Error(), "故意读失败") {
		t.Fatalf("错误文案应携带读取失败原因: %q", err.Error())
	}
}
