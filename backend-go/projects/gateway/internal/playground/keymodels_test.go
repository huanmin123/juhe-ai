package playground

// Key 模型候选分类与 GET /key-models 测试（设计 §3.2 真实 Key 化修订）+ 出站
// URL 守卫纯函数面测试（SSRF 拒绝表）。

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
)

// TestClassifyKeyCatalogModels Key 模型候选分类表（目录侧口径）：chat 三协议、
// image 家族白名单、embedding/rerank 协议声明、video/audio 供应商 × 协议、
// 白名单外供应商不产对应行；同名模型双 kind（veo-3）合法。
func TestClassifyKeyCatalogModels(t *testing.T) {
	cases := []struct {
		name         string
		providerCode string
		catalog      []CatalogItem
		want         []ModelOption
	}{
		{
			name:         "chat 三协议（同目录其余协议行各自归类）",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "m-chat", SupportedAPIProtocols: []string{"chat_completions"}},
				{Model: "m-claude", SupportedAPIProtocols: []string{"messages"}},
				{Model: "m-gemini", SupportedAPIProtocols: []string{"generate_content"}},
				{Model: "m-tts", SupportedAPIProtocols: []string{"audio_speech"}},
				{Model: "m-embed", SupportedAPIProtocols: []string{"embeddings"}},
			},
			want: []ModelOption{
				{ModelID: "m-chat", Kind: KindChat}, {ModelID: "m-claude", Kind: KindChat}, {ModelID: "m-gemini", Kind: KindChat},
				{ModelID: "m-tts", Kind: KindAudio}, {ModelID: "m-embed", Kind: KindEmbedding},
			},
		},
		{
			name:         "image 家族白名单（gpt × gpt-image-2）",
			providerCode: "gpt",
			catalog: []CatalogItem{
				{Model: "gpt-image-2", SupportedAPIProtocols: []string{"image_generation"}},
				{Model: "other-image-model", SupportedAPIProtocols: []string{"image_generation"}},
			},
			want: []ModelOption{{ModelID: "gpt-image-2", Kind: KindImage}},
		},
		{
			name:         "xai 生图家族",
			providerCode: "xai",
			catalog:      []CatalogItem{{Model: "grok-imagine-image", SupportedAPIProtocols: []string{"image_generation"}}},
			want:         []ModelOption{{ModelID: "grok-imagine-image", Kind: KindImage}},
		},
		{
			name:         "白名单供应商 × 双协议双 kind（veo-3）",
			providerCode: "gemini",
			catalog: []CatalogItem{
				{Model: "veo-3", SupportedAPIProtocols: []string{"video", "generate_content"}},
			},
			want: []ModelOption{{ModelID: "veo-3", Kind: KindChat}, {ModelID: "veo-3", Kind: KindVideo}},
		},
		{
			name:         "audio 白名单供应商 × audio_speech 协议",
			providerCode: "gemini",
			catalog: []CatalogItem{
				{Model: "tts-hd", SupportedAPIProtocols: []string{"audio_speech"}},
			},
			want: []ModelOption{{ModelID: "tts-hd", Kind: KindAudio}},
		},
		{
			name:         "embedding/rerank 协议声明（供应商不限媒体白名单）",
			providerCode: "deepseek",
			catalog: []CatalogItem{
				{Model: "text-embedding-v4", SupportedAPIProtocols: []string{"embeddings"}},
				{Model: "gte-rerank-v2", SupportedAPIProtocols: []string{"rerank"}},
			},
			want: []ModelOption{{ModelID: "text-embedding-v4", Kind: KindEmbedding}, {ModelID: "gte-rerank-v2", Kind: KindRerank}},
		},
		{
			name:         "非白名单供应商无媒体行（协议声明保留）",
			providerCode: "anthropic",
			catalog: []CatalogItem{
				{Model: "claude-video", SupportedAPIProtocols: []string{"video"}},
			},
			want: []ModelOption{},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ClassifyKeyCatalogModels(testCase.providerCode, testCase.catalog)
			if len(got) != len(testCase.want) {
				t.Fatalf("分类结果 = %v，期望 %v", got, testCase.want)
			}
			seen := map[string]bool{}
			for _, option := range got {
				seen[option.ModelID+"@"+string(option.Kind)] = true
			}
			for _, wanted := range testCase.want {
				if !seen[wanted.ModelID+"@"+string(wanted.Kind)] {
					t.Fatalf("分类结果缺 %s@%s（全部 %v）", wanted.ModelID, wanted.Kind, got)
				}
			}
		})
	}
}

// TestKeyModelsEndpoint GET /key-models 集成：{data} 信封内响应形状
// {modelId, kind, providerCode}（v1.3 UI 口径无计价字段）；Key 不存在/非本人
// 404 playground_key_not_found；缺 apiKeyId 400。
func TestKeyModelsEndpoint(t *testing.T) {
	deps := &Deps{KeyModels: &mockKeyModels{rows: map[string][]KeyModelRow{
		"key_1": {
			{ModelID: "gpt-5.4", Kind: KindChat, ProviderCode: "gpt"},
			{ModelID: "gpt-image-2", Kind: KindImage, ProviderCode: "gpt"},
		},
	}}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/key-models?apiKeyId=key_1", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Models []KeyModelRow `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(payload.Data.Models) != 2 {
		t.Fatalf("models = %+v", payload.Data.Models)
	}
	first := payload.Data.Models[0]
	if first.ModelID != "gpt-5.4" || first.Kind != KindChat || first.ProviderCode != "gpt" {
		t.Fatalf("首行不符: %+v", first)
	}
	// v1.3 UI 口径：行不含计价投影字段（序列化形状钉测）。
	body := recorder.Body.String()
	if strings.Contains(body, "imageEstimate") {
		t.Fatalf("响应行不得携带计价投影字段: %s", body)
	}

	// 表内无该 Key：404 playground_key_not_found。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/key-models?apiKeyId=key_missing", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("缺失 Key 状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
	var errPayload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &errPayload); err != nil {
		t.Fatalf("错误载荷解析失败: %v", err)
	}
	if errPayload.Code != "playground_key_not_found" {
		t.Fatalf("code = %q，期望 playground_key_not_found", errPayload.Code)
	}

	// 缺 apiKeyId：400。
	recorder = doJSON(mux, "GET", "/__aisys__/api/playground/key-models", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("缺 apiKeyId 状态码 = %d（body %s）", recorder.Code, recorder.Body.String())
	}
}

// TestKeyModelsEndpointEmptyList 信封恒为数组：Key 无候选时 {data:{models:[]}}
// （非 null）。
func TestKeyModelsEndpointEmptyList(t *testing.T) {
	deps := &Deps{KeyModels: &mockKeyModels{rows: map[string][]KeyModelRow{
		"key_1": {},
	}}}
	mux, _ := newTestRoutes(deps)
	recorder := doJSON(mux, "GET", "/__aisys__/api/playground/key-models?apiKeyId=key_1", nil)
	body := strings.TrimSpace(recorder.Body.String())
	if !strings.HasPrefix(body, `{"data":{"models":[`) {
		t.Fatalf("空列表信封不符: %s", body)
	}
}

// TestIPForbiddenTable SSRF 拒绝面表：私网/环回/链路本地/组播/CGNAT/保留段
// 全部拒绝，公网 IP（v4/v6）放行；IPv4-mapped IPv6 还原后判定。
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
		{"100.64.0.1", true},
		{"100.100.100.200", true},
		{"198.18.0.1", true},
		{"192.0.0.1", true},
		{"240.0.0.1", true},
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
		{"http://100.100.100.200/", true, "内网"},
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

// TestKeyModelProvidedByAccounts 提供账户复核表（§3.2）：能力域覆盖 kind、
// 域归一空值落 chat、清单空 = 不限制、启用映射按上游目标对清单、禁用映射与
// 域不符不产候选。
func TestKeyModelProvidedByAccounts(t *testing.T) {
	accounts := []KeyCandidateAccount{
		{CapabilityDomain: "", SupportedModels: []string{"m-direct"}},             // 空 → chat 归一 + 直配
		{CapabilityDomain: "embedding", SupportedModels: []string{"other-embed"}}, // embedding 域
		{CapabilityDomain: "Video", SupportedModels: []string{"m-video"}},         // 大小写归一
		{CapabilityDomain: "chat", SupportedModels: []string{"up-target"}, ModelMappings: []KeyAccountModelMapping{
			{SourceModel: "m-src", UpstreamModel: "up-target", Enabled: true},
			{SourceModel: "m-off", UpstreamModel: "up-target", Enabled: false},
		}},
	}
	cases := []struct {
		name  string
		kind  ModelKind
		model string
		want  bool
	}{
		{"空域归一 chat 直配", KindChat, "m-direct", true},
		{"空域归一 chat 清单不含", KindChat, "m-missing", false},
		{"embedding 域不覆盖 chat kind", KindChat, "other-embed", false},
		{"embedding 域直配", KindEmbedding, "other-embed", true},
		{"embedding 域清单不含该模型", KindEmbedding, "not-listed", false},
		{"video 域大小写归一", KindVideo, "m-video", true},
		{"启用映射源模型按上游目标命中", KindChat, "m-src", true},
		{"禁用映射不产候选", KindChat, "m-off", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := KeyModelProvidedByAccounts(testCase.kind, testCase.model, accounts); got != testCase.want {
				t.Fatalf("KeyModelProvidedByAccounts(%s, %s) = %v，期望 %v", testCase.kind, testCase.model, got, testCase.want)
			}
		})
	}
	// 清单空 = 不限制（单独集合，避免吞掉上方负例）。
	if !KeyModelProvidedByAccounts(KindChat, "any-model", []KeyCandidateAccount{{CapabilityDomain: "chat"}}) {
		t.Fatal("chat 域账户清单空应放行任意模型")
	}
	// 无任何提供账户不产候选。
	if KeyModelProvidedByAccounts(KindRerank, "m-rerank", nil) {
		t.Fatal("无提供账户应不产候选")
	}
}

// TestSelectKeyRowProvider (modelId, kind) 单行消歧表：scope rank 高者优先
// （personal > builtin > global）、同 rank providerCode 归一字典序升序、空表
// 返回 false。
func TestSelectKeyRowProvider(t *testing.T) {
	cases := []struct {
		name    string
		entries []KeyRowProviderEntry
		want    string
		wantOK  bool
	}{
		{"scope rank personal 压过 global", []KeyRowProviderEntry{{ProviderCode: "zz", Scope: "global"}, {ProviderCode: "aa", Scope: "personal"}}, "aa", true},
		{"scope rank builtin 压过 global", []KeyRowProviderEntry{{ProviderCode: "qwen", Scope: "global"}, {ProviderCode: "glm", Scope: "builtin"}}, "glm", true},
		{"同 rank 字典序小者", []KeyRowProviderEntry{{ProviderCode: "Volcengine", Scope: "global"}, {ProviderCode: "qwen", Scope: "global"}}, "qwen", true},
		{"同 rank 大小写归一后字典序", []KeyRowProviderEntry{{ProviderCode: "QWEN", Scope: "global"}, {ProviderCode: "qwen", Scope: "global"}}, "QWEN", true},
		{"全部空 provider 不产行", []KeyRowProviderEntry{{ProviderCode: "  ", Scope: "personal"}}, "", false},
		{"空表不产行", nil, "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := SelectKeyRowProvider(testCase.entries)
			if ok != testCase.wantOK || got != testCase.want {
				t.Fatalf("SelectKeyRowProvider = %q %v，期望 %q %v", got, ok, testCase.want, testCase.wantOK)
			}
		})
	}
}
