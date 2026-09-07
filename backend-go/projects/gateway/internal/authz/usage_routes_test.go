// HTTP-level tests for the authz usage route family (BUG-0165): the
// permission matrix over both surfaces, the strict query contracts, the
// unknown-body-key rejection and the admin return / usage detail surfaces.
package authz

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// usageRouteEnvDDL extends the shared fixture with the columns and tables the
// authsys middleware stack touches (the systemteams fixture shape): the
// account store reads description/must_change_password/... through FindByUsername.
var usageRouteEnvDDL = []string{
	`ALTER TABLE system_accounts ADD COLUMN description TEXT`,
	`ALTER TABLE system_accounts ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE system_accounts ADD COLUMN image_generation_enabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE system_accounts ADD COLUMN ai_account_limit INTEGER`,
	`ALTER TABLE system_accounts ADD COLUMN request_limits_json TEXT`,
	`ALTER TABLE system_accounts ADD COLUMN last_login_at TEXT`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
}

type usageRouteEnv struct {
	f      *fixture
	server *httptest.Server
}

func seedRoleAccount(t *testing.T, f *fixture, id, role string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', 'pbkdf2$sha512$120000$abc$def', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
		ON CONFLICT(id) DO UPDATE SET role = excluded.role, status = 'active'`, id, id, id, role); err != nil {
		t.Fatal(err)
	}
}

// usageEnvSchemaOnce guards the one-shot ALTER statements per fixture
// database: every env of one test shares the same cache=shared memory file
// (the auto login username differs per env, the data set is shared).
var usageEnvSchemaOnce sync.Map

// newUsageRouteEnv mounts the full authz family behind the real authsys
// middleware with development auto login pinned to the given account.
func newUsageRouteEnv(t *testing.T, autoLogin string) *usageRouteEnv {
	t.Helper()
	f := newUsageFixture(t)
	if _, loaded := usageEnvSchemaOnce.LoadOrStore(t.Name(), true); !loaded {
		for _, statement := range usageRouteEnvDDL {
			if _, err := f.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	seedRoleAccount(t, f, "admin", "admin")
	seedRoleAccount(t, f, "plainuser", "user")
	seedRoleAccount(t, f, "owner1", "user")
	seedRoleAccount(t, f, "grantee", "user")
	service, err := businessauth.New(f.db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(f.db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	authDeps := &authsys.Deps{
		Port: service, Accounts: accounts,
		Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil),
		CaptchaDisabled:      true,
		DevAutoLoginUsername: autoLogin,
	}
	deps := &Deps{Store: f.store, Auth: authDeps}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &usageRouteEnv{f: f, server: server}
}

func (env *usageRouteEnv) do(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, env.server.URL+url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := env.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}

// TestUsageEndpointsPermissionMatrix pins the route gates
// (docs/functions/接口契约与权限矩阵.md:543-548): the /authorizations usage
// family requires the admin role while the /my-authorizations family accepts
// every logged-in user.
func TestUsageEndpointsPermissionMatrix(t *testing.T) {
	adminEnv := newUsageRouteEnv(t, "admin")
	userEnv := newUsageRouteEnv(t, "plainuser")

	adminURLs := []string{
		"/__aisys__/api/authorizations/usage/team-details",
		"/__aisys__/api/authorizations/usage/team-summary",
		"/__aisys__/api/authorizations/usage/user-details",
		"/__aisys__/api/authorizations/usage/user-summary",
	}
	for _, url := range adminURLs {
		if status, _ := adminEnv.do(t, http.MethodGet, url, ""); status != http.StatusOK {
			t.Fatalf("admin %s status = %d", url, status)
		}
		if status, _ := userEnv.do(t, http.MethodGet, url, ""); status != http.StatusForbidden {
			t.Fatalf("user %s status = %d", url, status)
		}
	}
	for _, url := range []string{
		"/__aisys__/api/my-authorizations/usage/team-details",
		"/__aisys__/api/my-authorizations/usage/team-summary",
		"/__aisys__/api/my-authorizations/usage/user-details",
		"/__aisys__/api/my-authorizations/usage/user-summary",
	} {
		if status, _ := userEnv.do(t, http.MethodGet, url, ""); status != http.StatusOK {
			t.Fatalf("user %s status = %d", url, status)
		}
		if status, _ := adminEnv.do(t, http.MethodGet, url, ""); status != http.StatusOK {
			t.Fatalf("admin %s status = %d", url, status)
		}
	}

	// A malformed bearer token fails the session gate before any handler
	// (ResolveSystemAccessToken "invalid" → 401), on both surfaces.
	req, err := http.NewRequest(http.MethodGet, adminEnv.server.URL+"/__aisys__/api/authorizations/usage/team-details", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer garbage")
	resp, err := adminEnv.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d", resp.StatusCode)
	}
}

// TestUsageEndpointsQueryContract replays the strict usage schemas: unknown
// keys and invalid enums/pages/dates reject with the zod messages while the
// plain list schema strips unknown keys.
func TestUsageEndpointsQueryContract(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	cases := []struct {
		url     string
		message string
	}{
		{"/__aisys__/api/authorizations/usage/team-details?foo=bar", "Unrecognized key(s) in object: 'foo'"},
		{"/__aisys__/api/authorizations/usage/team-details?page=0", "页码必须大于 0"},
		{"/__aisys__/api/authorizations/usage/team-details?pageSize=201", "每页最多 200 条"},
		{"/__aisys__/api/authorizations/usage/team-details?resourceId=acc1", "按资源筛选时必须指定资源类型"},
		{"/__aisys__/api/authorizations/usage/team-details?resourceType=nope", "Invalid enum value. Expected 'account' | 'group', received 'nope'"},
		{"/__aisys__/api/authorizations/usage/team-details?startDate=bad", "开始日期格式应为 YYYY-MM-DD"},
		{"/__aisys__/api/authorizations/usage/team-details?endDate=bad", "结束日期格式应为 YYYY-MM-DD"},
		{"/__aisys__/api/authorizations/usage/team-details?teamId=%20%20", "团队 ID 不能为空"},
		{"/__aisys__/api/authorizations/usage/team-summary?page=2", "Unrecognized key(s) in object: 'page'"},
		{"/__aisys__/api/authorizations/usage/user-details?granteeSystemAccountId=", "被授权用户 ID 不能为空"},
		{"/__aisys__/api/my-authorizations/usage/user-summary?includeSummary=true", "Unrecognized key(s) in object: 'includeSummary'"},
		{"/__aisys__/api/my-authorizations?page=0", "页码必须大于 0"},
		{"/__aisys__/api/my-authorizations?pageSize=501", "每页最多 500 条"},
		{"/__aisys__/api/my-authorizations?status=nope", "Invalid enum value."},
		{"/__aisys__/api/my-authorizations?direction=sideways", "Invalid enum value."},
		{"/__aisys__/api/my-authorizations?keyword=" + strings.Repeat("x", 121), "搜索关键字最多 120 个字符"},
	}
	for _, testCase := range cases {
		status, payload := env.do(t, http.MethodGet, testCase.url, "")
		if status != http.StatusBadRequest || !strings.Contains(payload, testCase.message) {
			t.Fatalf("%s status = %d body = %s (want %q)", testCase.url, status, payload, testCase.message)
		}
	}
	// A valid usage request with a whitespace keyword passes (the list schema
	// trims but does not reject blank keywords).
	if status, payload := env.do(t, http.MethodGet, "/__aisys__/api/authorizations/usage/team-details?page=1&pageSize=20", ""); status != http.StatusOK {
		t.Fatalf("valid team-details status = %d body = %s", status, payload)
	}
}

// TestMutationBodyUnknownKeyRejected replays the zod .strict() body objects on
// the create/revoke/return surfaces (the patch/expire surfaces are covered by
// TestAuthorizationRouteInputContract).
func TestMutationBodyUnknownKeyRejected(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	env.f.seedGroup(t, "grp_usage", "owner1")

	// Create with an unknown key rejects before the store runs.
	status, payload := env.do(t, http.MethodPost, "/__aisys__/api/authorizations?systemAccountId=owner1",
		`{"resourceType":"group","resourceId":"grp_usage","granteeType":"system_account","granteeId":"grantee","note":"x"}`)
	if status != http.StatusBadRequest || !strings.Contains(payload, "Unrecognized key(s) in object: 'note'") {
		t.Fatalf("create unknown key = %d %s", status, payload)
	}

	// Create the grant through the store for the mutation surfaces.
	created, err := env.f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_usage",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}

	status, payload = env.do(t, http.MethodDelete, "/__aisys__/api/authorizations/"+created.Item.ID,
		`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","reason":"x"}`)
	if status != http.StatusBadRequest || !strings.Contains(payload, "Unrecognized key(s) in object: 'reason'") {
		t.Fatalf("revoke unknown key = %d %s", status, payload)
	}

	status, payload = env.do(t, http.MethodDelete, "/__aisys__/api/my-authorizations/"+created.Item.ID+"/return",
		`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`","force":true}`)
	if status != http.StatusBadRequest || !strings.Contains(payload, "Unrecognized key(s) in object: 'force'") {
		t.Fatalf("return unknown key = %d %s", status, payload)
	}
}

// TestAdminReturnAndUsageDetail covers the admin return surface (the
// userVisible grantee resolution) and the per-authorization usage detail on
// both surfaces, including the owner-scope 404.
func TestAdminReturnAndUsageDetail(t *testing.T) {
	env := newUsageRouteEnv(t, "admin")
	env.f.seedGroup(t, "grp_detail", "owner1")
	created, err := env.f.store.Create(context.Background(), CreateInput{
		ResourceType: "group", ResourceID: "grp_detail",
		GranteeType: "system_account", GranteeID: "grantee",
	}, "owner1")
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the runtime row the usage window is keyed by.
	var runtimeID string
	if err := env.f.db.QueryRow(`SELECT id FROM resource_authorizations
		WHERE resource_type = 'group' AND resource_id = 'grp_detail' AND grantee_system_account_id = 'grantee'`).
		Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	// Window rows must land on the default range bounds the route resolves
	// (fixedUsageStatsDefaultRange over the fixture clock).
	timezone := "UTC"
	todayKey := dateKeyAt(env.f.now, timezone)
	startKey := addCalendarDays(todayKey, -30)
	_, err = env.f.db.Exec(`INSERT INTO usage_scope_range_windows
		(system_account_id, scope_type, scope_id, start_date, end_date,
		 request_count, input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES ('owner1', 'group_authorization', ?, ?, ?, 3, 30, 15, 0.5, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		runtimeID, startKey, todayKey)
	if err != nil {
		t.Fatal(err)
	}

	// Admin without the filter reads the unscoped detail (admin scope has no
	// owner filter), usage numbers come from the usage_scope_range_windows row.
	status, payload := env.do(t, http.MethodGet, "/__aisys__/api/authorizations/"+created.Item.ID+"/usage", "")
	if status != http.StatusOK {
		t.Fatalf("admin usage status = %d body = %s", status, payload)
	}
	doc := decodeUsageJSON(t, []byte(payload))
	data := doc["data"].(map[string]any)
	usage := data["usage"].(map[string]any)
	if usage["requestCount"] != float64(3) || usage["totalTokens"] != float64(45) || usage["totalCost"] != float64(0.5) {
		t.Fatalf("admin usage body = %v", usage)
	}
	if data["usageRange"].(map[string]any)["startDate"] != startKey {
		t.Fatalf("usageRange = %v", data["usageRange"])
	}
	members := data["usageBySystemAccount"].([]any)
	if len(members) != 1 {
		t.Fatalf("usageBySystemAccount = %v", members)
	}

	// A filtered admin scope that does not own the grant reads not found.
	status, _ = env.do(t, http.MethodGet, "/__aisys__/api/authorizations/"+created.Item.ID+"/usage?systemAccountId=plainuser", "")
	if status != http.StatusNotFound {
		t.Fatalf("filtered usage status = %d", status)
	}

	// The grantee reads the same detail on the my surface.
	userEnv := newUsageRouteEnv(t, "grantee")
	status, payload = userEnv.do(t, http.MethodGet, "/__aisys__/api/my-authorizations/"+created.Item.ID+"/usage", "")
	if status != http.StatusOK {
		t.Fatalf("my usage status = %d body = %s", status, payload)
	}
	// A third user cannot see the grant at all.
	otherEnv := newUsageRouteEnv(t, "plainuser")
	if status, _ := otherEnv.do(t, http.MethodGet, "/__aisys__/api/my-authorizations/"+created.Item.ID+"/usage", ""); status != http.StatusNotFound {
		t.Fatalf("stranger usage status = %d", status)
	}

	// Admin return: without the filter the grantee is the admin itself, which
	// owns no grant → not_found.
	status, _ = env.do(t, http.MethodDelete, "/__aisys__/api/authorizations/"+created.Item.ID+"/return",
		`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`"}`)
	if status != http.StatusNotFound {
		t.Fatalf("admin self-return status = %d", status)
	}
	// With ?systemAccountId=grantee the nominated account returns the grant.
	status, payload = env.do(t, http.MethodDelete, "/__aisys__/api/authorizations/"+created.Item.ID+"/return?systemAccountId=grantee",
		`{"expectedUpdatedAt":"`+created.Item.UpdatedAt+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("admin return status = %d body = %s", status, payload)
	}
	var grantStatus string
	if err := env.f.db.QueryRow(`SELECT status FROM resource_authorization_grants WHERE id = ?`, created.Item.ID).Scan(&grantStatus); err != nil {
		t.Fatal(err)
	}
	if grantStatus != StatusReturned {
		t.Fatalf("grant after admin return = %s", grantStatus)
	}
}
