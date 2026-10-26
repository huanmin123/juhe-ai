package accounts

// w14b 手动测试选项/会话臂补齐：test_options_service 的目录查询（内置/自定义、
// 关键字过滤、selected 置顶、目录胜者比较）与 test_store 的会话读写/心跳/完成/
// 权限过滤、结果 Unmarshal、pg 布尔字面量等剩余分支。

import (
	"context"
	"database/sql"
	"testing"
)

// B4 ⑧：目录 fixture 随读取面切 models 单表（provider_model_catalog/
// custom_provider_models 旧表 DDL 已拆除）。builtin 行按 seedFixtureModels 的
// 同名 id REPLACE 覆盖（精确保控该模型名的目录行，避免种子全集 mode 行漏进
// 判定）；custom 行落 global/personal scope（旧表行等价形态）。
const w14bTestCatalogRows = `
INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
	supported_endpoint_modes_json, release_date, catalog_visible, created_at, updated_at)
VALUES ('mod-fixture-gpt-4o-mini-gpt', 'gpt', 'gpt-4o-mini', 'gpt-4o-mini', 'builtin', NULL, 'active',
	'["chat_json","chat_sse","responses_json","responses_sse"]', '2024-07-18', true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
       ('mdl-b4-w14b-gpt-old', 'gpt', 'gpt-old', 'gpt-old', 'builtin', NULL, 'active',
	'[]', NULL, true, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
			ON CONFLICT (id) DO UPDATE SET vendor_code = EXCLUDED.vendor_code, public_model = EXCLUDED.public_model, upstream_model = EXCLUDED.upstream_model, scope = EXCLUDED.scope, system_account_id = EXCLUDED.system_account_id, status = EXCLUDED.status, supported_endpoint_modes_json = EXCLUDED.supported_endpoint_modes_json, release_date = EXCLUDED.release_date, shutdown_date = EXCLUDED.shutdown_date, catalog_visible = EXCLUDED.catalog_visible, catalog_order = EXCLUDED.catalog_order, created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at;
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
	cancel_requested boolean NOT NULL DEFAULT false,
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
	env.exec(t, w14bTestCatalogRows)
	adminID := w14bSeedTestAccount(t, env, "acc-w14b-opt", "w14b-opt")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()

	// 自定义目录：全局 + 个人 scope，同名模型两条不同发布日期（胜者比较臂）；
	// retired 行不进候选。
	env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
		supported_endpoint_modes_json, release_date, created_at, updated_at)
		VALUES ('mdl-b4-w14b-custom-a-global', 'gpt', 'custom-a', 'custom-a', 'global', NULL, 'active',
			'["chat_json","chat_sse"]', '2024-01-01', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		       ('mdl-b4-w14b-custom-a-personal', 'gpt', 'custom-a', 'custom-a', 'personal', ?, 'active',
			'["chat_json","chat_sse"]', '2024-06-01', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		       ('mdl-b4-w14b-custom-old', 'gpt', 'custom-old', 'custom-old', 'global', NULL, 'retired',
			'[]', NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
			ON CONFLICT (id) DO UPDATE SET vendor_code = EXCLUDED.vendor_code, public_model = EXCLUDED.public_model, upstream_model = EXCLUDED.upstream_model, scope = EXCLUDED.scope, system_account_id = EXCLUDED.system_account_id, status = EXCLUDED.status, supported_endpoint_modes_json = EXCLUDED.supported_endpoint_modes_json, release_date = EXCLUDED.release_date, shutdown_date = EXCLUDED.shutdown_date, catalog_visible = EXCLUDED.catalog_visible, catalog_order = EXCLUDED.catalog_order, created_at = EXCLUDED.created_at, updated_at = EXCLUDED.updated_at`, adminID)

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

	// 方言字面量（PG 单态收口，双方言分支断言随 sqlite 臂退役）。
	if testTodayText(env.store) != "CURRENT_DATE::text" {
		t.Fatal("今日字面量不符")
	}
	if env.store.boolTrueLiteral() != "TRUE" || env.store.boolFalseLiteral() != "FALSE" {
		t.Fatal("布尔字面量不符")
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
