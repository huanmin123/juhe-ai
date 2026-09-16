package j3bmodelcheck

// w12g 波次：PostgreSQL 链路按子串注入故障（复用 wmPGCatalog.injectedFails）
// 覆盖 BackfillPostgres / VerifyPostgresBackfill / Run 的错误传播；SQLite 侧
// 用 w12gFakeConn 脚本驱动 copySQLiteTable / copySQLiteTrustAggregationState /
// RunSQLite / inspectSQLite 的错误与冲突分支。

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestW12GBackfillPostgresInjectedFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("set local statement timeout fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "SET LOCAL statement_timeout")
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "configure J3b PostgreSQL backfill transaction") {
			t.Fatalf("事务配置失败必须被包装: %v", err)
		}
	})

	t.Run("show read only fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		// SHOW 由 conn 直接处理，注入到 catalog 无法命中；用只读事务守卫替代覆盖。
		catalog.injectedFails = append(catalog.injectedFails, "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'")
		_ = catalog
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil {
			t.Fatal("注入失败必须上抛")
		}
	})

	t.Run("target schema inspection fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "current_database()")
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL backfill target schema") {
			t.Fatalf("target schema 校验失败必须被包装: %v", err)
		}
	})

	t.Run("legacy source table probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2")
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatalf("源表探测失败必须传播: %v", err)
		}
	})

	t.Run("legacy source read fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, `FROM "juhe_dataset"."model_check_runs"`)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL legacy table") {
			t.Fatalf("源表读取失败必须被包装: %v", err)
		}
	})

	t.Run("trust cursor read fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "FROM juhe_stats.stats_job_state WHERE scope_type=$1")
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL trust aggregation cursor") {
			t.Fatalf("游标读取失败必须被包装: %v", err)
		}
	})

	t.Run("max rows exceeded fails", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 2)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 1}); err == nil || !strings.Contains(err.Error(), "exceeds max rows per table") {
			t.Fatalf("超出每表行数上限必须失败: %v", err)
		}
	})

	t.Run("max bytes exceeded fails", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 2)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxBytesPerTable: 1}); err == nil || !strings.Contains(err.Error(), "exceeds max bytes per table") {
			t.Fatalf("超出每表字节上限必须失败: %v", err)
		}
	})

	t.Run("target row check fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, `FROM "juhe_j3b"."model_check_runs" WHERE`)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "check existing J3b PostgreSQL backfill row") {
			t.Fatalf("目标行检查失败必须被包装: %v", err)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "INSERT INTO juhe_j3b.")
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "insert J3b PostgreSQL") {
			t.Fatalf("插入失败必须被包装: %v", err)
		}
	})
}

func TestW12GVerifyPostgresBackfillInjectedFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("bad max rows rejected", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 1)
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: -2}); err == nil || !strings.Contains(err.Error(), "max rows per table must be between") {
			t.Fatalf("非法行数上限必须拒绝: %v", err)
		}
	})

	t.Run("transaction configure fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "SET LOCAL statement_timeout = '30s'")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "configure J3b PostgreSQL readback transaction") {
			t.Fatalf("readback 事务配置失败必须被包装: %v", err)
		}
	})

	t.Run("target schema inspection fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "current_database()")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL readback target schema") {
			t.Fatalf("target schema 校验失败必须被包装: %v", err)
		}
	})

	t.Run("source existence probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL table") {
			t.Fatalf("源表存在性失败必须被包装: %v", err)
		}
	})

	t.Run("source columns probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "ORDER BY ordinal_position")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL columns") {
			t.Fatalf("源列探测失败必须被包装: %v", err)
		}
	})

	t.Run("primary key probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "JOIN unnest(idx.indkey)")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL primary key") {
			t.Fatalf("主键探测失败必须被包装: %v", err)
		}
	})

	t.Run("evidence read fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "to_jsonb")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL") {
			t.Fatalf("证据读取失败必须被包装: %v", err)
		}
	})
}

func TestW12GRunPostgresInjectedFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("advisory lock fails", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		db := openWMFakePG(catalog)
		defer db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "pg_advisory_xact_lock")
		if _, err := Run(ctx, db, true); err == nil {
			t.Fatal("advisory lock 失败必须上抛")
		}
	})

	t.Run("post apply ddl fails", func(t *testing.T) {
		catalog := newWMpgCatalog()
		// schema 已存在且 owner 一致，仅缺表：apply 才会走到 DDL 执行。
		wmContractTargetCatalog(catalog)
		for _, tableName := range contracts.J3BModelCheckTables {
			delete(catalog.schemas[SchemaName], tableName)
		}
		db := openWMFakePG(catalog)
		defer db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "CREATE TABLE IF NOT EXISTS "+SchemaName+".")
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "schema bootstrap 失败") {
			t.Fatalf("DDL 失败必须被包装: %v", err)
		}
	})

	t.Run("owner mismatch fails closed", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		catalog.owners[SchemaName] = "someone-else"
		db := openWMFakePG(catalog)
		defer db.Close()
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "跨角色修改") {
			t.Fatalf("owner 漂移必须拒绝 apply: %v", err)
		}
	})
}

func TestW12GCopySQLiteTableScriptedBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("no compatible columns", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"a", "b"}, nil)},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"x", "y"}, nil)},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTable(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "no compatible columns") {
			t.Fatalf("无兼容列必须拒绝: %v", err)
		}
	})

	t.Run("unmapped source columns fail closed", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"a", "ghost"}, nil)},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"a"}, nil)},
		}})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTable(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "unmapped source columns") {
			t.Fatalf("仅源列必须拒绝: %v", err)
		}
	})

	t.Run("missing primary key", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"a"}, nil)},
			{match: "table_info", rows: w12gTableInfo([]string{"a"}, nil)},
		}})
		tx, err := source.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTable(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "has no primary key") {
			t.Fatalf("无主键必须拒绝: %v", err)
		}
	})

	t.Run("primary key not copyable", func(t *testing.T) {
		info := func() w12gFakeQuery {
			return w12gFakeQuery{match: "table_info", rows: w12gTableInfo([]string{"a", "key"}, []string{"key"})}
		}
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "table_info", rows: w12gTableInfo([]string{"a"}, nil)},
			info(), info(),
		}})
		tx, err := source.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTable(ctx, tx, source, "t"); err == nil || !strings.Contains(err.Error(), "is not copyable") {
			t.Fatalf("主键不可复制必须拒绝: %v", err)
		}
	})
}

func TestW12GCopyTrustAggregationStateScriptedBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("source missing fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}}},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationState(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "stats_job_state is missing") {
			t.Fatalf("游标源表缺失必须拒绝: %v", err)
		}
	})

	t.Run("existence probe fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", err: errW12GDriver},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationState(ctx, tx, source); err == nil {
			t.Fatal("存在性探测失败必须上抛")
		}
	})

	t.Run("shape validation fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", err: errW12GDriver},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationState(ctx, tx, source); err == nil {
			t.Fatal("形状校验失败必须上抛")
		}
	})

	t.Run("cursor read fails", func(t *testing.T) {
		source := w12gFakeDB(t, &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"stats_job_state"}}}},
			{match: "table_info", rows: w12gTableInfo([]string{
				"scope_type", "scope_id", "job_name", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at",
			}, nil)},
			{match: "FROM stats_job_state", err: errW12GDriver},
		}})
		target := w12gFakeDB(t, &w12gFakeConn{})
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := copySQLiteTrustAggregationState(ctx, tx, source); err == nil || !strings.Contains(err.Error(), "read J3b legacy trust aggregation cursor") {
			t.Fatalf("游标读取失败必须被包装: %v", err)
		}
	})
}

func TestW12GInspectSQLiteAndRunSQLiteScripted(t *testing.T) {
	ctx := context.Background()

	t.Run("inspectSQLite master read fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "sqlite_master", err: errW12GDriver}}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, conn)); err == nil {
			t.Fatal("sqlite_master 读取失败必须上抛")
		}
	})

	t.Run("inspectSQLite pragma fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
			{match: "table_info", err: errW12GDriver},
		}}
		if _, err := inspectSQLite(ctx, w12gFakeDB(t, conn)); err == nil {
			t.Fatal("table_info 失败必须上抛")
		}
	})

	t.Run("RunSQLite busy timeout fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "busy_timeout", err: errW12GDriver}}}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), false); err == nil {
			t.Fatal("PRAGMA 失败必须上抛")
		}
	})

	t.Run("RunSQLite apply schema exec fails", func(t *testing.T) {
		// 无 match 的脚本匹配一切查询：全部表报告缺失 → 进入 apply，随后 DDL 失败。
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			execErr:     errW12GDriver,
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "schema bootstrap 失败") {
			t.Fatalf("DDL 失败必须被包装: %v", err)
		}
	})

	t.Run("RunSQLite ensure columns fails", func(t *testing.T) {
		// schema DDL 成功（exec 无错），但 ensure 的 table_info 查询失败。
		conn := &w12gFakeConn{
			defaultRows: &w12gFakeRows{columns: []string{"name"}},
			queries: []w12gFakeQuery{
				{match: "table_info", err: errW12GDriver},
			},
		}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil || !strings.Contains(err.Error(), "run schema 失败") {
			t.Fatalf("run 列升级失败必须被包装: %v", err)
		}
	})

	t.Run("RunSQLite second inspection fails", func(t *testing.T) {
		// 第一次检查缺表 → apply；DDL 与 ensure 成功（ensure 查询无脚本时
		// pop 返回默认错误——因此提供可耗尽的 table_info 空脚本）。
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}}},
		}}
		if _, err := RunSQLite(ctx, w12gFakeDB(t, conn), true); err == nil {
			t.Fatal("二次检查失败必须上抛")
		}
	})
}

func TestW12GInspectTxIndexQueryInjected(t *testing.T) {
	ctx := context.Background()
	catalog := newWMpgCatalog()
	wmContractTargetCatalog(catalog)
	catalog.wmPopulateAllLegacySources(1)
	db := openWMFakePG(catalog)
	defer db.Close()
	catalog.injectedFails = append(catalog.injectedFails, "FROM pg_indexes")
	if _, err := Run(ctx, db, false); err == nil {
		t.Fatal("索引查询注入失败必须上抛")
	}
}
