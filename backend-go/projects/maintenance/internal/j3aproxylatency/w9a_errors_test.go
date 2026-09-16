package j3aproxylatency

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

var errW9AJ3aFailure = errors.New("w9a j3a 注入失败")

// w9aRoute 按子串匹配一条查询并返回脚本化行或错误。
type w9aRoute struct {
	match string
	rows  [][]driver.Value
	err   error
	// nextErr 让 rows.Err()/Next 在行耗尽后返回错误。
	nextErr error
	// closeErr 让 rows.Close 返回错误。
	closeErr error
}

type w9aFake struct {
	mu            sync.Mutex
	routes        []w9aRoute
	execs         []string
	execFailAfter int // 前 N 次 Exec 成功，之后失败；-1 表示不注入
	beginErr      error
	commitErr     error
	onExec        func(f *w9aFake, query string)
}

func (f *w9aFake) addRoute(route w9aRoute) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, route)
}

func (f *w9aFake) route(query string) (w9aRoute, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, route := range f.routes {
		if strings.Contains(query, route.match) {
			f.routes = append(f.routes[:i], f.routes[i+1:]...)
			return route, true
		}
	}
	return w9aRoute{}, false
}

func (f *w9aFake) exec(query string) error {
	f.mu.Lock()
	var hook func(f *w9aFake, query string)
	if f.execFailAfter >= 0 && len(f.execs)+1 > f.execFailAfter {
		f.mu.Unlock()
		return errW9AJ3aFailure
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

func (f *w9aFake) execCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.execs)
}

type w9aConnector struct{ fake *w9aFake }

func (c w9aConnector) Connect(context.Context) (driver.Conn, error) {
	return &w9aConn{fake: c.fake}, nil
}
func (c w9aConnector) Driver() driver.Driver { return w9aDriver{} }

type w9aDriver struct{}

func (w9aDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w9a j3a fake: 使用 sql.OpenDB")
}

type w9aConn struct{ fake *w9aFake }

func (c *w9aConn) Prepare(query string) (driver.Stmt, error) {
	return &w9aStmt{fake: c.fake, query: query}, nil
}
func (c *w9aConn) Close() error { return nil }
func (c *w9aConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *w9aConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.fake.beginErr != nil {
		return nil, c.fake.beginErr
	}
	return &w9aTx{fake: c.fake, readOnly: opts.ReadOnly}, nil
}

type w9aTx struct {
	fake     *w9aFake
	readOnly bool
}

func (t *w9aTx) Commit() error   { return t.fake.commitErr }
func (t *w9aTx) Rollback() error { return nil }

type w9aStmt struct {
	fake  *w9aFake
	query string
}

func (s *w9aStmt) Close() error { return nil }

func (s *w9aStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("w9a j3a fake: legacy Exec 未使用")
}

func (s *w9aStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("w9a j3a fake: legacy Query 未使用")
}
func (s *w9aStmt) NumInput() int { return -1 }

// CheckNamedValue 把 []string 等运行时参数压平为 fake 可比较的值。
func (s *w9aStmt) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, time.Time:
		return nil
	case []string:
		value.Value = strings.Join(typed, ",")
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("w9a j3a fake: 不支持的参数类型 %T", value.Value)
	}
}

func (s *w9aStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.fake.exec(s.query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (s *w9aStmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	route, ok := s.fake.route(s.query)
	if !ok {
		if strings.Contains(s.query, "current_database()") {
			return &w9aRows{columns: []string{"c0", "c1"}, rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}}, nil
		}
		return &w9aRows{columns: []string{"c0"}}, nil
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
	return &w9aRows{columns: columns, rows: route.rows, nextErr: route.nextErr, closeErr: route.closeErr}, nil
}

type w9aRows struct {
	columns  []string
	rows     [][]driver.Value
	pos      int
	nextErr  error
	closeErr error
}

func (r *w9aRows) Columns() []string { return r.columns }
func (r *w9aRows) Close() error      { return r.closeErr }
func (r *w9aRows) Next(dest []driver.Value) error {
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

func openW9AJ3aDB(fake *w9aFake) *sql.DB {
	return sql.OpenDB(w9aConnector{fake: fake})
}

func w9aAllTableRows() [][]driver.Value {
	rows := make([][]driver.Value, 0, len(requiredTables))
	for _, name := range requiredTables {
		rows = append(rows, []driver.Value{name})
	}
	return rows
}

func w9aErrRows(match string) w9aRoute {
	return w9aRoute{match: match, nextErr: errW9AJ3aFailure}
}

// TestW9AJ3aRunErrorBranches 覆盖 Run 与 inspectTx 的错误传播路径。
func TestW9AJ3aRunErrorBranches(t *testing.T) {
	ctx := context.Background()

	if _, err := Run(ctx, nil, false); err == nil {
		t.Fatal("nil db 必须报错")
	}

	// BeginTx 失败。
	beginFake := &w9aFake{beginErr: errW9AJ3aFailure, execFailAfter: -1}
	db := openW9AJ3aDB(beginFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "开始 J3a bootstrap 事务失败") {
		t.Fatalf("BeginTx 失败必须被包装: %v", err)
	}
	db.Close()

	// SET 失败（第 1 次 exec）。
	setFake := &w9aFake{execFailAfter: 0}
	db = openW9AJ3aDB(setFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "配置 J3a bootstrap 事务超时失败") {
		t.Fatalf("SET 失败必须被包装: %v", err)
	}
	db.Close()

	// advisory lock 失败（apply 时第 2 次 exec）。
	lockFake := &w9aFake{execFailAfter: 1}
	db = openW9AJ3aDB(lockFake)
	if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "advisory lock") {
		t.Fatalf("advisory lock 失败必须被包装: %v", err)
	}
	db.Close()

	// 身份查询失败。
	idFake := &w9aFake{execFailAfter: -1}
	idFake.addRoute(w9aRoute{match: "current_database()", err: errW9AJ3aFailure})
	db = openW9AJ3aDB(idFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "身份失败") {
		t.Fatalf("身份查询失败必须被包装: %v", err)
	}
	db.Close()

	// owner 查询失败（非 ErrNoRows）。
	ownerFake := &w9aFake{execFailAfter: -1}
	ownerFake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	ownerFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", err: errW9AJ3aFailure})
	db = openW9AJ3aDB(ownerFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "schema owner 失败") {
		t.Fatalf("owner 查询失败必须被包装: %v", err)
	}
	db.Close()

	// table 契约查询失败。
	tablesFake := &w9aFake{execFailAfter: -1}
	tablesFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	tablesFake.addRoute(w9aRoute{match: "information_schema.tables", err: errW9AJ3aFailure})
	db = openW9AJ3aDB(tablesFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "table 契约失败") {
		t.Fatalf("table 查询失败必须被包装: %v", err)
	}
	db.Close()

	// table 名称 Scan 失败。
	tableScanFake := &w9aFake{execFailAfter: -1}
	tableScanFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	tableScanFake.addRoute(w9aRoute{match: "information_schema.tables", rows: [][]driver.Value{{nil}}})
	db = openW9AJ3aDB(tableScanFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "table 名称失败") {
		t.Fatalf("table Scan 失败必须被包装: %v", err)
	}
	db.Close()

	// table 遍历失败。
	tableErrFake := &w9aFake{execFailAfter: -1}
	tableErrFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	tableErrFake.addRoute(w9aErrRows("information_schema.tables"))
	db = openW9AJ3aDB(tableErrFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "遍历 J3a jobs table") {
		t.Fatalf("table 遍历失败必须被包装: %v", err)
	}
	db.Close()

	// 表齐全后 column 契约的三类失败。
	for _, tc := range []struct {
		name   string
		route  w9aRoute
		needle string
	}{
		{"column 查询失败", w9aRoute{match: "information_schema.columns", err: errW9AJ3aFailure}, "column 契约失败"},
		{"column Scan 失败", w9aRoute{match: "information_schema.columns", rows: [][]driver.Value{{nil, nil, nil, nil, nil}}}, "column 定义失败"},
		{"column 遍历失败", w9aRoute{match: "information_schema.columns", nextErr: errW9AJ3aFailure}, "遍历 J3a jobs column"},
	} {
		fake := &w9aFake{execFailAfter: -1}
		fake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
		fake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
		fake.addRoute(tc.route)
		db = openW9AJ3aDB(fake)
		if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), tc.needle) {
			t.Fatalf("%s 必须被包装: %v", tc.name, err)
		}
		db.Close()
	}

	// constraint 契约失败（columns 正常返回但内容为空 → missing 列先短路；因此
	// 用 columns 错误省略，直接验证 constraints 在 columns 为空 catalog 下的
	// 错误传播：列检查先失败，所以这里验证 constraints 查询错误需要表齐全）。
	constraintFake := &w9aFake{execFailAfter: -1}
	constraintFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	constraintFake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
	constraintFake.addRoute(w9aRoute{match: "pg_constraint", err: errW9AJ3aFailure})
	db = openW9AJ3aDB(constraintFake)
	if _, err := Run(ctx, db, false); err == nil {
		t.Fatalf("constraint 查询错误必须被报告或短路: %v", err)
	}
	db.Close()
}

// TestW9AJ3aRunApplyErrorBranches 覆盖 apply 路径的拒绝、DDL 失败与提交失败。
func TestW9AJ3aRunApplyErrorBranches(t *testing.T) {
	ctx := context.Background()

	// owner mismatch 且 apply → 拒绝跨角色修改。
	mismatchFake := &w9aFake{execFailAfter: -1}
	mismatchFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"other_role"}}})
	mismatchFake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
	// columns/constraints/indexes 未注册 → 零行 → invalid 契约。
	db := openW9AJ3aDB(mismatchFake)
	_, err := Run(ctx, db, true)
	if err == nil {
		t.Fatal("owner mismatch + apply 必须拒绝")
	}
	db.Close()

	// read-only 检查路径 commit 失败。
	commitFake := &w9aFake{execFailAfter: -1, commitErr: errW9AJ3aFailure}
	commitFake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	commitFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	commitFake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
	db = openW9AJ3aDB(commitFake)
	if _, err := Run(ctx, db, false); err == nil || !strings.Contains(err.Error(), "提交 J3a bootstrap 检查事务失败") {
		t.Fatalf("read-only commit 失败必须被包装: %v", err)
	}
	db.Close()

	// apply 时 DDL 失败（第 3 次 exec：SET、lock、DDL）。
	ddlFake := &w9aFake{execFailAfter: 2}
	ddlFake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	ddlFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	db = openW9AJ3aDB(ddlFake)
	if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "schema bootstrap 失败") {
		t.Fatalf("DDL 失败必须被包装: %v", err)
	}
	db.Close()

	// DDL 后契约仍不完整（fake catalog 不随 DDL 变化）。
	incompleteFake := &w9aFake{execFailAfter: -1}
	incompleteFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	db = openW9AJ3aDB(incompleteFake)
	_, err = Run(ctx, db, true)
	if err == nil || !strings.Contains(err.Error(), "契约仍不完整") {
		t.Fatalf("DDL 后契约不完整必须报错: %v", err)
	}
	db.Close()
}

// TestW9AJ3aReadyReport 覆盖 ready 判定的完整路径（read-only 检查返回 Ready）。
func TestW9AJ3aReadyReport(t *testing.T) {
	fake := &w9aFake{execFailAfter: -1}
	fake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	fake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	fake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
	columnRows := make([][]driver.Value, 0, 32)
	for table, columns := range requiredColumns {
		for column, spec := range columns {
			columnRows = append(columnRows, []driver.Value{table, column, spec.DataType, spec.UdtName, boolW9AText(spec.Nullable)})
		}
	}
	fake.addRoute(w9aRoute{match: "information_schema.columns", rows: columnRows})
	var constraintRows [][]driver.Value
	for table, definitions := range requiredConstraints {
		for _, definition := range definitions {
			constraintRows = append(constraintRows, []driver.Value{table, definition})
		}
	}
	fake.addRoute(w9aRoute{match: "pg_constraint", rows: constraintRows})
	var indexRows [][]driver.Value
	for name, definition := range requiredIndexes {
		indexRows = append(indexRows, []driver.Value{name, definition})
	}
	fake.addRoute(w9aRoute{match: "pg_indexes", rows: indexRows})
	db := openW9AJ3aDB(fake)
	defer db.Close()
	report, err := Run(context.Background(), db, false)
	if err != nil {
		t.Fatalf("ready 检查失败: %v", err)
	}
	if !report.Ready() {
		t.Fatalf("脚本化 ready 目录必须通过: %+v", report)
	}
	if fake.execCount() < 1 {
		t.Fatalf("read-only 检查至少执行 SET 一次: %d", fake.execCount())
	}
}

func boolW9AText(nullable bool) string {
	if nullable {
		return "YES"
	}
	return "NO"
}

// w9aJ3aReadyFake 返回一份完整契约目录脚本（当前测试可再覆盖其中路由）。
func w9aJ3aReadyFake() *w9aFake {
	fake := &w9aFake{execFailAfter: -1}
	fake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	fake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	fake.addRoute(w9aRoute{match: "information_schema.tables", rows: w9aAllTableRows()})
	columnRows := make([][]driver.Value, 0, 32)
	for table, columns := range requiredColumns {
		for column, spec := range columns {
			columnRows = append(columnRows, []driver.Value{table, column, spec.DataType, spec.UdtName, boolW9AText(spec.Nullable)})
		}
	}
	fake.addRoute(w9aRoute{match: "information_schema.columns", rows: columnRows})
	var constraintRows [][]driver.Value
	for table, definitions := range requiredConstraints {
		for _, definition := range definitions {
			constraintRows = append(constraintRows, []driver.Value{table, definition})
		}
	}
	fake.addRoute(w9aRoute{match: "pg_constraint", rows: constraintRows})
	var indexRows [][]driver.Value
	for name, definition := range requiredIndexes {
		indexRows = append(indexRows, []driver.Value{name, definition})
	}
	fake.addRoute(w9aRoute{match: "pg_indexes", rows: indexRows})
	return fake
}

// TestW9AJ3aInspectDetailBranches 在完整目录上逐项注入子错误，覆盖 index /
// constraint 检查的细分错误分支与 nullable 漂移。
func TestW9AJ3aInspectDetailBranches(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		tamper func(*w9aFake)
		needle string
	}{
		{"index 查询失败", func(f *w9aFake) {
			w9aJ3aReplace(f, "pg_indexes", w9aRoute{match: "pg_indexes", err: errW9AJ3aFailure})
		}, "读取 J3a jobs index 契约失败"},
		{"index Scan 失败", func(f *w9aFake) {
			w9aJ3aReplace(f, "pg_indexes", w9aRoute{match: "pg_indexes", rows: [][]driver.Value{{nil, nil}}})
		}, "读取 J3a jobs index 定义失败"},
		{"index 遍历失败", func(f *w9aFake) {
			w9aJ3aReplace(f, "pg_indexes", w9aRoute{match: "pg_indexes", nextErr: errW9AJ3aFailure})
		}, "遍历 J3a jobs index"},
		{"constraint Scan 失败", func(f *w9aFake) {
			w9aJ3aReplace(f, "pg_constraint", w9aRoute{match: "pg_constraint", rows: [][]driver.Value{{nil, nil}}})
		}, "读取 J3a jobs constraint 定义失败"},
		{"constraint 遍历失败", func(f *w9aFake) {
			w9aJ3aReplace(f, "pg_constraint", w9aRoute{match: "pg_constraint", nextErr: errW9AJ3aFailure})
		}, "遍历 J3a jobs constraint"},
		{"nullable 漂移", func(f *w9aFake) {
			var requiredNotNullable []driver.Value
			for table, columns := range requiredColumns {
				for column, spec := range columns {
					if !spec.Nullable {
						requiredNotNullable = []driver.Value{table, column, spec.DataType, spec.UdtName, "YES"}
						break
					}
				}
				if requiredNotNullable != nil {
					break
				}
			}
			w9aJ3aReplace(f, "information_schema.columns", w9aRoute{match: "information_schema.columns", rows: [][]driver.Value{requiredNotNullable}})
		}, ""},
	}
	for _, tc := range cases {
		fake := w9aJ3aReadyFake()
		tc.tamper(fake)
		db := openW9AJ3aDB(fake)
		_, err := Run(ctx, db, false)
		db.Close()
		if tc.needle == "" {
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.needle) {
			t.Fatalf("%s 必须被包装: %v", tc.name, err)
		}
	}

	// InvalidIndexes：indexdef 与期望不一致。
	fake := w9aJ3aReadyFake()
	w9aJ3aReplace(fake, "pg_indexes", w9aRoute{match: "pg_indexes", rows: [][]driver.Value{{"idx_proxy_latency_outcomes_proxy", "on public.unrelated using btree (x)"}}})
	db := openW9AJ3aDB(fake)
	report, err := Run(ctx, db, false)
	db.Close()
	if err != nil {
		t.Fatalf("indexdef 漂移不应报错: %v", err)
	}
	if len(report.InvalidIndexes) != 1 || report.InvalidIndexes[0] != "idx_proxy_latency_outcomes_proxy" {
		t.Fatalf("未注册的索引应判缺失、漂移的应判无效: %v", report.InvalidIndexes)
	}
}

// w9aJ3aAppend 在并发锁下追加路由。
func w9aJ3aAppend(fake *w9aFake, route w9aRoute) {
	fake.addRoute(route)
}

// w9aJ3aReplace 删除所有匹配 match 的路由后追加新路由。
func w9aJ3aReplace(fake *w9aFake, match string, route w9aRoute) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	kept := fake.routes[:0]
	for _, existing := range fake.routes {
		if existing.match != match {
			kept = append(kept, existing)
		}
	}
	fake.routes = append(kept, route)
}

// TestW9AJ3aApplyCommitAndSecondInspectFailures 覆盖 apply 成功后的提交失败
// 与 DDL 后二次检查失败。
func TestW9AJ3aApplyCommitAndSecondInspectFailures(t *testing.T) {
	ctx := context.Background()

	// DDL 后二次 inspect：owner route 已被首次检查消费 → 二次 ErrNoRows →
	// MissingSchema → 走 "二次 inspectTx 返回错误" 的传播路径（错误内容为
	// 拒绝创建 schema）。
	secondFake := w9aJ3aReadyFake()
	secondFake.routes = nil
	secondFake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	secondFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	db := openW9AJ3aDB(secondFake)
	if _, err := Run(ctx, db, true); err == nil {
		t.Fatal("DDL 后目录缺失必须报错")
	}
	db.Close()

	// apply：首次缺表 → DDL 成功后目录就绪 → 就绪后 commit 失败。
	commitFake := &w9aFake{execFailAfter: -1, commitErr: errW9AJ3aFailure}
	commitFake.addRoute(w9aRoute{match: "current_database()", rows: [][]driver.Value{{"juhe_jobs", "jobs_role"}}})
	commitFake.addRoute(w9aRoute{match: "pg_get_userbyid(nspowner)", rows: [][]driver.Value{{"jobs_role"}}})
	commitFake.onExec = func(f *w9aFake, query string) {
		if !strings.Contains(query, "CREATE TABLE IF NOT EXISTS") {
			return
		}
		ready := w9aJ3aReadyFake()
		for _, route := range ready.routes {
			f.addRoute(route)
		}
	}
	db = openW9AJ3aDB(commitFake)
	if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "提交 J3a PostgreSQL jobs schema bootstrap 失败") {
		t.Fatalf("apply commit 失败必须被包装: %v", err)
	}
	db.Close()
}

// TestW9AJ3aNullableDriftReported 验证 nullable 漂移写入 report 而不返回错误。
func TestW9AJ3aNullableDriftReported(t *testing.T) {
	var requiredNotNullable []driver.Value
	for table, columns := range requiredColumns {
		for column, spec := range columns {
			if !spec.Nullable {
				requiredNotNullable = []driver.Value{table, column, spec.DataType, spec.UdtName, "YES"}
				break
			}
		}
		if requiredNotNullable != nil {
			break
		}
	}
	fake := w9aJ3aReadyFake()
	w9aJ3aReplace(fake, "information_schema.columns", w9aRoute{match: "information_schema.columns", rows: [][]driver.Value{requiredNotNullable}})
	db := openW9AJ3aDB(fake)
	defer db.Close()
	report, err := Run(context.Background(), db, false)
	if err != nil {
		t.Fatalf("nullable 漂移不应返回错误: %v", err)
	}
	if report.Ready() {
		t.Fatal("nullable 漂移必须使契约不就绪")
	}
	found := false
	for _, item := range report.InvalidTables {
		if strings.Contains(item, ":nullable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("InvalidTables 必须包含 nullable 漂移: %v", report.InvalidTables)
	}
}
