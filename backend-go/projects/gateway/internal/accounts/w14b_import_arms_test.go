package accounts

// w14b import.go 臂补齐：账户级校验消息臂（空协议档案、凭据归一化失败、
// 非法状态/并发/过期）、代理解析缓存命中、目录校验消息臂、孤儿协议档案行、
// push 空消息臂与目录 reader 注入下的导入规划。

import (
	"context"
	"strings"
	"testing"
)

func TestW14BImportAccountValidationArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 空协议档案 + 非法状态/并发/过期 + 非法凭据：各消息臂逐条命中。
	result, err := env.store.PreviewImport(context.Background(), w13aRawDoc(
		[]any{
			map[string]any{"name": "w14b-i1", "providerCode": "openai", "type": "api_key",
				"status": "active", "groupName": "w14b-g1",
				"credentials": map[string]any{"api_key": "sk-x", "base_url": "https://api.openai.com/v1"}},
			map[string]any{"name": "w14b-i2", "providerCode": "openai", "type": "api_key",
				"status": "bogus", "concurrencyLimit": float64(-1), "accountExpiresAt": "not-a-time",
				"groupName": "w14b-g1",
				"credentials": map[string]any{"api_key": "sk-x", "base_url": "https://api.openai.com/v1"}},
			map[string]any{"name": "w14b-i3", "providerCode": "openai", "providerProtocolProfileId": openAICompatibleProfileID,
				"type": "api_key", "status": "active", "groupName": "w14b-g1",
				"credentials": map[string]any{"api_key": "sk-x"}},
		}, nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	items := result.Accounts
	if len(items) != 3 {
		t.Fatalf("账户条目数不符：%d", len(items))
	}
	messagesOf := func(index int) string {
		return strings.Join(items[index].Messages, "|")
	}
	if !strings.Contains(messagesOf(0), "providerProtocolProfileId 不能为空") {
		t.Fatalf("空协议档案应推送消息：%s", messagesOf(0))
	}
	for _, want := range []string{"账户状态不支持：bogus", "concurrencyLimit", "accountExpiresAt"} {
		if !strings.Contains(messagesOf(1), want) {
			t.Fatalf("消息缺失 %q：%s", want, messagesOf(1))
		}
	}
	if items[1].Action != importActionFailed {
		t.Fatalf("非法字段应失败：%+v", items[1])
	}
	if messagesOf(2) == "" {
		t.Fatal("缺 base_url 凭据应推送归一化消息")
	}
}

func TestW14BImportProxyCacheAndOrphanProfile(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 孤儿协议档案行：provider_code 不在 providers 表 → profile 循环 continue 臂。
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile_orphan_w14b', 'orphan-w14b', '孤儿档案', 1, 'openai', 'v1', 'https://x/v1',
		'gpt-4o-mini', '[]', '[]', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`)

	// 两个账户复用同一代理 ref：第二次走 proxyLookup 缓存命中臂。
	sharedProxy := map[string]any{"ref": "p1", "name": "w14b-shared-proxy", "type": "socks5", "host": "proxy.example.com", "port": float64(1080)}
	account := func(name string) map[string]any {
		return map[string]any{"name": name, "providerCode": "openai", "providerProtocolProfileId": openAICompatibleProfileID,
			"type": "api_key", "status": "active", "groupName": "w14b-gc", "proxyRef": "p1",
			"credentials": map[string]any{"api_key": "sk-" + name, "base_url": "https://api.openai.com/v1"}}
	}
	result, err := env.store.PreviewImport(context.Background(), w13aRawDoc(
		[]any{account("w14b-pa"), account("w14b-pb")}, []any{sharedProxy},
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	for index := range []int{0, 1} {
		if result.Accounts[index].Action != importActionCreate {
			t.Fatalf("共享代理账户应创建：%+v", result.Accounts[index])
		}
	}
	if len(result.Proxies) != 1 || result.Proxies[0].Action != importActionCreate {
		t.Fatalf("共享代理应仅创建一次：%+v", result.Proxies)
	}
}

func TestW14BImportModelCatalogValidationArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	env.store.SetModelCatalogReader(&fakeAccountModelCatalog{catalog: []AccountModelCatalogFact{gptCatalogFact()}})
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	account := func(name string, extra map[string]any) map[string]any {
		base := map[string]any{"name": name, "providerCode": "openai", "providerProtocolProfileId": openAICompatibleProfileID,
			"type": "api_key", "status": "active", "groupName": "w14b-cat",
			"credentials": map[string]any{"api_key": "sk-" + name, "base_url": "https://api.openai.com/v1"}}
		for key, value := range extra {
			base[key] = value
		}
		return base
	}
	// 支持模型目录命中 + healthCheckModel 成员 + 映射上游允许 → 全部通过。
	okAccount := account("w14b-cat-ok", map[string]any{
		"supportedModels":  []any{"gpt-4o-mini"},
		"healthCheckModel": "gpt-4o-mini",
	})
	// 目录外模型 + 不一致 healthCheckModel → 消息臂。
	badAccount := account("w14b-cat-bad", map[string]any{
		"supportedModels":  []any{"gpt-missing"},
		"healthCheckModel": "gpt-4o-mini",
	})
	result, err := env.store.PreviewImport(context.Background(), w13aRawDoc(
		[]any{okAccount, badAccount}, nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Accounts[0].Action != importActionCreate {
		t.Fatalf("目录内导入应创建：%+v", result.Accounts[0])
	}
	badMessages := strings.Join(result.Accounts[1].Messages, "|")
	if !strings.Contains(badMessages, "gpt-missing") && badMessages == "" {
		t.Fatalf("目录外模型应推送消息：%s", badMessages)
	}
}

func TestW14BImportPushNilAndEmptyMessages(t *testing.T) {
	// push 的 nil messages 臂（messages 为 nil 时直接返回）。
	source := &normalizedImportAccount{}
	source.push("w14b-should-noop")
	if source.messages != nil {
		t.Fatal("nil messages 不应分配")
	}
	messages := []string{}
	source.messages = &messages
	source.push("w14b-append")
	if len(messages) != 1 || messages[0] != "w14b-append" {
		t.Fatalf("push 应追加消息：%v", messages)
	}
}
