package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// ---------------------------------------------------------------------------
// 模型选项查询：参数校验矩阵与过滤组合
// ---------------------------------------------------------------------------

func TestWeModelOptionsQueryVariants(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		query   string
		status  int
		message string
	}{
		{"非法 protocol", "?protocol=ollama", http.StatusBadRequest, "protocol 必须是 openai、anthropic 或 gemini"},
		{"非法 limit", "?limit=abc", http.StatusBadRequest, "limit 必须是 1 到 50 的整数"},
		{"limit 小数", "?limit=1.5", http.StatusBadRequest, "limit 必须是 1 到 50 的整数"},
		{"limit 越界", "?limit=51", http.StatusBadRequest, "limit 必须是 1 到 50 的整数"},
		{"未知供应商", "?providerCode=nope", http.StatusNotFound, "供应商不存在或已停用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/models/options"+tc.query, "")
			if code != tc.status || (tc.message != "" && payload["message"] != tc.message) {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, tc.status, tc.message)
			}
		})
	}

	// 组合查询：供应商 + 协议 + 关键词 + 上限 + 预选。
	code, options := env.do(t, http.MethodGet,
		"/__aisys__/api/providers/models/options?providerCode=gpt&protocol=openai&keyword=gpt-4o&limit=10&selectedIds=cat-1,cat-2", "")
	if code != http.StatusOK {
		t.Fatalf("组合查询: %d %v", code, options)
	}
	if len(dataArray(t, options)) == 0 {
		t.Fatalf("应命中模型: %v", options)
	}
	// keyword 的 token 边界语义：gpt-z 不应命中 gpt-4o。
	code, miss := env.do(t, http.MethodGet,
		"/__aisys__/api/providers/models/options?providerCode=gpt&keyword=gpt-z", "")
	if code != http.StatusOK || len(dataArray(t, miss)) != 0 {
		t.Fatalf("keyword 未命中: %d %v", code, miss)
	}
	// root + systemAccountId=user1：个人选项合并分支。
	user1 := env.requireAccount(t, "user1", "user-pass", "user")
	env.login(t, "root", "root-pass", "super_admin")
	code, personal := env.do(t, http.MethodGet,
		"/__aisys__/api/providers/models/options?providerCode=gpt&systemAccountId="+user1, "")
	if code != http.StatusOK {
		t.Fatalf("个人选项: %d %v", code, personal)
	}
	// systemAccountId=all：管理员查看全局合集。
	code, all := env.do(t, http.MethodGet,
		"/__aisys__/api/providers/models/options?providerCode=gpt&systemAccountId=all", "")
	if code != http.StatusOK {
		t.Fatalf("all 选项: %d %v", code, all)
	}
	// 协议维度过滤（anthropic/gemini 的 source code 解析分支）。
	for _, protocol := range []string{"anthropic", "gemini"} {
		code, byProtocol := env.do(t, http.MethodGet,
			"/__aisys__/api/providers/models/options?protocol="+protocol, "")
		if code != http.StatusOK {
			t.Fatalf("protocol %s: %d %v", protocol, code, byProtocol)
		}
	}
	// 个人范围 + 关键词组合。
	code, personalKeyword := env.do(t, http.MethodGet,
		"/__aisys__/api/providers/models/options?providerCode=gpt&systemAccountId="+user1+"&keyword=gpt-4o", "")
	if code != http.StatusOK {
		t.Fatalf("personal+keyword: %d %v", code, personalKeyword)
	}
}

func TestWeProvidersReadEndpointsVariants(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	user1, user2 := env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 管理员 + systemAccountId 过滤（requestSystemAccountID 的管理员分支）。
	code, list := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?systemAccountId="+user2, "")
	if code != http.StatusOK {
		t.Fatalf("管理员过滤列表: %d %v", code, list)
	}
	for _, row := range dataArray(t, list) {
		entry := row.(map[string]any)
		if entry["scope"] == "personal" && entry["systemAccountId"] != user2 {
			t.Fatalf("过滤失效: %v", entry)
		}
	}
	// all 关闭过滤。
	code, all := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?systemAccountId=all", "")
	if code != http.StatusOK || len(dataArray(t, all)) == 0 {
		t.Fatalf("all 列表: %d %v", code, all)
	}

	// 非管理员请求同一过滤：filter 被忽略（回落个人）。
	env.login(t, user1, "user-pass", "user")
	code, userList := env.do(t, http.MethodGet, "/__aisys__/api/providers/list?systemAccountId=ignored", "")
	if code != http.StatusOK {
		t.Fatalf("用户列表: %d %v", code, userList)
	}

	// includeInactive / includeUnpriced 查询（models 处理器的布尔解析）。
	env.login(t, "root", "root-pass", "super_admin")
	code, models := env.do(t, http.MethodGet, "/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=yes", "")
	if code != http.StatusOK || len(dataArray(t, models)) == 0 {
		t.Fatalf("include 组合: %d %v", code, models)
	}
	// 停用供应商对管理请求可见（viewScope=admin + 管理员角色分支）。
	code, disabledProvider := env.do(t, http.MethodGet, "/__aisys__/api/providers/anthropic?viewScope=admin", "")
	if code != http.StatusOK {
		t.Fatalf("管理请求停用供应商: %d %v", code, disabledProvider)
	}
	// options 与 definitions 读取面。
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/options", ""); code != http.StatusOK {
		t.Fatalf("options: %d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodGet, "/__aisys__/api/providers/definitions", ""); code != http.StatusOK {
		t.Fatalf("definitions: %d %v", code, payload)
	}
}

// ---------------------------------------------------------------------------
// 默认检查模型与删除：补充分支
// ---------------------------------------------------------------------------

func TestWeProvidersPutDefaultHealthCheckModelVariants(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 管理请求（带 systemAccountId=all）落系统级默认。
	code, systemDefault := env.do(t, http.MethodPut, "/__aisys__/api/providers/gemini/default-health-check-model?systemAccountId=all",
		`{"model":"gemini-2.5-pro"}`)
	if code != http.StatusOK {
		t.Fatalf("系统默认: %d %v", code, systemDefault)
	}
	// 未知供应商 → 404。
	code, missing := env.do(t, http.MethodPut, "/__aisys__/api/providers/nope/default-health-check-model",
		`{"model":"x"}`)
	if code != http.StatusNotFound || missing["message"] != "供应商不存在" {
		t.Fatalf("未知供应商: %d %v", code, missing)
	}
	// 非法 body → 400。
	code, badBody := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model",
		`{"bogus":1}`)
	if code != http.StatusBadRequest || badBody["message"] != "默认检查模型参数无效" {
		t.Fatalf("非法 body: %d %v", code, badBody)
	}
	// 非 JSON body → 400。
	code, notJSON := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model", `not-json`)
	if code != http.StatusBadRequest || notJSON["message"] != "请求体必须是 JSON 对象" {
		t.Fatalf("非 JSON: %d %v", code, notJSON)
	}

	// 个人偏好分支：普通用户必须带 systemAccountId（自身）。
	env.login(t, "user1", "user-pass", "user")
	code, personal := env.do(t, http.MethodPut, "/__aisys__/api/providers/gpt/default-health-check-model",
		`{"model":"gpt-4o-mini"}`)
	if code != http.StatusOK {
		t.Fatalf("个人偏好: %d %v", code, personal)
	}
}

func TestWeProvidersDeleteModelForbiddenAndMissing(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedWriteFixtures(t)
	env.requireAccount(t, "user2", "user-pass", "user")
	env.login(t, "user2", "user-pass", "user")

	// user2 删除 user1 的个人模型 → 404（按归属查询不可见）。
	code, missing := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusNotFound || missing["message"] != "自定义模型不存在" {
		t.Fatalf("他人模型不可见: %d %v", code, missing)
	}

	// user1 删除自己的模型（带默认引用清理分支）。
	env.login(t, "user1", "user-pass", "user")
	code, deleted := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusOK || dataMap(t, deleted)["deleted"] != true {
		t.Fatalf("删除自有模型: %d %v", code, deleted)
	}
	// 重复删除 → 404。
	code, again := env.do(t, http.MethodDelete, "/__aisys__/api/providers/gpt/models/cu-1", "")
	if code != http.StatusNotFound {
		t.Fatalf("重复删除: %d %v", code, again)
	}
}

// ---------------------------------------------------------------------------
// 关闭数据库统一驱动读路径错误分支（500 投影）
// ---------------------------------------------------------------------------

func TestWeProvidersReadErrorsWhenDatabaseClosed(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.login(t, "root", "root-pass", "super_admin")

	if err := env.db.Close(); err != nil {
		t.Fatal(err)
	}

	adminCtx := authsys.WithAuthContext(context.Background(), &authsys.AuthContext{
		SystemAccountID: "sys-root", Username: "root", Role: "super_admin",
	})
	call := func(handler http.Handler, target string, pathValues map[string]string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(adminCtx)
		for key, value := range pathValues {
			request.SetPathValue(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	deps := env.providersDeps
	cases := []struct {
		name       string
		handler    http.Handler
		pathValues map[string]string
	}{
		{"list", http.HandlerFunc(deps.listItems), nil},
		{"definitions", http.HandlerFunc(deps.listDefinitions), nil},
		{"options", http.HandlerFunc(deps.options), nil},
		{"definitionsRead", http.HandlerFunc(deps.definitions), nil},
		{"modelOptions", http.HandlerFunc(deps.modelOptions), nil},
		{"find", http.HandlerFunc(deps.find), map[string]string{"code": "gpt"}},
		{"models", http.HandlerFunc(deps.models), map[string]string{"code": "gpt"}},
		{"capabilities", http.HandlerFunc(deps.modelCapabilities), map[string]string{"code": "gpt", "modelId": "gpt-4o"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := call(tc.handler, "/x", tc.pathValues); code != http.StatusInternalServerError {
				t.Fatalf("%s = %d", tc.name, code)
			}
		})
	}
}
