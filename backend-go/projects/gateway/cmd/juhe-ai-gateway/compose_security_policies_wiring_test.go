package main

// securitypolicies 组合根接线测试：镜像 compose_scheduling_preferences_wiring_
// test.go 的注册判据——mountSecurityPolicies 挂载后 admin
// /__aisys__/api/security-policies 与 self /__aisys__/api/my-security-policies
// 两面端点全部可达（未登录 401 请先登录，注册判据），未挂载路径 404。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
)

// TestSecurityPoliciesComposeWiringRegistersDualSurfaces：路由真实挂载冒烟
// （批B2 验收 4）——未登录 GET 停在会话/角色中间件（401），未注册路径 404。
func TestSecurityPoliciesComposeWiringRegistersDualSurfaces(t *testing.T) {
	db := securityPoliciesWiringDB(t)
	authDeps := schedulingPreferencesWiringAuth(t, db)
	store, err := securitypolicies.NewStore(db, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	k := kernel.New(kernel.Options{SystemAPIPrefix: "/__aisys__"})
	mountSecurityPolicies(k, store, authDeps, nil, nil)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)

	registered := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/__aisys__/api/security-policies"},
		{http.MethodGet, "/__aisys__/api/security-policies/options"},
		{http.MethodGet, "/__aisys__/api/security-policies/secpol-x"},
		{http.MethodPost, "/__aisys__/api/security-policies"},
		{http.MethodPatch, "/__aisys__/api/security-policies/secpol-x"},
		{http.MethodDelete, "/__aisys__/api/security-policies/secpol-x"},
		{http.MethodGet, "/__aisys__/api/my-security-policies"},
		{http.MethodGet, "/__aisys__/api/my-security-policies/options"},
		{http.MethodGet, "/__aisys__/api/my-security-policies/secpol-x"},
		{http.MethodPost, "/__aisys__/api/my-security-policies"},
		{http.MethodPatch, "/__aisys__/api/my-security-policies/secpol-x"},
		{http.MethodDelete, "/__aisys__/api/my-security-policies/secpol-x"},
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
	response, err := http.Get(server.URL + "/__aisys__/api/security-policies-unrelated")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unrelated path must 404, got %d", response.StatusCode)
	}
}

// securityPoliciesWiringDB 复用调度偏好接线测试的 authsys 最小表形（探针在会
// 话中间件即返回，不触达业务表）。
func securityPoliciesWiringDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE TABLE IF NOT EXISTS system_accounts (
			id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT,
			role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL,
			must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0,
			ai_account_limit INTEGER, expedited_account_limit INTEGER, request_limits_json TEXT,
			last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS system_sessions (
			id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`)
		return err
	})
}
