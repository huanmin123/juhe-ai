package accounts

// w13a test_dispatch_routes.go 未覆盖臂补齐：测试诊断会话/任务路由的缺失
// 资源 404、树形子路由的未知形状与空白作用域、手工测试入口的参数与可用性
// 拒绝臂、草稿测试的缺账户字段臂。

import (
	"net/http"
	"strings"
	"testing"
)

func TestW13ATestSessionAndTaskMissingResources(t *testing.T) {
	env := newTestFamilyEnv(t, &fakeTestEffects{accept: true})
	env.login(t, "root", "root-pass", "super_admin")
	base := "/__aisys__/api/accounts"

	// 创建一个真实会话，确认 201 并取得会话形状。
	code, payload := env.do(t, http.MethodPost, base+"/test-sessions", "")
	if code != http.StatusCreated {
		t.Fatalf("创建会话应 201：%d %v", code, payload)
	}
	sessionID, _ := dataMap(t, payload)["id"].(string)
	if sessionID == "" {
		t.Fatalf("会话 ID 缺失：%v", payload)
	}

	// 心跳/结束/取消/查询：缺失会话 → 404。
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, base + "/test-sessions/sess-w13a-none/heartbeat"},
		{http.MethodPost, base + "/test-sessions/sess-w13a-none/complete"},
		{http.MethodPost, base + "/test-sessions/sess-w13a-none/cancel"},
		{http.MethodGet, base + "/test-sessions/sess-w13a-none"},
		{http.MethodGet, base + "/test-sessions/sess-w13a-none/tasks"},
	} {
		if code, payload := env.do(t, route.method, route.path, ""); code != http.StatusNotFound {
			t.Fatalf("%s 缺失会话应 404：%d %v", route.path, code, payload)
		}
	}
	// 缺失任务：取消与查询 → 404。
	if code, payload := env.do(t, http.MethodPost, base+"/test-tasks/task-w13a-none/cancel", ""); code != http.StatusNotFound {
		t.Fatalf("缺失任务取消应 404：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, base+"/test-tasks/task-w13a-none", ""); code != http.StatusNotFound {
		t.Fatalf("缺失任务查询应 404：%d %v", code, payload)
	}
	// 任务列表（含 ids 过滤）→ 200。
	if code, payload := env.do(t, http.MethodGet, base+"/test-tasks?ids="+sessionID, ""); code != http.StatusOK {
		t.Fatalf("任务列表应 200：%d %v", code, payload)
	}
	// 真实会话：心跳 200、查询 200、会话任务 200。
	if code, _ := env.do(t, http.MethodPost, base+"/test-sessions/"+sessionID+"/heartbeat", ""); code != http.StatusOK {
		t.Fatalf("心跳应 200：%d", code)
	}
	if code, _ := env.do(t, http.MethodGet, base+"/test-sessions/"+sessionID+"/tasks", ""); code != http.StatusOK {
		t.Fatalf("会话任务列表应 200：%d", code)
	}
	// 取消真实会话 → 200。
	if code, _ := env.do(t, http.MethodPost, base+"/test-sessions/"+sessionID+"/cancel", ""); code != http.StatusOK {
		t.Fatalf("取消会话应 200：%d", code)
	}
	// 树形子路由：未知形状 → API 404。
	if code, _ := env.do(t, http.MethodGet, base+"/test-bogus/x/y", ""); code != http.StatusNotFound {
		t.Fatalf("未知树形形状应 404：%d", code)
	}
	if code, _ := env.do(t, http.MethodPost, base+"/test-bogus/x/y", ""); code != http.StatusNotFound {
		t.Fatalf("未知树形 POST 应 404：%d", code)
	}
	// my-accounts 自面同样可达（空会话 201）。
	if code, _ := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/test-sessions", ""); code != http.StatusCreated {
		t.Fatalf("自面创建会话应 201：%d", code)
	}
}

func TestW13ATestAccountAndDraftGates(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-w13a-ta", adminID, "w13a-ta", "active")
	base := "/__aisys__/api/accounts"

	// 坏 JSON。
	if code, _ := env.doRaw(t, http.MethodPost, base+"/acc-w13a-ta/test", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON test 应 400：%d", code)
	}
	// 非法请求体（未知键）。
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13a-ta/test", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法 test 体应 400：%d %v", code, payload)
	}
	// 缺失账户 → 404。
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13a-none/test", `{}`); code != http.StatusNotFound {
		t.Fatalf("缺失账户 test 应 404：%d %v", code, payload)
	}
	// 停用账户 → 400 不可用。
	env.seedAccount(t, "acc-w13a-tad", adminID, "w13a-tad", "disabled")
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13a-tad/test", `{}`); code != http.StatusBadRequest {
		t.Fatalf("停用账户 test 应 400：%d %v", code, payload)
	}
	// 会话缺失引用 → 400/404（assertUsableTestSession 拒绝）。
	if code, payload := env.do(t, http.MethodPost, base+"/acc-w13a-ta/test", `{"testSessionId":"sess-w13a-none"}`); code >= http.StatusInternalServerError {
		t.Fatalf("缺失会话引用不应 5xx：%d %v", code, payload)
	}

	// 草稿测试：缺 account 字段 → 400；坏 JSON → 400。
	if code, payload := env.do(t, http.MethodPost, base+"/test-draft", `{}`); code != http.StatusBadRequest {
		t.Fatalf("缺 account 草稿测试应 400：%d %v", code, payload)
	}
	if code, _ := env.doRaw(t, http.MethodPost, base+"/test-draft", "{not-json"); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 草稿测试应 400：%d", code)
	}
	if code, payload := env.do(t, http.MethodPost, base+"/test-draft", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法草稿测试体应 400：%d %v", code, payload)
	}
	// 草稿测试：分组不存在 → 400 pipeline。
	if code, payload := env.do(t, http.MethodPost, base+"/test-draft",
		`{"account":{"groupId":"grp-w13a-none","providerCode":"gpt","type":"api_key","providerProtocolProfileId":"prof-gpt",
		"credentials":{"api_key":"sk-x","base_url":"https://x"}}}`); code != http.StatusBadRequest {
		t.Fatalf("草稿分组无效应 400：%d %v", code, payload)
	}
	_ = strings.TrimSpace("")
}
