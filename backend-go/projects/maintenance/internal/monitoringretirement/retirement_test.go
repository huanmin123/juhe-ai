package monitoringretirement

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件测试：PG fake 录制驱动按现有 wm_pg_fake 模式注入 to_regclass 探测
// 结果与执行失败，断言 12 张表的 DROP 语句、成功清单与 continue-on-error
// 语义（不连接任何真实 PostgreSQL）。SQLite 全链路臂随 SQLite 存储形态退役
// 删除（PLAN-20261024T051925945Z B4）。

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
