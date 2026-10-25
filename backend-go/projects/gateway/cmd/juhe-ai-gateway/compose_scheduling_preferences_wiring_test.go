package main

// schedulingpreferences 组合根接线测试：钉住双面 12 端点的注册判据与失效
// 端口类型约束（compose.go 中 NewStore 直接接收 *inval.Bus 的前提）。

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/schedulingpreferences"
)

// *inval.Bus 必须持续满足偏好 store 的失效端口（routestrategies.RuntimeInvalidator）：
// compose.go 把同一 bus 传给两个切片，类型漂移会在编译期暴露。
var _ routestrategies.RuntimeInvalidator = (*inval.Bus)(nil)

// TestSchedulingPreferencesComposeWiringRegistersDualSurfaces 复验组合根注册
// 判据：mountSchedulingPreferences 挂载后 admin /__aisys__/api/scheduling-
// preferences 与 self /__aisys__/api/my-scheduling-preferences 两面各 6 端点
// 全部可达（未登录 401 请先登录，注册判据），未挂载路径 404。
func TestSchedulingPreferencesComposeWiringRegistersDualSurfaces(t *testing.T) {
	authDeps := schedulingPreferencesWiringAuth(t)
	store, err := schedulingpreferences.NewStore(nil, time.Now, nil, nil)
	if err == nil {
		t.Fatalf("nil db store construction must fail, got %v", store)
	}
	db := schedulingPreferencesWiringDB(t)
	store, err = schedulingpreferences.NewStore(db, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	k := kernel.New(kernel.Options{SystemAPIPrefix: "/__aisys__"})
	mountSchedulingPreferences(k, store, authDeps, nil)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)

	registered := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/__aisys__/api/scheduling-preferences"},
		{http.MethodGet, "/__aisys__/api/scheduling-preferences/options"},
		{http.MethodGet, "/__aisys__/api/scheduling-preferences/schpref-x"},
		{http.MethodPost, "/__aisys__/api/scheduling-preferences"},
		{http.MethodPatch, "/__aisys__/api/scheduling-preferences/schpref-x"},
		{http.MethodDelete, "/__aisys__/api/scheduling-preferences/schpref-x"},
		{http.MethodGet, "/__aisys__/api/my-scheduling-preferences"},
		{http.MethodGet, "/__aisys__/api/my-scheduling-preferences/options"},
		{http.MethodGet, "/__aisys__/api/my-scheduling-preferences/schpref-x"},
		{http.MethodPost, "/__aisys__/api/my-scheduling-preferences"},
		{http.MethodPatch, "/__aisys__/api/my-scheduling-preferences/schpref-x"},
		{http.MethodDelete, "/__aisys__/api/my-scheduling-preferences/schpref-x"},
	}
	for _, endpoint := range registered {
		request, err := http.NewRequest(endpoint.method, server.URL+endpoint.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		// 端点已注册：未登录停在会话/角色中间件（401 请先登录）；未注册则
		// kernel 直接 404。
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s must register (unauthenticated 401), got %d", endpoint.method, endpoint.path, response.StatusCode)
		}
	}
	// 未注册路径对照：404 证明 401 判据确实来自注册面而非兜底。
	response, err := http.Get(server.URL + "/__aisys__/api/scheduling-preferences-unrelated")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unrelated path must 404, got %d", response.StatusCode)
	}
}

func schedulingPreferencesWiringAuth(t *testing.T) *authsys.Deps {
	t.Helper()
	service, err := businessauth.New(schedulingPreferencesWiringDB(t), modelcheckauth.Postgres, time.Now,
		businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(schedulingPreferencesWiringDB(t), modelcheckauth.Postgres, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
}

// schedulingPreferencesWiringDB 建共享内存库并保证 authsys 最小表形（探针在
// 会话中间件即返回，不触达业务表）。
func schedulingPreferencesWiringDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:schedulingpreferences-wiring-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
