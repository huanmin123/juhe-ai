package j3bmodelcheck

// w12g 波次：通过替换 backfill.go 的包级 helper 变量（Mock 边界），覆盖
// VerifySQLiteBackfill / BackfillSQLite 高层流程的全部错误传播分支，以及
// copySQLiteTable / copySQLiteTrustAggregationState 的行级错误分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

var errW12GMock = errors.New("w12g helper 注入失败")

// w12gSwap 临时替换一个包级 helper 变量并注册还原。
func w12gSwap[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	original := *target
	*target = replacement
	t.Cleanup(func() { *target = original })
}

func w12gReadyTarget(t *testing.T) *sql.DB {
	t.Helper()
	return wmOpenTempJ3b(t, filepath.Join(t.TempDir(), "target.db"))
}

func TestW12GVerifySQLiteBackfillHelperFailures(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)

	t.Run("dataset columns failure", func(t *testing.T) {
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			return nil, errW12GMock
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), errW12GMock.Error()) {
			t.Fatalf("columns 失败必须传播: %v", err)
		}
	})

	t.Run("target columns failure", func(t *testing.T) {
		calls := 0
		w12gSwap(t, &sqliteColumns, func(ctx context.Context, db *sql.DB, table string) ([]string, error) {
			calls++
			if calls%2 == 0 {
				return nil, errW12GMock
			}
			return sqliteColumnsImpl(ctx, db, table)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("target columns 失败必须传播")
		}
	})

	t.Run("digest and row count failures", func(t *testing.T) {
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			return "", errW12GMock
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("digest 失败必须传播")
		}
		w12gSwap(t, &sqliteTableDigestColumns, sqliteTableDigestColumnsImpl)
		w12gSwap(t, &tableRowCount, func(ctx context.Context, db *sql.DB, table string) (int64, error) {
			return 0, errW12GMock
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("行数失败必须传播")
		}
	})

	t.Run("optional target count failure", func(t *testing.T) {
		// dataset 删除 optional 表，使 optional 分支的 target count 被调用。
		w12gSwap(t, &tableRowCount, func(ctx context.Context, db *sql.DB, table string) (int64, error) {
			if table == "model_check_input_versions" {
				return 0, errW12GMock
			}
			return tableRowCountImpl(ctx, db, table)
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("optional 计数失败必须传播")
		}
	})

	t.Run("trust evidence failures", func(t *testing.T) {
		w12gSwap(t, &sqliteTrustAggregationStateEvidence, func(ctx context.Context, db *sql.DB, source bool) (int64, string, error) {
			return 0, "", errW12GMock
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("trust evidence 失败必须传播")
		}
	})
}

func TestW12GBackfillSQLiteHelperFailures(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()

	t.Run("table existence failure", func(t *testing.T) {
		w12gSwap(t, &sqliteTableExists, func(ctx context.Context, db *sql.DB, table string) (bool, error) {
			return false, errW12GMock
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil {
			t.Fatal("表存在性失败必须传播")
		}
	})

	t.Run("copy failure propagates", func(t *testing.T) {
		w12gSwap(t, &copySQLiteTable, func(ctx context.Context, tx *sql.Tx, source *sql.DB, table string) (copyStats, error) {
			return copyStats{}, errW12GMock
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), errW12GMock.Error()) {
			t.Fatalf("copy 失败必须传播: %v", err)
		}
	})

	t.Run("trust state copy failure propagates", func(t *testing.T) {
		w12gSwap(t, &copySQLiteTrustAggregationState, func(ctx context.Context, tx *sql.Tx, source *sql.DB) (copyStats, error) {
			return copyStats{}, errW12GMock
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil {
			t.Fatal("trust copy 失败必须传播")
		}
	})

	t.Run("target digest failure propagates", func(t *testing.T) {
		w12gSwap(t, &sqliteTableDigestColumns, func(ctx context.Context, db *sql.DB, table string, columns []string) (string, error) {
			return "", errW12GMock
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "digest J3b target table") {
			t.Fatalf("target digest 失败必须被包装: %v", err)
		}
	})

	t.Run("trust evidence failure propagates", func(t *testing.T) {
		w12gSwap(t, &sqliteTrustAggregationStateEvidence, func(ctx context.Context, db *sql.DB, source bool) (int64, string, error) {
			if !source {
				return 0, "", errW12GMock
			}
			return sqliteTrustAggregationStateEvidenceImpl(ctx, db, source)
		})
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil {
			t.Fatal("trust evidence 失败必须传播")
		}
	})
}

func TestW12GCopySQLiteTableRowLevelBranches(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()

	t.Run("scan and iterate failures via fake source", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, []string{"id"})},
			{match: "SELECT", rows: &w12gFakeRows{columns: []string{"id"}, rows: [][]driver.Value{{7}}, nextErr: errW12GDriver}},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// 行数据 7(int) 喂 *string 目标：database/sql 会转换，因此 Scan 成功；
		// 迭代错误在行后触发 rows.Err。
		if _, err := copySQLiteTableImpl(ctx, tx, source, "t"); err == nil {
			t.Fatal("迭代错误必须上抛")
		}
		_ = datasetPath
		_ = statsPath
	})

	t.Run("conflicting durable row fails closed", func(t *testing.T) {
		// dataset 与 target 各有一条同主键、不同 score 的行 → conflict 分支。
		dataset := wmOpenRW(t, datasetPath)
		defer dataset.Close()
		if _, err := dataset.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',90,100,'ok','{}','{}','{}','{}','openai-model-check-v1','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		dataset.Close()
		if _, err := target.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',1,100,'ok','{}','{}','{}','{}','openai-model-check-v1','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "backfill conflict in model_check_runs") {
			t.Fatalf("主键冲突必须失败闭环: %v", err)
		}
	})
}

func TestW12GCopyTrustStateRowLevelBranches(t *testing.T) {
	ctx := context.Background()
	_, targetPath, datasetPath, statsPath := w12gBuildBackfillTree(t)
	target := wmOpenRW(t, targetPath)
	defer target.Close()

	t.Run("iterate error fails closed", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{
				"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at",
			}, nil)},
			{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
				{"2026-08-27T10:00:00Z", "obs-1", "2026-08-27T10:01:00Z", nil, int64(3), "2026-08-27T10:01:00Z"},
			}, nextErr: errW12GDriver}},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationStateImpl(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "iterate") {
			t.Fatalf("游标迭代错误必须被包装: %v", err)
		}
	})

	t.Run("target count failure fails closed", func(t *testing.T) {
		// 真实成功场景已由其余测试覆盖；此处通过 swap 验证包装语义。
		w12gSwap(t, &sqliteTableExists, func(ctx context.Context, db *sql.DB, table string) (bool, error) {
			return table == "stats_job_state", nil
		})
		w12gSwap(t, &validateSQLiteTrustAggregationStateSource, func(ctx context.Context, source *sql.DB) error { return nil })
		w12gSwap(t, &sqliteTrustAggregationStateEvidence, func(ctx context.Context, db *sql.DB, source bool) (int64, string, error) {
			if source {
				return 1, "digest", nil
			}
			return 0, "", errW12GMock
		})
		if _, err := VerifySQLiteBackfill(ctx, targetPath, datasetPath, statsPath); err == nil {
			t.Fatal("target evidence 失败必须传播")
		}
	})
}
