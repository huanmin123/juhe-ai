package systemteams

// w11f 覆盖波次：脚本化 sqlite 连接层故障注入 + 直连 handler/纯函数分支测试。
// 连接器默认直通真实 sqlite，按查询子串注入 Query/Exec/Begin/Commit 故障、
// 受影响行数覆盖与罐装行（用于触发 Scan 错误与 rows.Err 错误臂）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authz"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var w11fBoom = errors.New("w11f boom")

// ---------------------------------------------------------------------------
// 故障脚本
// ---------------------------------------------------------------------------

type w11fRule struct {
	substr   string
	queryErr error
	execErr  error
	cols     []string
	rows     [][]driver.Value
	nextErr  error
	affected int64
	hasAff   bool
	limit    int // <=0 视为 1 次
	hits     int
	// exempt 标记负对照臂：注册后故意不命中，豁免 assertRulesFired。
	exempt bool
}

type w11fScript struct {
	mu         sync.Mutex
	t          *testing.T
	rules      []*w11fRule
	beginErr   error
	commitErr  error
	rollbackEr error
}

// allowUnfired 把规则标记为负对照（故意不命中），Cleanup 断言跳过。
func (r *w11fRule) allowUnfired() *w11fRule {
	r.exempt = true
	return r
}

// assertRulesFired 在测试收尾断言所有注入规则都被真实消费过（hits>0）：
// 子串与生产 SQL 漂移导致规则永不命中的伪覆盖臂在此变红。
func (s *w11fScript) assertRulesFired() {
	if s.t == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.hits == 0 && !r.exempt {
			s.t.Errorf("w11f 注入规则未命中（伪覆盖）：substr=%q", r.substr)
		}
	}
}

func (s *w11fScript) rule(r *w11fRule) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return r
}

// failQuery 注册一次性查询失败（默认一次，避免吞掉后续同子串规则）。
func (s *w11fScript) failQuery(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom})
}

// failQueryAlways 注册持续查询失败（同一子串每次命中都失败）。
func (s *w11fScript) failQueryAlways(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom, limit: -1})
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	return s.rule(&w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr})
}

// failExec 注册一次性 Exec 失败。
func (s *w11fScript) failExec(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fScript) execAffected(substr string, affected int64) *w11fRule {
	return s.rule(&w11fRule{substr: substr, affected: affected, hasAff: true})
}

func (s *w11fScript) failBegin() { s.mu.Lock(); s.beginErr = w11fBoom; s.mu.Unlock() }
func (s *w11fScript) failCommit() {
	s.mu.Lock()
	s.commitErr = w11fBoom
	s.mu.Unlock()
}

func (s *w11fScript) take(query string) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(query, r.substr) {
			continue
		}
		if r.limit >= 0 && r.hits >= max(r.limit, 1) {
			continue
		}
		r.hits++
		return r
	}
	return nil
}

// beginFailure 是一次性的: 首次 Begin 命中后清除。
func (s *w11fScript) beginFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.beginErr
	s.beginErr = nil
	return err
}

// commitFailure 是一次性的: 首次 Commit 命中后清除。
func (s *w11fScript) commitFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitErr
	s.commitErr = nil
	return err
}

type w11fConnector struct {
	base   driver.Connector
	script *w11fScript
}

func (c w11fConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &w11fConn{base: conn, script: c.script}, nil
}

func (c w11fConnector) Driver() driver.Driver { return c.base.Driver() }

type w11fConn struct {
	base   driver.Conn
	script *w11fScript
}

func (c *w11fConn) Prepare(query string) (driver.Stmt, error) { return c.base.Prepare(query) }
func (c *w11fConn) Close() error                              { return c.base.Close() }

func (c *w11fConn) Begin() (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	tx, err := c.base.Begin()
	if err != nil {
		return nil, err
	}
	return &w11fTx{base: tx, script: c.script}, nil
}

func (c *w11fConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.script.beginFailure(); err != nil {
		return nil, err
	}
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &w11fTx{base: tx, script: c.script}, nil
	}
	return c.Begin()
}

func (c *w11fConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.execErr != nil {
			return nil, rule.execErr
		}
		if rule.hasAff {
			return w11fResult{affected: rule.affected}, nil
		}
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *w11fConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if rule := c.script.take(query); rule != nil {
		if rule.queryErr != nil {
			return nil, rule.queryErr
		}
		if rule.cols != nil {
			return &w11fRows{cols: rule.cols, values: rule.rows, nextErr: rule.nextErr}, nil
		}
	}
	return c.base.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type w11fTx struct {
	base   driver.Tx
	script *w11fScript
}

func (t *w11fTx) Commit() error {
	if err := t.script.commitFailure(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}

func (t *w11fTx) Rollback() error { return t.base.Rollback() }

type w11fResult struct{ affected int64 }

func (r w11fResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11fResult) RowsAffected() (int64, error) { return r.affected, nil }

type w11fRows struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
	closed  bool
}

func (r *w11fRows) Columns() []string { return r.cols }
func (r *w11fRows) Close() error      { r.closed = true; return nil }
func (r *w11fRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	row := r.values[r.next]
	r.next++
	copy(dest, row)
	return nil
}

// ---------------------------------------------------------------------------
// 环境
// ---------------------------------------------------------------------------

// w11fSink 记录 OperationLogSink.Record 调用。
type w11fSink struct {
	mu      sync.Mutex
	entries []authsys.OperationLogEntry
}

func (s *w11fSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

func (s *w11fSink) recorded() []authsys.OperationLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authsys.OperationLogEntry{}, s.entries...)
}

// w11fEnv 是带故障脚本与 sink 的 systemteams 测试环境。
type w11fEnv struct {
	env    *testEnv
	script *w11fScript
	sink   *w11fSink
	deps   *Deps
}

func newW11FEnv(t *testing.T) *w11fEnv {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
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
	sink := &w11fSink{}
	deps := &Deps{Store: store, Sink: sink, Auth: authDeps}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	authDeps.MountAuth(k, "lax", false)
	deps.Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)
	env := &testEnv{deps: authDeps, store: store, authz: authzStore, k: k, server: server, jars: map[string]map[string]string{}}
	return &w11fEnv{env: env, script: script, sink: sink, deps: deps}
}

// w11fFailingStats 返回总是失败的统计脏标记端口。
type w11fFailingStats struct{}

func (w11fFailingStats) MarkAllGroupAccountStatsDirty(context.Context, string) error {
	return w11fBoom
}

// w11fStoreOver 在同一 DB 上重建 store，可注入失败副作用端口。
func (e *w11fEnv) w11fStoreOver(options ...Option) (*Store, error) {
	return NewStore(e.env.store.db, false, nil, e.env.authz, options...)
}

func w11fReq(method, target, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	return request
}

func w11fAuth(request *http.Request, role string) *http.Request {
	return w11fAuthAs(request, "w11f-actor", role)
}

func w11fAuthAs(request *http.Request, accountID, role string) *http.Request {
	return request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{
		SystemAccountID: accountID, Username: "w11f-user", Role: role,
	}))
}

func w11fCode(t *testing.T, handler func(http.ResponseWriter, *http.Request), request *http.Request) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	raw, _ := io.ReadAll(recorder.Result().Body)
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return recorder.Code, payload
}

// ---------------------------------------------------------------------------
// 纯函数分支
// ---------------------------------------------------------------------------

func TestW11FHelperBranches(t *testing.T) {
	// parseIntOr: 空 / 非数字 / 小于 1 / 合法。
	if got := parseIntOr("", 7); got != 7 {
		t.Fatalf("parseIntOr empty = %d", got)
	}
	if got := parseIntOr("abc", 7); got != 7 {
		t.Fatalf("parseIntOr garbage = %d", got)
	}
	if got := parseIntOr("0", 7); got != 7 {
		t.Fatalf("parseIntOr zero = %d", got)
	}
	if got := parseIntOr("3", 7); got != 3 {
		t.Fatalf("parseIntOr valid = %d", got)
	}
	// orText / valueOr。
	if orText("", "fb") != "fb" || orText("v", "fb") != "v" {
		t.Fatal("orText drift")
	}
	if valueOr(nil) != "" || valueOr(func() *string { v := "x"; return &v }()) != "x" {
		t.Fatal("valueOr drift")
	}
	// scopeFor: selfOnly / 非管理员 / admin+all / admin+filter。
	adminAuth := &authsys.AuthContext{SystemAccountID: "w11f-a", Role: "super_admin"}
	userAuth := &authsys.AuthContext{SystemAccountID: "w11f-u", Role: "user"}
	if got := scopeFor(w11fReq("GET", "/", ""), userAuth, true); got.IsAdmin || got.ViewerID != "w11f-u" {
		t.Fatalf("scopeFor selfOnly = %+v", got)
	}
	if got := scopeFor(w11fReq("GET", "/", ""), userAuth, false); got.IsAdmin {
		t.Fatalf("scopeFor user = %+v", got)
	}
	if got := scopeFor(w11fReq("GET", "/?systemAccountId=all", ""), adminAuth, false); !got.IsAdmin || got.FilterID != "" {
		t.Fatalf("scopeFor all = %+v", got)
	}
	if got := scopeFor(w11fReq("GET", "/?systemAccountId=xyz", ""), adminAuth, false); !got.IsAdmin || got.FilterID != "xyz" {
		t.Fatalf("scopeFor filter = %+v", got)
	}
	// keywordUpperBound 空前缀。
	if keywordUpperBound("") != "" {
		t.Fatal("keywordUpperBound empty drift")
	}
	// nullableString 两臂。
	if nullableString(nil) != nil {
		t.Fatal("nullableString nil drift")
	}
	value := "d"
	if got := nullableString(&value); got != "d" {
		t.Fatalf("nullableString value = %v", got)
	}
	// normalizeListWindow: page<1 / pageSize<1 / pageSize 超上限。
	if page, size := normalizeListWindow(0, 0); page != 1 || size != 20 {
		t.Fatalf("normalizeListWindow defaults = %d/%d", page, size)
	}
	if page, size := normalizeListWindow(2, 21); page != 2 || size != 20 {
		t.Fatalf("normalizeListWindow clamp = %d/%d", page, size)
	}
	// normalizeName: 空 / 超长。
	if _, err := normalizeName("   "); err == nil {
		t.Fatal("normalizeName empty must fail")
	}
	if _, err := normalizeName(strings.Repeat("长", 101)); err == nil {
		t.Fatal("normalizeName overflow must fail")
	}
	// normalizeDescription: nil / 超长。
	if got, err := normalizeDescription(nil); got != nil || err != nil {
		t.Fatalf("normalizeDescription nil = %v/%v", got, err)
	}
	long := strings.Repeat("d", 201)
	if _, err := normalizeDescription(&long); err == nil {
		t.Fatal("normalizeDescription overflow must fail")
	}
	// normalizeStatus: nil+合法 fallback / nil+非法 fallback / 非法值 / 合法值。
	if got, err := normalizeStatus(nil, "disabled"); err != nil || got != "disabled" {
		t.Fatalf("normalizeStatus fallback = %s/%v", got, err)
	}
	if _, err := normalizeStatus(nil, "bogus"); err == nil {
		t.Fatal("normalizeStatus bogus fallback must fail")
	}
	bad := "paused"
	if _, err := normalizeStatus(&bad, "active"); err == nil {
		t.Fatal("normalizeStatus bogus value must fail")
	}
	active := "active"
	if got, err := normalizeStatus(&active, "active"); err != nil || got != "active" {
		t.Fatalf("normalizeStatus active = %s/%v", got, err)
	}
	// itoa: 0 与多位数。
	if itoa(0) != "0" || itoa(421) != "421" {
		t.Fatal("itoa drift")
	}
	// table 的 PG 方言臂。
	if got := (&Store{pg: true}).table("x"); got != "juhe_business.x" {
		t.Fatalf("pg table = %q", got)
	}
	// NewStore nil DB。
	if _, err := NewStore(nil, false, nil, nil); err == nil {
		t.Fatal("NewStore(nil) must fail")
	}
	// ActorID 缺上下文。
	if _, err := (AccessScope{ViewerID: "  "}).ActorID(); err == nil {
		t.Fatal("ActorID blank must fail")
	}
	// JSONString: 非法 JSON 类型 → 错误。
	var js JSONString
	if err := js.UnmarshalJSON([]byte(`123`)); err == nil {
		t.Fatal("JSONString number must fail")
	}
	// canonicalExpectedVersion: 空 / 非法。
	if _, err := canonicalExpectedVersion("  "); err == nil {
		t.Fatal("canonicalExpectedVersion empty must fail")
	}
	if _, err := canonicalExpectedVersion("nope"); err == nil {
		t.Fatal("canonicalExpectedVersion garbage must fail")
	}
	// normalizeSystemAccountIDs: 空项。
	if _, err := normalizeSystemAccountIDs([]string{"ok", " "}); err == nil {
		t.Fatal("normalizeSystemAccountIDs blank must fail")
	}
	// changedFieldsContains 未命中。
	if changedFieldsContains([]string{"name"}, "status") {
		t.Fatal("changedFieldsContains drift")
	}
}

// ---------------------------------------------------------------------------
// 读路径故障
// ---------------------------------------------------------------------------

func TestW11FListPageFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store

	// 查询错误。
	w.script.failQuery("SELECT t.id")
	if _, _, err := store.ListPage(context.Background(), AccessScope{}, 1, 20, ""); err == nil {
		t.Fatal("ListPage query fault must fail")
	}

	// Scan 错误: member_count 位置是布尔值。
	w.script.canned("SELECT t.id", []string{"id", "name", "description", "status", "created_at", "updated_at", "member_count"},
		[][]driver.Value{{"i", "n", "d", "active", "c", "u", true}}, nil)
	if _, _, err := store.ListPage(context.Background(), AccessScope{}, 1, 20, ""); err == nil {
		t.Fatal("ListPage scan fault must fail")
	}

	// rows.Err 错误: 先回一行再在 Next 尾部报错。
	w.script.canned("SELECT t.id", []string{"id", "name", "description", "status", "created_at", "updated_at", "member_count"},
		[][]driver.Value{{"i", "n", "d", "active", "c", "u", int64(1)}}, w11fBoom)
	if _, _, err := store.ListPage(context.Background(), AccessScope{}, 1, 20, ""); err == nil {
		t.Fatal("ListPage rows.Err fault must fail")
	}

	// 真实数据: 21 支队伍 → hasMore 截断; keyword 前缀过滤; scoped 过滤。
	for i := 0; i < 21; i++ {
		mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
			VALUES (?, ?, 'w11f-actor', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`,
			"w11f-team-"+itoa(i), "w11f-"+itoa(i))
	}
	items, hasMore, err := store.ListPage(context.Background(), AccessScope{}, 1, 20, "")
	if err != nil || !hasMore || len(items) != 20 {
		t.Fatalf("ListPage hasMore = %v/%d/%v", hasMore, len(items), err)
	}
	items, _, err = store.ListPage(context.Background(), AccessScope{}, 1, 20, "w11f-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Name, "w11f-2") {
			t.Fatalf("keyword filter leaked %q", item.Name)
		}
	}
	// scoped: 仅显示成员所在团队。
	member := "w11f-member"
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-m1', 'w11f-team-0', ?, 'active', '2024-01-01T00:00:00.000Z', '', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`, member)
	scoped := AccessScope{ViewerID: member, IsAdmin: true, FilterID: member}
	items, _, err = store.ListPage(context.Background(), scoped, 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "w11f-team-0" {
		t.Fatalf("scoped ListPage = %v", items)
	}
}

func TestW11FFindDetailFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-t1', 'w11f-detail', 'w11f-actor', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)

	// 行查询错误。
	w.script.failQuery("SELECT id, name, COALESCE(description")
	if _, err := store.FindDetail(context.Background(), "w11f-t1", AccessScope{}); err == nil {
		t.Fatal("FindDetail query fault must fail")
	}
	// scope 计数查询错误。
	scoped := AccessScope{ViewerID: "w11f-actor", IsAdmin: true, FilterID: "w11f-member"}
	w.script.failQuery("AND system_account_id = ? AND status = 'active'")
	if _, err := store.FindDetail(context.Background(), "w11f-t1", scoped); err == nil {
		t.Fatal("FindDetail scope fault must fail")
	}
	// scope 之外的账号 → nil。
	if detail, err := store.FindDetail(context.Background(), "w11f-t1", scoped); err != nil || detail != nil {
		t.Fatalf("FindDetail outsider = %v/%v", detail, err)
	}
	// memberCount 查询错误。
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-m2', 'w11f-t1', 'w11f-member', 'active', '2024-01-01T00:00:00.000Z', '', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	w.script.failQuery("WHERE team_id = ? AND status = 'active'")
	if _, err := store.FindDetail(context.Background(), "w11f-t1", AccessScope{ViewerID: "w11f-member", IsAdmin: true, FilterID: "w11f-member"}); err == nil {
		t.Fatal("FindDetail memberCount fault must fail")
	}
	// 命中: scope 内账号可见带 memberCount 的 detail。
	detail, err := store.FindDetail(context.Background(), "w11f-t1", AccessScope{ViewerID: "w11f-member", IsAdmin: true, FilterID: "w11f-member"})
	if err != nil || detail == nil || detail.MemberCount != 1 {
		t.Fatalf("FindDetail hit = %+v/%v", detail, err)
	}
}

func w11fSeedTeamWithMembers(t *testing.T, w *w11fEnv) (teamID, revision, memberAccount string) {
	t.Helper()
	revision = "2024-01-01T00:00:00.000Z"
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-tm', 'w11f-members', 'w11f-actor', ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
		VALUES ('w11f-acc1', 'w11f-user1', 'w11f-name1', 'user', 'active', 'x', 0, ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-mm1', 'w11f-tm', 'w11f-acc1', 'active', ?, '', ?, ?)`, revision, revision, revision)
	return "w11f-tm", revision, "w11f-acc1"
}

func TestW11FListMembersFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	teamID, revision, memberAccount := w11fSeedTeamWithMembers(t, w)

	// 团队不存在 → nil。
	if page, err := store.ListMembers(context.Background(), "w11f-missing", AccessScope{}, 1, 20); err != nil || page != nil {
		t.Fatalf("ListMembers missing = %v/%v", page, err)
	}
	// 团队行查询错误。
	w.script.failQuery("SELECT id, updated_at FROM")
	if _, err := store.ListMembers(context.Background(), teamID, AccessScope{}, 1, 20); err == nil {
		t.Fatal("ListMembers team fault must fail")
	}
	// scope 计数查询错误。
	scoped := AccessScope{ViewerID: "w11f-actor", IsAdmin: true, FilterID: "w11f-other"}
	w.script.failQuery("AND system_account_id = ? AND status = 'active'")
	if _, err := store.ListMembers(context.Background(), teamID, scoped, 1, 20); err == nil {
		t.Fatal("ListMembers scope fault must fail")
	}
	// scope 之外的账号 → nil。
	if page, err := store.ListMembers(context.Background(), teamID, scoped, 1, 20); err != nil || page != nil {
		t.Fatalf("ListMembers outsider = %v/%v", page, err)
	}
	// memberCount 查询错误。
	inScope := AccessScope{ViewerID: memberAccount, IsAdmin: true, FilterID: memberAccount}
	w.script.failQuery("WHERE team_id = ? AND status = 'active'")
	if _, err := store.ListMembers(context.Background(), teamID, inScope, 1, 20); err == nil {
		t.Fatal("ListMembers memberCount fault must fail")
	}
	// 成员分页查询错误。
	w.script.failQuery("ORDER BY m.joined_at ASC")
	if _, err := store.ListMembers(context.Background(), teamID, inScope, 1, 20); err == nil {
		t.Fatal("ListMembers members fault must fail")
	}
	// Scan 错误: display_name 位置为布尔值。
	w.script.canned("ORDER BY m.joined_at ASC", []string{"id", "system_account_id", "display_name", "joined_at"},
		[][]driver.Value{{nil, "a", nil, "j"}}, nil)
	if _, err := store.ListMembers(context.Background(), teamID, inScope, 1, 20); err == nil {
		t.Fatal("ListMembers scan fault must fail")
	}
	// rows.Err 错误。
	w.script.canned("ORDER BY m.joined_at ASC", []string{"id", "system_account_id", "display_name", "joined_at"},
		[][]driver.Value{{"m", "a", nil, "j"}}, w11fBoom)
	if _, err := store.ListMembers(context.Background(), teamID, inScope, 1, 20); err == nil {
		t.Fatal("ListMembers rows.Err fault must fail")
	}
	// 命中。
	page, err := store.ListMembers(context.Background(), teamID, inScope, 1, 20)
	if err != nil || page == nil || len(page.Items) != 1 || page.Items[0].SystemAccountID != memberAccount {
		t.Fatalf("ListMembers hit = %+v/%v", page, err)
	}
	if page.UpdatedAt != revision || page.Total != 1 {
		t.Fatalf("ListMembers envelope = %+v", page)
	}
	_ = teamID
}

func TestW11FListHistoryFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	teamID, revision, memberAccount := w11fSeedTeamWithMembers(t, w)
	// 一条 removed 记录。
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, removed_at, created_by, created_at, updated_at)
		VALUES ('w11f-mm2', 'w11f-tm', 'w11f-acc1', 'removed', ?, ?, '', ?, ?)`,
		revision, revision, revision, revision)

	// 团队不存在 → nil。
	if page, err := store.ListHistory(context.Background(), "w11f-missing", AccessScope{}, 1, 20); err != nil || page != nil {
		t.Fatalf("ListHistory missing = %v/%v", page, err)
	}
	// 团队行查询错误。
	w.script.failQuery("SELECT id FROM")
	if _, err := store.ListHistory(context.Background(), teamID, AccessScope{}, 1, 20); err == nil {
		t.Fatal("ListHistory team fault must fail")
	}
	// scope 计数查询错误。
	scoped := AccessScope{ViewerID: "w11f-actor", IsAdmin: true, FilterID: "w11f-other"}
	w.script.failQuery("AND system_account_id = ? AND status = 'active'")
	if _, err := store.ListHistory(context.Background(), teamID, scoped, 1, 20); err == nil {
		t.Fatal("ListHistory scope fault must fail")
	}
	// scope 之外 → nil。
	if page, err := store.ListHistory(context.Background(), teamID, scoped, 1, 20); err != nil || page != nil {
		t.Fatalf("ListHistory outsider = %v/%v", page, err)
	}
	// 历史查询错误。
	w.script.failQuery("m.status = 'removed'")
	if _, err := store.ListHistory(context.Background(), teamID, AccessScope{}, 1, 20); err == nil {
		t.Fatal("ListHistory rows fault must fail")
	}
	// Scan 错误: removed_at 位置为布尔值。
	w.script.canned("m.status = 'removed'", []string{"id", "system_account_id", "display_name", "joined_at", "status", "removed_at"},
		[][]driver.Value{{nil, "a", nil, "j", "removed", nil}}, nil)
	if _, err := store.ListHistory(context.Background(), teamID, AccessScope{}, 1, 20); err == nil {
		t.Fatal("ListHistory scan fault must fail")
	}
	// rows.Err 错误。
	w.script.canned("m.status = 'removed'", []string{"id", "system_account_id", "display_name", "joined_at", "status", "removed_at"},
		[][]driver.Value{{"m", "a", nil, "j", "removed", nil}}, w11fBoom)
	if _, err := store.ListHistory(context.Background(), teamID, AccessScope{}, 1, 20); err == nil {
		t.Fatal("ListHistory rows.Err fault must fail")
	}
	// 命中。
	page, err := store.ListHistory(context.Background(), teamID, AccessScope{}, 1, 20)
	if err != nil || page == nil || len(page.Items) != 1 || page.Items[0].RemovedAt == nil {
		t.Fatalf("ListHistory hit = %+v/%v", page, err)
	}
	_ = memberAccount
}

func TestW11FCreateFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store

	// 名称超长。
	if _, err := store.Create(context.Background(), strings.Repeat("名", 101), nil, nil, "w11f-actor"); err == nil {
		t.Fatal("Create long name must fail")
	}
	// 说明超长。
	long := strings.Repeat("d", 201)
	if _, err := store.Create(context.Background(), "w11f-ok", &long, nil, "w11f-actor"); err == nil {
		t.Fatal("Create long description must fail")
	}
	// 状态非法。
	bad := "bogus"
	if _, err := store.Create(context.Background(), "w11f-ok", nil, &bad, "w11f-actor"); err == nil {
		t.Fatal("Create bogus status must fail")
	}
	// Exec 错误。
	w.script.failExec("INSERT INTO system_teams")
	if _, err := store.Create(context.Background(), "w11f-ok", nil, nil, "w11f-actor"); err == nil {
		t.Fatal("Create exec fault must fail")
	}
	// 命中: 带 description。
	description := "w11f-desc"
	item, err := store.Create(context.Background(), "w11f-ok", &description, nil, "w11f-actor")
	if err != nil || item.ID == "" || item.Description == nil || *item.Description != "w11f-desc" {
		t.Fatalf("Create hit = %+v/%v", item, err)
	}
}

// ---------------------------------------------------------------------------
// 写路径故障
// ---------------------------------------------------------------------------

func TestW11FPatchFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	access := AccessScope{ViewerID: "w11f-actor", IsAdmin: true}
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-pt', 'w11f-patch', 'w11f-actor', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	version := "2024-01-01T00:00:00.000Z"
	newName := "w11f-new"

	// Actor 缺失 / 版本缺失 / 版本非法。
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName}, AccessScope{}); err == nil {
		t.Fatal("Patch actor fault must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: " "}, access); err == nil {
		t.Fatal("Patch empty version must fail")
	}
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: "zzz"}, access); err == nil {
		t.Fatal("Patch garbage version must fail")
	}
	// Begin 失败。
	w.script.failBegin()
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch begin fault must fail")
	}
	// 行查询错误。
	w.script.failQuery("SELECT name, description, status, updated_at")
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch row fault must fail")
	}
	// 不存在的团队 → not_found。
	outcome, err := store.Patch(context.Background(), "w11f-missing", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access)
	if err != nil || outcome.Status != "not_found" {
		t.Fatalf("Patch missing = %+v/%v", outcome, err)
	}
	// 名称超长。
	tooLong := strings.Repeat("名", 101)
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &tooLong, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch long name must fail")
	}
	// 说明超长。
	longDesc := strings.Repeat("d", 201)
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Description: JSONString{Present: true, Value: longDesc}, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch long description must fail")
	}
	// 状态非法。
	bad := "bogus"
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Status: &bad, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch bogus status must fail")
	}
	// UPDATE 错误。
	w.script.failExec("UPDATE system_teams")
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch update fault must fail")
	}
	// 受影响行为 0 → conflict。
	w.script.execAffected("UPDATE system_teams", 0)
	outcome, err = store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access)
	if err != nil || outcome.Status != "conflict" {
		t.Fatalf("Patch affected0 = %+v/%v", outcome, err)
	}
	// Commit 失败。
	w.script.failCommit()
	if _, err := store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: version}, access); err == nil {
		t.Fatal("Patch commit fault must fail")
	}
	// 唯一名冲突 → 团队名称已存在。
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-pt2', 'w11f-taken', 'w11f-actor', '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`)
	taken := "w11f-taken"
	_, err = store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &taken, ExpectedUpdatedAt: version}, access)
	if err == nil || !strings.Contains(err.Error(), "团队名称已存在") {
		t.Fatalf("Patch duplicate = %v", err)
	}
	// afterCommit 失败: 状态翻转成功提交后统计端口报错（提交已生效、版本已推进）。
	failing, err := w.w11fStoreOver(WithSideEffects(w11fFailingStats{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	disabled := "disabled"
	currentVersion := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-pt'`)
	if _, err := failing.Patch(context.Background(), "w11f-pt", PatchInput{Status: &disabled, ExpectedUpdatedAt: currentVersion}, access); err == nil {
		t.Fatal("Patch afterCommit fault must fail")
	}
	// 正常更新（用当前版本）。
	currentVersion = mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-pt'`)
	outcome, err = store.Patch(context.Background(), "w11f-pt", PatchInput{Name: &newName, ExpectedUpdatedAt: currentVersion}, access)
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("Patch hit = %+v/%v", outcome, err)
	}
}

func TestW11FAddMembersFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	access := AccessScope{ViewerID: "w11f-actor", IsAdmin: true}
	revision := "2024-01-01T00:00:00.000Z"
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-at', 'w11f-add', 'w11f-actor', ?, ?)`, revision, revision)
	for i := 0; i < 3; i++ {
		mustExec(t, w.env, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', 0, ?, ?)`,
			"w11f-acc"+itoa(i), "w11f-user"+itoa(i), "w11f-name"+itoa(i), revision, revision)
	}

	// Actor / 规范化 / 批次边界。
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, AccessScope{}); err == nil {
		t.Fatal("AddMembers actor fault must fail")
	}
	if _, err := store.AddMembers(context.Background(), "w11f-at", nil, revision, access); err == nil {
		t.Fatal("AddMembers empty batch must fail")
	}
	big := make([]string, 21)
	for i := range big {
		big[i] = "w11f-acc" + itoa(i%3)
	}
	if _, err := store.AddMembers(context.Background(), "w11f-at", big[:20], revision, access); err == nil {
		t.Fatal("AddMembers oversized batch must fail")
	}
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, "zzz", access); err == nil {
		t.Fatal("AddMembers garbage version must fail")
	}
	// Begin 失败。
	w.script.failBegin()
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers begin fault must fail")
	}
	// 团队行查询错误。
	w.script.failQuery("WHERE id = ? AND status = 'active'")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers team fault must fail")
	}
	// 现有成员查询错误 / Scan 错误 / rows.Err 错误。
	w.script.failQuery("ORDER BY system_account_id ASC LIMIT")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers existing fault must fail")
	}
	w.script.canned("ORDER BY system_account_id ASC LIMIT", []string{"system_account_id"}, [][]driver.Value{{nil}}, nil)
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers existing scan fault must fail")
	}
	w.script.canned("ORDER BY system_account_id ASC LIMIT", []string{"system_account_id"}, [][]driver.Value{{"x"}}, w11fBoom)
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers existing rows.Err fault must fail")
	}
	// 21 个现有活跃成员 → 超上限。
	for i := 0; i < 21; i++ {
		mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
			VALUES (?, 'w11f-at', ?, 'active', ?, '', ?, ?)`,
			"w11f-am"+itoa(i), "w11f-bulk"+itoa(i), revision, revision, revision)
	}
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers existing overflow must fail")
	}
	mustExec(t, w.env, `DELETE FROM system_team_members WHERE team_id = 'w11f-at'`)

	// 团队成员不存在 → 校验错误。
	_, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-ghost"}, revision, access)
	if err == nil || !strings.Contains(err.Error(), "团队成员不存在或已停用") {
		t.Fatalf("AddMembers ghost = %v", err)
	}
	// 账户查询错误。
	w.script.failQuery("SELECT display_name, status FROM")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers account fault must fail")
	}
	// 团队 CAS 更新错误 / 受影响行为 0。
	w.script.failExec("SET updated_at = ?")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers team update fault must fail")
	}
	w.script.execAffected("SET updated_at = ?", 0)
	outcome, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access)
	if err != nil || outcome.Status != "conflict" {
		t.Fatalf("AddMembers affected0 = %+v/%v", outcome, err)
	}
	// 历史成员行查询错误。
	w.script.failQuery("ORDER BY created_at DESC")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access); err == nil {
		t.Fatal("AddMembers history fault must fail")
	}
	// 复活路径: removed 行 → UPDATE 复活; 再让复活 UPDATE 失败。
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-revive', 'w11f-at', 'w11f-acc0', 'removed', ?, '', ?, ?)`, revision, revision, revision)
	outcome, err = store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, revision, access)
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("AddMembers revive = %+v/%v", outcome, err)
	}
	if got := mustQueryString(t, w.env, `SELECT status FROM system_team_members WHERE id = 'w11f-revive'`); got != "active" {
		t.Fatalf("revive status = %q", got)
	}
	// 新插入失败（复活流程已提交并推进版本，改用当前版本）。
	currentRevision := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-at'`)
	mustExec(t, w.env, `DELETE FROM system_team_members WHERE team_id = 'w11f-at'`)
	w.script.failExec("INSERT INTO system_team_members")
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, currentRevision, access); err == nil {
		t.Fatal("AddMembers insert fault must fail")
	}
	// Commit 失败（上一失败已回滚，版本未变）。
	w.script.failCommit()
	if _, err := store.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc0"}, currentRevision, access); err == nil {
		t.Fatal("AddMembers commit fault must fail")
	}
	// afterCommit 失败（提交生效、版本推进）。
	failing, err := w.w11fStoreOver(WithSideEffects(w11fFailingStats{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	currentRevision = mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-at'`)
	if _, err := failing.AddMembers(context.Background(), "w11f-at", []string{"w11f-acc1"}, currentRevision, access); err == nil {
		t.Fatal("AddMembers afterCommit fault must fail")
	}
	// 存在成员缺失团队 → not_found。
	outcome, err = store.AddMembers(context.Background(), "w11f-missing", []string{"w11f-acc0"}, revision, access)
	if err != nil || outcome.Status != "not_found" {
		t.Fatalf("AddMembers missing = %+v/%v", outcome, err)
	}
}

func TestW11FRemoveMemberFaults(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	access := AccessScope{ViewerID: "w11f-actor", IsAdmin: true}
	revision := "2024-01-01T00:00:00.000Z"
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-rt', 'w11f-remove', 'w11f-actor', ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
		VALUES ('w11f-racc', 'w11f-ruser', 'w11f-rname', 'user', 'active', 'x', 0, ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-rm', 'w11f-rt', 'w11f-racc', 'active', ?, '', ?, ?)`, revision, revision, revision)

	// Actor / 版本 / Begin。
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, AccessScope{}); err == nil {
		t.Fatal("RemoveMember actor fault must fail")
	}
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", "zzz", access); err == nil {
		t.Fatal("RemoveMember garbage version must fail")
	}
	w.script.failBegin()
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember begin fault must fail")
	}
	// 团队行查询错误。
	w.script.failQuery("SELECT name, status, updated_at FROM")
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember team fault must fail")
	}
	// 成员行查询错误。
	w.script.failQuery("WHERE m.id = ?")
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember member fault must fail")
	}
	// 成员不存在 → not_found。
	outcome, change, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-missing", revision, access)
	if err != nil || outcome.Status != "not_found" || change != nil {
		t.Fatalf("RemoveMember missing member = %+v/%v", outcome, err)
	}
	// 计数查询错误 / Scan 错误。
	w.script.failQuery("ORDER BY system_account_id ASC LIMIT")
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember count fault must fail")
	}
	w.script.canned("ORDER BY system_account_id ASC LIMIT", []string{"system_account_id"}, [][]driver.Value{{nil}}, nil)
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember count scan fault must fail")
	}
	// 团队 CAS 更新错误 / 受影响行为 0。
	w.script.failExec("SET updated_at = ?")
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember team update fault must fail")
	}
	w.script.execAffected("SET updated_at = ?", 0)
	outcome, _, err = store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access)
	if err != nil || outcome.Status != "conflict" {
		t.Fatalf("RemoveMember affected0 = %+v/%v", outcome, err)
	}
	// 成员软删 UPDATE 错误。
	w.script.failExec("SET status = 'removed'")
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember soft delete fault must fail")
	}
	// Commit 失败。
	w.script.failCommit()
	if _, _, err := store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember commit fault must fail")
	}
	// afterCommit 失败: 提交成功但统计端口报错（成员已软删、版本已推进）。
	failing, err := w.w11fStoreOver(WithSideEffects(w11fFailingStats{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := failing.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", revision, access); err == nil {
		t.Fatal("RemoveMember afterCommit fault must fail")
	}
	// 正常移除: 重置成员为 active 并使用当前团队版本。
	mustExec(t, w.env, `UPDATE system_team_members SET status = 'active', removed_at = NULL WHERE id = 'w11f-rm'`)
	current := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-rt'`)
	outcome, change, err = store.RemoveMember(context.Background(), "w11f-rt", "w11f-rm", current, access)
	if err != nil || outcome.Status != "updated" || change == nil || change.TargetName != "w11f-rname" {
		t.Fatalf("RemoveMember hit = %+v/%+v/%v", outcome, change, err)
	}
	// 团队缺失 → not_found。
	outcome, _, err = store.RemoveMember(context.Background(), "w11f-missing", "w11f-rm", revision, access)
	if err != nil || outcome.Status != "not_found" {
		t.Fatalf("RemoveMember missing team = %+v/%v", outcome, err)
	}
}

// ---------------------------------------------------------------------------
// 路由层直连
// ---------------------------------------------------------------------------

func TestW11FRoutesDirectFaults(t *testing.T) {
	w := newW11FEnv(t)
	deps := w.deps
	revision := "2024-01-01T00:00:00.000Z"

	// 未登录: 各 handler 401。
	for _, call := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		target  string
	}{
		{"list", nil, "GET", "/__aisys__/api/system-teams"},
		{"find", nil, "GET", "/__aisys__/api/system-teams/x"},
		{"members", nil, "GET", "/__aisys__/api/system-teams/x/members"},
		{"history", nil, "GET", "/__aisys__/api/system-teams/x/members/history"},
		{"create", deps.create, "POST", "/__aisys__/api/system-teams"},
		{"patch", deps.patch, "PATCH", "/__aisys__/api/system-teams/x"},
		{"addMembers", deps.addMembers, "POST", "/__aisys__/api/system-teams/x/members"},
		{"removeMember", deps.removeMember, "DELETE", "/__aisys__/api/system-teams/x/members/y"},
	} {
		var request *http.Request
		if call.method == http.MethodGet {
			request = w11fReq(call.method, call.target, "")
		} else if call.method == http.MethodDelete {
			request = w11fReq(call.method, call.target, `{}`)
		} else {
			request = w11fReq(call.method, call.target, `{}`)
		}
		var handler func(http.ResponseWriter, *http.Request)
		switch call.name {
		case "list":
			handler = func(writer http.ResponseWriter, r *http.Request) { deps.list(writer, r, false) }
		case "find":
			handler = func(writer http.ResponseWriter, r *http.Request) { deps.find(writer, r, false) }
		case "members":
			handler = func(writer http.ResponseWriter, r *http.Request) { deps.members(writer, r, false) }
		case "history":
			handler = func(writer http.ResponseWriter, r *http.Request) { deps.history(writer, r, false) }
		default:
			handler = call.handler
		}
		code, payload := w11fCode(t, handler, request)
		if code != http.StatusUnauthorized || payload["message"] != "请先登录" {
			t.Fatalf("%s unauthenticated = %d %v", call.name, code, payload)
		}
	}

	// 存储层错误 → 500。
	w.script.failQueryAlways("FROM")
	for _, call := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		target  string
	}{
		{"list", func(writer http.ResponseWriter, r *http.Request) { deps.list(writer, r, false) }, "/__aisys__/api/system-teams"},
		{"find", func(writer http.ResponseWriter, r *http.Request) { deps.find(writer, r, false) }, "/__aisys__/api/system-teams/x"},
		{"members", func(writer http.ResponseWriter, r *http.Request) { deps.members(writer, r, false) }, "/__aisys__/api/system-teams/x/members"},
		{"history", func(writer http.ResponseWriter, r *http.Request) { deps.history(writer, r, false) }, "/__aisys__/api/system-teams/x/members/history"},
	} {
		request := w11fAuth(w11fReq(http.MethodGet, call.target, ""), "super_admin")
		code, payload := w11fCode(t, call.handler, request)
		if code != http.StatusInternalServerError || payload["message"] != "服务器内部错误" {
			t.Fatalf("%s store fault = %d %v", call.name, code, payload)
		}
	}

	// 非法 JSON 请求体 → 400 请求体无效。
	badBody := w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams", `{bad`), "super_admin")
	if code, payload := w11fCode(t, deps.create, badBody); code != http.StatusBadRequest || payload["message"] != "请求体无效" {
		t.Fatalf("create bad json = %d %v", code, payload)
	}
	badPatch := w11fAuth(w11fReq(http.MethodPatch, "/__aisys__/api/system-teams/x", `{bad`), "super_admin")
	if code, _ := w11fCode(t, deps.patch, badPatch); code != http.StatusBadRequest {
		t.Fatal("patch bad json must 400")
	}
	badAdd := w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams/x/members", `{bad`), "super_admin")
	if code, _ := w11fCode(t, deps.addMembers, badAdd); code != http.StatusBadRequest {
		t.Fatal("addMembers bad json must 400")
	}
	badRemove := w11fAuth(w11fReq(http.MethodDelete, "/__aisys__/api/system-teams/x/members/y", `{bad`), "super_admin")
	if code, _ := w11fCode(t, deps.removeMember, badRemove); code != http.StatusBadRequest {
		t.Fatal("removeMember bad json must 400")
	}

	// patch 无变更字段 → 400。
	emptyPatch := w11fAuth(w11fReq(http.MethodPatch, "/__aisys__/api/system-teams/x",
		`{"expectedUpdatedAt":"`+revision+`"}`), "super_admin")
	if code, payload := w11fCode(t, deps.patch, emptyPatch); code != http.StatusBadRequest || payload["message"] != "请至少提交一个团队变更字段" {
		t.Fatalf("patch empty = %d %v", code, payload)
	}

	// addMembers 空批次 → 400。
	emptyAdd := w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams/x/members",
		`{"systemAccountIds":[],"expectedUpdatedAt":"`+revision+`"}`), "super_admin")
	if code, payload := w11fCode(t, deps.addMembers, emptyAdd); code != http.StatusBadRequest || payload["message"] != "请至少选择一个团队成员" {
		t.Fatalf("addMembers empty = %d %v", code, payload)
	}

	// create 存储错误 → 400 创建团队失败。
	w2 := newW11FEnv(t)
	w2.script.failExec("INSERT INTO system_teams")
	request := w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams", `{"name":"w11f-route"}`), "super_admin")
	if code, payload := w11fCode(t, w2.deps.create, request); code != http.StatusBadRequest || payload["message"] != "创建团队失败" {
		t.Fatalf("create store fault = %d %v", code, payload)
	}

	// patch 存储错误 → 400 更新团队失败。
	w2.script.failQuery("SELECT name, description, status, updated_at")
	request = w11fAuth(w11fReq(http.MethodPatch, "/__aisys__/api/system-teams/x",
		`{"expectedUpdatedAt":"`+revision+`","name":"n"}`), "super_admin")
	if code, payload := w11fCode(t, w2.deps.patch, request); code != http.StatusBadRequest || payload["message"] != "更新团队失败" {
		t.Fatalf("patch store fault = %d %v", code, payload)
	}

	// addMembers 存储错误 → 400 添加团队成员失败。
	w2.script.failQuery("WHERE id = ? AND status = 'active'")
	request = w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams/x/members",
		`{"systemAccountIds":["a"],"expectedUpdatedAt":"`+revision+`"}`), "super_admin")
	if code, payload := w11fCode(t, w2.deps.addMembers, request); code != http.StatusBadRequest || payload["message"] != "添加团队成员失败" {
		t.Fatalf("addMembers store fault = %d %v", code, payload)
	}

	// removeMember 存储错误 → 400 移除团队成员失败。
	w2.script.failQuery("SELECT name, status, updated_at FROM")
	request = w11fAuth(w11fReq(http.MethodDelete, "/__aisys__/api/system-teams/x/members/y",
		`{"expectedUpdatedAt":"`+revision+`"}`), "super_admin")
	if code, payload := w11fCode(t, w2.deps.removeMember, request); code != http.StatusBadRequest || payload["message"] != "移除团队成员失败" {
		t.Fatalf("removeMember store fault = %d %v", code, payload)
	}
}

func TestW11FRoutesWithSink(t *testing.T) {
	w := newW11FEnv(t)
	deps := w.deps
	revision := "2024-01-01T00:00:00.000Z"

	// create 成功 → sink 记录 create。
	request := w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams",
		`{"name":"w11f-sink","description":"d"}`), "super_admin")
	code, _ := w11fCode(t, deps.create, request)
	if code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}
	teamID := mustQueryString(t, w.env, `SELECT id FROM system_teams WHERE name = 'w11f-sink'`)

	// addMembers 成功 → sink 记录 add_members（含成员显示名）。
	w.env.store.db.Exec(`INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
		VALUES ('w11f-sacc', 'w11f-suser', 'w11f-sname', 'user', 'active', 'x', 0, ?, ?)`, revision, revision)
	teamVersion := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = ?`, teamID)
	request = w11fAuth(w11fReq(http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["w11f-sacc"],"expectedUpdatedAt":"`+teamVersion+`"}`), "super_admin")
	request.SetPathValue("id", teamID)
	if code, _ := w11fCode(t, deps.addMembers, request); code != http.StatusOK {
		t.Fatalf("addMembers = %d", code)
	}

	// patch 成功 → sink 记录 update。
	teamVersion = mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = ?`, teamID)
	request = w11fAuth(w11fReq(http.MethodPatch, "/__aisys__/api/system-teams/"+teamID,
		`{"expectedUpdatedAt":"`+teamVersion+`","name":"w11f-sink2"}`), "super_admin")
	request.SetPathValue("id", teamID)
	if code, _ := w11fCode(t, deps.patch, request); code != http.StatusOK {
		t.Fatalf("patch = %d", code)
	}

	// removeMember 成功 → sink 记录 remove_member。
	memberRow := mustQueryString(t, w.env, `SELECT id FROM system_team_members WHERE team_id = ?`, teamID)
	current := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = ?`, teamID)
	request = w11fAuth(w11fReq(http.MethodDelete, "/__aisys__/api/system-teams/"+teamID+"/members/"+memberRow,
		`{"expectedUpdatedAt":"`+current+`"}`), "super_admin")
	request.SetPathValue("id", teamID)
	request.SetPathValue("memberId", memberRow)
	if code, _ := w11fCode(t, deps.removeMember, request); code != http.StatusOK {
		t.Fatalf("removeMember = %d", code)
	}

	entries := w.sink.recorded()
	actions := []string{}
	for _, entry := range entries {
		actions = append(actions, entry.Action)
	}
	want := []string{"create", "add_members", "update", "remove_member"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("sink actions = %v", actions)
	}
	for _, entry := range entries {
		if entry.Module != "system_teams" || entry.Mode != "admin" {
			t.Fatalf("sink entry drift = %+v", entry)
		}
	}
	if entries[1].Changes[0].After != "w11f-sname" {
		t.Fatalf("add_members change = %+v", entries[1].Changes[0])
	}
}

// TestW11FSelfRoutes 覆盖 Mount 中未被 HTTP 触达的只读 closure。
func TestW11FSelfRoutes(t *testing.T) {
	w := newW11FEnv(t)
	revision := "2024-01-01T00:00:00.000Z"
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-st', 'w11f-self', 'w11f-actor', ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
		VALUES ('w11f-sacc2', 'w11f-suser2', 'w11f-sname2', 'user', 'active', 'x', 0, ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-sm', 'w11f-st', 'w11f-sacc2', 'active', ?, '', ?, ?)`, revision, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, removed_at, created_by, created_at, updated_at)
		VALUES ('w11f-sh', 'w11f-st', 'w11f-sacc2', 'removed', ?, ?, '', ?, ?)`, revision, revision, revision, revision)

	// 管理员全量列表。
	code, payload := w11fCode(t, func(writer http.ResponseWriter, r *http.Request) {
		w.deps.list(writer, r, false)
	}, w11fAuth(w11fReq(http.MethodGet, "/__aisys__/api/system-teams?page=1&pageSize=5", ""), "super_admin"))
	if code != http.StatusOK {
		t.Fatalf("admin list = %d %v", code, payload)
	}

	// self 视角: find / members / history。
	for _, call := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request, bool)
	}{
		{"find", w.deps.find},
		{"members", w.deps.members},
		{"history", w.deps.history},
	} {
		request := w11fAuthAs(w11fReq(http.MethodGet, "/__aisys__/api/my-teams/w11f-st", ""), "w11f-sacc2", "user")
		request.SetPathValue("id", "w11f-st")
		code, payload := w11fCode(t, func(writer http.ResponseWriter, r *http.Request) {
			call.handler(writer, r, true)
		}, request)
		if code != http.StatusOK {
			t.Fatalf("self %s = %d %v", call.name, code, payload)
		}
	}
}

// TestW11FMountedRoutes 通过真实挂载路由覆盖 Mount 中未触达的 closure 与
// 404/版本错误/409 分支。
func TestW11FMountedRoutes(t *testing.T) {
	w := newW11FEnv(t)
	revision := "2024-01-01T00:00:00.000Z"
	memberAccount := w.env.login(t, "w11fmember", "w11f-pass", "user")
	w.env.user = "w11fmember"

	// 21 支队伍 → 列表 hasMore。
	for i := 0; i < 21; i++ {
		mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
			VALUES (?, ?, 'w11f-actor', ?, ?)`,
			"w11f-mr-"+itoa(i), "w11f-路由-"+itoa(i), revision, revision)
	}
	code, payload := w.env.do(t, http.MethodGet, "/__aisys__/api/my-teams?page=1&pageSize=20", "")
	if code != http.StatusOK {
		t.Fatalf("my-teams list = %d %v", code, payload)
	}

	// self 视角三闭包: find / members / history。
	teamID := "w11f-mr-0"
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-mrm', ?, ?, 'active', ?, '', ?, ?)`, teamID, memberAccount, revision, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, removed_at, created_by, created_at, updated_at)
		VALUES ('w11f-mrh', ?, ?, 'removed', ?, ?, '', ?, ?)`, teamID, memberAccount, revision, revision, revision, revision)
	for _, path := range []string{
		"/__aisys__/api/my-teams/" + teamID,
		"/__aisys__/api/my-teams/" + teamID + "/members",
		"/__aisys__/api/my-teams/" + teamID + "/members/history",
	} {
		code, payload = w.env.do(t, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("self route %s = %d %v", path, code, payload)
		}
	}

	// 管理员列表 closure + hasMore 分支。
	w.env.login(t, "w11fadmin", "w11f-pass", "super_admin")
	w.env.user = "w11fadmin"
	code, payload = w.env.do(t, http.MethodGet, "/__aisys__/api/system-teams?page=1&pageSize=20", "")
	if code != http.StatusOK || payload["data"].(map[string]any)["hasMore"] != true {
		t.Fatalf("admin list hasMore = %d %v", code, payload)
	}

	// 404: find / members / history 对不存在团队。
	for _, path := range []string{
		"/__aisys__/api/system-teams/w11f-nope",
		"/__aisys__/api/system-teams/w11f-nope/members",
		"/__aisys__/api/system-teams/w11f-nope/members/history",
	} {
		code, payload = w.env.do(t, http.MethodGet, path, "")
		if code != http.StatusNotFound || payload["message"] != "团队不存在" {
			t.Fatalf("missing team %s = %d %v", path, code, payload)
		}
	}

	// 版本格式错误: addMembers 与 removeMember → 400。
	code, payload = w.env.do(t, http.MethodPost, "/__aisys__/api/system-teams/"+teamID+"/members",
		`{"systemAccountIds":["`+memberAccount+`"],"expectedUpdatedAt":"bad"}`)
	if code != http.StatusBadRequest || payload["message"] != "团队版本格式不正确" {
		t.Fatalf("addMembers bad version = %d %v", code, payload)
	}
	code, payload = w.env.do(t, http.MethodDelete, "/__aisys__/api/system-teams/"+teamID+"/members/w11f-mrm",
		`{"expectedUpdatedAt":"bad"}`)
	if code != http.StatusBadRequest || payload["message"] != "团队版本格式不正确" {
		t.Fatalf("removeMember bad version = %d %v", code, payload)
	}

	// removeMember 过期版本 → 409。
	code, payload = w.env.do(t, http.MethodDelete, "/__aisys__/api/system-teams/"+teamID+"/members/w11f-mrm",
		`{"expectedUpdatedAt":"2001-01-01T00:00:00.000Z"}`)
	if code != http.StatusConflict || payload["message"] != "团队已被其他操作更新，请刷新后重试" {
		t.Fatalf("removeMember conflict = %d %v", code, payload)
	}
}

// TestW11FMutationEdgeBranches 补充 store 层边角分支。
func TestW11FMutationEdgeBranches(t *testing.T) {
	w := newW11FEnv(t)
	store := w.env.store
	access := AccessScope{ViewerID: "w11f-actor", IsAdmin: true}
	revision := "2024-01-01T00:00:00.000Z"
	mustExec(t, w.env, `INSERT INTO system_teams (id, name, created_by, created_at, updated_at)
		VALUES ('w11f-et', 'w11f-edge', 'w11f-actor', ?, ?)`, revision, revision)

	// 21 个不同 ID → store 层批次上限。
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = "w11f-distinct-" + itoa(i)
	}
	if _, err := store.AddMembers(context.Background(), "w11f-et", ids, revision, access); err == nil {
		t.Fatal("AddMembers 21 distinct must fail")
	}

	// 有效 description 字符串 patch（赋值臂 + rowPatch 字符串臂）。
	description := "w11f-new-desc"
	outcome, err := store.Patch(context.Background(), "w11f-et", PatchInput{Description: JSONString{Present: true, Value: description}, ExpectedUpdatedAt: revision}, access)
	if err != nil || outcome.Status != "updated" {
		t.Fatalf("Patch description = %+v/%v", outcome, err)
	}
	if outcome.Result.RowPatch["description"] != description {
		t.Fatalf("rowPatch description = %v", outcome.Result.RowPatch)
	}

	// RemoveMember 过期版本 → conflict。
	outcome2, _, err := store.RemoveMember(context.Background(), "w11f-et", "w11f-ghost", "2001-01-01T00:00:00.000Z", access)
	if err != nil || outcome2.Status != "conflict" {
		t.Fatalf("RemoveMember conflict = %+v/%v", outcome2, err)
	}

	// 复活 UPDATE 失败: removed 行 + 复活语句故障（description patch 已推进版本）。
	currentRevision := mustQueryString(t, w.env, `SELECT updated_at FROM system_teams WHERE id = 'w11f-et'`)
	mustExec(t, w.env, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, must_change_password, created_at, updated_at)
		VALUES ('w11f-eacc', 'w11f-euser', 'w11f-ename', 'user', 'active', 'x', 0, ?, ?)`, revision, revision)
	mustExec(t, w.env, `INSERT INTO system_team_members (id, team_id, system_account_id, status, joined_at, created_by, created_at, updated_at)
		VALUES ('w11f-erevive', 'w11f-et', 'w11f-eacc', 'removed', ?, '', ?, ?)`, revision, revision, revision)
	w.script.failExec("SET status = 'active', joined_at")
	if _, err := store.AddMembers(context.Background(), "w11f-et", []string{"w11f-eacc"}, currentRevision, access); err == nil {
		t.Fatal("AddMembers revive fault must fail")
	}
}
