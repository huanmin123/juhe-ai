package proxyprofiles

// 代理配置家族契约测试：选项合并、分页列表、创建（重名 409）、管理面
// PATCH（版本 CAS 409 / 连接字段变更重置检测状态 / 空补丁 no-op）、删除的
// 在用 409 与 204 前置（Node proxies.routes.ts + proxy.repository.ts）。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

const proxySchema = `
	CREATE TABLE proxy_profiles (
		id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT,
		type TEXT NOT NULL, host TEXT NOT NULL, port INTEGER NOT NULL, username TEXT,
		password_encrypted TEXT, enabled INTEGER NOT NULL DEFAULT 1, test_status TEXT NOT NULL DEFAULT 'unknown',
		latency_ms INTEGER, outbound_ip TEXT, outbound_region TEXT, last_test_message TEXT,
		last_tested_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE UNIQUE INDEX idx_proxy_profiles_name_unique ON proxy_profiles(name);
	CREATE TABLE accounts (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, deleted_at TEXT, proxy_profile_id TEXT);
`

type proxyFixture struct {
	store *Store
	db    *sql.DB
	sink  *recordingSink
	deps  *authsys.Deps
	now   time.Time
}

type recordingSink struct {
	entries []authsys.OperationLogEntry
}

func (s *recordingSink) Record(entry authsys.OperationLogEntry, r *http.Request) {
	s.entries = append(s.entries, entry)
}

func newProxyFixture(t *testing.T) *proxyFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(proxySchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	sink := &recordingSink{}
	sequence := 0
	store, err := NewStore(Deps{
		DB:        db,
		PGDialect: false,
		Secret:    "test-secret",
		Now:       func() time.Time { return now },
		NewID: func(prefix string) string {
			sequence++
			return prefix + "_test_" + strconv.Itoa(sequence)
		},
	})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return &proxyFixture{store: store, db: db, sink: sink, now: now}
}

func (f *proxyFixture) auth(role string) *authsys.AuthContext {
	if role == "" {
		role = "admin"
	}
	return &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", DisplayName: "Admin", Role: role, SessionID: "s1"}
}

func TestProxyOptionsMergesWindowAndSelected(t *testing.T) {
	fixture := newProxyFixture(t)
	seed := []string{
		`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at) VALUES
			('p-1', 'sa-1', 'Beta', 'http', 'h1', 8080, 1, '2026-01-01', '2026-01-01'),
			('p-2', 'sa-1', 'Alpha', 'http', 'h2', 8080, 1, '2026-01-02', '2026-01-02'),
			('p-3', 'sa-1', '停用', 'http', 'h3', 8080, 0, '2026-01-03', '2026-01-03')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	options, err := fixture.store.ListOptions(context.Background(), "", 50, []string{"p-1"})
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	// 停用代理不进选项；选中项并入后按 name 排序。
	if len(options) != 2 || options[0].ID != "p-2" || options[1].ID != "p-1" {
		t.Fatalf("options wrong: %#v", options)
	}
	// selectedIds 契约：CSV / 超限 / 非法键。
	if _, message := parseSelectedProxyOptionIds(urlValuesOf(map[string][]string{"selectedIds": {"a,b"}})); message != "代理选项 selectedIds 无效" {
		t.Fatalf("csv selectedIds not rejected: %q", message)
	}
	if _, message := parseSelectedProxyOptionIds(urlValuesOf(map[string][]string{"selectedIds[0]": {"a"}})); message != "代理选项 selectedIds 无效" {
		t.Fatalf("indexed selectedIds not rejected: %q", message)
	}
	many := urlValuesOf(map[string][]string{})
	for index := 0; index < 21; index++ {
		many["selectedIds[]"] = append(many["selectedIds[]"], "id-"+string(rune('a'+index)))
	}
	if _, message := parseSelectedProxyOptionIds(many); message != "代理选项 selectedIds 最多 20 个" {
		t.Fatalf("selectedIds cap not enforced: %q", message)
	}
}

func urlValuesOf(raw map[string][]string) map[string][]string { return raw }

func TestProxyListPageContract(t *testing.T) {
	fixture := newProxyFixture(t)
	for index := 0; index < 3; index++ {
		if _, err := fixture.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, created_at, updated_at)
			VALUES (?, 'sa-1', ?, 'http', 'h', 8080, 1, ?, ?)`,
			"p-"+string(rune('a'+index)), "Proxy"+string(rune('A'+index)),
			time.Date(2026, 9, 1+index, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z"),
			time.Date(2026, 9, 1+index, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	result, err := fixture.store.ListPage(context.Background(), 1, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// updated_at DESC：最新的 C 排第一；total 是上界（hasMore 时 +1）。
	if len(result.Items) != 2 || result.Items[0].Name != "ProxyC" || !result.HasMore || result.Total != 3 {
		t.Fatalf("list page wrong: %#v", result)
	}
	if result.Items[0].UpdatedAt != "2026-09-03T00:00:00.000Z" {
		t.Fatalf("updatedAt wrong: %q", result.Items[0].UpdatedAt)
	}
}

func TestProxyCreateDuplicateNameConflict(t *testing.T) {
	fixture := newProxyFixture(t)
	input := proxyInput{}
	name := "共享代理"
	description := ""
	proxyType := "http"
	host := "proxy.internal"
	port := 8080
	enabled := true
	input.Name, input.Type, input.Host, input.Port = &name, &proxyType, &host, &port
	input.Description, input.HasDescription = &description, true
	input.Enabled = &enabled
	created, err := fixture.store.Create(context.Background(), input, "sa-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.TestStatus != "unknown" || created.Enabled != true {
		t.Fatalf("created profile wrong: %#v", created)
	}
	_, err = fixture.store.Create(context.Background(), input, "sa-1")
	var duplicate *DuplicateNameError
	if err == nil || !asDuplicate(err, &duplicate) {
		t.Fatalf("duplicate not detected: %v", err)
	}
	if duplicate.Error() != "代理名称已存在：共享代理" {
		t.Fatalf("duplicate message wrong: %q", duplicate.Error())
	}
}

func asDuplicate(err error, target **DuplicateNameError) bool {
	if typed, ok := err.(*DuplicateNameError); ok {
		*target = typed
		return true
	}
	return false
}

func TestProxyPatchCASAndTestStateReset(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-09-01T00:00:00.000Z", "passed")
	// 空补丁 → changed:false（Node 需要至少一个字段，路由层已挡；store 层
	// 的 no-op 语义保持一致）。
	outcome, err := fixture.store.Patch(context.Background(), "p-1", proxyInput{ExpectedUpdatedAt: "2026-09-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	if outcome == nil || outcome.Mutation.Changed {
		t.Fatalf("empty patch should be unchanged: %#v", outcome)
	}
	// 版本不匹配 → 409。
	host := "new.internal"
	_, err = fixture.store.Patch(context.Background(), "p-1", proxyInput{Host: &host, ExpectedUpdatedAt: "2026-08-01T00:00:00.000Z"})
	if err != ErrConflict {
		t.Fatalf("stale revision not conflict: %v", err)
	}
	// host 变更 → changed + 连接类变更重置检测状态 + runtimeChanged。
	newHost := "new.internal"
	outcome, err = fixture.store.Patch(context.Background(), "p-1", proxyInput{Host: &newHost, ExpectedUpdatedAt: "2026-09-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !outcome.Mutation.Changed || !outcome.RuntimeChanged {
		t.Fatalf("patch outcome wrong: %#v", outcome)
	}
	if outcome.Mutation.UpdatedAt != "2026-09-04T12:00:00.000Z" {
		t.Fatalf("patch updatedAt wrong: %q", outcome.Mutation.UpdatedAt)
	}
	var testStatus string
	if err := fixture.db.QueryRow(`SELECT test_status FROM proxy_profiles WHERE id = 'p-1'`).Scan(&testStatus); err != nil {
		t.Fatalf("read test status: %v", err)
	}
	if testStatus != "unknown" {
		t.Fatalf("connection change should reset test status: %q", testStatus)
	}
	// 仅名称变更 → 不重置检测状态。
	fixture.seedProfile(t, "p-2", "2026-09-01T00:00:00.000Z", "passed")
	fixture.db.Exec(`UPDATE proxy_profiles SET test_status = 'passed' WHERE id = 'p-2'`)
	newName := "改名"
	outcome, err = fixture.store.Patch(context.Background(), "p-2", proxyInput{Name: &newName, ExpectedUpdatedAt: "2026-09-01T00:00:00.000Z"})
	if err != nil {
		t.Fatalf("rename patch: %v", err)
	}
	if outcome.RuntimeChanged {
		t.Fatalf("rename should not reset runtime state: %#v", outcome)
	}
	if err := fixture.db.QueryRow(`SELECT test_status FROM proxy_profiles WHERE id = 'p-2'`).Scan(&testStatus); err != nil {
		t.Fatalf("read: %v", err)
	}
	if testStatus != "passed" {
		t.Fatalf("rename should keep test status: %q", testStatus)
	}
}

func TestProxyDeleteInUseGuard(t *testing.T) {
	fixture := newProxyFixture(t)
	fixture.seedProfile(t, "p-1", "2026-09-01T00:00:00.000Z", "unknown")
	if _, err := fixture.db.Exec(`INSERT INTO accounts (id, name, proxy_profile_id) VALUES ('a-1', '账户A', 'p-1')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	_, err := fixture.store.Delete(context.Background(), "p-1")
	var inUse *InUseError
	if err == nil || !asInUse(err, &inUse) {
		t.Fatalf("in-use not detected: %v", err)
	}
	if inUse.Error() != "这个代理仍被 1 个账户使用，请先在账户管理中解绑或改绑后再删除：账户A" {
		t.Fatalf("in-use message wrong: %q", inUse.Error())
	}
	// 解绑后可删除。
	fixture.db.Exec(`UPDATE accounts SET proxy_profile_id = NULL WHERE id = 'a-1'`)
	name, err := fixture.store.Delete(context.Background(), "p-1")
	if err != nil || name != "代理-p-1" {
		t.Fatalf("delete wrong: %q %v", name, err)
	}
	// 再删 → not found。
	name, err = fixture.store.Delete(context.Background(), "p-1")
	if err != nil || name != "" {
		t.Fatalf("second delete wrong: %q %v", name, err)
	}
}

func asInUse(err error, target **InUseError) bool {
	if typed, ok := err.(*InUseError); ok {
		*target = typed
		return true
	}
	return false
}

func TestProxyCreateRouteWritesOperationLog(t *testing.T) {
	fixture := newProxyFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies", strings.NewReader(`{"name":"代理X","type":"socks5","host":"h","port":1080,"enabled":true}`))
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create not 201: %d %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.sink.entries) != 1 || fixture.sink.entries[0].Module != "proxies" || fixture.sink.entries[0].Action != "create" {
		t.Fatalf("operation log wrong: %#v", fixture.sink.entries)
	}
	if !strings.Contains(fixture.sink.entries[0].Summary, "代理X") {
		t.Fatalf("summary wrong: %q", fixture.sink.entries[0].Summary)
	}
	// Node 无条件记录 password 变更（safeChange 敏感域）；body 未带密码时
	// After 渲染为「未设置」。
	passwordChange := false
	for _, change := range fixture.sink.entries[0].Changes {
		if change.Field == "password" {
			passwordChange = true
			if change.Before != "未设置" || change.After != "未设置" || !change.Sensitive {
				t.Fatalf("password change wrong: %#v", change)
			}
		}
	}
	if !passwordChange {
		t.Fatalf("password change missing: %#v", fixture.sink.entries[0].Changes)
	}
	// 带密码创建 → After「已变更」。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies", strings.NewReader(`{"name":"代理Y","type":"http","host":"h2","port":8080,"password":"secret"}`))
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("password create not 201: %d %s", recorder.Code, recorder.Body.String())
	}
	after := ""
	for _, change := range fixture.sink.entries[1].Changes {
		if change.Field == "password" {
			after = change.After
		}
	}
	if after != "已变更" {
		t.Fatalf("password after wrong: %q", after)
	}
	// schema 失败 → 400 代理参数无效。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies", strings.NewReader(`{"name":"代理X","unknown":1}`))
	request = request.WithContext(authsys.WithAuthContext(request.Context(), fixture.auth("")))
	createHandler(recorder, request, fixture.store, fixture.sink)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid body not 400: %d", recorder.Code)
	}
}

func (f *proxyFixture) seedProfile(t *testing.T, id, updatedAt, testStatus string) {
	t.Helper()
	name := "代理-" + id
	if _, err := f.db.Exec(`INSERT INTO proxy_profiles (id, system_account_id, name, type, host, port, enabled, test_status, created_at, updated_at)
		VALUES (?, 'sa-1', ?, 'http', 'h', 8080, 1, ?, ?, ?)`, id, name, testStatus, updatedAt, updatedAt); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
}
