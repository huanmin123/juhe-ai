package monitoringretirement

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 本文件提供两组测试：
//   - SQLite 全链路：三个临时库文件真实建表 + 造行，RunSQLite 断言逐表删除
//     与幂等重跑（第二次只报告已清退、零写入、绝不创建缺失文件）；
//   - PG fake 录制驱动：按现有 wm_pg_fake 模式注入 to_regclass 探测结果与
//     执行失败，断言 12 张表的 DROP 语句、成功清单与 continue-on-error 语义。
//     不连接任何真实 PostgreSQL。

var sqliteTestDDLs = map[string][]string{
	StoreStats: {
		`CREATE TABLE system_metrics_samples (id TEXT PRIMARY KEY, sampled_at TEXT NOT NULL)`,
		`CREATE TABLE system_metrics_hourly (stat_hour TEXT PRIMARY KEY)`,
		`CREATE TABLE system_metrics_trend_windows (window_key TEXT NOT NULL, bucket_key TEXT NOT NULL, PRIMARY KEY (window_key, bucket_key))`,
		`CREATE TABLE process_event_loop_samples (id TEXT PRIMARY KEY, sampled_at TEXT NOT NULL)`,
		`CREATE TABLE process_event_loop_hourly (stat_hour TEXT NOT NULL, process_role TEXT NOT NULL, PRIMARY KEY (stat_hour, process_role))`,
		`CREATE TABLE process_event_loop_trend_windows (window_key TEXT NOT NULL, bucket_key TEXT NOT NULL, process_role TEXT NOT NULL, PRIMARY KEY (window_key, bucket_key, process_role))`,
	},
	StoreGoRuntimeMetrics: {
		`CREATE TABLE go_runtime_metrics_samples (id TEXT PRIMARY KEY)`,
		`CREATE TABLE go_runtime_metrics_hourly (id TEXT PRIMARY KEY)`,
		`CREATE TABLE go_runtime_metrics_trend_windows (id TEXT PRIMARY KEY)`,
	},
	StoreTableMonitor: {
		`CREATE TABLE database_storage_snapshots (id TEXT PRIMARY KEY)`,
		`CREATE TABLE table_storage_snapshots (id TEXT PRIMARY KEY)`,
		`CREATE TABLE table_monitor_owner_leases (lease_key TEXT PRIMARY KEY)`,
	},
}

// seededSQLiteTargets 在临时目录建三个库文件：逐表建 DDL 并各插一行，返回
// 对应的 SQLiteTargets。
func seededSQLiteTargets(t *testing.T) (SQLiteTargets, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{
		StoreStats:            filepath.Join(dir, "stats.sqlite3"),
		StoreGoRuntimeMetrics: filepath.Join(dir, "go-runtime-metrics.sqlite3"),
		StoreTableMonitor:     filepath.Join(dir, "table-monitor.sqlite3"),
	}
	for store, path := range paths {
		db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
		if err != nil {
			t.Fatalf("open %s: %v", store, err)
		}
		db.SetMaxOpenConns(1)
		for _, ddl := range sqliteTestDDLs[store] {
			if _, err := db.Exec(ddl); err != nil {
				t.Fatalf("create table in %s: %v", store, err)
			}
			table := strings.TrimPrefix(strings.TrimPrefix(ddl, "CREATE TABLE "), "")
			table = table[:strings.Index(table, " ")]
			if _, err := db.Exec("INSERT INTO " + table + " (" + sqliteTestRowColumn(table) + ") VALUES (" + sqliteTestRowValue(table) + ")"); err != nil {
				t.Fatalf("seed row in %s.%s: %v", store, table, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %s: %v", store, err)
		}
	}
	targets := SQLiteTargets{
		Stats:            paths[StoreStats],
		GoRuntimeMetrics: paths[StoreGoRuntimeMetrics],
		TableMonitor:     paths[StoreTableMonitor],
	}
	return targets, paths
}

// sqliteTestRowColumn / sqliteTestRowValue 给每张测试表一行最小合法数据。
func sqliteTestRowColumn(table string) string {
	switch table {
	case "system_metrics_trend_windows":
		return "window_key, bucket_key"
	case "process_event_loop_hourly":
		return "stat_hour, process_role"
	case "process_event_loop_trend_windows":
		return "window_key, bucket_key, process_role"
	case "table_monitor_owner_leases":
		return "lease_key"
	case "system_metrics_hourly":
		return "stat_hour"
	case "system_metrics_samples", "process_event_loop_samples":
		return "id, sampled_at"
	default:
		return "id"
	}
}

func sqliteTestRowValue(table string) string {
	switch table {
	case "system_metrics_trend_windows":
		return "'w1', 'b1'"
	case "process_event_loop_hourly":
		return "'h1', 'gateway'"
	case "process_event_loop_trend_windows":
		return "'w1', 'b1', 'gateway'"
	case "table_monitor_owner_leases":
		return "'lease-1'"
	case "system_metrics_hourly":
		return "'h1'"
	case "system_metrics_samples", "process_event_loop_samples":
		return "'row-1', '2026-10-14T00:00:00.000Z'"
	default:
		return "'row-1'"
	}
}

// sqliteTableNames 返回库内全部用户表名集合。
func sqliteTableNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("query tables in %s: %v", path, err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables in %s: %v", path, err)
	}
	return names
}

func TestRunSQLiteDropsAllTwelveTablesAndIsIdempotent(t *testing.T) {
	targets, paths := seededSQLiteTargets(t)
	now := time.Now().UTC()

	report, err := RunSQLite(context.Background(), targets, now)
	if err != nil {
		t.Fatalf("first RunSQLite: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("first run must be ready, failed=%v", report.FailedTables)
	}
	if report.DroppedCount != 12 {
		t.Fatalf("DroppedCount=%d want 12", report.DroppedCount)
	}
	if len(report.Targets) != 3 {
		t.Fatalf("targets=%d want 3", len(report.Targets))
	}
	if report.AlreadyRetired {
		t.Fatalf("first run must not be AlreadyRetired")
	}
	// 逐库断言 12 张表全部消失。
	for store, path := range paths {
		tables := sqliteTableNames(t, path)
		if len(tables) != 0 {
			t.Fatalf("%s still has tables: %v", store, tables)
		}
	}
	// 幂等重跑：全部记为已缺席，零失败。
	second, err := RunSQLite(context.Background(), targets, now)
	if err != nil {
		t.Fatalf("second RunSQLite: %v", err)
	}
	if !second.Ready() || !second.AlreadyRetired {
		t.Fatalf("second run ready=%v alreadyRetired=%v failed=%v", second.Ready(), second.AlreadyRetired, second.FailedTables)
	}
	if second.DroppedCount != 0 || second.AlreadyAbsent != 12 {
		t.Fatalf("second run dropped=%d absent=%d want 0/12", second.DroppedCount, second.AlreadyAbsent)
	}
}

func TestRunSQLiteMissingFilesAreBenignAndNeverCreated(t *testing.T) {
	dir := t.TempDir()
	targets := SQLiteTargets{
		Stats:            filepath.Join(dir, "stats.sqlite3"),
		GoRuntimeMetrics: filepath.Join(dir, "go-runtime-metrics.sqlite3"),
		TableMonitor:     filepath.Join(dir, "table-monitor.sqlite3"),
	}
	report, err := RunSQLite(context.Background(), targets, time.Now().UTC())
	if err != nil {
		t.Fatalf("RunSQLite on empty dir: %v", err)
	}
	if !report.Ready() || !report.AlreadyRetired {
		t.Fatalf("missing files must be ready and already retired: %+v", report)
	}
	if report.AlreadyAbsent != 12 {
		t.Fatalf("AlreadyAbsent=%d want 12", report.AlreadyAbsent)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("list dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("RunSQLite must not create files, got %d entries", len(entries))
	}
	for _, target := range report.Targets {
		if target.FileExisted {
			t.Fatalf("target %s must report fileExisted=false", target.Store)
		}
	}
}

// —— PG fake 录制驱动（按 schema 包 wm_pg_fake 模式）。

type fakePGRecorder struct {
	mu sync.Mutex
	// regclassResults 是 to_regclass 探测的 FIFO 脚本结果：值按调用顺序消费。
	regclassResults []any
	// dropFailures 按 FIFO 作用于 DROP TABLE 执行：非 nil 即返回错误。
	dropFailures []error
	execs        []string
	// connectFail 非 nil 时 Connect 直接失败（模拟目标库不可达）。
	connectFail error
}

func (r *fakePGRecorder) nextRegclass() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.regclassResults) == 0 {
		return nil
	}
	value := r.regclassResults[0]
	r.regclassResults = r.regclassResults[1:]
	return value
}

func (r *fakePGRecorder) nextDropFailure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.dropFailures) == 0 {
		return nil
	}
	err := r.dropFailures[0]
	r.dropFailures = r.dropFailures[1:]
	return err
}

func (r *fakePGRecorder) recordExec(query string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.execs = append(r.execs, query)
}

func (r *fakePGRecorder) execQueries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.execs...)
}

type fakePGConn struct{ rec *fakePGRecorder }

func (c *fakePGConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake pg: Prepare 不应被调用")
}
func (c *fakePGConn) Close() error { return nil }
func (c *fakePGConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake pg: Begin 不应被调用")
}

func (c *fakePGConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(strings.ToUpper(query), "DROP TABLE") {
		if err := c.rec.nextDropFailure(); err != nil {
			return nil, err
		}
	}
	c.rec.recordExec(query)
	return driver.RowsAffected(1), nil
}

func (c *fakePGConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "to_regclass") {
		return &fakePGRows{columns: []string{"c0"}, rows: [][]driver.Value{{c.rec.nextRegclass()}}}, nil
	}
	return &fakePGRows{columns: []string{"c0"}}, nil
}

type fakePGConnector struct{ rec *fakePGRecorder }

func (c fakePGConnector) Connect(context.Context) (driver.Conn, error) {
	if c.rec.connectFail != nil {
		return nil, c.rec.connectFail
	}
	return &fakePGConn{rec: c.rec}, nil
}
func (c fakePGConnector) Driver() driver.Driver { return fakePGDriver{} }

type fakePGDriver struct{}

func (fakePGDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake pg: 请使用 sql.OpenDB")
}

type fakePGRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *fakePGRows) Columns() []string { return r.columns }
func (r *fakePGRows) Close() error      { return nil }
func (r *fakePGRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func openFakePG(rec *fakePGRecorder) *sql.DB {
	return sql.OpenDB(fakePGConnector{rec: rec})
}

// allRegclassValues 构造 12 次探测的脚本结果：present=false 时全部返回 NULL。
func allRegclassValues(present bool) []any {
	values := make([]any, 0, 12)
	for range RetiredTables() {
		if present {
			values = append(values, SchemaName+".t")
		} else {
			values = append(values, nil)
		}
	}
	return values
}

func TestRunPostgresDropsAllTwelveTablesInStatsSchema(t *testing.T) {
	rec := &fakePGRecorder{regclassResults: allRegclassValues(true)}
	db := openFakePG(rec)
	defer db.Close()

	report, err := RunPostgres(context.Background(), db, time.Now().UTC())
	if err != nil {
		t.Fatalf("RunPostgres: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("report must be ready, failed=%v", report.FailedTables)
	}
	if report.DroppedCount != 12 || report.AlreadyAbsent != 0 {
		t.Fatalf("dropped=%d absent=%d want 12/0", report.DroppedCount, report.AlreadyAbsent)
	}
	queries := rec.execQueries()
	if len(queries) != 12 {
		t.Fatalf("exec count=%d want 12（探测走查询不录制，DROP 每表一条）", len(queries))
	}
	for _, table := range RetiredTables() {
		drop := "DROP TABLE IF EXISTS " + SchemaName + "." + table
		found := false
		for _, query := range queries {
			if query == drop {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing drop statement %q in %v", drop, queries)
		}
	}
	if got := len(report.Targets); got != 1 {
		t.Fatalf("targets=%d want 1 (single PG database)", got)
	}
	if report.Targets[0].Store != SchemaName {
		t.Fatalf("target store=%q want %q", report.Targets[0].Store, SchemaName)
	}
}

func TestRunPostgresRerunIsAlreadyRetired(t *testing.T) {
	rec := &fakePGRecorder{regclassResults: allRegclassValues(false)}
	db := openFakePG(rec)
	defer db.Close()

	report, err := RunPostgres(context.Background(), db, time.Now().UTC())
	if err != nil {
		t.Fatalf("RunPostgres rerun: %v", err)
	}
	if !report.Ready() || !report.AlreadyRetired {
		t.Fatalf("rerun ready=%v alreadyRetired=%v", report.Ready(), report.AlreadyRetired)
	}
	if report.DroppedCount != 0 || report.AlreadyAbsent != 12 {
		t.Fatalf("rerun dropped=%d absent=%d want 0/12", report.DroppedCount, report.AlreadyAbsent)
	}
}

func TestRunPostgresContinuesAfterPerTableFailureAndListsSucceeded(t *testing.T) {
	// 第 2 张表（system_metrics_hourly，statsTables 顺序第二）的 DROP 注入失败：
	// 其余 11 张仍必须执行，报告列出失败项与成功清单。
	rec := &fakePGRecorder{
		regclassResults: allRegclassValues(true),
		dropFailures:    []error{nil, errors.New("injected drop failure")},
	}
	db := openFakePG(rec)
	defer db.Close()

	report, err := RunPostgres(context.Background(), db, time.Now().UTC())
	if err != nil {
		t.Fatalf("RunPostgres must not abort on per-table failure: %v", err)
	}
	if report.Ready() {
		t.Fatalf("report must not be ready with an injected failure")
	}
	if len(report.FailedTables) != 1 || report.FailedTables[0] != SchemaName+".system_metrics_hourly" {
		t.Fatalf("failed tables=%v want [%s.system_metrics_hourly]", report.FailedTables, SchemaName)
	}
	if report.DroppedCount != 11 {
		t.Fatalf("dropped=%d want 11（其余表继续清退）", report.DroppedCount)
	}
	queries := rec.execQueries()
	drops := 0
	for _, query := range queries {
		if strings.Contains(strings.ToUpper(query), "DROP TABLE") {
			drops++
		}
	}
	if drops != 11 {
		t.Fatalf("recorded drop executions=%d want 11（注入失败的那次执行不录制，其余 11 张表全部继续）", drops)
	}
}

func TestRunPostgresUnreachableTargetFailsClosed(t *testing.T) {
	rec := &fakePGRecorder{connectFail: errors.New("connection refused")}
	db := openFakePG(rec)
	defer db.Close()

	_, err := RunPostgres(context.Background(), db, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "不可达") {
		t.Fatalf("unreachable target must fail with 明确报错, got %v", err)
	}
}

func TestRetiredTablesCoversExactlyTwelveUniqueNames(t *testing.T) {
	tables := RetiredTables()
	if len(tables) != 12 {
		t.Fatalf("RetiredTables=%d want 12", len(tables))
	}
	seen := map[string]bool{}
	for _, table := range tables {
		if seen[table] {
			t.Fatalf("duplicate table %q", table)
		}
		seen[table] = true
	}
	for _, table := range append(append([]string{}, statsTables...), append(goRuntimeMetricsTables, tableMonitorTables...)...) {
		if !seen[table] {
			t.Fatalf("table %q missing from RetiredTables", table)
		}
	}
}
