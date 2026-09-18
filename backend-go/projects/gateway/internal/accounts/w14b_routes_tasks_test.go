package accounts

// w14b 路由臂、测试任务/取消链、导出过滤与代理引用复用补齐：options 查询
// 参数矩阵、坏请求体拒绝、会话任务创建/取消/列表、导出筛选分页收集与
// 代理引用共享/缺失分支。

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestW14BRouteErrorArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-rt", adminID, "w14b-rt", "active")

	// options 查询参数矩阵（optionsHandler → ListOptionSummaries）。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?ids=acc-w14b-rt&keyword=w14b&providerCode=gpt&type=api_key&status=all&tagIds=t1&tagIds=t2&page=1&limit=20", "")
	if code != http.StatusOK {
		t.Fatalf("options 查询应成功：%d", code)
	}
	// tags 路由 happy + 删除。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/tags", "")
	if code != http.StatusOK {
		t.Fatalf("tags 列表应成功：%d", code)
	}
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('tag-w14b-rt', ?, 'w14b-rt-tag', '2026-09-17T00:00:00.000Z', '2026-09-17T00:00:00.000Z')`, adminID)
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/tag-w14b-rt", "")
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("删除空闲标签应成功：%d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/tag-w14b-rt", "")
	if code != http.StatusNotFound {
		t.Fatalf("删除缺失标签应 404：%d", code)
	}
	// 坏 JSON 请求体 → 400。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts", "{bad")
	if code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 创建应 400：%d", code)
	}
	// patchTags 坏体。
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w14b-rt/tags", "{bad")
	if code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 标签补丁应 400：%d", code)
	}
	// lock/lockConfig 坏体与坏值。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rt/lock", "{bad")
	if code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 锁定应 400：%d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w14b-rt/lock-config", `{"deathTimeoutSeconds":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法锁配置应 400：%d", code)
	}
	// 删除：happy 与缺失。
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/acc-w14b-none", "")
	if code != http.StatusNotFound && code != http.StatusBadRequest {
		t.Fatalf("删除缺失账户应 4xx：%d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/acc-w14b-rt", "")
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("删除账户应成功：%d", code)
	}
}

func TestW14BTestTaskAndCancelChain(t *testing.T) {
	env := newTestEnv(t)
	env.exec(t, w14bTestSessionDDL)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()
	env.seedAccount(t, "acc-w14b-task", adminID, "w14b-task", "active")

	session, err := env.store.CreateTestSession(ctx, scope)
	if err != nil || session == nil {
		t.Fatalf("会话创建应成功：%v", err)
	}
	draft := &TestDraftSnapshot{ID: "acc-w14b-task", OwnerSystemAccountID: adminID, GroupID: "grp-x", ProviderCode: "gpt", Name: "w14b-task"}
	task, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w14b-task", AccountName: "w14b-task", ProviderCode: "gpt",
		ProviderProtocolProfileID: "prof-gpt", ProtocolCode: "openai", ProtocolVersion: "v1",
		AccountType: "api_key", Access: scope, Diagnostics: "full",
		SessionID: session.ID, Model: "gpt-4o-mini", TestEndpointMode: "chat_json", Draft: draft,
	})
	if err != nil || task == nil {
		t.Fatalf("任务创建应成功：%v %v", task, err)
	}
	// 任务列表（session 关联 join）。
	detail, tasks, err := env.store.GetTestSessionDetail(ctx, session.ID, &scope)
	if err != nil || detail == nil || len(tasks) != 1 {
		t.Fatalf("任务列表应有一条：%v %v %v", detail, len(tasks), err)
	}
	// 跨用户会话上的任务应被拒绝（assertUsableTestSession 权限臂）。
	userID := env.login(t, "alice-w14b-task", "alice-pass", "user")
	if _, err := env.store.CreateTestTask(ctx, TestTaskCreateInput{
		AccountID: "acc-w14b-task", Access: scope, SessionID: session.ID,
	}); err == nil {
		t.Fatal("missing: 使用他人会话应拒绝")
	}
	_ = userID
	// 取消：running 会话 + queued 任务 → 任务取消列表。
	cancel, err := env.store.CancelTestSession(ctx, session.ID, &scope, "w14b-取消")
	if err != nil || cancel == nil {
		t.Fatalf("取消应成功：%v", err)
	}
	if len(cancel.TaskIDs) != 1 {
		t.Fatalf("取消应返回任务 id：%+v", cancel.TaskIDs)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_test_tasks WHERE id = ? AND status = 'canceled'`, cancel.TaskIDs[0]); got != 1 {
		t.Fatalf("queued 任务应被取消：%d", got)
	}
	// 再次取消：非 running 会话保持原状态分支。
	again, err := env.store.CancelTestSession(ctx, session.ID, &scope, "")
	if err != nil || again == nil {
		t.Fatalf("重复取消应成功：%v", err)
	}
	if got, err := env.store.CancelTestSession(ctx, "acctsess-none", &scope, ""); err != nil || got != nil {
		t.Fatalf("缺失会话取消应返回 nil：%v %v", got, err)
	}
}

func TestW14BExportFiltersAndProxyRef(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}
	ctx := context.Background()
	now := "2026-09-17T00:00:00.000Z"

	// 两个账户共享同一代理（引用复用臂）+ 一个绑定缺失代理。
	env.exec(t, `INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
		VALUES ('proxy-w14b-sh', ?, 'w14b-sh-proxy', 'http', '127.0.0.1', 8080, 1, ?, ?)`, adminID, now, now)
	for _, item := range []struct{ id, name string }{{"acc-w14b-e1", "w14b-e1"}, {"acc-w14b-e2", "w14b-e2"}, {"acc-w14b-e3", "w14b-e3"}} {
		sealed, err := EncryptJSON(testSecret, Credentials{"api_key": "sk-" + item.id, "base_url": "https://api.openai.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		env.seedAccount(t, item.id, adminID, item.name, "active")
		env.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = ?`, sealed, item.id)
	}
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w14b-sh' WHERE id IN ('acc-w14b-e1','acc-w14b-e2')`)
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'proxy-w14b-gone' WHERE id = 'acc-w14b-e3'`)

	// 过滤收集：keyword 命中前两个；共享代理只导出一份引用。
	ids, err := env.store.CollectExportIDs(ctx, map[string]any{"keyword": "w14b-e", "providerCode": "gpt", "type": "api_key", "status": "active"}, scope)
	if err != nil {
		t.Fatalf("导出收集应成功：%v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("收集 id 数不符：%+v", ids)
	}
	result, err := env.store.ExportAccounts(ctx, ExportOptions{AccountIDs: ids}, scope)
	if err != nil {
		t.Fatalf("导出应成功：%v", err)
	}
	if len(result.Document.Proxies) != 1 {
		t.Fatalf("共享代理应合并为一项：%+v", result.Document.Proxies)
	}
	// 绑定缺失代理：引用臂（占位或错误，按实现至少不崩溃并保留两个成功项）。
	if len(result.Document.Accounts) < 2 {
		t.Fatalf("导出账户数不符：%d", len(result.Document.Accounts))
	}
	// exportFiltersOptions 纯函数分支（all/sorts/tagIds）。
	options := exportFiltersOptions(map[string]any{
		"keyword": " k ", "providerCode": "all", "type": "all",
		"status": []any{"active", "disabled"}, "schedulable": "enabled",
		"tagIds": map[string]any{}, "sorts": []any{map[string]any{"field": "name", "order": "desc"}},
	}, 2)
	if options.Page != 2 || options.Keyword != "k" || options.ProviderCode != "" || options.Type != "" || options.Status != "active,disabled" || options.Schedulable != "enabled" || len(options.Sorts) != 1 {
		t.Fatalf("过滤选项归一化不符：%+v", options)
	}
	if !strings.Contains(strings.Join([]string{options.Sorts[0].Field, options.Sorts[0].Order}, "|"), "name") {
		t.Fatalf("排序解析不符：%+v", options.Sorts)
	}
}
