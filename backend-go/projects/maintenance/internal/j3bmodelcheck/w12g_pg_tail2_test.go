package j3bmodelcheck

// w12g 波次：收尾冲刺——VerifyPostgresBackfill 的 SHOW/inspect/存在性/主键
// /证据错误传播、trust 越界分支，以及 Run check 模式的 commit 错误。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestW12GVerifyPostgresBackfillTailBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("show transaction read only fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "SHOW transaction_read_only", err: errW12GDriver}}}
		if _, err := VerifyPostgresBackfill(ctx, w12gFakeDB(t, conn), PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL readback transaction mode") {
			t.Fatalf("SHOW 失败必须被包装: %v", err)
		}
	})

	t.Run("inspection fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "SHOW transaction_read_only", rows: &w12gFakeRows{columns: []string{"transaction_read_only"}, rows: [][]driver.Value{{"on"}}}},
			{match: "current_database()", err: errW12GDriver},
		}}
		if _, err := VerifyPostgresBackfill(ctx, w12gFakeDB(t, conn), PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL readback target schema") {
			t.Fatalf("inspect 失败必须被包装: %v", err)
		}
	})

	t.Run("source existence probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatalf("源存在性失败必须传播: %v", err)
		}
	})

	t.Run("target primary key probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		// 逐次注入无法区分两次主键查询；用 catalog 删除 target 表主键使
		// targetKeys 为空，覆盖主键长度不匹配分支。
		for _, table := range catalog.schemas[SchemaName] {
			table.primaryKeys = nil
		}
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "primary key projection mismatch" {
			t.Fatalf("主键缺失必须报告 mismatch: %+v", report.Tables)
		}
	})

	t.Run("source evidence fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "to_jsonb")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL table") {
			t.Fatalf("证据读取失败必须传播: %v", err)
		}
	})
}
