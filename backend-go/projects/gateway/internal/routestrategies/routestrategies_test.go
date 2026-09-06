package routestrategies

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func intPtr(v int) *int { return &v }

func errorsAs(err error, target any) bool { return errors.As(err, target) }

type recordingSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *recordingSink) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, entry := range s.entries {
		out = append(out, entry.OperationKey)
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
	deps            *authsys.Deps
	k               *kernel.Kernel
	server          *httptest.Server
	jar             map[string]string
	mu              sync.Mutex
	sink            *recordingSink
	inval           *recordingInvalidator
	db              *sql.DB
	store           *Store
	validationInval *recordingValidationInvalidator
}

func newTestEnv(t *testing.T) *testEnv {
	return newTestEnvFull(t, nil)
}

// newTestEnvFull wires an optional speed-first runtime facade before Mount so
// the runtime read endpoints register (composition parity).
func newTestEnvFull(t *testing.T, facade SpeedFirstRuntimeFacade) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:routestrategies-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
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
		`CREATE TABLE IF NOT EXISTS group_accounts (system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (group_id, account_id))`,
		`CREATE TABLE IF NOT EXISTS group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', expires_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS group_account_stats_dirty (group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL DEFAULT 'normal', status TEXT NOT NULL DEFAULT 'active', is_default INTEGER NOT NULL DEFAULT 0, config_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique ON route_strategies(system_account_id, name)`,
		`CREATE TABLE IF NOT EXISTS route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 1, weight INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO providers (code, enabled) VALUES ('openai', 1), ('anthropic', 1)`); err != nil {
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
	if facade != nil {
		store.SetSpeedFirstRuntimeFacade(facade)
	}
	validationInval := &recordingValidationInvalidator{}
	store.SetValidationCacheInvalidator(validationInval)
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Store: store, Auth: deps, Sink: sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, k: k, server: server, jar: map[string]string{}, sink: sink, inval: invalidator, db: db, store: store, validationInval: validationInval}
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

// createGroup inserts a group row directly (owner id + enabled flag) and
// returns its id; the tests only exercise routestrategies reads of the groups
// table, so the HTTP groups family is not mounted here.
func (e *testEnv) createGroup(t *testing.T, ownerID, name string, enabled bool) string {
	t.Helper()
	id := "grp-" + name + "-" + strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", "")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	enabledFlag := 0
	if enabled {
		enabledFlag = 1
	}
	e.exec(t, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, ?, 'openai', ?, 0, 'personal', ?, ?)`, id, ownerID, name, enabledFlag, now, now)
	return id
}

func (e *testEnv) createStrategy(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, path, body)
}

// bindingJSON renders one binding entry for request bodies.
func bindingJSON(groupID string, priority int) string {
	if priority > 0 {
		return `{"groupId":"` + groupID + `","priority":` + fmt.Sprint(priority) + `}`
	}
	return `{"groupId":"` + groupID + `"}`
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

func (e *testEnv) strategyUpdatedAt(t *testing.T, id string) string {
	t.Helper()
	var updatedAt string
	if err := e.db.QueryRow(`SELECT updated_at FROM route_strategies WHERE id = ?`, id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	return updatedAt
}

func boolPtr(v bool) *bool { return &v }

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func items(t *testing.T, payload map[string]any) []any {
	t.Helper()
	data := dataMap(t, payload)
	list, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing items: %v", data)
	}
	return list
}

func changedSet(t *testing.T, payload map[string]any) map[string]bool {
	t.Helper()
	changed := map[string]bool{}
	for _, field := range dataMap(t, payload)["changedFields"].([]any) {
		changed[field.(string)] = true
	}
	return changed
}

// hybridConfigBody builds a contiguous 1-10 coverage config with two tiers.
func hybridConfigBody(firstMax int, secondModel string) string {
	return `{"scoringModel":"score-model-a","levelRoutes":[` +
		`{"minLevel":1,"maxLevel":` + fmt.Sprint(firstMax) + `,"targetModel":"model-low"},` +
		`{"minLevel":` + fmt.Sprint(firstMax+1) + `,"maxLevel":10,"targetModel":"` + secondModel + `"}]}`
}

func TestRouteStrategyAdminLifecycle(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)

	// Create (201): normal mode default, one binding, preview present.
	body := `{"name":"策略一","description":"首个策略","groupBindings":[` + bindingJSON(groupA, 0) + `]}`
	code, createdPayload := env.createStrategy(t, "/__aisys__/api/route-strategies", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, createdPayload)
	}
	created := dataMap(t, createdPayload)
	if created["id"] == nil || created["name"] != "策略一" || created["mode"] != "normal" ||
		created["status"] != "active" || created["isDefault"] != false ||
		created["bindingCount"] != float64(1) || created["apiKeyCount"] != float64(0) {
		t.Fatalf("create payload: %v", created)
	}
	preview := created["groupBindingPreview"].([]any)
	if len(preview) != 1 || preview[0].(map[string]any)["groupId"] != groupA ||
		preview[0].(map[string]any)["groupEnabled"] != true {
		t.Fatalf("create preview: %v", preview)
	}
	if created["systemAccountId"] != adminID {
		t.Fatalf("admin create must include systemAccountId: %v", created["systemAccountId"])
	}
	strategyID := created["id"].(string)
	if !strings.HasPrefix(strategyID, "route_strategy_") {
		t.Fatalf("strategy id prefix: %s", strategyID)
	}
	normalRoutingConfig := created["normalRoutingConfig"].(map[string]any)
	if normalRoutingConfig["schedulingPreference"] != "cost_first" {
		t.Fatalf("default scheduling preference: %v", normalRoutingConfig)
	}

	// Idempotent create → 409 (dedup guard, same fingerprint).
	code, duplicate := env.do(t, http.MethodPost, "/__aisys__/api/route-strategies", body)
	if code != http.StatusConflict {
		t.Fatalf("idempotent create: %d %v", code, duplicate)
	}

	// Same name, different payload → dedup guard passes (different
	// fingerprint) and the owner-scoped unique name renders 409 at the store.
	// The HTTP surface is asserted at store level like the M05 slice because
	// the guard shadows identical payloads (TestGroupStoreLevelDuplicateName).
	{
		store, storeErr := NewStore(env.db, false, nil, nil, nil)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		access := AccessScope{ViewerID: adminID}
		groupID := groupA
		first, createErr := store.Create(context.Background(), MutationInput{
			Name: ptrString("重名策略"), HasBindings: true,
			Bindings: []BindingInput{{GroupID: groupID, Priority: intPtr(1), Weight: intPtr(1), Status: "active"}},
		}, access)
		if createErr != nil {
			t.Fatalf("dup first create: %v", createErr)
		}
		_, createErr = store.Create(context.Background(), MutationInput{
			Name: ptrString("重名策略"), HasBindings: true,
			Bindings: []BindingInput{{GroupID: groupID, Priority: intPtr(1), Weight: intPtr(2), Status: "active"}},
		}, access)
		var conflict *ConflictError
		if !errorsAs(createErr, &conflict) || !strings.Contains(conflict.Message, "策略路由名称已存在") {
			t.Fatalf("duplicate name: %v", createErr)
		}
		_ = first
		// Remove the extra row so later pagination assertions hold.
		env.exec(t, `DELETE FROM route_strategy_groups WHERE route_strategy_id = ?`, first.ID)
		env.exec(t, `DELETE FROM route_strategies WHERE id = ?`, first.ID)
	}

	// Second strategy + list pagination with pageSize+1 probe.
	code, secondPayload := env.createStrategy(t, "/__aisys__/api/route-strategies",
		`{"name":"策略二","mode":"weighted","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("weighted create: %d %v", code, secondPayload)
	}
	weightedID := dataMap(t, secondPayload)["id"].(string)
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?page=1&pageSize=1", "")
	if code != 200 {
		t.Fatalf("page 1: %d %v", code, page1)
	}
	page1Data := dataMap(t, page1)
	if len(page1Data["items"].([]any)) != 1 || page1Data["hasMore"] != true || page1Data["total"] != float64(2) ||
		page1Data["page"] != float64(1) || page1Data["pageSize"] != float64(1) || page1Data["generatedAt"] == nil {
		t.Fatalf("page 1 payload: %v", page1Data)
	}
	code, page2 := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?page=2&pageSize=1", "")
	page2Data := dataMap(t, page2)
	if code != 200 || len(page2Data["items"].([]any)) != 1 || page2Data["hasMore"] != false || page2Data["total"] != float64(2) {
		t.Fatalf("page 2: %d %v", code, page2Data)
	}

	// Keyword is a case-sensitive name prefix.
	code, filtered := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?keyword=%E7%AD%96%E7%95%A5%E4%BA%8C", "")
	if code != 200 || len(items(t, filtered)) != 1 {
		t.Fatalf("keyword filter: %d %v", code, filtered)
	}
	code, caseMiss := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?keyword=alpha", "")
	if code != 200 || len(items(t, caseMiss)) != 0 {
		t.Fatalf("keyword must be case-sensitive over names: %d %v", code, caseMiss)
	}

	// mode/status filters.
	code, modeFilter := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?mode=weighted", "")
	if code != 200 || len(items(t, modeFilter)) != 1 || items(t, modeFilter)[0].(map[string]any)["id"] != weightedID {
		t.Fatalf("mode filter: %d %v", code, modeFilter)
	}
	code, allFilter := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies?mode=all&status=all", "")
	if code != 200 || len(items(t, allFilter)) != 2 {
		t.Fatalf("all filter: %d %v", code, allFilter)
	}

	// Detail: full bindings + owner fields.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+strategyID, "")
	detailData := dataMap(t, detail)
	if code != 200 || detailData["systemAccountId"] != adminID || detailData["systemAccountName"] == nil {
		t.Fatalf("detail: %d %v", code, detailData)
	}
	bindings := detailData["groupBindings"].([]any)
	if len(bindings) != 1 || bindings[0].(map[string]any)["priority"] != float64(1) ||
		bindings[0].(map[string]any)["weight"] != float64(1) ||
		bindings[0].(map[string]any)["status"] != "active" {
		t.Fatalf("detail bindings: %v", bindings)
	}
	updatedAt := detailData["updatedAt"].(string)

	// Optimistic locking: stale version → 409 + currentUpdatedAt.
	code, stale := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+strategyID,
		`{"expectedUpdatedAt":"2000-01-01T00:00:00.000Z","name":"renamed"}`)
	if code != http.StatusConflict || stale["message"] != "策略路由已被其他操作更新，请刷新后重试" {
		t.Fatalf("stale patch: %d %v", code, stale)
	}
	if stale["currentUpdatedAt"] != updatedAt {
		t.Fatalf("conflict must carry currentUpdatedAt: %v", stale)
	}

	// Binding replacement (whole-set semantics) + rename.
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"策略一改名","groupBindings":[`+bindingJSON(groupB, 0)+`]}`)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, patched)
	}
	changed := changedSet(t, patched)
	if !changed["name"] || !changed["groupBindings"] || len(changed) != 2 {
		t.Fatalf("changedFields: %v", changed)
	}
	patchData := dataMap(t, patched)
	rowPatch := patchData["rowPatch"].(map[string]any)
	if rowPatch["bindingCount"] != float64(1) || rowPatch["updatedAt"] == nil {
		t.Fatalf("rowPatch: %v", rowPatch)
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ? AND group_id = ?`, strategyID, groupB) != 1 {
		t.Fatal("bindings must be replaced with the new set")
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ?`, strategyID) != 1 {
		t.Fatal("old binding must be gone")
	}

	// No-change patch → 400 请提供要修改的策略路由内容.
	nextUpdatedAt := env.strategyUpdatedAt(t, strategyID)
	code, noop := env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+strategyID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`"}`)
	if code != http.StatusBadRequest || noop["message"] != "请提供要修改的策略路由内容" {
		t.Fatalf("empty patch: %d %v", code, noop)
	}

	// Operation logs + invalidation reasons.
	seen := map[string]bool{}
	for _, key := range env.sink.keys() {
		seen[key] = true
	}
	if !seen["route_strategies.create"] || !seen["route_strategies.update"] {
		t.Fatalf("operation log keys: %v", env.sink.keys())
	}
	if !env.inval.has("route_strategy_created") || !env.inval.has("route_strategy_updated") {
		t.Fatalf("invalidation reasons: %v", env.inval.reasons)
	}

	// Delete without references → 204 + log + invalidation.
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/"+weightedID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	code, gone := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies/"+weightedID, "")
	if code != http.StatusNotFound || gone["message"] != "策略路由不存在" {
		t.Fatalf("after delete: %d %v", code, gone)
	}
	seen = map[string]bool{}
	for _, key := range env.sink.keys() {
		seen[key] = true
	}
	if !seen["route_strategies.delete"] || !env.inval.has("route_strategy_deleted") {
		t.Fatalf("delete observability: %v %v", env.sink.keys(), env.inval.reasons)
	}
	// Delete again → 404.
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/"+weightedID, "")
	if code != http.StatusNotFound {
		t.Fatalf("double delete: %d", code)
	}
}

func TestRouteStrategyModeConfigValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	groupB := env.createGroup(t, adminID, "bravo", true)
	path := "/__aisys__/api/route-strategies"

	// normal + two bindings → 普通路由只能绑定一个启用分组.
	code, payload := env.createStrategy(t, path,
		`{"name":"n2","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "普通路由只能绑定一个启用分组" {
		t.Fatalf("normal two bindings: %d %v", code, payload)
	}

	// normal + hybridRoutingConfig → 普通路由不能配置混合评分规则.
	code, payload = env.createStrategy(t, path,
		`{"name":"nh","hybridRoutingConfig":`+hybridConfigBody(5, "model-high")+`,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "普通路由不能配置混合评分规则" {
		t.Fatalf("normal hybrid config: %d %v", code, payload)
	}

	// weighted + hybridRoutingConfig → 只有混合智能路由可以配置混合评分规则.
	code, payload = env.createStrategy(t, path,
		`{"name":"wh","mode":"weighted","hybridRoutingConfig":`+hybridConfigBody(5, "model-high")+`,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "只有混合智能路由可以配置混合评分规则" {
		t.Fatalf("weighted hybrid config: %d %v", code, payload)
	}

	// hybrid_smart + normalRoutingConfig → 只有普通路由可以配置调度偏好.
	code, payload = env.createStrategy(t, path,
		`{"name":"hn","mode":"hybrid_smart","normalRoutingConfig":{"schedulingPreference":"speed_first"},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "只有普通路由可以配置调度偏好" {
		t.Fatalf("hybrid normal config: %d %v", code, payload)
	}

	// hybrid_smart without config → 混合路由配置不能为空.
	code, payload = env.createStrategy(t, path,
		`{"name":"hc","mode":"hybrid_smart","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "混合路由配置不能为空" {
		t.Fatalf("hybrid missing config: %d %v", code, payload)
	}

	// speed_first normal config ok; deadline range enforced.
	code, speedPayload := env.createStrategy(t, path,
		`{"name":"sf","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":5}},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("speed_first create: %d %v", code, speedPayload)
	}
	speed := dataMap(t, speedPayload)
	config := speed["normalRoutingConfig"].(map[string]any)
	if config["firstByteDeadlineMs"] != float64(20000) {
		t.Fatalf("deadline: %v", config)
	}
	speedFirst := config["speedFirstConfig"].(map[string]any)
	if speedFirst["slowTriggerCount"] != float64(5) || speedFirst["slowWindowSeconds"] != float64(120) {
		t.Fatalf("speedFirst defaults: %v", speedFirst)
	}
	code, badDeadline := env.createStrategy(t, path,
		`{"name":"sf2","normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":9999},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || badDeadline["message"] != "首字截止时间必须是 10000-60000 毫秒" {
		t.Fatalf("bad deadline: %d %v", code, badDeadline)
	}

	// failover rules: primary + backup.
	code, payload = env.createStrategy(t, path,
		`{"name":"f1","mode":"failover","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "故障回退路由需要一个主用分组和至少一个备用分组" {
		t.Fatalf("failover single: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path,
		`{"name":"f2","mode":"failover","groupBindings":[`+bindingJSON(groupA, 0)+`,`+bindingJSON(groupB, 0)+`],"status":"disabled"}`)
	if code != http.StatusCreated {
		t.Fatalf("failover create: %d %v", code, payload)
	}
	failoverID := dataMap(t, payload)["id"].(string)

	// round_robin + weighted create fine.
	code, _ = env.createStrategy(t, path, `{"name":"rr","mode":"round_robin","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("round_robin create: %d", code)
	}

	// Invalid mode / status / unknown field / weight range / priority.
	code, payload = env.createStrategy(t, path, `{"name":"im","mode":"bogus","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("invalid mode: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"uf","bogusField":1,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由参数无效" {
		t.Fatalf("unknown field: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path,
		`{"name":"hw","groupBindings":[{"groupId":"`+groupA+`","weight":101}]}`)
	if code != http.StatusBadRequest || payload["message"] != "分组权重必须在 1-100 之间" {
		t.Fatalf("weight range: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path,
		`{"name":"bp","groupBindings":[{"groupId":"`+groupA+`","priority":0}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由分组优先级必须是大于 0 的整数" {
		t.Fatalf("priority: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path,
		`{"name":"db","groupBindings":[{"groupId":"`+groupA+`"},{"groupId":"`+groupA+`"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由绑定分组不能重复" {
		t.Fatalf("duplicate binding: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"nb","groupBindings":[]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由至少需要绑定一个分组" {
		t.Fatalf("empty bindings: %d %v", code, payload)
	}

	// hybrid_smart full normalization: defaults materialized (verified via
	// the detail endpoint — the create response is the list-item projection
	// without hybridRoutingConfig, matching createdRouteStrategyListItem).
	code, hybridPayload := env.createStrategy(t, path,
		`{"name":"hy","mode":"hybrid_smart","hybridRoutingConfig":`+hybridConfigBody(5, "model-high")+`,"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("hybrid create: %d %v", code, hybridPayload)
	}
	hybridID := dataMap(t, hybridPayload)["id"].(string)
	code, hybridDetail := env.do(t, http.MethodGet, path+"/"+hybridID, "")
	if code != 200 {
		t.Fatalf("hybrid detail: %d %v", code, hybridDetail)
	}
	hybridConfig := dataMap(t, hybridDetail)["hybridRoutingConfig"].(map[string]any)
	if hybridConfig["scoringCacheEnabled"] != true || hybridConfig["scoringTimeoutMs"] != float64(15000) {
		t.Fatalf("hybrid defaults: %v", hybridConfig)
	}
	inspection := hybridConfig["qualityInspection"].(map[string]any)
	if inspection["enabled"] != true || inspection["scoringModel"] != "score-model-a" || inspection["maxTriggerLevel"] != float64(6) {
		t.Fatalf("quality inspection defaults: %v", inspection)
	}
	levelRoutes := hybridConfig["levelRoutes"].([]any)
	if len(levelRoutes) != 2 {
		t.Fatalf("levelRoutes: %v", levelRoutes)
	}

	// hybrid coverage violations.
	code, payload = env.createStrategy(t, path, `{"name":"hy2","mode":"hybrid_smart","hybridRoutingConfig":{"scoringModel":"m","levelRoutes":[{"minLevel":1,"maxLevel":5,"targetModel":"a"},{"minLevel":6,"maxLevel":9,"targetModel":"b"}]},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "混合路由等级范围必须按从小到大连续覆盖 1-10" {
		t.Fatalf("coverage: %d %v", code, payload)
	}
	code, payload = env.createStrategy(t, path, `{"name":"hy3","mode":"hybrid_smart","hybridRoutingConfig":{"scoringModel":"m","levelRoutes":[{"minLevel":1,"maxLevel":5,"targetModel":"a"},{"minLevel":6,"maxLevel":10,"targetModel":"a"}]},"groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "混合路由至少需要配置 2 个不同的目标模型" {
		t.Fatalf("distinct models: %d %v", code, payload)
	}

	// Binding boundary: disabled group cannot be activated; foreign groups
	// are covered in the self-scope test.
	code, payload = env.createStrategy(t, path, `{"name":"dg","groupBindings":[{"groupId":"disabled-group"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由只能绑定自己的分组或有效授权给自己的分组" {
		t.Fatalf("unknown group: %d %v", code, payload)
	}
	disabledGroup := env.createGroup(t, adminID, "g-off", false)
	code, payload = env.createStrategy(t, path, `{"name":"dgb","groupBindings":[{"groupId":"`+disabledGroup+`"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由不能启用已停用分组：g-off" {
		t.Fatalf("disabled group: %d %v", code, payload)
	}

	// Failover keeps demanding one enabled backup: disabling the only
	// active backup is rejected (Node validateRouteStrategyModeBindings).
	updatedAt := env.strategyUpdatedAt(t, failoverID)
	code, payload = env.do(t, http.MethodPatch, path+"/"+failoverID,
		`{"expectedUpdatedAt":"`+updatedAt+`","groupBindings":[{"groupId":"`+groupA+`","priority":1},{"groupId":"`+groupB+`","priority":2,"status":"disabled"}]}`)
	if code != http.StatusBadRequest || payload["message"] != "故障回退路由至少需要一个启用备用分组" {
		t.Fatalf("failover disabled backup guard: %d %v", code, payload)
	}

	// Weight change on the primary reconciles in place (same binding row).
	updatedAt = env.strategyUpdatedAt(t, failoverID)
	code, patched := env.do(t, http.MethodPatch, path+"/"+failoverID,
		`{"expectedUpdatedAt":"`+updatedAt+`","groupBindings":[{"groupId":"`+groupA+`","priority":1,"weight":5},{"groupId":"`+groupB+`","priority":2}]}`)
	if code != 200 {
		t.Fatalf("failover weight patch: %d %v", code, patched)
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ? AND group_id = ? AND weight = 5`, failoverID, groupA) != 1 {
		t.Fatal("binding weight must be reconciled")
	}

	// Mode switch without bindings must satisfy the new mode: normal →
	// failover fails while only one binding exists.
	normalID := speed["id"].(string)
	updatedAt = env.strategyUpdatedAt(t, normalID)
	code, payload = env.do(t, http.MethodPatch, path+"/"+normalID,
		`{"expectedUpdatedAt":"`+updatedAt+`","mode":"failover"}`)
	if code != http.StatusBadRequest || payload["message"] != "故障回退路由需要一个主用分组和至少一个备用分组" {
		t.Fatalf("mode switch binding rule: %d %v", code, payload)
	}
}

func TestRouteStrategyDeleteProtection(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)
	path := "/__aisys__/api/route-strategies"

	code, createdPayload := env.createStrategy(t, path,
		`{"name":"默认策略","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, createdPayload)
	}
	strategyID := dataMap(t, createdPayload)["id"].(string)

	// Default strategy (is_default=1) cannot be deleted.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `UPDATE route_strategies SET is_default = 1, updated_at = ? WHERE id = ?`, now, strategyID)
	code, defaultGuard := env.do(t, http.MethodDelete, path+"/"+strategyID, "")
	if code != http.StatusBadRequest || defaultGuard["message"] != "默认策略路由不允许删除" {
		t.Fatalf("default guard: %d %v", code, defaultGuard)
	}
	env.exec(t, `UPDATE route_strategies SET is_default = 0 WHERE id = ?`, strategyID)

	// Referenced by an API key cannot be deleted.
	env.exec(t, `INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, status, created_at, updated_at)
		VALUES ('key-1', ?, ?, 'k', 'active', ?, ?)`, adminID, strategyID, now, now)
	code, keyGuard := env.do(t, http.MethodDelete, path+"/"+strategyID, "")
	if code != http.StatusBadRequest || keyGuard["message"] != "策略路由已被 1 个 API Key 使用，请先解绑" {
		t.Fatalf("api key guard: %d %v", code, keyGuard)
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategies WHERE id = ?`, strategyID) != 1 {
		t.Fatal("strategy must survive the guards")
	}
	env.exec(t, `DELETE FROM api_keys WHERE id = 'key-1'`)

	code, _ = env.do(t, http.MethodDelete, path+"/"+strategyID, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if env.count(t, `SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id = ?`, strategyID) != 0 {
		t.Fatal("bindings must cascade")
	}
}

func TestRouteStrategyMySelfScope(t *testing.T) {
	env := newTestEnv(t)
	userAID := env.login(t, "alice", "alice-pass", "user")
	groupA := env.createGroup(t, userAID, "alice-group", true)
	myPath := "/__aisys__/api/my-route-strategies"

	code, createdPayload := env.createStrategy(t, myPath,
		`{"name":"alice-strategy","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("my create: %d %v", code, createdPayload)
	}
	created := dataMap(t, createdPayload)
	aliceStrategy := created["id"].(string)
	// Self create stamps the caller as owner.
	if env.count(t, `SELECT COUNT(*) FROM route_strategies WHERE id = ? AND system_account_id = ?`, aliceStrategy, userAID) != 1 {
		t.Fatal("self create must stamp the caller as owner")
	}
	if created["systemAccountId"] != nil {
		t.Fatalf("self response must omit systemAccountId: %v", created)
	}

	// userB cannot see or mutate alice's strategy via my-*.
	bobID := env.login(t, "bob", "bob-pass", "user")
	code, forbidden := env.do(t, http.MethodGet, myPath+"/"+aliceStrategy, "")
	if code != http.StatusNotFound || forbidden["message"] != "策略路由不存在" {
		t.Fatalf("bob read alice strategy: %d %v", code, forbidden)
	}
	updatedAt := env.strategyUpdatedAt(t, aliceStrategy)
	code, _ = env.do(t, http.MethodPatch, myPath+"/"+aliceStrategy,
		`{"expectedUpdatedAt":"`+updatedAt+`","name":"hijacked"}`)
	if code != http.StatusNotFound {
		t.Fatalf("bob patch alice strategy: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, myPath+"/"+aliceStrategy, "")
	if code != http.StatusNotFound {
		t.Fatalf("bob delete alice strategy: %d", code)
	}

	// Bob's my-* list only shows bob's own rows.
	groupB := env.createGroup(t, bobID, "bob-group", true)
	code, bobCreated := env.createStrategy(t, myPath,
		`{"name":"bob-strategy","groupBindings":[`+bindingJSON(groupB, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("bob create: %d %v", code, bobCreated)
	}
	bobStrategy := dataMap(t, bobCreated)["id"].(string)
	code, bobList := env.do(t, http.MethodGet, myPath, "")
	list := items(t, bobList)
	if code != 200 || len(list) != 1 || list[0].(map[string]any)["id"] != bobStrategy {
		t.Fatalf("bob list: %d %v", code, bobList)
	}

	// Binding boundary through my-*: bob cannot bind alice's group.
	code, payload := env.createStrategy(t, myPath,
		`{"name":"bob-steal","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusBadRequest || payload["message"] != "策略路由只能绑定自己的分组或有效授权给自己的分组" {
		t.Fatalf("bob bind alice group: %d %v", code, payload)
	}

	// Bob (user role) cannot reach the admin surface.
	code, adminDenied := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies", "")
	if code != http.StatusForbidden || adminDenied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, adminDenied)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/route-strategies",
		`{"name":"x","groupBindings":[{"groupId":"`+groupB+`"}]}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin create as user: %d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/route-strategies/"+bobStrategy,
		`{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z","name":"y"}`)
	if code != http.StatusForbidden {
		t.Fatalf("admin patch as user: %d", code)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/route-strategies/"+bobStrategy, "")
	if code != http.StatusForbidden {
		t.Fatalf("admin delete as user: %d", code)
	}

	// Admin /my-route-strategies is forced to self scope too.
	env.login(t, "root", "root-pass", "super_admin")
	code, adminSelf := env.do(t, http.MethodGet, myPath, "")
	if code != 200 || len(items(t, adminSelf)) != 0 {
		t.Fatalf("admin my list must be self-scoped: %d %v", code, adminSelf)
	}
	// Admin surface sees everything; self create through my-* stamps admin.
	code, adminList := env.do(t, http.MethodGet, "/__aisys__/api/route-strategies", "")
	if code != 200 || len(items(t, adminList)) != 2 {
		t.Fatalf("admin list: %d %v", code, adminList)
	}
}

func TestRouteStrategyPermissionMatrixAndValidation(t *testing.T) {
	env := newTestEnv(t)
	adminPath := "/__aisys__/api/route-strategies"
	myPath := "/__aisys__/api/my-route-strategies"

	// Anonymous access refused on both surfaces.
	code, anonymous := env.do(t, http.MethodGet, myPath, "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my list: %d %v", code, anonymous)
	}
	code, _ = env.do(t, http.MethodGet, adminPath, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin list: %d", code)
	}
	code, _ = env.do(t, http.MethodPost, adminPath, `{"name":"x"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin create: %d", code)
	}

	adminID := env.login(t, "root", "root-pass", "super_admin")
	groupA := env.createGroup(t, adminID, "alpha", true)

	code, createdPayload := env.createStrategy(t, adminPath,
		`{"name":"矩阵","groupBindings":[`+bindingJSON(groupA, 0)+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, createdPayload)
	}
	strategyID := dataMap(t, createdPayload)["id"].(string)

	// Missing/invalid expectedUpdatedAt.
	code, missingVersion := env.do(t, http.MethodPatch, adminPath+"/"+strategyID, `{"name":"x"}`)
	if code != http.StatusBadRequest || missingVersion["message"] != "策略路由配置版本格式不正确" {
		t.Fatalf("missing version: %d %v", code, missingVersion)
	}
	code, badVersion := env.do(t, http.MethodPatch, adminPath+"/"+strategyID,
		`{"expectedUpdatedAt":"not-a-time","name":"x"}`)
	if code != http.StatusBadRequest || badVersion["message"] != "策略路由配置版本格式不正确" {
		t.Fatalf("bad version: %d %v", code, badVersion)
	}

	// Detail 404 on both surfaces for unknown ids.
	code, _ = env.do(t, http.MethodGet, adminPath+"/missing-id", "")
	if code != http.StatusNotFound {
		t.Fatalf("admin detail 404: %d", code)
	}
	code, _ = env.do(t, http.MethodGet, myPath+"/missing-id", "")
	if code != http.StatusNotFound {
		t.Fatalf("my detail 404: %d", code)
	}

	// pageSize clamps to 200; page floors at 1.
	code, clamped := env.do(t, http.MethodGet, adminPath+"?pageSize=500&page=-3", "")
	clampedData := dataMap(t, clamped)
	if code != 200 || clampedData["pageSize"] != float64(200) || clampedData["page"] != float64(1) {
		t.Fatalf("clamp: %d %v", code, clampedData)
	}

	// Description lifecycle: set, then null (rowPatch carries null), then
	// no-op.
	updatedAt := env.strategyUpdatedAt(t, strategyID)
	code, patched := env.do(t, http.MethodPatch, adminPath+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","description":"描述"}`)
	if code != 200 {
		t.Fatalf("description patch: %d %v", code, patched)
	}
	if !changedSet(t, patched)["description"] {
		t.Fatalf("description change missing: %v", patched)
	}
	// Re-read detail: description now set.
	code, detail := env.do(t, http.MethodGet, adminPath+"/"+strategyID, "")
	if code != 200 || dataMap(t, detail)["description"] != "描述" {
		t.Fatalf("detail description: %d %v", code, detail)
	}
	updatedAt = env.strategyUpdatedAt(t, strategyID)
	code, unpatched := env.do(t, http.MethodPatch, adminPath+"/"+strategyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","description":null}`)
	if code != 200 {
		t.Fatalf("null description patch: %d %v", code, unpatched)
	}
	if !changedSet(t, unpatched)["description"] {
		t.Fatalf("null description change missing: %v", unpatched)
	}
	if value, present := dataMap(t, unpatched)["rowPatch"].(map[string]any)["description"]; !present || value != nil {
		t.Fatalf("rowPatch must carry description null: %v", unpatched)
	}

	// No-op patch with identical null description → empty changedFields.
	nextUpdatedAt := env.strategyUpdatedAt(t, strategyID)
	code, noop := env.do(t, http.MethodPatch, adminPath+"/"+strategyID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","description":null}`)
	if code != 200 || len(dataMap(t, noop)["changedFields"].([]any)) != 0 {
		t.Fatalf("no-change patch: %d %v", code, noop)
	}

	// status filter reflect patch.
	code, _ = env.do(t, http.MethodPatch, adminPath+"/"+strategyID,
		`{"expectedUpdatedAt":"`+env.strategyUpdatedAt(t, strategyID)+`","status":"disabled"}`)
	if code != 200 {
		t.Fatalf("status patch: %d", code)
	}
	code, disabled := env.do(t, http.MethodGet, adminPath+"?status=disabled", "")
	if code != 200 || len(items(t, disabled)) != 1 {
		t.Fatalf("status filter: %d %v", code, disabled)
	}
	code, active := env.do(t, http.MethodGet, adminPath+"?status=active", "")
	if code != 200 || len(items(t, active)) != 0 {
		t.Fatalf("active filter: %d %v", code, active)
	}
}
