package accounts

// w13a m09_routes.go 未覆盖臂补齐：批量编辑上下文/批量更新/导入预览/导入
// 确认/导出的空白作用域、坏 JSON、非法请求体与导出空匹配臂，以及
// pipelineErrorMessage 纯函数。

import (
	"errors"
	"net/http"
	"testing"
)

func TestW13AM09RouteValidationArms(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 各路由空白作用域拒绝（59-62 / 82-85 / 141-143 / 159-162 / 216-219）。
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/__aisys__/api/accounts/batch-edit-context?systemAccountId="},
		{http.MethodPost, "/__aisys__/api/accounts/batch-update?systemAccountId="},
		{http.MethodPost, "/__aisys__/api/accounts/import/preview?systemAccountId="},
		{http.MethodPost, "/__aisys__/api/accounts/import/confirm?systemAccountId="},
		{http.MethodPost, "/__aisys__/api/accounts/export?systemAccountId="},
	} {
		if code, payload := env.do(t, route.method, route.path, `{}`); code != http.StatusBadRequest {
			t.Fatalf("%s 空白作用域应 400：%d %v", route.path, code, payload)
		}
	}

	// 各路由坏 JSON（decode 臂）。
	for _, route := range []string{
		"/__aisys__/api/accounts/batch-edit-context",
		"/__aisys__/api/accounts/batch-update",
		"/__aisys__/api/accounts/import/preview",
		"/__aisys__/api/accounts/import/confirm",
		"/__aisys__/api/accounts/export",
	} {
		if code, _ := env.doRaw(t, http.MethodPost, route, "{not-json"); code != http.StatusBadRequest {
			t.Fatalf("%s 坏 JSON 应 400：%d", route, code)
		}
	}

	// 非法请求体：批量上下文缺 accountIds（87-90 batchEditContextBody）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-edit-context", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法批量上下文应 400：%d %v", code, payload)
	}
	// 批量更新非法体（92-94 batchUpdateBody）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法批量更新应 400：%d %v", code, payload)
	}
	// 导入预览非法体（150-153 parseImportBody）；确认路由受 mutation 去重
	// 守护（同指纹失败缓存 409），用非空作用域区分键后覆盖同一解析臂（173-176）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/preview", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法导入预览应 400：%d %v", code, payload)
	}
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/import/confirm?systemAccountId=all", `{"bogus":1}`); code != http.StatusBadRequest {
		t.Fatalf("非法导入确认应 400：%d %v", code, payload)
	}
	// 导出非法体：未知键、双键、超上限（221-228 parseExportBody）。
	for _, body := range []string{
		`{"bogus":1}`,
		`{"accountIds":["a"],"filters":{}}`,
		`{"accountIds":"not-a-list"}`,
	} {
		if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", body); code != http.StatusBadRequest {
			t.Fatalf("非法导出体应 400：%d %v", code, payload)
		}
	}
	// 导出按筛选无匹配 → 400 当前筛选条件下没有匹配（CollectExportIDs 空臂）。
	if code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export",
		`{"filters":{"keyword":"w13a-无匹配-abcdef"}}`); code != http.StatusBadRequest {
		t.Fatalf("无匹配导出应 400：%d %v", code, payload)
	}
}

func TestW13APipelineErrorMessage(t *testing.T) {
	if got := pipelineErrorMessage(nil, "回退文案"); got != "回退文案" {
		t.Fatalf("nil 错误应回退：%q", got)
	}
	if got := pipelineErrorMessage(errors.New("  "), "回退文案"); got != "回退文案" {
		t.Fatalf("空白错误应回退：%q", got)
	}
	if got := pipelineErrorMessage(errors.New(" 实际错误 "), "回退文案"); got != "实际错误" {
		t.Fatalf("错误消息应去空白透传：%q", got)
	}
}
