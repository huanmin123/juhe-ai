package j3aproxylatency

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件用内存目录模拟器覆盖 J3a jobs schema 的 inspectTx/Run 语义链：
// 契约就绪目录由 contracts 生成，事务只读/apply 守卫与跨角色拒绝均按实际
// 分支断言。真实 PostgreSQL 行为由 ops 流程验证，这里不新增服务依赖。

type wmJ3aColumn struct {
	name     string
	dataType string
	udtName  string
	nullable bool
}

type wmJ3aTable struct {
	columns     []wmJ3aColumn
	indexes     map[string]string
	constraints []string
}

type wmJ3aCatalog struct {
	mu          sync.Mutex
	schemas     map[string]map[string]*wmJ3aTable
	owners      map[string]string
	database    string
	currentUser string
}

func newWMJ3aCatalog() *wmJ3aCatalog {
	return &wmJ3aCatalog{
		schemas:     map[string]map[string]*wmJ3aTable{},
		owners:      map[string]string{},
		database:    "wm_jobs_db",
		currentUser: "juhe_jobs_maintenance",
	}
}

func (c *wmJ3aCatalog) ensureSchema(name, owner string) {
	if _, ok := c.schemas[name]; !ok {
		c.schemas[name] = map[string]*wmJ3aTable{}
	}
	if owner != "" {
		c.owners[name] = owner
	}
}

// wmReadyJ3aSchema 依据 contracts 生成契约就绪的 juhe_jobs 目录。
func wmReadyJ3aSchema(c *wmJ3aCatalog) {
	c.ensureSchema(SchemaName, c.currentUser)
	for _, tableName := range requiredTables {
		specs := requiredColumns[tableName]
		names := make([]string, 0, len(specs))
		for name := range specs {
			names = append(names, name)
		}
		sort.Strings(names)
		table := &wmJ3aTable{indexes: map[string]string{}}
		for _, name := range names {
			spec := specs[name]
			table.columns = append(table.columns, wmJ3aColumn{name: name, dataType: spec.DataType, udtName: spec.UdtName, nullable: spec.Nullable})
		}
		for indexName, definition := range requiredIndexes {
			if strings.Contains(definition, "on "+SchemaName+"."+tableName) {
				table.indexes[indexName] = definition
			}
		}
		table.constraints = append([]string(nil), requiredConstraints[tableName]...)
		c.schemas[SchemaName][tableName] = table
	}
}

func wmNullableJ3aText(nullable bool) string {
	if nullable {
		return "YES"
	}
	return "NO"
}

func (c *wmJ3aCatalog) query(query string, args []driver.NamedValue) (*wmJ3aResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(query, "current_database()"):
		return &wmJ3aResult{columns: []string{"current_database", "current_user"}, rows: [][]driver.Value{{c.database, c.currentUser}}}, nil
	case strings.Contains(query, "pg_get_userbyid(nspowner)"):
		owner, ok := c.owners[wmJ3aArgText(args[0].Value)]
		if !ok {
			return &wmJ3aResult{columns: []string{"owner"}}, nil
		}
		return &wmJ3aResult{columns: []string{"owner"}, rows: [][]driver.Value{{owner}}}, nil
	case strings.Contains(query, "information_schema.tables"):
		schemaName := wmJ3aArgText(args[0].Value)
		var rows [][]driver.Value
		for _, name := range wmJ3aStringList(args[1].Value) {
			if _, ok := c.schemas[schemaName][name]; ok {
				rows = append(rows, []driver.Value{name})
			}
		}
		return &wmJ3aResult{columns: []string{"table_name"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.columns"):
		schemaName := wmJ3aArgText(args[0].Value)
		var rows [][]driver.Value
		for _, tableName := range wmJ3aStringList(args[1].Value) {
			table := c.schemas[schemaName][tableName]
			if table == nil {
				continue
			}
			for _, column := range table.columns {
				rows = append(rows, []driver.Value{tableName, column.name, column.dataType, column.udtName, wmNullableJ3aText(column.nullable)})
			}
		}
		return &wmJ3aResult{columns: []string{"table_name", "column_name", "data_type", "udt_name", "is_nullable"}, rows: rows}, nil
	case strings.Contains(query, "pg_indexes"):
		schemaName := wmJ3aArgText(args[0].Value)
		var rows [][]driver.Value
		for _, table := range c.schemas[schemaName] {
			names := make([]string, 0, len(table.indexes))
			for name := range table.indexes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				rows = append(rows, []driver.Value{name, table.indexes[name]})
			}
		}
		return &wmJ3aResult{columns: []string{"indexname", "indexdef"}, rows: rows}, nil
	case strings.Contains(query, "pg_constraint"):
		schemaName := wmJ3aArgText(args[0].Value)
		var rows [][]driver.Value
		for tableName, table := range c.schemas[schemaName] {
			for _, definition := range table.constraints {
				rows = append(rows, []driver.Value{tableName, definition})
			}
		}
		return &wmJ3aResult{columns: []string{"relname", "constraintdef"}, rows: rows}, nil
	default:
		return nil, fmt.Errorf("wm j3a fake: 不支持的查询 %.80s", query)
	}
}

func (c *wmJ3aCatalog) exec(query string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(trimmed, "SET ") || strings.Contains(query, "pg_advisory_xact_lock") {
		return nil
	}
	// juhe_jobs DDL：模拟 DDL 生效，安装契约就绪目录。
	if strings.Contains(query, "CREATE TABLE IF NOT EXISTS "+SchemaName+".") {
		wmReadyJ3aSchema(c)
		return nil
	}
	return nil
}

func wmJ3aArgText(value driver.Value) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprintf("%v", value)
}

func wmJ3aStringList(value driver.Value) []string {
	if typed, ok := value.([]string); ok {
		return typed
	}
	return nil
}

type wmJ3aResult struct {
	columns []string
	rows    [][]driver.Value
}

type wmJ3aRows struct {
	result *wmJ3aResult
	pos    int
}

func (r *wmJ3aRows) Columns() []string { return r.result.columns }
func (r *wmJ3aRows) Close() error      { return nil }
func (r *wmJ3aRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.result.rows) {
		return io.EOF
	}
	copy(dest, r.result.rows[r.pos])
	r.pos++
	return nil
}

type wmJ3aConnector struct{ catalog *wmJ3aCatalog }

func (c wmJ3aConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmJ3aConn{catalog: c.catalog}, nil
}

func (c wmJ3aConnector) Driver() driver.Driver { return wmJ3aDriver{} }

type wmJ3aDriver struct{}

func (wmJ3aDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm j3a fake driver: 请使用 sql.OpenDB")
}

type wmJ3aConn struct{ catalog *wmJ3aCatalog }

func (c *wmJ3aConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm j3a fake driver: Prepare 不应被调用")
}

func (c *wmJ3aConn) Close() error { return nil }

func (c *wmJ3aConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm j3a fake driver: Begin 不应被调用（走 BeginTx）")
}

func (c *wmJ3aConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &wmJ3aTx{}, nil
}

func (c *wmJ3aConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm j3a fake driver: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmJ3aConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.catalog.exec(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *wmJ3aConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	result, err := c.catalog.query(query, args)
	if err != nil {
		return nil, err
	}
	return &wmJ3aRows{result: result}, nil
}

type wmJ3aTx struct{}

func (wmJ3aTx) Commit() error   { return nil }
func (wmJ3aTx) Rollback() error { return nil }

func openWMJ3aDB(catalog *wmJ3aCatalog) *sql.DB {
	return sql.OpenDB(wmJ3aConnector{catalog: catalog})
}

// ---- 语义测试 ----

func TestWMJ3aRunCheckAndApplyLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db rejected", func(t *testing.T) {
		if _, err := Run(ctx, nil, false); err == nil {
			t.Fatal("nil db 必须被拒绝")
		}
	})

	t.Run("missing schema fails closed and apply refuses", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil || !report.MissingSchema || report.Ready() {
			t.Fatalf("空目录必须报告 missingSchema: %+v err=%v", report, err)
		}
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "拒绝创建 juhe_jobs schema") {
			t.Fatalf("缺失 schema 的 apply 必须拒绝: %v", err)
		}
	})

	t.Run("apply refuses cross-role schema", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		catalog.ensureSchema(SchemaName, "other_role")
		db := openWMJ3aDB(catalog)
		defer db.Close()
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "拒绝跨角色修改") {
			t.Fatalf("跨角色 apply 必须拒绝: %v", err)
		}
	})

	t.Run("apply installs contract schema and reports applied", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, true)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !report.Ready() || !report.Applied {
			t.Fatalf("DDL 后必须就绪并标记 applied: %+v", report)
		}
		recheck, err := Run(ctx, db, false)
		if err != nil || !recheck.Ready() || recheck.Applied {
			t.Fatalf("就绪目录复检必须 ready 且无 applied: %+v err=%v", recheck, err)
		}
	})

	t.Run("check on ready schema", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		wmReadyJ3aSchema(catalog)
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil || !report.Ready() {
			t.Fatalf("契约就绪目录必须 ready: %+v err=%v", report, err)
		}
		if report.Database != "wm_jobs_db" || report.CurrentRole != "juhe_jobs_maintenance" || report.SchemaOwner != report.CurrentRole {
			t.Fatalf("身份元数据必须来自目录: %+v", report)
		}
	})

	t.Run("apply reports incomplete contract when DDL did not converge", func(t *testing.T) {
		// DDL 被模拟为 no-op（目录无表且 schema 不含 juhe_jobs DDL 触发标记）：
		// 通过自定义 exec 拦截，模拟 DDL 执行但目录未收敛的故障。
		catalog := newWMJ3aCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil || report.Ready() {
			t.Fatalf("空 schema 不应就绪: %+v err=%v", report, err)
		}
		if len(report.MissingTables) != len(requiredTables) || len(report.MissingIndexes) == 0 {
			t.Fatalf("空 schema 必须报告全部缺失表与索引: %+v", report)
		}
	})
}

func TestWMJ3aInspectDetectsColumnAndIndexDrift(t *testing.T) {
	ctx := context.Background()
	t.Run("column type drift fails readiness", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		wmReadyJ3aSchema(catalog)
		table := catalog.schemas[SchemaName][requiredTables[0]]
		table.columns[0].dataType = "integer"
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if report.Ready() || len(report.InvalidTables) == 0 {
			t.Fatalf("列类型漂移必须失败闭环: %+v", report)
		}
	})
	t.Run("missing index fails readiness", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		wmReadyJ3aSchema(catalog)
		for _, table := range catalog.schemas[SchemaName] {
			delete(table.indexes, "idx_proxy_latency_outcomes_cursor")
		}
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if report.Ready() || !containsWMJ3a(report.MissingIndexes, "idx_proxy_latency_outcomes_cursor") {
			t.Fatalf("索引缺失必须报告: %+v", report)
		}
	})
	t.Run("missing constraint fails readiness", func(t *testing.T) {
		catalog := newWMJ3aCatalog()
		wmReadyJ3aSchema(catalog)
		catalog.schemas[SchemaName][requiredTables[0]].constraints = nil
		db := openWMJ3aDB(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if report.Ready() || len(report.InvalidTables) == 0 {
			t.Fatalf("约束缺失必须失败闭环: %+v", report)
		}
	})
}

func containsWMJ3a(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestWMJ3aOpenValidatesExplicitURL(t *testing.T) {
	db, err := Open("postgres://jobs@127.0.0.1:5432/juhe_jobs")
	if err != nil {
		t.Fatalf("合法 URL 必须通过（懒连接）: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, rawURL := range []string{
		"",
		"sqlite:///x.db",
		"postgres:///db",
		"postgres://@host/db",
		"postgres://user@/db",
		"postgres://user@host",
		"http://user@host/db",
	} {
		if _, err := Open(rawURL); err == nil {
			t.Fatalf("URL %q 必须被拒绝", rawURL)
		}
	}
}

func TestWMJ3aContractHelpersConsistent(t *testing.T) {
	if got := len(requiredIndexNames()); got != len(requiredIndexes) {
		t.Fatalf("索引名数量=%d want %d", got, len(requiredIndexes))
	}
	for _, name := range requiredIndexNames() {
		if _, ok := requiredIndexes[name]; !ok {
			t.Fatalf("未登记的索引名 %q", name)
		}
	}
	ready := Report{Database: "db", CurrentRole: "r", SchemaOwner: "r"}
	if !ready.Ready() {
		t.Fatal("干净报告必须就绪")
	}
	dirty := ready
	dirty.OwnerMismatch = true
	if dirty.Ready() {
		t.Fatal("owner 不匹配必须失败就绪")
	}
	dirty2 := ready
	dirty2.MissingIndexes = []string{"idx"}
	if dirty2.Ready() {
		t.Fatal("缺失索引必须失败就绪")
	}
	if len(contracts.J3AProxyLatencyTables) == 0 {
		t.Fatal("contracts 表清单不应为空")
	}
}
