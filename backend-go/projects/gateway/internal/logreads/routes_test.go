package logreads

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

type testEnv struct {
	deps     *authsys.Deps
	store    operationlog.Store
	lease    operationlog.OwnerLease
	business *sql.DB
	server   *httptest.Server
	jar      map[string]string
	created  map[string]string
	mu       sync.Mutex
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:logreads-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active', password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL DEFAULT 0, image_generation_enabled INTEGER NOT NULL DEFAULT 0, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
	} {
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
	deps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	// F4 store on a physically distinct SQLite pair; the business database
	// mirrors system_accounts so list/detail keep the actor name projection.
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	business := createBusinessSettings(t, businessPath)
	store, err := operationlog.OpenStore(operationlog.Config{Mode: operationlog.ModeSQLite, DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: businessPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "logreads-test", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.MountAuth(k, "lax", false)
	(&Deps{Reader: store, Auth: deps}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	return &testEnv{deps: deps, store: store, lease: lease, business: business, server: server, jar: map[string]string{}, created: map[string]string{}}
}

func createBusinessSettings(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key)); CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
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

// login creates the account on first use and re-issues a session on every
// call, so tests can switch actors without losing earlier account IDs.
func (e *testEnv) login(t *testing.T, username, password, role string) string {
	t.Helper()
	id, ok := e.created[username]
	if !ok {
		mustChangePassword := false
		created, err := e.deps.Accounts.Create(context.Background(), authsys.CreateInput{
			Username: username, DisplayName: username + "_name", Password: password, Role: role,
			MustChangePassword: &mustChangePassword,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.business.Exec(`INSERT INTO system_accounts (id, username, display_name) VALUES (?, ?, ?)`, created.ID, username, username+"_name"); err != nil {
			t.Fatal(err)
		}
		id = created.ID
		e.created[username] = id
	}
	code, payload := e.do(t, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"`+username+`","password":"`+password+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login %s failed: %d %v", username, code, payload)
	}
	return id
}

func (e *testEnv) seed(t *testing.T, input operationlog.Input) {
	t.Helper()
	if _, err := e.store.Persist(context.Background(), e.lease, input); err != nil {
		t.Fatal(err)
	}
}

// resetSession drops the cookie jar so subsequent calls act anonymously.
func (e *testEnv) resetSession() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.jar = map[string]string{}
}

func opLog(id, actor, role, module, action, summary, createdAt string) operationlog.Input {
	return operationlog.Input{
		ID: id, ActorSystemAccountID: actor, ActorRole: role,
		Module: module, Action: action, OperationKey: module + "." + action,
		ResourceType: "group", Summary: summary, CreatedAt: createdAt,
		ClientIP: "203.0.113.9",
	}
}

func at(base time.Time, offset time.Duration) string {
	return base.Add(offset).UTC().Format(time.RFC3339Nano)
}

// newSeededEnv builds the shared F4 history: two personal group creates, an
// admin adjustment scoped to alice, one all-users notice, one bob-only
// record, and a 40-day-old record carrying an exact trace ID (outside the
// default management window).
func newSeededEnv(t *testing.T) (*testEnv, map[string]string) {
	t.Helper()
	env := newTestEnv(t)
	aliceID := env.login(t, "alice", "alice-pass", "user")
	bobID := env.login(t, "bob", "bob-pass", "user")
	rootID := env.login(t, "root", "root-pass", "super_admin")
	now := time.Now().UTC()
	traceUUID := "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"

	aliceGroup := opLog("m13-alice-group", aliceID, "user", "groups", "create", "创建分组 alpha", at(now, -3*time.Hour))
	aliceGroup.ResourceID, aliceGroup.ResourceName = "grp-alpha", "alpha"
	aliceGroup.Changes = []operationlog.Change{{Field: "name", Label: "名称", After: "alpha"}}
	env.seed(t, aliceGroup)
	env.seed(t, opLog("m13-bob-group", bobID, "user", "groups", "create", "创建分组 bravo", at(now, -2*time.Hour)))
	adminAdjust := opLog("m13-admin-adjust", rootID, "admin", "groups", "update", "管理员调整 alice 配额", at(now, -90*time.Minute))
	adminAdjust.OperationScopeSystemAccountID = aliceID
	env.seed(t, adminAdjust)
	globalNotice := opLog("m13-global-notice", bobID, "user", "system", "announce", "全量系统公告 global", at(now, -60*time.Minute))
	globalNotice.VisibilityScope = "all_users"
	env.seed(t, globalNotice)
	env.seed(t, opLog("m13-bob-private", bobID, "user", "apikeys", "create", "bob 私密记录", at(now, -30*time.Minute)))
	oldTrace := opLog("m13-old-trace", aliceID, "user", "accounts", "update", "四十天前的追踪 old", at(now, -40*24*time.Hour))
	oldTrace.TraceID = traceUUID
	env.seed(t, oldTrace)

	return env, map[string]string{
		"alice": aliceID, "bob": bobID, "root": rootID,
		"aliceGroup": "m13-alice-group", "bobGroup": "m13-bob-group",
		"adminAdjust": "m13-admin-adjust", "globalNotice": "m13-global-notice",
		"bobPrivate": "m13-bob-private", "oldTrace": "m13-old-trace", "traceUUID": traceUUID,
	}
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object: %v", payload)
	}
	return data
}

func itemIDs(t *testing.T, payload map[string]any) []string {
	t.Helper()
	data := dataMap(t, payload)
	raw, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("missing items array: %v", data)
	}
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	return ids
}

func sameIDs(ids []string, want ...string) bool {
	if len(ids) != len(want) {
		return false
	}
	for index, id := range ids {
		if id != want[index] {
			return false
		}
	}
	return true
}

func TestOperationLogAdminListDetailFiltersAndPermissions(t *testing.T) {
	env, ids := newSeededEnv(t)
	env.login(t, "root", "root-pass", "super_admin")

	// Unfiltered list: the default 31-day window keeps the five recent
	// records, newest first, and excludes the 40-day-old trace record.
	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs", "")
	if code != http.StatusOK {
		t.Fatalf("admin list: %d %v", code, payload)
	}
	data := dataMap(t, payload)
	if got := itemIDs(t, payload); !sameIDs(got, ids["bobPrivate"], ids["globalNotice"], ids["adminAdjust"], ids["bobGroup"], ids["aliceGroup"]) {
		t.Fatalf("admin list order: %v", got)
	}
	if data["total"] != float64(5) || data["hasMore"] != false || data["page"] != float64(1) || data["pageSize"] != float64(20) {
		t.Fatalf("admin list envelope: %v", data)
	}
	first := data["items"].([]any)[0].(map[string]any)
	if first["actorSystemAccountId"] != ids["bob"] || first["actorSystemAccountName"] != "bob_name" ||
		first["module"] != "apikeys" || first["action"] != "create" || first["summary"] != "bob 私密记录" {
		t.Fatalf("admin list item projection: %v", first)
	}

	// Pagination over the same window.
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?page=1&pageSize=2", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, page1), ids["bobPrivate"], ids["globalNotice"]) {
		t.Fatalf("admin page 1: %d %v", code, page1)
	}
	if dataMap(t, page1)["hasMore"] != true || dataMap(t, page1)["total"] != float64(3) {
		t.Fatalf("admin page 1 envelope: %v", dataMap(t, page1))
	}
	code, page3 := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?page=3&pageSize=2", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, page3), ids["aliceGroup"]) {
		t.Fatalf("admin page 3: %d %v", code, page3)
	}
	if dataMap(t, page3)["hasMore"] != false || dataMap(t, page3)["total"] != float64(5) {
		t.Fatalf("admin page 3 envelope: %v", dataMap(t, page3))
	}

	// Scalar filters.
	code, byModule := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?module=groups", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byModule), ids["adminAdjust"], ids["bobGroup"], ids["aliceGroup"]) {
		t.Fatalf("module filter: %d %v", code, byModule)
	}
	code, byAction := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?action=create", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byAction), ids["bobPrivate"], ids["bobGroup"], ids["aliceGroup"]) {
		t.Fatalf("action filter: %d %v", code, byAction)
	}
	code, byKeyword := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?summaryKeyword=alpha", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byKeyword), ids["aliceGroup"]) {
		t.Fatalf("summaryKeyword filter: %d %v", code, byKeyword)
	}
	code, byActor := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?actorSystemAccountId="+ids["bob"], "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byActor), ids["bobPrivate"], ids["globalNotice"], ids["bobGroup"]) {
		t.Fatalf("actor filter: %d %v", code, byActor)
	}
	code, byScope := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?operationScopeSystemAccountId="+ids["alice"], "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byScope), ids["adminAdjust"]) {
		t.Fatalf("operation scope filter: %d %v", code, byScope)
	}

	// affectedSystemAccountId covers viewer rows plus all_users records;
	// the literal "all" is ignored by the store.
	code, byAffected := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?affectedSystemAccountId="+ids["alice"], "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byAffected), ids["globalNotice"], ids["adminAdjust"], ids["aliceGroup"]) {
		t.Fatalf("affected filter: %d %v", code, byAffected)
	}
	code, allAffected := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?affectedSystemAccountId=all", "")
	if code != http.StatusOK || len(itemIDs(t, allAffected)) != 5 {
		t.Fatalf("affected=all must be ignored: %d %v", code, allAffected)
	}

	// An exact trace ID bypasses the default window; a bare prefix does not.
	code, byTrace := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?traceId="+ids["traceUUID"], "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byTrace), ids["oldTrace"]) {
		t.Fatalf("exact trace filter: %d %v", code, byTrace)
	}
	code, byPrefix := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?traceId=a1b2c3d4", "")
	if code != http.StatusOK || len(itemIDs(t, byPrefix)) != 0 {
		t.Fatalf("trace prefix must keep the default window: %d %v", code, byPrefix)
	}

	// Absolute ranges, including a numeric-offset form of the same instant.
	offsetZone := time.FixedZone("UTC+8", 8*3600)
	offsetStart := time.Now().UTC().Add(-70 * time.Minute).In(offsetZone).Format(time.RFC3339Nano)
	values := url.Values{"startAt": []string{offsetStart}}
	code, byOffsetRange := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?"+values.Encode(), "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byOffsetRange), ids["bobPrivate"], ids["globalNotice"]) {
		t.Fatalf("offset startAt filter: %d %v", code, byOffsetRange)
	}
	code, byStart := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?startAt="+url.QueryEscape(at(time.Now().UTC(), -70*time.Minute)), "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byStart), ids["bobPrivate"], ids["globalNotice"]) {
		t.Fatalf("startAt filter: %d %v", code, byStart)
	}
	// endAt alone skips the default window, so the 40-day-old record inside
	// the range is returned too.
	code, byEnd := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?endAt="+url.QueryEscape(at(time.Now().UTC(), -70*time.Minute)), "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byEnd), ids["adminAdjust"], ids["bobGroup"], ids["aliceGroup"], ids["oldTrace"]) {
		t.Fatalf("endAt filter: %d %v", code, byEnd)
	}

	// Invalid bounds are request faults with the Node messages.
	code, badStart := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?startAt=not-a-time", "")
	if code != http.StatusBadRequest || badStart["message"] != "开始时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid startAt: %d %v", code, badStart)
	}
	code, naiveStart := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?startAt=2026-08-13T00:00:00", "")
	if code != http.StatusBadRequest || naiveStart["message"] != "开始时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("naive startAt: %d %v", code, naiveStart)
	}
	code, naiveEnd := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs?endAt=2026-08-13T00:00:00", "")
	if code != http.StatusBadRequest || naiveEnd["message"] != "结束时间必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("naive endAt: %d %v", code, naiveEnd)
	}

	// Admin detail: full supplement with viewers, changes and client IP.
	code, detail := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs/"+ids["aliceGroup"], "")
	if code != http.StatusOK {
		t.Fatalf("admin detail: %d %v", code, detail)
	}
	supplement := dataMap(t, detail)
	if supplement["operationKey"] != "groups.create" || supplement["resourceType"] != "group" ||
		supplement["resourceId"] != "grp-alpha" || supplement["visibilityScope"] != "targeted" || supplement["clientIp"] != "203.0.113.9" {
		t.Fatalf("admin detail scalar fields: %v", supplement)
	}
	if len(supplement["changes"].([]any)) != 1 || len(supplement["targets"].([]any)) != 1 {
		t.Fatalf("admin detail changes/targets: %v", supplement)
	}
	viewers := supplement["viewers"].([]any)
	if len(viewers) != 1 {
		t.Fatalf("admin detail viewers: %v", viewers)
	}
	viewer := viewers[0].(map[string]any)
	if viewer["systemAccountId"] != ids["alice"] || viewer["visibilityReason"] != "actor_self" || viewer["systemAccountName"] != "alice_name" {
		t.Fatalf("admin detail viewer projection: %v", viewer)
	}
	code, missing := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs/m13-missing", "")
	if code != http.StatusNotFound || missing["message"] != "操作日志不存在" {
		t.Fatalf("admin missing detail: %d %v", code, missing)
	}

	// The admin surface denies non-admin and anonymous callers.
	env.login(t, "alice", "alice-pass", "user")
	code, denied := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs", "")
	if code != http.StatusForbidden || denied["message"] != "需要管理员权限" {
		t.Fatalf("admin list as user: %d %v", code, denied)
	}
	code, deniedDetail := env.do(t, http.MethodGet, "/__aisys__/api/operation-logs/"+ids["aliceGroup"], "")
	if code != http.StatusForbidden || deniedDetail["message"] != "需要管理员权限" {
		t.Fatalf("admin detail as user: %d %v", code, deniedDetail)
	}
}

func TestOperationLogMySurfaceVisibilityAndSelfScope(t *testing.T) {
	env, ids := newSeededEnv(t)

	// Anonymous callers are refused before any visibility filtering.
	env.resetSession()
	code, anonymous := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs", "")
	if code != http.StatusUnauthorized || anonymous["message"] != "请先登录" {
		t.Fatalf("anonymous my list: %d %v", code, anonymous)
	}

	// Alice sees her viewer rows plus all_users records, including the
	// 40-day-old one (the self surface has no default window).
	env.login(t, "alice", "alice-pass", "user")
	code, aliceList := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs", "")
	if code != http.StatusOK {
		t.Fatalf("alice my list: %d %v", code, aliceList)
	}
	if got := itemIDs(t, aliceList); !sameIDs(got, ids["globalNotice"], ids["adminAdjust"], ids["aliceGroup"], ids["oldTrace"]) {
		t.Fatalf("alice visibility: %v", got)
	}
	if dataMap(t, aliceList)["total"] != float64(4) {
		t.Fatalf("alice my list envelope: %v", dataMap(t, aliceList))
	}

	// Self-surface pagination.
	code, page1 := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?page=1&pageSize=2", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, page1), ids["globalNotice"], ids["adminAdjust"]) {
		t.Fatalf("alice page 1: %d %v", code, page1)
	}
	if dataMap(t, page1)["hasMore"] != true || dataMap(t, page1)["total"] != float64(3) {
		t.Fatalf("alice page 1 envelope: %v", dataMap(t, page1))
	}
	code, page2 := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?page=2&pageSize=2", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, page2), ids["aliceGroup"], ids["oldTrace"]) {
		t.Fatalf("alice page 2: %d %v", code, page2)
	}

	// Shared filters work; admin-only filters are dropped, not applied.
	code, byModule := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?module=system", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byModule), ids["globalNotice"]) {
		t.Fatalf("alice module filter: %d %v", code, byModule)
	}
	code, foreignKeyword := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?summaryKeyword=bravo", "")
	if code != http.StatusOK || len(itemIDs(t, foreignKeyword)) != 0 {
		t.Fatalf("alice foreign keyword: %d %v", code, foreignKeyword)
	}
	code, byStart := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?startAt="+url.QueryEscape(at(time.Now().UTC(), -70*time.Minute)), "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, byStart), ids["globalNotice"]) {
		t.Fatalf("alice startAt filter: %d %v", code, byStart)
	}
	code, adminFilterLeak := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?actorSystemAccountId="+ids["bob"], "")
	if code != http.StatusOK || len(itemIDs(t, adminFilterLeak)) != 4 {
		t.Fatalf("actor filter must be dropped on the self surface: %d %v", code, adminFilterLeak)
	}
	code, affectedLeak := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs?affectedSystemAccountId="+ids["alice"], "")
	if code != http.StatusOK || len(itemIDs(t, affectedLeak)) != 4 {
		t.Fatalf("affected filter must be dropped on the self surface: %d %v", code, affectedLeak)
	}

	// Personal detail: full level for her own viewer row, but viewers list
	// and client IP stay trimmed.
	code, ownDetail := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs/"+ids["aliceGroup"], "")
	if code != http.StatusOK {
		t.Fatalf("alice own detail: %d %v", code, ownDetail)
	}
	own := dataMap(t, ownDetail)
	if own["operationKey"] != "groups.create" || len(own["changes"].([]any)) != 1 || len(own["targets"].([]any)) != 1 {
		t.Fatalf("alice own detail fields: %v", own)
	}
	if len(own["viewers"].([]any)) != 0 {
		t.Fatalf("personal detail must trim viewers: %v", own["viewers"])
	}
	if _, present := own["clientIp"]; present {
		t.Fatalf("personal detail must trim clientIp: %v", own)
	}

	// An all_users record stays readable but collapses to summary level.
	code, globalDetail := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs/"+ids["globalNotice"], "")
	if code != http.StatusOK {
		t.Fatalf("alice global detail: %d %v", code, globalDetail)
	}
	global := dataMap(t, globalDetail)
	if global["operationKey"] != "system.announce" || len(global["changes"].([]any)) != 0 || len(global["viewers"].([]any)) != 0 {
		t.Fatalf("all_users detail must be summary level: %v", global)
	}

	// Bob-only records are invisible to alice on the self surface.
	for _, hidden := range []string{ids["bobGroup"], ids["bobPrivate"]} {
		code, forbidden := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs/"+hidden, "")
		if code != http.StatusNotFound || forbidden["message"] != "操作日志不存在" {
			t.Fatalf("alice foreign detail %s: %d %v", hidden, code, forbidden)
		}
	}

	// Even an admin is pinned to the self scope on my-*.
	env.login(t, "root", "root-pass", "super_admin")
	code, rootSelf := env.do(t, http.MethodGet, "/__aisys__/api/my-operation-logs", "")
	if code != http.StatusOK || !sameIDs(itemIDs(t, rootSelf), ids["globalNotice"], ids["adminAdjust"]) {
		t.Fatalf("admin my list must be self scoped: %d %v", code, rootSelf)
	}
}
