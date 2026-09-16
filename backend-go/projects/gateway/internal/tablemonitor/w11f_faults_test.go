package tablemonitor

// w11f 覆盖波次：脚本化 sqlite 连接层故障注入 + 纯函数/handler 直连分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
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
	limit    int
	hits     int
}

type w11fScript struct {
	mu    sync.Mutex
	rules []*w11fRule
}

func (s *w11fScript) rule(r *w11fRule) *w11fRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, r)
	return r
}

func (s *w11fScript) failQuery(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom})
}

func (s *w11fScript) failQueryAlways(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, queryErr: w11fBoom, limit: -1})
}

func (s *w11fScript) canned(substr string, cols []string, rows [][]driver.Value, nextErr error) *w11fRule {
	return s.rule(&w11fRule{substr: substr, cols: cols, rows: rows, nextErr: nextErr})
}

func (s *w11fScript) failExec(substr string) *w11fRule {
	return s.rule(&w11fRule{substr: substr, execErr: w11fBoom})
}

func (s *w11fScript) execAffected(substr string, affected int64) *w11fRule {
	return s.rule(&w11fRule{substr: substr, affected: affected, hasAff: true})
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
func (c *w11fConn) Begin() (driver.Tx, error)                 { return c.base.Begin() }
func (c *w11fConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.base.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.base.Begin()
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

type w11fResult struct{ affected int64 }

func (r w11fResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11fResult) RowsAffected() (int64, error) { return r.affected, nil }

type w11fRows struct {
	cols    []string
	values  [][]driver.Value
	next    int
	nextErr error
}

func (r *w11fRows) Columns() []string { return r.cols }
func (r *w11fRows) Close() error      { return nil }
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

func newW11FDB(t *testing.T, script *w11fScript) *sql.DB {
	t.Helper()
	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + strconv.Itoa(int(w11fDBSeq.Add(1))) + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// w11fAuthRequest 构造带 admin 上下文的请求。
func w11fAuthRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	auth := &authsys.AuthContext{SystemAccountID: "w11f-admin", Username: "w11f-admin", Role: "admin"}
	return request.WithContext(authsys.WithAuthContext(request.Context(), auth))
}

func w11fInvoke(t *testing.T, handler any, request *http.Request) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	w11fServe(t, handler, recorder, request)
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
	// Error 方法与 NewDurableRecordMaintenanceDispatch。
	if errCacheBackoff.Error() == "" {
		t.Fatal("errCacheBackoff drift")
	}
	if _, err := NewDurableRecordMaintenanceDispatch(nil, false, nil); err == nil {
		t.Fatal("nil dispatch db must fail")
	}
	if _, err := NewStore(nil, false); err == nil {
		t.Fatal("nil store db must fail")
	}
	// PG 方言 arm。
	if got := (&DurableDispatch{pg: true}).table(); got != "juhe_dataset."+recordMaintenanceTableName {
		t.Fatalf("pg dispatch table = %q", got)
	}
	if got := (&Store{pg: true}).table("x"); got != "juhe_stats.x" {
		t.Fatalf("pg store table = %q", got)
	}
	// isSchemaMissing 全矩阵。
	if isSchemaMissing(nil) {
		t.Fatal("nil schema missing drift")
	}
	for _, message := range []string{"no such table: x", "no such column: y", "SQLSTATE 42P01", "SQLSTATE 42703", "relation does not exist"} {
		if !isSchemaMissing(errors.New(message)) {
			t.Fatalf("isSchemaMissing(%q) must be true", message)
		}
	}
	if isSchemaMissing(w11fBoom) {
		t.Fatal("unrelated error must not be schema missing")
	}
	// roleRank 已知与未知角色。
	if roleRank("business") != 0 || roleRank("unknown") != len(monitoredDatabaseRoles) {
		t.Fatal("roleRank drift")
	}
	// basename。
	if basename("/data/business.sqlite3") != "business.sqlite3" || basename(`C:\data\stats.db`) != "stats.db" || basename("plain.db") != "plain.db" {
		t.Fatal("basename drift")
	}
	// latestSampledAt。
	databases := []DatabaseSnapshot{{SampledAt: "2026-01-01T00:00:00.000Z"}, {SampledAt: "2026-02-01T00:00:00.000Z"}}
	if latest := latestSampledAt(databases); latest == nil || *latest != "2026-02-01T00:00:00.000Z" {
		t.Fatal("latestSampledAt drift")
	}
	if latestSampledAt(nil) != nil {
		t.Fatal("latestSampledAt nil drift")
	}
	// row 取值分支。
	testRow := row{"t": "text", "b": []byte("bytes"), "nil": nil, "i": int64(3), "f": 4.5, "s": "12.5", "bad": "x", "missing-key": nil}
	if testRow.text("t") != "text" || testRow.text("b") != "bytes" || testRow.text("nil") != "" || testRow.text("nope") != "" {
		t.Fatal("row.text drift")
	}
	if testRow.nullText("t") == nil || *testRow.nullText("t") != "text" || testRow.nullText("nil") != nil || testRow.nullText("nope") != nil {
		t.Fatal("row.nullText drift")
	}
	if got := testRow.number("i"); got == nil || *got != 3 {
		t.Fatal("row.number int drift")
	}
	if got := testRow.number("f"); got == nil || *got != 4.5 {
		t.Fatal("row.number float drift")
	}
	if got := testRow.number("s"); got == nil || *got != 12.5 {
		t.Fatal("row.number string drift")
	}
	if testRow.number("bad") != nil || testRow.number("nil") != nil || testRow.number("nope") != nil {
		t.Fatal("row.number invalid drift")
	}
	// instantMillis / canonicalInstant / validInstant。
	if _, ok := instantMillis("not-a-time"); ok {
		t.Fatal("instantMillis garbage drift")
	}
	if _, ok := instantMillis("2026-01-01 00:00:00Z"); ok {
		t.Fatal("instantMillis non-rfc3339 drift")
	}
	if _, ok := canonicalInstant("zzz"); ok {
		t.Fatal("canonicalInstant garbage drift")
	}
	if validInstant("zzz") || !validInstant("2026-01-01T00:00:00Z") {
		t.Fatal("validInstant drift")
	}
	// coerceOptionalQueryInt 全分支。
	values := func(raw string) url.Values { return url.Values{"limit": {raw}} }
	if _, has, issue := coerceOptionalQueryInt(url.Values{}, "limit", 1, 10); has || issue != "" {
		t.Fatal("absent limit drift")
	}
	if _, _, issue := coerceOptionalQueryInt(values(""), "limit", 1, 10); issue != zodNumberMin(1) {
		t.Fatalf("blank min>0 = %q", issue)
	}
	if _, _, issue := coerceOptionalQueryInt(values(""), "limit", 0, 10); issue != "" {
		t.Fatalf("blank min=0 = %q", issue)
	}
	if _, _, issue := coerceOptionalQueryInt(values("x"), "limit", 1, 10); issue != "Expected number, received nan" {
		t.Fatalf("nan = %q", issue)
	}
	if _, _, issue := coerceOptionalQueryInt(values("1.5"), "limit", 1, 10); !strings.Contains(issue, "integer") {
		t.Fatalf("fraction = %q", issue)
	}
	if _, _, issue := coerceOptionalQueryInt(values("0"), "limit", 1, 10); issue != zodNumberMin(1) {
		t.Fatalf("below min = %q", issue)
	}
	if _, _, issue := coerceOptionalQueryInt(values("11"), "limit", 1, 10); issue != zodNumberMax(10) {
		t.Fatalf("above max = %q", issue)
	}
	if value, has, issue := coerceOptionalQueryInt(values("5"), "limit", 1, 10); !has || issue != "" || value != 5 {
		t.Fatal("valid limit drift")
	}
	// cleanupBlockedReason 两分支。
	if !strings.Contains(cleanupBlockedReason("worker_ipc_unavailable"), "supervisor") {
		t.Fatal("ipc unavailable reason drift")
	}
	if !strings.Contains(cleanupBlockedReason("other"), "投递失败") {
		t.Fatal("default reason drift")
	}
	// unrecognizedKeys。
	if keys := unrecognizedKeys(map[string]any{"a": 1, "b": 2}, "a"); len(keys) != 1 || keys[0] != "b" {
		t.Fatalf("unrecognizedKeys = %v", keys)
	}
	// cleanupActor / authContextField nil 分支。
	if cleanupActor(httptest.NewRequest(http.MethodPost, "/", nil)) != "anonymous" {
		t.Fatal("cleanupActor anonymous drift")
	}
	if authContextField(nil, func(a *authsys.AuthContext) string { return a.Username }) != "" {
		t.Fatal("authContextField nil drift")
	}
	// cleanupFingerprint。
	request := httptest.NewRequest(http.MethodPost, "/?x=1", nil)
	if _, err := cleanupFingerprint(request); err != nil {
		t.Fatal(err)
	}
	// NewOverviewCacheWithLookup 窗口旋钮。
	cache := NewOverviewCacheWithLookup(func(name string) string {
		if name == envTableMonitorOverviewFreshWindowMs {
			return "999999999999"
		}
		return "1"
	})
	if cache.fresh != tableMonitorOverviewMaxStaleMs {
		t.Fatalf("fresh clamp = %v", cache.fresh)
	}
	if cache.stale < cache.fresh {
		t.Fatalf("stale >= fresh: %v/%v", cache.stale, cache.fresh)
	}
	cache = NewOverviewCacheWithLookup(func(name string) string {
		if name == envTableMonitorOverviewFreshWindowMs {
			return "bad"
		}
		return "-5"
	})
	if cache.fresh != tableMonitorOverviewDefaultFreshMs {
		t.Fatalf("fresh fallback = %v", cache.fresh)
	}
	// parsePositiveDurationEnvMs / clampOverviewWindowMs。
	if got := parsePositiveDurationEnvMs(func(string) string { return "NaN" }, "X", 5*time.Millisecond); got != 5*time.Millisecond {
		t.Fatal("parse NaN drift")
	}
	if got := clampOverviewWindowMs(time.Microsecond); got != time.Millisecond {
		t.Fatal("clamp min drift")
	}
}

// ---------------------------------------------------------------------------
// dispatch / store 故障
// ---------------------------------------------------------------------------

func TestW11FDispatchFaults(t *testing.T) {
	script := &w11fScript{}
	db := newW11FDB(t, script)
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 建表失败 / 列举失败 / ALTER 失败 → worker_dispatch_failed。
	script.failExec("CREATE TABLE")
	if result := dispatch.EnqueueNonBusinessDataCleanup(ctx, RecordMaintenanceJob{ID: "w11f-job", CutoffAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z"}); result.Queued || result.DroppedReason != "worker_dispatch_failed" {
		t.Fatalf("schema fault = %+v", result)
	}
	script2 := &w11fScript{}
	db2 := newW11FDB(t, script2)
	dispatch2, _ := NewDurableRecordMaintenanceDispatch(db2, false, time.Now)
	script2.failQuery("PRAGMA table_info")
	if result := dispatch2.EnqueueNonBusinessDataCleanup(ctx, RecordMaintenanceJob{ID: "w11f-job"}); result.Queued {
		t.Fatalf("columns fault = %+v", result)
	}
	// 旧表缺列 → ALTER 补列成功。
	script3 := &w11fScript{}
	db3 := newW11FDB(t, script3)
	if _, err := db3.Exec(`CREATE TABLE ` + recordMaintenanceTableName + ` (id TEXT PRIMARY KEY, type TEXT NOT NULL, cutoff_at TEXT NOT NULL, batch_size INTEGER NOT NULL, max_batches INTEGER NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	dispatch3, _ := NewDurableRecordMaintenanceDispatch(db3, false, time.Now)
	if result := dispatch3.EnqueueNonBusinessDataCleanup(ctx, RecordMaintenanceJob{ID: "w11f-job", CutoffAt: "c", CreatedAt: "2026-01-01T00:00:00Z"}); !result.Queued {
		t.Fatalf("upgrade enqueue = %+v", result)
	}
	// INSERT 失败。
	script4 := &w11fScript{}
	db4 := newW11FDB(t, script4)
	if _, err := db4.Exec(`CREATE TABLE ` + recordMaintenanceTableName + ` (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	dispatch4, _ := NewDurableRecordMaintenanceDispatch(db4, false, time.Now)
	script4.failExec("INSERT INTO")
	if result := dispatch4.EnqueueNonBusinessDataCleanup(ctx, RecordMaintenanceJob{ID: "w11f-job", CreatedAt: "2026-01-01T00:00:00Z"}); result.Queued {
		t.Fatalf("insert fault = %+v", result)
	}

	// 快照入队: 无效 updatedAt / 序列化失败 / 建表失败 / INSERT 失败 / 成功。
	script5 := &w11fScript{}
	db5 := newW11FDB(t, script5)
	dispatch5, _ := NewDurableRecordMaintenanceDispatch(db5, false, time.Now)
	if result := dispatch5.EnqueueAccountUsageSnapshotUpsert(ctx, RecordMaintenanceSnapshotJob{UpdatedAt: "zzz"}); result.Queued {
		t.Fatalf("snapshot bad time = %+v", result)
	}
	if result := dispatch5.EnqueueAccountUsageSnapshotUpsert(ctx, RecordMaintenanceSnapshotJob{UpdatedAt: "2026-01-01T00:00:00Z", Snapshot: map[string]any{"bad": make(chan int)}}); result.Queued {
		t.Fatalf("snapshot marshal fault = %+v", result)
	}
	if result := dispatch5.EnqueueAccountUsageSnapshotUpsert(ctx, RecordMaintenanceSnapshotJob{UpdatedAt: "2026-01-01T00:00:00Z", Snapshot: map[string]any{"k": 1}}); !result.Queued {
		t.Fatalf("snapshot hit = %+v", result)
	}
	// 幂等 ensured 缓存 + 恢复路径。
	if err := dispatch5.ensureSchema(ctx); err != nil {
		t.Fatalf("ensured cache = %v", err)
	}
	// newRecordMaintenanceJobID 形状。
	if id := newRecordMaintenanceJobID(time.Now()); !strings.HasPrefix(id, "recmaint_") {
		t.Fatalf("job id = %q", id)
	}
}

// TestW11FDispatchPGColumns 用脚本化列结果覆盖 existingColumns 的 PG arm。
func TestW11FDispatchPGColumns(t *testing.T) {
	script := &w11fScript{}
	db := newW11FDB(t, script)
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, true, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// CREATE 成功（脚本放行 affected），列查询走 information_schema 罐装行，ALTER 也放行。
	script.execAffected("CREATE TABLE", 0)
	for i := 0; i < 6; i++ {
		script.execAffected("ALTER TABLE", 0)
	}
	script.canned("information_schema.columns", []string{"column_name"},
		[][]driver.Value{{"id"}, {"type"}, {"cutoff_at"}, {"batch_size"}, {"max_batches"}, {"created_at"}, {"account_id"}}, nil)
	if err := dispatch.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 已 ensured → 直接返回。
	if err := dispatch.ensureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	// PG 列举查询错误 / Scan 错误 / rows.Err。
	script2 := &w11fScript{}
	db2 := newW11FDB(t, script2)
	dispatch2, _ := NewDurableRecordMaintenanceDispatch(db2, true, time.Now)
	script2.execAffected("CREATE TABLE", 0)
	script2.failQuery("information_schema.columns")
	if err := dispatch2.ensureSchema(context.Background()); err == nil {
		t.Fatal("pg columns fault must fail")
	}
	script3 := &w11fScript{}
	db3 := newW11FDB(t, script3)
	dispatch3, _ := NewDurableRecordMaintenanceDispatch(db3, true, time.Now)
	script3.execAffected("CREATE TABLE", 0)
	script3.canned("information_schema.columns", []string{"column_name"}, [][]driver.Value{{true}}, nil)
	if err := dispatch3.ensureSchema(context.Background()); err == nil {
		t.Fatal("pg columns scan fault must fail")
	}
	script4 := &w11fScript{}
	db4 := newW11FDB(t, script4)
	dispatch4, _ := NewDurableRecordMaintenanceDispatch(db4, true, time.Now)
	script4.execAffected("CREATE TABLE", 0)
	script4.canned("information_schema.columns", []string{"column_name"}, [][]driver.Value{{"id"}}, w11fBoom)
	if err := dispatch4.ensureSchema(context.Background()); err == nil {
		t.Fatal("pg columns rows.Err fault must fail")
	}

	// SQLite 列举 Scan 错误 / rows.Err。
	script5 := &w11fScript{}
	db5 := newW11FDB(t, script5)
	if _, err := db5.Exec(`CREATE TABLE ` + recordMaintenanceTableName + ` (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	dispatch5, _ := NewDurableRecordMaintenanceDispatch(db5, false, time.Now)
	// PRAGMA 行的 name 列为 NULL 时 Scan 失败。
	script5.canned("PRAGMA table_info", []string{"cid", "name", "type", "notnull", "dflt_value", "pk"},
		[][]driver.Value{{int64(0), nil, "TEXT", int64(0), nil, int64(0)}}, nil)
	if err := dispatch5.ensureSchema(context.Background()); err == nil {
		t.Fatal("sqlite columns scan fault must fail")
	}
}

func TestW11FStoreReadFaults(t *testing.T) {
	script := &w11fScript{}
	db := newW11FDB(t, script)
	if _, err := db.Exec(monitorSchema); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 数据库快照查询错误 / 计数查询错误 / 表快照查询错误。
	script.failQuery("FROM database_storage_snapshots AS snapshots")
	if _, err := store.LoadOverview(ctx, 1, 10, ""); err == nil {
		t.Fatal("overview databases fault must fail")
	}
	script.failQuery("SELECT COUNT(*) AS total")
	if _, err := store.LoadOverview(ctx, 1, 10, ""); err == nil {
		t.Fatal("overview count fault must fail")
	}
	script.failQuery("FROM table_storage_snapshots AS snapshots")
	if _, err := store.LoadOverview(ctx, 1, 10, ""); err == nil {
		t.Fatal("overview tables fault must fail")
	}
	// 缺表 → ErrSchemaUnavailable。
	script2 := &w11fScript{}
	db2 := newW11FDB(t, script2)
	store2, _ := NewStore(db2, false)
	if _, err := store2.LoadOverview(ctx, 1, 10, ""); err != ErrSchemaUnavailable {
		t.Fatalf("missing schema = %v", err)
	}
	// 表历史 / 库历史错误分支 + 角色并列排序。
	script.failQuery("FROM table_storage_snapshots")
	if _, err := store.LoadTableHistory(ctx, "business", "accounts", "2026-01-01T00:00:00.000Z", "2026-02-01T00:00:00.000Z", 10); err == nil {
		t.Fatal("table history fault must fail")
	}
	script.failQuery("FROM database_storage_snapshots")
	if _, err := store.LoadDatabaseHistory(ctx, "2026-01-01T00:00:00.000Z", "2026-02-01T00:00:00.000Z", 10); err == nil {
		t.Fatal("database history fault must fail")
	}
	// 同一 sampled_at 的角色 tiebreak。
	if _, err := db.Exec(`INSERT INTO database_storage_snapshots (database_role, database_path, sampled_at) VALUES
		('business', '/b', '2026-05-01T00:00:00.000Z'), ('stats', '/s', '2026-05-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	points, err := store.LoadDatabaseHistory(ctx, "2026-04-01T00:00:00.000Z", "2026-06-01T00:00:00.000Z", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundBusinessFirst := false
	for index, point := range points {
		if point.SampledAt == "2026-05-01T00:00:00.000Z" {
			foundBusinessFirst = point.DatabaseRole == "business" && points[index+1].DatabaseRole == "stats"
			break
		}
	}
	if !foundBusinessFirst {
		t.Fatalf("role tiebreak = %+v", points)
	}
}

// TestW11FStorePostgresOverview 用罐装行驱动 loadOverviewPostgres。
func TestW11FStorePostgresOverview(t *testing.T) {
	script := &w11fScript{}
	db := newW11FDB(t, script)
	store, err := NewStore(db, true)
	if err != nil {
		t.Fatal(err)
	}
	databaseCols := []string{"database_role", "database_path", "sampled_at", "file_bytes", "wal_bytes", "shm_bytes", "free_bytes", "table_count"}
	script.canned("SELECT DISTINCT ON (database_role)", databaseCols, [][]driver.Value{
		{"business", "/data/business.sqlite3", "2026-09-04T00:00:00.000Z", int64(200), int64(20), nil, int64(6), int64(13)},
		{"stats", "/data/stats.sqlite3", "2026-09-04T00:00:00.000Z", int64(50), int64(5), nil, int64(2), int64(7)},
	}, nil)
	script.canned("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(2)}}, nil)
	tableCols := []string{"database_role", "table_name", "sampled_at", "table_kind", "parent_table_name", "is_partition", "is_archive",
		"row_count", "table_bytes", "index_bytes", "total_bytes", "growth_bytes_1h", "growth_rows_1h", "growth_bytes_24h", "growth_rows_24h"}
	script.canned("WITH latest_snapshots", tableCols, [][]driver.Value{
		{"business", "accounts", "2026-09-04T00:00:00.000Z", nil, nil, int64(0), int64(0), int64(20), int64(200), int64(50), int64(250), nil, nil, int64(130), int64(10)},
	}, nil)

	overview, err := store.LoadOverview(context.Background(), 1, 10, "acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Databases) != 2 || len(overview.Tables) != 1 || overview.Total != 2 || !overview.HasMore {
		t.Fatalf("pg overview = %+v", overview)
	}
	if overview.Tables[0].TableName != "accounts" || overview.Tables[0].GrowthBytes24h == nil {
		t.Fatalf("pg table row = %+v", overview.Tables[0])
	}

	// PG 错误分支。
	script2 := &w11fScript{}
	db2 := newW11FDB(t, script2)
	store2, _ := NewStore(db2, true)
	script2.failQuery("SELECT DISTINCT ON (database_role)")
	if _, err := store2.LoadOverview(context.Background(), 1, 10, ""); err == nil {
		t.Fatal("pg overview databases fault must fail")
	}
	script3 := &w11fScript{}
	db3 := newW11FDB(t, script3)
	store3, _ := NewStore(db3, true)
	script3.canned("SELECT DISTINCT ON (database_role)", databaseCols, [][]driver.Value{{"business", "/b", "s", int64(1), int64(1), nil, int64(1), int64(1)}}, nil)
	script3.failQuery("SELECT COUNT(*) AS total")
	if _, err := store3.LoadOverview(context.Background(), 1, 10, ""); err == nil {
		t.Fatal("pg overview count fault must fail")
	}
	script4 := &w11fScript{}
	db4 := newW11FDB(t, script4)
	store4, _ := NewStore(db4, true)
	script4.canned("SELECT DISTINCT ON (database_role)", databaseCols, [][]driver.Value{{"business", "/b", "s", int64(1), int64(1), nil, int64(1), int64(1)}}, nil)
	script4.canned("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(0)}}, nil)
	script4.failQuery("WITH latest_snapshots")
	if _, err := store4.LoadOverview(context.Background(), 1, 10, ""); err == nil {
		t.Fatal("pg overview tables fault must fail")
	}
}

// ---------------------------------------------------------------------------
// handler 直连
// ---------------------------------------------------------------------------

func TestW11FHandlers(t *testing.T) {
	deps, store := newMonitorFixture(t)
	ctx := context.Background()

	// overview: 非法 page / pageSize / keyword 超长 / refresh 非法。
	code, payload := w11fInvoke(t, deps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?page=x"))
	if code != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("overview page nan = %d %v", code, payload)
	}
	code, _ = w11fInvoke(t, deps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?pageSize=x"))
	if code != http.StatusBadRequest {
		t.Fatalf("overview pageSize nan = %d", code)
	}
	code, payload = w11fInvoke(t, deps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?keyword="+strings.Repeat("a", 201)))
	if code != http.StatusBadRequest || payload["message"] != "请求参数无效" {
		t.Fatalf("overview keyword = %d %v", code, payload)
	}
	code, payload = w11fInvoke(t, deps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?refresh=zzz"))
	if code != http.StatusBadRequest {
		t.Fatalf("overview refresh = %d %v", code, payload)
	}
	// 非缓存able 查询（keyword）直读 store。
	code, payload = w11fInvoke(t, deps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?keyword=acc&refresh=1"))
	if code != http.StatusOK {
		t.Fatalf("overview keyword hit = %d %v", code, payload)
	}
	// store 错误 → 503 typed unavailable。
	faultDeps := &Deps{Store: &w11fFailingStore{}, Cache: nil}
	code, payload = w11fInvoke(t, faultDeps.overviewHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/overview?keyword=x"))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("overview typed unavailable = %d %v", code, payload)
	}

	// history: 参数无效 / 窗口无效 / limit 无效 / store 错误 / 命中。
	code, payload = w11fInvoke(t, deps.historyHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=bogus&tableName=x"))
	if code != http.StatusBadRequest || payload["message"] != "表监控历史参数无效" {
		t.Fatalf("history bad role = %d %v", code, payload)
	}
	code, _ = w11fInvoke(t, deps.historyHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=business"))
	if code != http.StatusBadRequest {
		t.Fatalf("history missing table = %d", code)
	}
	code, payload = w11fInvoke(t, deps.historyHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=accounts&startAt=zzz"))
	if code != http.StatusBadRequest || payload["message"] != zodTimeInvalid {
		t.Fatalf("history bad window = %d %v", code, payload)
	}
	code, payload = w11fInvoke(t, deps.historyHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=accounts&limit=x"))
	if code != http.StatusBadRequest {
		t.Fatalf("history bad limit = %d %v", code, payload)
	}
	code, payload = w11fInvoke(t, deps.historyHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/history?databaseRole=business&tableName=accounts&startAt=2026-09-01T00:00:00Z&endAt=2026-09-05T00:00:00Z"))
	if code != http.StatusOK || len(payload["data"].([]any)) == 0 {
		t.Fatalf("history hit = %d %v", code, payload)
	}
	// database-history: 窗口 / limit / 命中。
	code, payload = w11fInvoke(t, deps.databaseHistoryHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/database-history?startAt=zzz"))
	if code != http.StatusBadRequest {
		t.Fatalf("database history bad window = %d %v", code, payload)
	}
	code, payload = w11fInvoke(t, deps.databaseHistoryHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/database-history?limit=x"))
	if code != http.StatusBadRequest {
		t.Fatalf("database history bad limit = %d %v", code, payload)
	}
	code, payload = w11fInvoke(t, deps.databaseHistoryHandler, w11fAuthRequest(http.MethodGet, "/__aisys__/api/table-monitor/database-history?startAt=2026-09-01T00:00:00Z&endAt=2026-09-05T00:00:00Z"))
	if code != http.StatusOK || len(payload["data"].([]any)) == 0 {
		t.Fatalf("database history hit = %d %v", code, payload)
	}
	_ = store
	_ = ctx
}

// w11fFailingStore 恒定返回 schema 不可用。
type w11fFailingStore struct{}

func (s *w11fFailingStore) LoadOverview(context.Context, int, int, string) (Overview, error) {
	return Overview{}, ErrSchemaUnavailable
}

func (s *w11fFailingStore) LoadTableHistory(context.Context, string, string, string, string, int) ([]TableHistoryPoint, error) {
	return nil, ErrSchemaUnavailable
}

func (s *w11fFailingStore) LoadDatabaseHistory(context.Context, string, string, int) ([]DatabaseHistoryPoint, error) {
	return nil, ErrSchemaUnavailable
}

// TestW11FCleanupBlockedRecord 覆盖 cleanup 投递失败时的 blocked 回执与日志。
func TestW11FCleanupBlockedRecord(t *testing.T) {
	script := &w11fScript{}
	db := newW11FDB(t, script)
	dispatch, err := NewDurableRecordMaintenanceDispatch(db, false, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	script.failExec("CREATE TABLE")
	sink := &w11fRecordingSink{}
	deps := &Deps{Store: nil, Cache: nil, Dispatch: dispatch, Sink: sink}

	request := w11fAuthRequest(http.MethodPost, "/__aisys__/api/table-monitor/non-business-data/cleanup")
	request.Header.Set("Content-Type", "application/json")
	body := `{"cutoffAt":"2026-09-01T00:00:00Z"}`
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	request.Body = io.NopCloser(strings.NewReader(body))
	guarded := deps.cleanupGuard()(http.HandlerFunc(deps.cleanupHandler))
	code, payload := w11fInvoke(t, guarded, request)
	if code != http.StatusOK {
		t.Fatalf("cleanup blocked receipt = %d %v", code, payload)
	}
	data := payload["data"].(map[string]any)
	if data["queued"] != false || data["blockedReason"] == nil {
		t.Fatalf("blocked receipt = %v", data)
	}
	entries := sink.Entries()
	if len(entries) != 1 || !strings.Contains(entries[0].Summary, "未提交") {
		t.Fatalf("cleanup log = %+v", entries)
	}

	// 成功投递 + 日志。
	script2 := &w11fScript{}
	db2 := newW11FDB(t, script2)
	dispatch2, _ := NewDurableRecordMaintenanceDispatch(db2, false, time.Now)
	sink2 := &w11fRecordingSink{}
	deps2 := &Deps{Dispatch: dispatch2, Sink: sink2}
	body2 := `{"cutoffAt":"2026-08-01T00:00:00Z"}`
	request2 := w11fAuthRequest(http.MethodPost, "/__aisys__/api/table-monitor/non-business-data/cleanup")
	request2.Header.Set("Content-Type", "application/json")
	request2.Header.Set("Content-Length", strconv.Itoa(len(body2)))
	request2.Body = io.NopCloser(strings.NewReader(body2))
	guarded2 := deps2.cleanupGuard()(http.HandlerFunc(deps2.cleanupHandler))
	code, payload = w11fInvoke(t, guarded2, request2)
	if code != http.StatusOK {
		t.Fatalf("cleanup queued receipt = %d %v", code, payload)
	}
	if data = payload["data"].(map[string]any); data["queued"] != true || data["blockedReason"] != nil {
		t.Fatalf("queued receipt = %v", data)
	}
	if len(sink2.Entries()) != 1 || !strings.Contains(sink2.Entries()[0].Summary, "提交非业务数据硬清理任务") {
		t.Fatalf("queued log = %+v", sink2.Entries())
	}
}

// w11fRecordingSink 记录操作日志。
type w11fRecordingSink struct {
	entries []authsys.OperationLogEntry
}

func (s *w11fRecordingSink) Record(entry authsys.OperationLogEntry, _ *http.Request) {
	s.entries = append(s.entries, entry)
}

func (s *w11fRecordingSink) Entries() []authsys.OperationLogEntry {
	return s.entries
}

var _ = kernel.WriteOK

// w11fDBSeq 保证同测试内多个 DSN 唯一。
var w11fDBSeq atomic.Int64

// w11fServe 兼容 http.HandlerFunc 与 http.Handler 两种形态。
func w11fServe(t *testing.T, handler any, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	switch typed := handler.(type) {
	case http.HandlerFunc:
		typed(w, r)
	case func(http.ResponseWriter, *http.Request):
		typed(w, r)
	case http.Handler:
		typed.ServeHTTP(w, r)
	default:
		t.Fatalf("unsupported handler type %T", handler)
	}
}
