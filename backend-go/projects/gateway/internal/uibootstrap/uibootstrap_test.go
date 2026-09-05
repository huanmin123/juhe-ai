package uibootstrap

// ui-bootstrap 契约测试：引用数据读取（providerDefaults / preferredDefault
// RouteStrategy）、admin 无目标 scope 的 400、my-* 的 self 钳制与
// 系统账户不存在 404（Node ui-bootstrap.routes.ts +
// user-reference-data.repository.ts）。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const bootstrapSchema = `
	CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT);
	CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE route_strategies (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, mode TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT '');
	CREATE TABLE route_strategy_groups (route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL);
`

func newBootstrapFixture(t *testing.T) *Deps {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(bootstrapSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	seed := []string{
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sa-1', 'user1', 'User One')`,
		`INSERT INTO groups (id, name, system_account_id, provider_code, enabled, is_default) VALUES
			('g-1', '默认组', 'sa-1', 'openai', 1, 1),
			('g-2', '代码组', 'sa-1', 'anthropic', 1, 1),
			('g-3', '停用组', 'sa-1', 'gemini', 0, 1)`,
		`INSERT INTO route_strategies (id, name, system_account_id, mode, status, is_default, created_at) VALUES
			('rs-1', '策略一', 'sa-1', 'normal', 'active', 1, '2026-01-01'),
			('rs-2', '策略二', 'sa-1', 'hybrid', 'paused', 1, '2026-01-02')`,
		`INSERT INTO route_strategy_groups (route_strategy_id, system_account_id, group_id, status) VALUES
			('rs-1', 'sa-1', 'g-1', 'active'),
			('rs-2', 'sa-1', 'g-2', 'paused')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &Deps{DB: db, PGDialect: false, Auth: nil}
}

func invokeBootstrap(t *testing.T, deps *Deps, selfOnly bool, target string, auth *authsys.AuthContext) *httptest.ResponseRecorder {
	t.Helper()
	handler := deps.options(selfOnly)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func TestBootstrapReferenceDataAdminScoped(t *testing.T) {
	deps := newBootstrapFixture(t)
	// admin 无目标 scope → 400 请选择目标系统账户。
	recorder := invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unscoped admin not 400: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeBootstrap(t, recorder)["message"]; got != "请选择目标系统账户" {
		t.Fatalf("unscoped admin message wrong: %#v", got)
	}
	// 带 scope 的 admin 读取完整引用数据。
	recorder = invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=sa-1", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("scoped admin not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBootstrap(t, recorder)["data"].(map[string]any)
	if data["systemAccountId"] != "sa-1" {
		t.Fatalf("systemAccountId wrong: %#v", data)
	}
	// Node keeps disabled default groups in the list (enabled only gates the
	// preferred strategy), ordered by provider_code ASC.
	defaults := data["providerDefaults"].([]any)
	if len(defaults) != 3 {
		t.Fatalf("providerDefaults wrong: %#v", defaults)
	}
	anthropic := defaults[0].(map[string]any)
	if anthropic["providerCode"] != "anthropic" {
		t.Fatalf("first default wrong: %#v", anthropic)
	}
	if anthropic["defaultGroup"].(map[string]any)["id"] != "g-2" {
		t.Fatalf("default group wrong: %#v", anthropic)
	}
	strategy := anthropic["defaultRouteStrategy"].(map[string]any)
	if strategy["id"] != "rs-2" || strategy["status"] != "paused" {
		t.Fatalf("default route strategy wrong: %#v", strategy)
	}
	openai := defaults[2].(map[string]any)
	if openai["providerCode"] != "openai" || openai["defaultGroup"].(map[string]any)["id"] != "g-1" {
		t.Fatalf("openai default wrong: %#v", openai)
	}
	// preferred 只认 gpt 供应商 + enabled 组 + active 绑定；openai 不等于 gpt，
	// 因此 preferred 缺省。
	if _, ok := data["preferredDefaultRouteStrategy"]; ok {
		t.Fatalf("preferred should be absent: %#v", data)
	}
}

func TestBootstrapSelfSurfacePinsCaller(t *testing.T) {
	deps := newBootstrapFixture(t)
	// my-* 即便 admin 调用也钳制到自身（forceSelfAccessScope）。
	auth := adminAuthBootstrap("super_admin")
	auth.SystemAccountID = "sa-1"
	recorder := invokeBootstrap(t, deps, true, "/__aisys__/api/my-ui-bootstrap/options?systemAccountId=someone-else", auth)
	if recorder.Code != http.StatusOK {
		t.Fatalf("self surface not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	data := decodeBootstrap(t, recorder)["data"].(map[string]any)
	if data["systemAccountId"] != "sa-1" {
		t.Fatalf("self scope wrong: %#v", data)
	}
	// 不存在的账户 → 404 系统账户不存在。
	missing := adminAuthBootstrap("super_admin")
	missing.SystemAccountID = "ghost"
	recorder = invokeBootstrap(t, deps, true, "/__aisys__/api/my-ui-bootstrap/options", missing)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing account not 404: %d", recorder.Code)
	}
	if got := decodeBootstrap(t, recorder)["message"]; got != "系统账户不存在" {
		t.Fatalf("404 message wrong: %#v", got)
	}
}

func TestBootstrapBlankScopeQueryRejected(t *testing.T) {
	deps := newBootstrapFixture(t)
	recorder := invokeBootstrap(t, deps, false, "/__aisys__/api/ui-bootstrap/options?systemAccountId=", adminAuthBootstrap("admin"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("blank scope not 400: %d", recorder.Code)
	}
}

func adminAuthBootstrap(role string) *authsys.AuthContext {
	if role == "" {
		role = "admin"
	}
	return &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", DisplayName: "Admin", Role: role, SessionID: "s1"}
}

func decodeBootstrap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return payload
}
