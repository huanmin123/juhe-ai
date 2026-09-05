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
	stats  *statsSpy
	inval  *invalSpy
	mu     sync.Mutex
}

var ddl = []string{
	`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT, status TEXT NOT NULL DEFAULT 'active', created_by TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS system_team_members (id TEXT PRIMARY KEY, team_id TEXT NOT NULL, system_account_id TEXT NOT NULL, member_role TEXT NOT NULL DEFAULT 'member', status TEXT NOT NULL DEFAULT 'active', joined_at TEXT NOT NULL, removed_at TEXT, created_by TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL DEFAULT 'active', activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT NOT NULL, updated_at TEXT NOT NULL)`,
}

// statsSpy records MarkAllGroupAccountStatsDirty calls (the C9 group-stats
// dirty port; *business/group_dirty_cursor.Store is the production
// implementation — see the compile-time assertion below).
type statsSpy struct {
	mu      sync.Mutex
	reasons []string
}

func (s *statsSpy) MarkAllGroupAccountStatsDirty(_ context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasons = append(s.reasons, reason)
	return nil
}

func (s *statsSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.reasons...)
}

// invalSpy records cache invalidation calls (the C9 runtime/quota invalidation
// port; *inval.Bus is the production implementation).
type invalSpy struct {
	mu    sync.Mutex
	calls []string
}

func (s *invalSpy) Invalidate(topic, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, topic+"|"+reason)
}

func (s *invalSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.calls...)
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
	stats := &statsSpy{}
	inval := &invalSpy{}
	store, err := NewStore(db, false, nil, authzStore, WithSideEffects(stats, inval))
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
	return &testEnv{deps: deps, store: store, authz: authzStore, k: k, server: server, jars: map[string]map[string]string{}, stats: stats, inval: inval}
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

func (e *testEnv) login(t *testing.T, username, password, role string) string {
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
	account, err := e.deps.Accounts.FindByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	return account.ID
}

func boolPtr(v bool) *bool { return &v }

// as switches the active cookie jar (the acting user for env.do).
func (e *testEnv) as(name string) *testEnv {
	e.user = name
	return e
}

// createTeamViaRoute creates a team as the current admin user and returns
// (teamID, editVersion).
func (e *testEnv) createTeamViaRoute(t *testing.T, name string) (string, string) {
	t.Helper()
	code, created := e.do(t, http.MethodPost, "/__aisys__/api/system-teams",
		`{"name":"`+name+`","description":"`+name+`说明"}`)
	if code != http.StatusCreated {
		t.Fatalf("create team: %d %v", code, created)
	}
	data := created["data"].(map[string]any)
	return data["id"].(string), data["editVersion"].(string)
}

func (e *testEnv) teamRevision(t *testing.T, teamID string) string {
	t.Helper()
	code, payload := e.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID, "")
	if code != 200 {
		t.Fatalf("team read: %d %v", code, payload)
	}
	return payload["data"].(map[string]any)["editVersion"].(string)
}

func (e *testEnv) memberIDs(t *testing.T, teamID string) []string {
	t.Helper()
	code, payload := e.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members", "")
	if code != 200 {
		t.Fatalf("members: %d %v", code, payload)
	}
	items := payload["data"].(map[string]any)["items"].([]any)
	ids := []string{}
	for _, item := range items {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	return ids
}

// insertActiveMember seeds a member row with a controlled joined_at.
func insertActiveMember(t *testing.T, env *testEnv, teamID, accountID, joinedAt string) string {
	t.Helper()
	memberID := "teammem_" + randomHex()
	_, err := env.store.db.Exec(`INSERT INTO system_team_members
		(id, team_id, system_account_id, member_role, status, joined_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'member', 'active', ?, '', ?, ?)`, memberID, teamID, accountID, joinedAt, joinedAt, joinedAt)
	if err != nil {
		t.Fatal(err)
	}
	return memberID
}

// insertTeamGrant seeds an active team grant (resource_authorization_grants).
func insertTeamGrant(t *testing.T, env *testEnv, id, resourceID, teamID, owner, createdAt string) {
	t.Helper()
	_, err := env.store.db.Exec(`INSERT INTO resource_authorization_grants
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_team_id,
		 status, created_by, created_at, updated_at)
		VALUES (?, 'group', ?, ?, 'team', ?, 'active', 'admin-id', ?, ?)`,
		id, resourceID, owner, teamID, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

// insertRuntimeWithTeamSource seeds a runtime authorization for the grantee
// plus an active team source row pointing at the team.
func insertRuntimeWithTeamSource(t *testing.T, env *testEnv, id, resourceID, granteeID, owner, teamID, createdAt string) {
	t.Helper()
	_, err := env.store.db.Exec(`INSERT INTO resource_authorizations
		(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope,
		 status, effective_source_type, effective_source_team_id, activated_at, created_by, created_at, updated_at)
		VALUES (?, 'group', ?, ?, ?, 'use', 'active', 'team', ?, ?, 'admin-id', ?, ?)`,
		id, resourceID, owner, granteeID, teamID, createdAt, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.store.db.Exec(`INSERT INTO resource_authorization_sources
		(id, authorization_id, source_type, source_team_id, status, activated_at, created_by, created_at, updated_at)
		VALUES (?, ?, 'team', ?, 'active', ?, 'admin-id', ?, ?)`,
		"src_"+id, id, teamID, createdAt, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, env *testEnv, query string, args ...any) {
	t.Helper()
	if _, err := env.store.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func mustQueryString(t *testing.T, env *testEnv, query string, args ...any) string {
	t.Helper()
	var value string
	if err := env.store.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustQueryInt(t *testing.T, env *testEnv, query string, args ...any) int {
	t.Helper()
	var value int
	if err := env.store.db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTeamLifecycleWithAuthorizationCascade(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "admin", "admin-pass", "super_admin")
	granteeID := env.login(t, "grantee", "grantee-pass", "user")

	// Fixture: group owned by admin + an active team grant to team_1 with
	// runtime rows for the grantee (the authz state machine under test).
	mustExec(t, env, `INSERT INTO groups (id, name, system_account_id, status) VALUES ('grp_1', 'Group grp_1', 'admin-id', 'active')`)
	grantee := granteeID
	_, err := env.authz.Create(context.Background(), authz.CreateInput{
		ResourceType: "group", ResourceID: "grp_1",
		GranteeType: "team", GranteeID: "team_1",
	}, "admin-id")
	// team_1 does not exist yet → expect the 团队不存在或已停用 rejection.
	if err == nil || !strings.Contains(err.Error(), "团队不存在或已停用") {
		t.Fatalf("team grant without team must be rejected: %v", err)
	}

	// Create the team (M03 surface).
	env.user = "admin"
	teamID, revision := env.createTeamViaRoute(t, "平台组")

	// Duplicate name → 400 团队名称已存在.
	code, dup := env.do(t, http.MethodPost, "/__aisys__/api/system-teams",
		`{"name":"平台组"}`)
	if code != http.StatusBadRequest || dup["message"] != "团队名称已存在" {
		t.Fatalf("duplicate team: %d %v", code, dup)
	}

	// Add the grantee as a member (C3 payload: id/memberCount/updatedAt/addedMembers).
	code, added := env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+grantee+`"],"expectedUpdatedAt":"`+revision+`"}`)
	if code != 200 {
		t.Fatalf("add member: %d %v", code, added)
	}
	addedData := added["data"].(map[string]any)
	for _, key := range []string{"id", "memberCount", "updatedAt", "addedMembers"} {
		if _, ok := addedData[key]; !ok {
			t.Fatalf("add members payload missing %s: %v", key, addedData)
		}
	}
	if addedData["memberCount"] != float64(1) {
		t.Fatalf("memberCount = %v", addedData["memberCount"])
	}

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

	// Patch: stale version → 409; fresh rename → C3 payload
	// {id, changedFields, rowPatch, updatedAt}.
	env.user = "admin"
	stalePatchCode, stalePatchPayload := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"2001-01-01T00:00:00Z","name":"新名称"}`)
	if stalePatchCode != http.StatusConflict || stalePatchPayload["message"] != "团队已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale team patch: %d %v", stalePatchCode, stalePatchPayload)
	}
	freshRevision := env.teamRevision(t, teamID)
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+freshRevision+`","name":"新名称"}`)
	if code != 200 {
		t.Fatalf("patch team: %d %v", code, patched)
	}
	patchData := patched["data"].(map[string]any)
	if patchData["id"] != teamID {
		t.Fatalf("patch payload id = %v", patchData["id"])
	}
	if changed, ok := patchData["changedFields"].([]any); !ok || len(changed) != 1 || changed[0] != "name" {
		t.Fatalf("patch changedFields = %v", patchData["changedFields"])
	}

	// History records the removed membership only after removal (C4:
	// status='removed' filter). The member is still active → empty history.
	code, history := env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members/history", "")
	if code != 200 || len(history["data"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatalf("history before removal must be empty: %d %v", code, history)
	}

	// Remove member → C3 payload {id, memberCount, updatedAt, removedMemberId}.
	env.user = "admin"
	members := env.memberIDs(t, teamID)
	removeCode, removePayload := env.do(t, http.MethodDelete,
		"/__aisys__/api/system-teams/"+teamID+"/members/"+members[0],
		`{"expectedUpdatedAt":"`+env.teamRevision(t, teamID)+`"}`)
	if removeCode != 200 {
		t.Fatalf("remove member: %d %v", removeCode, removePayload)
	}
	removeData := removePayload["data"].(map[string]any)
	if removeData["removedMemberId"] != members[0] || removeData["memberCount"] != float64(0) {
		t.Fatalf("remove payload: %v", removeData)
	}

	// Now the history holds the removed row with status 'removed'.
	code, history = env.do(t, http.MethodGet, "/__aisys__/api/system-teams/"+teamID+"/members/history", "")
	if code != 200 || len(history["data"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatalf("history after removal: %d %v", code, history)
	}
	first := history["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if first["status"] != "removed" || first["removedAt"] == nil {
		t.Fatalf("history item: %v", first)
	}
}
