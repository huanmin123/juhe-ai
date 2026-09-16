package j3bmodelcheck

// w12g 波次：最后一层分支补齐。
// 不可达（w12g 登记）：
//   - backfill.go:84-121（路径 stat 之后的 open/query_only 组合，见
//     w12g_sqlite_flows_test.go 头部说明）、242-279 中的连接级错误、
//     320-322/333-335（真实 tx 计数/commit 失败）、616-618（filepath.Abs
//     失败）、674-676（target stat 非 NotExist 错误，Windows 无稳定构造）、
//     423-425（rows.Scan 到 []any 目标恒成功）
//   - bootstrap.go:49-51/65-67（sql.Open 惰性）、514-523 中 inspectTx
//     的 Scan 分支以 string 为目标恒成功

import (
	"context"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- backfill.go 剩余 ----------

func TestW12GCopyTrustStateRowLevelScripted(t *testing.T) {
	ctx := context.Background()

	t.Run("scan target row conflict branch", func(t *testing.T) {
		// existing 行不同值 → conflict。
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"}, nil)},
			{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c"}}}},
		}})
		// target tx：existing 行返回不同 cursor_id → conflict 分支。
		target := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "FROM model_trust_aggregation_state WHERE scope_key=?", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"scope", "other", "other", "other", "other", int64(1), "other"}}}},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationStateImpl(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "backfill conflict in model_trust_aggregation_state") {
			t.Fatalf("游标冲突必须失败: %v", err)
		}
	})

	t.Run("existing check fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"}, nil)},
			{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c"}}}},
		}})
		conn := &w12gFakeConn{
			queries: []w12gFakeQuery{
				{match: "FROM model_trust_aggregation_state WHERE scope_key=?", err: errW12GDriver},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTrustAggregationStateImpl(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "check J3b trust aggregation cursor") {
			t.Fatalf("existing 检查失败必须被包装: %v", err)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"}, nil)},
			{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c"}}}},
		}})
		conn := &w12gFakeConn{
			execErr: errW12GDriver,
			queries: []w12gFakeQuery{
				{match: "FROM model_trust_aggregation_state WHERE scope_key=?", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}}},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTrustAggregationStateImpl(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "insert J3b trust aggregation cursor") {
			t.Fatalf("插入失败必须被包装: %v", err)
		}
	})

	t.Run("count fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"}, nil)},
			{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c"}}}},
		}})
		conn := &w12gFakeConn{
			queries: []w12gFakeQuery{
				{match: "FROM model_trust_aggregation_state WHERE scope_key=?", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}}},
				{match: "COUNT(*)", err: errW12GDriver},
			},
		}
		_, tx := w12gBeginTx(t, conn)
		if _, err := copySQLiteTrustAggregationStateImpl(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "count J3b trust aggregation cursor target") {
			t.Fatalf("计数失败必须被包装: %v", err)
		}
	})
}

func TestW12GTrustStateValidationAndEvidenceScripted(t *testing.T) {
	ctx := context.Background()
	columns := []string{"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"}

	t.Run("validation query error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "table_info", err: errW12GDriver}}}
		if err := validateSQLiteTrustAggregationStateSourceImpl(ctx, w12gFakeDB(t, conn)); err == nil {
			t.Fatal("校验查询失败必须上抛")
		}
	})

	t.Run("evidence read and iterate errors", func(t *testing.T) {
		connRead := &w12gFakeConn{queries: []w12gFakeQuery{{match: "FROM stats_job_state", err: errW12GDriver}}}
		if _, _, err := sqliteTrustAggregationStateEvidenceImpl(ctx, w12gFakeDB(t, connRead), true); err == nil {
			t.Fatal("evidence 读取失败必须上抛")
		}
		connIter := &w12gFakeConn{queries: []w12gFakeQuery{{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c"}}, nextErr: errW12GDriver}}}}
		if _, _, err := sqliteTrustAggregationStateEvidenceImpl(ctx, w12gFakeDB(t, connIter), true); err == nil {
			t.Fatal("evidence 迭代失败必须上抛")
		}
		connNonUnique := &w12gFakeConn{queries: []w12gFakeQuery{{match: "FROM stats_job_state", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "3", "c", "c"}, {"cur2", "c", "c", "c", "3", "c", "c"}}}}}}
		if _, _, err := sqliteTrustAggregationStateEvidenceImpl(ctx, w12gFakeDB(t, connNonUnique), true); err == nil || !strings.Contains(err.Error(), "not unique") {
			t.Fatal("非唯一游标必须失败")
		}
		_ = columns
	})
}

func TestW12GSQLiteColumnsTxScripted(t *testing.T) {
	ctx := context.Background()
	conn := &w12gFakeConn{queries: []w12gFakeQuery{
		{match: "table_info", rows: w12gRowsBadTableInfo()},
		{match: "table_info", rows: w12gRowsIterErrTableInfo()},
		{match: "table_info", rows: w12gTableInfo([]string{}, nil)},
	}}
	db := w12gFakeDB(t, conn)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := sqliteColumnsTx(ctx, tx, "t"); err == nil {
		t.Fatal("tx columns Scan 失败必须上抛")
	}
	if _, err := sqliteColumnsTx(ctx, tx, "t"); err == nil {
		t.Fatal("tx columns 迭代失败必须上抛")
	}
	if _, err := sqliteColumnsTx(ctx, tx, "t"); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("tx 空列必须报告缺失: %v", err)
	}
}

func TestW12GBackfillPathsHardlinkShared(t *testing.T) {
	root := t.TempDir()
	dataset := filepath.Join(root, "dataset.db")
	if err := os.WriteFile(dataset, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "hardlink.db")
	if err := os.Link(dataset, hardlink); err != nil {
		t.Skipf("当前文件系统不支持硬链接: %v", err)
	}
	stats := filepath.Join(root, "stats.db")
	if err := os.WriteFile(stats, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	// target 与 dataset 是同一条物理文件（硬链接）→ SameFile 检查拒绝。
	err := ValidateSQLiteBackfillPaths(hardlink, dataset, stats)
	if err == nil || !strings.Contains(err.Error(), "share a physical file") {
		t.Fatalf("硬链接共享文件必须拒绝: %v", err)
	}
	ok, err := distinctSQLitePaths(hardlink, stats)
	if err != nil || !ok {
		t.Fatalf("独立文件必须通过: ok=%v err=%v", ok, err)
	}
	ok, err = distinctSQLitePaths(dataset, hardlink)
	if err != nil || ok {
		t.Fatalf("硬链接必须判为共享: ok=%v err=%v", ok, err)
	}
}

// ---------- postgres_backfill.go 剩余 ----------

func TestW12GBackfillPostgresTableScriptedPropagation(t *testing.T) {
	ctx := context.Background()
	columnsRows := func() w12gFakeQuery {
		return w12gFakeQuery{match: "ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"id", "text", "text", "NO", nil, "NO"},
			{"score", "integer", "int4", "YES", nil, "NO"},
		}}}
	}
	exists := func() w12gFakeQuery {
		return w12gFakeQuery{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}}
	}
	pks := func() w12gFakeQuery {
		return w12gFakeQuery{match: "pg_index", rows: &w12gFakeRows{columns: []string{"attname"}, rows: [][]driver.Value{{"id"}}}}
	}
	item := postgresBackfillTable{name: "model_check_runs", sourceSchema: "juhe_dataset"}

	t.Run("source existence fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "table_schema=$1 AND table_name=$2", err: errW12GDriver}}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatalf("源存在性失败必须传播: %v", err)
		}
	})

	t.Run("target missing", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(),
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{false}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "target table") {
			t.Fatalf("目标缺失必须拒绝: %v", err)
		}
	})

	t.Run("source columns fail", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			{match: "ORDER BY ordinal_position", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL columns") {
			t.Fatalf("源列失败必须传播: %v", err)
		}
	})

	t.Run("target columns fail", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			columnsRows(),
			{match: "ORDER BY ordinal_position", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil {
			t.Fatal("目标列失败必须传播")
		}
	})

	t.Run("projection unmapped fails", func(t *testing.T) {
		src := w12gFakeQuery{match: "ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{{"ghost", "text", "text", "NO", nil, "NO"}}}}
		conn := &w12gFakeConn{queries: []w12gFakeQuery{exists(), exists(), src, columnsRows()}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column") {
			t.Fatalf("未映射列必须拒绝: %v", err)
		}
	})

	t.Run("primary key mismatch fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			exists(), exists(),
			columnsRows(), columnsRows(),
			pks(),
			{match: "pg_index", rows: &w12gFakeRows{columns: []string{"attname"}, rows: [][]driver.Value{{"other"}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "primary key projection mismatch") {
			t.Fatalf("主键投影不匹配必须拒绝: %v", err)
		}
	})

	t.Run("source missing reported", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{false}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTable(ctx, tx, item, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "legacy source table") {
			t.Fatalf("源缺失必须拒绝: %v", err)
		}
	})
}

func TestW12GBackfillPostgresTrustStateEarlyBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("source exists probe fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "table_schema=$1 AND table_name=$2", err: errW12GDriver}}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatal("源存在性失败必须传播")
		}
	})

	t.Run("target missing", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}},
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{false}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil || !strings.Contains(err.Error(), "target table") {
			t.Fatal("目标缺失必须拒绝")
		}
	})

	t.Run("shape validation fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}},
			{match: "table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{true}}}},
			{match: "ORDER BY ordinal_position", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := backfillPostgresTrustAggregationState(ctx, tx, normalizedPostgresBackfillOptions{maxRows: 1, maxBytes: 1 << 20}); err == nil {
			t.Fatal("形状校验失败必须传播")
		}
	})
}

func TestW12GValidatePostgresColumnShapeBranches(t *testing.T) {
	col := postgresBackfillColumn{DataType: "text", UdtName: "text"}
	actual := map[string]postgresBackfillColumn{"a": col, "b": col}
	expected := map[string]postgresBackfillColumn{"a": col}
	if err := validatePostgresColumnShape(actual, expected, "t"); err == nil || !strings.Contains(err.Error(), "unexpected columns") {
		t.Fatalf("列数不匹配必须拒绝: %v", err)
	}
	badType := map[string]postgresBackfillColumn{"a": {DataType: "integer", UdtName: "int4"}}
	if err := validatePostgresColumnShape(badType, expected, "t"); err == nil || !strings.Contains(err.Error(), "is incompatible") {
		t.Fatalf("列类型不匹配必须拒绝: %v", err)
	}
}

// ---------- bootstrap.go 剩余 ----------

func TestW12GInspectSQLitePrimaryKeyAndIndexErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("primary key query fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{"id"}, nil)},
			{match: "table_info", err: errW12GDriver},
		}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, conn)); err == nil {
			t.Fatal("主键查询失败必须上抛")
		}
	})

	t.Run("index query fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "index_list", err: errW12GDriver}}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, conn)); err == nil {
			t.Fatal("索引查询失败必须上抛")
		}
	})
}

func TestW12GSqliteIndexesScripted(t *testing.T) {
	ctx := context.Background()
	connErr := &w12gFakeConn{queries: []w12gFakeQuery{{match: "index_list", err: errW12GDriver}}}
	if _, err := sqliteIndexes(ctx, w12gFakeDB(t, connErr), "t"); err == nil {
		t.Fatal("index_list 失败必须上抛")
	}
	connScan := &w12gFakeConn{queries: []w12gFakeQuery{{match: "index_list", rows: &w12gFakeRows{columns: []string{"seq", "name", "unique", "origin", "partial"}, rows: [][]driver.Value{{true, "idx", 1, "c", 0}}}}}}
	if _, err := sqliteIndexes(ctx, w12gFakeDB(t, connScan), "t"); err == nil {
		t.Fatal("index_list Scan 失败必须上抛")
	}
	connIter := &w12gFakeConn{queries: []w12gFakeQuery{{match: "index_list", rows: &w12gFakeRows{columns: []string{"seq", "name", "unique", "origin", "partial"}, rows: [][]driver.Value{{int64(0), "idx", int64(1), "c", int64(0)}}, nextErr: errW12GDriver}}}}
	if _, err := sqliteIndexes(ctx, w12gFakeDB(t, connIter), "t"); err == nil {
		t.Fatal("index_list 迭代失败必须上抛")
	}
}
