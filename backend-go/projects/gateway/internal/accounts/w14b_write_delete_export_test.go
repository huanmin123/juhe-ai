package accounts

// w14b write.go / delete.go / export.go 臂补齐：Create 的供应商/档案/凭据/
// 调度/代理/分组校验臂、健康型账户删除的墓碑入队链、导出的过滤/实例跳过/
// 代理引用/超级优先回显等剩余分支。

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func w14bSeedCreateProvider(t *testing.T, env *testEnv) {
	t.Helper()
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-w14b-dis', 'w14bdis', '停用供应商', 0, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile-w14b-dis', 'w14bdis', '停用档案', 0, 'openai', 'v1', 'https://x/v1',
		'gpt-4o-mini', '["api_key"]', '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO providers (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
		VALUES ('prov-w14b-live', 'w14blive', '启用供应商', 1, '[]', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled, protocol_code,
		protocol_version, base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at)
		VALUES ('profile-w14b-live-dis', 'w14blive', '启用供应商停用档案', 0, 'openai', 'v1', 'https://x/v1',
		'gpt-4o-mini', '["api_key"]', '[]', ?, ?)`, now, now)
}

func w14bValidCreateInput() CreateInput {
	return CreateInput{
		ProviderCode: "openai", ProviderProtocolProfileID: openAICompatibleProfileID,
		Name: "w14b-create", AccountType: "api_key",
		Credentials:     Credentials{"api_key": "sk-w14b-create-123", "base_url": "https://api.openai.com/v1"},
		SupportedModels: []string{"gpt-4o-mini"},
	}
}

func TestW14BCreateValidationArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	w14bSeedCreateProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()
	// openai 供应商的启用默认分组（默认分组解析按 owner+provider 命中）。
	nowSeed := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, is_default, enabled, group_type, created_at, updated_at)
		VALUES ('grp-w14b-openai-default', ?, 'w14b-openai-default', 'openai', 1, 1, 'personal', ?, ?)`, adminID, nowSeed, nowSeed)

	// 供应商不存在 / 停用。
	if _, err := env.store.Create(ctx, CreateInput{ProviderCode: "bogus", AccountType: "api_key"}, scope); err == nil || !strings.Contains(err.Error(), "不支持的供应商") {
		t.Fatalf("未知供应商应拒绝：%v", err)
	}
	if _, err := env.store.Create(ctx, CreateInput{ProviderCode: "w14bdis", ProviderProtocolProfileID: "profile-w14b-dis", AccountType: "api_key"}, scope); err == nil || !strings.Contains(err.Error(), "供应商已停用") {
		t.Fatalf("停用供应商应拒绝：%v", err)
	}
	// 档案：为空 / 不存在 / 停用。
	valid := w14bValidCreateInput()
	if _, err := env.store.Create(ctx, CreateInput{ProviderCode: valid.ProviderCode, AccountType: valid.AccountType}, scope); err == nil || !strings.Contains(err.Error(), "供应商协议档案不能为空") {
		t.Fatalf("空档案应拒绝：%v", err)
	}
	if _, err := env.store.Create(ctx, CreateInput{ProviderCode: valid.ProviderCode, ProviderProtocolProfileID: "profile-nope", AccountType: valid.AccountType}, scope); err == nil || !strings.Contains(err.Error(), "供应商协议档案无效") {
		t.Fatalf("未知档案应拒绝：%v", err)
	}
	if _, err := env.store.Create(ctx, CreateInput{ProviderCode: "w14blive", ProviderProtocolProfileID: "profile-w14b-live-dis", AccountType: valid.AccountType}, scope); err == nil || !strings.Contains(err.Error(), "供应商协议档案已停用") {
		t.Fatalf("停用档案应拒绝：%v", err)
	}

	// 档案账户类型不支持 / 健康检查形态非法。
	badType := valid
	badType.AccountType = "google_oauth"
	if _, err := env.store.Create(ctx, badType, scope); err == nil {
		t.Fatal("档案未注册账户类型应拒绝")
	}
	badMode := "responses_bogus"
	badModeInput := valid
	badModeInput.HealthCheckEndpointMode = &badMode
	if _, err := env.store.Create(ctx, badModeInput, scope); err == nil || !strings.Contains(err.Error(), "账户参数无效") {
		t.Fatalf("非法检查形态应拒绝：%v", err)
	}

	// 凭据来源缺失（api_key 无 api_key）。
	noKey := valid
	noKey.Credentials = Credentials{"base_url": "https://api.openai.com/v1"}
	if _, err := env.store.Create(ctx, noKey, scope); err == nil {
		t.Fatal("缺 api_key 应拒绝")
	}

	// 非法可用性时间计划。
	badSchedule := valid
	badSchedule.AvailabilitySchedule = map[string]any{"enabled": "x"}
	if _, err := env.store.Create(ctx, badSchedule, scope); err == nil {
		t.Fatal("非法时间计划应拒绝")
	}

	// 并发/优先级越界。
	lowConcurrency := 0
	concurrencyInput := valid
	concurrencyInput.ConcurrencyLimit = &lowConcurrency
	if _, err := env.store.Create(ctx, concurrencyInput, scope); err == nil || !strings.Contains(err.Error(), "并发限制必须") {
		t.Fatalf("并发 0 应拒绝：%v", err)
	}
	negativePriority := -1
	priorityInput := valid
	priorityInput.Priority = &negativePriority
	if _, err := env.store.Create(ctx, priorityInput, scope); err == nil || !strings.Contains(err.Error(), "优先级必须") {
		t.Fatalf("负优先级应拒绝：%v", err)
	}

	// 代理不存在。
	missingProxy := "proxy-nope"
	proxyInput := valid
	proxyInput.ProxyProfileID = &missingProxy
	if _, err := env.store.Create(ctx, proxyInput, scope); err == nil || !strings.Contains(err.Error(), "代理不存在或已停用") {
		t.Fatalf("缺失代理应拒绝：%v", err)
	}

	// 支持模型为空 / 检查模型不在支持集合。
	emptyModels := valid
	emptyModels.SupportedModels = nil
	if _, err := env.store.Create(ctx, emptyModels, scope); err == nil || !strings.Contains(err.Error(), "支持模型") {
		t.Fatalf("空支持模型应拒绝：%v", err)
	}
	emptyHealth := ""
	healthInput := valid
	healthInput.HealthCheckModel = &emptyHealth
	if _, err := env.store.Create(ctx, healthInput, scope); err == nil || !strings.Contains(err.Error(), "检查模型不能为空") {
		t.Fatalf("空检查模型应拒绝：%v", err)
	}
	otherHealth := "gpt-not-in-list"
	healthInput.HealthCheckModel = &otherHealth
	if _, err := env.store.Create(ctx, healthInput, scope); err == nil || !strings.Contains(err.Error(), "检查模型") {
		t.Fatalf("越界检查模型应拒绝：%v", err)
	}

	// 显式分组不存在 / 供应商不一致。
	missingGroup := "grp-nope"
	groupInput := valid
	groupInput.GroupID = &missingGroup
	if _, err := env.store.Create(ctx, groupInput, scope); err == nil {
		t.Fatal("缺失分组应拒绝")
	}
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at)
		VALUES ('grp-w14b-other', ?, 'w14b-other-group', 'other-prov', ?, ?)`, adminID, now, now)
	otherGroup := "grp-w14b-other"
	groupInput.GroupID = &otherGroup
	if _, err := env.store.Create(ctx, groupInput, scope); err == nil || !strings.Contains(err.Error(), "账户分组无效") {
		t.Fatalf("跨供应商分组应拒绝：%v", err)
	}

	// 空 owner scope。
	if _, err := env.store.Create(ctx, valid, AccessScope{}); err == nil || !strings.Contains(err.Error(), "缺少系统账户上下文") {
		t.Fatalf("空 scope 应拒绝：%v", err)
	}

	// 成功路径（覆盖代理解析 + 过期 + 备注 + 超级优先臂）。
	enabledProxy := "proxy-w14b-on"
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES (?, ?, 'w14b-on-proxy', 'http', '127.0.0.1', 7890, 1, ?, ?)`, enabledProxy, adminID, now, now)
	expires := "2031-01-01T00:00:00Z"
	notes := "w14b 创建备注"
	success := valid
	success.ProxyProfileID = &enabledProxy
	success.AccountExpiresAt = &expires
	success.Notes = &notes
	super := true
	success.SuperPriorityEnabled = &super
	success.Status = CreationStatus{Status: "active", SkipInitialHealthCheck: true}
	created, err := env.store.Create(ctx, success, scope)
	if err != nil {
		t.Fatalf("合法创建应成功：%v", err)
	}
	if got := env.queryCell(t, `SELECT proxy_profile_id FROM accounts WHERE id = ?`, created.ID); got != enabledProxy {
		t.Fatalf("代理应绑定：%s", got)
	}
	if env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND super_priority_enabled = 1`, created.ID) != 1 {
		t.Fatal("超级优先应落库")
	}

	// 删除该账户：健康型账户走墓碑入队链。
	deleted, err := env.store.Delete(ctx, created.ID, scope)
	if err != nil || !deleted {
		t.Fatalf("删除应成功：%v %v", deleted, err)
	}
	if env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ? AND event_kind = 'tombstone'`, created.ID) != 1 {
		t.Fatal("删除应写入健康墓碑事件")
	}
	// 再次删除：已删除账户返回 false。
	deleted, err = env.store.Delete(ctx, created.ID, scope)
	if err != nil || deleted {
		t.Fatalf("重复删除应返回 false：%v %v", deleted, err)
	}
	if deleted, err := env.store.Delete(ctx, "", scope); err != nil || deleted {
		t.Fatalf("空 id 删除应返回 false,nil：%v %v", deleted, err)
	}
}

func TestW14BExportAccountsArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()

	if _, err := env.store.ExportAccounts(ctx, ExportOptions{AccountIDs: []string{}}, scope); err == nil || !strings.Contains(err.Error(), "请选择要导出的") {
		t.Fatalf("空列表应拒绝：%v", err)
	}
	tooMany := make([]string, accountExportMaxAccounts+1)
	for i := range tooMany {
		tooMany[i] = "acc-w14b-over-" + strconv.Itoa(i)
	}
	if _, err := env.store.ExportAccounts(ctx, ExportOptions{AccountIDs: tooMany}, scope); err == nil || !strings.Contains(err.Error(), "单次最多导出") {
		t.Fatalf("超上限应拒绝：%v", err)
	}

	now := "2026-09-17T00:00:00.000Z"
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w14b-exp", "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w14b-exp', ?, 'w14b-exp-proxy', 'socks5', 'h', 1080, 1, ?, ?)`, adminID, now, now)
	// 正常账户：超级优先 + 降级备用 + 代理 + 分组绑定 + 备注。
	env.seedAccount(t, "acc-w14b-exp-a", adminID, "w14b-exp-a", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ?, super_priority_enabled = 1, fallback_enabled = 1,
		proxy_profile_id = 'proxy-w14b-exp', notes = 'w14b 导出备注', priority = 3 WHERE id = 'acc-w14b-exp-a'`, sealed)
	env.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, created_at, updated_at)
		VALUES (?, 'grp-w14b-exp', 'acc-w14b-exp-a', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at)
		VALUES ('grp-w14b-exp', ?, 'w14b-exp-group', 'openai', ?, ?)`, adminID, now, now)
	// 授权实例账户（导出应跳过）+ 损坏密文账户。
	env.seedAccount(t, "acc-w14b-exp-inst", adminID, "w14b-exp-inst", "active")
	env.exec(t, `UPDATE accounts SET authorization_instance_authorization_id = 'authz-w14b' WHERE id = 'acc-w14b-exp-inst'`)
	env.seedAccount(t, "acc-w14b-exp-bad", adminID, "w14b-exp-bad", "active")
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'corrupted-w14b' WHERE id = 'acc-w14b-exp-bad'`)

	result, err := env.store.ExportAccounts(ctx, ExportOptions{AccountIDs: []string{"acc-w14b-exp-a", "acc-w14b-exp-inst"}}, scope)
	if err != nil {
		t.Fatalf("导出应成功：%v", err)
	}
	if len(result.Document.Accounts) != 1 {
		t.Fatalf("授权实例应被跳过：%+v", result.Document.Accounts)
	}
	exported := result.Document.Accounts[0]
	if exported.SuperPriorityEnabled == nil || !*exported.SuperPriorityEnabled || exported.FallbackEnabled == nil || !*exported.FallbackEnabled {
		t.Fatalf("超级优先/降级应回显：%+v", exported)
	}
	if exported.ProxyRef == nil {
		t.Fatalf("代理引用应回显：%+v", exported)
	}
	if exported.GroupName == nil || *exported.GroupName != "w14b-exp-group" {
		t.Fatalf("分组名应回显：%+v", exported)
	}

	// 损坏密文账户导出报错。
	if _, err := env.store.ExportAccounts(ctx, ExportOptions{AccountIDs: []string{"acc-w14b-exp-bad"}}, scope); err == nil {
		t.Fatal("损坏密文应报错")
	}
}
