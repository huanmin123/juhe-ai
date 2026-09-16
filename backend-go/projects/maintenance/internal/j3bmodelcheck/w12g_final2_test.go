package j3bmodelcheck

// w12g 波次：backfill.go helper 的最后一批脚本化分支（digest 循环回退、
// sqliteColumnsTx、sqlitePrimaryKeys、copySQLiteTable 冲突）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestW12GDigestLoopRemainingBranches(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()

	t.Run("target columns failure in digest loop", func(t *testing.T) {
		calls := map[string]int{}
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			calls[table]++
			// digest 循环的第三次 target 列查询时失败（tableRowCount 计数）。
			if table == "account_quality_health_hourly" && calls[table] == 4 {
				return nil, errW12GMock
			}
			return sqliteColumnsImpl(ctx, db, table)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "digest J3b target columns") {
			t.Fatalf("digest target columns 失败必须被包装: %v", err)
		}
	})

	t.Run("digest loop no common columns", func(t *testing.T) {
		calls := map[string]int{}
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			calls[table]++
			// digest 循环的 stats 回退返回不相交列 → 公共列为空。
			if table == "account_quality_health_hourly" && calls[table] == 2 {
				return []string{"w12g_disjoint"}, nil
			}
			return sqliteColumnsImpl(ctx, db, table)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "has no common columns") {
			t.Fatalf("摘要无公共列必须失败: %v", err)
		}
	})

	t.Run("digest loop digest failure", func(t *testing.T) {
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			if table == "account_quality_health_hourly" {
				return "", errW12GMock
			}
			return sqliteTableDigestColumnsImpl(ctx, db, table, columns)
		})
		w12gSwap(t, &sqliteTableDigestColumns, sqliteTableDigestColumnsImpl)
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			if table == "account_quality_health_hourly" {
				return "", errW12GMock
			}
			return sqliteTableDigestColumnsImpl(ctx, db, table, columns)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "digest J3b target table") {
			t.Fatalf("摘要 digest 失败必须被包装: %v", err)
		}
	})
}

func TestW12GTrustValidationMissingAndExtra(t *testing.T) {
	ctx := context.Background()
	// 缺列：table_info 返回不含必需列的形状。
	connMissing := &w12gFakeConn{queries: []w12gFakeQuery{{match: "table_info", rows: w12gTableInfo([]string{"scope_type"}, nil)}}}
	err := validateSQLiteTrustAggregationStateSourceImpl(ctx, w12gFakeDB(t, connMissing))
	if err == nil || !strings.Contains(err.Error(), "missing source columns") {
		t.Fatalf("缺列必须拒绝: %v", err)
	}
	// 多列：形状完整但存在未映射列。
	connExtra := &w12gFakeConn{queries: []w12gFakeQuery{{match: "table_info", rows: w12gTableInfo(append([]string{
		"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at",
	}, "w12g_extra"), nil)}}}
	err = validateSQLiteTrustAggregationStateSourceImpl(ctx, w12gFakeDB(t, connExtra))
	if err == nil || !strings.Contains(err.Error(), "unmapped source columns") {
		t.Fatalf("未映射列必须拒绝: %v", err)
	}
}

func TestW12GSQLitePrimaryKeysTxScripted(t *testing.T) {
	ctx := context.Background()
	conn := &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", rows: w12gRowsBadTableInfo()},
		{match: "table_info", rows: w12gRowsIterErrTableInfo()},
	}}
	db := w12gFakeDB(t, conn)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := sqlitePrimaryKeys(ctx, tx, "t"); err == nil {
		t.Fatal("tx 主键 Scan 失败必须上抛")
	}
	if _, err := sqlitePrimaryKeys(ctx, tx, "t"); err == nil {
		t.Fatal("tx 主键迭代失败必须上抛")
	}
}

func TestW12GCopySQLiteTableConflictScripted(t *testing.T) {
	ctx := context.Background()
	source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
		{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"r1"}}}},
	}})
	conn := &w12gFakeConn{
		defaultRows: &w12gFakeRows{columns: []string{"c"}},
		queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: `FROM "t" WHERE`, rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"different"}}}},
		},
	}
	_, tx := w12gBeginTx(t, conn)
	if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "backfill conflict in t") {
		t.Fatalf("目标行冲突必须失败: %v", err)
	}
}

func TestW12GSqliteTableEvidenceAgainstSourceScripted(t *testing.T) {
	ctx := context.Background()
	source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", err: errW12GDriver},
	}})
	target := w12gFakeDB(t, &w12gFakeConn{})
	if _, _, err := sqliteTableEvidenceAgainstSource(ctx, target, source, "t"); err == nil {
		t.Fatal("source 列错误必须上抛")
	}
	tSrc := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", rows: w12gTableInfo([]string{"id"}, nil)},
	}})
	tTarget := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", err: errW12GDriver},
	}})
	if _, _, err := sqliteTableEvidenceAgainstSource(ctx, tTarget, tSrc, "t"); err == nil {
		t.Fatal("target 列错误必须上抛")
	}
}
