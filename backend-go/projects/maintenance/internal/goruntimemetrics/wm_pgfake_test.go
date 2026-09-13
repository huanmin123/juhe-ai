package goruntimemetrics

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
)

// 本文件用内存目录模拟器覆盖 Go runtime metrics 预检（inspect 族）与
// Run 的 apply 语义：契约就绪目录按 requiredColumns/requiredPrimaryKeys
// 生成，空目录与漂移目录必须失败闭环。真实 PostgreSQL 行为仍由运维流程
// 验证，这里不新增服务依赖。

type wmMetricsCatalog struct {
	mu          sync.Mutex
	tables      map[string]*wmMetricsTable
	schemaReady bool
	database    string
	currentUser string
}

type wmMetricsTable struct {
	columns map[string]wmMetricsColumn
	pks     []string
}

type wmMetricsColumn struct {
	dataType string
	udtName  string
	nullable bool
}

func newWMMetricsCatalog(ready bool) *wmMetricsCatalog {
	catalog := &wmMetricsCatalog{
		tables:      map[string]*wmMetricsTable{},
		schemaReady: ready,
		database:    "wm_metrics_db",
		currentUser: "juhe_maintenance",
	}
	if ready {
		wmBuildReadyMetricsSchema(catalog)
	}
	return catalog
}

func wmBuildReadyMetricsSchema(catalog *wmMetricsCatalog) {
	for tableName, columns := range requiredColumns {
		table := &wmMetricsTable{columns: map[string]wmMetricsColumn{}, pks: append([]string(nil), requiredPrimaryKeys[tableName]...)}
		for name, spec := range columns {
			nullable := optionalColumn(tableName, name)
			table.columns[name] = wmMetricsColumn{dataType: spec.dataType, udtName: spec.udtName, nullable: nullable}
		}
		// 契约之外的列不允许出现；就绪目录只含契约列。
		catalog.tables[tableName] = table
	}
}

func (c *wmMetricsCatalog) query(query string, args []driver.NamedValue) (*wmMetricsResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case strings.Contains(query, "current_database()"):
		return &wmMetricsResult{columns: []string{"database", "role"}, rows: [][]driver.Value{{c.database, c.currentUser}}}, nil
	case strings.Contains(query, "information_schema.table_constraints"):
		tableName := wmMetricsArgText(args[1].Value)
		table := c.tables[tableName]
		var rows [][]driver.Value
		if table != nil {
			for _, pk := range table.pks {
				rows = append(rows, []driver.Value{pk})
			}
		}
		return &wmMetricsResult{columns: []string{"column_name"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.tables"):
		// IN ($2,$3,$4)：args[1..3] 为三张表名。
		var rows [][]driver.Value
		for _, name := range []string{wmMetricsArgText(args[1].Value), wmMetricsArgText(args[2].Value), wmMetricsArgText(args[3].Value)} {
			if _, ok := c.tables[name]; ok {
				rows = append(rows, []driver.Value{name})
			}
		}
		return &wmMetricsResult{columns: []string{"table_name"}, rows: rows}, nil
	case strings.Contains(query, "is_nullable <> 'NO'"):
		var rows [][]driver.Value
		for tableName, table := range c.tables {
			names := make([]string, 0, len(table.columns))
			for name := range table.columns {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if table.columns[name].nullable {
					rows = append(rows, []driver.Value{tableName, name})
				}
			}
		}
		return &wmMetricsResult{columns: []string{"table_name", "column_name"}, rows: rows}, nil
	case strings.Contains(query, "information_schema.columns"):
		var rows [][]driver.Value
		for _, tableName := range []string{wmMetricsArgText(args[1].Value), wmMetricsArgText(args[2].Value), wmMetricsArgText(args[3].Value)} {
			table := c.tables[tableName]
			if table == nil {
				continue
			}
			names := make([]string, 0, len(table.columns))
			for name := range table.columns {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				column := table.columns[name]
				rows = append(rows, []driver.Value{tableName, name, column.dataType, column.udtName, wmMetricsNullableText(column.nullable)})
			}
		}
		return &wmMetricsResult{columns: []string{"table_name", "column_name", "data_type", "udt_name", "is_nullable"}, rows: rows}, nil
	default:
		return nil, fmt.Errorf("wm metrics fake: 不支持的查询 %.80s", query)
	}
}

func (c *wmMetricsCatalog) exec(query string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(trimmed, "CREATE ") || strings.HasPrefix(trimmed, "ALTER ") {
		// gometrics DDL 在模拟器中视为 no-op；目录状态由测试预置。
		return nil
	}
	return fmt.Errorf("wm metrics fake: 不支持的执行 %.80s", query)
}

func wmMetricsNullableText(nullable bool) string {
	if nullable {
		return "YES"
	}
	return "NO"
}

func wmMetricsArgText(value driver.Value) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return fmt.Sprintf("%v", value)
}

type wmMetricsResult struct {
	columns []string
	rows    [][]driver.Value
}

type wmMetricsRows struct {
	result *wmMetricsResult
	pos    int
}

func (r *wmMetricsRows) Columns() []string { return r.result.columns }
func (r *wmMetricsRows) Close() error      { return nil }
func (r *wmMetricsRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.result.rows) {
		return io.EOF
	}
	copy(dest, r.result.rows[r.pos])
	r.pos++
	return nil
}

type wmMetricsConnector struct{ catalog *wmMetricsCatalog }

func (c wmMetricsConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmMetricsConn{catalog: c.catalog}, nil
}

func (c wmMetricsConnector) Driver() driver.Driver { return wmMetricsDriver{} }

type wmMetricsDriver struct{}

func (wmMetricsDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm metrics fake driver: 请使用 sql.OpenDB")
}

type wmMetricsConn struct{ catalog *wmMetricsCatalog }

func (c *wmMetricsConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm metrics fake driver: Prepare 不应被调用")
}

func (c *wmMetricsConn) Close() error { return nil }

func (c *wmMetricsConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm metrics fake driver: Begin 不应被调用")
}

func (c *wmMetricsConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm metrics fake driver: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmMetricsConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.catalog.exec(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *wmMetricsConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	result, err := c.catalog.query(query, args)
	if err != nil {
		return nil, err
	}
	return &wmMetricsRows{result: result}, nil
}

func openWMMetricsDB(catalog *wmMetricsCatalog) *sql.DB {
	return sql.OpenDB(wmMetricsConnector{catalog: catalog})
}

// ---- 语义测试 ----

func TestWMRunNilDBRejected(t *testing.T) {
	if _, err := Run(context.Background(), nil, false); err == nil {
		t.Fatal("nil db 必须被拒绝")
	}
}

func TestWMRunCheckOnEmptySchemaReportsAllMissing(t *testing.T) {
	db := openWMMetricsDB(newWMMetricsCatalog(false))
	defer db.Close()
	report, err := Run(context.Background(), db, false)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report.Ready() || len(report.MissingTables) != len(requiredTables) {
		t.Fatalf("空 schema 必须报告全部缺失表: %+v", report)
	}
	if report.Schema != SchemaName {
		t.Fatalf("报告必须标注 schema: %+v", report)
	}
	if report.Database != "wm_metrics_db" || report.CurrentRole != "juhe_maintenance" {
		t.Fatalf("身份元数据必须来自目录: %+v", report)
	}
}

func TestWMRunCheckOnReadySchema(t *testing.T) {
	db := openWMMetricsDB(newWMMetricsCatalog(true))
	defer db.Close()
	report, err := Run(context.Background(), db, false)
	if err != nil || !report.Ready() {
		t.Fatalf("契约就绪目录必须 ready: %+v err=%v", report, err)
	}
}

func TestWMRunApplyOnReadySchemaMarksApplied(t *testing.T) {
	db := openWMMetricsDB(newWMMetricsCatalog(true))
	defer db.Close()
	report, err := Run(context.Background(), db, true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !report.Ready() || !report.Applied {
		t.Fatalf("apply 后必须 ready 且标记 applied: %+v", report)
	}
}

func TestWMRunApplyFailsWhenContractNotConverged(t *testing.T) {
	// 目录缺表且 DDL 在模拟器中为 no-op：apply 后契约仍不完整必须报错。
	db := openWMMetricsDB(newWMMetricsCatalog(false))
	defer db.Close()
	if _, err := Run(context.Background(), db, true); err == nil || !strings.Contains(err.Error(), "契约仍不完整") {
		t.Fatalf("未收敛的 apply 必须报错: %v", err)
	}
}

func TestWMInspectDetectsColumnAndPrimaryKeyDrift(t *testing.T) {
	t.Run("column type drift", func(t *testing.T) {
		catalog := newWMMetricsCatalog(true)
		catalog.tables["go_runtime_metrics_samples"].columns["goroutines"] = wmMetricsColumn{dataType: "text", udtName: "text"}
		db := openWMMetricsDB(catalog)
		defer db.Close()
		report, err := Run(context.Background(), db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if report.Ready() || len(report.InvalidTables) == 0 {
			t.Fatalf("列类型漂移必须失败闭环: %+v", report)
		}
	})
	t.Run("required column missing", func(t *testing.T) {
		catalog := newWMMetricsCatalog(true)
		delete(catalog.tables["go_runtime_metrics_hourly"].columns, "sample_count")
		db := openWMMetricsDB(catalog)
		defer db.Close()
		report, err := Run(context.Background(), db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		found := false
		for _, entry := range report.InvalidTables {
			if strings.Contains(entry, "go_runtime_metrics_hourly.sample_count") {
				found = true
			}
		}
		if report.Ready() || !found {
			t.Fatalf("缺列必须逐项报告: %+v", report)
		}
	})
	t.Run("nullable required column rejected", func(t *testing.T) {
		catalog := newWMMetricsCatalog(true)
		catalog.tables["go_runtime_metrics_samples"].columns["goroutines"] = wmMetricsColumn{dataType: "bigint", udtName: "int8", nullable: true}
		db := openWMMetricsDB(catalog)
		defer db.Close()
		report, err := Run(context.Background(), db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		found := false
		for _, entry := range report.InvalidTables {
			if strings.Contains(entry, "goroutines:nullable") {
				found = true
			}
		}
		if report.Ready() || !found {
			t.Fatalf("必需列可空必须失败闭环: %+v", report)
		}
	})
	t.Run("primary key drift", func(t *testing.T) {
		catalog := newWMMetricsCatalog(true)
		catalog.tables["go_runtime_metrics_hourly"].pks = []string{"service"}
		db := openWMMetricsDB(catalog)
		defer db.Close()
		report, err := Run(context.Background(), db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		found := false
		for _, entry := range report.InvalidTables {
			if strings.Contains(entry, "go_runtime_metrics_hourly:primary_key=service") {
				found = true
			}
		}
		if report.Ready() || !found {
			t.Fatalf("主键漂移必须失败闭环: %+v", report)
		}
	})
}

func TestWMOpenValidatesExplicitURL(t *testing.T) {
	db, err := Open("postgres://metrics@127.0.0.1:5432/juhe_metrics")
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
	} {
		if _, err := Open(rawURL); err == nil {
			t.Fatalf("URL %q 必须被拒绝", rawURL)
		}
	}
}

func TestWMOptionalColumnAllowlist(t *testing.T) {
	allowed := []string{"cpu_percent", "rss_bytes", "fd_count"}
	for _, column := range allowed {
		if !optionalColumn("go_runtime_metrics_samples", column) {
			t.Fatalf("samples 的可选列 %s 必须被接受", column)
		}
	}
	for _, column := range []string{"goroutines", "heap_objects", "service"} {
		if optionalColumn("go_runtime_metrics_samples", column) {
			t.Fatalf("samples 的必需列 %s 不得是可选", column)
		}
	}
	allowedHourly := []string{"avg_cpu_percent", "max_cpu_percent", "avg_rss_bytes", "max_rss_bytes", "avg_fd_count", "max_fd_count"}
	for _, column := range allowedHourly {
		if !optionalColumn("go_runtime_metrics_hourly", column) {
			t.Fatalf("hourly 的可选列 %s 必须被接受", column)
		}
		if optionalColumn("go_runtime_metrics_trend_windows", column) && column == "goroutines" {
			t.Fatalf("trend_windows 不应接受 %s", column)
		}
	}
	if optionalColumn("go_runtime_metrics_hourly", "sample_count") {
		t.Fatal("hourly 的必需列不得是可选")
	}
}

func TestWMSameStringsHelper(t *testing.T) {
	if !sameStrings([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("相同切片必须相等")
	}
	if sameStrings([]string{"a"}, []string{"a", "b"}) || sameStrings([]string{"a"}, []string{"b"}) {
		t.Fatal("不同切片必须不等")
	}
	if !sameStrings(nil, nil) {
		t.Fatal("空切片必须相等")
	}
}
