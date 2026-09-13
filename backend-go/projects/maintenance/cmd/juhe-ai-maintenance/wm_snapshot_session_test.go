package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schemasnapshot"
)

// 本文件覆盖 schema snapshot 的会话层：openSnapshotDB 的方言拒绝、
// writeSnapshotJSON 的 Node 兼容输出、以及 runSnapshotSession 在只读事务中
// 完整采集与提交的闭环。pg catalog 由路由式假驱动应答（复刻 Node 工具的
// 查询清单），真实 PostgreSQL 不在本测试范围。

type wmSnapshotConn struct{ routes map[string]wmSnapshotResult }

type wmSnapshotResult struct {
	columns []string
	rows    [][]driver.Value
}

func (c *wmSnapshotConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm snapshot fake: Prepare 不应被调用")
}

func (c *wmSnapshotConn) Close() error { return nil }

func (c *wmSnapshotConn) Begin() (driver.Tx, error) {
	return nil, errors.New("wm snapshot fake: Begin 不应被调用（走 BeginTx）")
}

type wmSnapshotTx struct{}

func (wmSnapshotTx) Commit() error   { return nil }
func (wmSnapshotTx) Rollback() error { return nil }

func (c *wmSnapshotConn) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return &wmSnapshotTx{}, nil
}

func (c *wmSnapshotConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "set_config(") {
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("wm snapshot fake: unexpected exec %.60s", query)
}

func (c *wmSnapshotConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	for key, result := range c.routes {
		if strings.Contains(query, key) {
			return &wmSnapshotRows{columns: result.columns, rows: result.rows}, nil
		}
	}
	return nil, fmt.Errorf("wm snapshot fake: unexpected query %.80s", query)
}

type wmSnapshotRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
}

func (r *wmSnapshotRows) Columns() []string { return r.columns }
func (r *wmSnapshotRows) Close() error      { return nil }
func (r *wmSnapshotRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type wmSnapshotConnector struct{ conn *wmSnapshotConn }

func (c wmSnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}

func (c wmSnapshotConnector) Driver() driver.Driver { return wmSnapshotDriver{} }

type wmSnapshotDriver struct{}

func (wmSnapshotDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm snapshot fake: 请使用 sql.OpenDB")
}

// wmSnapshotRoutes 复刻 Node postgres-schema-snapshot 的完整 catalog 查询族。
func wmSnapshotRoutes() map[string]wmSnapshotResult {
	return map[string]wmSnapshotResult{
		"pg_get_userbyid(n.nspowner)": {
			columns: []string{"name", "owner", "acl"},
			rows:    [][]driver.Value{{"public", "postgres", nil}, {"juhe_business", "juhe_app", "wm-acl"}},
		},
		"current_database()": {
			columns: []string{"name", "oid", "serverAddress", "serverPort"},
			rows:    [][]driver.Value{{"wmdb", "16384", "10.0.0.9", int64(5432)}},
		},
		"pg_roles": {
			columns: []string{"name", "superuser", "createRole", "createDb", "canLogin", "replication", "bypassRls"},
			rows:    [][]driver.Value{{"juhe_app", false, true, false, true, false, false}},
		},
		"pg_extension": {
			columns: []string{"name", "version", "schema"},
			rows:    [][]driver.Value{{"plpgsql", "1.0", nil}},
		},
		"c.relacl": {
			columns: []string{"schema", "name", "kind", "owner", "persistence", "acl"},
			rows:    [][]driver.Value{{"public", "api_keys", "r", "postgres", "p", nil}},
		},
		"pg_attribute": {
			columns: []string{"schema", "relation", "name", "ordinal", "type", "udt", "nullable", "default_definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "id", int64(1), "bigint", "int8", false, nil}},
		},
		"pg_constraint": {
			columns: []string{"schema", "relation", "name", "type", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "api_keys_pkey", "p", "PRIMARY KEY (id)"}},
		},
		"pg_index": {
			columns: []string{"schema", "relation", "name", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "api_keys_pkey", "CREATE UNIQUE INDEX api_keys_pkey ON public.api_keys USING btree (id)"}},
		},
		"pg_proc": {
			columns: []string{"schema", "name", "identityArguments", "definition"},
			rows:    [][]driver.Value{{"public", "touch_ts", "()", "CREATE FUNCTION public.touch_ts()"}},
		},
		"pg_trigger": {
			columns: []string{"schema", "relation", "name", "definition"},
			rows:    [][]driver.Value{{"public", "api_keys", "touch_ts_trigger", "CREATE TRIGGER touch_ts_trigger"}},
		},
		"pg_get_viewdef": {
			columns: []string{"schema", "name", "materialized", "definition"},
			rows:    [][]driver.Value{{"public", "active_keys", false, "SELECT id FROM public.api_keys"}},
		},
		"pg_inherits": {
			columns: []string{"schema", "relation", "parentSchema", "parentRelation"},
			rows:    [][]driver.Value{{"juhe_business", "usage_2026_09", "juhe_business", "usage_daily"}},
		},
		"relkind='S'": {
			columns: []string{"schema", "name", "owner"},
			rows:    [][]driver.Value{{"public", "api_keys_id_seq", "postgres"}},
		},
	}
}

func TestWMRunSnapshotSessionCollectsAndPrints(t *testing.T) {
	db := sql.OpenDB(wmSnapshotConnector{conn: &wmSnapshotConn{routes: wmSnapshotRoutes()}})
	defer db.Close()
	output := wmCaptureStdout(t, func() {
		if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err != nil {
			t.Errorf("runSnapshotSession: %v", err)
		}
	})
	var snapshot schemasnapshot.SchemaSnapshot
	if err := json.Unmarshal([]byte(output), &snapshot); err != nil {
		t.Fatalf("快照输出必须是 JSON: %v\n%s", err, output)
	}
	if snapshot.Target != schemasnapshot.TargetTest || snapshot.SchemaVersion != 1 || snapshot.Digest == "" {
		t.Fatalf("快照元数据不完整: target=%s version=%d digest=%q", snapshot.Target, snapshot.SchemaVersion, snapshot.Digest)
	}
	if snapshot.Database.Name != "wmdb" || snapshot.Database.ServerAddress == nil || *snapshot.Database.ServerAddress != "10.0.0.9" {
		t.Fatalf("数据库身份必须来自目录: %+v", snapshot.Database)
	}
	if len(snapshot.Schemas) != 2 || len(snapshot.Relations) != 1 || len(snapshot.Partitions) != 1 {
		t.Fatalf("快照对象集合异常: %+v", snapshot)
	}
	// 缩进 + 末尾换行是 Node JSON.stringify(output, null, 2) 的输出契约。
	if !strings.Contains(output, "\n  \"schemaVersion\": 1") || !strings.HasSuffix(output, "\n") {
		t.Fatalf("输出必须是缩进 JSON 且以换行结尾: %.80s", output)
	}
}

func TestWMRunSnapshotSessionPropagatesCatalogError(t *testing.T) {
	// 缺少 pg_roles 路由：采集必须以错误失败（会话层不上抛 os.Exit）。
	routes := wmSnapshotRoutes()
	delete(routes, "pg_roles")
	db := sql.OpenDB(wmSnapshotConnector{conn: &wmSnapshotConn{routes: routes}})
	defer db.Close()
	if err := runSnapshotSession(context.Background(), db, schemasnapshot.TargetTest); err == nil {
		t.Fatal("目录查询失败必须上抛")
	}
}

func TestWMOpenSnapshotDBRejectsNonPostgresDialects(t *testing.T) {
	for _, rawURL := range []string{"", "sqlite:///x.db", "mysql://u@h/db", "http://h/db"} {
		if _, err := openSnapshotDB(rawURL, schemasnapshot.TargetTest); err == nil {
			t.Fatalf("方言 %q 必须被拒绝", rawURL)
		}
	}
	db, err := openSnapshotDB("postgres://snapshot@127.0.0.1:5432/wmdb", schemasnapshot.TargetProduction)
	if err != nil {
		t.Fatalf("合法 postgres URL 必须通过（懒连接）: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestWMWriteSnapshotJSONKeepsNodeContract(t *testing.T) {
	output := wmCaptureStdout(t, func() {
		if err := writeSnapshotJSON(schemasnapshot.SchemaSnapshot{
			SchemaVersion: 1, Target: schemasnapshot.TargetTest,
			Schemas: []schemasnapshot.SchemaEntry{{Name: "public&<x>", Owner: "postgres"}},
		}); err != nil {
			t.Errorf("writeSnapshotJSON: %v", err)
		}
	})
	// HTML 转义关闭：<, >, & 保持字面量（对齐 JavaScript JSON.stringify）。
	if !strings.Contains(output, `"public&<x>"`) {
		t.Fatalf("输出必须保留字面量字符: %q", output)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("输出必须是合法 JSON: %v", err)
	}
}
