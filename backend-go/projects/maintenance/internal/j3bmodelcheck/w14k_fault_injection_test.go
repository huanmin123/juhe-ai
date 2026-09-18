package j3bmodelcheck

// w14k 波次：沿 w12g 不可达登记逐条复核，用真实 SQLite 文件、w12gFakeConn
// 脚本与 wmPGCatalog injectedFails 补测可实际触达的分支。终态 95.2%。
//
// w12g 登记复核结论：
//   - 维持不可达（实测佐证）：
//     1) verifyQueryOnly 的 PRAGMA 为连接级读取、不触碰文件内容（文本文件
//        也返回 ro=true/err=nil）——backfill.go open/query_only 错误与
//        “只读未生效”分支不可达；
//     2) sql.Open 懒注册（pgx/modernc 均推迟解析）；
//     3) rows.Scan 到 []any/指针/固定 string 目标在 fake 行值类型受控下
//        恒成功；
//     4) rows.Err/rows.Close/tx.Commit 错误在真实驱动与现有 fake 下均无
//        构造入口（wmFakeTx.Commit 恒 nil）；
//     5) filepath.Abs/os.Stat 非 NotExist 错误在 Windows 无稳定构造。
//   - 误判纠正（实际可达，本文件已补测）：ValidateSQLiteBackfillPaths 的
//     stat/Abs 错误、BackfillSQLite 对损坏/空/缺列 target 的错误链、
//     RunSQLite 的 PRAGMA/inspect/索引查询错误、inspectSQLite 索引枚举
//     错误、sqliteTableEvidenceAgainstSource 的 digest 失败、BackfillPostgres
//     的 normalize/缺表/超限/游标探测与写入错误、Run apply 的 DDL 失败、
//     VerifyPostgresBackfill 的只读事务/超限/trust 源缺失/目标证据错误。
//   - 仍登记不可达的补充项：BackfillPostgres 的 SHOW Scan/isolation 不匹配
//     （fake 恒 serializable）、source 与 target 同串查询的 err 透传无法
//     区分命中侧（postgresBackfillColumns/PrimaryKeys 的 target 侧）、
//     apply 后契约仍不完整（fake DDL 恒安装完整目录）、Verify 的可写事务
//     早退（readback 契约恒 ReadOnly 事务）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- SQLite：真实文件构造 ----------

func TestW14KValidateSQLitePathsStatsMissingSource(t *testing.T) {
	root := t.TempDir()
	err := ValidateSQLiteBackfillPaths(filepath.Join(root, "target.db"), filepath.Join(root, "missing-dataset.db"), filepath.Join(root, "missing-stats.db"))
	if err == nil || !strings.Contains(err.Error(), "stat J3b SQLite source path") {
		t.Fatalf("缺失 dataset 必须报 stat 错误: %v", err)
	}
}

func TestW14KBackfillSQLiteTargetNotADatabase(t *testing.T) {
	ctx := context.Background()
	_, _, datasetPath, statsPath := w12gBuildBackfillTree(t)
	textTarget := w12gTextFile(t, "text-target.db")
	target, err := OpenSQLite(textTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil {
		t.Fatal("文本 target 的 inspect 必须失败")
	}
}

func TestW14KBackfillSQLiteTargetSchemaIncomplete(t *testing.T) {
	ctx := context.Background()
	_, _, datasetPath, statsPath := w12gBuildBackfillTree(t)
	emptyTarget := filepath.Join(t.TempDir(), "empty-target.db")
	target, err := OpenSQLite(emptyTarget)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := BackfillSQLite(ctx, target, datasetPath, statsPath); err == nil || !strings.Contains(err.Error(), "target schema is incomplete") {
		t.Fatalf("空 target 必须报 schema 不完整: %v", err)
	}
}

func TestW14KBackfillSQLiteSourceColumnMismatch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	targetPath := filepath.Join(root, "target.db")
	datasetPath := filepath.Join(root, "dataset.db")
	statsPath := filepath.Join(root, "stats.db")
	target := wmOpenTempJ3b(t, targetPath)
	mustCloseW12G(t, target)
	dataset := wmOpenTempJ3b(t, datasetPath)
	// 源表缺契约投影所需列：copy 必须失败闭环。
	if _, err := dataset.Exec(`DROP TABLE model_check_runs`); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, status TEXT)`); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	mustCloseW12G(t, dataset)
	// wmOpenRW 以 mode=rw 打开，stats 文件必须先存在；0 字节是合法空库。
	if err := os.WriteFile(statsPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stats := wmOpenRW(t, statsPath)
	wmCreateStatsJobState(t, stats)
	wmInsertLegacyCursor(t, stats, "w14k-obs", "")
	mustCloseW12G(t, stats)
	backfillTarget, err := OpenSQLite(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backfillTarget.Close()
	if _, err := BackfillSQLite(ctx, backfillTarget, datasetPath, statsPath); err == nil {
		t.Fatal("源列缺失的 copy 必须失败")
	}
}

func TestW14KRunSQLitePragmasAndInspectFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("text file fails PRAGMA busy_timeout", func(t *testing.T) {
		textPath := w12gTextFile(t, "runsqlite-text.db")
		db, err := OpenSQLite(textPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := RunSQLite(ctx, db, false); err == nil {
			t.Fatal("文本文件的 PRAGMA 必须失败")
		}
	})
	t.Run("inspect failure propagates", func(t *testing.T) {
		// PRAGMA busy_timeout（Exec）成功，inspect 的首个目录查询无脚本命中。
		db := w12gFakeDB(t, &w12gFakeConn{})
		defer db.Close()
		if _, err := RunSQLite(ctx, db, false); err == nil {
			t.Fatal("目录查询失败必须上抛")
		}
	})
	t.Run("index listing failure propagates", func(t *testing.T) {
		// 表与列脚本应答正常，PRAGMA index_list 无脚本命中 → 索引查询失败。
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			{match: "sqlite_master", rows: &w12gFakeRows{columns: []string{"name"}, rows: [][]driver.Value{{"model_check_runs"}}}},
		}}
		db := w12gFakeDB(t, conn)
		defer db.Close()
		if _, err := RunSQLite(ctx, db, false); err == nil {
			t.Fatal("索引列表失败必须上抛")
		}
	})
}

func TestW14KSQLiteTableEvidenceDigestFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	target, err := OpenSQLite(filepath.Join(root, "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	source, err := OpenSQLite(filepath.Join(root, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	// 目标表存在但无主键：digest 阶段必须失败（"has no primary key" 透传）。
	for _, path := range []string{filepath.Join(root, "target.db"), filepath.Join(root, "source.db")} {
		db, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS w14k_nopk (id TEXT)`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := sqliteTableEvidenceAgainstSource(ctx, target, source, "w14k_nopk"); err == nil || !strings.Contains(err.Error(), "no primary key") {
		t.Fatalf("无主键表的 digest 必须失败: %v", err)
	}
}

// ---------- PostgreSQL：wmPGCatalog 注入 ----------

func TestW14KBackfillPostgresInjectedBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("nil database rejected", func(t *testing.T) {
		if _, err := BackfillPostgres(ctx, nil, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("nil db 必须拒绝: %v", err)
		}
	})

	t.Run("read-only transaction blocks backfill", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("只读事务必须拒绝 backfill: %v", err)
		}
	})

	t.Run("missing target trust table", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		delete(catalog.schemas[SchemaName], trustAggregationStateTable)
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), trustAggregationStateTable) {
			t.Fatalf("缺目标 trust 表必须报错: %v", err)
		}
	})

	t.Run("missing target fact table", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		delete(catalog.schemas[SchemaName], "model_check_runs")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "model_check_runs") {
			t.Fatalf("缺目标事实表必须报错: %v", err)
		}
	})

	t.Run("legacy table exceeds max bytes", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 1)
		db.Close()
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxBytesPerTable: 1}); err == nil {
			t.Fatal("单行字节超限必须报错")
		}
	})

	t.Run("existing cursor probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "FROM juhe_j3b.model_trust_aggregation_state")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "check J3b PostgreSQL trust aggregation cursor") {
			t.Fatalf("游标探测失败必须被包装: %v", err)
		}
	})

	t.Run("cursor insert fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "INSERT INTO juhe_j3b.model_trust_aggregation_state")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "insert J3b PostgreSQL trust aggregation cursor") {
			t.Fatalf("游标写入失败必须被包装: %v", err)
		}
	})

	t.Run("primary key probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "pg_constraint")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer ro.Close()
		if _, err := BackfillPostgres(ctx, ro, PostgresBackfillOptions{}); err == nil {
			t.Fatal("主键探测失败必须上抛")
		}
	})
}

func TestW14KVerifyPostgresBackfillInjectedBranches(t *testing.T) {
	ctx := context.Background()

	// 登记（w14k）：VerifyPostgresBackfill 的“事务可写 fail-closed”早退分支
	// （postgres_backfill_readback.go SHOW transaction_read_only=off）在真实
	// 与 fake 驱动下均不可达——readback 契约固定以 ReadOnly 事务开启，fake
	// 连接按 driver.TxOptions 透传。该分支属防御性守卫，维持 w12g 口径登记。

	t.Run("target schema inspection fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "current_database()")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		if _, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "readback target schema") {
			t.Fatalf("目标 schema 校验失败必须被包装: %v", err)
		}
	})

	t.Run("primary key probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, "pg_constraint")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		if _, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{}); err == nil {
			t.Fatal("只读事务下主键探测失败必须上抛")
		}
	})

	t.Run("target evidence read fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.injectedFails = append(catalog.injectedFails, `FROM "juhe_j3b"`)
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		if _, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{}); err == nil {
			t.Fatal("目标证据读取失败必须上抛")
		}
	})

	t.Run("source exceeds row limit", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 2)
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 1})
		if err != nil {
			t.Fatalf("超限必须返回报告: %v", err)
		}
		if report.Ready {
			t.Fatalf("超限必须未就绪: %+v", report)
		}
		found := false
		for _, note := range report.Tables {
			if strings.Contains(note, "exceeds row limit") {
				found = true
			}
		}
		if !found {
			t.Fatalf("报告必须记录超限: %+v", report.Tables)
		}
	})

	t.Run("missing legacy trust source", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		db.Close()
		catalog.wmDropSourceTable("juhe_stats", "stats_job_state")
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		if _, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "stats_job_state") {
			t.Fatalf("缺 legacy trust 源表必须报错: %v", err)
		}
	})
}

// ---------- w14k 补充批次：normalize/apply-DDL/超限分支 ----------

func TestW14KBackfillPostgresOptionsNormalization(t *testing.T) {
	_, catalog := wmReadyJ3bDB(t, 1)
	db := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
	defer db.Close()
	if _, err := BackfillPostgres(context.Background(), db, PostgresBackfillOptions{MaxRowsPerTable: -1}); err == nil || !strings.Contains(err.Error(), "max rows per table") {
		t.Fatalf("负数 max rows 必须被拒绝: %v", err)
	}
}

func TestW14KRunApplySchemaExecutionFailure(t *testing.T) {
	ctx := context.Background()
	db, catalog := wmReadyJ3bDB(t, 1)
	db.Close()
	// 契约目录缺一张表：apply 进入 DDL 阶段；注入使 DDL 执行失败。
	delete(catalog.schemas[SchemaName], "model_check_runs")
	catalog.injectedFails = append(catalog.injectedFails, "CREATE TABLE")
	ro := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
	defer ro.Close()
	if _, err := Run(ctx, ro, true); err == nil || !strings.Contains(err.Error(), "schema bootstrap 失败") {
		t.Fatalf("apply DDL 失败必须被包装: %v", err)
	}
}

func TestW14KVerifyPostgresTargetExceedsRowLimit(t *testing.T) {
	ctx := context.Background()
	_, catalog := wmReadyJ3bDB(t, 1)
	// 目标表比源表多一行：source 不超限、target 超限（maxRows=1）。
	table := catalog.schemas[SchemaName]["model_check_runs"]
	table.rows = append(table.rows,
		map[string]driver.Value{"id": "w14k-extra-1"},
		map[string]driver.Value{"id": "w14k-extra-2"})
	ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
	defer ro.Close()
	report, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{MaxRowsPerTable: 1})
	if err != nil {
		t.Fatalf("超限必须返回报告: %v", err)
	}
	if report.Tables["model_check_runs"] != "target exceeds row limit" {
		t.Fatalf("必须记录 target 超限: %+v", report.Tables)
	}
}

func TestW14KVerifyPostgresTrustExceedsRowLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("source exceeds", func(t *testing.T) {
		_, catalog := wmReadyJ3bDB(t, 1)
		stats := catalog.schemas["juhe_stats"]["stats_job_state"]
		stats.rows = append(stats.rows, map[string]driver.Value{
			"scope_type": "global", "scope_id": "", "job_name": "model-trust-observation-aggregation",
			"cursor_created_at": "2026-08-27T11:00:00Z", "cursor_id": "w14k-obs-2", "last_success_at": nil,
			"last_error_message": nil, "lag_seconds": int64(4), "updated_at": "2026-08-27T11:01:00Z",
		})
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		report, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{MaxRowsPerTable: 1})
		if err != nil {
			t.Fatalf("trust 超限必须返回报告: %v", err)
		}
		if report.Tables[trustAggregationStateTable] != "source exceeds row limit" {
			t.Fatalf("必须记录 trust 源超限: %+v", report.Tables)
		}
	})

	t.Run("target exceeds", func(t *testing.T) {
		_, catalog := wmReadyJ3bDB(t, 1)
		target := catalog.schemas[SchemaName][trustAggregationStateTable]
		// target 证据查询按契约 scope key 过滤，追加行必须同 key 才可计数。
		key := driver.Value(trustAggregationStateScopeKey)
		target.rows = append(target.rows,
			map[string]driver.Value{"scope_key": key},
			map[string]driver.Value{"scope_key": key})
		ro := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer ro.Close()
		report, err := VerifyPostgresBackfill(ctx, ro, PostgresReadbackOptions{MaxRowsPerTable: 1})
		if err != nil {
			t.Fatalf("trust 超限必须返回报告: %v", err)
		}
		if report.Tables[trustAggregationStateTable] != "target exceeds row limit" {
			t.Fatalf("必须记录 trust 目标超限: %+v", report.Tables)
		}
	})
}
