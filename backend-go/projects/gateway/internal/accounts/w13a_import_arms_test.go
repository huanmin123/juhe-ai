package accounts

// w13a import.go 未覆盖臂补齐：导入字段解析纯函数全分支、native 规划校验臂
// （代理/分组/协议档案/凭据/目录）、代理与分组解析臂、执行器失败-跳过-复用
// 臂。复用 w2 导入测试的 w2NativeDoc / w2APIKeyAccount / seedOpenAICompatibleProvider 基建。

import (
	"context"
	"strings"
	"testing"
)

func TestW13AImportFieldParsers(t *testing.T) {
	messages := []string{}
	// importOptionalTextField：缺失 / 非字符串 / 去空白。
	if got := importOptionalTextField(map[string]any{}, "k", "字段", &messages); got != "" || len(messages) != 0 {
		t.Fatalf("缺失应返回空：%q", got)
	}
	if got := importOptionalTextField(map[string]any{"k": 3}, "k", "字段", &messages); got != "" || len(messages) != 1 {
		t.Fatalf("非字符串应记消息：%q %v", got, messages)
	}
	if got := importOptionalTextField(map[string]any{"k": " x "}, "k", "字段", &messages); got != "x" {
		t.Fatalf("合法值应去空白：%q", got)
	}
	messages = nil
	// importOptionalBooleanField：缺失 / 非布尔 / 合法。
	if got := importOptionalBooleanField(map[string]any{}, "k", "字段", &messages); got != nil {
		t.Fatal("缺失应返回 nil")
	}
	if got := importOptionalBooleanField(map[string]any{"k": "x"}, "k", "字段", &messages); got != nil || len(messages) != 1 {
		t.Fatalf("非布尔应记消息：%v %v", got, messages)
	}
	if got := importOptionalBooleanField(map[string]any{"k": true}, "k", "字段", &messages); got == nil || !*got {
		t.Fatalf("合法布尔应透传：%v", got)
	}
	messages = nil
	// importOptionalPositiveIntegerField：缺失 0 / 非整数 / 零 / 合法。
	if got := importOptionalPositiveIntegerField(map[string]any{}, "k", "字段", &messages); got != 0 || len(messages) != 0 {
		t.Fatalf("缺失应为 0：%d", got)
	}
	if got := importOptionalPositiveIntegerField(map[string]any{"k": "x"}, "k", "字段", &messages); got != 0 || len(messages) != 1 {
		t.Fatalf("非整数应记消息：%d %v", got, messages)
	}
	if got := importOptionalPositiveIntegerField(map[string]any{"k": float64(0)}, "k", "字段", &messages); got != 0 || len(messages) != 2 {
		t.Fatalf("零应记消息：%d %v", got, messages)
	}
	if got := importOptionalPositiveIntegerField(map[string]any{"k": float64(3)}, "k", "字段", &messages); got != 3 {
		t.Fatalf("合法整数应透传：%d", got)
	}
	messages = nil
	// importOptionalNonNegativeIntegerField：缺失 -1 / 非整数 / 负数 / 合法。
	if got := importOptionalNonNegativeIntegerField(map[string]any{}, "k", "字段", &messages); got != -1 {
		t.Fatalf("缺失应为 -1：%d", got)
	}
	if got := importOptionalNonNegativeIntegerField(map[string]any{"k": 1.5}, "k", "字段", &messages); got != -1 || len(messages) != 1 {
		t.Fatalf("非整数应记消息：%d %v", got, messages)
	}
	if got := importOptionalNonNegativeIntegerField(map[string]any{"k": float64(-1)}, "k", "字段", &messages); got != -1 || len(messages) != 2 {
		t.Fatalf("负数应记消息：%d %v", got, messages)
	}
	if got := importOptionalNonNegativeIntegerField(map[string]any{"k": float64(2)}, "k", "字段", &messages); got != 2 {
		t.Fatalf("合法应透传：%d", got)
	}
	messages = nil
	// importOptionalStringArrayField：缺失 / 非数组 / 空数组 / 空白项 / 合法。
	if got := importOptionalStringArrayField(map[string]any{}, "k", "字段", &messages); got != nil {
		t.Fatal("缺失应返回 nil")
	}
	if got := importOptionalStringArrayField(map[string]any{"k": "x"}, "k", "字段", &messages); got != nil || len(messages) != 1 {
		t.Fatalf("非数组应记消息：%v %v", got, messages)
	}
	if got := importOptionalStringArrayField(map[string]any{"k": []any{}}, "k", "字段", &messages); got != nil || len(messages) != 2 {
		t.Fatalf("空数组应记消息：%v %v", got, messages)
	}
	if got := importOptionalStringArrayField(map[string]any{"k": []any{" "}}, "k", "字段", &messages); got != nil || len(messages) != 3 {
		t.Fatalf("空白项应记消息：%v %v", got, messages)
	}
	if got := importOptionalStringArrayField(map[string]any{"k": []any{" a ", "b"}}, "k", "字段", &messages); len(got) != 2 {
		t.Fatalf("合法数组应透传：%v", got)
	}
	messages = nil
	// importOptionalHealthCheckEndpointModeField：缺失 / 非法 / 合法。
	if got := importOptionalHealthCheckEndpointModeField(map[string]any{}, "k", "字段", &messages); got != "" {
		t.Fatalf("缺失应为空：%q", got)
	}
	if got := importOptionalHealthCheckEndpointModeField(map[string]any{"k": "bogus"}, "k", "字段", &messages); got != "" || len(messages) != 1 {
		t.Fatalf("非法应记消息：%q %v", got, messages)
	}
	if got := importOptionalHealthCheckEndpointModeField(map[string]any{"k": "chat_sse"}, "k", "字段", &messages); got != "chat_sse" {
		t.Fatalf("合法形态应透传：%q", got)
	}
}

func TestW13AImportTagsDateTimeMappingsParsers(t *testing.T) {
	messages := []string{}
	// importOptionalTagsField：缺失 / 非数组 / 非字符串项 / 空白跳过 / 超长 / 去重 / 超量。
	if got := importOptionalTagsField(map[string]any{}, "tags", "标签", &messages); got != nil {
		t.Fatal("缺失应返回 nil")
	}
	if got := importOptionalTagsField(map[string]any{"tags": "x"}, "tags", "标签", &messages); got != nil || len(messages) != 1 {
		t.Fatalf("非数组应记消息：%v %v", got, messages)
	}
	if got := importOptionalTagsField(map[string]any{"tags": []any{3}}, "tags", "标签", &messages); got != nil || len(messages) != 2 {
		t.Fatalf("非字符串项应记消息：%v %v", got, messages)
	}
	if got := importOptionalTagsField(map[string]any{"tags": []any{" ", " a ", "a"}}, "tags", "标签", &messages); len(got) != 1 || got[0] != "a" {
		t.Fatalf("空白应跳过且去重：%v %v", got, messages)
	}
	if got := importOptionalTagsField(map[string]any{"tags": []any{strings.Repeat("签", 41)}}, "tags", "标签", &messages); got != nil || len(messages) != 3 {
		t.Fatalf("超长标签应记消息：%v %v", got, messages)
	}
	many := []any{}
	for i := 0; i < maxTagsPerAccount+1; i++ {
		many = append(many, "标签"+string(rune('A'+i)))
	}
	if got := importOptionalTagsField(map[string]any{"tags": many}, "tags", "标签", &messages); got != nil || len(messages) != 4 {
		t.Fatalf("超过 24 个标签应记消息：%v", messages)
	}
	messages = nil
	// importOptionalDateTimeField：缺失 / 非字符串 / 空白 / 非法 / 合法归一化。
	if got := importOptionalDateTimeField(map[string]any{}, "t", "时间", &messages); got != "" {
		t.Fatalf("缺失应为空：%q", got)
	}
	if got := importOptionalDateTimeField(map[string]any{"t": 3}, "t", "时间", &messages); got != "" || len(messages) != 1 {
		t.Fatalf("非字符串应记消息：%q %v", got, messages)
	}
	if got := importOptionalDateTimeField(map[string]any{"t": " "}, "t", "时间", &messages); got != "" || len(messages) != 2 {
		t.Fatalf("空白应记消息：%q %v", got, messages)
	}
	if got := importOptionalDateTimeField(map[string]any{"t": "not-a-time"}, "t", "时间", &messages); got != "" || len(messages) != 3 {
		t.Fatalf("非法应记消息：%q %v", got, messages)
	}
	if got := importOptionalDateTimeField(map[string]any{"t": "2026-09-17T08:00:00Z"}, "t", "时间", &messages); got == "" {
		t.Fatalf("合法时间应归一化：%q %v", got, messages)
	}
	messages = nil
	// importModelMappingsField：缺失 / 非数组 / 项非对象 / 字段缺失 / 恒等映射跳过 /
	// 重复 source / enabled false。
	if got := importModelMappingsField(map[string]any{}, "mappings", "映射", &messages); got != nil {
		t.Fatal("缺失应返回 nil")
	}
	if got := importModelMappingsField(map[string]any{"mappings": "x"}, "mappings", "映射", &messages); got != nil || len(messages) != 1 {
		t.Fatalf("非数组应记消息：%v %v", got, messages)
	}
	if got := importModelMappingsField(map[string]any{"mappings": []any{"x"}}, "mappings", "映射", &messages); got != nil || len(messages) != 2 {
		t.Fatalf("项非对象应记消息：%v %v", got, messages)
	}
	if got := importModelMappingsField(map[string]any{"mappings": []any{map[string]any{}}}, "mappings", "映射", &messages); got != nil || len(messages) != 3 {
		t.Fatalf("字段缺失应记消息：%v %v", got, messages)
	}
	identity := importModelMappingsField(map[string]any{"mappings": []any{
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions",
			"upstreamModel": "m", "upstreamEndpointFamily": "chat_completions"},
	}}, "mappings", "映射", &messages)
	if len(identity) != 0 {
		t.Fatalf("恒等映射应被丢弃：%v", identity)
	}
	if got := importModelMappingsField(map[string]any{"mappings": []any{
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u", "upstreamEndpointFamily": "responses"},
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u2", "upstreamEndpointFamily": "responses"},
	}}, "mappings", "映射", &messages); got != nil || len(messages) == 0 {
		t.Fatalf("重复 source 应记消息：%v %v", got, messages)
	}
	messages = nil
	enabled := importModelMappingsField(map[string]any{"mappings": []any{
		map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u", "upstreamEndpointFamily": "responses", "enabled": false},
	}}, "mappings", "映射", &messages)
	if len(enabled) != 1 || enabled[0].Enabled == nil || *enabled[0].Enabled {
		t.Fatalf("enabled false 应保留：%+v %v", enabled, messages)
	}
	messages = nil
	// 映射族枚举。
	if importMappingSourceFamily("messages") != "messages" || importMappingSourceFamily("stream_generate_content") != "stream_generate_content" ||
		importMappingSourceFamily("bogus") != "" || importMappingSourceFamily(3) != "" {
		t.Fatal("source 族枚举不一致")
	}
	if importMappingUpstreamFamily("generate_content") != "generate_content" || importMappingUpstreamFamily("stream_generate_content") != "" {
		t.Fatal("upstream 族不支持流式 generate_content")
	}
	if importMappingText(" x ") != "x" || importMappingText(3) != "" {
		t.Fatal("映射文本归一不一致")
	}
	// normalizeImportStatus / normalizeImportProxyType / textPointerOrNil。
	for _, value := range []string{"active", "pending_test", "disabled"} {
		if normalizeImportStatus(value) != value {
			t.Fatalf("状态 %s 应透传", value)
		}
	}
	if normalizeImportStatus("bogus") != "" || normalizeImportStatus(3) != "" {
		t.Fatal("非法状态应为空")
	}
	for _, value := range []string{"HTTP", "https", "socks5", "socks5h"} {
		if normalizeImportProxyType(value) != strings.ToLower(value) {
			t.Fatalf("代理类型 %s 应归一化", value)
		}
	}
	if normalizeImportProxyType("ftp") != "" || normalizeImportProxyType(nil) != "" {
		t.Fatal("非法代理类型应为空")
	}
	if got := textPointerOrNil(" x "); got == nil || *got != " x " {
		t.Fatalf("文本指针应原样保留：%v", got)
	}
	if got := textPointerOrNil(""); got != nil {
		t.Fatalf("空文本应为 nil：%v", got)
	}
}

func TestW13AParseImportBodyArms(t *testing.T) {
	// data 可在解析层缺省（根校验在 buildImportPlan 内做）。
	if _, ok := parseImportBody(map[string]any{}); !ok {
		t.Fatal("缺 data 在解析层应通过")
	}
	// sourceMode 非字符串。
	if _, ok := parseImportBody(map[string]any{"data": map[string]any{}, "sourceMode": 3}); ok {
		t.Fatal("sourceMode 非字符串应拒绝")
	}
	// options 非对象。
	if _, ok := parseImportBody(map[string]any{"data": map[string]any{}, "options": "x"}); ok {
		t.Fatal("options 非对象应拒绝")
	}
	// options 未知键。
	if _, ok := parseImportBody(map[string]any{"data": map[string]any{}, "options": map[string]any{"bogus": 1}}); ok {
		t.Fatal("options 未知键应拒绝")
	}
	// 各布尔选项非布尔与合法。
	if _, ok := parseImportBody(map[string]any{"data": map[string]any{}, "options": map[string]any{"createMissingGroups": "x"}}); ok {
		t.Fatal("createMissingGroups 非布尔应拒绝")
	}
	if _, ok := parseImportBody(map[string]any{"data": map[string]any{}, "options": map[string]any{"createMissingProxies": "x"}}); ok {
		t.Fatal("createMissingProxies 非布尔应拒绝")
	}
	request, ok := parseImportBody(map[string]any{
		"data": map[string]any{}, "sourceMode": importSourceNative,
		"options": map[string]any{"createMissingGroups": true, "createMissingProxies": nil, "skipDuplicates": true},
	})
	if !ok || request.Options.CreateMissingGroups == nil || !*request.Options.CreateMissingGroups ||
		request.Options.SkipDuplicates == nil || !*request.Options.SkipDuplicates ||
		request.Options.CreateMissingProxies != nil {
		t.Fatalf("合法导入体解析不一致：%+v %v", request.Options, ok)
	}
}

// w13aRawDoc 构造原生文档的 raw 形态（允许任意条目类型以覆盖非对象臂）。
func w13aRawDoc(accounts, proxies []any) map[string]any {
	if accounts == nil {
		accounts = []any{}
	}
	if proxies == nil {
		proxies = []any{}
	}
	return map[string]any{
		"type": accountImportProtocolType, "version": float64(accountImportProtocolVersion),
		"accounts": accounts, "proxies": proxies,
	}
}

func TestW13AImportPlanValidationArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 代理臂：非对象、重复 ref、type 不支持、必填缺失、复用现有。
	result, err := env.store.PreviewImport(context.Background(), w13aRawDoc(
		[]any{w2APIKeyAccount("w13a-n1", "w13a-g1")},
		[]any{
			"not-an-object",
			map[string]any{"ref": "p1", "name": "w13a-p1", "type": "ftp", "host": "h", "port": float64(1)},
			map[string]any{"ref": "p1", "name": "w13a-p2", "type": "socks5", "host": "h", "port": float64(1)},
			map[string]any{"ref": "", "name": "", "type": "", "host": "", "port": float64(0)},
		},
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proxies) != 4 || result.Proxies[0].Action != importActionFailed {
		t.Fatalf("代理规划结果不一致：%+v", result.Proxies)
	}
	if result.Proxies[1].Action != importActionFailed || !strings.Contains(strings.Join(result.Proxies[1].Messages, "|"), "代理 type 不支持") {
		t.Fatalf("不支持类型应失败：%+v", result.Proxies[1])
	}
	if result.Proxies[2].Action != importActionFailed || !strings.Contains(strings.Join(result.Proxies[2].Messages, "|"), "代理 ref 重复") {
		t.Fatalf("重复 ref 应失败：%+v", result.Proxies[2])
	}
	if result.Proxies[3].Action != importActionFailed {
		t.Fatalf("必填缺失应失败：%+v", result.Proxies[3])
	}

	// 代理复用：已有同名代理 → reuse。
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-exist', ?, 'w13a-exist-proxy', 'socks5', 'h', 1080, 1, 'unknown', ?, ?)`, adminID, now, now)
	result, err = env.store.PreviewImport(context.Background(), w2NativeDoc(
		[]map[string]any{w2APIKeyAccount("w13a-n2", "w13a-g1")},
		[]map[string]any{map[string]any{"ref": "p9", "name": "w13a-exist-proxy", "type": "socks5", "host": "h", "port": float64(1)}},
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionReuse {
		t.Fatalf("同名代理应复用：%+v", result.Proxies)
	}

	// 账户臂：非对象、未知字段、缺 providerCode、缺 type、非法 status、
	// 非法并发、非法过期、未支持供应商、账户类型不支持、协议档案缺失。
	result, err = env.store.PreviewImport(context.Background(), w13aRawDoc(
		[]any{
			"not-an-object",
			map[string]any{"bogus": 1},
			map[string]any{"name": "w13a-n3", "type": "api_key", "status": "bogus", "concurrencyLimit": float64(-1),
				"accountExpiresAt": "not-a-time", "providerCode": "bogus-provider",
				"providerProtocolProfileId": "profile_bogus", "groupName": "w13a-g2",
				"credentials": map[string]any{"api_key": "sk-x", "base_url": "https://x"}},
			map[string]any{"name": "w13a-n4", "providerCode": "openai", "providerProtocolProfileId": openAICompatibleProfileID,
				"status": "active", "groupName": "w13a-g2", "type": "google_oauth",
				"credentials": map[string]any{"api_key": "sk-x", "base_url": "https://x"}},
		},
		nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 4 {
		t.Fatalf("账户规划数量不一致：%d", len(result.Accounts))
	}
	for _, index := range []int{0, 1, 2, 3} {
		if result.Accounts[index].Action != importActionFailed {
			t.Fatalf("第 %d 个账户应失败：%+v", index, result.Accounts[index])
		}
	}
	joined := strings.Join(result.Accounts[2].Messages, "|")
	for _, want := range []string{"不支持的供应商", "账户状态不支持", "concurrencyLimit", "accountExpiresAt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("消息缺 %q：%s", want, joined)
		}
	}
	if !strings.Contains(strings.Join(result.Accounts[3].Messages, "|"), "不支持账户类型") {
		t.Fatalf("账户类型不支持应记消息：%v", result.Accounts[3].Messages)
	}
	if result.CanImport {
		t.Fatal("全部失败时不可导入")
	}
	if result.Summary.Accounts.Failed != 4 {
		t.Fatalf("失败汇总不一致：%+v", result.Summary.Accounts)
	}
}

func TestW13AImportGroupAndProxyResolutionArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID) // gpt 默认分组，供应商与 openai 不一致
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-off', ?, 'w13a-off-proxy', 'socks5', 'h', 1080, 0, 'unknown', ?, ?)`, adminID, now, now)

	account := func(overrides map[string]any) map[string]any {
		base := w2APIKeyAccount("w13a-r1", "w13a-g")
		for key, value := range overrides {
			base[key] = value
		}
		return base
	}
	result, err := env.store.PreviewImport(context.Background(), w2NativeDoc(
		[]map[string]any{
			// groupId + groupName 同时填写 + groupId 不存在。
			account(map[string]any{"groupId": "grp-w13a-none", "groupName": "w13a-g", "name": "w13a-r1"}),
			// groupId 存在但供应商不一致。
			account(map[string]any{"groupId": "grp-default-" + adminID, "name": "w13a-r2"}),
			// groupId 与 groupName 都缺。
			account(map[string]any{"groupId": nil, "groupName": nil, "name": "w13a-r3"}),
			// proxyRef + proxyProfileId 同时填写。
			account(map[string]any{"groupName": "w13a-g2", "name": "w13a-r4", "proxyRef": "p", "proxyProfileId": "proxy-w13a-off"}),
			// proxyProfileId 不存在。
			account(map[string]any{"groupName": "w13a-g2", "name": "w13a-r5", "proxyProfileId": "proxy-w13a-none"}),
			// proxyProfileId 已停用。
			account(map[string]any{"groupName": "w13a-g2", "name": "w13a-r6", "proxyProfileId": "proxy-w13a-off"}),
			// proxyRef 不存在（未在文档中规划）。
			account(map[string]any{"groupName": "w13a-g2", "name": "w13a-r7", "proxyRef": "p-missing"}),
			// proxyRef 命中已停用的直接引用。
			account(map[string]any{"groupName": "w13a-g2", "name": "w13a-r8", "proxyRef": "proxy-w13a-off"}),
		},
		nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	assertMessage := func(index int, want string) {
		t.Helper()
		if got := strings.Join(result.Accounts[index].Messages, "|"); !strings.Contains(got, want) {
			t.Fatalf("第 %d 个账户缺 %q：%s", index, want, got)
		}
	}
	// groupId+groupName 同填的提示进 Warnings，groupId 不存在的报错进 Messages。
	if len(result.Accounts[0].Warnings) == 0 || !strings.Contains(result.Accounts[0].Warnings[0], "优先使用 groupId") {
		t.Fatalf("双填分组应记警告：%+v", result.Accounts[0].Warnings)
	}
	if got := strings.Join(result.Accounts[0].Messages, "|"); !strings.Contains(got, "分组不存在或无权使用") {
		t.Fatalf("第 0 个账户缺分组不存在消息：%s", got)
	}
	assertMessage(1, "分组供应商与账户供应商不一致")
	assertMessage(2, "账户 groupId 或 groupName 必填")
	assertMessage(3, "proxyRef 和 proxyProfileId 只能填写一个")
	assertMessage(4, "代理不存在")
	assertMessage(5, "代理已停用")
	assertMessage(6, "代理引用不存在")
	assertMessage(7, "代理已停用")

	// 分组不存在 + 未启用创建 → 分组不存在消息（createMissingGroups=false）。
	result, err = env.store.PreviewImport(context.Background(), w2NativeDoc(
		[]map[string]any{account(map[string]any{"name": "w13a-r9"})},
		nil,
	), "", ImportOptions{CreateMissingGroups: boolPtrW10(false)}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Accounts[0].Messages, "|"), "分组不存在") {
		t.Fatalf("未启用创建应报分组不存在：%+v", result.Accounts[0])
	}
}

func TestW13AImportExecuteArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 执行器：非法 sourceMode → buildImportPlan 错误（408-410）。
	if _, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(nil, nil), "bogus", ImportOptions{}, scope); err == nil {
		t.Fatal("非法 sourceMode 应报错")
	}
	// 执行器：CanImport=false → 提前返回（413-415）。
	result, err := env.store.ExecuteImport(context.Background(), map[string]any{"type": "bogus"}, "", ImportOptions{}, scope)
	if err != nil || result == nil || result.CanImport || result.Imported {
		t.Fatalf("不可导入计划应原样返回：%+v %v", result, err)
	}

	// 库内同名账户在执行层失败（既有测试把该语义固定为当前契约；
	// skipDuplicates 只作用于文档内重复，执行层跳过臂要求原始唯一索引
	// 错误，而 Store.Create 已归一化，故库内重名恒为 failed）。
	if _, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{w2APIKeyAccount("w13a-dup-acct", "w13a-g-exec")},
		[]map[string]any{w2Proxy("p-exec", "w13a-exec-proxy")},
	), "", ImportOptions{}, scope); err != nil {
		t.Fatal(err)
	}
	result, err = env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{w2APIKeyAccount("w13a-dup-acct", "w13a-g-exec2")},
		nil,
	), "", ImportOptions{SkipDuplicates: boolPtrW10(true)}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed ||
		!strings.Contains(strings.Join(result.Accounts[0].Messages, "|"), "账户名称已存在") {
		t.Fatalf("库内重名应失败：%+v", result.Accounts)
	}

	// 执行：未启用代理创建 + 账户引用文档内代理 → 代理跳过（1558-1559）、
	// 账户在计划层因代理未创建失败（1363-1364）。
	result, err = env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{withAPIKeyAccountOverrides(w2APIKeyAccount("w13a-px-acct", "w13a-g-exec3"), map[string]any{"proxyRef": "p-new"})},
		[]map[string]any{w2Proxy("p-new", "w13a-never-proxy")},
	), "", ImportOptions{CreateMissingProxies: boolPtrW10(false)}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionSkip {
		t.Fatalf("未启用创建时代理应跳过：%+v", result.Proxies)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionFailed ||
		!strings.Contains(strings.Join(result.Accounts[0].Messages, "|"), "代理引用未创建") {
		t.Fatalf("账户应因代理未创建失败：%+v", result.Accounts)
	}

	// 重复导入幂等：同名代理/分组已存在 → 计划层直接复用（ExecuteImport
	// 先重新规划，同名行在第二次规划即被解析为 reuse；执行器 INSERT 级的
	// 重名复用臂依赖 PG 唯一索引，SQLite 测试 schema 无该索引，不可达）。
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('proxy-w13a-reuse-hit', ?, 'w13a-reuse-proxy', 'socks5', 'h', 1080, 1, 'unknown', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO "groups" (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-w13a-reuse-hit', ?, 'w13a-reuse-group', 'openai', 1, 0, 'personal', ?, ?)`, adminID, now, now)
	result, err = env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{w2APIKeyAccount("w13a-reuse-acct2", "w13a-reuse-group")},
		[]map[string]any{w2Proxy("p-reuse2", "w13a-reuse-proxy")},
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionReuse || result.Proxies[0].ProxyProfileID == nil {
		t.Fatalf("同名代理应复用：%+v", result.Proxies)
	}
	if result.Summary.Groups.Reuse != 1 {
		t.Fatalf("同名分组应复用：%+v", result.Summary.Groups)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || result.Accounts[0].AccountID == nil {
		t.Fatalf("复用导入应创建账户：%+v", result.Accounts)
	}
}

func TestW13AImportCreateInputAndFullExecute(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 全字段成功导入：健康检查模型/形态、并发、优先级、开关、标签、过期与
	// 备注（importCreateInput 全臂）。映射与支持模型保持同协议同族。
	account := withAPIKeyAccountOverrides(w2APIKeyAccount("w13a-full-acct", "w13a-g-full"), map[string]any{
		"concurrencyLimit":        float64(7),
		"priority":                float64(2),
		"superPriorityEnabled":    true,
		"healthCheckModel":        "gpt-4o-mini",
		"healthCheckEndpointMode": "chat_json",
		"accountExpiresAt":        "2027-01-01T00:00:00Z",
		"notes":                   "w13a 备注创建",
		"tags":                    []any{"w13a-tag-a", " w13a-tag-a "},
		"supportedModels":         []any{"gpt-4o-mini", "m-a", "u-a"},
		"modelMappings":           []any{map[string]any{"sourceModel": "m-a", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u-a", "upstreamEndpointFamily": "chat_completions"}},
		"temporaryUnavailableContinuousProbeEnabled": false,
	})
	result, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{account},
		[]map[string]any{w2Proxy("p-full", "w13a-full-proxy")},
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Action != importActionCreate || result.Accounts[0].AccountID == nil {
		t.Fatalf("全字段导入应创建成功：%+v", result.Accounts)
	}
	created := result.Accounts[0].AccountID
	if env.count(t, `SELECT concurrency_limit FROM accounts WHERE id = ?`, created) != 7 {
		t.Fatal("并发限制应落库")
	}
	if got := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, created); got != "chat_json" {
		t.Fatalf("检查形态应落库：%q", got)
	}
	if got := env.queryCell(t, `SELECT notes FROM accounts WHERE id = ?`, created); got != "w13a 备注创建" {
		t.Fatalf("备注应落库：%q", got)
	}

	// PG 方言 PreviewImport → 表缺失错误（434-436）。
	pgStore, err := NewStore(env.db, true, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgStore.PreviewImport(context.Background(), w2NativeDoc(nil, nil), "", ImportOptions{}, scope); err == nil {
		t.Fatal("PG 方言预览应失败")
	}
}

func TestW13ABatchEditContextBodyArms(t *testing.T) {
	if _, _, message := batchEditContextBody(map[string]any{"bogus": 1}); message == "" {
		t.Fatal("未知键应拒绝")
	}
	if _, _, message := batchEditContextBody(map[string]any{"accountIds": "x"}); message == "" {
		t.Fatal("accountIds 非数组应拒绝")
	}
	if _, _, message := batchEditContextBody(map[string]any{"accountIds": []any{"a"}}); message == "" {
		t.Fatal("单账户应拒绝")
	}
	if _, _, message := batchEditContextBody(map[string]any{"accountIds": []any{"a", "a"}}); message == "" {
		t.Fatal("重复账户应拒绝")
	}
	if _, _, message := batchEditContextBody(map[string]any{"accountIds": []any{"a", ""}}); message == "" {
		t.Fatal("空白 ID 应拒绝")
	}
	ids, fields, message := batchEditContextBody(map[string]any{
		"accountIds": []any{" a ", "b"}, "fields": []any{"supportedModels", "modelMappings"},
	})
	if message != "" || len(ids) != 2 || ids[0] != "a" || len(fields) != 2 {
		t.Fatalf("合法批量上下文应通过：%v %v %q", ids, fields, message)
	}
	if _, _, message := batchEditContextBody(map[string]any{
		"accountIds": []any{"a", "b"}, "fields": []any{"bogus"},
	}); message == "" {
		t.Fatal("未知字段应拒绝")
	}
	if _, _, message := batchEditContextBody(map[string]any{
		"accountIds": []any{"a", "b"}, "fields": []any{"supportedModels", "modelMappings", "supportedEndpointModes", "supportedModels"},
	}); message == "" {
		t.Fatal("超过 3 个字段应拒绝")
	}
}

func withAPIKeyAccountOverrides(base map[string]any, overrides map[string]any) map[string]any {
	for key, value := range overrides {
		if value == nil {
			delete(base, key)
			continue
		}
		base[key] = value
	}
	return base
}
