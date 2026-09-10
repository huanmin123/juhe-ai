package accounts

// W2 手动测试选项纯函数与 Store 读取单测：endpoint-mode 运行时归一、模型
// 映射解析、目录候选合并、查询参数归一与可用性 gate（test_options_service.go
// 与 test_dispatch_routes.go 的无副作用分支）。HTTP handler 链路另见
// w2_test_dispatch_test.go。

import (
	"context"
	"testing"
	"time"
)

func TestW2NormalizeManualTestOptionsQuery(t *testing.T) {
	cases := []struct {
		name    string
		query   map[string][]string
		want    ManualTestOptionsQuery
		message string
	}{
		{"默认", map[string][]string{}, ManualTestOptionsQuery{Limit: 50}, ""},
		{"keyword 去空白", map[string][]string{"keyword": {"  gpt  "}}, ManualTestOptionsQuery{Keyword: "gpt", Limit: 50}, ""},
		{"limit 下界", map[string][]string{"limit": {"1"}}, ManualTestOptionsQuery{Limit: 1}, ""},
		{"limit 上界", map[string][]string{"limit": {"50"}}, ManualTestOptionsQuery{Limit: 50}, ""},
		{"limit 为零", map[string][]string{"limit": {"0"}}, ManualTestOptionsQuery{}, "limit 必须是 1 到 50 的整数"},
		{"limit 超界", map[string][]string{"limit": {"51"}}, ManualTestOptionsQuery{}, "limit 必须是 1 到 50 的整数"},
		{"limit 非数字", map[string][]string{"limit": {"1a"}}, ManualTestOptionsQuery{}, "limit 必须是 1 到 50 的整数"},
		{"selectedIds 去重与拆分", map[string][]string{"selectedIds": {"a,b", "b", " c "}},
			ManualTestOptionsQuery{Limit: 50, SelectedIDs: []string{"a", "b", "c"}}, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, message := NormalizeManualTestOptionsQuery(testCase.query)
			if message != testCase.message {
				t.Fatalf("消息不一致：%q != %q", message, testCase.message)
			}
			if message != "" {
				return
			}
			if got.Limit != testCase.want.Limit || got.Keyword != testCase.want.Keyword {
				t.Fatalf("查询不一致：%+v != %+v", got, testCase.want)
			}
			if len(got.SelectedIDs) != len(testCase.want.SelectedIDs) {
				t.Fatalf("selectedIds 不一致：%v != %v", got.SelectedIDs, testCase.want.SelectedIDs)
			}
			for index, id := range testCase.want.SelectedIDs {
				if got.SelectedIDs[index] != id {
					t.Fatalf("selectedIds 不一致：%v", got.SelectedIDs)
				}
			}
		})
	}

	// selectedIds 数量上限 50：超出部分直接截断。
	ids := map[string][]string{"selectedIds": {}}
	values := []string{}
	for index := 0; index < 60; index++ {
		values = append(values, string(rune('a'+index%26))+string(rune('0'+index/26)))
	}
	ids["selectedIds"] = values
	got, message := NormalizeManualTestOptionsQuery(ids)
	if message != "" || len(got.SelectedIDs) != 50 {
		t.Fatalf("selectedIds 上限不一致：%d %q", len(got.SelectedIDs), message)
	}
}

func TestW2RuntimeEndpointModeNormalizers(t *testing.T) {
	context := endpointModeDefaultContext{providerCode: "gpt", accountType: "api_key",
		protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}

	t.Run("anthropic 过滤未知并回退默认", func(t *testing.T) {
		got := normalizeAnthropicEndpointModesForRuntime([]string{"messages_json", "bogus", "messages_json"}, context)
		if len(got) != 1 || got[0] != "messages_json" {
			t.Fatalf("归一结果不一致：%v", got)
		}
		fallback := normalizeAnthropicEndpointModesForRuntime([]string{"nope"}, context)
		if len(fallback) == 0 || fallback[0] != "messages_json" {
			t.Fatalf("默认回退不一致：%v", fallback)
		}
	})
	t.Run("gemini 过滤未知并回退默认", func(t *testing.T) {
		got := normalizeGeminiEndpointModesForRuntime([]string{"generate_content_sse", "chat_json"}, context)
		if len(got) != 1 || got[0] != "generate_content_sse" {
			t.Fatalf("归一结果不一致：%v", got)
		}
		fallback := normalizeGeminiEndpointModesForRuntime(nil, context)
		if len(fallback) == 0 {
			t.Fatal("默认回退不应为空")
		}
		for _, mode := range fallback {
			if !isGeminiEndpointMode(mode) {
				t.Fatalf("默认值包含非 gemini 模式：%s", mode)
			}
		}
	})
	t.Run("hybrid 过滤未知并回退全量", func(t *testing.T) {
		got := normalizeHybridEndpointModesForRuntime([]string{"chat_json", "messages_json", "weird", "chat_json"})
		if len(got) != 2 || got[0] != "chat_json" || got[1] != "messages_json" {
			t.Fatalf("归一结果不一致：%v", got)
		}
		fallback := normalizeHybridEndpointModesForRuntime([]string{"weird"})
		if len(fallback) != len(hybridEndpointModeValues) {
			t.Fatalf("全量回退不一致：%v", fallback)
		}
	})
}

func TestW2OpenAIModelMappingRuntimeConversion(t *testing.T) {
	openAI := manualTestModeSource{providerCode: "gpt", protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}
	hybrid := manualTestModeSource{providerCode: hybridProviderCode}

	cases := []struct {
		name      string
		mapping   ModelMapping
		source    manualTestModeSource
		supported bool
	}{
		{"同族恒等", ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "chat_completions"}, openAI, true},
		{"gemini 流式到非流式", ModelMapping{SourceEndpointFamily: "stream_generate_content", UpstreamEndpointFamily: "generate_content"}, openAI, true},
		{"openai responses 到 chat", ModelMapping{SourceEndpointFamily: "responses", UpstreamEndpointFamily: "chat_completions"}, openAI, true},
		{"openai messages 到 chat 不支持", ModelMapping{SourceEndpointFamily: "messages", UpstreamEndpointFamily: "chat_completions"}, openAI, false},
		{"hybrid messages 到 chat", ModelMapping{SourceEndpointFamily: "messages", UpstreamEndpointFamily: "chat_completions"}, hybrid, true},
		{"hybrid chat 到 messages", ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "messages"}, hybrid, true},
		{"hybrid responses 到 messages", ModelMapping{SourceEndpointFamily: "responses", UpstreamEndpointFamily: "messages"}, hybrid, true},
		{"hybrid generate_content 到 chat", ModelMapping{SourceEndpointFamily: "generate_content", UpstreamEndpointFamily: "chat_completions"}, hybrid, true},
		{"hybrid stream 到 messages", ModelMapping{SourceEndpointFamily: "stream_generate_content", UpstreamEndpointFamily: "messages"}, hybrid, true},
		{"hybrid chat 到 generate_content", ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "generate_content"}, hybrid, true},
		{"hybrid responses 到 generate_content", ModelMapping{SourceEndpointFamily: "responses", UpstreamEndpointFamily: "generate_content"}, hybrid, true},
		{"hybrid messages 到 generate_content", ModelMapping{SourceEndpointFamily: "messages", UpstreamEndpointFamily: "generate_content"}, hybrid, true},
		{"hybrid chat 到 chat 变体不支持", ModelMapping{SourceEndpointFamily: "chat_completions", UpstreamEndpointFamily: "responses"}, hybrid, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := isOpenAIModelMappingRuntimeConversionSupported(testCase.mapping, testCase.source)
			if got != testCase.supported {
				t.Fatalf("转换支持判定不一致：%v", got)
			}
		})
	}
}

func TestW2ResolveTestAccountModelMapping(t *testing.T) {
	source := manualTestModeSource{
		providerCode: "gpt", protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion,
		modelMappings: []ModelMapping{
			{SourceModel: "m1", SourceEndpointFamily: "responses", UpstreamModel: "u1", UpstreamEndpointFamily: "chat_completions", Enabled: boolPtr(true)},
			{SourceModel: "m2", SourceEndpointFamily: "chat_completions", UpstreamModel: "u2", UpstreamEndpointFamily: "responses", Enabled: boolPtr(false)},
			{SourceModel: "m3", SourceEndpointFamily: "chat_completions", UpstreamModel: "m3", UpstreamEndpointFamily: "chat_completions"},
			{SourceModel: "m4", SourceEndpointFamily: "chat_completions", UpstreamModel: "u4", UpstreamEndpointFamily: "messages"},
		},
	}
	if got := resolveTestAccountModelMapping(source, "", "responses"); got != nil {
		t.Fatalf("空模型应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "m1", ""); got != nil {
		t.Fatalf("空 family 应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "m1", "unknown_family"); got != nil {
		t.Fatalf("不支持 family 应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "missing", "responses"); got != nil {
		t.Fatalf("无映射应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "m2", "chat_completions"); got != nil {
		t.Fatalf("禁用映射应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "m3", "chat_completions"); got != nil {
		t.Fatalf("恒等映射应返回 nil：%v", got)
	}
	if got := resolveTestAccountModelMapping(source, "m4", "chat_completions"); got != nil {
		t.Fatalf("不支持的转换应返回 nil：%v", got)
	}
	resolved := resolveTestAccountModelMapping(source, "m1", "responses")
	if resolved == nil || resolved.upstreamModel != "u1" || resolved.upstreamEndpointFamily != "chat_completions" {
		t.Fatalf("正常解析不一致：%v", resolved)
	}
	// gemini 的 openai chat 档案不支持 messages 源。
	geminiChat := manualTestModeSource{providerCode: geminiProviderCode,
		providerProtocolProfileID: geminiOpenAIChatV1BetaProfile,
		modelMappings:             source.modelMappings}
	if got := resolveTestAccountModelMapping(geminiChat, "m1", "messages"); got != nil {
		t.Fatalf("gemini chat 档案 messages 源应返回 nil：%v", got)
	}
}

func TestW2IsAccountManualTestModel(t *testing.T) {
	openAI := manualTestModeSource{providerCode: "gpt", protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}
	anthropic := manualTestModeSource{providerCode: anthropicProviderCode, protocolCode: anthropicProtocolCodeConstant, protocolVersion: anthropicProtocolVersionConstant}
	gemini := manualTestModeSource{providerCode: geminiProviderCode, protocolCode: geminiProtocolCodeConstant, protocolVersion: geminiProtocolVersionConstant}
	hybrid := manualTestModeSource{providerCode: hybridProviderCode}

	t.Run("audio 模式一律不可测", func(t *testing.T) {
		if isAccountManualTestModel(testCatalogItem{mode: "Audio"}, openAI) {
			t.Fatal("audio 模式应不可测")
		}
	})
	t.Run("有启用映射即通过", func(t *testing.T) {
		source := manualTestModeSource{modelMappings: []ModelMapping{{SourceModel: "m1", Enabled: boolPtr(true)}}}
		if !isAccountManualTestModel(testCatalogItem{model: "m1", mode: "image_generation"}, source) {
			t.Fatal("映射命中的模型应可测")
		}
		disabled := manualTestModeSource{modelMappings: []ModelMapping{{SourceModel: "m1", Enabled: boolPtr(false)}}}
		if isAccountManualTestModel(testCatalogItem{model: "m1", mode: "image_generation"}, disabled) {
			t.Fatal("禁用映射不应命中")
		}
	})
	t.Run("空协议列表按模式排除图像", func(t *testing.T) {
		if !isAccountManualTestModel(testCatalogItem{mode: "chat"}, openAI) {
			t.Fatal("空协议列表的文本模型应可测")
		}
		if isAccountManualTestModel(testCatalogItem{mode: "image_generation"}, openAI) {
			t.Fatal("图像生成模式应排除")
		}
		if isAccountManualTestModel(testCatalogItem{mode: "image"}, openAI) {
			t.Fatal("图像模式应排除")
		}
	})
	t.Run("hybrid 协议判定", func(t *testing.T) {
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"chat_completions"}}, hybrid) {
			t.Fatal("hybrid 支持文本协议")
		}
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"stream_generate_content"}}, hybrid) {
			t.Fatal("hybrid 支持流式协议")
		}
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"images"}}, manualTestModeSource{providerCode: hybridProviderCode, accountType: "api_key"}) {
			t.Fatal("hybrid api_key 支持 images")
		}
		if isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"images"}}, manualTestModeSource{providerCode: hybridProviderCode, accountType: "oauth"}) {
			t.Fatal("hybrid oauth 不支持 images")
		}
		if isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"embeddings"}}, hybrid) {
			t.Fatal("hybrid 不支持 embeddings")
		}
	})
	t.Run("openai 协议判定", func(t *testing.T) {
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"responses"}}, openAI) {
			t.Fatal("openai 支持 responses")
		}
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"images"}}, manualTestModeSource{providerCode: "gpt", protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion, accountType: "api_key"}) {
			t.Fatal("openai api_key 支持 images")
		}
		if isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"messages"}}, openAI) {
			t.Fatal("openai 不支持 messages")
		}
	})
	t.Run("anthropic 与 gemini 协议判定", func(t *testing.T) {
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"messages"}}, anthropic) {
			t.Fatal("anthropic 支持 messages")
		}
		if isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"chat_completions"}}, anthropic) {
			t.Fatal("anthropic 不支持 chat_completions")
		}
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"generate_content"}}, gemini) {
			t.Fatal("gemini 支持 generate_content")
		}
		if !isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"interactions"}}, gemini) {
			t.Fatal("gemini 支持 interactions")
		}
		if isAccountManualTestModel(testCatalogItem{supportedAPIProtocols: []string{"responses"}}, gemini) {
			t.Fatal("gemini 不支持 responses")
		}
	})
}

func TestW2EndpointModeProtocolFamily(t *testing.T) {
	if _, err := endpointModeProtocolFamily("images_json"); err == nil {
		t.Fatal("images_json 应返回错误")
	}
	families := map[string]string{
		"chat_json": "chat_completions", "chat_sse": "chat_completions",
		"responses_json": "responses", "responses_sse": "responses",
		"messages_json": "messages", "messages_sse": "messages",
		"generate_content_sse": "stream_generate_content",
		"generate_content_json": "generate_content", "count_tokens": "generate_content",
	}
	for mode, family := range families {
		got, err := endpointModeProtocolFamily(mode)
		if err != nil || got != family {
			t.Fatalf("family 不一致：%s -> %q %v", mode, got, err)
		}
	}
}

func TestW2OptionMergeHelpers(t *testing.T) {
	t.Run("发布日期归一", func(t *testing.T) {
		cases := map[string]string{
			"2026-01-02": "2026-01-02",
			" 2026-01-02T10:00:00Z": "2026-01-02",
			"20260102":   "",
			"2026-1-2":   "",
			"2026-13-01": "",
			"2026-00-10": "",
			"2026-01-32": "",
			"":           "",
		}
		for input, want := range cases {
			if got := normalizedOptionReleaseDate(input); got != want {
				t.Fatalf("日期归一不一致：%q -> %q (期望 %q)", input, got, want)
			}
		}
	})
	t.Run("scope 优先级", func(t *testing.T) {
		if optionScopePriorityValue(catalogScopePersonal) != 3 || optionScopePriorityValue(catalogScopeGlobal) != 2 ||
			optionScopePriorityValue("built_in") != 1 || optionScopePriorityValue("") != 1 {
			t.Fatal("scope 优先级不一致")
		}
		if testCatalogScopePriority(catalogScopePersonal) != 3 || testCatalogScopePriority(catalogScopeGlobal) != 2 ||
			testCatalogScopePriority(catalogScopeBuiltIn) != 1 {
			t.Fatal("目录 scope 优先级不一致")
		}
	})
	t.Run("字符串去重", func(t *testing.T) {
		if got := dedupeTestStrings([]string{"a", "b", "a"}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Fatalf("去重结果不一致：%v", got)
		}
	})
	t.Run("openai 兼容供应商剔除自身", func(t *testing.T) {
		got := testCatalogBuiltInSourceCodes("openai", []string{"openai", "gpt", "  "})
		// 只剔除 openai 兼容自身；空白清洗由 normalizeTestProviderCodeList 负责。
		if len(got) != 2 || got[0] != "gpt" {
			t.Fatalf("剔除结果不一致：%v", got)
		}
		other := testCatalogBuiltInSourceCodes("gpt", []string{"gpt", "x"})
		if len(other) != 2 {
			t.Fatalf("非 openai 兼容供应商应原样返回：%v", other)
		}
	})
	t.Run("目录日期比较", func(t *testing.T) {
		if testCatalogReleaseDate("2026-01-02T00:00:00Z") != "2026-01-02" || testCatalogReleaseDate("short") != "" {
			t.Fatal("目录日期归一不一致")
		}
	})
	t.Run("启用映射检测", func(t *testing.T) {
		mappings := []ModelMapping{{SourceModel: "m1", Enabled: boolPtr(false)}, {SourceModel: "m2"}}
		if hasEnabledTestModelMapping(mappings, "m1") {
			t.Fatal("禁用映射不应命中")
		}
		if !hasEnabledTestModelMapping(mappings, "m2") {
			t.Fatal("默认启用映射应命中")
		}
	})
}

func TestW2AccountTestEndpointModeOrder(t *testing.T) {
	anthropic := manualTestModeSource{providerCode: anthropicProviderCode, protocolCode: anthropicProtocolCodeConstant,
		protocolVersion: anthropicProtocolVersionConstant, healthCheckEndpointMode: "messages_sse"}
	got := accountTestEndpointModeOrder(anthropic)
	if len(got) != 2 || got[0] != "messages_sse" || got[1] != "messages_json" {
		t.Fatalf("anthropic 顺序不一致：%v", got)
	}
	gemini := manualTestModeSource{providerCode: geminiProviderCode, protocolCode: geminiProtocolCodeConstant,
		protocolVersion: geminiProtocolVersionConstant}
	got = accountTestEndpointModeOrder(gemini)
	if len(got) == 0 || got[0] != "interactions_json" {
		t.Fatalf("gemini 顺序不一致：%v", got)
	}
	hybrid := manualTestModeSource{providerCode: hybridProviderCode, healthCheckEndpointMode: "chat_json"}
	got = accountTestEndpointModeOrder(hybrid)
	if len(got) < 8 || got[0] != "chat_json" {
		t.Fatalf("hybrid 顺序不一致：%v", got)
	}
	oauth := manualTestModeSource{providerCode: "gpt", accountType: "oauth", healthCheckEndpointMode: "responses_sse"}
	got = accountTestEndpointModeOrder(oauth)
	if len(got) != 2 || got[0] != "responses_sse" || got[1] != "responses_json" {
		t.Fatalf("oauth 顺序不一致：%v", got)
	}
	apiKey := manualTestModeSource{providerCode: "gpt", accountType: "api_key", healthCheckEndpointMode: "chat_sse"}
	got = accountTestEndpointModeOrder(apiKey)
	if len(got) != 4 || got[0] != "chat_sse" || got[3] != "responses_json" {
		t.Fatalf("api_key 顺序不一致：%v", got)
	}
}

func TestW2AccountManualTestEndpointModesFilters(t *testing.T) {
	// anthropic 源只保留 anthropic 模式，且顺序按 mode order 收敛。
	source := manualTestModeSource{
		providerCode: anthropicProviderCode, protocolCode: anthropicProtocolCodeConstant, protocolVersion: anthropicProtocolVersionConstant,
		supportedEndpointModes: []string{"messages_json", "chat_json", "messages_sse"},
		healthCheckEndpointMode: "messages_sse",
	}
	got := accountManualTestEndpointModes(source)
	if len(got) != 2 || got[0] != "messages_sse" || got[1] != "messages_json" {
		t.Fatalf("anthropic 模式过滤不一致：%v", got)
	}
	// 未知协议族回落为空。
	unknown := manualTestModeSource{providerCode: "custom", protocolCode: "grpc", protocolVersion: "v9"}
	if got := accountManualTestEndpointModes(unknown); len(got) != 0 {
		t.Fatalf("未知协议族应为空：%v", got)
	}
}

func TestW2AccountTestUnavailableMessage(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	blocker := func(scope, status, reason, label string) EffectiveAvailability {
		availability := EffectiveAvailability{Available: false, Status: status, Label: label}
		if scope != "" {
			availability.BlockerScope = &scope
		}
		if reason != "" {
			availability.Reason = &reason
		}
		return availability
	}
	ownerItem := ListItem{AccessType: "owner"}
	if message := accountTestUnavailableMessage(ownerItem, now); message != "" {
		t.Fatalf("owner 账户始终可测：%q", message)
	}
	authorizedAvailable := ListItem{AccessType: "authorized",
		EffectiveAvailability: EffectiveAvailability{Available: true}}
	if message := accountTestUnavailableMessage(authorizedAvailable, now); message != "" {
		t.Fatalf("可用授权实例应可测：%q", message)
	}
	runtimeBlocker := ListItem{AccessType: "authorized", EffectiveAvailability: blocker("runtime", "cooldown", "冷却", "冷却中")}
	if message := accountTestUnavailableMessage(runtimeBlocker, now); message != "" {
		t.Fatalf("runtime blocker 应放行：%q", message)
	}
	instanceDisabled := ListItem{AccessType: "authorized", EffectiveAvailability: blocker("authorized_instance", "instance_disabled", "", "")}
	if message := accountTestUnavailableMessage(instanceDisabled, now); message != "" {
		t.Fatalf("instance_disabled 应放行：%q", message)
	}
	// 可恢复失败态：active + 未过期 + 绑定分组 → 放行；其余拒绝并带原因。
	groupID := "grp-1"
	recoverable := ListItem{AccessType: "authorized", BoundGroupID: &groupID, Status: "pending_test",
		EffectiveAvailability: blocker("authorized_instance", "instance_pending_test", "等待健康检查", "待检查")}
	if message := accountTestUnavailableMessage(recoverable, now); message != "" {
		t.Fatalf("可恢复失败态应放行：%q", message)
	}
	expired := "2020-01-01T00:00:00Z"
	expiredItem := ListItem{AccessType: "authorized", BoundGroupID: &groupID, Status: "pending_test", AccountExpiresAt: &expired,
		EffectiveAvailability: blocker("authorized_instance", "instance_pending_test", "等待健康检查", "待检查")}
	if message := accountTestUnavailableMessage(expiredItem, now); message != "等待健康检查" {
		t.Fatalf("过期实例应拒绝并带原因：%q", message)
	}
	activeBlocked := ListItem{AccessType: "authorized", BoundGroupID: &groupID, Status: "active",
		EffectiveAvailability: blocker("authorized_instance", "instance_error", "实例错误", "错误")}
	if message := accountTestUnavailableMessage(activeBlocked, now); message != "实例错误" {
		t.Fatalf("active 实例应拒绝：%q", message)
	}
	// 未绑定分组时不可恢复。
	unbound := ListItem{AccessType: "authorized", Status: "pending_test",
		EffectiveAvailability: blocker("authorized_instance", "instance_pending_test", "等待健康检查", "待检查")}
	if message := accountTestUnavailableMessage(unbound, now); message != "等待健康检查" {
		t.Fatalf("未绑定分组应拒绝：%q", message)
	}
	// 无原因时回退 label。
	labeled := ListItem{AccessType: "authorized",
		EffectiveAvailability: blocker("", "", "", "维护中")}
	if message := accountTestUnavailableMessage(labeled, now); message != "维护中" {
		t.Fatalf("label 回退不一致：%q", message)
	}
}

func TestW2TestCatalogSourceCodes(t *testing.T) {
	env := newTestFamilyEnv(t, nil)
	env.login(t, "root", "root-pass", "super_admin")
	// gpt 供应商（openai/v1 档案）是 openai 协议族的既有成员。
	env.seedTestCatalog(t)
	store := env.store
	ctx := ensureCtx(context.Background())

	// 基础供应商：gpt（openai/v1 档案）+ anthropic + gemini + openai 兼容供应商。
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-anth', 'anthropic', 'Anthropic', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('profile_anthropic_anthropic_v1', 'anthropic', 'Anthropic', 1, 'anthropic', 'v1',
		'https://api.anthropic.com', 'm', '["api_key"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gem', 'gemini', 'Gemini', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('profile_gemini_gemini_v1beta', 'gemini', 'Gemini', 1, 'gemini', 'v1beta',
		'https://generativelanguage.googleapis.com', 'm', '["api_key"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-openai-compat-w2', 'openai', 'OpenAI 兼容', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('profile_openai_openai_v1_w2', 'openai', 'OpenAI 兼容', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'm', '["api_key"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-off-w2', 'offprovider', '停用', 0, '[]', ?, ?)`, now, now)

	t.Run("普通供应商直通", func(t *testing.T) {
		codes, err := store.testCatalogSourceCodes(ctx, "gpt")
		if err != nil || len(codes) != 1 || codes[0] != "gpt" {
			t.Fatalf("gpt 源码不一致：%v %v", codes, err)
		}
	})
	t.Run("空供应商返回空", func(t *testing.T) {
		codes, err := store.testCatalogSourceCodes(ctx, "  ")
		if err != nil || len(codes) != 0 {
			t.Fatalf("空供应商应返回空：%v %v", codes, err)
		}
	})
	t.Run("hybrid 聚合三个协议族", func(t *testing.T) {
		codes, err := store.testCatalogSourceCodes(ctx, hybridProviderCode)
		if err != nil {
			t.Fatalf("hybrid 源码错误：%v", err)
		}
		set := map[string]bool{}
		for _, code := range codes {
			set[code] = true
			if code == hybridProviderCode {
				t.Fatalf("聚合结果不应包含 hybrid 自身：%v", codes)
			}
		}
		for _, expected := range []string{"gpt", "anthropic", "gemini", "openai"} {
			if !set[expected] {
				t.Fatalf("聚合缺少 %s：%v", expected, codes)
			}
		}
		// 停用供应商不进入聚合。
		if set["offprovider"] {
			t.Fatalf("停用供应商不应进入聚合：%v", codes)
		}
	})
	t.Run("openai 兼容聚合同协议供应商", func(t *testing.T) {
		codes, err := store.testCatalogSourceCodes(ctx, "openai")
		if err != nil {
			t.Fatalf("openai 源码错误：%v", err)
		}
		set := map[string]bool{}
		for _, code := range codes {
			set[code] = true
		}
		if !set["openai"] || !set["gpt"] {
			t.Fatalf("openai 兼容聚合缺少自身或 gpt：%v", codes)
		}
		if set["anthropic"] {
			t.Fatalf("openai 兼容聚合不应包含 anthropic：%v", codes)
		}
	})

	t.Run("内置源码剔除 openai 兼容自身", func(t *testing.T) {
		got := testCatalogBuiltInSourceCodes("openai", []string{"openai", "gpt"})
		for _, code := range got {
			if code == "openai" {
				t.Fatalf("内置源码不应包含 openai 兼容自身：%v", got)
			}
		}
	})
}

func TestW2StoreHelpersEdge(t *testing.T) {
	t.Run("scopedOwnerID 空 access", func(t *testing.T) {
		if scopedOwnerID(nil) != "" {
			t.Fatal("nil access 应返回空")
		}
	})
	t.Run("credentialTextPresent", func(t *testing.T) {
		if credentialTextPresent(" x ") != true || credentialTextPresent("   ") != false ||
			credentialTextPresent(42) != false || credentialTextPresent(nil) != false {
			t.Fatal("credentialTextPresent 判定不一致")
		}
	})
	t.Run("scheduleToMap 与 JSON 串", func(t *testing.T) {
		if scheduleToMap(nil) != nil {
			t.Fatal("nil schedule 应返回 nil map")
		}
		if scheduleToJSONString("not-a-schedule") != nil {
			t.Fatal("非 schedule 类型应返回 nil")
		}
		schedule := &AvailabilitySchedule{Enabled: true}
		if scheduleToJSONString(schedule) == nil {
			t.Fatal("schedule 应能序列化")
		}
	})
	t.Run("normalizeDraftTextList 去重去空白", func(t *testing.T) {
		got := normalizeDraftTextList([]string{" a ", "a", "", "b"})
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Fatalf("归一结果不一致：%v", got)
		}
	})
}
