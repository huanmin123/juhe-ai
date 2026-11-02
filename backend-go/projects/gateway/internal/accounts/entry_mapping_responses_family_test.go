package accounts

// responses→responses 同族改名映射回归（2026-11-02 生产缺陷）。
//
// 生产现象：编辑 AI 账户时把「账号模型别名」的上游协议选成 Responses，保存
// 恒 400「请求参数无效」（账户 api.zeekai.cc-v44hst，2026-11-02 14:09-14:12
// 连续 4 次）。根因是写侧 §5.2 矩阵 entryMappingPairTargetModes 漏了
// responses→responses——前端 accountModelMappingProtocolMatrix 把它当同族
// 改名放行、运行时 IsAdmissibleModelMapping 的 source==upstream 分支也放行，
// 只有写侧校验拒绝；且生产库里已有 144 行此类存量映射（导入链直插写入）。
//
// 矩阵外的组合仍必须拒绝，且拒绝文案必须是中文：kernel localize 会把不含
// CJK 的 400 文案改写成「请求参数无效」，原文从响应侧不可见（BUG-0337 口径）。

import (
	"net/http"
	"strings"
	"testing"
)

// TestResponsesIdentityMappingSavesWithResponsesOnlyConnection 锁定
// Responses-only 账户（gpt.oauth / xai.oauth 的 responses_json/responses_sse
// 词表）能保存同族改名映射——这是该形态账户唯一可用的映射方向。
func TestResponsesIdentityMappingSavesWithResponsesOnlyConnection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "responses 同族改名")

	// Responses-only 连接 + 健康检查三件套同批切换（§8 同现契约）。
	code, payload := w2PatchBody(t, env, id, `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["responses_json","responses_sse"],"enabled":true}],
		"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"responses_json","healthCheckModel":"gpt-4o-mini"`)
	if code != http.StatusOK {
		t.Fatalf("切换 Responses-only 连接应成功：%d %v", code, payload)
	}

	code, payload = w2PatchBody(t, env, id, `"supportedModels":["gpt-4o-mini","gpt-4o"],
		"modelMappings":[{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o","upstreamEndpointFamily":"responses"}]`)
	if code != http.StatusOK {
		t.Fatalf("responses→responses 同族改名应保存成功：%d %v", code, payload)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?
		AND source_endpoint_family = 'responses' AND target_endpoint_family = 'responses'
		AND source_model = 'gpt-4o-mini'`, id); got != 1 {
		t.Fatalf("responses→responses 映射未落库：%d", got)
	}
}

// TestResponsesIdentityMappingCoexistsWithChatAliases 覆盖生产截图的混合载荷：
// chat_completions→chat_completions 与 responses→responses 同账户共存时必须
// 全部保存（修复前整批 400，chat 行也被连带拒绝）。
func TestResponsesIdentityMappingCoexistsWithChatAliases(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	id := w2PatchAccount(t, env, "responses 混合载荷")

	code, payload := w2PatchBody(t, env, id, `"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["chat_json","chat_sse","responses_json","responses_sse"],"enabled":true}],
		"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"chat_json","healthCheckModel":"gpt-4o-mini"`)
	if code != http.StatusOK {
		t.Fatalf("四形态连接应成功：%d %v", code, payload)
	}

	code, payload = w2PatchBody(t, env, id, `"supportedModels":["gpt-4o-mini","gpt-4o"],
		"modelMappings":[
			{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"chat_completions","upstreamModel":"gpt-4o","upstreamEndpointFamily":"chat_completions"},
			{"sourceModel":"gpt-4o-mini","sourceEndpointFamily":"responses","upstreamModel":"gpt-4o","upstreamEndpointFamily":"responses"}
		]`)
	if code != http.StatusOK {
		t.Fatalf("混合同族改名应保存成功：%d %v", code, payload)
	}
	for _, family := range []string{"chat_completions", "responses"} {
		if got := env.count(t, `SELECT COUNT(*) FROM account_entry_mappings WHERE account_id = ?
			AND source_endpoint_family = ? AND target_endpoint_family = ?`, id, family, family); got != 1 {
			t.Fatalf("%s 同族改名映射未落库：%d", family, got)
		}
	}
}

// TestResponsesIdentityMappingStillRejectsUnsupportedPairs 锁定矩阵边界：
// responses→responses 放行不能顺带放开其它非法组合，且拒绝文案必须中文可归因。
func TestResponsesIdentityMappingStillRejectsUnsupportedPairs(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		target  string
		message string
	}{
		{
			// chat_completions→responses 由 §5.2 明确禁止：Chat 请求无法合成
			// Responses 的服务端状态、compact 与 hosted tools 语义。
			name: "chat 到 responses", source: "chat_completions", target: "responses",
			message: "Chat Completions 到 Responses",
		},
		{
			name: "responses 到 tts", source: "responses", target: "tts",
			message: "Responses 到 TTS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEntryMappingFamilyLinkage(tc.source, tc.target,
				[]string{"chat_json", "chat_sse", "responses_json", "responses_sse", "audio_speech"},
				[]string{"chat_json", "chat_sse", "responses_json", "responses_sse", "audio_speech"})
			if err == nil {
				t.Fatalf("%s→%s 应拒绝", tc.source, tc.target)
			}
			// 含 CJK 才不会被 kernel localize 改写（BUG-0337 口径）：断言原文
			// 同时带中文前缀与两端族名，用户可直接归因。
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("拒绝文案应点名协议组合并含中文：%v", err)
			}
			if !strings.Contains(err.Error(), "账号模型别名") {
				t.Fatalf("拒绝文案应含中文前缀（localize 不改写）：%v", err)
			}
		})
	}
}

// TestResponsesIdentityMappingRequiresResponsesModes 锁定 §5.3 联动：放行
// family pair 不等于放行任意账户——目标连接与目标模型都必须真实具备
// responses_json/responses_sse 之一，否则仍 400 且中文可归因。
func TestResponsesIdentityMappingRequiresResponsesModes(t *testing.T) {
	err := validateEntryMappingFamilyLinkage("responses", "responses",
		[]string{"chat_json", "chat_sse"},
		[]string{"chat_json", "chat_sse", "responses_json"})
	if err == nil {
		t.Fatal("连接缺 Responses 形态时应拒绝")
	}
	if !strings.Contains(err.Error(), "账号模型别名") || !strings.Contains(err.Error(), "Responses") {
		t.Fatalf("应中文点名 Responses 上游形态缺失：%v", err)
	}

	// 模型侧缺 Responses 形态同样拒绝（交集为空）。
	if err := validateEntryMappingFamilyLinkage("responses", "responses",
		[]string{"responses_json", "responses_sse"},
		[]string{"chat_json", "chat_sse"}); err == nil {
		t.Fatal("模型缺 Responses 形态时应拒绝")
	}

	// 两侧存在共同形态即放行（§5.3 要求的是交集非空，不要求形态全等）。
	if err := validateEntryMappingFamilyLinkage("responses", "responses",
		[]string{"responses_json", "responses_sse"}, []string{"responses_sse"}); err != nil {
		t.Fatalf("共有 responses_sse 应可执行：%v", err)
	}
	// 形态完全不重叠时仍拒绝。
	if err := validateEntryMappingFamilyLinkage("responses", "responses",
		[]string{"responses_json"}, []string{"responses_sse"}); err == nil {
		t.Fatal("connection∩model 交集为空时应拒绝")
	}
}
