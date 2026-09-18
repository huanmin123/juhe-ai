package accounts

// w14b 手动测试选项/会话臂补齐：test_options_service 的目录查询（内置/自定义、
// 关键字过滤、selected 置顶、目录胜者比较）与 test_store 的会话读写/心跳/完成/
// 权限过滤、结果 Unmarshal、pg 布尔字面量等剩余分支。

import (
	"context"
	"database/sql"
	"testing"
)

const w14bTestCatalogDDL = `
CREATE TABLE IF NOT EXISTS provider_model_catalog (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider_code TEXT NOT NULL,
	model TEXT NOT NULL,
	mode TEXT,
	release_date TEXT,
	supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
	catalog_order INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'active',
	catalog_visible INTEGER NOT NULL DEFAULT 1,
	shutdown_date TEXT
);
CREATE TABLE IF NOT EXISTS custom_provider_models (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	provider_code TEXT NOT NULL,
	model TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT 'global',
	system_account_id TEXT,
	mode TEXT,
	release_date TEXT,
	supported_api_protocols_json TEXT NOT NULL DEFAULT '[]',
	status TEXT NOT NULL DEFAULT 'active',
	shutdown_date TEXT
);
`

const w14bTestSessionDDL = `
CREATE TABLE IF NOT EXISTS account_test_sessions (
	id TEXT PRIMARY KEY,
	request_system_account_id TEXT NOT NULL,
	request_role TEXT NOT NULL,
	request_system_account_filter_id TEXT,
	status TEXT NOT NULL,
	cancel_reason TEXT,
	last_heartbeat_at TEXT,
	cancel_requested_at TEXT,
	finished_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_test_session_tasks (
	session_id TEXT NOT NULL,
	task_id TEXT NOT NULL,
	created_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, task_id)
);
CREATE TABLE IF NOT EXISTS account_test_tasks (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	account_name TEXT,
	provider_code TEXT,
	provider_protocol_profile_id TEXT,
	protocol_code TEXT,
	protocol_version TEXT,
	account_type TEXT,
	request_system_account_id TEXT,
	request_role TEXT,
	request_system_account_filter_id TEXT,
	diagnostics TEXT,
	draft_account_encrypted TEXT,
	status TEXT NOT NULL DEFAULT 'queued',
	status_message TEXT,
	model TEXT,
	test_endpoint_mode TEXT,
	result_json TEXT,
	cancel_requested INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL DEFAULT '',
	queued_at TEXT NOT NULL DEFAULT '',
	queued_deadline_at TEXT,
	started_at TEXT,
	finished_at TEXT,
	updated_at TEXT NOT NULL DEFAULT '',
	error_message TEXT
);
`

func w14bSeedTestAccount(t *testing.T, env *testEnv, id, name string) string {
	t.Helper()
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, id, adminID, name, "active")
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-" + id, "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ?, protocol_code = 'openai', protocol_version = 'v1' WHERE id = ?`, sealed, id)
	return adminID
}

func TestW14BManualTestOptionsFlow(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestCatalogDDL)
	adminID := w14bSeedTestAccount(t, env, "acc-w14b-opt", "w14b-opt")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()

	// 内置目录：两条 openai 模型（一条含发布日期）。
	env.exec(t, `INSERT INTO provider_model_catalog (provider_code, model, mode, release_date, supported_api_protocols_json, status, catalog_visible)
		VALUES ('gpt', 'gpt-4o-mini', NULL, '2024-07-18', '["chat_completions","responses"]', 'active', 1),
		       ('gpt', 'gpt-old', NULL, NULL, '[]', 'active', 1)`)
	// 自定义目录：全局 + 个人 scope，同名模型两条不同发布日期（胜者比较臂）。
	env.exec(t, `INSERT INTO custom_provider_models (provider_code, model, scope, system_account_id, mode, release_date, supported_api_protocols_json, status)
		VALUES ('gpt', 'custom-a', 'global', NULL, NULL, '2024-01-01', '["chat_completions"]', 'active'),
		       ('gpt', 'custom-a', 'personal', ?, NULL, '2024-06-01', '["chat_completions"]', 'active'),
		       ('gpt', 'custom-old', 'global', NULL, NULL, NULL, '[]', 'retired')`, adminID)

	// 空 accountID → nil, nil。
	if got, err := env.store.FindManualTestOptionsContext(ctx, "", &scope); err != nil || got != nil {
		t.Fatalf("空账户应返回 nil：%v %v", got, err)
	}
	got, err := env.store.FindManualTestOptionsContext(ctx, "acc-w14b-opt", &scope)
	if err != nil || got == nil {
		t.Fatalf("测试上下文应返回：%v %v", got, err)
	}
	options, err := env.store.AccountManualTestOptions(ctx, got, ManualTestOptionsQuery{Limit: 10})
	if err != nil {
		t.Fatalf("选项查询应成功：%v", err)
	}
	if len(options) < 3 {
		t.Fatalf("内置+自定义选项应齐全：%d", len(options))
	}
	// keyword 过滤 + selected 置顶。
	filtered, err := env.store.AccountManualTestOptions(ctx, got, ManualTestOptionsQuery{Keyword: "custom", Limit: 5, SelectedIDs: []string{"custom-a"}})
	if err != nil {
		t.Fatalf("过滤查询应成功：%v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("selected+keyword 过滤应保留两条：%+v", filtered)
	}
	for _, option := range filtered {
		if option.ID == "gpt-old" {
			t.Fatalf("keyword 过滤应排除未选中的非匹配模型：%+v", filtered)
		}
	}
	// 能力查询：内置模型命中；未收录模型报错。
	capabilities, err := env.store.AccountManualTestModelCapabilities(ctx, got, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("能力查询应成功：%v", err)
	}
	if capabilities.ID != "gpt-4o-mini" || len(capabilities.TestEndpointModes) == 0 {
		t.Fatalf("能力回显不符：%+v", capabilities)
	}
	if _, err := env.store.AccountManualTestModelCapabilities(ctx, got, "  "); err == nil {
		t.Fatal("空模型应拒绝")
	}
	if _, err := env.store.AccountManualTestModelCapabilities(ctx, got, "gpt-not-in-catalog"); err == nil {
		t.Fatal("目录外模型应拒绝")
	}
	model, mode, err := env.store.ResolveAccountManualTestSelection(ctx, got, "gpt-4o-mini", "")
	if err != nil || model != "gpt-4o-mini" || mode == "" {
		t.Fatalf("选择解析应成功：%s %s %v", model, mode, err)
	}
	if _, _, err := env.store.ResolveAccountManualTestSelection(ctx, got, "gpt-4o-mini", "bogus_mode"); err == nil {
		t.Fatal("非法形态应拒绝")
	}
	// 损坏密文 → 上下文解密失败臂（nil, nil）。
	env.exec(t, `UPDATE accounts SET credentials_encrypted = 'corrupted-w14b-opt' WHERE id = 'acc-w14b-opt'`)
	if got, err := env.store.FindManualTestOptionsContext(ctx, "acc-w14b-opt", &scope); err != nil || got != nil {
		t.Fatalf("损坏密文应返回 nil：%v %v", got, err)
	}
	if got, err := env.store.FindManualTestCapabilitiesContext(ctx, "acc-w14b-opt", "gpt-4o-mini", &scope); err != nil || got != nil {
		t.Fatalf("损坏密文能力上下文应返回 nil：%v %v", got, err)
	}

	// pg 字面量分支（直接构造 pg 视角 Store）。
	pgStore := &Store{pg: true}
	if testTodayText(pgStore) != "CURRENT_DATE::text" || testTodayText(env.store) != "date('now')" {
		t.Fatal("今日字面量分支不符")
	}
	if pgStore.boolTrueLiteral() != "TRUE" || pgStore.boolFalseLiteral() != "FALSE" ||
		env.store.boolTrueLiteral() != "1" || env.store.boolFalseLiteral() != "0" {
		t.Fatal("pg 布尔字面量分支不符")
	}
	if normalizeTestProviderCodeList([]string{" openai ", "", "openai", "OPENAI"})[0] != "openai" {
		t.Fatal("provider code 归一化应去重")
	}
	if got := normalizeTestProviderCodeList([]string{"  "}); len(got) != 0 {
		t.Fatalf("空白列表应为空：%v", got)
	}
}

func TestW14BTestSessionStoreArms(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestSessionDDL)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	adminScope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()

	// 空 viewer 拒绝。
	if _, err := env.store.CreateTestSession(ctx, AccessScope{}); err == nil {
		t.Fatal("空 viewer 应拒绝")
	}
	session, err := env.store.CreateTestSession(ctx, adminScope)
	if err != nil || session == nil {
		t.Fatalf("会话创建应成功：%v %v", session, err)
	}
	// 读取 + 详情（无任务）。
	got, err := env.store.GetTestSession(ctx, session.ID, &adminScope)
	if err != nil || got == nil || got.ID != session.ID {
		t.Fatalf("会话读取应成功：%v %v", got, err)
	}
	detail, tasks, err := env.store.GetTestSessionDetail(ctx, session.ID, &adminScope)
	if err != nil || detail == nil || tasks == nil {
		t.Fatalf("会话详情应成功：%v %v %v", detail, tasks, err)
	}
	// 心跳（running → 刷新）+ 完成。
	beat, err := env.store.HeartbeatTestSession(ctx, session.ID, &adminScope)
	if err != nil || beat == nil {
		t.Fatalf("心跳应成功：%v %v", beat, err)
	}
	completed, err := env.store.CompleteTestSession(ctx, session.ID, &adminScope)
	if err != nil || completed == nil {
		t.Fatalf("完成应成功：%v %v", completed, err)
	}
	if completed.Status != TestSessionCompleted {
		t.Fatalf("会话应完成：%s", completed.Status)
	}
	// 空 id / 权限不匹配 → nil。
	if got, err := env.store.GetTestSession(ctx, "  ", &adminScope); err != nil || got != nil {
		t.Fatalf("空会话 id 应返回 nil：%v %v", got, err)
	}
	if got, err := env.store.GetTestSession(ctx, session.ID, nil); err != nil || got == nil {
		t.Fatalf("nil scope 允许读取：%v %v", got, err)
	}
	userID := env.login(t, "alice-w14b-sess", "alice-pass", "user")
	if got, err := env.store.GetTestSession(ctx, session.ID, &AccessScope{ViewerID: userID}); err != nil || got != nil {
		t.Fatalf("跨用户读取应拒绝：%v %v", got, err)
	}
	if got, tasks, err := env.store.GetTestSessionDetail(ctx, session.ID, &AccessScope{ViewerID: userID}); err != nil || got != nil || tasks != nil {
		t.Fatalf("跨用户详情应拒绝：%v %v %v", got, tasks, err)
	}
	// 管理员 filter 过滤：带 filter 的会话对其他 filter 不可见。
	filteredSession, err := env.store.CreateTestSession(ctx, AccessScope{ViewerID: adminID, IsAdmin: true, FilterID: adminID})
	if err != nil || filteredSession == nil {
		t.Fatalf("带 filter 会话创建应成功：%v %v", filteredSession, err)
	}
	if got, err := env.store.GetTestSession(ctx, filteredSession.ID, &AccessScope{ViewerID: adminID, IsAdmin: true, FilterID: "other-w14b"}); err != nil || got != nil {
		t.Fatalf("filter 不匹配应拒绝：%v %v", got, err)
	}
	if got, err := env.store.GetTestSession(ctx, filteredSession.ID, &AccessScope{ViewerID: adminID, IsAdmin: true, FilterID: adminID}); err != nil || got == nil {
		t.Fatalf("filter 匹配应可读：%v %v", got, err)
	}
	// 任务结果反序列化臂。
	var results AccountTestResult
	if err := results.UnmarshalJSON([]byte("not-json")); err == nil {
		t.Fatal("非 JSON 应拒绝")
	}
	if err := results.UnmarshalJSON([]byte(`{"accountId":"a"}`)); err == nil {
		t.Fatal("缺 message 应拒绝")
	}
	if err := results.UnmarshalJSON([]byte(`{"accountId":"a","message":"ok"}`)); err != nil {
		t.Fatalf("合法结果应通过：%v", err)
	}
	// testEndpointModeOrNull 非法值臂。
	if got := testEndpointModeOrNull(sql.NullString{String: "bogus", Valid: true}); got != nil {
		t.Fatalf("非法形态应返回 nil：%v", *got)
	}
	if got := testEndpointModeOrNull(sql.NullString{String: "count_tokens", Valid: true}); got == nil || *got != "count_tokens" {
		t.Fatalf("合法形态应返回：%v", got)
	}
}
