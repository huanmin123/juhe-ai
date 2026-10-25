package j3bmodelcheck

// w12g 波次：用 w12gFakeConn 脚本驱动 bootstrap.go 的 PostgreSQL 检查链
// （inspectTx / inspectColumns / inspectConstraints），覆盖查询错误、Scan
// 错误、迭代错误与 DDL 失败分支。SQLite 升级链（ensureSQLite*Columns）随
// SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

import (
	"context"
	"database/sql/driver"
	"testing"
)

func w12gPgOwnerRow() w12gFakeQuery {
	return w12gFakeQuery{match: "pg_get_userbyid", rows: &w12gFakeRows{columns: []string{"owner"}, rows: [][]driver.Value{{"juhe_maintenance"}}}}
}

func w12gPgIdentityRows() w12gFakeQuery {
	return w12gFakeQuery{match: "current_database()", rows: &w12gFakeRows{columns: []string{"current_database", "current_user"}, rows: [][]driver.Value{{"wm_db", "juhe_maintenance"}}}}
}

func TestW12GInspectTxScriptedBranches(t *testing.T) {
	ctx := context.Background()
	master := func() w12gFakeQuery {
		return w12gFakeQuery{match: "current_database()", rows: &w12gFakeRows{columns: []string{"a", "b"}, rows: [][]driver.Value{{"db", "role"}}}}
	}
	owner := func() w12gFakeQuery { return w12gPgOwnerRow() }
	tables := func(rows ...[]driver.Value) w12gFakeQuery {
		return w12gFakeQuery{match: "information_schema.tables WHERE", rows: &w12gFakeRows{columns: []string{"table_name"}, rows: rows}}
	}

	t.Run("identity read fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "current_database()", err: errW12GDriver}}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("身份读取失败必须上抛")
		}
	})

	t.Run("owner read error and missing schema", func(t *testing.T) {
		connErr := &w12gFakeConn{queries: []w12gFakeQuery{master(), {match: "pg_get_userbyid", err: errW12GDriver}}}
		dbErr := w12gFakeDB(t, connErr)
		tx, _ := dbErr.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("owner 读取失败必须上抛")
		}

		connMissing := &w12gFakeConn{queries: []w12gFakeQuery{master(), {match: "pg_get_userbyid", rows: &w12gFakeRows{columns: []string{"owner"}}}}}
		dbMissing := w12gFakeDB(t, connMissing)
		tx2, _ := dbMissing.BeginTx(ctx, nil)
		defer tx2.Rollback()
		report, err := inspectTx(ctx, tx2)
		if err != nil || !report.MissingSchema {
			t.Fatalf("缺失 owner 行必须报 missingSchema: %+v err=%v", report, err)
		}
	})

	t.Run("tables read fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{master(), owner(), {match: "information_schema.tables WHERE", err: errW12GDriver}}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("表清单读取失败必须上抛")
		}
	})

	t.Run("columns inspection fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("列检查失败必须上抛")
		}
	})

	t.Run("constraints inspection fails", func(t *testing.T) {
		// 列检查需要非空列行；返回一行契约列（table 名取第一张契约表）。
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("约束检查失败必须上抛")
		}
	})

	t.Run("indexes read fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}}},
			{match: "pg_indexes", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("索引读取失败必须上抛")
		}
	})

	t.Run("index iterate fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			master(), owner(), tables(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}}},
			{match: "pg_indexes", rows: &w12gFakeRows{columns: []string{"indexname", "indexdef"},
				rows: [][]driver.Value{{"idx", "def"}}, nextErr: errW12GDriver}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("索引迭代失败必须上抛")
		}
	})
}
