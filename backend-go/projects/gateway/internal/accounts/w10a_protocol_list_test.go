package accounts

// w10a 第三批纯函数：协议映射矩阵断言/消息、响应检查规则归一化、额度恢复
// 时区、快照可选字段、列表排序列与有效可用性、用量作用域。全直测。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW10AProtocolMappingMatrix(t *testing.T) {
	// 端点族标签。
	if accountModelMappingEndpointFamilyLabel(mappingFamilyResponses) != "Responses" {
		t.Fatal("Responses 标签不一致")
	}
	if accountModelMappingEndpointFamilyLabel(mappingFamilyMessages) != "Messages" {
		t.Fatal("Messages 标签不一致")
	}
	if accountModelMappingEndpointFamilyLabel(mappingFamilyGenerateContent) != "Gemini GenerateContent" {
		t.Fatal("GenerateContent 标签不一致")
	}
	if accountModelMappingEndpointFamilyLabel(mappingFamilyStreamGenerateContent) != "Gemini StreamGenerateContent" {
		t.Fatal("StreamGenerateContent 标签不一致")
	}
	if accountModelMappingEndpointFamilyLabel("bogus") != "Chat Completions" {
		t.Fatal("默认标签应回退 Chat Completions")
	}

	// 协议池映射。
	if code, version := protocolPoolForMapping(mappingFamilyMessages); code != anthropicProtocolCodeConstant || version != anthropicProtocolVersionConstant {
		t.Fatalf("Messages 池错误：%s %s", code, version)
	}
	if code, version := protocolPoolForMapping(mappingFamilyGenerateContent); code != geminiProtocolCodeConstant || version != geminiProtocolVersionConstant {
		t.Fatalf("GenerateContent 池错误：%s %s", code, version)
	}
	if code, version := protocolPoolForMapping(mappingFamilyChatCompletions); code != openAIProtocolCode || version != openAIProtocolVersion {
		t.Fatalf("ChatCompletions 池错误：%s %s", code, version)
	}

	// 不可转换消息（三形态）。
	if msg := unsupportedProtocolConversionMessage(mappingFamilyMessages, mappingFamilyResponses); !strings.Contains(msg, "Anthropic Messages") {
		t.Fatalf("Messages 跨协议消息不一致：%s", msg)
	}
	if msg := unsupportedProtocolConversionMessage(mappingFamilyGenerateContent, mappingFamilyResponses); !strings.Contains(msg, "Gemini GenerateContent") {
		t.Fatalf("Gemini 跨协议消息不一致：%s", msg)
	}
	if msg := unsupportedProtocolConversionMessage(mappingFamilyChatCompletions, mappingFamilyResponses); !strings.Contains(msg, "Chat Completions 到 Responses") {
		t.Fatalf("默认跨协议消息不一致：%s", msg)
	}
	if msg := unsupportedHybridProtocolConversionMessage(mappingFamilyMessages, mappingFamilyChatCompletions); !strings.Contains(msg, "Messages 到 Chat Completions") {
		t.Fatalf("混合不可转换消息不一致：%s", msg)
	}
	if msg := missingUpstreamEndpointFamilyCapabilityMessage(mappingFamilyResponses); !strings.Contains(msg, "Responses") {
		t.Fatalf("缺失上游能力消息不一致：%s", msg)
	}

	// 单映射断言：合法配对通过、非法拒绝。
	if err := assertSupportedAccountModelMappingEndpointFamilyConversion(mappingFamilyChatCompletions, mappingFamilyChatCompletions); err != nil {
		t.Fatalf("合法映射应通过：%v", err)
	}
	if err := assertSupportedAccountModelMappingEndpointFamilyConversion(mappingFamilyChatCompletions, mappingFamilyResponses); err == nil {
		t.Fatal("非法映射应拒绝")
	}

	openAIProfile := protocolPredicateInput{protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion}
	anthropicProfile := protocolPredicateInput{protocolCode: anthropicProtocolCodeConstant, protocolVersion: anthropicProtocolVersionConstant}
	geminiProfile := protocolPredicateInput{protocolCode: geminiProtocolCodeConstant, protocolVersion: geminiProtocolVersionConstant}
	unknownProfile := protocolPredicateInput{protocolCode: "weird", protocolVersion: "v9"}

	// assertAccountModelMappingProtocolAllowed 各拒绝臂。
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{}, unknownProfile, nil); err == nil ||
		!strings.Contains(err.Error(), "当前供应商协议不支持模型映射") {
		t.Fatalf("未知协议档案应拒绝：%v", err)
	}
	geminiOpenAIChat := protocolPredicateInput{protocolCode: openAIProtocolCode, protocolVersion: openAIProtocolVersion,
		providerProtocolProfileID: geminiOpenAIChatV1BetaProfileID}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyGenerateContent, UpstreamEndpointFamily: mappingFamilyGenerateContent,
	}, geminiOpenAIChat, nil); err == nil || !strings.Contains(err.Error(), "Gemini OpenAI Chat") {
		t.Fatalf("Gemini OpenAI Chat 来源限制：%v", err)
	}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyChatCompletions, UpstreamEndpointFamily: mappingFamilyResponses,
	}, geminiOpenAIChat, nil); err == nil || !strings.Contains(err.Error(), "上游协议只能是 Chat Completions") {
		t.Fatalf("Gemini OpenAI Chat 上游限制：%v", err)
	}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyChatCompletions, UpstreamEndpointFamily: mappingFamilyChatCompletions,
	}, geminiProfile, nil); err == nil || !strings.Contains(err.Error(), "暂不支持账号模型别名") {
		t.Fatalf("普通 Gemini 档案应拒绝：%v", err)
	}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyResponses, UpstreamEndpointFamily: mappingFamilyResponses,
	}, anthropicProfile, nil); err == nil || !strings.Contains(err.Error(), "不支持 OpenAI 账号模型别名") {
		t.Fatalf("OpenAI 来源在非 OpenAI 档案应拒绝：%v", err)
	}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyMessages, UpstreamEndpointFamily: mappingFamilyMessages,
	}, openAIProfile, nil); err == nil || !strings.Contains(err.Error(), "不支持 Anthropic Messages 账号模型别名") {
		t.Fatalf("Messages 来源在非 Anthropic 档案应拒绝：%v", err)
	}
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyGenerateContent, UpstreamEndpointFamily: mappingFamilyGenerateContent,
	}, geminiProfile, nil); err == nil || !strings.Contains(err.Error(), "暂不支持账号模型别名") {
		t.Fatalf("Gemini 来源在非 native 档案应拒绝：%v", err)
	}

	// 合法 openai 映射通过。
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyChatCompletions, UpstreamEndpointFamily: mappingFamilyChatCompletions,
	}, openAIProfile, []string{"chat_json"}); err != nil {
		t.Fatalf("合法 openai 映射应通过：%v", err)
	}
	// 缺失上游能力 → missingUpstream 消息。
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyChatCompletions, UpstreamEndpointFamily: mappingFamilyChatCompletions,
	}, openAIProfile, nil); err == nil || !strings.Contains(err.Error(), "要求账户至少启用一种对应的上游接口能力") {
		t.Fatalf("缺失上游能力应拒绝：%v", err)
	}
	// requiresNativeResponses：responses→responses 无 responses 能力。
	if err := assertAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyResponses, UpstreamEndpointFamily: mappingFamilyResponses,
	}, openAIProfile, []string{"chat_json"}); err == nil || !strings.Contains(err.Error(), "原生上游") {
		t.Fatalf("requiresNativeResponses 应拒绝：%v", err)
	}

	// assertHybridAccountModelMappingProtocolAllowed。
	validHybrid := ModelMapping{
		SourceEndpointFamily: mappingFamilyChatCompletions, UpstreamEndpointFamily: mappingFamilyChatCompletions,
	}
	if err := assertHybridAccountModelMappingProtocolAllowed(validHybrid, []string{"chat_json"}); err != nil {
		t.Fatalf("合法混合映射应通过：%v", err)
	}
	if err := assertHybridAccountModelMappingProtocolAllowed(ModelMapping{
		SourceEndpointFamily: mappingFamilyGenerateContent, UpstreamEndpointFamily: mappingFamilyResponses,
	}, []string{"responses_json"}); err == nil {
		t.Fatal("规则表外的混合映射应拒绝")
	}
	if err := assertHybridAccountModelMappingProtocolAllowed(validHybrid, nil); err == nil ||
		!strings.Contains(err.Error(), "要求账户至少启用一种对应的上游接口能力") {
		t.Fatalf("混合缺失能力应拒绝：%v", err)
	}
}

func TestW10AResponseInspection(t *testing.T) {
	if _, err := normalizeResponseInspectionMatch("x"); err == nil {
		t.Fatal("非对象应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"clientProfiles": "x"}); err == nil {
		t.Fatal("clientProfiles 非数组应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"clientProfiles": []any{"a", "b", "c", "d", "e", "f", "g"}}); err == nil {
		t.Fatal("clientProfiles 超长应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"clientProfiles": []any{"bogus"}}); err == nil {
		t.Fatal("clientProfiles 非法值应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"errorCodes": "x"}); err == nil {
		t.Fatal("文本字段非数组应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"errorCodes": func() []any {
		out := []any{}
		for i := 0; i < 51; i++ {
			out = append(out, "x")
		}
		return out
	}()}); err == nil {
		t.Fatal("文本字段超长应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"errorCodes": []any{42}}); err == nil {
		t.Fatal("文本字段含非字符串应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"errorCodes": []any{"  "}}); err == nil {
		t.Fatal("空白项应拒绝")
	}
	if _, err := normalizeResponseInspectionMatch(map[string]any{"unknown": 1}); err == nil {
		t.Fatal("未知键应拒绝")
	}
	normalized, err := normalizeResponseInspectionMatch(map[string]any{
		"clientProfiles": []any{"codex"}, "errorCodes": []any{"429"},
	})
	if err != nil || len(normalized["errorCodes"].([]any)) != 1 || len(normalized["clientProfiles"].([]any)) != 1 {
		t.Fatalf("合法 match 应归一化：%v %v", normalized, err)
	}
	// responseInspectionRuleHasMatcher。
	if responseInspectionRuleHasMatcher(map[string]any{}) {
		t.Fatal("无 match 应返回 false")
	}
	if !responseInspectionRuleHasMatcher(map[string]any{"match": map[string]any{"errorCodes": []any{"429"}}}) {
		t.Fatal("有非空 matcher 应返回 true")
	}
	if responseInspectionRuleHasMatcher(map[string]any{"match": map[string]any{"errorCodes": []any{"  "}}}) {
		t.Fatal("全空白 matcher 应返回 false")
	}
}

func TestW10AQuotaRecoveryTimezone(t *testing.T) {
	if !validQuotaRecoveryTimezone("Asia/Shanghai") {
		t.Fatal("IANA 时区应通过")
	}
	if !validQuotaRecoveryTimezone("UTC") || !validQuotaRecoveryTimezone("GMT") {
		t.Fatal("UTC/GMT 应通过")
	}
	if !validQuotaRecoveryTimezone("UTC+08:00") || !validQuotaRecoveryTimezone("GMT-05:00") {
		t.Fatal("带偏移的 UTC/GMT 应通过")
	}
	if validQuotaRecoveryTimezone("Mars/Olympus") {
		t.Fatal("未知时区应拒绝")
	}
	if validQuotaRecoveryTimezone("UTC+08:00:00") {
		t.Fatal("非法偏移应拒绝")
	}
}

func TestW10ASnapshotOptionalHelpers(t *testing.T) {
	if optionalSnapshotString("abc") != "abc" || optionalSnapshotString(3) != "" {
		t.Fatal("optionalSnapshotString 语义不一致")
	}
	if value, err := requiredRFC3339Instant("2026-09-16T00:00:00Z", "时间"); err != nil || value != "2026-09-16T00:00:00Z" {
		t.Fatalf("合法时间应透传：%q %v", value, err)
	}
	if _, err := requiredRFC3339Instant("not-a-time", "时间"); err == nil {
		t.Fatal("非法时间应拒绝")
	}
}

func TestW10AListSortAndAvailability(t *testing.T) {
	cases := map[string]string{
		"priority":         "accounts.priority",
		"superPriority":    "accounts.super_priority_enabled",
		"fallback":         "accounts.fallback_enabled",
		"name":             "accounts.name",
		"type":             "accounts.type",
		"providerCode":     "accounts.provider_code",
		"systemAccount":    "COALESCE(system_accounts_ref.display_name, system_accounts_ref.username, accounts.system_account_id)",
		"concurrency":      "accounts.concurrency_limit",
		"accountExpiresAt": "accounts.account_expires_at",
		"lastUsedAt":       "accounts.last_used_at",
		"unknown":          "accounts.priority",
	}
	for field, want := range cases {
		if got := listSortColumn(field, "now"); got != want {
			t.Fatalf("listSortColumn(%s)：got %q want %q", field, got, want)
		}
	}
	if sortCol := listSortColumn("status", "now"); !strings.Contains(sortCol, "accounts.last_error_code") {
		t.Fatalf("status 排序应含状态 CASE：%s", sortCol)
	}

	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	strPtr := func(value string) *string { return &value }
	// ownerEffectiveAvailability 各分支。
	if got := ownerEffectiveAvailability(ListItem{Status: "active", Schedulable: true}, now); got.Available != true {
		t.Fatalf("健康账户应可用：%+v", got)
	}
	if got := ownerEffectiveAvailability(ListItem{Status: "active"}, now); got.Status != "instance_unschedulable" {
		t.Fatalf("停调账户应 instance_unschedulable：%+v", got)
	}
	if got := ownerEffectiveAvailability(ListItem{Status: "active", AccountExpiresAt: strPtr("2020-01-01T00:00:00Z")}, now); got.Status != "instance_expired" {
		t.Fatalf("过期账户应 instance_expired：%+v", got)
	}
	if got := ownerEffectiveAvailability(ListItem{Status: "active", LastErrorCode: strPtr("account_expired")}, now); got.Status != "instance_expired" {
		t.Fatalf("过期标记应 instance_expired：%+v", got)
	}
	if got := ownerEffectiveAvailability(ListItem{Status: "active", CooldownUntil: strPtr("2030-01-01T00:00:00Z")}, now); got.Status != "instance_cooldown" {
		t.Fatalf("冷却应 instance_cooldown：%+v", got)
	}
	for _, status := range []string{"disabled", "pending_test", "error", "rate_limited", "temporary_unavailable", "quality_isolated"} {
		got := ownerEffectiveAvailability(ListItem{Status: status, LastErrorMessage: strPtr("原因")}, now)
		if got.Available || !strings.HasPrefix(got.Status, "instance_") {
			t.Fatalf("状态 %s 应不可用：%+v", status, got)
		}
	}
	// 过去的冷却时间不阻塞。
	if got := ownerEffectiveAvailability(ListItem{Status: "active", Schedulable: true, CooldownUntil: strPtr("2020-01-01T00:00:00Z")}, now); got.Available != true {
		t.Fatalf("已过冷却不应阻塞：%+v", got)
	}
}

func TestW10AUsageScope(t *testing.T) {
	withAuth := accountUsageScope("row-1", "sys-1", "ra-1")
	if withAuth.ScopeType != usageScopeTypeAccountAuthorization || withAuth.ScopeID != "ra-1" {
		t.Fatalf("授权作用域不一致：%+v", withAuth)
	}
	withoutAuth := accountUsageScope("row-1", "sys-1", "")
	if withoutAuth.ScopeType != usageScopeTypeAccount || withoutAuth.ScopeID != "row-1" {
		t.Fatalf("账户作用域不一致：%+v", withoutAuth)
	}
}

func TestW10AUsageStatsTodayKey(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (
		system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	key := env.store.usageStatsTodayKey(context.Background())
	if key == "" {
		t.Fatal("缺失配置应回退 UTC 日期键")
	}
	// 合法时区配置生效（不会报错）。
	env.exec(t, `INSERT INTO system_settings (system_account_id, key, value_json, created_at, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '"Asia/Shanghai"', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if k := env.store.usageStatsTodayKey(context.Background()); k == "" {
		t.Fatal("合法时区应产出日期键")
	}
	// 非法时区回退 UTC。
	env.exec(t, `UPDATE system_settings SET value_json = '"Mars/Olympus"' WHERE key = 'usageStatsTimezone'`)
	if k := env.store.usageStatsTodayKey(context.Background()); k == "" {
		t.Fatal("非法时区应回退 UTC")
	}
}
