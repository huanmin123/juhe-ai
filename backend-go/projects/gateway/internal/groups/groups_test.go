package groups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, entry := range s.entries {
		out = append(out, entry.Module+"."+entry.Action)
	}
	return out
}

type recordingInvalidator struct {
	mu      sync.Mutex
	reasons []string
}

func (i *recordingInvalidator) Invalidate(_ string, reason string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.reasons = append(i.reasons, reason)
}

func (i *recordingInvalidator) has(reason string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, candidate := range i.reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}

type testEnv struct {
	deps   *authsys.Deps
	k      *kernel.Kernel
	server *httptest.Server
	jar    map[string]string
	mu     sync.Mutex
	sink   *recordingSink
	inval  *recordingInvalidator
	db     *sql.DB
	authz  *authz.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:groups-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, deleted_at TEXT, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name)`,
		`CREATE TABLE IF NOT EXISTS group_accounts (system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (group_id, account_id))`,
		`CREATE TABLE IF NOT EXISTS group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Real resource_authorizations shape (Node business-schema.ts, same
		// DDL the authz slice tests use).
		`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', effective_source_type TEXT, effective_source_team_id TEXT, activated_at TEXT, last_source_changed_at TEXT, remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, revoked_reason TEXT, updated_at TEXT NOT NULL)`,
		// Sources read by the list/detail authorization summary (Node
		// authorization-read-loaders.ts).
		`CREATE TABLE IF NOT EXISTS resource_authorization_sources (id TEXT PRIMARY KEY, authorization_id TEXT NOT NULL, source_type TEXT NOT NULL, source_team_id TEXT, status TEXT NOT NULL, activated_at TEXT, ended_at TEXT, ended_reason TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_teams (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
		`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		// Return-authorization route surfaces (the authz return domain reads
		// and writes these tables through internal/authz).
		`CREATE TABLE IF NOT EXISTS resource_authorization_grants (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, resource_owner_system_account_id TEXT NOT NULL, grantee_type TEXT NOT NULL, grantee_system_account_id TEXT, grantee_team_id TEXT, scope TEXT NOT NULL DEFAULT 'use', status TEXT NOT NULL DEFAULT 'active', remark TEXT, expires_at TEXT, limits_json TEXT, created_by TEXT NOT NULL, created_at TEXT NOT NULL, revoked_by TEXT, revoked_at TEXT, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO providers (code, enabled) VALUES ('openai', 1), ('anthropic', 1), ('disabled-provider', 0)`); err != nil {
		t.Fatal(err)
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	sink := &recordingSink{}
	invalidator := &recordingInvalidator{}
	store, err := NewStore(db, false, nil, nil, invalidator)
	if err != nil {
		t.Fatal(err)
	}
	authzStore, err := authz.NewStore(db, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink, Authz: authzStore}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, authz: authzStore}
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
	for name, value := range e.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	e.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	for _, c := range response.Cookies() {
		if c.Value != "" {
			e.jar[c.Name] = c.Value
		} else {
			delete(e.jar, c.Name)
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
	created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
		Username: username, DisplayName: username + "_name", Password: password, Role: role,
		MustChangePassword: boolPtr(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login failed: %d %v", code, payload)
	}
	return created.ID
}

func (e *testEnv) createGroup(t *testing.T, path, name string) (string, map[string]any) {
	t.Helper()
	code, payload := e.do(t, http.MethodPost, path, `{"name":"`+name+`","providerCode":"openai"}`)
	if code != http.StatusCreated {
		t.Fatalf("create %s via %s: %d %v", name, path, code, payload)
	}
	data := payload["data"].(map[string]any)
	return data["id"].(string), data
}

func (e *testEnv) exec(t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := e.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (e *testEnv) bindAccount(t *testing.T, ownerID, groupID, accountID, bindingStatus string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO accounts (id, system_account_id, deleted_at, created_at) VALUES (?, ?, NULL, ?)`, accountID, ownerID, now)
	e.exec(t, `INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		ownerID, groupID, accountID, now, now)
	_ = bindingStatus
}

func (e *testEnv) bindRouteStrategy(t *testing.T, strategyID, ownerID, groupID, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e.exec(t, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		strategyID+"-"+groupID, strategyID, ownerID, groupID, status, now, now)
}

func boolPtr(v bool) *bool { return &v }

// storedPolicyJSON builds a full strict stored scheduling policy document
// (the shape Node parseGroupSchedulingPolicyJson demands on read: every
// stored key present, only stored keys present).
func storedPolicyJSON(t *testing.T, overrides map[string]any) string {
	t.Helper()
	policy := map[string]any{
		"mode":                            "balanced_fast",
		"defaultSoftConcurrency":          5000,
		"fastFirstEnabled":                true,
		"fallbackOnQueueEnabled":          true,
		"breakAffinityOnSoftLimit":        true,
		"breakAffinityOnQueueWaitMs":      0,
		"slowRequestThresholdMs":          30_000,
		"firstOutputSlowThresholdMs":      15_000,
		"recentTimeoutWindowSeconds":      120,
		"recentTimeoutPenaltyThreshold":   2,
		"maxQueueWaitMs":                  60_000,
		"maxQueueSize":                    5000,
		"perApiKeyQueueLimit":             5000,
		"clientIpConcurrencyLimit":        0,
		"clientIpConcurrencyOverflowMode": "reject",
		"imageLaneMaxConcurrency":         0,
	}
	for key, value := range overrides {
		policy[key] = value
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func TestGroupAdminLifecycle(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	// Create (201) with contract fields.
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"alpha","providerCode":"openai","description":"首个分组"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	created := dataMap(t, payload)
	alphaID := created["id"].(string)
	if created["name"] != "alpha" || created["providerCode"] != "openai" ||
		created["isDefault"] != false || created["canEdit"] != true || created["canDelete"] != true ||
		created["groupType"] != "personal" || created["accessType"] != "owner" ||
		created["ownerSystemAccountId"] != adminID || created["canReturn"] != false {
		t.Fatalf("create payload: %v", created)
	}
	// The create list-item projection carries the eight counter accountStats
	// only (no todayUsage/usage keys) and no memberCount/accountIds.
	createStats := created["accountStats"].(map[string]any)
	if len(createStats) != 8 || createStats["currentConcurrency"] != float64(0) {
		t.Fatalf("create accountStats: %v", createStats)
	}
	if _, present := created["memberCount"]; present {
		t.Fatalf("create payload must not carry memberCount: %v", created)
	}
	if _, present := created["schedulingPolicy"]; present {
		t.Fatalf("personal group list item must omit schedulingPolicy: %v", created["schedulingPolicy"])
	}

	// Idempotent create → 409 (dedup guard).
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"alpha","providerCode":"openai","description":"首个分组"}`)
	if code != http.StatusConflict {
		t.Fatalf("idempotent create: %d %v", code, duplicate)
	}

	// Second group + rename onto an existing name → store-level 409.
	bravoID, _ := env.createGroup(t, "/__aisys__/api/groups", "bravo")
	bravoDetailCode, bravoDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+bravoID, "")
	if bravoDetailCode != 200 {
		t.Fatalf("bravo detail: %d %v", bravoDetailCode, bravoDetail)
	}
	bravoUpdatedAt := dataMap(t, bravoDetail)["updatedAt"].(string)
	code, renamed := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+bravoID,
		`{"expectedUpdatedAt":"`+bravoUpdatedAt+`","name":"alpha"}`)
	if code != http.StatusConflict || !strings.Contains(renamed["message"].(string), "分组名称已存在") {
		t.Fatalf("rename onto existing name: %d %v", code, renamed)
	}

	// Provider validation.
	code, badProvider := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"gamma","providerCode":"missing"}`)
	if code != http.StatusBadRequest || badProvider["message"] != "不支持的供应商：missing" {
		t.Fatalf("unknown provider: %d %v", code, badProvider)
	}
	code, disabledProvider := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"gamma","providerCode":"disabled-provider"}`)
	if code != http.StatusBadRequest || disabledProvider["message"] != "供应商已停用：disabled-provider" {
		t.Fatalf("disabled provider: %d %v", code, disabledProvider)
	}

	// High-concurrency group carries a defaulted policy.
	code, hc := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"hc","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":100}}`)
	if code != http.StatusCreated {
		t.Fatalf("hc create: %d %v", code, hc)
	}
	hcID := dataMap(t, hc)["id"].(string)
	code, hcDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+hcID, "")
	policy := dataMap(t, hcDetail)["schedulingPolicy"].(map[string]any)
	if policy["defaultSoftConcurrency"] != float64(100) || policy["mode"] != "balanced_fast" || policy["maxQueueWaitMs"] != float64(60_000) {
		t.Fatalf("hc policy: %v", policy)
	}

	// Pagination + keyword prefix (alpha, bravo, hc).
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/groups?page=1&pageSize=2", "")
	page1Data := dataMap(t, page1)
	if code != 200 || len(page1Data["items"].([]any)) != 2 || page1Data["hasMore"] != true || page1Data["total"] != float64(3) {
		t.Fatalf("page 1: %d %v", code, page1)
	}
	code, page2 := env.do(t, http.MethodGet, "/__aisys__/api/groups?page=2&pageSize=2", "")
	page2Data := dataMap(t, page2)
	if code != 200 || len(page2Data["items"].([]any)) != 1 || page2Data["hasMore"] != false || page2Data["total"] != float64(3) {
		t.Fatalf("page 2: %d %v", code, page2)
	}
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/groups?keyword=al", "")
	filteredItems := dataMap(t, filtered)["items"].([]any)
	if code != 200 || len(filteredItems) != 1 || filteredItems[0].(map[string]any)["name"] != "alpha" {
		t.Fatalf("keyword filter: %d %v", code, filtered)
	}

	// Detail + members.
	env.bindAccount(t, adminID, alphaID, "acc-1", "active")
	env.bindAccount(t, adminID, alphaID, "acc-2", "active")
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+alphaID, "")
	detailData := dataMap(t, detail)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, detail)
	}
	accountIDs := detailData["accountIds"].([]any)
	if len(accountIDs) != 2 || accountIDs[0] != "acc-1" {
		t.Fatalf("accountIds: %v", accountIDs)
	}
	// The owner summary overrides accountStats.total with the member count
	// (Node buildGroupSummaries includeAccountIds branch).
	if detailData["accountStats"].(map[string]any)["total"] != float64(2) {
		t.Fatalf("detail accountStats.total: %v", detailData["accountStats"])
	}
	if _, present := detailData["memberCount"]; present {
		t.Fatalf("detail payload must not carry memberCount: %v", detailData)
	}
	if detailData["permissions"].(map[string]any)["canDelete"] != true {
		t.Fatalf("owner permissions: %v", detailData["permissions"])
	}

	// Optimistic locking.
	updatedAt := detailData["updatedAt"].(string)
	code, stale := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"2000-01-01T00:00:00.000Z","name":"alpha-renamed"}`)
	if code != http.StatusConflict || stale["message"] != "分组已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", code, stale)
	}
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"alpha-renamed","description":"更新说明","groupType":"high_concurrency"}`)
	if code != 200 {
		t.Fatalf("fresh patch: %d %v", code, patched)
	}
	patchData := dataMap(t, patched)
	changed := map[string]bool{}
	for _, field := range patchData["changedFields"].([]any) {
		changed[field.(string)] = true
	}
	if !changed["name"] || !changed["description"] || !changed["groupType"] || len(changed) != 3 {
		t.Fatalf("changedFields: %v", patchData["changedFields"])
	}
	nextUpdatedAt := patchData["updatedAt"].(string)
	if nextUpdatedAt == updatedAt {
		t.Fatal("patch must bump updatedAt")
	}

	// No-change patch returns the current version with no fields.
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`"}`)
	if code != 400 || noop["message"] != "请提供要修改的分组内容" {
		t.Fatalf("empty patch: %d %v", code, noop)
	}

	// Provider change blocked while the group owns enabled accounts.
	code, providerBlocked := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","providerCode":"anthropic"}`)
	if code != http.StatusBadRequest || providerBlocked["message"] != "已有账户的分组不允许修改供应商" {
		t.Fatalf("provider change blocked: %d %v", code, providerBlocked)
	}

	// Route-strategy binding guard on disable.
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-1', ?, '策略一', 'active')`, adminID)
	env.bindRouteStrategy(t, "rs-1", adminID, alphaID, "active")
	code, disableBlocked := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+alphaID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","enabled":false}`)
	if code != http.StatusBadRequest || !strings.Contains(disableBlocked["message"].(string), "唯一可用启用分组") {
		t.Fatalf("disable blocked: %d %v", code, disableBlocked)
	}

	// Default-group patch guard.
	defaultID := "grp-default"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, '默认分组', 'openai', 1, 1, 'personal', ?, ?)`, defaultID, adminID, now, now)
	code, defaultBlocked := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+defaultID,
		`{"expectedUpdatedAt":"`+now+`","name":"renamed-default"}`)
	if code != http.StatusBadRequest || defaultBlocked["message"] != "默认分组不允许修改" {
		t.Fatalf("default patch: %d %v", code, defaultBlocked)
	}

	// Operation log entries for create.
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["groups.create"] || !seen["groups.update"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}
	if !env.inval.has("group_created") || !env.inval.has("group_updated") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
}

func TestGroupDeleteCascadeAndGuards(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupID, _ := env.createGroup(t, "/__aisys__/api/groups", "doomed")

	// Default-group delete guard.
	code, defaultBlocked := env.do(t, http.MethodDelete, "/__aisys__/api/groups/grp-default", "")
	if code != http.StatusNotFound {
		t.Fatalf("missing group delete must 404: %d %v", code, defaultBlocked)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES ('grp-default', ?, '默认分组', 'openai', 1, 1, 'personal', ?, ?)`, adminID, now, now)
	code, defaultGuard := env.do(t, http.MethodDelete, "/__aisys__/api/groups/grp-default", "")
	if code != http.StatusBadRequest || defaultGuard["message"] != "默认分组不能删除" {
		t.Fatalf("default delete guard: %d %v", code, defaultGuard)
	}

	// Sole active binding of an active strategy blocks the delete.
	env.exec(t, `INSERT INTO route_strategies (id, system_account_id, name, status) VALUES ('rs-del', ?, '删除策略', 'active')`, adminID)
	env.bindRouteStrategy(t, "rs-del", adminID, groupID, "active")
	code, deleteBlocked := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusBadRequest || !strings.Contains(deleteBlocked["message"].(string), "唯一可用启用分组") {
		t.Fatalf("delete blocked: %d %v", code, deleteBlocked)
	}

	// Adding a second active enabled binding unblocks the delete.
	env.createGroup(t, "/__aisys__/api/groups", "survivor")
	survivorDetailCode, survivorDetail := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if survivorDetailCode != 200 {
		t.Fatalf("list: %d %v", survivorDetailCode, survivorDetail)
	}
	var survivorID string
	for _, item := range dataMap(t, survivorDetail)["items"].([]any) {
		itemMap := item.(map[string]any)
		if itemMap["name"] == "survivor" {
			survivorID = itemMap["id"].(string)
		}
	}
	if survivorID == "" {
		t.Fatal("survivor group missing")
	}
	env.bindRouteStrategy(t, "rs-del", adminID, survivorID, "active")

	// Cascade payloads.
	env.bindAccount(t, adminID, groupID, "acc-del", "active")
	env.exec(t, `INSERT INTO group_authorization_settings (authorization_id, system_account_id, group_id, enabled, group_type, created_at, updated_at)
		VALUES ('authz-1', ?, ?, 1, 'personal', ?, ?)`, adminID, groupID, now, now)

	code, deletePayload := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, deletePayload)
	}
	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNotFound || gone["message"] != "分组不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	if env.count(t, `SELECT COUNT(*) FROM group_accounts WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("group_accounts must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_authorization_settings WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("group_authorization_settings must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE group_id = ?`, groupID) != 0 {
		t.Fatal("route_strategy_groups must cascade")
	}
	if env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty WHERE group_id = ? AND reason = 'group_deleted'`, groupID) != 1 {
		t.Fatal("stats dirty row must be marked")
	}
	if !env.inval.has("group_deleted") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}
	seen := map[string]bool{}
	for _, action := range env.sink.actions() {
		seen[action] = true
	}
	if !seen["groups.delete"] {
		t.Fatalf("operation log actions: %v", env.sink.actions())
	}

	// Delete again → 404.
	code, again := env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+groupID, "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d %v", code, again)
	}
}

func TestGroupMyGroupsSelfScope(t *testing.T) {
	env := newTestEnv(t)
	userAID := env.login(t, "alice", "alice-pass", "user")
	aliceGroupID, created := env.createGroup(t, "/__aisys__/api/my-groups", "alice-group")
	if created["ownerSystemAccountId"] != userAID {
		t.Fatalf("my-groups create owner: %v", created)
	}

	// userB cannot see or mutate alice's group via my-*.
	env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-groups/"+aliceGroupID, "")
	if code != http.StatusNotFound || forbidden["message"] != "分组不存在" {
		t.Fatalf("bob read alice group: %d %v", code, forbidden)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/my-groups/"+aliceGroupID,
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","name":"hijacked"}`)
	if code != http.StatusNotFound {
		t.Fatalf("bob patch alice group: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/my-groups/"+aliceGroupID, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob delete alice group: %d", code)
	}

	// Bob's list only shows bob's own groups.
	bobGroupID, _ := env.createGroup(t, "/__aisys__/api/my-groups", "bob-group")
	code, bobList := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	items := dataMap(t, bobList)["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != bobGroupID {
		t.Fatalf("bob list: %d %v", code, bobList)
	}

	// Bob (user role) cannot reach the admin surface.
	code, adminDenied := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != http.StatusForbidden || adminDenied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, adminDenied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"name":"x","providerCode":"openai"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin create as user: %d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+bobGroupID, `{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","name":"y"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin patch as user: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/groups/"+bobGroupID, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete as user: %d", code)
	}

	// Admin /my-groups is forced to self scope too (admin-owned rows only).
	env.login(t, "root", "root-pass", "super_admin")
	code, adminSelf := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	if code != 200 || len(dataMap(t, adminSelf)["items"].([]any)) != 0 {
		t.Fatalf("admin my-groups must be self-scoped: %d %v", code, adminSelf)
	}
	// Admin surface sees everything (systemAccountId filter supported).
	code, adminAll := env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != 200 || len(dataMap(t, adminAll)["items"].([]any)) != 2 {
		t.Fatalf("admin list: %d %v", code, adminAll)
	}
}

func TestGroupPermissionMatrixAnonymousAndValidation(t *testing.T) {
	env := newTestEnv(t)

	// Anonymous access is refused on both surfaces.
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/my-groups", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my-groups: %d %v", code, anonymous)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/groups", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous groups: %d", code)
	}

	env.login(t, "root", "root-pass", "super_admin")
	code, invalid := env.do(t, http.MethodPost, "/__aisys__/api/groups", `{"providerCode":"openai"}`)
	if code != http.StatusBadRequest || invalid["message"] != "分组参数无效" {
		t.Fatalf("missing name: %d %v", code, invalid)
	}
	code, unknownField := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"ok","providerCode":"openai","bogusField":1}`)
	if code != http.StatusBadRequest || unknownField["message"] != "分组参数无效" {
		t.Fatalf("unknown field: %d %v", code, unknownField)
	}
	createdID, _ := env.createGroup(t, "/__aisys__/api/groups", "patched-target")
	code, missingVersion := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID, `{"name":"new-name"}`)
	if code != http.StatusBadRequest || missingVersion["message"] != "分组参数无效" {
		t.Fatalf("missing expectedUpdatedAt: %d %v", code, missingVersion)
	}
	code, badVersion := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID,
		`{"expectedUpdatedAt":"not-a-time","name":"new-name"}`)
	if code != http.StatusBadRequest || badVersion["message"] != "分组参数无效" {
		t.Fatalf("bad expectedUpdatedAt: %d %v", code, badVersion)
	}
	code, badPolicy := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"policy-broken","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":0}}`)
	if code != http.StatusBadRequest || badPolicy["message"] != "分组参数无效" {
		t.Fatalf("bad policy: %d %v", code, badPolicy)
	}
	// Route-level schema rejects null optional fields and null/float/unknown
	// schedulingPolicy sub-keys exactly like the Node zod schema (BUG-0163
	// null/子字段主张)。
	code, nullDescription := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"null-desc","providerCode":"openai","description":null}`)
	if code != http.StatusBadRequest || nullDescription["message"] != "分组参数无效" {
		t.Fatalf("null description: %d %v", code, nullDescription)
	}
	code, nullSubKey := env.do(t, http.MethodPost, "/__aisys__/api/groups",
		`{"name":"null-policy","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"defaultSoftConcurrency":null}}`)
	if code != http.StatusBadRequest || nullSubKey["message"] != "分组参数无效" {
		t.Fatalf("null policy sub-key: %d %v", code, nullSubKey)
	}
	unknownSubKey := `{"name":"unknown-policy","providerCode":"openai","groupType":"high_concurrency","schedulingPolicy":{"bogusKey":1}}`
	code, unknownPolicy := env.do(t, http.MethodPost, "/__aisys__/api/groups", unknownSubKey)
	if code != http.StatusBadRequest || unknownPolicy["message"] != "分组参数无效" {
		t.Fatalf("unknown policy sub-key: %d %v", code, unknownPolicy)
	}
	code, nullEnabledPatch := env.do(t, http.MethodPatch, "/__aisys__/api/groups/"+createdID,
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","enabled":null}`)
	if code != http.StatusBadRequest || nullEnabledPatch["message"] != "分组参数无效" {
		t.Fatalf("null enabled patch: %d %v", code, nullEnabledPatch)
	}

	// Detail 404 for unknown ids on both surfaces.
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/groups/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("admin detail 404: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/my-groups/does-not-exist", "")
	if code != http.StatusNotFound {
		t.Fatalf("my detail 404: %d", code)
	}
}

// TestGroupStoreLevelDuplicateName covers the storage-side unique-name
// conflict mapping (the HTTP dedup guard shadows it for identical payloads).
func TestGroupStoreLevelDuplicateName(t *testing.T) {
	env := newTestEnv(t)
	store, err := NewStore(env.db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	access := AccessScope{ViewerID: "owner-1"}
	first, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("openai"),
	}, access)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("openai"),
	}, access)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Message, "分组名称已存在") {
		t.Fatalf("duplicate create: %v", err)
	}
	// Same name under another provider is allowed.
	_, err = store.Create(context.Background(), MutationInput{
		Name: ptrString("dupe"), ProviderCode: ptrString("anthropic"),
	}, access)
	if err != nil {
		t.Fatalf("other provider create: %v", err)
	}
	detail, err := store.FindDetail(context.Background(), first.ID, access)
	if err != nil || detail == nil || detail.Name != "dupe" {
		t.Fatalf("detail: %v %v", detail, err)
	}
}
