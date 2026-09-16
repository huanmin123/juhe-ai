package j3bmodelcheck

// w12g 波次：针对当前 inspectTx 实现的表清单迭代、inspectColumns /
// inspectConstraints 的错误分支与索引校验错误的最后补齐。

import (
	"context"
	"database/sql/driver"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func w12gAllTableRows() w12gFakeQuery {
	rows := make([][]driver.Value, 0, len(contracts.J3BModelCheckTables))
	for _, name := range contracts.J3BModelCheckTables {
		rows = append(rows, []driver.Value{name})
	}
	return w12gFakeQuery{match: "information_schema.tables WHERE", rows: &w12gFakeRows{columns: []string{"table_name"}, rows: rows}}
}

func TestW12GInspectTxIterateAndCloseErrors(t *testing.T) {
	ctx := context.Background()
	identity := func() w12gFakeQuery {
		return w12gFakeQuery{match: "current_database()", rows: &w12gFakeRows{columns: []string{"db", "user"}, rows: [][]driver.Value{{"db", "role"}}}}
	}
	owner := func() w12gFakeQuery { return w12gPgOwnerRow() }

	t.Run("tables iterate error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(),
			{match: "information_schema.tables WHERE", rows: &w12gFakeRows{columns: []string{"table_name"}, rows: [][]driver.Value{{"model_check_runs"}}, nextErr: errW12GDriver}},
		}}
		db := w12gFakeDB(t, conn)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("表清单迭代失败必须上抛")
		}
	})

	t.Run("columns query and iterate errors", func(t *testing.T) {
		connQuery := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, connQuery)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("列查询失败必须上抛")
		}

		connIter := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}, rows: [][]driver.Value{{"model_check_runs", "id", "text", "text", "NO"}}, nextErr: errW12GDriver}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}}},
			{match: "pg_indexes", rows: &w12gFakeRows{columns: []string{"indexname", "indexdef"}}},
		}}
		db2 := w12gFakeDB(t, connIter)
		tx2, _ := db2.BeginTx(ctx, nil)
		defer tx2.Rollback()
		if _, err := inspectTx(ctx, tx2); err == nil {
			t.Fatal("列迭代失败必须上抛")
		}
	})

	t.Run("constraints query and iterate errors", func(t *testing.T) {
		connQuery := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, connQuery)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("约束查询失败必须上抛")
		}

		connIter := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}, rows: [][]driver.Value{{"model_check_runs", "primary key (id)"}}, nextErr: errW12GDriver}},
			{match: "pg_indexes", rows: &w12gFakeRows{columns: []string{"indexname", "indexdef"}}},
		}}
		db2 := w12gFakeDB(t, connIter)
		tx2, _ := db2.BeginTx(ctx, nil)
		defer tx2.Rollback()
		if _, err := inspectTx(ctx, tx2); err == nil {
			t.Fatal("约束迭代失败必须上抛")
		}
	})

	t.Run("indexes query and iterate errors", func(t *testing.T) {
		connQuery := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}}},
			{match: "pg_indexes", err: errW12GDriver},
		}}
		db := w12gFakeDB(t, connQuery)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := inspectTx(ctx, tx); err == nil {
			t.Fatal("索引查询失败必须上抛")
		}

		connIter := &w12gFakeConn{queries: []w12gFakeQuery{
			identity(), owner(), w12gAllTableRows(),
			{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"t", "c", "d", "u", "n"}}},
			{match: "pg_constraint", rows: &w12gFakeRows{columns: []string{"t", "d"}}},
			{match: "pg_indexes", rows: &w12gFakeRows{columns: []string{"indexname", "indexdef"}, rows: [][]driver.Value{{"idx", "def"}}, nextErr: errW12GDriver}},
		}}
		db2 := w12gFakeDB(t, connIter)
		tx2, _ := db2.BeginTx(ctx, nil)
		defer tx2.Rollback()
		if _, err := inspectTx(ctx, tx2); err == nil {
			t.Fatal("索引迭代失败必须上抛")
		}
	})
}

func TestW12GUnknownDispositionEvidencePresent(t *testing.T) {
	// 未知 disposition 且证据齐备 → default 分支 "coverage disposition missing"。
	inventory := []LegacyJ3bFact{{
		Name: "model_paired_similarity_windows", SourceSchema: "juhe_stats", SourceTable: "model_paired_similarity_windows",
		Disposition: "w12g-unknown-disposition",
	}}
	evidence := map[string]LegacyJ3bFactEvidence{
		"model_paired_similarity_windows": {SourceSchema: "juhe_stats", SourceTable: "model_paired_similarity_windows", Digest: w12gDigest("x")},
	}
	report := ValidateLegacyJ3bFactCoverage(inventory, evidence)
	if report.Facts["model_paired_similarity_windows"] != "coverage disposition missing" {
		t.Fatalf("未知 disposition 且证据齐备必须报告 disposition missing: %+v", report.Facts)
	}
}
