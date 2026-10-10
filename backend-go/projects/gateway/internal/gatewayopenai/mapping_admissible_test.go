package gatewayopenai

import (
	"testing"
)

// IsAdmissibleModelMapping：单条账户模型映射行的运行时可执行性判定
// （启用 + 源族可接受集 + 协议档案排除 + 恒等拒绝 + 转换矩阵），与
// ResolveAccountModelMapping 共享唯一函数源（网关模型列表账户并集设计 4.2）。

func boolPtr(v bool) *bool { return &v }

func admissibleMappingRow(sourceModel, sourceFamily, upstreamModel, upstreamFamily string, enabled *bool) AccountModelMapping {
	return AccountModelMapping{
		SourceModel:            sourceModel,
		SourceEndpointFamily:   sourceFamily,
		UpstreamModel:          upstreamModel,
		UpstreamEndpointFamily: upstreamFamily,
		Enabled:                enabled,
	}
}

func TestIsAdmissibleModelMapping(t *testing.T) {
	// 非 OpenAI 协议档案、非 hybrid：只剩同族改写与 stream→generate 放行。
	plainAccount := &RuntimeAccount{ProviderCode: "custom"}
	openAIAccount := &RuntimeAccount{ProviderCode: "openai"}
	openAIProfileAccount := &RuntimeAccount{
		ProviderCode:    "openai",
		ProtocolCode:    ProtocolCode,
		ProtocolVersion: ProtocolVersion,
	}
	hybridAccount := &RuntimeAccount{ProviderCode: "hybrid"}
	geminiProfileHybridAccount := &RuntimeAccount{
		ProviderCode:              "hybrid",
		ProviderProtocolProfileID: GeminiOpenAIChatProfileID,
	}
	geminiProfileOpenAIAccount := &RuntimeAccount{
		ProviderCode:              "openai",
		ProviderProtocolProfileID: GeminiOpenAIChatProfileID,
	}

	cases := []struct {
		name    string
		mapping AccountModelMapping
		account *RuntimeAccount
		want    bool
	}{
		// ① 启用门：Enabled 非 nil 且 false 排除；nil 视为启用。
		{"enabled=false excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(false)), openAIAccount, false},
		{"enabled=nil counts as enabled", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), openAIAccount, true},
		{"enabled=true admitted", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(true)), openAIAccount, true},

		// ② 源族可接受集：未知族与空族排除。
		{"unknown source family excluded", admissibleMappingRow("m5", "unknown_family", "m1", FamilyChatCompletions, nil), openAIAccount, false},
		{"empty source family excluded", admissibleMappingRow("m5", "", "m1", FamilyChatCompletions, nil), openAIAccount, false},

		// ③ 协议档案排除：GeminiOpenAIChatProfileID + messages 源族，即使
		// hybrid 矩阵支持 messages→chat_completions 也被前置档案门排除。
		{"gemini profile excludes messages source (hybrid matrix would allow)", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), geminiProfileHybridAccount, false},
		{"gemini profile excludes messages source (openai provider)", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), geminiProfileOpenAIAccount, false},
		{"gemini profile admits non-messages source", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), geminiProfileHybridAccount, true},

		// ④ 恒等改写拒绝：模型与族全同不是映射；同模型跨族不是恒等。
		{"identity row excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyChatCompletions, nil), openAIAccount, false},
		{"same model cross family is not identity (hybrid)", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyAnthropicMessages, nil), hybridAccount, true},

		// ⑤ 转换矩阵：同族改写与 stream_generate_content→generate_content 恒放行。
		{"same family rewrite admitted (openai)", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), openAIAccount, true},
		{"gemini stream to generate admitted (non-hybrid)", admissibleMappingRow("m5", FamilyGeminiStreamGenerate, "m1", FamilyGeminiGenerateContent, nil), plainAccount, true},

		// responses→chat_completions 仅 OpenAI 协议档案（或 hybrid）。
		{"responses to chat admitted (openai protocol profile)", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), openAIProfileAccount, true},
		{"responses to chat excluded (non-openai non-hybrid)", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), plainAccount, false},
		{"responses to chat admitted (hybrid)", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), hybridAccount, true},

		// hybrid 族对按 isOpenAIModelMappingRuntimeConversionSupported 实际矩阵。
		{"hybrid messages to chat admitted", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), hybridAccount, true},
		{"hybrid chat to messages admitted", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyAnthropicMessages, nil), hybridAccount, true},
		{"hybrid responses to messages admitted", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyAnthropicMessages, nil), hybridAccount, true},
		{"hybrid chat to generate_content admitted", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyGeminiGenerateContent, nil), hybridAccount, true},
		{"hybrid stream_generate to messages admitted", admissibleMappingRow("m5", FamilyGeminiStreamGenerate, "m1", FamilyAnthropicMessages, nil), hybridAccount, true},
		{"non-hybrid chat to generate_content excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyGeminiGenerateContent, nil), openAIAccount, false},
		{"non-hybrid messages to chat excluded", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), openAIAccount, false},
		{"hybrid chat to video excluded", admissibleMappingRow("m5", FamilyChatCompletions, "v1", FamilyVideoGeneration, nil), hybridAccount, false},

		// 媒体源族仅同族模型名改写；跨媒体族与媒体↔文本族均拒绝。
		{"media tts same family rename admitted", admissibleMappingRow("tts-a", FamilyTts, "tts-b", FamilyTts, nil), openAIAccount, true},
		{"media video to tts excluded", admissibleMappingRow("v1", FamilyVideoGeneration, "tts-b", FamilyTts, nil), hybridAccount, false},
		{"media video to chat excluded", admissibleMappingRow("v1", FamilyVideoGeneration, "m1", FamilyChatCompletions, nil), hybridAccount, false},

		// account 为 nil：无模型事实主体，恒不可执行。
		{"nil account excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAdmissibleModelMapping(tc.mapping, tc.account)
			if got != tc.want {
				t.Fatalf("IsAdmissibleModelMapping got %v want %v (mapping %+v)", got, tc.want, tc.mapping)
			}
		})
	}
}

// TestIsAdmissibleModelMappingConsistentWithResolve 锁定与
// ResolveAccountModelMapping 的判定一致性：IsAdmissible 为 true 时，以该行的
// source_model / source_endpoint_family 作为请求（Resolve 还要求精确匹配，
// 故请求输入直接取自行字段）必然解析到该行且字段一致；为 false 时必然解析不到。
func TestIsAdmissibleModelMappingConsistentWithResolve(t *testing.T) {
	cases := []struct {
		name    string
		mapping AccountModelMapping
		account RuntimeAccount
	}{
		{"enabled row", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(true)), RuntimeAccount{ProviderCode: "openai"}},
		{"disabled row", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(false)), RuntimeAccount{ProviderCode: "openai"}},
		{"unknown family", admissibleMappingRow("m5", "unknown_family", "m1", FamilyChatCompletions, nil), RuntimeAccount{ProviderCode: "openai"}},
		{"gemini profile messages source", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), RuntimeAccount{ProviderCode: "hybrid", ProviderProtocolProfileID: GeminiOpenAIChatProfileID}},
		{"identity row", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyChatCompletions, nil), RuntimeAccount{ProviderCode: "openai"}},
		{"matrix supported (responses to chat, openai profile)", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), RuntimeAccount{ProviderCode: "openai", ProtocolCode: ProtocolCode, ProtocolVersion: ProtocolVersion}},
		{"matrix unsupported (messages to chat, non-hybrid)", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), RuntimeAccount{ProviderCode: "openai"}},
		{"hybrid supported (chat to messages)", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyAnthropicMessages, nil), RuntimeAccount{ProviderCode: "hybrid"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := tc.account
			account.ModelMappings = []AccountModelMapping{tc.mapping}
			admissible := IsAdmissibleModelMapping(tc.mapping, &account)
			resolved := ResolveAccountModelMapping(&account, tc.mapping.SourceModel, tc.mapping.SourceEndpointFamily)
			if admissible && resolved == nil {
				t.Fatalf("admissible row did not resolve: %+v", tc.mapping)
			}
			if !admissible && resolved != nil {
				t.Fatalf("inadmissible row resolved: %+v -> %+v", tc.mapping, resolved)
			}
			if admissible &&
				(resolved.SourceModel != tc.mapping.SourceModel ||
					resolved.SourceEndpointFamily != tc.mapping.SourceEndpointFamily ||
					resolved.UpstreamModel != tc.mapping.UpstreamModel ||
					resolved.UpstreamEndpointFamily != tc.mapping.UpstreamEndpointFamily) {
				t.Fatalf("resolved fields mismatch: row %+v -> resolved %+v", tc.mapping, resolved)
			}
		})
	}

	// nil account：两侧都不可解析。
	if IsAdmissibleModelMapping(admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), nil) {
		t.Fatal("nil account should not be admissible")
	}
	if ResolveAccountModelMapping(nil, "m5", FamilyChatCompletions) != nil {
		t.Fatal("nil account should not resolve")
	}
}

// TestResolveSkipsDisabledRowAndPicksNext 锁定 Resolve 的既有查找语义
// （重构不变项）：循环跳过禁用行并命中后续启用行，逐行判定仍与
// IsAdmissibleModelMapping 一致。
func TestResolveSkipsDisabledRowAndPicksNext(t *testing.T) {
	disabled := admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(false))
	enabled := admissibleMappingRow("m5", FamilyChatCompletions, "m2", FamilyChatCompletions, boolPtr(true))
	account := &RuntimeAccount{ProviderCode: "openai", ModelMappings: []AccountModelMapping{disabled, enabled}}

	resolved := ResolveAccountModelMapping(account, "m5", FamilyChatCompletions)
	if resolved == nil || resolved.UpstreamModel != "m2" {
		t.Fatalf("expected enabled row m2, got %+v", resolved)
	}
	if IsAdmissibleModelMapping(disabled, account) {
		t.Fatal("disabled row should not be admissible")
	}
	if !IsAdmissibleModelMapping(enabled, account) {
		t.Fatal("enabled row should be admissible")
	}
}
