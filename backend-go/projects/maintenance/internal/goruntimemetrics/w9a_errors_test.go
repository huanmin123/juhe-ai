package goruntimemetrics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

var errW9AGoMetricsFailure = errors.New("w9a gometrics 注入失败")

// w9aGoRoute 按子串匹配一条查询并返回脚本化行或错误。
type w9aGoRoute struct {
	match    string
	rows     [][]driver.Value
	err      error
	nextErr  error
	closeErr error
}

type w9aGoQueryFunc struct {
	match string
	fn    func(args []driver.NamedValue) ([][]driver.Value, error)
}

type w9aGoFake struct {
	mu            sync.Mutex
	routes        []w9aGoRoute
	queryFuncs    []w9aGoQueryFunc
	execs         []string
	execFailAfter int // 前 N 次 Exec 成功，之后失败；-1 不注入
	beginErr      error
	commitErr     error
	onExec        func(f *w9aGoFake, query string)
}

func (f *w9aGoFake) addRoute(route w9aGoRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, route)
}

func (f *w9aGoFake) replace(match string, route w9aGoRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.routes[:0]
	for _, existing := range f.routes {
		if existing.match != match {
			kept = append(kept, existing)
		}
	}
	f.routes = append(kept, route)
	funcs := f.queryFuncs[:0]
	for _, existing := range f.queryFuncs {
		if existing.match != match {
			funcs = append(funcs, existing)
		}
	}
	f.queryFuncs = funcs
}

func (f *w9aGoFake) route(query string) (w9aGoRoute, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, route := range f.routes {
		if strings.Contains(query, route.match) {
			f.routes = append(f.routes[:i], f.routes[i+1:]...)
			return route, true
		}
	}
	return w9aGoRoute{}, false
}

func (f *w9aGoFake) exec(query string) error {
	f.mu.Lock()
	var hook func(f *w9aGoFake, query string)
	if f.execFailAfter >= 0 && len(f.execs)+1 > f.execFailAfter {
		f.mu.Unlock()
		return errW9AGoMetricsFailure
	}
	f.execs = append(f.execs, query)
	if f.onExec != nil {
		hook = f.onExec
	}
	f.mu.Unlock()
	if hook != nil {
		hook(f, query)
	}
	return nil
}

func (f *w9aGoFake) execCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execs)
}

type w9aGoConnector struct{ fake *w9aGoFake }

func (c w9aGoConnector) Connect(context.Context) (driver.Conn, error) {
	return &w9aGoConn{fake: c.fake}, nil
}
func (c w9aGoConnector) Driver() driver.Driver { return w9aGoDriver{} }

type w9aGoDriver struct{}

func (w9aGoDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w9a gometrics fake: 使用 sql.OpenDB")
}

type w9aGoConn struct{ fake *w9aGoFake }

func (c *w9aGoConn) Prepare(query string) (driver.Stmt, error) {
	return &w9aGoStmt{fake: c.fake, query: query}, nil
}
func (c *w9aGoConn) Close() error { return nil }
func (c *w9aGoConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *w9aGoConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.fake.beginErr != nil {
		return nil, c.fake.beginErr
	}
	return &w9aGoTx{fake: c.fake, readOnly: opts.ReadOnly}, nil
}

type w9aGoTx struct {
	fake     *w9aGoFake
	readOnly bool
}

func (t *w9aGoTx) Commit() error   { return t.fake.commitErr }
func (t *w9aGoTx) Rollback() error { return nil }

type w9aGoStmt struct {
	fake  *w9aGoFake
	query string
}

func (s *w9aGoStmt) Close() error  { return nil }
func (s *w9aGoStmt) NumInput() int { return -1 }

func (s *w9aGoStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("w9a gometrics fake: legacy Exec 未使用")
}

func (s *w9aGoStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("w9a gometrics fake: legacy Query 未使用")
}

func (s *w9aGoStmt) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, time.Time:
		_ = typed
		return nil
	case []string:
		value.Value = strings.Join(typed, ",")
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("w9a gometrics fake: 不支持的参数类型 %T", value.Value)
	}
}

func (s *w9aGoStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.fake.exec(s.query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (s *w9aGoStmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	for _, qf := range s.fake.queryFuncs {
		if strings.Contains(s.query, qf.match) {
			rows, err := qf.fn(args)
			if err != nil {
				return nil, err
			}
			columns := []string{"c0"}
			if len(rows) > 0 {
				columns = make([]string, len(rows[0]))
				for i := range rows[0] {
					columns[i] = fmt.Sprintf("c%d", i)
				}
			}
			return &w9aGoRows{columns: columns, rows: rows}, nil
		}
	}
	route, ok := s.fake.route(s.query)
	if !ok {
		if strings.Contains(s.query, "current_database()") {
			return &w9aGoRows{columns: []string{"c0", "c1"}, rows: [][]driver.Value{{"juhe_stats", "metrics_role"}}}, nil
		}
		return &w9aGoRows{columns: []string{"c0"}}, nil
	}
	if route.err != nil {
		return nil, route.err
	}
	columns := []string{"c0"}
	if len(route.rows) > 0 {
		columns = make([]string, len(route.rows[0]))
		for i := range route.rows[0] {
			columns[i] = fmt.Sprintf("c%d", i)
		}
	}
	return &w9aGoRows{columns: columns, rows: route.rows, nextErr: route.nextErr, closeErr: route.closeErr}, nil
}

type w9aGoRows struct {
	columns  []string
	rows     [][]driver.Value
	pos      int
	nextErr  error
	closeErr error
}

func (r *w9aGoRows) Columns() []string { return r.columns }
func (r *w9aGoRows) Close() error      { return r.closeErr }
func (r *w9aGoRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func openW9AGoMetricsDB(fake *w9aGoFake) *sql.DB {
	return sql.OpenDB(w9aGoConnector{fake: fake})
}

// w9aGoReadyFake 返回完整就绪目录脚本。
func w9aGoReadyFake() *w9aGoFake {
	fake := &w9aGoFake{execFailAfter: -1}
	fake.addRoute(w9aGoRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_stats", "metrics_role"}}})
	tableRows := make([][]driver.Value, 0, len(requiredTables))
	for _, name := range requiredTables {
		tableRows = append(tableRows, []driver.Value{name})
	}
	fake.addRoute(w9aGoRoute{match: "information_schema.tables", rows: tableRows})
	columnRows := make([][]driver.Value, 0, 96)
	for table, columns := range requiredColumns {
		for column, spec := range columns {
			columnRows = append(columnRows, []driver.Value{table, column, spec.dataType, spec.udtName, "NO"})
		}
	}
	nullableRows := [][]driver.Value{
		{"go_runtime_metrics_samples", "cpu_percent"},
		{"go_runtime_metrics_samples", "rss_bytes"},
		{"go_runtime_metrics_samples", "fd_count"},
	}
	// nullability 查询（2 列）必须先于 columns 查询（5 列）注册：两个查询共享
	// "information_schema.columns" 子串，FIFO 先命中先消费。
	fake.addRoute(w9aGoRoute{match: "is_nullable <> 'NO'", rows: nullableRows})
	fake.addRoute(w9aGoRoute{match: "udt_name", rows: columnRows})
	// primary key 查询按 $2 表名参数返回各自的主键列序。
	fake.queryFuncs = append(fake.queryFuncs, w9aGoQueryFunc{
		match: "tc.constraint_type='PRIMARY KEY'",
		fn: func(args []driver.NamedValue) ([][]driver.Value, error) {
			table := fmt.Sprintf("%v", args[1].Value)
			expected, ok := requiredPrimaryKeys[table]
			if !ok {
				return nil, nil
			}
			rows := make([][]driver.Value, 0, len(expected))
			for _, column := range expected {
				rows = append(rows, []driver.Value{column})
			}
			return rows, nil
		},
	})
	return fake
}

// TestW9AGoMetricsRunErrorBranches 覆盖 Run 与 inspect 的错误传播路径。
func TestW9AGoMetricsRunErrorBranches(t *testing.T) {
	ctx := context.Background()

	if _, err := Run(ctx, nil, false); err == nil {
		t.Fatal("nil db 必须报错")
	}

	idFake := &w9aGoFake{execFailAfter: -1}
	idFake.addRoute(w9aGoRoute{match: "current_database()", err: errW9AGoMetricsFailure})
	db := openW9AGoMetricsDB(idFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "身份失败") {
		t.Fatalf("身份查询失败必须被包装: %v", err)
	}
	db.Close()

	for _, tc := range []struct {
		name   string
		route  w9aGoRoute
		needle string
	}{
		{"tables 查询失败", w9aGoRoute{match: "information_schema.tables", err: errW9AGoMetricsFailure}, "table 契约失败"},
		{"tables Scan 失败", w9aGoRoute{match: "information_schema.tables", rows: [][]driver.Value{{nil}}}, "table 名称失败"},
		{"tables 遍历失败", w9aGoRoute{match: "information_schema.tables", nextErr: errW9AGoMetricsFailure}, "遍历 Go runtime metrics table"},
		{"columns 查询失败", w9aGoRoute{match: "udt_name", err: errW9AGoMetricsFailure}, "column 契约失败"},
		{"columns Scan 失败", w9aGoRoute{match: "udt_name", rows: [][]driver.Value{{nil, nil, nil, nil, nil}}}, "column 定义失败"},
		{"columns 遍历失败", w9aGoRoute{match: "udt_name", nextErr: errW9AGoMetricsFailure}, "遍历 Go runtime metrics column"},
		{"nullability 查询失败", w9aGoRoute{match: "is_nullable <> 'NO'", err: errW9AGoMetricsFailure}, "nullability 契约失败"},
		{"nullability Scan 失败", w9aGoRoute{match: "is_nullable <> 'NO'", rows: [][]driver.Value{{nil, nil}}}, "nullability 定义失败"},
		{"nullability 遍历失败", w9aGoRoute{match: "is_nullable <> 'NO'", nextErr: errW9AGoMetricsFailure}, "遍历 Go runtime metrics nullability"},
		{"primary key 查询失败", w9aGoRoute{match: "tc.constraint_type='PRIMARY KEY'", err: errW9AGoMetricsFailure}, "primary key 契约失败"},
		{"primary key Scan 失败", w9aGoRoute{match: "tc.constraint_type='PRIMARY KEY'", rows: [][]driver.Value{{nil}}}, "primary key 定义失败"},
		{"primary key 遍历失败", w9aGoRoute{match: "tc.constraint_type='PRIMARY KEY'", nextErr: errW9AGoMetricsFailure}, "遍历 Go runtime metrics primary key"},
	} {
		fake := w9aGoReadyFake()
		fake.replace(tc.route.match, tc.route)
		conn := openW9AGoMetricsDB(fake)
		_, err := Run(ctx, conn, false)
		conn.Close()
		if err == nil || !strings.Contains(err.Error(), tc.needle) {
			t.Fatalf("%s 必须被包装: %v", tc.name, err)
		}
	}
}

// TestW9AGoMetricsRunApplyPaths 覆盖 apply 的 DDL 失败、不完整与就绪路径。
func TestW9AGoMetricsRunApplyPaths(t *testing.T) {
	ctx := context.Background()

	// EnsureSchema 首条 DDL 失败。
	ddlFake := &w9aGoFake{execFailAfter: 0}
	db := openW9AGoMetricsDB(ddlFake)
	if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "schema bootstrap 失败") {
		t.Fatalf("EnsureSchema 失败必须被包装: %v", err)
	}
	db.Close()

	// DDL 全部成功但契约不完整（无 catalog 脚本）。
	incompleteFake := &w9aGoFake{execFailAfter: -1}
	db = openW9AGoMetricsDB(incompleteFake)
	report, err := Run(ctx, db, true)
	if err == nil || !strings.Contains(err.Error(), "契约仍不完整") {
		t.Fatalf("DDL 后契约不完整必须报错: %v", err)
	}
	db.Close()

	// 完整目录 → apply 成功且 Applied=true。
	readyFake := w9aGoReadyFake()
	readyFake.onExec = func(f *w9aGoFake, query string) {
		if strings.Contains(query, "CREATE TABLE IF NOT EXISTS") && f.execCount() >= 4 {
			tableRows := make([][]driver.Value, 0, len(requiredTables))
			for _, name := range requiredTables {
				tableRows = append(tableRows, []driver.Value{name})
			}
			f.addRoute(w9aGoRoute{match: "information_schema.tables", rows: tableRows})
		}
	}
	db = openW9AGoMetricsDB(readyFake)
	report, err = Run(ctx, db, true)
	if err != nil {
		t.Fatalf("就绪目录 apply 失败: %v", err)
	}
	if !report.Ready() || !report.Applied {
		t.Fatalf("apply 报告必须就绪: %+v", report)
	}
	db.Close()
}

// TestW9AGoMetricsHelpers 覆盖 optionalColumn 与 sameStrings 的分支。
func TestW9AGoMetricsHelpers(t *testing.T) {
	if !optionalColumn("go_runtime_metrics_samples", "cpu_percent") ||
		!optionalColumn("go_runtime_metrics_samples", "rss_bytes") ||
		!optionalColumn("go_runtime_metrics_samples", "fd_count") {
		t.Fatal("samples 的三个可选列必须被识别")
	}
	if optionalColumn("go_runtime_metrics_samples", "goroutines") {
		t.Fatal("非可选列不得放行")
	}
	if !optionalColumn("go_runtime_metrics_hourly", "avg_cpu_percent") ||
		!optionalColumn("go_runtime_metrics_trend_windows", "max_fd_count") {
		t.Fatal("聚合表可选列必须被识别")
	}
	if optionalColumn("go_runtime_metrics_hourly", "sample_count") {
		t.Fatal("聚合表非可选列不得放行")
	}

	if !sameStrings([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("相同切片必须相等")
	}
	if sameStrings([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("长度不同不得相等")
	}
	if sameStrings([]string{"a"}, []string{"b"}) {
		t.Fatal("内容不同不得相等")
	}
}
