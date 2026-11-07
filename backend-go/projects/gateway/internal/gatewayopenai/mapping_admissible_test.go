package gatewayopenai

import (
	"testing"
)

// IsAdmissibleModelMapping：单条账户模型映射行的运行时可执行性判定
// （启用 + 源族可接受集 + 恒等拒绝 + 转换矩阵），与
// ResolveAccountModelMapping 共享唯一函数源（网关模型列表账户并集设计 4.2）。
// 2026-11-07 跨协议模型映射退役后只保留协议等价映射：同族改写与
// stream_generate_content→generate_content 归一；其余跨族映射（含 OpenAI v1
// Responses→Chat Completions bridge）由写侧
// validateEntryMappingFamilyLinkage（§5.2 + §5.3）在保存时拒绝。

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
	// 拆门后判定不再读取账户身份字段；普通账户（无协议档案投影）即真实形态。
	plainAccount := &RuntimeAccount{}

	cases := []struct {
		name    string
		mapping AccountModelMapping
		account *RuntimeAccount
		want    bool
	}{
		// ① 启用门：Enabled 非 nil 且 false 排除；nil 视为启用。
		{"enabled=false excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(false)), plainAccount, false},
		{"enabled=nil counts as enabled", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), plainAccount, true},
		{"enabled=true admitted", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(true)), plainAccount, true},

		// ② 源族可接受集：未知族与空族排除。
		{"unknown source family excluded", admissibleMappingRow("m5", "unknown_family", "m1", FamilyChatCompletions, nil), plainAccount, false},
		{"empty source family excluded", admissibleMappingRow("m5", "", "m1", FamilyChatCompletions, nil), plainAccount, false},

		// ③ 跨协议映射已随 hybrid 退役：messages 源族到 chat 上游不再放行。
		{"messages source excluded", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), plainAccount, false},
		{"identity chat row still excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyChatCompletions, nil), plainAccount, false},

		// ④ 恒等改写拒绝：模型与族全同不是映射；同模型跨族同样不执行。
		{"identity row excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyChatCompletions, nil), plainAccount, false},
		{"same model cross family excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyAnthropicMessages, nil), plainAccount, false},

		// ⑤ 转换矩阵：同族改写与 stream_generate_content→generate_content 恒放行。
		{"same family rewrite admitted", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, nil), plainAccount, true},
		{"gemini stream to generate admitted", admissibleMappingRow("m5", FamilyGeminiStreamGenerate, "m1", FamilyGeminiGenerateContent, nil), plainAccount, true},

		// 2026-11-07 退役：OpenAI v1 Responses→Chat Completions bridge 不再放行；
		// 其余跨族 family 对在运行时与写侧同时拒绝。
		{"responses to chat bridge retired", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), plainAccount, false},
		{"chat to messages excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyAnthropicMessages, nil), plainAccount, false},
		{"responses to messages excluded", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyAnthropicMessages, nil), plainAccount, false},
		{"chat to generate_content excluded", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyGeminiGenerateContent, nil), plainAccount, false},
		{"stream_generate to messages excluded", admissibleMappingRow("m5", FamilyGeminiStreamGenerate, "m1", FamilyAnthropicMessages, nil), plainAccount, false},
		{"messages to generate_content excluded", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyGeminiGenerateContent, nil), plainAccount, false},

		// 矩阵外族对拒绝（chat→responses 不在 §5.2 矩阵，写侧同样拒绝）。
		{"chat to responses excluded (not in matrix)", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyResponses, nil), plainAccount, false},
		{"generate_content to responses excluded", admissibleMappingRow("m5", FamilyGeminiGenerateContent, "m1", FamilyResponses, nil), plainAccount, false},

		// 媒体源族仅同族模型名改写；跨媒体族与媒体↔文本族均拒绝。
		{"media tts same family rename admitted", admissibleMappingRow("tts-a", FamilyTts, "tts-b", FamilyTts, nil), plainAccount, true},
		{"media video to tts excluded", admissibleMappingRow("v1", FamilyVideoGeneration, "tts-b", FamilyTts, nil), plainAccount, false},
		{"media video to chat excluded", admissibleMappingRow("v1", FamilyVideoGeneration, "m1", FamilyChatCompletions, nil), plainAccount, false},
		{"chat to video excluded", admissibleMappingRow("m5", FamilyChatCompletions, "v1", FamilyVideoGeneration, nil), plainAccount, false},

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
		{"enabled row", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(true)), RuntimeAccount{}},
		{"disabled row", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyChatCompletions, boolPtr(false)), RuntimeAccount{}},
		{"unknown family", admissibleMappingRow("m5", "unknown_family", "m1", FamilyChatCompletions, nil), RuntimeAccount{}},
		{"messages source (former profile exclusion removed)", admissibleMappingRow("m5", FamilyAnthropicMessages, "m1", FamilyChatCompletions, nil), RuntimeAccount{}},
		{"identity row", admissibleMappingRow("m5", FamilyChatCompletions, "m5", FamilyChatCompletions, nil), RuntimeAccount{}},
		{"matrix retired (responses to chat, any account)", admissibleMappingRow("m5", FamilyResponses, "m1", FamilyChatCompletions, nil), RuntimeAccount{}},
		{"matrix unsupported (chat to responses)", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyResponses, nil), RuntimeAccount{}},
		{"matrix unsupported (chat to messages)", admissibleMappingRow("m5", FamilyChatCompletions, "m1", FamilyAnthropicMessages, nil), RuntimeAccount{}},
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
	account := &RuntimeAccount{ModelMappings: []AccountModelMapping{disabled, enabled}}

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
