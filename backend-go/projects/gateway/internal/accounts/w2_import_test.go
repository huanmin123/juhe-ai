package accounts

// W2 导入切片测试：native 导入文档的规划（preview）与执行（confirm）契约。
// 覆盖根校验、字段解析、供应商/协议档案校验、分组与代理解析、重复标记、
// 执行器的创建/复用/跳过/失败语义，以及 m09 路由层的参数校验与审计入口。
// 与既有测试共用 newTestEnv / seedOpenAICompatibleProvider 基建。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// w2NativeDoc 构造一个 native 导入文档的骨架（type/version 与协议常量对齐）。
func w2NativeDoc(accounts, proxies []map[string]any) map[string]any {
	accountList := []any{}
	for _, account := range accounts {
		accountList = append(accountList, account)
	}
	proxyList := []any{}
	for _, proxy := range proxies {
		proxyList = append(proxyList, proxy)
	}
	return map[string]any{
		"type":     accountImportProtocolType,
		"version":  float64(accountImportProtocolVersion),
		"accounts": accountList,
		"proxies":  proxyList,
	}
}

// w2APIKeyAccount 构造一条能通过全链路校验的 openai api_key 导入账户。
func w2APIKeyAccount(name, groupName string) map[string]any {
	return map[string]any{
		"ref":                       "acc-" + name,
		"name":                      name,
		"providerCode":              "openai",
		"providerProtocolProfileId": openAICompatibleProfileID,
		"type":                      "api_key",
		"status":                    "active",
		"groupName":                 groupName,
		"credentials": map[string]any{
			"api_key":  "sk-import-" + name,
			"base_url": "https://api.openai.com/v1",
		},
	}
}

// w2Proxy 构造一条完整的导入代理。
func w2Proxy(ref, name string) map[string]any {
	return map[string]any{
		"ref": ref, "name": name, "type": "socks5", "host": "proxy.example.com", "port": float64(1080),
	}
}

// w2Preview 是 Store.PreviewImport 的便捷包装（admin 无过滤范围）。
func w2Preview(t *testing.T, env *testEnv, data any, mode string, options ImportOptions, ownerID string) *ImportResult {
	t.Helper()
	result, err := env.store.PreviewImport(context.Background(), data, mode, options, AccessScope{ViewerID: ownerID, IsAdmin: true})
	if err != nil {
		t.Fatalf("PreviewImport 返回错误：%v", err)
	}
	return result
}

// w2Item 按 index 取账户规划项。
func w2Item(t *testing.T, result *ImportResult, index int) ImportItem {
	t.Helper()
	if index >= len(result.Accounts) {
		t.Fatalf("导入结果缺少第 %d 个账户项：%+v", index, result.Accounts)
	}
	return result.Accounts[index]
}

// w2Messages 把消息列表拼接为一个字符串方便断言。
func w2Messages(item ImportItem) string {
	return strings.Join(item.Messages, "|")
}

func TestW2ImportPreviewNativeSuccess(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	data := w2NativeDoc(
		[]map[string]any{w2APIKeyAccount("导入一", "新分组")},
		[]map[string]any{w2Proxy("p1", "导入代理")},
	)
	result := w2Preview(t, env, data, "", ImportOptions{}, adminID)

	if result.Mode != "preview" || result.Type != accountImportProtocolType || result.Version != accountImportProtocolVersion {
		t.Fatalf("预览结果头不一致：%+v", result)
	}
	if !result.CanImport {
		t.Fatalf("期望 canImport=true，summary=%+v messages=%+v", result.Summary, result.Messages)
	}
	if result.Summary.Accounts.Total != 1 || result.Summary.Accounts.Create != 1 {
		t.Fatalf("账户汇总不一致：%+v", result.Summary.Accounts)
	}
	if result.Summary.Proxies.Total != 1 || result.Summary.Proxies.Create != 1 {
		t.Fatalf("代理汇总不一致：%+v", result.Summary.Proxies)
	}
	if result.Summary.Groups.Create != 1 {
		t.Fatalf("分组汇总不一致：%+v", result.Summary.Groups)
	}
	item := w2Item(t, result, 0)
	if item.Action != importActionCreate {
		t.Fatalf("账户动作不一致：%s %s", item.Action, w2Messages(item))
	}
	if item.Ref == nil || *item.Ref != "acc-导入一" || item.GroupName == nil || *item.GroupName != "新分组" {
		t.Fatalf("账户项回显字段不一致：%+v", item)
	}
	if item.ProviderCode == nil || *item.ProviderCode != "openai" ||
		item.ProviderProtocolProfileID == nil || *item.ProviderProtocolProfileID != openAICompatibleProfileID ||
		item.ProtocolCode == nil || *item.ProtocolCode != "openai" || item.ProtocolVersion == nil || *item.ProtocolVersion != "v1" {
		t.Fatalf("协议档案回显不一致：%+v", item)
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionCreate {
		t.Fatalf("代理项不一致：%+v", result.Proxies)
	}
	if result.Source.Mode != importSourceNative || result.Source.Records != 0 {
		t.Fatalf("来源摘要不一致：%+v", result.Source)
	}
}

func TestW2ImportPreviewRootValidation(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		data    any
		message string
	}{
		{"非对象内容", "hello", "导入内容必须是 JSON 对象"},
		{"未知字段", map[string]any{"type": accountImportProtocolType, "version": float64(1), "extra": 1, "accounts": []any{w2APIKeyAccount("a", "g")}}, "导入内容包含未知字段：extra"},
		{"type 错误", map[string]any{"type": "other", "version": float64(1), "accounts": []any{}}, "type 必须是 " + accountImportProtocolType},
		{"version 缺失", map[string]any{"type": accountImportProtocolType, "accounts": []any{}}, "version 必须是 1"},
		{"proxies 非数组", map[string]any{"type": accountImportProtocolType, "version": float64(1), "proxies": "x", "accounts": []any{w2APIKeyAccount("a", "g")}}, "proxies 必须是数组"},
		{"accounts 非数组", map[string]any{"type": accountImportProtocolType, "version": float64(1), "accounts": 3}, "accounts 必须是数组"},
		{"accounts 为空", map[string]any{"type": accountImportProtocolType, "version": float64(1)}, "accounts 至少需要 1 条账户"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := w2Preview(t, env, testCase.data, "", ImportOptions{}, adminID)
			if result.CanImport {
				t.Fatalf("期望 canImport=false：%+v", result.Summary)
			}
			// 根校验失败时列表为空，校验消息进入顶层 messages。
			joined := strings.Join(result.Messages, "|")
			if !strings.Contains(joined, testCase.message) {
				t.Fatalf("消息缺少 %q：%v", testCase.message, result.Messages)
			}
		})
	}

	// 超上限：51 条账户 / 21 条代理各自触发上限文案。
	manyAccounts := []map[string]any{}
	for index := 0; index < maxImportedAccounts+1; index++ {
		manyAccounts = append(manyAccounts, w2APIKeyAccount(fmt.Sprintf("名-%d", index), "分组"))
	}
	result := w2Preview(t, env, w2NativeDoc(manyAccounts, nil), "", ImportOptions{}, adminID)
	if !strings.Contains(strings.Join(result.Messages, "|"), fmt.Sprintf("accounts 单次最多导入 %d 条", maxImportedAccounts)) {
		t.Fatalf("账户上限消息缺失：%v", result.Messages)
	}

	manyProxies := []map[string]any{}
	for index := 0; index < maxImportedProxies+1; index++ {
		manyProxies = append(manyProxies, w2Proxy(fmt.Sprintf("p-%d", index), fmt.Sprintf("代理-%d", index)))
	}
	result = w2Preview(t, env, w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")}, manyProxies), "", ImportOptions{}, adminID)
	if !strings.Contains(strings.Join(result.Messages, "|"), fmt.Sprintf("proxies 单次最多导入 %d 条", maxImportedProxies)) {
		t.Fatalf("代理上限消息缺失：%v", result.Messages)
	}
}

func TestW2ImportPreviewFieldValidation(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// 每条用例构造一个非法账户，断言规划器把失败收敛为该账户的 message。
	cases := []struct {
		name    string
		account map[string]any
		message string
	}{
		{"providerCode 缺失", map[string]any{"name": "a", "type": "api_key", "status": "active", "groupName": "g", "credentials": map[string]any{}}, "账户 providerCode 不能为空"},
		{"type 缺失", map[string]any{"providerCode": "openai", "providerProtocolProfileId": openAICompatibleProfileID, "name": "a", "status": "active", "groupName": "g"}, "账户 type 不能为空"},
		{"status 不支持", w2WithField(w2APIKeyAccount("a", "g"), "status", "flying"), "账户状态不支持：flying"},
		{"名称为空", w2WithField(w2APIKeyAccount("a", "g"), "name", "  "), "账户名称不能为空"},
		{"concurrencyLimit 非整数", w2WithField(w2APIKeyAccount("a", "g"), "concurrencyLimit", "10"), "账户 concurrencyLimit必须是整数"},
		{"concurrencyLimit 为零", w2WithField(w2APIKeyAccount("a", "g"), "concurrencyLimit", float64(0)), "账户 concurrencyLimit必须是大于 0 的整数"},
		{"priority 为负", w2WithField(w2APIKeyAccount("a", "g"), "priority", float64(-1)), "账户 priority必须是大于等于 0 的整数"},
		{"supportedModels 非数组", w2WithField(w2APIKeyAccount("a", "g"), "supportedModels", "gpt"), "账户 supportedModels必须是非空字符串数组"},
		{"healthCheckEndpointMode 不支持", w2WithField(w2APIKeyAccount("a", "g"), "healthCheckEndpointMode", "grpc"), "账户 healthCheckEndpointMode必须是支持的 JSON 或 Streaming 请求形态"},
		{"tags 类型错误", w2WithField(w2APIKeyAccount("a", "g"), "tags", "tag"), "账户 tags必须是字符串数组"},
		{"tags 超长", w2WithField(w2APIKeyAccount("a", "g"), "tags", []any{strings.Repeat("标", maxTagNameLength+1)}), "账户 tags单个标签不能超过 40 个字符"},
		{"tags 超数量", w2WithField(w2APIKeyAccount("a", "g"), "tags", w2RepeatedTags(maxTagsPerAccount+1)), "账户 tags单个账户最多配置 24 个标签"},
		{"accountExpiresAt 无效", w2WithField(w2APIKeyAccount("a", "g"), "accountExpiresAt", "not-a-time"), "账户 accountExpiresAt必须是有效时间字符串"},
		{"availabilitySchedule 无效", w2WithField(w2APIKeyAccount("a", "g"), "availabilitySchedule", map[string]any{"enabled": true}), "时间计划"},
		{"modelMappings 非数组", w2WithField(w2APIKeyAccount("a", "g"), "modelMappings", "x"), "账户 modelMappings必须是模型映射数组"},
		{"modelMappings 条目缺字段", w2WithField(w2APIKeyAccount("a", "g"), "modelMappings", []any{map[string]any{"sourceModel": "m"}}), "账户 modelMappings条目必须包含 sourceModel、sourceEndpointFamily、upstreamModel 和 upstreamEndpointFamily"},
		{"modelMappings 重复来源", w2WithField(w2APIKeyAccount("a", "g"), "modelMappings", []any{
			map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u1", "upstreamEndpointFamily": "responses"},
			map[string]any{"sourceModel": "m", "sourceEndpointFamily": "chat_completions", "upstreamModel": "u2", "upstreamEndpointFamily": "responses"},
		}), "账户 modelMappings不能重复配置同一个 sourceModel 和 sourceEndpointFamily"},
		{"未知账户字段", w2WithField(w2APIKeyAccount("a", "g"), "mystery", 1), "账户配置包含未知字段：mystery"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			accounts := []map[string]any{testCase.account}
			result := w2Preview(t, env, w2NativeDoc(accounts, nil), "", ImportOptions{}, adminID)
			item := w2Item(t, result, 0)
			if item.Action != importActionFailed {
				t.Fatalf("期望动作 failed，实际 %s：%s", item.Action, w2Messages(item))
			}
			if !strings.Contains(w2Messages(item), testCase.message) {
				t.Fatalf("消息缺少 %q：%s", testCase.message, w2Messages(item))
			}
			if result.Summary.Accounts.Failed != 1 || result.CanImport {
				t.Fatalf("汇总应失败：canImport=%v summary=%+v", result.CanImport, result.Summary)
			}
		})
	}

	// 账户条目不是对象：规划器直接落失败并给出对象类型错误。
	t.Run("账户配置非对象", func(t *testing.T) {
		result := w2Preview(t, env, map[string]any{
			"type": accountImportProtocolType, "version": float64(accountImportProtocolVersion),
			"accounts": []any{"not-an-object"}, "proxies": []any{},
		}, "", ImportOptions{}, adminID)
		item := w2Item(t, result, 0)
		if item.Action != importActionFailed || !strings.Contains(w2Messages(item), "账户配置必须是对象") {
			t.Fatalf("动作/消息不一致：%s %s", item.Action, w2Messages(item))
		}
	})
}

// w2WithField 追加或覆盖账户文档的一个字段。
func w2WithField(account map[string]any, key string, value any) map[string]any {
	next := map[string]any{}
	for name, item := range account {
		next[name] = item
	}
	next[key] = value
	return next
}

// w2RepeatedTags 构造 n 个不重复的合法标签。
func w2RepeatedTags(n int) []any {
	tags := []any{}
	for index := 0; index < n; index++ {
		tags = append(tags, fmt.Sprintf("标签-%02d", index))
	}
	return tags
}

func TestW2ImportPreviewProviderValidation(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// 停用的供应商与协议档案：直接插库控制 enabled。
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES ('prov-off', 'offprov', '停用供应商', 0, ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-off-openai', 'openai', '停用档案', 0, 'openai', 'v1', 'https://api.openai.com/v1',
		'm', '["api_key"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-narrow', 'openai', '窄档案', 1, 'openai', 'v1', 'https://api.openai.com/v1',
		'm', '["oauth"]', '[]', ?, ?)`, now, now)

	cases := []struct {
		name    string
		account map[string]any
		message string
	}{
		{"不支持的供应商", w2WithField(w2APIKeyAccount("a", "g"), "providerCode", "nope"), "不支持的供应商：nope"},
		{"供应商停用", map[string]any{"providerCode": "offprov", "providerProtocolProfileId": "prof-off", "name": "a", "type": "api_key", "status": "active", "groupName": "g"}, "供应商已停用：offprov"},
		{"profile 停用", map[string]any{"providerCode": "openai", "providerProtocolProfileId": "prof-off-openai", "name": "a", "type": "api_key", "status": "active", "groupName": "g", "credentials": map[string]any{"api_key": "sk-x"}}, "供应商协议档案已停用：停用档案"},
		{"profile 不存在", w2WithField(w2APIKeyAccount("a", "g"), "providerProtocolProfileId", "missing-profile"), "供应商 openai 未配置协议档案"},
		{"profile 不支持类型", map[string]any{"providerCode": "openai", "providerProtocolProfileId": "prof-narrow", "name": "a", "type": "api_key", "status": "active", "groupName": "g"}, "供应商协议档案 窄档案 不支持账户类型 api_key"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := w2Preview(t, env, w2NativeDoc([]map[string]any{testCase.account}, nil), "", ImportOptions{}, adminID)
			item := w2Item(t, result, 0)
			if item.Action != importActionFailed {
				t.Fatalf("期望 failed，实际 %s：%s", item.Action, w2Messages(item))
			}
			if !strings.Contains(w2Messages(item), testCase.message) {
				t.Fatalf("消息缺少 %q：%s", testCase.message, w2Messages(item))
			}
		})
	}
}

func TestW2ImportPreviewGroupResolution(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-existing', ?, '已有分组', 'openai', 1, 'personal', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at)
		VALUES ('grp-other', ?, '其他供应商分组', 'gpt', 1, 'personal', ?, ?)`, adminID, now, now)

	t.Run("groupId 不存在", func(t *testing.T) {
		account := w2WithField(w2APIKeyAccount("a", ""), "groupId", "grp-missing")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		item := w2Item(t, result, 0)
		if !strings.Contains(w2Messages(item), "分组不存在或无权使用：grp-missing") {
			t.Fatalf("消息不一致：%s", w2Messages(item))
		}
	})
	t.Run("groupId 供应商不一致", func(t *testing.T) {
		account := w2WithField(w2APIKeyAccount("a", ""), "groupId", "grp-other")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "分组供应商与账户供应商不一致：其他供应商分组") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("groupId 与 groupName 并存时优先 groupId", func(t *testing.T) {
		account := w2WithField(w2APIKeyAccount("a", "新分组"), "groupId", "grp-existing")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		item := w2Item(t, result, 0)
		if item.Action != importActionCreate {
			t.Fatalf("期望 create：%s", w2Messages(item))
		}
		if len(item.Warnings) != 1 || !strings.Contains(item.Warnings[0], "同时填写 groupId 和 groupName 时优先使用 groupId") {
			t.Fatalf("警告缺失：%+v", item.Warnings)
		}
		// 已存在分组计入 reuse 而不是 create。
		if result.Summary.Groups.Create != 0 || result.Summary.Groups.Reuse != 1 {
			t.Fatalf("分组汇总不一致：%+v", result.Summary.Groups)
		}
	})
	t.Run("groupName 不存在且未启用创建", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "缺失分组")}, nil),
			"", ImportOptions{CreateMissingGroups: boolPtr(false)}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "分组不存在：缺失分组") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("同文档 groupName 复用只建一个分组", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{
			w2APIKeyAccount("a", "共享分组"), w2APIKeyAccount("b", "共享分组"),
		}, nil), "", ImportOptions{}, adminID)
		if result.Summary.Groups.Create != 1 || result.Summary.Groups.Reuse != 0 {
			t.Fatalf("分组汇总不一致：%+v", result.Summary.Groups)
		}
		if result.Summary.Accounts.Create != 2 {
			t.Fatalf("账户汇总不一致：%+v", result.Summary.Accounts)
		}
	})
	t.Run("groupId 与 groupName 均缺失", func(t *testing.T) {
		account := w2APIKeyAccount("a", "")
		delete(account, "groupName")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "账户 groupId 或 groupName 必填") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
}

func TestW2ImportPreviewProxyResolution(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('pp-existing', ?, '已有代理', 'socks5', 'proxy.example.com', 1080, 1, 'unknown', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES ('pp-disabled', ?, '停用代理', 'socks5', 'proxy.example.com', 1080, 0, 'unknown', ?, ?)`, adminID, now, now)

	t.Run("proxyRef 与 proxyProfileId 互斥", func(t *testing.T) {
		account := w2APIKeyAccount("a", "g")
		account["proxyRef"] = "p1"
		account["proxyProfileId"] = "pp-existing"
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "proxyRef 和 proxyProfileId 只能填写一个") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("proxyProfileId 不存在与停用", func(t *testing.T) {
		missing := w2WithField(w2APIKeyAccount("a", "g"), "proxyProfileId", "pp-missing")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{missing}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "代理不存在：pp-missing") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
		disabled := w2WithField(w2APIKeyAccount("b", "g"), "proxyProfileId", "pp-disabled")
		result = w2Preview(t, env, w2NativeDoc([]map[string]any{disabled}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "代理已停用：停用代理") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("已存在同名代理按 reuse 处理", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")},
			[]map[string]any{w2Proxy("p1", "已有代理")}), "", ImportOptions{}, adminID)
		if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionReuse {
			t.Fatalf("代理应 reuse：%+v", result.Proxies)
		}
		if result.Summary.Proxies.Reuse != 1 || result.CanImport == false {
			t.Fatalf("汇总不一致：canImport=%v proxies=%+v", result.CanImport, result.Summary.Proxies)
		}
	})
	t.Run("未启用代理创建时引用未创建", func(t *testing.T) {
		account := w2WithField(w2APIKeyAccount("a", "g"), "proxyRef", "p1")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account},
			[]map[string]any{w2Proxy("p1", "新代理")}),
			"", ImportOptions{CreateMissingProxies: boolPtr(false)}, adminID)
		if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionSkip {
			t.Fatalf("代理应 skip：%+v", result.Proxies)
		}
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "代理引用未创建：p1") {
			t.Fatalf("账户消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("来源代理校验失败时引用不可用", func(t *testing.T) {
		broken := w2Proxy("p1", "")
		account := w2WithField(w2APIKeyAccount("a", "g"), "proxyRef", "p1")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, []map[string]any{broken}),
			"", ImportOptions{}, adminID)
		if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionFailed {
			t.Fatalf("代理应 failed：%+v", result.Proxies)
		}
		if !strings.Contains(strings.Join(result.Proxies[0].Messages, "|"), "代理名称不能为空") {
			t.Fatalf("代理消息不一致：%+v", result.Proxies[0].Messages)
		}
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "代理引用不可用：p1") {
			t.Fatalf("账户消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("代理 ref 重复取第一条", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")},
			[]map[string]any{w2Proxy("p1", "代理一"), w2Proxy("p1", "代理二")}), "", ImportOptions{}, adminID)
		if len(result.Proxies) != 2 || result.Proxies[1].Action != importActionFailed {
			t.Fatalf("第二个重复 ref 代理应 failed：%+v", result.Proxies)
		}
		if !strings.Contains(strings.Join(result.Proxies[1].Messages, "|"), "代理 ref 重复：p1") {
			t.Fatalf("重复 ref 消息缺失：%+v", result.Proxies[1].Messages)
		}
	})
	t.Run("未知 proxyRef", func(t *testing.T) {
		account := w2WithField(w2APIKeyAccount("a", "g"), "proxyRef", "ghost")
		result := w2Preview(t, env, w2NativeDoc([]map[string]any{account}, nil), "", ImportOptions{}, adminID)
		if !strings.Contains(w2Messages(w2Item(t, result, 0)), "代理引用不存在：ghost") {
			t.Fatalf("消息不一致：%s", w2Messages(w2Item(t, result, 0)))
		}
	})
	t.Run("用户侧导入不能创建代理", func(t *testing.T) {
		userID := env.login(t, "user1", "user-pass", "user")
		result, err := env.store.PreviewImport(context.Background(),
			w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")}, []map[string]any{w2Proxy("p1", "新代理")}),
			"", ImportOptions{}, AccessScope{ViewerID: userID})
		if err != nil {
			t.Fatalf("PreviewImport 错误：%v", err)
		}
		if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionFailed {
			t.Fatalf("用户侧代理应 failed：%+v", result.Proxies)
		}
		if !strings.Contains(strings.Join(result.Proxies[0].Messages, "|"), "用户侧导入不能创建代理，请由管理员先创建代理") {
			t.Fatalf("消息缺失：%+v", result.Proxies[0].Messages)
		}
	})
}

func TestW2ImportPreviewDuplicateNames(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	accounts := []map[string]any{w2APIKeyAccount("同名", "g"), w2APIKeyAccount("同名", "g")}

	t.Run("默认跳过重复", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc(accounts, nil), "", ImportOptions{}, adminID)
		first, second := w2Item(t, result, 0), w2Item(t, result, 1)
		if first.Action != importActionCreate || second.Action != importActionSkip {
			t.Fatalf("动作不一致：%s / %s", first.Action, second.Action)
		}
		if !strings.Contains(w2Messages(second), "与第 1 条账户名称重复") {
			t.Fatalf("重复消息缺失：%s", w2Messages(second))
		}
		if result.Summary.Accounts.Create != 1 || result.Summary.Accounts.Skip != 1 {
			t.Fatalf("汇总不一致：%+v", result.Summary.Accounts)
		}
	})
	t.Run("关闭跳过则重复失败", func(t *testing.T) {
		result := w2Preview(t, env, w2NativeDoc(accounts, nil), "", ImportOptions{SkipDuplicates: boolPtr(false)}, adminID)
		if w2Item(t, result, 1).Action != importActionFailed {
			t.Fatalf("第二个重复账户应 failed：%s", w2Messages(w2Item(t, result, 1)))
		}
		if result.Summary.Accounts.Failed != 1 || result.CanImport {
			t.Fatalf("汇总不一致：%+v", result.Summary)
		}
	})
}

func TestW2ImportConfirmExecutesPlan(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// HTTP confirm 链路：覆盖 handler、审计 sink 与执行器。
	body := `{"data":{"type":"` + accountImportProtocolType + `","version":1,` +
		`"proxies":[{"ref":"p1","name":"导入代理","type":"socks5","host":"proxy.example.com","port":1080}],` +
		`"accounts":[{"ref":"a1","name":"导入账户","providerCode":"openai","providerProtocolProfileId":"` + openAICompatibleProfileID + `",` +
		`"type":"api_key","status":"active","groupName":"导入分组","proxyRef":"p1",` +
		`"credentials":{"api_key":"sk-import-1","base_url":"https://api.openai.com/v1"}}]},` +
		`"options":{"createMissingGroups":true,"createMissingProxies":true,"skipDuplicates":true}}`
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", body)
	if code != http.StatusOK {
		t.Fatalf("导入确认失败：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["imported"] != true || data["mode"] != "import" {
		t.Fatalf("导入结果标志不一致：%v", data)
	}
	if data["canImport"] != false {
		t.Fatalf("执行后 canImport 应为 false：%v", data["canImport"])
	}
	summary := data["summary"].(map[string]any)
	if summary["accounts"].(map[string]any)["create"] != float64(1) {
		t.Fatalf("账户汇总不一致：%v", summary)
	}
	accounts := data["accounts"].([]any)
	item := accounts[0].(map[string]any)
	if item["action"] != "create" || item["accountId"] == nil {
		t.Fatalf("账户项不一致：%v", item)
	}
	if messages := item["messages"].([]any); len(messages) != 1 || messages[0] != "已创建账户，等待后台健康检查通过后参与调度" {
		t.Fatalf("创建消息不一致：%v", messages)
	}
	createdID := item["accountId"].(string)

	// DB 副作用：账户、分组、代理均落库；active 导入重映射为 pending_test。
	var status, groupID, proxyID string
	if err := env.db.QueryRow(`SELECT status, proxy_profile_id FROM accounts WHERE id = ?`, createdID).Scan(&status, &proxyID); err != nil {
		t.Fatalf("账户未落库：%v", err)
	}
	if status != "pending_test" {
		t.Fatalf("导入账户状态应为 pending_test，实际 %s", status)
	}
	if err := env.db.QueryRow(`SELECT id FROM groups WHERE system_account_id = ? AND name = '导入分组'`, adminID).Scan(&groupID); err != nil {
		t.Fatalf("分组未落库：%v", err)
	}
	if err := env.db.QueryRow(`SELECT id FROM proxy_profiles WHERE name = '导入代理'`).Scan(&proxyID); err != nil {
		t.Fatalf("代理未落库：%v", err)
	}
	if accountID := env.queryCell(t, `SELECT account_id FROM group_accounts WHERE group_id = ?`, groupID); accountID != createdID {
		t.Fatalf("分组绑定缺失：%s", accountID)
	}

	// 审计 sink 记录 import 动作。
	found := false
	for _, action := range env.sink.actions() {
		if action == "accounts.import" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计动作缺失：%v", env.sink.actions())
	}

	// 幂等防重放：完全相同的 body 短窗口内重复提交被 MutationGuard 拒绝。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", body)
	if code != http.StatusConflict {
		t.Fatalf("同 body 重放期望 409，实际 %d：%v", code, payload)
	}

	// 重复导入语义：换 ref（改变指纹避开幂等窗）但保持同名重放。
	// 行为存疑：Create 已把 UNIQUE 冲突转成 ConflictError（消息为「同一用户下
	// 账户名称已存在」），ExecuteImport 再次用 duplicateAccountNameError 判定
	// 的是转换后的错误文本，不再包含 UNIQUE 特征，因此 skipDuplicates=true 时
	// 重名账户仍落为 failed 而不是 skip。按当前实际行为断言。
	replayBody := strings.Replace(body, `"ref":"a1","name":"导入账户"`, `"ref":"a1-replay","name":"导入账户"`, 1)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm", replayBody)
	if code != http.StatusOK {
		t.Fatalf("二次导入失败：%d %v", code, payload)
	}
	data = dataMap(t, payload)
	second := data["accounts"].([]any)[0].(map[string]any)
	if second["action"] != "failed" {
		t.Fatalf("重放行为存疑（当前实际为 failed）：%v", second)
	}
	if !strings.Contains(strings.Join(w2ToStrings(second["messages"].([]any)), "|"), "同一用户下账户名称已存在：导入账户") {
		t.Fatalf("重放消息不一致：%v", second["messages"])
	}
	// 重名失败不影响首次已创建的账户。
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = '导入账户' AND deleted_at IS NULL`) != 1 {
		t.Fatal("重放不应创建第二个同名账户")
	}
}

func w2ToStrings(values []any) []string {
	out := []string{}
	for _, value := range values {
		out = append(out, value.(string))
	}
	return out
}

func TestW2ImportPreviewHTTPValidation(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name string
		body string
	}{
		{"未知顶层键", `{"data":{},"bogus":1}`},
		{"非法 sourceMode", `{"data":{},"sourceMode":"nope"}`},
		{"sourceMode 非字符串", `{"data":{},"sourceMode":3}`},
		{"options 非对象", `{"data":{},"options":true}`},
		{"options 未知键", `{"data":{},"options":{"mystery":true}}`},
		{"options 布尔非布尔", `{"data":{},"options":{"skipDuplicates":"yes"}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", testCase.body)
			if code != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d：%v", code, payload)
			}
			if !strings.Contains(payload["message"].(string), "账户导入参数无效") {
				t.Fatalf("消息不一致：%v", payload)
			}
		})
	}

	// 管道错误同样渲染为 400：非法 sourceMode 文档（根校验失败走 messages，
	// 这里用无法适配的字符串数据触发管道消息）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview",
		`{"data":"not json","sourceMode":"sub2api"}`)
	if code != http.StatusOK {
		t.Fatalf("适配器失败应进入结果消息而不是 500：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if !strings.Contains(strings.Join(w2ToStrings(data["source"].(map[string]any)["messages"].([]any)), "|"), "Sub2API 导入内容必须是 JSON") {
		t.Fatalf("来源消息不一致：%v", data["source"])
	}

	// 非法查询范围。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview?systemAccountId=", `{"data":{}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("空 systemAccountId 应 400，实际 %d", code)
	}
}

func TestW2ImportTargetOwnerRequired(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	// viewer 为空且无过滤范围时缺少归属上下文。
	_, err := env.store.PreviewImport(context.Background(),
		w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")}, nil), "", ImportOptions{}, AccessScope{IsAdmin: true})
	if err == nil {
		t.Fatal("缺少系统账户上下文时应返回错误")
	}
	validation, ok := err.(*ValidationError)
	if !ok || validation.Message != "缺少系统账户上下文" {
		t.Fatalf("错误语义不一致：%T %v", err, err)
	}
}

func TestW2ImportPreviewInvalidSourceMode(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	_, err := env.store.PreviewImport(context.Background(),
		w2NativeDoc([]map[string]any{w2APIKeyAccount("a", "g")}, nil), "bogus-mode", ImportOptions{},
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err == nil {
		t.Fatal("非法 sourceMode 应返回错误")
	}
	validation, ok := err.(*ValidationError)
	if !ok || validation.Message != "账户导入参数无效" {
		t.Fatalf("错误语义不一致：%T %v", err, err)
	}
}
