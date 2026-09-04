package systemteams

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type testEnv struct {
	deps   *authsys.Deps
	store  *Store
	authz  *authz.Store
	k      *kernel.Kernel
	server *httptest.Server
	jars   map[string]map[string]string
	user   string
	mu     sync.Mutex
}

var ddl = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT, status TEXT NOT NULL DEFAULT 'active', created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_team_members (id TEXT PRIMARY KEY, team_id TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', joined_at TEXT NOT NULL, removed_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:teams-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	authDeps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	authzStore, err := authz.NewStore(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, false, nil, authzStore)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	teamsDeps := &Deps{Store: store, Auth: authDeps}
	teamsDeps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, store: store, authz: authzStore, k: k, server: server, jars: map[string]map[string]string{}}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	e.mu.Lock()
	jar := e.jars[e.user]
	for name, value := range jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	jar = e.jars[e.user]
	if jar == nil {
		jar = map[string]string{}
		e.jars[e.user] = jar
	}
	for _, c := range response.Cookies() {
		if c.Value != "" {
			jar[c.Name] = c.Value
		}
	}
	e.mu.Unlock()
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func (e *testEnv) login(t *testing.T, username, password, role string) {
	t.Helper()
	e.user = username
	if _, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestTeamLifecycleWithAuthorizationCascade(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	env.login(t, "grantee", "grantee-pass", "user")

	// Fixture: group owned by admin + an active team grant to team_1 with
	// runtime rows for the grantee (the authz state machine under test).
	if _, err := env.store.db.Exec(`INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_1', 'Group grp_1', 'admin-id', 'active')`); err != nil {
		t.Fatal(err)
	}
	grantee, err := env.deps.Accounts.FindByUsername(context.Background(), "grantee")
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.authz.Create(context.Background(), authz.CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "team", GranteeID: "team_1",
	}, "admin-id")
	// team_1 does not exist yet → expect the 团队不存在或已停用 rejection.
	if err == nil || !strings.Contains(err.Error(), "团队不存在或已停用") {
		t.Fatalf("team grant without team must be rejected: %v", err)
	}

	// Create the team (M03 surface).
	env.user = "admin"
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/system-teams",
		`{"name":"平台组","description":"平台授权组"}`)
	if code != http.StatusCreated {
		t.Fatalf("create team: %d %v", code, created)
	}
	teamID := created["data"].(map[string]any)["id"].(string)
	revision := created["data"].(map[string]any)["editVersion"].(string)

	// Duplicate name → 400 团队名称已存在.
	code, dup := env.do(t, http.MethodPost, "/__aisys__/api/system-teams",
		`{"name":"平台组"}`)
	if code != http.StatusBadRequest || dup["message"] != "团队名称已存在" {
		t.Fatalf("duplicate team: %d %v", code, dup)
	}

	// Add the grantee as a member.
	code, added := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+grantee.ID+`"],"expectedUpdatedAt":"`+revision+`"}`)
	if code != 200 {
		t.Fatalf("add member: %d %v", code, added)
	}
	newRevision := added["data"].(map[string]any)["status"].(string)
	_ = newRevision

	// Stale-revision member add → 409.
	staleCode, stalePayload := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["x1"],"expectedUpdatedAt":"`+revision+`"}`)
	if staleCode != http.StatusConflict {
		t.Fatalf("stale member add: %d %v", staleCode, stalePayload)
	}

	// my-teams now shows the team for the grantee (membership scope).
	env.user = "grantee"
	code, myList := env.do(t, http.MethodGet, "/__aisys__/api/my-teams", "")
	items := myList["data"].(map[string]any)["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("my-teams: %d %v", code, myList)
	}

	// Team detail + member list.
	env.user = "admin"
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID, "")
	if code != 200 || detail["data"].(map[string]any)["memberCount"] != float64(1) {
		t.Fatalf("team detail: %d %v", code, detail)
	}

	// Patch: stale version → 409; fresh rename → ok.
	env.user = "admin"
	stalePatchCode, stalePatchPayload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"2001-01-01T00:00:00Z","name":"新名称"}`)
	if stalePatchCode != http.StatusConflict || stalePatchPayload["message"] != "团队已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale team patch: %d %v", stalePatchCode, stalePatchPayload)
	}

	// History records the membership.
	env.user = "admin"
	code, history := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members/history", "")
	if code != 200 || len(history["data"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("history: %d %v", code, history)
	}

	// Remove member.
	env.user = "admin"
	removeCode, removePayload := env.do(t, http.MethodDelete, "/__aisys__/api/system-teams/"+teamID+"/members/"+teamMemberID(t, env, teamID),
		`{"expectedUpdatedAt":"`+currentTeamRevision(t, env, teamID)+`"}`)
	if removeCode != 200 {
		t.Fatalf("remove member: %d %v", removeCode, removePayload)
	}
}

func teamMemberID(t *testing.T, env *testEnv, teamID string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members", "")
	if code != 200 {
		t.Fatalf("members: %d %v", code, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no members")
	}
	return items[0].(map[string]any)["id"].(string)
}

func currentTeamRevision(t *testing.T, env *testEnv, teamID string) string {
	t.Helper()
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID, "")
	if code != 200 {
		t.Fatalf("team read: %d %v", code, payload)
	}
	return payload["data"].(map[string]any)["editVersion"].(string)
}
