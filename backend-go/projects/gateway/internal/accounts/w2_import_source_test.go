package accounts

// W2 导入来源适配器测试：Sub2API 导出与 NewAPI/One-API Channel 转储到 native
// 导入文档的改写契约（import_source.go）。CLIProxyAPI/YAML 适配器由
// import_source_yaml_test.go 覆盖，此处不重复。

import (
	"context"
	"strings"
	"testing"
)

// w2PreviewSource 以指定来源模式做预览并返回结果（admin 无过滤范围）。
func w2PreviewSource(t *testing.T, env *testEnv, data any, mode string, ownerID string) *ImportResult {
	t.Helper()
	result, err := env.store.PreviewImport(context.Background(), data, mode, ImportOptions{},
		AccessScope{ViewerID: ownerID, IsAdmin: true})
	if err != nil {
		t.Fatalf("PreviewImport(%s) 错误：%v", mode, err)
	}
	return result
}

// w2SourceText 把来源摘要的消息拼接为一个字符串。
func w2SourceText(result *ImportResult) string {
	return strings.Join(result.Source.Messages, "|")
}

func TestW2Sub2APIAdapterAPIKeyAccounts(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"unknown_top": 1,
		"proxies": []any{
			map[string]any{"proxy_key": "p1", "name": "来源代理", "protocol": "SOCKS5",
				"host": "proxy.example.com", "port": float64(1080), "status": "active",
				"username": "u1", "password": "pw", "extra": true},
			map[string]any{"proxy_key": "p2", "name": "停用代理", "protocol": "http",
				"host": "proxy.example.com", "port": float64(8080), "status": "disabled"},
		},
		"accounts": []any{
			map[string]any{"name": "密钥账户", "platform": "OpenAI", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-live-1", "base_url": "https://api.openai.com/v1"},
				"proxy_key":   "p1", "concurrency": float64(3), "priority": float64(2),
				"notes": "备注", "expires_at": float64(1767225600), "legacy_field": true},
			map[string]any{"name": "多密钥账户", "platform": "gpt", "type": "api_key",
				"credentials": map[string]any{"api_keys": []any{"sk-a-1", "sk-a-1", "sk-***", "sk-a-2", "  ", 42},
					"base_url": "https://api.openai.com/v1"}},
			map[string]any{"name": "引用缺失", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-live-2"}, "proxy_key": "ghost"},
			map[string]any{"name": "引用停用", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-live-3"}, "proxy_key": "p2"},
			map[string]any{"name": "平台不支持", "platform": "anthropic", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-live-4"}},
			map[string]any{"name": "类型不支持", "platform": "openai", "type": "vertex",
				"credentials": map[string]any{"api_key": "sk-live-5"}},
			map[string]any{"name": "缺凭据", "platform": "openai", "type": "api_key"},
		},
	}
	result := w2PreviewSource(t, env, document, importSourceSub2Api, adminID)

	source := result.Source
	if source.Mode != importSourceSub2Api || source.Records != 7 || source.Accepted != 2 || source.Skipped != 5 {
		t.Fatalf("来源摘要不一致：%+v", source)
	}
	// 未识别字段计数：顶层 1（unknown_top）+ 代理 extra 1 + 账户 legacy_field 1。
	if source.IgnoredFields != 3 {
		t.Fatalf("忽略字段计数不一致：%d", source.IgnoredFields)
	}

	if len(result.Accounts) != 2 {
		t.Fatalf("应产出 2 个可导入账户：%+v", result.Accounts)
	}
	first := result.Accounts[0]
	if first.Name == nil || *first.Name != "密钥账户" || first.Action != importActionCreate {
		t.Fatalf("第一个账户项不一致：%+v", first)
	}
	// Sub2API 的 OpenAI 平台账户落到 openai 兼容供应商与协议档案。
	if first.ProviderCode == nil || *first.ProviderCode != openAICompatibleProvider ||
		first.ProviderProtocolProfileID == nil || *first.ProviderProtocolProfileID != openAICompatibleProfileID {
		t.Fatalf("供应商映射不一致：%+v", first)
	}
	if first.ProxyRef == nil || *first.ProxyRef != "sub2api-proxy-1" {
		t.Fatalf("代理引用映射不一致：%+v", first)
	}
	second := result.Accounts[1]
	if second.Name == nil || *second.Name != "多密钥账户" || second.Action != importActionCreate {
		t.Fatalf("第二个账户项不一致：%+v", second)
	}

	// 代理改写：ref 序号化、type 归一化为小写；停用代理不进入文档。
	if len(result.Proxies) != 1 || result.Proxies[0].Ref == nil || *result.Proxies[0].Ref != "sub2api-proxy-1" {
		t.Fatalf("代理项不一致：%+v", result.Proxies)
	}

	// 跳过原因逐项落进来源消息（上限 8 条内全量展开）。
	text := w2SourceText(result)
	for _, expected := range []string{
		"引用的来源代理不存在",
		"引用的来源代理不可用或超过本次导入上限",
		"只支持 OpenAI 平台账户",
		"账户类型不是可导入的 API Key 或 OAuth",
		"缺少 credentials",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("来源消息缺少 %q：%s", expected, text)
		}
	}

	// 执行导入验证凭据转换落库：多 api_keys → failover 列表。
	executed, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api, ImportOptions{},
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport 错误：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 2 {
		t.Fatalf("执行汇总不一致：%+v", executed.Summary)
	}
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE name = '多密钥账户'`).Scan(&sealed); err != nil {
		t.Fatalf("多密钥账户未落库：%v", err)
	}
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("凭据解密失败：%v", err)
	}
	if credentials["api_key"] != "sk-a-1" {
		t.Fatalf("主密钥不一致：%v", credentials["api_key"])
	}
	keys, ok := credentials["api_keys"].([]any)
	if !ok || len(keys) != 2 || keys[0] != "sk-a-1" || keys[1] != "sk-a-2" {
		t.Fatalf("failover 密钥列表不一致（masked/重复/空白/非字符串应被丢弃）：%v", credentials["api_keys"])
	}
	if credentials["api_key_strategy"] != "failover" {
		t.Fatalf("密钥策略不一致：%v", credentials["api_key_strategy"])
	}
}

func TestW2Sub2APIAdapterOAuthAccounts(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	// OAuth 账户固定映射 gpt 供应商的 profile_gpt_openai_v1 档案；基础夹具
	// 只有 prof-gpt，这里补齐导入链引用的档案 id。
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-gpt-import', 'gpt', 'OpenAI', 1, '["gpt-4o-mini"]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('profile_gpt_openai_v1', 'gpt', 'OpenAI 官方协议', 1, 'openai', 'v1',
		'https://api.openai.com/v1', 'gpt-4o-mini', '["oauth"]', '[]', ?, ?)`, now, now)

	document := map[string]any{"accounts": []any{
		map[string]any{"name": "OAuth 账户", "platform": "chatgpt", "type": "OAuth",
			"credentials": map[string]any{"refresh_token": "rt-1", "access_token": "at-1",
				"account_id": "acc-1", "expires_at": "2026-06-01T00:00:00Z", "client_id": "cid",
				"id_token": "it", "token_type": "Bearer", "scope": "openid", "email": "a@b.c",
				"organization_id": "org", "chatgpt_user_id": "cu", "plan_type": "pro",
				"base_url": "https://api.openai.com/v1"}},
		map[string]any{"name": "缺 account_id 的 OAuth", "platform": "openai", "type": "oauth",
			"credentials": map[string]any{"access_token": "at-2"}},
		map[string]any{"name": "仅 refresh", "platform": "openai", "type": "oauth",
			"credentials": map[string]any{"refresh_token": "rt-3", "access_token": "orphan"}},
	}}
	result := w2PreviewSource(t, env, document, importSourceSub2Api, adminID)
	if result.Source.Records != 3 || result.Source.Accepted != 2 || result.Source.Skipped != 1 {
		t.Fatalf("来源摘要不一致：%+v", result.Source)
	}
	first := result.Accounts[0]
	// OAuth 账户固定映射到 gpt 供应商的 OpenAI v1 档案。
	if first.ProviderCode == nil || *first.ProviderCode != gptVendorCode ||
		first.ProviderProtocolProfileID == nil || *first.ProviderProtocolProfileID != gptOpenAIV1ProfileID ||
		first.AccountType == nil || *first.AccountType != "oauth" {
		t.Fatalf("OAuth 供应商映射不一致：%+v", first)
	}

	// 执行落库后校验凭据细节（仅 refresh 时 access_token 被丢弃）。
	_, err := env.store.ExecuteImport(context.Background(), document, importSourceSub2Api, ImportOptions{},
		AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("ExecuteImport 错误：%v", err)
	}
	var sealed string
	if err := env.db.QueryRow(`SELECT credentials_encrypted FROM accounts WHERE name = '仅 refresh'`).Scan(&sealed); err != nil {
		t.Fatalf("账户未落库：%v", err)
	}
	var credentials Credentials
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatalf("凭据解密失败：%v", err)
	}
	if _, exists := credentials["access_token"]; exists {
		t.Fatalf("无 account_id 时 access_token 应被丢弃：%v", credentials)
	}
	if credentials["refresh_token"] != "rt-3" {
		t.Fatalf("refresh_token 不一致：%v", credentials)
	}
}

func TestW2Sub2APIAdapterMalformedInputs(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	t.Run("字符串非 JSON", func(t *testing.T) {
		result := w2PreviewSource(t, env, "{broken", importSourceSub2Api, adminID)
		if !strings.Contains(w2SourceText(result), "Sub2API 导入内容必须是 JSON") {
			t.Fatalf("消息不一致：%s", w2SourceText(result))
		}
	})
	t.Run("根不是对象", func(t *testing.T) {
		result := w2PreviewSource(t, env, []any{1}, importSourceSub2Api, adminID)
		if !strings.Contains(w2SourceText(result), "来源导入内容必须是对象") {
			t.Fatalf("消息不一致：%s", w2SourceText(result))
		}
	})
	t.Run("缺 accounts 数组", func(t *testing.T) {
		result := w2PreviewSource(t, env, map[string]any{"proxies": []any{}}, importSourceSub2Api, adminID)
		if !strings.Contains(w2SourceText(result), "Sub2API 数据未包含 accounts 数组") {
			t.Fatalf("消息不一致：%s", w2SourceText(result))
		}
	})
	t.Run("账户条目非对象", func(t *testing.T) {
		result := w2PreviewSource(t, env, map[string]any{"accounts": []any{"x"}}, importSourceSub2Api, adminID)
		if !strings.Contains(w2SourceText(result), "账户不是对象") {
			t.Fatalf("消息不一致：%s", w2SourceText(result))
		}
	})
	t.Run("代理条目非对象计入忽略字段", func(t *testing.T) {
		result := w2PreviewSource(t, env, map[string]any{
			"accounts": []any{map[string]any{"name": "a", "platform": "openai", "type": "api_key",
				"credentials": map[string]any{"api_key": "sk-x"}}},
			"proxies": []any{"not-an-object"},
		}, importSourceSub2Api, adminID)
		if result.Source.IgnoredFields != 1 {
			t.Fatalf("忽略字段计数不一致：%d", result.Source.IgnoredFields)
		}
	})
	t.Run("内嵌 data 包装", func(t *testing.T) {
		inner := map[string]any{"accounts": []any{map[string]any{"name": "内嵌", "platform": "openai",
			"type": "api_key", "credentials": map[string]any{"api_key": "sk-x"}}}}
		result := w2PreviewSource(t, env, map[string]any{"data": inner}, importSourceSub2Api, adminID)
		if result.Source.Accepted != 1 || len(result.Accounts) != 1 || *result.Accounts[0].Name != "内嵌" {
			t.Fatalf("内嵌 data 未展开：%+v", result.Source)
		}
	})
}

func TestW2ChannelAdapterNewAPIAndOneAPI(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	document := []any{
		map[string]any{"id": float64(1), "type": float64(1), "key": "sk-chan-1", "name": "频道一",
			"base_url": "https://api.openai.com/v1", "group": "频道分组", "status": float64(1), "weight": float64(0)},
		map[string]any{"type": "OpenAI-Compatible", "key": "sk-chan-2\nsk-chan-3", "status": "enabled"},
		map[string]any{"type": float64(8), "key": "sk-x"},                                         // 非 OpenAI 类型
		map[string]any{"type": float64(1), "key": ""},                                             // 缺 key
		map[string]any{"type": float64(1), "key": "sk-a"},                                         // base_url 缺省走官方
		map[string]any{"type": float64(1), "key": "sk-b", "base_url": "http://127.0.0.1:8080/v1"}, // 私网拒绝
		map[string]any{"type": float64(1), "key": "sk-c", "group": "a,b"},                         // 非法分组名回退
		map[string]any{"type": float64(1), "key": "sk-d", "status": float64(0)},                   // 停用状态透传
		"not-an-object",
	}
	result := w2PreviewSource(t, env, document, importSourceNewAPI, adminID)
	if result.Source.Records != 9 {
		t.Fatalf("记录数不一致：%+v", result.Source)
	}
	if result.Source.Accepted != 5 || result.Source.Skipped != 4 {
		t.Fatalf("接受/跳过数不一致：%+v", result.Source)
	}
	// 忽略字段计数：weight（未知记录字段）+ 私网 base_url（上游地址策略拒绝）。
	if result.Source.IgnoredFields != 2 {
		t.Fatalf("忽略字段计数不一致：%d", result.Source.IgnoredFields)
	}

	names := map[string]bool{}
	for _, item := range result.Accounts {
		names[*item.Name] = true
		if item.ProviderCode == nil || *item.ProviderCode != openAICompatibleProvider {
			t.Fatalf("Channel 账户供应商映射不一致：%+v", item)
		}
	}
	if !names["频道一"] || !names["NewAPI Channel 2"] || !names["NewAPI Channel 5"] || !names["NewAPI Channel 7"] || !names["NewAPI Channel 8"] {
		t.Fatalf("账户名称集合不一致：%v", names)
	}
	text := w2SourceText(result)
	for _, expected := range []string{"Channel 不是该来源定义的 OpenAI 类型", "Channel 缺少 API Key", "Channel Base URL 不符合上游地址策略", "Channel 不是对象"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("来源消息缺少 %q：%s", expected, text)
		}
	}
	// One-API 模式：标签与默认分组名切换，支持 {data:[...]} 包装。
	wrapped := map[string]any{"data": []any{map[string]any{"type": float64(1), "key": "sk-one-1"}}}
	result = w2PreviewSource(t, env, wrapped, importSourceOneAPI, adminID)
	if result.Source.Accepted != 1 || *result.Accounts[0].Name != "One-API Channel 1" {
		t.Fatalf("One-API 默认命名不一致：%+v", result.Accounts)
	}

	// items 包装同样被识别。
	result = w2PreviewSource(t, env, map[string]any{"items": []any{map[string]any{"type": "openai", "key": "sk-one-2"}}},
		importSourceOneAPI, adminID)
	if result.Source.Accepted != 1 {
		t.Fatalf("items 包装未识别：%+v", result.Source)
	}

	// items 非数组时整体回退为单条记录，按未知 OpenAI 类型跳过。
	result = w2PreviewSource(t, env, map[string]any{"items": "x"}, importSourceNewAPI, adminID)
	if result.Source.Accepted != 0 || !strings.Contains(w2SourceText(result), "Channel 不是该来源定义的 OpenAI 类型") {
		t.Fatalf("消息不一致：%s", w2SourceText(result))
	}
	result = w2PreviewSource(t, env, "not-json", importSourceOneAPI, adminID)
	if !strings.Contains(w2SourceText(result), "One-API 导入内容必须是 JSON") {
		t.Fatalf("消息不一致：%s", w2SourceText(result))
	}
}
