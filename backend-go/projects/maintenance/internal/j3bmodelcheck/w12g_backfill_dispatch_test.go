package j3bmodelcheck

// w12g 波次：backfill.go 与 postgres_backfill.go 的最后一层可达分支。
// VerifySQLiteBackfill / BackfillSQLite 的 helper 失败传播通过 mock dispatch
// 覆盖；copySQLiteTable 行级分支与 BackfillPostgres 的事务错误用 fake 脚本。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func TestW12GVerifySQLiteBackfillMockDispatch(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)

	t.Run("no common columns fails closed", func(t *testing.T) {
		// source 与 target 交替返回不相交的单列集合。
		calls := 0
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			calls++
			if calls%2 == 1 {
				return []string{"w12g_source_only"}, nil
			}
			return []string{"w12g_target_only"}, nil
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "has no common columns") {
			t.Fatalf("无公共列必须失败: %v", err)
		}
	})

	t.Run("target digest failure", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			calls++
			if calls%2 == 0 {
				return "", errW12GMock
			}
			return sqliteTableDigestColumnsImpl(ctx, db, table, columns)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("target digest 失败必须传播")
		}
	})

	t.Run("target row count failure", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &tableRowCount, func(ctx context.Context, db *sql.DB, table string) (int64, error) {
			calls++
			if calls%2 == 0 {
				return 0, errW12GMock
			}
			return tableRowCountImpl(ctx, db, table)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("target 行数失败必须传播")
		}
	})

	t.Run("stats existence failure", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &sqliteTableExists, func(ctx context.Context, db *sql.DB, table string) (bool, error) {
			calls++
			if table == "stats_job_state" {
				return false, errW12GMock
			}
			return sqliteTableExistsImpl(ctx, db, table)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("stats_job_state 探测失败必须传播")
		}
	})

	t.Run("target trust evidence failure", func(t *testing.T) {
		w12gSwap(t, &sqliteTrustAggregationStateEvidence, func(ctx context.Context, db *sql.DB, source bool) (int64, string, error) {
			if !source {
				return 0, "", errW12GMock
			}
			return sqliteTrustAggregationStateEvidenceImpl(ctx, db, source)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("target trust evidence 失败必须传播")
		}
	})
}

func TestW12GBackfillSQLiteMockDispatch(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()

	t.Run("dataset query_only verify fails", func(t *testing.T) {
		w12gSwap(t, &verifyQueryOnly, func(ctx context.Context, db *sql.DB) (bool, error) {
			return false, errW12GMock
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "verify J3b legacy dataset query_only") {
			t.Fatalf("dataset 只读校验失败必须被包装: %v", err)
		}
	})

	t.Run("dataset not query only fails", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &verifyQueryOnly, func(ctx context.Context, db *sql.DB) (bool, error) {
			calls++
			if calls == 1 {
				return false, nil
			}
			return verifyQueryOnlyImpl(ctx, db)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "dataset must be query_only") {
			t.Fatalf("dataset 非只读必须拒绝: %v", err)
		}
	})

	t.Run("stats not query only fails", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &verifyQueryOnly, func(ctx context.Context, db *sql.DB) (bool, error) {
			calls++
			if calls == 2 {
				return false, nil
			}
			return verifyQueryOnlyImpl(ctx, db)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "stats must be query_only") {
			t.Fatalf("stats 非只读必须拒绝: %v", err)
		}
	})

	t.Run("digest loop source columns failure", func(t *testing.T) {
		// 同一 stats 表的首次列查询（copy 阶段）成功；摘要循环里 dataset 缺表
		// 后的 stats 回退再次失败 → wrap "digest J3b source table"。
		calls := map[string]int{}
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			calls[table]++
			if table == "account_quality_health_hourly" && calls[table] >= 2 {
				return nil, errW12GMock
			}
			return sqliteColumnsImpl(ctx, db, table)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "digest J3b source table") {
			t.Fatalf("摘要源列失败必须被包装: %v", err)
		}
	})

	t.Run("digest loop target digest failure", func(t *testing.T) {
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			if table == "account_quality_health_hourly" {
				return "", errW12GMock
			}
			return sqliteTableDigestColumnsImpl(ctx, db, table, columns)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "digest J3b target table") {
			t.Fatalf("摘要 target digest 失败必须被包装: %v", err)
		}
	})

	t.Run("trust evidence failure at end", func(t *testing.T) {
		w12gSwap(t, &sqliteTrustAggregationStateEvidence, func(ctx context.Context, db *sql.DB, source bool) (int64, string, error) {
			if !source {
				return 0, "", errW12GMock
			}
			return sqliteTrustAggregationStateEvidenceImpl(ctx, db, source)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil {
			t.Fatal("尾部 trust evidence 失败必须传播")
		}
	})
}

func TestW12GCopySQLiteTableTxScripted(t *testing.T) {
	ctx := context.Background()

	t.Run("scan and iterate and insert failures", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"r1"}}, nextErr: errW12GDriver}},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: `FROM "t" WHERE`, rows: &w12gFakeRows{columns: []string{"id"}}},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil {
			t.Fatal("迭代错误必须上抛")
		}
	})

	t.Run("existing check fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"r1"}}}},
		}})
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"c"}},
			queries: []w12gFakeQuery{
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: `FROM "t" WHERE`, err: errW12GDriver},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "check existing") {
			t.Fatalf("existing 检查失败必须被包装: %v", err)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"r1"}}}},
		}})
		conn := &w12gFakeConn{
			execErr:     errW12GDriver,
			defaultRows: &w12gFakeRows{columns: []string{"c"}},
			queries: []w12gFakeQuery{
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: `FROM "t" WHERE`, rows: &w12gFakeRows{columns: []string{"id"}}},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "insert J3b backfill table") {
			t.Fatalf("插入失败必须被包装: %v", err)
		}
	})

	t.Run("prepare fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{"r1"}}}},
		}})
		conn := &w12gFakeConn{
			prepareErr:  true,
			defaultRows: &w12gFakeRows{columns: []string{"c"}},
			queries: []w12gFakeQuery{
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
				{match: `FROM "t" WHERE`, rows: &w12gFakeRows{columns: []string{"id"}}},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "prepare J3b backfill table") {
			t.Fatalf("prepare 失败必须被包装: %v", err)
		}
	})
}

func TestW12GBackfillPostgresBeginAndCommit(t *testing.T) {
	ctx := context.Background()

	t.Run("begin fails", func(t *testing.T) {
		conn := &w12gFakeConn{beginErr: true}
		if _, err := BackfillPostgres(ctx, w12gFakeDB(t, conn), PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "begin J3b PostgreSQL backfill") {
			t.Fatalf("begin 失败必须被包装: %v", err)
		}
	})

}

func TestW12GBackfillSQLiteStatsVerifyQueryOnlyFails(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()
	calls := 0
	w12gSwap(t, &verifyQueryOnly, func(ctx context.Context, db *sql.DB) (bool, error) {
		calls++
		if calls == 2 {
			return false, errW12GMock
		}
		return verifyQueryOnlyImpl(ctx, db)
	})
	if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "verify J3b legacy stats query_only") {
		t.Fatalf("stats 只读校验失败必须被包装: %v", err)
	}
}
