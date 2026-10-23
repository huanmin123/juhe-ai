package accounts

// w13a test_options_service.go 纯函数补臂：端点能力运行时归一（OpenAI/
// Anthropic/Gemini）、测试端点顺序、模型映射族与运行时转换支持、
// 查询参数归一与上限、凭据端点能力投影、去重。

import (
	"reflect"
	"testing"
)

func TestW13ATestEndpointModeRuntimeHelpers(t *testing.T) {
	defaults := endpointModeDefaultContext{providerCode: "openai", accountType: "api_key"}
	// OpenAI：未知值丢弃、去重、空集回退默认。
	got := normalizeOpenAIEndpointModesForRuntime([]string{"chat_json", "chat_json", "bogus", "chat_sse"}, defaults)
	if !reflect.DeepEqual(got, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("OpenAI 运行时归一不一致：%v", got)
	}
	if modes := normalizeOpenAIEndpointModesForRuntime([]string{"bogus"}, defaults); len(modes) == 0 {
		t.Fatal("空集应回退默认")
	}
	// Anthropic / Gemini。
	anthropicDefaults := endpointModeDefaultContext{providerCode: "anthropic", accountType: "api_key", protocolCode: "anthropic", protocolVersion: "v1"}
	got = normalizeAnthropicEndpointModesForRuntime([]string{"messages_json", "bogus"}, anthropicDefaults)
	if !reflect.DeepEqual(got, []string{"messages_json"}) {
		t.Fatalf("Anthropic 归一不一致：%v", got)
	}
	got = normalizeAnthropicEndpointModesForRuntime(nil, anthropicDefaults)
	if len(got) == 0 || got[0] != "messages_json" {
		t.Fatalf("Anthropic 默认应 messages_json：%v", got)
	}
	geminiDefaults := endpointModeDefaultContext{providerCode: "gemini", accountType: "api_key", protocolCode: "gemini", protocolVersion: "v1beta"}
	got = normalizeGeminiEndpointModesForRuntime([]string{"generate_content_json"}, geminiDefaults)
	if !reflect.DeepEqual(got, []string{"generate_content_json"}) {
		t.Fatalf("Gemini 归一不一致：%v", got)
	}
	got = normalizeGeminiEndpointModesForRuntime(nil, geminiDefaults)
	if len(got) == 0 {
		t.Fatal("Gemini 默认不应为空")
	}
	// Hybrid 运行时归一已随 §555 终态扫描拆除（B4④）：legacy hybrid token
	// 不再是供应商维度，协议面分支（anthropic/gemini/openai）之外一律按
	// 未注册上下文处理。
}

func TestW13AAccountManualTestEndpointModes(t *testing.T) {
	// OpenAI api_key：chat 族默认在前，health 模式优先。
	source := manualTestModeSource{
		ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1",
		ProtocolCode: "openai", ProtocolVersion: "v1", AccountType: "api_key",
		HealthCheckEndpointMode: "chat_sse",
	}
	modes := accountManualTestEndpointModes(source)
	if len(modes) == 0 || modes[0] != "chat_sse" {
		t.Fatalf("健康检查模式应排最前：%v", modes)
	}
	// Anthropic 协议只保留 messages 族（流式优先：messages_sse 在前）。
	anthropic := manualTestModeSource{
		ProviderCode: "anthropic", ProviderProtocolProfileID: "profile_anthropic_anthropic_v1",
		ProtocolCode: "anthropic", ProtocolVersion: "v1", AccountType: "api_key",
	}
	if modes = accountManualTestEndpointModes(anthropic); len(modes) == 0 || modes[0] != "messages_sse" {
		t.Fatalf("Anthropic 顺序不一致（流式优先）：%v", modes)
	}
	// Gemini 协议。
	gemini := manualTestModeSource{
		ProviderCode: "gemini", ProviderProtocolProfileID: "profile_gemini_native_v1beta",
		ProtocolCode: "gemini", ProtocolVersion: "v1beta", AccountType: "api_key",
	}
	if modes = accountManualTestEndpointModes(gemini); len(modes) == 0 {
		t.Fatal("Gemini 模式不应为空")
	}
	// legacy hybrid token（无协议面）：与未知供应商同类，终态为空（B4④ 残留
	// 门拆除，hybrid 分支退役）。
	hybrid := manualTestModeSource{
		ProviderCode: "hybrid", AccountType: "api_key",
	}
	if modes = accountManualTestEndpointModes(hybrid); len(modes) != 0 {
		t.Fatalf("hybrid token 未注册上下文应为空：%v", modes)
	}
	// 未知供应商：空。
	unknown := manualTestModeSource{ProviderCode: "bogus", AccountType: "api_key"}
	if modes = accountManualTestEndpointModes(unknown); len(modes) != 0 {
		t.Fatalf("未知供应商应为空：%v", modes)
	}
}

func TestW13ATestMappingHelpers(t *testing.T) {
	if !isTestMappingSourceFamily("stream_generate_content") || isTestMappingSourceFamily("bogus") {
		t.Fatal("映射源族判定不一致")
	}
	openaiSource := manualTestModeSource{
		ProviderCode: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
	}
	// 同族直通、流式转非流式、responses→chat 在 OpenAI 协议支持。
	if !isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "chat_completions"}, openaiSource) {
		t.Fatal("同族应支持")
	}
	if !isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "stream_generate_content", UpstreamEndpointFamily: "generate_content"}, openaiSource) {
		t.Fatal("流式→非流式应支持")
	}
	if !isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "responses", UpstreamEndpointFamily: "chat_completions"}, openaiSource) {
		t.Fatal("responses→chat 在 OpenAI 协议应支持")
	}
	if isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "messages", UpstreamEndpointFamily: "chat_completions"}, openaiSource) {
		t.Fatal("messages→chat 在非 OpenAI 协议面不应支持")
	}
	// legacy hybrid token 的跨协议转换矩阵已随 §555 终态扫描拆除（B4④）：
	// hybrid 来源只剩同族直通与 stream→generate 归一，跨协议一律不支持。
	hybrid := manualTestModeSource{ProviderCode: "hybrid"}
	if !isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "chat_completions"}, hybrid) {
		t.Fatal("hybrid 同族直通应保留")
	}
	if isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "messages", UpstreamEndpointFamily: "chat_completions"}, hybrid) {
		t.Fatal("hybrid messages→chat 跨协议转换应随矩阵拆除退役")
	}
	if isOpenAIModelMappingRuntimeConversionSupported(ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "bogus"}, hybrid) {
		t.Fatal("未知目标不应支持")
	}
}

func TestW13ANormalizeManualTestOptionsQuery(t *testing.T) {
	// 默认 limit 50、keyword 与 selectedIds 归一。
	query, message := NormalizeManualTestOptionsQuery(map[string][]string{
		"keyword": {" 关键词 "}, "selectedIds": {"a,b", "b", " "}, "selectedIds[]": {"c"},
	})
	if message != "" || query.Limit != 50 || query.Keyword != "关键词" ||
		!reflect.DeepEqual(query.SelectedIDs, []string{"a", "b", "c"}) {
		t.Fatalf("查询归一不一致：%+v %q", query, message)
	}
	// limit 非法：非数字、0、51。
	for _, limit := range []string{"abc", "0", "51"} {
		if _, message := NormalizeManualTestOptionsQuery(map[string][]string{"limit": {limit}}); message == "" {
			t.Fatalf("limit %s 应拒绝", limit)
		}
	}
	if query, message = NormalizeManualTestOptionsQuery(map[string][]string{"limit": {"7"}}); message != "" || query.Limit != 7 {
		t.Fatalf("合法 limit 应通过：%+v %q", query, message)
	}
	// selectedIds 上限 50。
	many := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		many = append(many, "id"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	query, _ = NormalizeManualTestOptionsQuery(map[string][]string{"selectedIds": many})
	if len(query.SelectedIDs) != 50 {
		t.Fatalf("selectedIds 应截断到 50：%d", len(query.SelectedIDs))
	}
	// supportedEndpointModesFromCredentials：缺失/nil/非数组/混合。
	if got := supportedEndpointModesFromCredentials(Credentials{}); len(got) != 0 {
		t.Fatalf("缺失应为空：%v", got)
	}
	if got := supportedEndpointModesFromCredentials(Credentials{"supported_endpoint_modes": nil}); len(got) != 0 {
		t.Fatalf("nil 应为空：%v", got)
	}
	if got := supportedEndpointModesFromCredentials(Credentials{"supported_endpoint_modes": "x"}); len(got) != 0 {
		t.Fatalf("非数组应为空：%v", got)
	}
	if got := supportedEndpointModesFromCredentials(Credentials{"supported_endpoint_modes": []any{"chat_json", 3, "chat_sse"}}); !reflect.DeepEqual(got, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("混合数组应过滤：%v", got)
	}
	// dedupeTestStrings：按值去重、保留顺序（空串不剔除）。
	if got := dedupeTestStrings([]string{"a", "b", "a", ""}); !reflect.DeepEqual(got, []string{"a", "b", ""}) {
		t.Fatalf("去重不一致：%v", got)
	}
	if got := firstQueryText([]string{" x "}); got != "x" || firstQueryText(nil) != "" {
		t.Fatal("首值归一不一致")
	}
	if got := normalizedQueryTextList([]string{"a,a", " b "}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("列表归一不一致：%v", got)
	}
}
