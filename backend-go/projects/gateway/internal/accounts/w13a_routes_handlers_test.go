package accounts

// w13a routes.go 未覆盖臂补齐（二）：HTTP 处理器臂 —— 克隆上下文禁止臂、
// 各写路由的坏 JSON/空白作用域拒绝、锁死配置最小提交校验、admin all 过滤、
// 标签删除族与详情缺失。纯解析器直测见 w13a_routes_parsers_test.go。

import (
	"net/http"
	"strings"
	"testing"
)

func TestW13ACloneContextForbiddenAndMissing(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-cl", adminID, "w13a-cl", "active")
	// 授权实例（带来源账户）不能克隆 → 403 禁止臂（clone.go forbidden 映射）。
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'acc-w13a-src',
		authorization_instance_authorization_id = 'authz-1' WHERE id = 'acc-w13a-cl'`)
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-cl/clone-context", ""); code != http.StatusForbidden {
		t.Fatalf("授权实例克隆应 403：%d %v", code, payload)
	}
	// 缺失账户 → 404。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-none/clone-context", ""); code != http.StatusNotFound {
		t.Fatalf("缺失账户克隆应 404：%d %v", code, payload)
	}
}

func TestW13AWriteRoutesRejectBadBodies(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-bj", adminID, "w13a-bj", "active")

	// create：空白 systemAccountId 显式拒绝。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts?systemAccountId=",
		createPayload("w13a-bj2")); code != http.StatusBadRequest {
		t.Fatalf("空白作用域 create 应 400：%d %v", code, payload)
	}
	// create：坏 JSON → DecodeJSON 400。
	if code, _ := env.doRaw(t, http.MethodPost, "/__aisys__/api/accounts", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON create 应 400：%d", code)
	}
	// patchBasic：坏 JSON。
	if code, _ := env.doRaw(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w13a-bj", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON patch 应 400：%d", code)
	}
	// lock/unlock/lock-config：坏 JSON。
	for _, path := range []string{"/lock", "/unlock", "/lock-config"} {
		if code, _ := env.doRaw(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-bj"+path, "{not-json"); code != http.StatusBadRequest {
			t.Fatalf("坏 JSON %s 应 400：%d", path, code)
		}
	}
	// lock-config：合法 JSON 但没有任何配置项 → 400。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-bj/lock-config",
		`{"expectedConfigRevision":1}`); code != http.StatusBadRequest {
		t.Fatalf("缺配置项 lock-config 应 400：%d %v", code, payload)
	}
	// lock：缺失账户 → 404（lockNotFoundMessage）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-none/lock",
		`{"expectedConfigRevision":1}`); code != http.StatusNotFound {
		t.Fatalf("缺失账户 lock 应 404：%d %v", code, payload)
	}
	// lock-config：缺失账户 → 404。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w13a-none/lock-config",
		`{"expectedConfigRevision":1,"lockDeathTimeoutSeconds":300}`); code != http.StatusNotFound {
		t.Fatalf("缺失账户 lock-config 应 404：%d %v", code, payload)
	}
	// remove：缺失账户 → 404。
	if code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/acc-w13a-none", ""); code != http.StatusNotFound {
		t.Fatalf("缺失账户 remove 应 404：%d %v", code, payload)
	}
	// detail：缺失账户 → 404。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w13a-none", ""); code != http.StatusNotFound {
		t.Fatalf("缺失账户 detail 应 404：%d %v", code, payload)
	}
}

func TestW13AListAndOptionsQueryArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-q", adminID, "w13a-query", "active")

	// admin + systemAccountId=all（adminScope filter all 分支）。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts?systemAccountId=all", ""); code != http.StatusOK {
		t.Fatalf("all 过滤 list 应 200：%d %v", code, payload)
	}
	// 组合查询参数：sorts 多段、ids、status、schedulable、keyword。
	query := "?sorts=name:asc&sorts=priority:desc&ids=acc-w13a-q&status=active,pending_test&schedulable=enabled&keyword=w13a"
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts"+query, ""); code != http.StatusOK {
		t.Fatalf("组合查询 list 应 200：%d %v", code, payload)
	}
	// options：limit 下界与非法值。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?limit=0", ""); code != http.StatusOK {
		t.Fatalf("limit=0 options 应 200：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?limit=abc", ""); code != http.StatusOK {
		t.Fatalf("非法 limit options 应 200：%d %v", code, payload)
	}
	// options：空白 systemAccountId 未包 scoped（admin 注册裸 handler）→ 200。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options?systemAccountId=", ""); code != http.StatusOK {
		t.Fatalf("空白作用域 options 应 200：%d %v", code, payload)
	}
	// my-accounts 自面：list/options/tags/detail 可访问。
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/options", ""); code != http.StatusOK {
		t.Fatalf("my options 应 200：%d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/tags", ""); code != http.StatusOK {
		t.Fatalf("my tags 应 200：%d", code)
	}
	if code, _ := env.do(t, http.MethodGet, "/__aisys__/api/my-accounts/acc-w13a-q/edit-basic", ""); code != http.StatusOK {
		t.Fatalf("my detail 应 200：%d", code)
	}
}

func TestW13ATagDeleteArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-tg", adminID, "w13a-tag", "active")
	now := "2026-09-17T00:00:00.000Z"
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('acctag-w13a-used', ?, 'w13a-used', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at)
		VALUES ('acctag-w13a-free', ?, 'w13a-free', ?, ?)`, adminID, now, now)
	env.exec(t, `INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at)
		VALUES ('acc-w13a-tg', 'acctag-w13a-used', ?, ?)`, adminID, now)

	// 绑定中的标签删除 → 400（TagInUseError）。
	if code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/acctag-w13a-used", ""); code != http.StatusBadRequest {
		t.Fatalf("占用标签删除应 400：%d %v", code, payload)
	}
	// 空闲标签删除 → 204。
	if code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/acctag-w13a-free", ""); code != http.StatusNoContent {
		t.Fatalf("空闲标签删除应 204：%d %v", code, payload)
	}
	// 缺失标签 → 404。
	if code, payload := env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/acctag_w13a_missing", ""); code != http.StatusNotFound {
		t.Fatalf("缺失标签删除应 404：%d %v", code, payload)
	}
	// my 面：同判定。
	if code, _ := env.do(t, http.MethodDelete, "/__aisys__/api/my-accounts/tags/acctag_w13a_missing", ""); code != http.StatusNotFound {
		t.Fatalf("my 缺失标签删除应 404：%d", code)
	}
}

// doRaw 发送原始文本请求体（Content-Type: application/json）并返回状态码。
func (e *testEnv) doRaw(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	e.mu.Lock()
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
		}
	}
	e.mu.Unlock()
	raw := make([]byte, 0)
	_, _ = response.Body.Read(raw)
	response.Body.Close()
	return response.StatusCode, nil
}
