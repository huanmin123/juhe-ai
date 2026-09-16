package j3bmodelcheck

// w12g 波次：用 w12gFakeConn 脚本直调 PostgreSQL backfill/readback 的
// helper，覆盖查询错误、迭代/关闭错误、上限越界、冲突与插入失败分支。
// 注意：PG 目录查询的 Scan 目标全部为 string/NullString，driver.Value 均
// 可转换，Scan 错误分支不可达（与 businesshandoff 同一结论）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func w12gBeginTx(t *testing.T, conn *w12gFakeConn) (context.Context, *sql.Tx) {
	t.Helper()
	db := w12gFakeDB(t, conn)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return context.Background(), tx
}

func TestW12GPostgresTableHelpersScripted(t *testing.T) {
	ctx := context.Background()

	t.Run("table exists error", func(t *testing.T) {
		_, tx := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.tables", err: errW12GDriver}}})
		if _, err := postgresTableExists(ctx, tx, "s", "t"); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatalf("表存在性错误必须被包装: %v", err)
		}
	})

	t.Run("backfill columns errors", func(t *testing.T) {
		_, tx := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", err: errW12GDriver}}})
		if _, err := postgresBackfillColumns(ctx, tx, "s", "t"); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL columns") {
			t.Fatalf("列读取错误必须被包装: %v", err)
		}
		_, txIter := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", rows: w12gRowsIterErrTableInfo()}}})
		if _, err := postgresBackfillColumns(ctx, txIter, "s", "t"); err == nil || !strings.Contains(err.Error(), "iterate J3b PostgreSQL columns") {
			t.Fatalf("列迭代错误必须被包装: %v", err)
		}
		_, txEmpty := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"c"}}}}})
		if _, err := postgresBackfillColumns(ctx, txEmpty, "s", "t"); err == nil || !strings.Contains(err.Error(), "has no columns") {
			t.Fatalf("无列必须拒绝: %v", err)
		}
	})

	t.Run("primary keys errors", func(t *testing.T) {
		_, tx := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "pg_index", err: errW12GDriver}}})
		if _, err := postgresPrimaryKeys(ctx, tx, "s", "t"); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL primary key") {
			t.Fatalf("主键读取错误必须被包装: %v", err)
		}
		_, txIter := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "pg_index", rows: &w12gFakeRows{columns: []string{"attname"}, rows: [][]driver.Value{{"id"}}, nextErr: errW12GDriver}}}})
		if _, err := postgresPrimaryKeys(ctx, txIter, "s", "t"); err == nil {
			t.Fatal("主键迭代错误必须上抛")
		}
	})

	t.Run("readback columns errors", func(t *testing.T) {
		_, tx := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", err: errW12GDriver}}})
		if _, err := postgresColumns(ctx, tx, "s", "t"); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL columns") {
			t.Fatalf("readback 列读取错误必须被包装: %v", err)
		}
		_, txIter := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"column_name"}, rows: [][]driver.Value{{"id"}}, nextErr: errW12GDriver}}}})
		if _, err := postgresColumns(ctx, txIter, "s", "t"); err == nil {
			t.Fatal("readback 列迭代错误必须上抛")
		}
		_, txEmpty := w12gBeginTx(t, &w12gFakeConn{queries: []w12gFakeQuery{{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"column_name"}}}}})
		if _, err := postgresColumns(ctx, txEmpty, "s", "t"); err == nil || !strings.Contains(err.Error(), "has no columns") {
			t.Fatalf("readback 无列必须拒绝: %v", err)
		}
	})
}
