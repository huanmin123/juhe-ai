package accounts

// w14b 测试任务取消/失败链、映射目标模型能力解析、散点纯函数臂
// （rulePriority/stringContextField/锁表引用/组统计脏标记/actor 匿名）。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW14BTestTaskCancelFailArms(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestSessionDDL)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()
	env.seedAccount(t, "acc-w14b-tc", adminID, "w14b-tc", "active")

	session, err := env.store.CreateTestSession(ctx, scope)
	if err != nil || session == nil {
		t.Fatalf("会话创建应成功：%v", err)
	}
	task, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w14b-tc", AccountName: "w14b-tc", ProviderCode: "gpt",
		ProviderProtocolProfileID: "prof-gpt", ProtocolCode: "openai", ProtocolVersion: "v1",
		AccountType: "api_key", Access: scope, Diagnostics: "full", SessionID: session.ID,
	})
	if err != nil || task == nil {
		t.Fatalf("任务创建应成功：%v %v", task, err)
	}
	// queued → 取消（queued 分支）。
	canceled, err := env.store.CancelTestTask(ctx, task.ID, &scope)
	if err != nil || canceled == nil || canceled.Status != TestTaskCanceled {
		t.Fatalf("queued 任务应被取消：%+v %v", canceled, err)
	}
	// finished 任务取消无动作（status switch 默认分支）。
	again, err := env.store.CancelTestTask(ctx, task.ID, &scope)
	if err != nil || again == nil {
		t.Fatalf("已完成任务取消应安全：%v %v", again, err)
	}
	// running 任务 → cancel_requested 分支。
	task2, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w14b-tc", Access: scope,
	})
	if err != nil || task2 == nil {
		t.Fatalf("第二任务应创建：%v", err)
	}
	env.exec(t, `UPDATE account_test_tasks SET status = 'running' WHERE id = ?`, task2.ID)
	runningCanceled, err := env.store.CancelTestTask(ctx, task2.ID, &scope)
	if err != nil || runningCanceled == nil {
		t.Fatalf("running 取消应成功：%v %v", runningCanceled, err)
	}
	// FailTestTask。
	if err := env.store.FailTestTask(ctx, task2.ID, "w14b 失败"); err != nil {
		t.Fatalf("失败标记应成功：%v", err)
	}
	if err := env.store.FailTestTask(ctx, "", "noop"); err != nil {
		t.Fatalf("空 id 失败标记应安全：%v", err)
	}
	// 缺失/无权限任务。
	if got, err := env.store.CancelTestTask(ctx, "accttest-none", &scope); err != nil || got != nil {
		t.Fatalf("缺失任务取消应返回 nil：%v %v", got, err)
	}
	userID := env.login(t, "alice-w14b-tc", "alice-pass", "user")
	if got, err := env.store.CancelTestTask(ctx, task.ID, &AccessScope{ViewerID: userID}); err != nil || got != nil {
		t.Fatalf("跨用户取消应拒绝：%v %v", got, err)
	}
	_ = strings.TrimSpace("")
}

func TestW14BTargetModelModesWithMapping(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestCatalogDDL)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()
	env.seedAccount(t, "acc-w14b-map", adminID, "w14b-map", "active")
	sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-w14b-map", "base_url": "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-w14b-map'`, sealed)
	// 目录：源模型 + 映射上游模型。
	env.exec(t, `INSERT INTO provider_model_catalog (provider_code, model, mode, release_date, supported_api_protocols_json, status, catalog_visible)
		VALUES ('gpt', 'gpt-4o-mini', NULL, '2024-07-18', '["chat_completions","responses"]', 'active', 1),
		       ('gpt', 'custom-a', NULL, '2024-06-01', '["chat_completions"]', 'active', 1)`)
	// 账户模型映射：gpt-4o-mini → custom-a（触发映射解析 + 目录缓存臂）。
	env.exec(t, `INSERT INTO account_model_mappings (account_id, provider_code, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, created_at, updated_at)
		VALUES ('acc-w14b-map', 'gpt', 'gpt-4o-mini', 'chat_completions', 'custom-a', 'chat_completions', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`)

	gotOptions, err := env.store.FindManualTestOptionsContext(ctx, "acc-w14b-map", &scope)
	if err != nil || gotOptions == nil {
		t.Fatalf("选项上下文应返回：%v %v", gotOptions, err)
	}
	// 带映射的能力上下文：命中 resolveTestAccountModelMapping + 上游目录缓存臂。
	got, err := env.store.FindManualTestCapabilitiesContext(ctx, "acc-w14b-map", "gpt-4o-mini", &scope)
	if err != nil || got == nil {
		t.Fatalf("能力上下文应返回：%v %v", got, err)
	}
	if len(got.ModelMappings) != 1 {
		t.Fatalf("能力上下文应带映射：%+v", got.ModelMappings)
	}
	capabilities, err := env.store.AccountManualTestModelCapabilities(ctx, got, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("映射能力解析应成功：%v", err)
	}
	if len(capabilities.TestEndpointModes) == 0 {
		t.Fatalf("映射后应有可用形态：%+v", capabilities)
	}
	// 纯函数臂。
	if rulePriority(map[string]any{"priority": float64(7)}) != 7 || rulePriority(map[string]any{}) != 0 {
		t.Fatal("rulePriority 分支不符")
	}
	if text := stringContextField("  value  "); text == nil || *text != "value" {
		t.Fatalf("stringContextField 值不符：%v", text)
	}
	if text := stringContextField(3); text != nil {
		t.Fatalf("非字符串应返回 nil：%v", text)
	}
	observed := cooldownRetestObservationStartedAtForStatus("temporary_unavailable", time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	if !observed.Valid {
		t.Fatal("冷却状态应记录观察起点")
	}
	if observed := cooldownRetestObservationStartedAtForStatus("active", time.Now()); observed.Valid {
		t.Fatal("active 不应记录观察起点")
	}
	if lockStateTableRef(true) != "juhe_business.account_lock_states" || lockStateTableRef(false) != "account_lock_states" {
		t.Fatal("锁表引用分支不符")
	}
	store := &Store{pg: true}
	if store.forUpdate() != " FOR UPDATE" || env.store.forUpdate() != "" {
		t.Fatal("forUpdate 分支不符")
	}
	if actor := func() string {
		request, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
		return actorResolver(request)
	}(); actor != "anonymous" {
		t.Fatalf("匿名 actor 应回退：%q", actor)
	}
	// 组统计脏标记：无绑定账户安全返回。
	if err := env.store.markBatchGroupStatsDirty(ctx, []string{"acc-w14b-map"}, "w14b"); err != nil {
		t.Fatalf("组统计标记应成功：%v", err)
	}
	if env.store.testEffectsOrNil() != nil {
		t.Fatal("未接线 effects 应返回 nil 端口")
	}
}
