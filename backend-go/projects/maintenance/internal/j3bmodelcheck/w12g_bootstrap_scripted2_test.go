package j3bmodelcheck

// w12g 波次：Run / RunSQLite / inspectSQLite / inspectTx 内部剩余分支的
// 脚本化覆盖（BeginTx/DDL/commit 失败、Scan/迭代错误、apply 后契约仍不完整）。
// 不可达（w12g 登记）：bootstrap.go:49-51 与 65-67 的 sql.Open 错误分支
// （pgx/sqlite 驱动已注册且连接惰性建立）。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestW12GRunSQLiteRemainingBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db rejected", func(t *testing.T) {
		if _, err := RunSQLite(ctx, nil, false); err == nil || !strings.Contains(err.Error(), "未初始化") {
			t.Fatalf("nil db 必须拒绝: %v", err)
		}
	})

	t.Run("busy timeout fails", func(t *testing.T) {
		conn := &w12gFakeConn{pragmaErr: true}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("PRAGMA 失败必须上抛")
		}
	})

	t.Run("begin tx fails", func(t *testing.T) {
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			beginErr:    true,
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "transaction 失败") {
			t.Fatalf("BeginTx 失败必须被包装: %v", err)
		}
	})

	t.Run("second inspection fails after apply", func(t *testing.T) {
		// 第一轮检查全部缺表 → apply → DDL/升级成功 → 二轮 sqlite_master 失败。
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			queries: []w12gFakeQuery{
				{match: "sqlite_master", err: errW12GDriver},
			},
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil {
			t.Fatal("二次检查失败必须上抛")
		}
	})

	t.Run("apply leaves contract incomplete", func(t *testing.T) {
		// apply 后仍是空目录 → "契约仍不完整"。
		conn := &w12gFakeConn{defaultRows: &w12gFakeRows{columns: []string{"name"}}}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "契约仍不完整") {
			t.Fatalf("apply 后不完整必须失败: %v", err)
		}
	})

	t.Run("observation upgrade failure is wrapped", func(t *testing.T) {
		w12gDebugPop = true
		defer func() { w12gDebugPop = false }()
		// PRAGMA 与主键查询共用 table_info 脚本；空行集让 run 升级全部 ALTER
		// 成功执行，随后 observation 升级的查询注入失败。
		full := w12gTableInfo([]string{"id"}, []string{"id"})
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			queries: []w12gFakeQuery{
				{rows: &w12gFakeRows{columns: []string{"name"}}},
				{match: "table_info", rows: full},
				{match: "table_info", err: errW12GDriver},
			},
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "observation schema 失败") {
			t.Fatalf("observation 升级失败必须被包装: %v", err)
		}
	})

	t.Run("trust upgrade failure is wrapped", func(t *testing.T) {
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			queries: []w12gFakeQuery{
				{rows: &w12gFakeRows{columns: []string{"name"}}},
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, nil)},
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, nil)},
				{match: "table_info", err: errW12GDriver},
			},
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "trust aggregation schema 失败") {
			t.Fatalf("trust 升级失败必须被包装: %v", err)
		}
	})
}

func TestW12GInspectSQLiteRemainingBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("table info scan and iterate errors", func(t *testing.T) {
		// 首个 sqlite_master 找到一张表，其 table_info 先 Scan 失败。
		connScan := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
			{match: "table_info", rows: w12gRowsBadTableInfo()},
		}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, connScan)); err == nil {
			t.Fatal("table_info Scan 失败必须上抛")
		}
		connIter := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
			{match: "table_info", rows: w12gRowsIterErrTableInfo()},
		}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, connIter)); err == nil {
			t.Fatal("table_info 迭代失败必须上抛")
		}
	})

	t.Run("primary key and index errors", func(t *testing.T) {
		// table_info 正常但主键查询失败。
		connPK := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, nil)},
			{match: "table_info", err: errW12GDriver},
		}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, connPK)); err == nil {
			t.Fatal("主键查询失败必须上抛")
		}
		// 索引查询失败：默认响应（缺表）→ 走到 sqliteIndexes 的 index_list 失败。
		connIdx := &w12gFakeConn{queries: []w12gFakeQuery{{match: "index_list", err: errW12GDriver}}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, connIdx)); err == nil {
			t.Fatal("索引查询失败必须上抛")
		}
	})
}

func TestW12GRunPostgresRemainingBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("begin tx fails", func(t *testing.T) {
		conn := &w12gFakeConn{beginErr: true}
		if _, err := Run(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("BeginTx 失败必须上抛")
		}
	})

	t.Run("set local fails", func(t *testing.T) {
		conn := &w12gFakeConn{execErr: errW12GDriver, defaultRows: &w12gFakeRows{columns: []string{"c"}}}
		if _, err := Run(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("SET LOCAL 失败必须上抛")
		}
	})

	t.Run("inspection fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "current_database()", err: errW12GDriver}}}
		if _, err := Run(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("inspectTx 失败必须上抛")
		}
	})

	t.Run("commit fails", func(t *testing.T) {
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"c"}},
			commitErr:   errW12GDriver,
		}
		if _, err := Run(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("commit 失败必须上抛")
		}
	})
}

func TestW12GInspectTxColumnAndConstraintRows(t *testing.T) {
	ctx := context.Background()
	master := func() w12gFakeQuery {
		return w12gFakeQuery{match: "current_database()", rows: &w12gFakeRows{columns: []string{"a", "b"}, rows: [][]driver.Value{{"db", "role"}}}}
	}
	owner := func() w12gFakeQuery { return w12gPgOwnerRow() }
	tables := func() w12gFakeQuery {
		return w12gFakeQuery{match: "information_schema.tables WHERE", rows: &w12gFakeRows{columns: []string{"table_name"}, rows: [][]driver.Value{{"model_check_runs"}}}}
	}

	t.Run("columns scan error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}, rows: [][]driver.Value{{"model_check_runs", "id", true, true, "NO"}}}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("列 Scan 失败必须上抛")
		}
	})

	t.Run("columns iterate error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}, rows: [][]driver.Value{{"model_check_runs", "id", "text", "text", "NO"}}, nextErr: errW12GDriver}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("列迭代失败必须上抛")
		}
	})

	t.Run("constraint scan error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}, rows: [][]driver.Value{{"model_check_runs", true}}}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("约束 Scan 失败必须上抛")
		}
	})

	t.Run("constraint iterate error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}, rows: [][]driver.Value{{"model_check_runs", "PRIMARY KEY (id)"}}, nextErr: errW12GDriver}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("约束迭代失败必须上抛")
		}
	})
}
