package schemasnapshot

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

var errW9ASnapshotInjected = errors.New("w9a snapshot 注入失败")

// w9aSnapResult 描述一条目录查询的脚本化结果。
type w9aSnapResult struct {
	columns  []string
	rows     [][]driver.Value
	queryErr error
	// nextErr 让 rows.Err() 返回错误（行耗尽后）。
	nextErr error
}

// w9aSnapConn 按 SQL 常量 key 匹配查询，支持错误注入。
type w9aSnapConn struct {
	routes map[string]w9aSnapResult
}

func (c *w9aSnapConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9a snapshot fake: Prepare 不应被调用")
}
func (c *w9aSnapConn) Close() error { return nil }
func (c *w9aSnapConn) Begin() (driver.Tx, error) {
	return nil, errors.New("w9a snapshot fake: Begin 不应被调用")
}

func (c *w9aSnapConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	for key, result := range c.routes {
		if strings.Contains(query, key) {
			if result.queryErr != nil {
				return nil, result.queryErr
			}
			columns := result.columns
			if columns == nil {
				columns = []string{"c0"}
			}
			return &w9aSnapRows{columns: columns, rows: result.rows, nextErr: result.nextErr}, nil
		}
	}
	return nil, fmt.Errorf("w9a snapshot fake: unexpected query %.80s", query)
}

// CheckNamedValue 放行 catalog 查询的 text[] 参数（schemaNames []string），
// 其余类型回退 database/sql 默认转换。
func (c *w9aSnapConn) CheckNamedValue(nv *driver.NamedValue) error {
	if _, ok := nv.Value.([]string); ok {
		return nil
	}
	return driver.ErrSkip
}

type w9aSnapRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
	nextErr error
}

func (r *w9aSnapRows) Columns() []string { return r.columns }
func (r *w9aSnapRows) Close() error      { return nil }
func (r *w9aSnapRows) Next(dest []driver.Value) error {
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

type w9aSnapConnector struct{ conn *w9aSnapConn }

func (c w9aSnapConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c w9aSnapConnector) Driver() driver.Driver                        { return w9aSnapDriver{} }

type w9aSnapDriver struct{}

func (w9aSnapDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w9a snapshot fake: 使用 sql.OpenDB")
}

func openW9ASnapshotDB(conn *w9aSnapConn) *sql.DB {
	return sql.OpenDB(w9aSnapConnector{conn: conn})
}

// w9aNormalRows 返回每个目录查询的一条合法行（可通过 Scan）。
func w9aNormalRows() map[string]w9aSnapResult {
	return map[string]w9aSnapResult{
		"pg_get_userbyid(n.nspowner)": {columns: []string{"name", "owner", "acl"}, rows: [][]driver.Value{{"public", "postgres", nil}}},
		"current_database()":          {columns: []string{"name", "oid", "serverAddress", "serverPort"}, rows: [][]driver.Value{{"snapdb", "16384", nil, int64(5432)}}},
		"pg_roles":                    {columns: []string{"name", "superuser", "createRole", "createDb", "canLogin", "replication", "bypassRls"}, rows: [][]driver.Value{{"app", false, false, false, true, false, false}}},
		"pg_extension":                {columns: []string{"name", "version", "schema"}, rows: [][]driver.Value{{"plpgsql", "1.0", nil}}},
		"c.relacl::text AS acl":       {columns: []string{"schema", "name", "kind", "owner", "persistence", "acl"}, rows: [][]driver.Value{{"public", "t1", "r", "postgres", "p", nil}}},
		"a.attnum AS ordinal":         {columns: []string{"schema", "relation", "name", "ordinal", "type", "udt", "nullable", "default_definition"}, rows: [][]driver.Value{{"public", "t1", "id", int16(1), "bigint", "int8", false, nil}}},
		"pg_constraint":               {columns: []string{"schema", "relation", "name", "type", "definition"}, rows: [][]driver.Value{{"public", "t1", "t1_pkey", "p", "PRIMARY KEY (id)"}}},
		"pg_get_indexdef":             {columns: []string{"schema", "relation", "name", "definition"}, rows: [][]driver.Value{{"public", "t1", "t1_pkey", "CREATE INDEX ..."}}},
		"pg_get_functiondef":          {columns: []string{"schema", "name", "identityArguments", "definition"}, rows: [][]driver.Value{{"public", "fn", "()", "CREATE FUNCTION ..."}}},
		"pg_trigger":                  {columns: []string{"schema", "relation", "name", "definition"}, rows: [][]driver.Value{{"public", "t1", "trg", "CREATE TRIGGER ..."}}},
		"pg_get_viewdef":              {columns: []string{"schema", "name", "materialized", "definition"}, rows: [][]driver.Value{{"public", "v1", false, "SELECT 1"}}},
		"pg_inherits":                 {columns: []string{"schema", "relation", "parentSchema", "parentRelation"}, rows: [][]driver.Value{{"public", "t1_2026", "public", "t1"}}},
		"relkind='S'":                 {columns: []string{"schema", "name", "owner"}, rows: [][]driver.Value{{"public", "t1_id_seq", "postgres"}}},
	}
}

// w9aQueryOrder 是 CollectSnapshot 消费目录查询的顺序（用于逐个注入失败）。
func w9aQueryOrder() []string {
	return []string{
		"pg_get_userbyid(n.nspowner)",
		"current_database()",
		"pg_roles",
		"pg_extension",
		"c.relacl::text AS acl",
		"a.attnum AS ordinal",
		"pg_constraint",
		"pg_get_indexdef",
		"pg_get_functiondef",
		"pg_trigger",
		"pg_get_viewdef",
		"pg_inherits",
		"relkind='S'",
	}
}

// TestW9ACollectSnapshotQueryFailuresInSequence 逐个查询注入失败，覆盖
// CollectSnapshot 中每个目录查询的错误传播分支。
func TestW9ACollectSnapshotQueryFailuresInSequence(t *testing.T) {
	order := w9aQueryOrder()
	for _, failingKey := range order {
		routes := w9aNormalRows()
		result := routes[failingKey]
		result.queryErr = errW9ASnapshotInjected
		routes[failingKey] = result
		db := openW9ASnapshotDB(&w9aSnapConn{routes: routes})
		_, err := CollectSnapshot(context.Background(), db, TargetTest)
		db.Close()
		if err == nil || !errors.Is(err, errW9ASnapshotInjected) {
			t.Fatalf("查询 %s 失败必须原样传播: %v", failingKey, err)
		}
	}
}

// TestW9ACollectSnapshotScanFailuresInSequence 逐个查询注入 NULL 行，覆盖
// 各 scan 回调的错误分支与 CollectSnapshot 的对应返回。
func TestW9ACollectSnapshotScanFailuresInSequence(t *testing.T) {
	order := w9aQueryOrder()
	for _, failingKey := range order {
		routes := w9aNormalRows()
		result := routes[failingKey]
		nullRow := make([]driver.Value, len(result.columns))
		result.rows = [][]driver.Value{nullRow}
		routes[failingKey] = result
		db := openW9ASnapshotDB(&w9aSnapConn{routes: routes})
		_, err := CollectSnapshot(context.Background(), db, TargetTest)
		db.Close()
		if err == nil {
			t.Fatalf("查询 %s 的 NULL 行必须触发 Scan 失败", failingKey)
		}
	}
}

// TestW9ACollectSnapshotRowsErrInSequence 用 nextErr 覆盖 collectRows /
// collectOneRow 的 rows.Err 分支与 found=false 分支。
func TestW9ACollectSnapshotRowsErrInSequence(t *testing.T) {
	order := w9aQueryOrder()
	for _, failingKey := range order {
		routes := w9aNormalRows()
		result := routes[failingKey]
		result.nextErr = errW9ASnapshotInjected
		result.rows = nil
		routes[failingKey] = result
		db := openW9ASnapshotDB(&w9aSnapConn{routes: routes})
		_, err := CollectSnapshot(context.Background(), db, TargetTest)
		db.Close()
		if err == nil {
			t.Fatalf("查询 %s 的 rows.Err 必须传播", failingKey)
		}
	}
}

// TestW9ASnapshotStableJSONErrorBranches 覆盖 stableNormalize 的递归错误分支。
func TestW9ASnapshotStableJSONErrorBranches(t *testing.T) {
	// 嵌套在 map/slice 里的不支持类型必须向上传播错误。
	if _, err := StableJSON(map[string]any{"nested": make(chan int)}); err == nil {
		t.Fatal("map 内不支持类型必须报错")
	}
	if _, err := StableJSON([]any{make(chan int)}); err == nil {
		t.Fatal("slice 内不支持类型必须报错")
	}
	if _, err := StableJSON(map[int]string{1: "x"}); err == nil {
		t.Fatal("非 string map key 必须报错")
	}
	if _, err := StableJSON([][]byte{{1}}); err == nil {
		t.Fatal("嵌套 []byte 必须报错")
	}
	// nullableDigest 与 DigestDefinition 的边界。
	if nullableDigest(nil) != nil {
		t.Fatal("nil 的 digest 必须是 nil")
	}
	if nullableDigest("x") == nil || *nullableDigest("x") != DigestDefinition("x") {
		t.Fatal("非 nil 的 digest 必须与 DigestDefinition 一致")
	}
	if DigestDefinition(nil) != DigestDefinition("") {
		t.Fatal("nil 与空串的 digest 必须一致")
	}
	if formatCapturedAt(timeNowW9A()) == "" {
		t.Fatal("capturedAt 不能为空")
	}
}

// TestW9AWriteStableStringEscapes 锁定 JSON.stringify 的转义规则。
func TestW9AWriteStableStringEscapes(t *testing.T) {
	encoded, err := StableJSON("quote\" back\\ nl\n cr\r tab\t bs\b ff\f ctl\x01 unicode ✓")
	if err != nil {
		t.Fatalf("StableJSON(string): %v", err)
	}
	text := string(encoded)
	for _, fragment := range []string{`\"`, `\\`, `\n`, `\r`, `\t`, `\b`, `\f`, `\u0001`, `✓`} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("转义输出缺少 %q: %s", fragment, text)
		}
	}
}

func timeNowW9A() time.Time {
	return time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
}
