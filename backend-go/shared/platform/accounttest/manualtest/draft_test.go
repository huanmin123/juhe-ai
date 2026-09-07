package manualtest

// D3 修复（常驻审查第五轮）的回归测试：worker 侧草稿解密归一化必须对
// clientCompatibility / healthCheckEndpointMode 做 Node 枚举校验
// （account-test-tasks.repository.ts:1672-1673 / 1750-1751、1774-1779）；
// 非法值让整份草稿作废（nil → 任务回退保存账户测试路径）。

import "testing"

// validDraftRecord 返回一份通过必填校验的最小草稿。
func validDraftRecord() map[string]any {
	return map[string]any{
		"id":                      "acc_1",
		"ownerSystemAccountId":    "sys_owner",
		"groupId":                 "group_1",
		"providerCode":            "openai",
		"name":                    "测试账户",
		"type":                    "api_key",
		"credentials":             map[string]any{"api_key": "sk-test"},
		"clientCompatibility":     "openai_standard",
		"healthCheckModel":        "gpt-test",
		"healthCheckEndpointMode": "chat_json",
	}
}

func TestNormalizeDraftSnapshotAcceptsEnumValues(t *testing.T) {
	for _, compatibility := range []string{"openai_standard", "codex_responses"} {
		record := validDraftRecord()
		record["clientCompatibility"] = compatibility
		for _, mode := range []string{"images_json", "chat_json", "chat_sse", "responses_json", "responses_sse",
			"messages_json", "messages_sse", "generate_content_json", "generate_content_sse",
			"interactions_json", "interactions_sse"} {
			record["healthCheckEndpointMode"] = mode
			if draft := normalizeDraftSnapshot(record); draft == nil {
				t.Fatalf("compatibility=%s mode=%s must normalize", compatibility, mode)
			}
		}
	}
}

func TestNormalizeDraftSnapshotRejectsInvalidEnums(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"clientCompatibility 外来值", func(record map[string]any) {
			record["clientCompatibility"] = "claude_native"
		}},
		{"clientCompatibility 空值", func(record map[string]any) {
			record["clientCompatibility"] = ""
		}},
		{"healthCheckEndpointMode 外来值", func(record map[string]any) {
			record["healthCheckEndpointMode"] = "chat_xml"
		}},
		{"healthCheckEndpointMode 非健康检查形态", func(record map[string]any) {
			record["healthCheckEndpointMode"] = "count_tokens"
		}},
	}
	for _, testCase := range cases {
		record := validDraftRecord()
		testCase.mutate(record)
		if draft := normalizeDraftSnapshot(record); draft != nil {
			t.Fatalf("%s: draft must be discarded (nil), got %+v", testCase.name, draft)
		}
	}
}
