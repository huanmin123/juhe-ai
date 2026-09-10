package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// 传输层与模型能力选择是路由决策的核心：协议选择、账户映射匹配、模型能力
// 交集与请求体装配的契约必须与 Node chat-transport.ts / chat-model-options.ts
// 一致。本文件覆盖 transport.go、generation_parameters.go 的纯函数面。

func testToolW3(name string) *toolDefinition {
	return &toolDefinition{ID: name, Version: "1.0.0", ModelName: name, Description: "测试工具", InputSchema: map[string]any{"type": "object"}, MaxArgumentBytes: 1024, MaxResultBytes: 1024, TimeoutMs: 1000, Environments: []string{"development", "test"}, DuplicatePolicy: "reuse_exact"}
}

// TestChatTransportAccountSupportsProtocolW3 表驱动覆盖账户映射匹配。
func TestChatTransportAccountSupportsProtocolW3(t *testing.T) {
	enabled := true
	disabled := false
	cases := []struct {
		name    string
		account ChatTransportAccount
		model   string
		proto   ChatTransportProtocol
		want    bool
	}{
		{"直连匹配", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5"}}}, "gpt-5", ProtocolChatCompletions, true},
		// 映射不命中时模型直通：仅凭 endpoint mode 也可服务该协议。
		{"endpoint family 不匹配仍直通", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", SourceEndpointFamily: "responses"}}}, "gpt-5", ProtocolChatCompletions, true},
		{"禁用映射被跳过后直通", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &disabled, SourceModel: "gpt-5"}}}, "gpt-5", ProtocolChatCompletions, true},
		{"supportedModels 不含上游模型", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, SupportedModels: []string{"other"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamModel: "up-1"}}}, "gpt-5", ProtocolChatCompletions, false},
		{"supportedModels 含上游模型", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, SupportedModels: []string{"up-1"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamModel: "up-1"}}}, "gpt-5", ProtocolChatCompletions, true},
		{"上游协议改写需要对应模式", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamEndpointFamily: "responses"}}}, "gpt-5", ProtocolChatCompletions, false},
		{"上游协议改写命中", ChatTransportAccount{SupportedEndpointModes: []string{"responses_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamEndpointFamily: "responses"}}}, "gpt-5", ProtocolChatCompletions, true},
		{"messages 桥", ChatTransportAccount{SupportedEndpointModes: []string{"messages_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamEndpointFamily: "messages"}}}, "gpt-5", ProtocolChatCompletions, true},
		{"未知上游协议拒绝", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", UpstreamEndpointFamily: "bogus"}}}, "gpt-5", ProtocolChatCompletions, false},
		{"无 requiredMode 拒绝", ChatTransportAccount{SupportedEndpointModes: []string{"chat_sse"}}, "gpt-5", "bogus", false},
		{"模式缺失拒绝", ChatTransportAccount{SupportedEndpointModes: []string{"responses_sse"}, ModelMappings: []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5"}}}, "gpt-5", ProtocolChatCompletions, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := chatTransportAccountSupportsProtocol(testCase.account, testCase.model, testCase.proto); got != testCase.want {
				t.Fatalf("结果 = %v, 期望 %v", got, testCase.want)
			}
		})
	}
}

// TestTransportPlumbingW3 覆盖协议选择、hosted 工具与预算内容的纯函数契约。
func TestTransportPlumbingW3(t *testing.T) {
	if got := selectChatTransport([]ChatTransportProtocol{ProtocolChatCompletions, ProtocolResponses}, true); got != ProtocolResponses {
		t.Fatalf("preferResponses 应选 responses: %s", got)
	}
	if got := selectChatTransport([]ChatTransportProtocol{ProtocolResponses, ProtocolChatCompletions}, false); got != ProtocolChatCompletions {
		t.Fatalf("无偏好时应选 chat_completions: %s", got)
	}
	if got := selectChatTransport([]ChatTransportProtocol{ProtocolResponses}, true); got != ProtocolResponses {
		t.Fatalf("仅有 responses: %s", got)
	}
	if got := selectChatTransport(nil, true); got != ProtocolChatCompletions {
		t.Fatalf("空列表兜底: %s", got)
	}
	if got := normalizeChatHostedTools([]string{"image_generation", "web_search", "bogus", "web_search"}); !equalStringsW3(got, []string{"web_search", "image_generation"}) {
		t.Fatalf("normalizeChatHostedTools = %v", got)
	}
	if got := mapChatHostedToolsToResponses([]string{"web_search"}); len(got) != 1 || got[0]["type"] != "web_search" {
		t.Fatalf("mapChatHostedToolsToResponses = %v", got)
	}
	if got := resolveChatBudgetContent(ProtocolResponses, "正文", []ChatTransportInputBlock{{Type: "input_text", Text: "a"}, {Type: "input_image", DataURL: "data:"}, {Type: "input_text", Text: "b"}}); got != "a\nb" {
		t.Fatalf("resolveChatBudgetContent = %q", got)
	}
	if got := resolveChatBudgetContent(ProtocolChatCompletions, "正文", nil); got != "正文" {
		t.Fatalf("chat 协议应保留原文: %q", got)
	}
	groups := []string{"g1", "g1", "g2", ""}
	loader := func(groupID, model, endpointFamily string) []ChatTransportAccount {
		if groupID == "g1" {
			return []ChatTransportAccount{{ID: "a1", SupportedEndpointModes: []string{"chat_sse"}, ModelMappings: []ChatTransportModelMapping{{SourceModel: model}}}}
		}
		return []ChatTransportAccount{{ID: "a2", SupportedEndpointModes: []string{"responses_sse"}, ModelMappings: []ChatTransportModelMapping{{SourceModel: model}}}}
	}
	protocols := resolveChatSupportedProtocols(groups, "gpt-5", loader)
	if len(protocols) != 2 || protocols[0] != ProtocolChatCompletions || protocols[1] != ProtocolResponses {
		t.Fatalf("resolveChatSupportedProtocols = %v", protocols)
	}
	responsesParams := transportGenerationParameters(ProtocolResponses, &ChatGenerationParameters{Temperature: floatPtrW3(0.5), MaxOutputTokens: floatPtrW3(100), Seed: floatPtrW3(1)})
	if responsesParams["max_output_tokens"] != float64(100) || responsesParams["temperature"] != 0.5 || hasKeyW3(responsesParams, "seed") || hasKeyW3(responsesParams, "frequency_penalty") {
		t.Fatalf("responses 参数映射不正确: %v", responsesParams)
	}
	if got := transportGenerationParameters(ProtocolChatCompletions, &ChatGenerationParameters{FrequencyPenalty: floatPtrW3(0.1), PresencePenalty: floatPtrW3(-0.1), MaxOutputTokens: floatPtrW3(50), Seed: floatPtrW3(9), TopP: floatPtrW3(0.9)}); got["max_completion_tokens"] != float64(50) || hasKeyW3(got, "max_output_tokens") {
		t.Fatalf("chat 参数映射不正确: %v", got)
	}
	if got := transportGenerationParameters(ProtocolResponses, nil); len(got) != 0 {
		t.Fatalf("nil 参数应返回空 map: %v", got)
	}
	if got := compileChatInternalTools(ProtocolResponses, []*toolDefinition{testToolW3("t")}); got[0]["name"] != "t" || hasKeyW3(got[0], "function") {
		t.Fatalf("responses 工具编译不正确: %v", got)
	}
	if got := compileChatInternalTools(ProtocolChatCompletions, []*toolDefinition{testToolW3("t")}); got[0]["function"] == nil {
		t.Fatalf("chat 工具编译不正确: %v", got)
	}
	continuation := buildChatToolContinuation(ProtocolResponses, []any{"keep"}, []ChatToolExecutionOutput{{CallID: "c1", ModelOutput: "out"}})
	if continuation[0] != "keep" || continuation[1].(map[string]any)["call_id"] != "c1" {
		t.Fatalf("responses 续答装配不正确: %v", continuation)
	}
	chatContinuation := buildChatToolContinuation(ProtocolChatCompletions, nil, []ChatToolExecutionOutput{{CallID: "c1", ModelOutput: "out"}})
	if chatContinuation[0].(map[string]any)["role"] != "tool" {
		t.Fatalf("chat 续答装配不正确: %v", chatContinuation)
	}
	if got := toResponsesMessageContent(ChatTransportMessage{Role: "assistant", Content: "文本"}); got != "文本" {
		t.Fatalf("assistant 文本应原样: %v", got)
	}
	if got := toResponsesMessageContent(ChatTransportMessage{Role: "user", Content: "文本"}); got == nil {
		t.Fatalf("user 文本应转为块")
	}
	if got := toResponsesMessageContent(ChatTransportMessage{Role: "user", Content: []ChatTransportInputBlock{{Type: "input_image", DataURL: "data:x"}}}); got == nil {
		t.Fatalf("块内容应转换")
	}
	if got := toResponsesMessageContent(ChatTransportMessage{Role: "user", Content: 42}); got != 42 {
		t.Fatalf("其他类型应原样: %v", got)
	}
}

func floatPtrW3(value float64) *float64 { return &value }

func equalStringsW3(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func hasKeyW3(value map[string]any, key string) bool {
	_, ok := value[key]
	return ok
}

// TestBuildChatTransportRequestW3 覆盖两种协议的请求体装配。
func TestBuildChatTransportRequestW3(t *testing.T) {
	path, body := buildChatTransportRequest(ChatTransportRequestInput{
		Protocol: ProtocolResponses, Instructions: "sys", Model: "gpt-5",
		History:        []ChatTransportMessage{{Role: "user", Content: "历史"}, {Role: "user", Content: []ChatTransportInputBlock{{Type: "input_text", Text: "块历史"}}}},
		CurrentContent: "问题", CurrentBlocks: []ChatTransportInputBlock{{Type: "input_image", DataURL: "data:img"}},
		EffectiveTools: []string{"web_search"}, InternalTools: []*toolDefinition{testToolW3("diagnostic_echo")},
		ReasoningEffort: "low", ServiceTier: "priority",
		GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW3(0.3)}, PromptCacheKey: "cache-1",
	})
	if path != "/v1/responses" {
		t.Fatalf("path = %s", path)
	}
	if body["instructions"] != "sys" || body["reasoning"] == nil || body["service_tier"] != "priority" || body["temperature"] != 0.3 || body["prompt_cache_key"] != "cache-1" {
		t.Fatalf("responses 请求体字段不正确: %v", body)
	}
	tools := body["tools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["type"] != "web_search" {
		t.Fatalf("responses 工具装配不正确: %v", tools)
	}
	if body["parallel_tool_calls"] != false || body["tool_choice"] != "auto" {
		t.Fatalf("工具控制字段不正确: %v", body)
	}
	input := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input 长度 = %d, 期望 3（历史 2 + 当前 1）", len(input))
	}
	chatPath, chatBody := buildChatTransportRequest(ChatTransportRequestInput{
		Protocol: ProtocolChatCompletions, Instructions: "sys", Model: "gpt-5",
		History: []ChatTransportMessage{{Role: "user", Content: "历史"}}, CurrentContent: "问题",
		InternalTools: []*toolDefinition{testToolW3("diagnostic_echo")}, ReasoningEffort: "high", PromptCacheKey: "cache-2",
	})
	if chatPath != "/v1/chat/completions" {
		t.Fatalf("chat path = %s", chatPath)
	}
	messages := chatBody["messages"].([]any)
	if len(messages) != 3 || messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("chat 消息装配不正确: %v", messages)
	}
	if chatBody["reasoning_effort"] != "high" || chatBody["stream_options"] == nil {
		t.Fatalf("chat 请求体字段不正确: %v", chatBody)
	}
	if chatBody["tools"] == nil || chatBody["parallel_tool_calls"] != false {
		t.Fatalf("chat 工具装配不正确: %v", chatBody)
	}
	// 空 CurrentBlocks 时以纯文本块兜底。
	_, fallbackBody := buildChatTransportRequest(ChatTransportRequestInput{Protocol: ProtocolResponses, Model: "gpt-5", CurrentContent: "问题"})
	inputBlocks := fallbackBody["input"].([]any)
	if len(inputBlocks) != 1 {
		t.Fatalf("兜底输入块数量 = %d", len(inputBlocks))
	}
}

// TestBuildChatModelOptionsW3 覆盖模型能力交集装配。
func TestBuildChatModelOptionsW3(t *testing.T) {
	yes := true
	catalog := []ProviderModelCatalogItem{
		{Model: "gpt-5", ProviderCode: "openai", SupportsPromptCaching: &yes,
			SupportedReasoningEfforts: []string{"low", "high", "bogus"}, DefaultReasoningEffort: strPtrT("low"),
			SupportedServiceTiers: []string{"priority"}, ContextWindowTokens: int64PtrT(100000), MaxOutputTokens: int64PtrT(20000),
			SupportedAPIProtocols: []string{"chat_completions", "responses"}, InputModalities: []string{"text"}, OutputModalities: []string{"text"}, SupportedTools: []string{"function_calling"}},
		{Model: "gpt-5", ProviderCode: "openai", SupportsPromptCaching: &yes,
			SupportedReasoningEfforts: []string{"low"}, DefaultReasoningEffort: strPtrT("low"),
			SupportedServiceTiers: []string{"priority"},
			SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "gpt-5-mini", ProviderCode: "openai",
			SupportedAPIProtocols: []string{"chat_completions", "responses"}},
	}
	options := buildChatModelOptions([]string{"gpt-5", "gpt-5", "", "gpt-5-mini"}, catalog)
	if len(options) != 2 {
		t.Fatalf("options 数量 = %d", len(options))
	}
	first := options[0]
	if !equalStringsW3(first.SupportedReasoningEfforts, []string{"low"}) {
		t.Fatalf("能力交集应只剩 low: %v", first.SupportedReasoningEfforts)
	}
	if !first.SupportsPromptCaching {
		t.Fatalf("全量 true 才支持缓存")
	}
	if !equalStringsW3(first.SupportedAPIProtocols, []string{"chat_completions"}) {
		t.Fatalf("协议交集不正确: %v", first.SupportedAPIProtocols)
	}
	// 任一目录行缺 context window 时整体视为未知。
	if first.ContextWindowTokens != nil {
		t.Fatalf("目录行缺失时 contextWindow 应为未知: %v", first.ContextWindowTokens)
	}
	if first.MaxInputTokens != nil {
		t.Fatalf("目录行缺失时 maxInputTokens 应为未知: %v", first.MaxInputTokens)
	}
	// 单行时取已知最小值并推导 maxInputTokens。
	single := buildChatModelOptions([]string{"gpt-5"}, catalog[:1])[0]
	if single.ContextWindowTokens == nil || *single.ContextWindowTokens != 100000 {
		t.Fatalf("contextWindow 取最小已知值: %v", single.ContextWindowTokens)
	}
	if single.MaxInputTokens == nil || *single.MaxInputTokens != 80000 {
		t.Fatalf("maxInputTokens 应由窗口-输出推导: %v", single.MaxInputTokens)
	}
	if !equalStringsW3(first.SupportedServiceTiers, []string{"default", "priority"}) {
		t.Fatalf("服务等级应补 default: %v", first.SupportedServiceTiers)
	}
	if first.DefaultReasoningEffort != "low" {
		t.Fatalf("默认推理级别 = %q", first.DefaultReasoningEffort)
	}
	if len(options[1].SupportedReasoningEfforts) != 0 || options[1].SupportsPromptCaching {
		t.Fatalf("空目录模型能力应为空: %+v", options[1])
	}
	// minimumKnownCapability：任一未知/非正即整体未知。
	if minimumKnownCapability([]*int64{int64PtrT(5), nil}) != nil || minimumKnownCapability([]*int64{int64PtrT(0)}) != nil {
		t.Fatalf("minimumKnownCapability 契约不正确")
	}
	if got := commonReasoningDefault([]ProviderModelCatalogItem{{DefaultReasoningEffort: strPtrT("bogus")}}, []string{"low"}); got != "" {
		t.Fatalf("非法默认级别应返回空: %q", got)
	}
	if got := sortCatalogModels([]string{"b", "a"}); !equalStringsW3(got, []string{"a", "b"}) {
		t.Fatalf("sortCatalogModels = %v", got)
	}
	if got := containsAny([]string{"x", "responses"}, []string{"chat_completions", "responses"}); !got {
		t.Fatalf("containsAny 命中失败")
	}
	if got := intersectStringCapabilityLists([][]string{{"a", "b"}, {"b", "c"}}); !equalStringsW3(got, []string{"b"}) {
		t.Fatalf("交集 = %v", got)
	}
	if got := intersectStringCapabilityLists(nil); len(got) != 0 {
		t.Fatalf("空交集应为空: %v", got)
	}
}

// TestGenerationParameterCapabilitiesW3 覆盖按供应商的参数能力表。
func TestGenerationParameterCapabilitiesW3(t *testing.T) {
	gpt := generationParameterCapabilitiesForModel("GPT", "gpt-5", int64PtrT(8192))
	if _, ok := gpt["responses"]; !ok {
		t.Fatalf("gpt-5 responses 应有参数表")
	}
	nonReasoning := generationParameterCapabilitiesForModel("gpt", "gpt-4.1", nil)
	if len(nonReasoning["chat_completions"]) != 6 {
		t.Fatalf("gpt-4.1 应支持全部 6 个参数: %d", len(nonReasoning["chat_completions"]))
	}
	xai := generationParameterCapabilitiesForModel("xai", "grok-reasoning", nil)
	if len(xai["chat_completions"]) != 4 {
		t.Fatalf("xai reasoning 模型参数数量不正确: %d", len(xai["chat_completions"]))
	}
	deepseek := generationParameterCapabilitiesForModel("deepseek", "deepseek-chat", nil)
	if len(deepseek) != 1 || len(deepseek["chat_completions"]) != 3 {
		t.Fatalf("deepseek-chat 参数表不正确: %v", deepseek)
	}
	anthropic := generationParameterCapabilitiesForModel("anthropic", "claude-opus-4.8", nil)
	if len(anthropic["chat_completions"]) != 1 {
		t.Fatalf("claude 4.8 应只保留 maxOutputTokens: %v", anthropic["chat_completions"])
	}
	gemini := generationParameterCapabilitiesForModel("gemini", "gemini-3-pro", nil)
	if len(gemini["responses"]) != 1 {
		t.Fatalf("gemini-3 responses 应只保留 maxOutputTokens")
	}
	glm := generationParameterCapabilitiesForModel("glm", "glm-5", nil)
	if len(glm) != 1 || len(glm["chat_completions"]) != 3 {
		t.Fatalf("glm 参数表不正确: %v", glm)
	}
	if unknown := generationParameterCapabilitiesForModel("vendor-x", "m", nil); len(unknown) != 0 {
		t.Fatalf("未知供应商应为空: %v", unknown)
	}
	maxTokensEntry, _ := findCapability(gpt["chat_completions"], "maxOutputTokens")
	if maxTokensEntry.Max != 8192 {
		t.Fatalf("maxOutputTokens 应被钳制到 8192: %v", maxTokensEntry)
	}
}

// TestFlattenGenerationParametersW3 覆盖目录行到能力列表的收敛。
func TestFlattenGenerationParametersW3(t *testing.T) {
	items := []ProviderModelCatalogItem{
		{Model: "gpt-5", ProviderCode: "gpt", MaxOutputTokens: int64PtrT(4096), SupportedAPIProtocols: []string{"chat_completions", "responses"}},
	}
	flat := flattenGenerationParameters(items)
	if len(flat) == 0 {
		t.Fatalf("flatten 结果不应为空")
	}
	for _, capability := range flat {
		if capability.Max > 4096 && capability.Parameter == "maxOutputTokens" {
			t.Fatalf("maxOutputTokens 应被目录行钳制: %+v", capability)
		}
	}
	// 显式能力表优先。
	explicit := flattenGenerationParameters([]ProviderModelCatalogItem{{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"chat_completions"},
		GenerationParameterCapabilities: map[string][]ChatGenerationParameterCapability{"chat_completions": {{Parameter: "temperature", Min: 0, Max: 1, DefaultValue: 0.5}}}}})
	if len(explicit) != 1 || explicit[0].Parameter != "temperature" {
		t.Fatalf("显式能力表应优先: %v", explicit)
	}
	// 协议不一致 → 空表。
	mixed := flattenGenerationParameters([]ProviderModelCatalogItem{
		{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"chat_completions"}},
		{Model: "m", ProviderCode: "gpt", SupportedAPIProtocols: []string{"responses"}},
	})
	if len(mixed) != 0 {
		t.Fatalf("协议不一致应收敛为空: %v", mixed)
	}
	if got := intersectGenerationParameterCapabilityLists(nil); len(got) != 0 {
		t.Fatalf("空列表交集应为空")
	}
	emptyList := intersectGenerationParameterCapabilityLists([][]ChatGenerationParameterCapability{{{Parameter: "seed", Min: 0, Max: 10}}, {}})
	if len(emptyList) != 0 {
		t.Fatalf("含空子列表应返回空: %v", emptyList)
	}
	dropped := limitGenerationParameterMaxOutputTokens(map[string][]ChatGenerationParameterCapability{
		"chat_completions": {{Parameter: "maxOutputTokens", Min: 100, Max: 100000, DefaultValue: 4096}},
	}, int64PtrT(50))
	if len(dropped["chat_completions"]) != 0 {
		t.Fatalf("钳制后低于 min 应丢弃: %v", dropped)
	}
}

// TestConstrainChatModelOptionForAccountsW3 覆盖路由级能力收窄。
func TestConstrainChatModelOptionForAccountsW3(t *testing.T) {
	enabled := true
	option := &ChatModelOption{ID: "gpt-5", SupportedAPIProtocols: []string{"chat_completions", "responses"},
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
			{Parameter: "maxOutputTokens", Min: 1, Max: 128000, DefaultValue: 4096},
		}}
	accounts := []ChatTransportAccount{{
		ID: "a1", ProviderCode: "gpt", Type: "oauth",
		SupportedEndpointModes: []string{"responses_sse"},
		ModelMappings:          []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", SourceEndpointFamily: "responses"}},
	}}
	constrained := constrainChatModelOptionForAccounts(option, "gpt-5", accounts, []ChatTransportProtocol{ProtocolResponses})
	if !equalStringsW3(constrained.SupportedAPIProtocols, []string{"responses"}) {
		t.Fatalf("协议应收窄: %v", constrained.SupportedAPIProtocols)
	}
	// gpt oauth 账户拒绝所有参数。
	if len(constrained.GenerationParameters) != 0 {
		t.Fatalf("oauth 账户应收窄掉全部参数: %v", constrained.GenerationParameters)
	}
	plain := []ChatTransportAccount{{
		ID: "a2", ProviderCode: "gpt", Type: "api_key",
		SupportedEndpointModes: []string{"chat_sse"},
		ModelMappings:          []ChatTransportModelMapping{{Enabled: &enabled, SourceModel: "gpt-5", SourceEndpointFamily: "chat_completions"}},
	}}
	kept := constrainChatModelOptionForAccounts(option, "gpt-5", plain, nil)
	// 请求协议为 nil 时按 option 自身协议过滤：responses 无可达账户被剔除。
	if !equalStringsW3(kept.SupportedAPIProtocols, []string{"chat_completions"}) || len(kept.GenerationParameters) != 2 {
		t.Fatalf("普通账户应保留能力: %v %v", kept.SupportedAPIProtocols, kept.GenerationParameters)
	}
}

// TestParseStreamMessageBodyW3 表驱动覆盖流式请求体校验。
func TestParseStreamMessageBodyW3(t *testing.T) {
	valid := map[string]json.RawMessage{
		"clientMessageId": json.RawMessage(`"cmid-1"`), "content": json.RawMessage(`"问题"`), "model": json.RawMessage(`"gpt-5"`),
		"replaceTurnId": json.RawMessage(`" "`), "reasoningEffort": json.RawMessage(`"low"`), "serviceTier": json.RawMessage(`"flex"`),
	}
	body, err := parseStreamMessageBody(valid)
	if err != nil {
		t.Fatalf("合法请求体解析失败: %v", err)
	}
	if body.ClientMessageID != "cmid-1" || body.Model != "gpt-5" || body.ReasoningEffort != "low" || body.ServiceTier != "flex" {
		t.Fatalf("字段解析不正确: %+v", body)
	}
	if body.ReplaceTurnID != "" {
		t.Fatalf("空白 replaceTurnId 应被忽略")
	}
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"clientMessageId 非字符串", `{"clientMessageId":1}`, "Expected string, received number"},
		{"clientMessageId 空", `{"clientMessageId":" "}`, "String must contain at least 1 character(s)"},
		{"clientMessageId 超长", `{"clientMessageId":"` + strings.Repeat("a", 101) + `"}`, "String must contain at most 100 character(s)"},
		{"content 缺失", `{"clientMessageId":"a","model":"m"}`, "请输入消息"},
		{"content 过长", `{"clientMessageId":"a","content":"` + strings.Repeat("字", 196609) + `","model":"m"}`, "消息内容过长"},
		{"model 缺失", `{"clientMessageId":"a","content":"c"}`, "请选择模型"},
		{"非法思考级别", `{"clientMessageId":"a","content":"c","model":"m","reasoningEffort":"bogus"}`, "Invalid enum value"},
		{"非法服务等级", `{"clientMessageId":"a","content":"c","model":"m","serviceTier":"bogus"}`, "Invalid enum value"},
		{"contentBlocks 非数组", `{"clientMessageId":"a","content":"c","model":"m","contentBlocks":1}`, "Expected array, received number"},
		{"contentBlocks 超数量", `{"clientMessageId":"a","content":"c","model":"m","contentBlocks":[` + strings.TrimSuffix(strings.Repeat(`{"type":"input_text","text":"x"},`, 12), ",") + `]}`, "at most 11 element(s)"},
		{"未知块类型", `{"contentBlocks":[{"type":"bogus"}]}`, "Invalid input"},
		{"文本块缺 text", `{"contentBlocks":[{"type":"input_text"}]}`, "Required"},
		{"文本块 text 非字符串", `{"contentBlocks":[{"type":"input_text","text":1}]}`, "Expected string, received number"},
		{"图片缺 assetId", `{"contentBlocks":[{"type":"input_image"}]}`, "Required"},
		{"图片 assetId 空", `{"contentBlocks":[{"type":"input_image","assetId":" "}]}`, "图片资产 ID 不能为空"},
		{"图片 assetId 超长", `{"contentBlocks":[{"type":"input_image","assetId":"` + strings.Repeat("a", 121) + `"}]}`, "at most 120 character(s)"},
		{"未知块字段", `{"contentBlocks":[{"type":"input_text","text":"x","extra":1}]}`, `Unrecognized key: "extra"`},
		{"图片超 5 张", `{"contentBlocks":[{"type":"input_image","assetId":"a1"},{"type":"input_image","assetId":"a2"},{"type":"input_image","assetId":"a3"},{"type":"input_image","assetId":"a4"},{"type":"input_image","assetId":"a5"},{"type":"input_image","assetId":"a6"}]}`, "最多粘贴 5 张图片"},
		{"图片重复", `{"contentBlocks":[{"type":"input_image","assetId":"a1"},{"type":"input_image","assetId":"a1"}]}`, "同一张图片不能重复引用"},
		{"未知顶层键", `{"clientMessageId":"a","content":"c","model":"m","extra":1}`, `Unrecognized key: "extra"`},
		{"generationParameters 非对象", `{"clientMessageId":"a","content":"c","model":"m","generationParameters":1}`, "Expected object, received number"},
		{"参数值非数字", `{"clientMessageId":"a","content":"c","model":"m","generationParameters":{"temperature":"x"}}`, "Expected number, received string"},
		{"参数键未知", `{"clientMessageId":"a","content":"c","model":"m","generationParameters":{"bogus":1}}`, `Unrecognized key: "bogus"`},
		{"seed 非整数", `{"clientMessageId":"a","content":"c","model":"m","generationParameters":{"seed":1.5}}`, "Expected int, received"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(testCase.raw), &raw); err != nil {
				t.Fatalf("测试载荷无效: %v", err)
			}
			_, err := parseStreamMessageBody(raw)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, 期望包含 %q", err, testCase.wantErr)
			}
			var request *invalidRequestError
			if !errorsAs(err, &request) {
				t.Fatalf("应返回 invalidRequestError")
			}
		})
	}
	t.Run("合法图片与参数", func(t *testing.T) {
		var raw map[string]json.RawMessage
		payload := `{"clientMessageId":"a","content":"c","model":"m","contentBlocks":[{"type":"input_text","text":"看"},{"type":"input_image","assetId":"asset-1"}],"generationParameters":{"temperature":0.7,"seed":42,"maxOutputTokens":1024}}`
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			t.Fatalf("载荷无效: %v", err)
		}
		body, err := parseStreamMessageBody(raw)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if len(body.ContentBlocks) != 2 || body.GenerationParameters == nil || body.GenerationParameters.Seed == nil {
			t.Fatalf("解析结果不正确: %+v", body)
		}
	})
}

// TestResolveChatModelRequestOptionsW3 覆盖思考级别/服务等级/生成参数校验。
func TestResolveChatModelRequestOptionsW3(t *testing.T) {
	rt := &chatRoutes{deps: &Deps{}}
	option := &ChatModelOption{
		SupportedReasoningEfforts: []string{"low"}, SupportedServiceTiers: []string{"priority"},
		GenerationParameters: []ChatGenerationParameterCapability{
			{Parameter: "temperature", Min: 0, Max: 2, DefaultValue: 1},
			{Parameter: "seed", Min: 0, Max: 100, DefaultValue: 0},
		},
		MaxInputTokens: int64PtrT(4000),
	}
	body := &streamMessageBody{ReasoningEffort: "low", ServiceTier: "priority", GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW3(0.5)}}
	effort, tier, params, maxInput, err := resolveChatModelRequestOptions(rt, option, body)
	if err != nil || effort != "low" || tier != "priority" || maxInput == nil || *maxInput != 4000 {
		t.Fatalf("合法组合失败: %v %+v", err, params)
	}
	if params.Temperature == nil || *params.Temperature != 0.5 {
		t.Fatalf("生成参数应透传: %+v", params)
	}
	notSupported := &streamMessageBody{ReasoningEffort: "high"}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, notSupported); err == nil {
		t.Fatalf("不支持的思考级别应报错")
	}
	tierMismatch := &streamMessageBody{ServiceTier: "flex"}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, tierMismatch); err == nil {
		t.Fatalf("不支持的服务等级应报错")
	}
	both := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW3(1), TopP: floatPtrW3(1)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, both); err == nil {
		t.Fatalf("temperature 与 topP 互斥")
	}
	outOfRange := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Temperature: floatPtrW3(5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, outOfRange); err == nil {
		t.Fatalf("超范围参数应报错")
	}
	notAllowed := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{FrequencyPenalty: floatPtrW3(1)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, notAllowed); err == nil {
		t.Fatalf("未声明参数应报错")
	}
	fractionalSeed := &streamMessageBody{GenerationParameters: &ChatGenerationParameters{Seed: floatPtrW3(1.5)}}
	if _, _, _, _, err := resolveChatModelRequestOptions(rt, option, fractionalSeed); err == nil {
		t.Fatalf("非整数 seed 应报错")
	}
}

// TestSystemInstructionsAndCacheKeyW3 覆盖系统指令装配与缓存键派生。
func TestSystemInstructionsAndCacheKeyW3(t *testing.T) {
	version, text, hash := buildChatSystemInstructions([]string{"web_search"}, []string{"generate_image"})
	if version != "chat-system-v4" || !strings.Contains(text, "图片生成工具") || !strings.Contains(text, "避免重复调用") || len(hash) != 64 {
		t.Fatalf("系统指令装配不正确: %s %d", version, len(hash))
	}
	_, plain, _ := buildChatSystemInstructions(nil, nil)
	if strings.Contains(plain, "图片生成工具") || strings.Contains(plain, "避免重复调用") {
		t.Fatalf("无工具时不应包含工具段落")
	}
	key1 := buildChatPromptCacheKey("owner", "key", "conv")
	key2 := buildChatPromptCacheKey("owner", "key", "conv2")
	if key1 == "" || key1 == key2 || strings.ContainsAny(key1, "+/=") {
		t.Fatalf("prompt cache key 派生不正确")
	}
}
