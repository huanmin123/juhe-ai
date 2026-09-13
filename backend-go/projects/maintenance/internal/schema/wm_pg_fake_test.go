package schema

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

	_ "modernc.org/sqlite"
)

// 本文件提供 schema 包共享的 PG 录制驱动：EnsurePostgres / EnsurePostgresSeeds
// 通过 *sql.DB 注入，无需真实 PostgreSQL。录制器捕获全部语句供语义断言，
// 支持 FIFO 脚本化查询结果与按序号注入执行失败。真实 PG 行为仍由
// JUHE_AI_PG_SCHEMA_SMOKE_URL 门控的 smoke 测试覆盖（本文件不改动该门控）。

type wmCapturedStatement struct {
	query string
	args  []any
}

type wmScriptedRows struct {
	match string
	rows  [][]driver.Value
}

type wmSchemaRecorder struct {
	mu            sync.Mutex
	execs         []wmCapturedStatement
	queries       []wmCapturedStatement
	failExecAfter int
	scripted      []wmScriptedRows
	scriptedMu    sync.Mutex
}

func (r *wmSchemaRecorder) recordExec(query string, args []driver.NamedValue) error {
	captured := wmCaptureArgs(query, args)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failExecAfter > 0 && len(r.execs)+1 > r.failExecAfter {
		return fmt.Errorf("wm schema fake: 注入执行失败（第 %d 条）", len(r.execs)+1)
	}
	r.execs = append(r.execs, captured)
	return nil
}

func (r *wmSchemaRecorder) recordQuery(query string, args []driver.NamedValue) {
	captured := wmCaptureArgs(query, args)
	r.mu.Lock()
	r.queries = append(r.queries, captured)
	r.mu.Unlock()
}

func wmCaptureArgs(query string, args []driver.NamedValue) wmCapturedStatement {
	values := make([]any, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	return wmCapturedStatement{query: query, args: values}
}

func (r *wmSchemaRecorder) execCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.execs)
}

func (r *wmSchemaRecorder) execQueries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	queries := make([]string, len(r.execs))
	for i, statement := range r.execs {
		queries[i] = statement.query
	}
	return queries
}

func (r *wmSchemaRecorder) queryCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queries)
}

// script 注册一条按子串匹配的查询结果（FIFO，先登记先消费）。
func (r *wmSchemaRecorder) script(match string, columns []string, rows [][]driver.Value) {
	r.scriptedMu.Lock()
	defer r.scriptedMu.Unlock()
	r.scripted = append(r.scripted, wmScriptedRows{match: match, rows: rows})
}

func (r *wmSchemaRecorder) popScripted(query string) ([][]driver.Value, bool) {
	r.scriptedMu.Lock()
	defer r.scriptedMu.Unlock()
	for index, item := range r.scripted {
		if strings.Contains(query, item.match) {
			r.scripted = append(r.scripted[:index], r.scripted[index+1:]...)
			return item.rows, true
		}
	}
	return nil, false
}

type wmSchemaConnector struct{ rec *wmSchemaRecorder }

func (c wmSchemaConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmSchemaConn{rec: c.rec}, nil
}

func (c wmSchemaConnector) Driver() driver.Driver { return wmSchemaDriver{} }

type wmSchemaDriver struct{}

func (wmSchemaDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm schema fake driver: 请使用 sql.OpenDB")
}

type wmSchemaConn struct{ rec *wmSchemaRecorder }

func (c *wmSchemaConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm schema fake driver: Prepare 不应被调用（走 Execer/Queryer）")
}

func (c *wmSchemaConn) Close() error { return nil }

func (c *wmSchemaConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm schema fake driver: Begin 不应被调用")
}

func (c *wmSchemaConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &wmSchemaTx{}, nil
}

func (c *wmSchemaConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm schema fake driver: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmSchemaConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.rec.recordExec(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(3), nil
}

func (c *wmSchemaConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.recordQuery(query, args)
	if rows, ok := c.rec.popScripted(query); ok {
		columns := make([]string, 0, 1)
		if len(rows) > 0 {
			for i := range rows[0] {
				columns = append(columns, fmt.Sprintf("c%d", i))
			}
		} else {
			columns = append(columns, "c0")
		}
		return &wmSchemaRows{columns: columns, rows: rows}, nil
	}
	// 默认零行结果：QueryRow().Scan() 语义等价 sql.ErrNoRows。
	return &wmSchemaRows{columns: []string{"c0"}}, nil
}

type wmSchemaTx struct{}

func (wmSchemaTx) Commit() error   { return nil }
func (wmSchemaTx) Rollback() error { return nil }

type wmSchemaRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *wmSchemaRows) Columns() []string { return r.columns }
func (r *wmSchemaRows) Close() error      { return nil }
func (r *wmSchemaRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

// openWMSchemaFakeDB 打开录制驱动的数据库句柄。
func openWMSchemaFakeDB(rec *wmSchemaRecorder) *sql.DB {
	return sql.OpenDB(wmSchemaConnector{rec: rec})
}

// TestWMEnsurePostgresExecutesContractStatements 覆盖 EnsurePostgres 的执行
// 循环：每条契约语句一次执行、每个 schema 组一次 CREATE SCHEMA，且执行顺序
// 先建 schema 再 SET search_path。
func TestWMEnsurePostgresExecutesContractStatements(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result, err := EnsurePostgres(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	if result.StatementCount != len(postgresSchemaStatements) {
		t.Fatalf("StatementCount=%d want %d", result.StatementCount, len(postgresSchemaStatements))
	}
	if result.SchemaCount != 6 {
		t.Fatalf("SchemaCount=%d want 6（Node 六库）", result.SchemaCount)
	}
	if got := rec.execCount(); got != len(postgresSchemaStatements)+6 {
		t.Fatalf("执行次数=%d want %d（614 语句 + 6 CREATE SCHEMA）", got, len(postgresSchemaStatements)+6)
	}
	queries := rec.execQueries()
	if !strings.HasPrefix(queries[0], `CREATE SCHEMA IF NOT EXISTS "juhe_business"`) {
		t.Fatalf("首条执行应是 juhe_business 的 CREATE SCHEMA: %q", queries[0])
	}
	if !strings.Contains(queries[1], `SET search_path TO "juhe_business", public;`) {
		t.Fatalf("第二条执行应设置 search_path: %q", queries[1])
	}
}

// TestWMEnsurePostgresPropagatesFailureWithPosition 覆盖执行失败时的语句定位
// 错误包装。
func TestWMEnsurePostgresPropagatesFailureWithPosition(t *testing.T) {
	rec := &wmSchemaRecorder{failExecAfter: 2}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	_, err := EnsurePostgres(context.Background(), db)
	// 第 1 次执行是 CREATE SCHEMA，第 3 次失败即 schema 组内第 1 条语句。
	if err == nil || !strings.Contains(err.Error(), "postgres schema statement 1 (juhe_business/") {
		t.Fatalf("失败必须带语句定位: %v", err)
	}
}

// TestWMQuotePGIdentifierEscapesEmbeddedQuotes 锁定标识符引用的转义规则。
func TestWMQuotePGIdentifierEscapesEmbeddedQuotes(t *testing.T) {
	if got := quotePGIdentifier(`juhe_"x`); got != `"juhe_""x"` {
		t.Fatalf("quotePGIdentifier 转义错误: %q", got)
	}
	if got := quotePGIdentifier("plain"); got != `"plain"` {
		t.Fatalf("普通标识符必须加双引号: %q", got)
	}
}

// TestWMEnsurePostgresSeedsRunsPortableSubset 覆盖 EnsurePostgresSeeds 的完整
// 可移植子集执行（QueryRow 全部 ErrNoRows → 走“全量插入 + 修复跳过”路径）。
func TestWMEnsurePostgresSeedsRunsPortableSubset(t *testing.T) {
	rec := &wmSchemaRecorder{}
	db := openWMSchemaFakeDB(rec)
	defer db.Close()
	result, err := EnsurePostgresSeeds(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsurePostgresSeeds: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("StatementCount 必须为正: %d", result.StatementCount)
	}
	queries := rec.execQueries()
	joined := strings.Join(queries, "\n")
	for _, fragment := range []string{
		`INSERT INTO "juhe_business"."system_accounts"`,
		`INSERT INTO "juhe_business"."global_settings"`,
		`INSERT INTO "juhe_business"."providers"`,
		`INSERT INTO "juhe_business"."protocols"`,
		`INSERT INTO "juhe_business"."external_integration_sources"`,
		`INSERT INTO "juhe_business"."system_settings"`,
	} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("可移植 seed 缺少语句 %s", fragment)
		}
	}
	// 每个 profile 一次 account_types 修复查询；ErrNoRows 下不产生 UPDATE。
	if got := rec.queryCount(); got != len(pgSeedProfiles) {
		t.Fatalf("修复查询次数=%d want %d", got, len(pgSeedProfiles))
	}
	for _, query := range queries {
		if strings.Contains(query, "account_types_json") && strings.HasPrefix(strings.TrimSpace(query), "UPDATE") {
			t.Fatalf("ErrNoRows 路径不应产生修复 UPDATE: %q", query)
		}
	}
}
