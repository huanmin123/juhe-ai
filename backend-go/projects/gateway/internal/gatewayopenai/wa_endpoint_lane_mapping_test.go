package gatewayopenai

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 路径识别契约（对齐 Node openAIEndpointFamilyFromPath：小写包含匹配）。
func TestWAEndpointFamilyFromPath(t *testing.T) {
	cases := []struct {
		path   string
		family string
		ok     bool
	}{
		{"/v1/chat/completions", FamilyChatCompletions, true},
		{"/CHAT/COMPLETIONS", FamilyChatCompletions, true},
		{"/v1/responses", FamilyResponses, true},
		{"/v1/models/responses/list", FamilyResponses, true}, // 包含匹配
		{"", "", false},
		{"/v1/embeddings", "", false},
	}
	for _, tc := range cases {
		family, ok := endpointFamilyFromPath(tc.path)
		if ok != tc.ok || family != tc.family {
			t.Fatalf("endpointFamilyFromPath(%q) = %q/%v，期望 %q/%v", tc.path, family, ok, tc.family, tc.ok)
		}
	}
}

func TestWAResponseEndpointFamilyFromPath(t *testing.T) {
	cases := []struct {
		path   string
		family gatewayproto.ResponseEndpointFamily
	}{
		{"/v1/chat/completions", gatewayproto.EndpointFamilyChatCompletions},
		{"/v1/responses", gatewayproto.EndpointFamilyResponses},
		{"/v1/models", gatewayproto.EndpointFamilyUnknown},
	}
	for _, tc := range cases {
		if got := responseEndpointFamilyFromPath(tc.path); got != tc.family {
			t.Fatalf("responseEndpointFamilyFromPath(%q) = %q，期望 %q", tc.path, got, tc.family)
		}
	}
}

func TestWAIsProtocolRequestPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/responses", true},
		{"/v1/models", true},
		// 模型子路径不在协议面（仅精确 /models；现行行为）。
		{"/v1/models/gpt-x", false},
		{"/v1/images/generations", true},
		{"/v1/images", true},
		{"/v1/embeddings", true},
		{"/v1/audio/transcriptions", true},
		{"/v1/audio", true},
		{"/v1/other", false},
		{"/messages", false},
	}
	for _, tc := range cases {
		if got := IsProtocolRequestPath(tc.path); got != tc.want {
			t.Fatalf("IsProtocolRequestPath(%q) = %v，期望 %v", tc.path, got, tc.want)
		}
	}
	// query 不参与 models 判定。
	if !IsProtocolRequestPath("/v1/models?limit=5") {
		t.Fatal("带 query 的 models 应命中")
	}
}

func TestWAIsModelsRequestOpenAI(t *testing.T) {
	if !IsModelsRequest("GET", "/v1/models?limit=5") {
		t.Fatal("GET models 应命中")
	}
	if IsModelsRequest("POST", "/v1/models") {
		t.Fatal("POST 不应命中")
	}
	if IsModelsRequest("GET", "/v1/models/gpt-x") {
		t.Fatal("子路径不应命中")
	}
}

func TestWASplitPathAndQuery(t *testing.T) {
	path, query := SplitPathAndQuery("/v1/models?limit=5")
	if path != "/v1/models" || query != "?limit=5" {
		t.Fatalf("SplitPathAndQuery = %q/%q", path, query)
	}
	path, query = SplitPathAndQuery("/v1/models")
	if path != "/v1/models" || query != "" {
		t.Fatalf("无 query = %q/%q", path, query)
	}
}

func TestWANormalizedPathWithoutVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/v1/chat", "/chat"},
		{"/chat", "/chat"},
		{"chat", "/chat"},
		{"/v1", "/"},
		{"/v1beta/x", "/v1beta/x"},
	}
	for _, tc := range cases {
		if got := normalizedPathWithoutVersion(tc.in); got != tc.want {
			t.Fatalf("normalizedPathWithoutVersion(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestWAEndpointModeForShape(t *testing.T) {
	cases := []struct {
		path      string
		stream    bool
		want      gatewayproto.EndpointMode
		wantFound bool
	}{
		{"/v1/chat/completions", false, gatewayproto.EndpointModeChatJSON, true},
		{"/v1/chat/completions", true, gatewayproto.EndpointModeChatSSE, true},
		{"/v1/responses", false, gatewayproto.EndpointModeResponsesJSON, true},
		{"/v1/responses", true, gatewayproto.EndpointModeResponsesSSE, true},
		{"/v1/embeddings", false, "", false},
	}
	for _, tc := range cases {
		got, found := endpointModeForShape(tc.path, tc.stream)
		if found != tc.wantFound || got != tc.want {
			t.Fatalf("endpointModeForShape(%q,%v) = %q/%v，期望 %q/%v", tc.path, tc.stream, got, found, tc.want, tc.wantFound)
		}
	}
}

// 图片通道判定：路径 / 模型名 / 工具描述 / 输出模态四条路径。
func TestWAResolveRequestLane(t *testing.T) {
	t.Run("图片端点", func(t *testing.T) {
		if got := ResolveRequestLane("/v1/images/generations", nil, ""); got != gatewayproto.LaneImage {
			t.Fatalf("图片端点 = %q", got)
		}
	})
	t.Run("图片模型", func(t *testing.T) {
		if got := ResolveRequestLane("/v1/chat/completions", nil, "gpt-image-1"); got != gatewayproto.LaneImage {
			t.Fatalf("gpt-image = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, "dall-e-3"); got != gatewayproto.LaneImage {
			t.Fatalf("dall-e = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, "imagen-4"); got != gatewayproto.LaneImage {
			t.Fatalf("imagen = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, "nano-banana-pro"); got != gatewayproto.LaneImage {
			t.Fatalf("nano-banana = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, "gemini-2.5-flash-image"); got != gatewayproto.LaneImage {
			t.Fatalf("gemini image = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, "gemini-2.5-flash"); got != gatewayproto.LaneText {
			t.Fatalf("普通 gemini = %q", got)
		}
	})
	t.Run("工具提示", func(t *testing.T) {
		body := mustParseJSON(t, `{"tools":[{"type":"image_generation"}]}`)
		if got := ResolveRequestLane("/v1/responses", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("image_generation 工具 = %q", got)
		}
		body = mustParseJSON(t, `{"tools":[{"type":"web_search"}]}`)
		if got := ResolveRequestLane("/v1/responses", body, "gpt-x"); got != gatewayproto.LaneText {
			t.Fatalf("非图片工具 = %q", got)
		}
		// tool_choice 显式指定图片工具。
		body = mustParseJSON(t, `{"tool_choice":{"type":"image_generation"}}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("tool_choice 图片 = %q", got)
		}
		// tool_choice=required 且只有图片工具。
		body = mustParseJSON(t, `{"tool_choice":"required","tools":[{"type":"image_generation"}]}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("required 图片 = %q", got)
		}
		// 字符串工具名。
		body = mustParseJSON(t, `{"tools":["image_generation"]}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("字符串工具 = %q", got)
		}
	})
	t.Run("输出模态", func(t *testing.T) {
		body := mustParseJSON(t, `{"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("responseModalities image = %q", got)
		}
		body = mustParseJSON(t, `{"generation_config":{"response_mime_type":"image/png"}}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneImage {
			t.Fatalf("response_mime_type image = %q", got)
		}
		body = mustParseJSON(t, `{"generationConfig":{"responseModalities":["TEXT"]}}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneText {
			t.Fatalf("纯文本模态 = %q", got)
		}
	})
	t.Run("普通文本", func(t *testing.T) {
		body := mustParseJSON(t, `{"messages":[{"role":"user","content":"hi"}]}`)
		if got := ResolveRequestLane("/v1/chat/completions", body, "gpt-x"); got != gatewayproto.LaneText {
			t.Fatalf("普通请求 = %q", got)
		}
		if got := ResolveRequestLane("/v1/chat/completions", nil, ""); got != gatewayproto.LaneText {
			t.Fatalf("空请求 = %q", got)
		}
	})
	t.Run("inspectImageGenerationTools 深度与计数", func(t *testing.T) {
		// 超过 4 层深度的工具定义不再累计。
		deep := map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{"e": map[string]any{"type": "image_generation"}}}}}}
		inspection := inspectImageGenerationTools(deep)
		if inspection.imageToolCount != 0 || inspection.forcedImageGeneration {
			t.Fatalf("超深工具不应累计: %+v", inspection)
		}
		// 混合工具：image 工具存在即视为图片提示（forced 仅在只有图片工具
		// 且 tool_choice=required 等场景）。
		mixed := map[string]any{"tools": []any{
			map[string]any{"type": "image_generation"},
			map[string]any{"type": "web_search"},
		}}
		inspection = inspectImageGenerationTools(mixed)
		if inspection.imageToolCount != 1 || inspection.nonImageToolCount != 1 || inspection.forcedImageGeneration {
			t.Fatalf("混合工具计数 = %+v", inspection)
		}
		if !requestBodyHasImageGenerationHint(mixed) {
			t.Fatal("含 image_generation 工具应触发图片通道")
		}
	})
}

// 账户模型映射解析契约。
func TestWAResolveAccountModelMapping(t *testing.T) {
	waMapping := func(source, upstream string) AccountModelMapping {
		return AccountModelMapping{SourceModel: "m", SourceEndpointFamily: source, UpstreamModel: "m-up", UpstreamEndpointFamily: upstream}
	}
	hybrid := &RuntimeAccount{ProviderCode: "hybrid", ProtocolCode: "openai", ProtocolVersion: "v1", ModelMappings: []AccountModelMapping{waMapping(FamilyChatCompletions, FamilyAnthropicMessages)}}

	t.Run("混合账户 chat 到 messages", func(t *testing.T) {
		resolved := ResolveAccountModelMapping(hybrid, " m ", FamilyChatCompletions)
		if resolved == nil {
			t.Fatal("应解析出映射")
		}
		if resolved.UpstreamModel != "m-up" || resolved.UpstreamEndpointFamily != FamilyAnthropicMessages {
			t.Fatalf("resolved = %+v", resolved)
		}
	})
	t.Run("空模型或空来源族", func(t *testing.T) {
		if ResolveAccountModelMapping(hybrid, "  ", FamilyChatCompletions) != nil {
			t.Fatal("空模型不解析")
		}
		if ResolveAccountModelMapping(hybrid, "m", "") != nil {
			t.Fatal("空来源族不解析")
		}
	})
	t.Run("来源族白名单", func(t *testing.T) {
		if ResolveAccountModelMapping(hybrid, "m", "unknown_family") != nil {
			t.Fatal("未知来源族不解析")
		}
		if ResolveAccountModelMapping(hybrid, "m", FamilyGeminiStreamGenerate) != nil {
			t.Fatal("无匹配行不解析")
		}
	})
	t.Run("gemini-openai-chat 档位拒绝 anthropic 来源", func(t *testing.T) {
		account := &RuntimeAccount{
			ProviderProtocolProfileID: GeminiOpenAIChatProfileID,
			ProviderCode:              "hybrid",
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			ModelMappings:             []AccountModelMapping{waMapping(FamilyAnthropicMessages, FamilyChatCompletions)},
		}
		if resolved := ResolveAccountModelMapping(account, "m", FamilyAnthropicMessages); resolved != nil {
			t.Fatalf("gemini-openai-chat 档位应拒绝 anthropic 来源: %+v", resolved)
		}
	})
	t.Run("禁用与同构映射", func(t *testing.T) {
		disabled := false
		account := &RuntimeAccount{ProviderCode: "hybrid", ModelMappings: []AccountModelMapping{
			{SourceModel: "m", SourceEndpointFamily: FamilyChatCompletions, UpstreamModel: "m", UpstreamEndpointFamily: FamilyChatCompletions},
			{SourceModel: "m", SourceEndpointFamily: FamilyResponses, UpstreamModel: "m2", UpstreamEndpointFamily: FamilyChatCompletions, Enabled: &disabled},
		}}
		account.ProtocolCode = "openai"
		account.ProtocolVersion = "v1"
		if resolved := ResolveAccountModelMapping(account, "m", FamilyChatCompletions); resolved != nil {
			t.Fatalf("同构映射不解析: %+v", resolved)
		}
		if resolved := ResolveAccountModelMapping(account, "m", FamilyResponses); resolved != nil {
			t.Fatalf("禁用映射不解析: %+v", resolved)
		}
	})
	t.Run("openai 账户 responses 到 chat", func(t *testing.T) {
		account := &RuntimeAccount{ProtocolCode: "openai", ProtocolVersion: "v1", ModelMappings: []AccountModelMapping{
			waMapping(FamilyResponses, FamilyChatCompletions),
		}}
		resolved := ResolveAccountModelMapping(account, "m", FamilyResponses)
		if resolved == nil || resolved.UpstreamModel != "m-up" {
			t.Fatalf("openai responses 到 chat 应解析: %+v", resolved)
		}
		// 同一行在非 openai 协议档位且非混合账户下不受支持。
		other := &RuntimeAccount{ProviderCode: "vendor", ProtocolCode: "anthropic", ProtocolVersion: "v1", ModelMappings: account.ModelMappings}
		if ResolveAccountModelMapping(other, "m", FamilyResponses) != nil {
			t.Fatal("非 openai/混合账户不支持该转换")
		}
	})
	t.Run("nil 账户", func(t *testing.T) {
		if ResolveAccountModelMapping(nil, "m", FamilyChatCompletions) != nil {
			t.Fatal("nil 账户无映射可解析")
		}
	})
	t.Run("转换支持矩阵", func(t *testing.T) {
		hybridAccount := &RuntimeAccount{ProviderCode: "hybrid"}
		sameFamily := &AccountModelMapping{SourceEndpointFamily: FamilyChatCompletions, UpstreamEndpointFamily: FamilyChatCompletions}
		if !isOpenAIModelMappingRuntimeConversionSupported(sameFamily, nil) {
			t.Fatal("同族总是支持")
		}
		geminiFlatten := &AccountModelMapping{SourceEndpointFamily: FamilyGeminiStreamGenerate, UpstreamEndpointFamily: FamilyGeminiGenerateContent}
		if !isOpenAIModelMappingRuntimeConversionSupported(geminiFlatten, nil) {
			t.Fatal("gemini stream 到非 stream 支持")
		}
		cross := &AccountModelMapping{SourceEndpointFamily: FamilyGeminiGenerateContent, UpstreamEndpointFamily: FamilyAnthropicMessages}
		if isOpenAIModelMappingRuntimeConversionSupported(cross, &RuntimeAccount{ProviderCode: "vendor"}) {
			t.Fatal("非混合账户不支持跨协议")
		}
		if !isOpenAIModelMappingRuntimeConversionSupported(cross, hybridAccount) {
			t.Fatal("混合账户支持 gemini 到 messages")
		}
		// 其余跨协议组合。
		pairs := [][2]string{
			{FamilyResponses, FamilyAnthropicMessages},
			{FamilyChatCompletions, FamilyGeminiGenerateContent},
			{FamilyResponses, FamilyGeminiGenerateContent},
			{FamilyAnthropicMessages, FamilyGeminiGenerateContent},
		}
		for _, pair := range pairs {
			mapping := &AccountModelMapping{SourceEndpointFamily: pair[0], UpstreamEndpointFamily: pair[1]}
			if !isOpenAIModelMappingRuntimeConversionSupported(mapping, hybridAccount) {
				t.Fatalf("混合账户应支持 %v 到 %v", pair[0], pair[1])
			}
		}
	})
}

func TestWAMappingPredicates(t *testing.T) {
	resolved := func(source, upstream string) *gatewayproto.ResolvedModelMapping {
		return &gatewayproto.ResolvedModelMapping{SourceEndpointFamily: source, UpstreamEndpointFamily: upstream}
	}
	if !isOpenAIResponsesToChatCompletionsModelMapping(resolved(FamilyResponses, FamilyChatCompletions)) {
		t.Fatal("responses 到 chat 判定错误")
	}
	if !isAnthropicMessagesToChatCompletionsModelMapping(resolved(FamilyAnthropicMessages, FamilyChatCompletions)) {
		t.Fatal("anthropic 到 chat 判定错误")
	}
	if !isGeminiGenerateContentToChatCompletionsModelMapping(resolved(FamilyGeminiStreamGenerate, FamilyChatCompletions)) {
		t.Fatal("gemini stream 到 chat 判定错误")
	}
	if isGeminiGenerateContentToChatCompletionsModelMapping(nil) {
		t.Fatal("nil 映射应 false")
	}
	if !isCrossProtocolBridgeToAnthropicMessagesModelMapping(resolved(FamilyResponses, "messages")) {
		t.Fatal("存储词表 messages 应识别")
	}
	if NormalizeAnthropicFamily("messages") != true || NormalizeAnthropicFamily("anthropic_messages") != true || NormalizeAnthropicFamily("chat_completions") {
		t.Fatal("NormalizeAnthropicFamily 语义错误")
	}
}

func TestWAGeminiGenerateContentBridgeQuery(t *testing.T) {
	if got := geminiGenerateContentBridgeQuery(""); got != "" {
		t.Fatalf("空 query = %q", got)
	}
	if got := geminiGenerateContentBridgeQuery("?alt=sse&key=secret&x=1"); got != "?x=1" {
		t.Fatalf("删 alt/key = %q", got)
	}
	if got := geminiGenerateContentBridgeQuery("?%zz=1"); got != "" {
		t.Fatalf("非法 query = %q", got)
	}
	if got := geminiGenerateContentBridgeQuery("?alt=sse"); got != "" {
		t.Fatalf("全删除后 = %q", got)
	}
}
