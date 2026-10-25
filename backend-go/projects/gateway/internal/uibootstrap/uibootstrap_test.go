package uibootstrap

// ui-bootstrap 契约测试：引用数据读取（providerDefaults / preferredDefault
// RouteStrategy）、admin 无目标 scope 的 400、my-* 的 self 钳制与
// 系统账户不存在 404（Node ui-bootstrap.routes.ts +
// user-reference-data.repository.ts）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// bootstrapSchema mirrors the target groups contract (供应商解耦设计 §8):
// 组无 provider 维度，每个 system_account_id 至多一个 is_default=1 分组
// （唯一部分索引在维护库 DDL 上表达，夹具按业务语义播种单默认组）。
// 列型沿 maintenance 生产业务 DDL（groups/route_strategies 的 enabled 与
// is_default 为 integer，`is_default = 1` 谓词即生产形态）。
const bootstrapSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE juhe_business.system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT);
	CREATE TABLE juhe_business.groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE juhe_business.route_strategies (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, mode TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT '');
	CREATE TABLE juhe_business.route_strategy_groups (route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL);
`

func newBootstrapFixture(t *testing.T) *Deps {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, bootstrapSchema)
		return err
	})
	seed := []string{
		`INSERT INTO juhe_business.system_accounts (id, username, display_name) VALUES ('sa-1', 'user1', 'User One')`,
		// 单默认组（设计 §8）：owner 的默认分组不再按供应商分叉。
		`INSERT INTO juhe_business.groups (id, name, system_account_id, enabled, is_default) VALUES
			('g-1', '默认组', 'sa-1', 1, 1)`,
		`INSERT INTO juhe_business.route_strategies (id, name, system_account_id, mode, status, is_default, created_at) VALUES
			('rs-1', '策略一', 'sa-1', 'normal', 'active', 1, '2026-01-01'),
			('rs-2', '策略二', 'sa-1', 'hybrid', 'paused', 1, '2026-01-02')`,
		`INSERT INTO juhe_business.route_strategy_groups (route_strategy_id, system_account_id, group_id, status) VALUES
			('rs-1', 'sa-1', 'g-1', 'active'),
			('rs-2', 'sa-1', 'g-1', 'paused')`,
	}
	for _, statement := range seed {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &Deps{DB: db, Auth: nil}
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
	// 组无 provider 维度（设计 §8）：owner 的唯一默认组按 GPT 口径投影为单条
	// providerDefaults；默认路由按「启用绑定 + 启用策略」优先排序（rs-1 active
	// 绑定在前）。
	defaults := data["providerDefaults"].([]any)
	if len(defaults) != 1 {
		t.Fatalf("providerDefaults wrong: %#v", defaults)
	}
	only := defaults[0].(map[string]any)
	if only["providerCode"] != "gpt" {
		t.Fatalf("default providerCode wrong: %#v", only)
	}
	if only["defaultGroup"].(map[string]any)["id"] != "g-1" {
		t.Fatalf("default group wrong: %#v", only)
	}
	strategy := only["defaultRouteStrategy"].(map[string]any)
	if strategy["id"] != "rs-1" || strategy["status"] != "active" {
		t.Fatalf("default route strategy wrong: %#v", strategy)
	}
	// preferred：默认组启用 + 绑定 active + 策略 active → rs-1。
	preferred := data["preferredDefaultRouteStrategy"].(map[string]any)
	if preferred["id"] != "rs-1" || preferred["mode"] != "normal" {
		t.Fatalf("preferred strategy wrong: %#v", preferred)
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
