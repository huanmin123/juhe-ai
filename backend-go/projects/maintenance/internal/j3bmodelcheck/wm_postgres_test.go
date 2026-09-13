package j3bmodelcheck

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// 本文件用 wmPGCatalog 目录模拟器覆盖 J3b PostgreSQL 语义链路：
// bootstrap Run/inspectTx 契约校验、BackfillPostgres 复制幂等、
// VerifyPostgresBackfill 只读回读闭环，以及行值归一化 helper。
// 真实 PostgreSQL 行为仍由 env 门控 smoke（TestPostgresBackfillReadbackSmoke）
// 覆盖，这里不新增任何真实服务依赖。

// wmReadyJ3bDB 返回挂接契约就绪目录（含 legacy 源数据）的数据库句柄。
func wmReadyJ3bDB(t *testing.T, legacyRows int) (*sql.DB, *wmPGCatalog) {
	t.Helper()
	catalog := newWMpgCatalog()
	wmContractTargetCatalog(catalog)
	catalog.wmPopulateAllLegacySources(legacyRows)
	db := openWMFakePG(catalog)
	t.Cleanup(func() { _ = db.Close() })
	return db, catalog
}

func TestWMJ3bRunCheckAndApplyLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db rejected", func(t *testing.T) {
		if _, err := Run(ctx, nil, false); err == nil {
			t.Fatal("nil db 必须被拒绝")
		}
	})

	t.Run("missing schema fails closed on check", func(t *testing.T) {
		catalog := newWMpgCatalog()
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil {
			t.Fatalf("check on empty catalog: %v", err)
		}
		if !report.MissingSchema || report.Ready() {
			t.Fatalf("空目录必须报告 missingSchema 且不就绪: %+v", report)
		}
	})

	t.Run("apply refuses to create missing schema", func(t *testing.T) {
		catalog := newWMpgCatalog()
		db := openWMFakePG(catalog)
		defer db.Close()
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "拒绝创建 juhe_j3b schema") {
			t.Fatalf("缺失 schema 的 apply 必须拒绝: %v", err)
		}
	})

	t.Run("apply refuses cross-role schema", func(t *testing.T) {
		catalog := newWMpgCatalog()
		catalog.ensureSchema(SchemaName, "somebody_else")
		db := openWMFakePG(catalog)
		defer db.Close()
		if _, err := Run(ctx, db, true); err == nil || !strings.Contains(err.Error(), "拒绝跨角色修改") {
			t.Fatalf("跨角色 apply 必须拒绝: %v", err)
		}
	})

	t.Run("check reports missing tables before apply", func(t *testing.T) {
		catalog := newWMpgCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := Run(ctx, db, false)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		if report.Ready() || len(report.MissingTables) != len(contracts.J3BModelCheckTables) {
			t.Fatalf("空 schema 必须报告全部缺失表: %+v", report)
		}
	})

	t.Run("apply installs contract schema and flips applied", func(t *testing.T) {
		catalog := newWMpgCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := Run(ctx, db, true)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !report.Ready() || !report.Applied {
			t.Fatalf("DDL 之后的目录必须就绪且标记 applied: %+v", report)
		}
		recheck, err := Run(ctx, db, false)
		if err != nil || !recheck.Ready() || recheck.Applied {
			t.Fatalf("就绪目录的复检必须保持 ready 且不标记 applied: %+v err=%v", recheck, err)
		}
	})

	t.Run("apply on ready schema short-circuits without applied flag", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 0)
		report, err := Run(ctx, db, true)
		if err != nil {
			t.Fatalf("apply on ready: %v", err)
		}
		if !report.Ready() || report.Applied {
			t.Fatalf("已就绪目录的 apply 不应重复执行 DDL: %+v", report)
		}
	})
}

func TestWMInspectTxDetectsColumnAndIndexDrift(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		mutate      func(catalog *wmPGCatalog)
		wantInvalid bool
		wantMissing bool
	}{
		{
			name:        "column type drift",
			mutate:      func(c *wmPGCatalog) { c.wmMutateTargetColumn("model_check_runs", "score", "text") },
			wantInvalid: true,
		},
		{
			name: "missing index",
			mutate: func(c *wmPGCatalog) {
				for _, table := range c.schemas[SchemaName] {
					delete(table.indexes, "idx_model_check_runs_created")
				}
			},
			wantMissing: true,
		},
		{
			name: "missing constraint",
			mutate: func(c *wmPGCatalog) {
				c.schemas[SchemaName]["model_check_outcomes"].constraints = nil
			},
			wantInvalid: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, catalog := wmReadyJ3bDB(t, 0)
			test.mutate(catalog)
			report, err := Run(ctx, db, false)
			if err != nil {
				t.Fatalf("check with drift: %v", err)
			}
			if report.Ready() {
				t.Fatalf("漂移目录必须失败闭环: %+v", report)
			}
			if test.wantInvalid && len(report.InvalidTables) == 0 {
				t.Fatalf("应报告 InvalidTables: %+v", report)
			}
			if test.wantMissing && len(report.MissingIndexes) == 0 {
				t.Fatalf("应报告 MissingIndexes: %+v", report)
			}
		})
	}
}

func TestWMBackfillPostgresCopiesFactsIdempotently(t *testing.T) {
	ctx := context.Background()
	db, _ := wmReadyJ3bDB(t, 2)

	report, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
	if err != nil {
		t.Fatalf("首次 backfill: %v", err)
	}
	if report.TransactionIsolation != "serializable" {
		t.Fatalf("backfill 必须运行在 serializable 事务: %+v", report)
	}
	for _, item := range postgresLegacyJ3bFactTables {
		if report.Tables[item.name].SourceRows != 2 || report.Tables[item.name].InsertedRows != 2 {
			t.Fatalf("表 %s 首次复制应 source=2 inserted=2: %+v", item.name, report.Tables[item.name])
		}
		if len(report.Tables[item.name].Projection) == 0 || len(report.Tables[item.name].PrimaryKeys) == 0 {
			t.Fatalf("表 %s 报告必须记录投影与主键: %+v", item.name, report.Tables[item.name])
		}
	}
	state := report.Tables[trustAggregationStateTable]
	if state.SourceRows != 1 || state.InsertedRows != 1 || state.SourceSchema != "juhe_stats" || state.TargetSchema != SchemaName {
		t.Fatalf("trust 游标应被复制一次: %+v", state)
	}

	repeat, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
	if err != nil {
		t.Fatalf("幂等 backfill: %v", err)
	}
	for _, item := range postgresLegacyJ3bFactTables {
		if repeat.Tables[item.name].InsertedRows != 0 || repeat.Tables[item.name].SkippedRows != 2 {
			t.Fatalf("重复 backfill 必须全部跳过: %s %+v", item.name, repeat.Tables[item.name])
		}
	}
	if repeat.Tables[trustAggregationStateTable].SkippedRows != 1 {
		t.Fatalf("trust 游标重复 backfill 必须跳过: %+v", repeat.Tables[trustAggregationStateTable])
	}
}

func TestWMVerifyPostgresBackfillMatchesAfterBackfill(t *testing.T) {
	ctx := context.Background()
	db, _ := wmReadyJ3bDB(t, 2)
	if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if !report.Ready || !report.TransactionReadOnly {
		t.Fatalf("复制后的回读必须就绪: %+v errors=%v", report, report.Tables)
	}
	for _, item := range postgresLegacyJ3bFactTables {
		if report.Tables[item.name] != "match" {
			t.Fatalf("表 %s 应 match: %s", item.name, report.Tables[item.name])
		}
		if report.SourceRows[item.name] != 2 || report.TargetRows[item.name] != 2 {
			t.Fatalf("表 %s 行数应一致为 2: %+v", item.name, report)
		}
		if report.SourceDigest[item.name] == "" || report.SourceDigest[item.name] != report.TargetDigest[item.name] {
			t.Fatalf("表 %s 双侧 digest 应一致且非空", item.name)
		}
		if report.SourceExceededRowLimit[item.name] || report.TargetExceededRowLimit[item.name] {
			t.Fatalf("表 %s 不应超限", item.name)
		}
	}
	if report.Tables[trustAggregationStateTable] != "match" {
		t.Fatalf("trust 游标应 match: %s", report.Tables[trustAggregationStateTable])
	}

	verifiedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	manifest, err := NewPostgresJ3bReadbackManifest(report, J3bReadbackManifestOptions{SourceSnapshotIdentity: "wm-snapshot-1", VerifiedAt: verifiedAt})
	if err != nil {
		t.Fatalf("生成 PG readback manifest: %v", err)
	}
	if manifest.TargetSchema != SchemaName || len(manifest.Tables) != 9 {
		t.Fatalf("manifest 应覆盖 9 张表: %+v", manifest)
	}
	if errs := contracts.ValidateJ3bReadbackManifest(manifest, verifiedAt.Add(time.Minute), 3600); len(errs) != 0 {
		t.Fatalf("manifest 必须通过共享契约校验: %v", errs)
	}

	if _, err := NewPostgresJ3bReadbackManifest(PostgresBackfillVerificationReport{Ready: true}, J3bReadbackManifestOptions{SourceSnapshotIdentity: "wm", VerifiedAt: verifiedAt}); err == nil {
		t.Fatal("非只读事务的 report 不得生成 manifest")
	}
	if _, err := NewPostgresJ3bReadbackManifest(report, J3bReadbackManifestOptions{VerifiedAt: verifiedAt}); err == nil {
		t.Fatal("缺少 snapshot identity 不得生成 manifest")
	}
}

func TestWMVerifyPostgresBackfillFailsClosed(t *testing.T) {
	ctx := context.Background()
	t.Run("transaction writable", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		catalog.wmPopulateAllLegacySources(1)
		db := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatalf("readback: %v", err)
		}
		if report.Ready || report.Tables["__transaction__"] != "transaction is writable" {
			t.Fatalf("可写事务必须失败闭环: %+v", report)
		}
	})

	t.Run("target schema incomplete", func(t *testing.T) {
		catalog := newWMpgCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatalf("readback: %v", err)
		}
		if report.Ready || report.Tables["__target_schema__"] != "target schema incomplete" {
			t.Fatalf("目标 schema 不完整必须失败闭环: %+v", report)
		}
	})

	t.Run("mandatory source absent", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 0)
		catalog.wmDropSourceTable("juhe_dataset", "model_check_runs")
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatalf("readback: %v", err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "mandatory source table absent" {
			t.Fatalf("强制源表缺失必须失败闭环: %+v", report)
		}
	})

	t.Run("source exceeds row limit", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 3)
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 2})
		if err != nil {
			t.Fatalf("readback: %v", err)
		}
		if report.Ready {
			t.Fatalf("超限必须失败闭环: %+v", report)
		}
		found := false
		for _, item := range postgresLegacyJ3bFactTables {
			if report.Tables[item.name] == "source exceeds row limit" {
				found = true
			}
		}
		if !found {
			t.Fatalf("应至少有一张表报告超限: %+v", report.Tables)
		}
	})

	t.Run("row drift after backfill", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatalf("backfill: %v", err)
		}
		// 直接篡改目标行，模拟复制后的上游漂移。
		for _, row := range catalog.schemas[SchemaName]["model_check_runs"].rows {
			if row["id"] != nil {
				row["score"] = int64(999)
				break
			}
		}
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatalf("readback: %v", err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "drift" {
			t.Fatalf("目标漂移必须报告 drift: %+v", report)
		}
	})
}

func TestWMBackfillPostgresRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	t.Run("nil db", func(t *testing.T) {
		if _, err := BackfillPostgres(ctx, nil, PostgresBackfillOptions{}); err == nil {
			t.Fatal("nil db 必须被拒绝")
		}
	})

	t.Run("target schema incomplete", func(t *testing.T) {
		catalog := newWMpgCatalog()
		catalog.ensureSchema(SchemaName, catalog.currentUser)
		db := openWMFakePG(catalog)
		defer db.Close()
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err == nil || !strings.Contains(err.Error(), "target schema is incomplete") {
			t.Fatalf("目标不完整必须失败: %v", err)
		}
	})

	t.Run("mandatory legacy source missing", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 0)
		catalog.wmDropSourceTable("juhe_stats", "model_trust_observation_receipts")
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
		if err == nil || !strings.Contains(err.Error(), "legacy source table juhe_stats.model_trust_observation_receipts is missing") {
			t.Fatalf("强制源表缺失必须失败: %v", err)
		}
	})

	t.Run("trust cursor source missing", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 0)
		catalog.wmDropSourceTable("juhe_stats", "stats_job_state")
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
		if err == nil || !strings.Contains(err.Error(), "juhe_stats.stats_job_state is missing") {
			t.Fatalf("trust 游标源表缺失必须失败: %v", err)
		}
	})

	t.Run("source exceeds max rows", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 5)
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 2})
		if err == nil || !strings.Contains(err.Error(), "exceeds max rows per table") {
			t.Fatalf("源超限必须失败: %v", err)
		}
	})

	t.Run("source exceeds max bytes", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 5)
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 10, MaxBytesPerTable: 64})
		if err == nil || !strings.Contains(err.Error(), "exceeds max bytes per table") {
			t.Fatalf("源字节超限必须失败: %v", err)
		}
	})

	t.Run("read-only transaction refused", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		catalog.wmPopulateAllLegacySources(1)
		db := sql.OpenDB(wmFakeConnector{catalog: catalog, forceReadOnly: true})
		defer db.Close()
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("只读事务必须拒绝复制: %v", err)
		}
	})

	t.Run("unready trust cursor shape", func(t *testing.T) {
		// stats_job_state 多出一个未映射列时，列形状校验必须拒绝。
		db, catalog := wmReadyJ3bDB(t, 0)
		state := catalog.schemas["juhe_stats"]["stats_job_state"]
		state.columns = append(state.columns, wmPGColumnSpec{name: "node_extra", dataType: "text", udtName: "text"})
		_, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{})
		if err == nil || !strings.Contains(err.Error(), "unexpected columns") {
			t.Fatalf("游标源表多余列必须拒绝: %v", err)
		}
	})
}

func TestWMVerifyPostgresBackfillRejectsInvalidBoundsAndNilDB(t *testing.T) {
	if _, err := VerifyPostgresBackfill(context.Background(), nil, PostgresReadbackOptions{}); err == nil {
		t.Fatal("nil db 必须被拒绝")
	}
	tests := []struct {
		name  string
		value int64
	}{
		{"negative", -1},
		{"over maximum", MaximumPostgresReadbackMaxRows + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizePostgresReadbackMaxRows(test.value); err == nil {
				t.Fatalf("maxRows=%d 必须被拒绝", test.value)
			}
		})
	}
	if got, err := normalizePostgresReadbackMaxRows(0); err != nil || got != DefaultPostgresReadbackMaxRows {
		t.Fatalf("maxRows=0 必须取默认值: got=%d err=%v", got, err)
	}
}

func TestWMOpenValidatesExplicitPostgresURL(t *testing.T) {
	db, err := Open("postgres://maintenance@127.0.0.1:5432/juhe_maintenance")
	if err != nil {
		t.Fatalf("合法 URL 必须通过解析（懒连接）: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, rawURL := range []string{
		"",
		"   ",
		"sqlite:///tmp/x.db",
		"postgres:///juhe",
		"postgres://@127.0.0.1:5432/juhe",
		"postgres://maintenance@/juhe",
		"http://maintenance@127.0.0.1:5432/juhe",
	} {
		if _, err := Open(rawURL); err == nil {
			t.Fatalf("URL %q 必须被拒绝", rawURL)
		}
	}
}

func TestWMOpenSQLiteRequiresExplicitPath(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if _, err := OpenSQLite(path); err == nil {
			t.Fatalf("空路径必须被拒绝: %q", path)
		}
	}
}

func TestWMPostgresColumnsHelpersOnFake(t *testing.T) {
	ctx := context.Background()
	db, _ := wmReadyJ3bDB(t, 0)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	columns, err := postgresColumns(ctx, tx, SchemaName, "model_check_runs")
	if err != nil {
		t.Fatalf("postgresColumns: %v", err)
	}
	if len(columns) == 0 || !containsString(columns, "id") {
		t.Fatalf("model_check_runs 应包含 id 列: %v", columns)
	}
	if _, err := postgresColumns(ctx, tx, SchemaName, "missing_table"); err == nil || !strings.Contains(err.Error(), "has no columns") {
		t.Fatalf("缺失表必须报错: %v", err)
	}
	if _, err := postgresColumns(ctx, tx, SchemaName, "missing_table"); err == nil {
		t.Fatal("缺失表第二次仍必须报错")
	}
	keys, err := postgresPrimaryKeys(ctx, tx, SchemaName, "account_quality_health_hourly")
	if err != nil || len(keys) != 2 {
		t.Fatalf("复合主键应按声明顺序返回: %v err=%v", keys, err)
	}
}

func TestWMBackfillValueHelpersCoverAllKinds(t *testing.T) {
	cases := []struct {
		name      string
		left      any
		right     any
		wantEqual bool
	}{
		{"nil vs nil", nil, nil, true},
		{"nil vs text", nil, "x", false},
		{"text equal", "abc", "abc", true},
		{"text drift", "abc", "abd", false},
		{"bytes vs bytes", []byte("a"), []byte("a"), true},
		{"time equal", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), true},
		{"time zone equal", time.Date(2026, 1, 2, 11, 4, 5, 0, time.FixedZone("CST", 8*3600)), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), true},
		{"time drift", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), false},
		{"time vs text", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "x", false},
		{"int equal", int64(7), int64(7), true},
		{"int drift", int64(7), int64(8), false},
		{"bool equal", true, true, true},
		{"bool drift", true, false, false},
		{"float equal", 1.5, 1.5, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := postgresBackfillValuesEqual(test.left, test.right); got != test.wantEqual {
				t.Fatalf("postgresBackfillValuesEqual(%v,%v)=%t want %t", test.left, test.right, got, test.wantEqual)
			}
		})
	}
	// normalizeValue 决定 digest 稳定性：同类不同表示必须可区分。
	if normalizeValue(nil) != "null:" {
		t.Fatalf("nil 归一化: %q", normalizeValue(nil))
	}
	if normalizeValue([]byte("raw")) != "text:raw" {
		t.Fatalf("[]byte 归一化应走 text 通道: %q", normalizeValue([]byte("raw")))
	}
	if normalizeValue(sql.RawBytes("raw")) != "text:raw" {
		t.Fatalf("RawBytes 归一化应走 text 通道: %q", normalizeValue(sql.RawBytes("raw")))
	}
	if normalizeValue(int(3)) != "int:3" || normalizeValue(int64(3)) != "int64:3" {
		t.Fatalf("int/int64 归一化必须可区分: %q / %q", normalizeValue(int(3)), normalizeValue(int64(3)))
	}
	if normalizeValue(float32(1.5)) == normalizeValue(float64(1.5)) {
		t.Fatalf("float32/float64 归一化应保留类型前缀")
	}
	if normalizeValue(errors.New("x")) == "" {
		t.Fatal("未知类型回退 fmt 表示")
	}

	// postgresBackfillRowBytes 的各类型分支。
	bytes := postgresBackfillRowBytes([]any{nil, []byte("abcd"), "abc", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), int64(123), true})
	if bytes <= 0 {
		t.Fatalf("行字节统计必须为正: %d", bytes)
	}
	if got := postgresBackfillRowBytes([]any{nil}); got != 0 {
		t.Fatalf("nil 单元格不计字节: %d", got)
	}
	if got := postgresBackfillRowBytes([]any{int64(123)}); got != 3 {
		t.Fatalf("整数按十进制文本计字节: %d", got)
	}
}

func TestWMPostgresBackfillProjectionRules(t *testing.T) {
	column := func(name, dataType, udt string, nullable, defaulted bool) postgresBackfillColumn {
		return postgresBackfillColumn{Name: name, DataType: dataType, UdtName: udt, Nullable: nullable, HasDefault: defaulted}
	}
	source := map[string]postgresBackfillColumn{
		"id": column("id", "text", "text", false, false), "score": column("score", "integer", "int4", false, false),
	}
	target := map[string]postgresBackfillColumn{
		"id": column("id", "text", "text", false, false), "score": column("score", "integer", "int4", false, false),
		"note": column("note", "text", "text", true, false), "flag": column("flag", "boolean", "bool", false, true),
	}
	projection, err := postgresBackfillProjection(source, target, "wm")
	if err != nil {
		t.Fatalf("合法投影: %v", err)
	}
	if len(projection) != 2 {
		t.Fatalf("投影只应包含源列: %v", projection)
	}

	badType := map[string]postgresBackfillColumn{"id": column("id", "varchar", "varchar", false, false)}
	if _, err := postgresBackfillProjection(badType, target, "wm"); err == nil || !strings.Contains(err.Error(), "type/nullability mismatch") {
		t.Fatalf("类型漂移必须拒绝: %v", err)
	}
	unmapped := map[string]postgresBackfillColumn{"legacy_only": column("legacy_only", "text", "text", false, false)}
	if _, err := postgresBackfillProjection(unmapped, target, "wm"); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column") {
		t.Fatalf("未映射源列必须拒绝: %v", err)
	}
	requiredMissing := map[string]postgresBackfillColumn{"id": column("id", "text", "text", false, false)}
	strictTarget := map[string]postgresBackfillColumn{"id": column("id", "text", "text", false, false), "required": column("required", "text", "text", false, false)}
	if _, err := postgresBackfillProjection(requiredMissing, strictTarget, "wm"); err == nil || !strings.Contains(err.Error(), "required but absent") {
		t.Fatalf("目标必需列缺失必须拒绝: %v", err)
	}
	emptyTarget := map[string]postgresBackfillColumn{"note": column("note", "text", "text", true, false)}
	if _, err := postgresBackfillProjection(nil, emptyTarget, "wm"); err == nil || !strings.Contains(err.Error(), "no public projection") {
		t.Fatalf("空投影必须拒绝: %v", err)
	}

	// readback 投影：源列必须全部出现在目标列清单中。
	if _, err := postgresReadbackProjection([]string{"a", "legacy_extra"}, []string{"a", "b"}, "wm"); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column") {
		t.Fatalf("readback 未映射源列必须拒绝: %v", err)
	}
	if _, err := postgresReadbackProjection([]string{}, []string{"a"}, "wm"); err == nil || !strings.Contains(err.Error(), "no public projection") {
		t.Fatalf("readback 空投影必须拒绝: %v", err)
	}
	got, err := postgresReadbackProjection([]string{"b", "a"}, []string{"a", "b", "c"}, "wm")
	if err != nil || len(got) != 2 {
		t.Fatalf("readback 投影应为交集: %v err=%v", got, err)
	}
}

func TestWMContainsAllAndStringSliceHelpers(t *testing.T) {
	if !containsAll([]string{"a", "b", "c"}, []string{"c", "a"}) {
		t.Fatal("containsAll 应接受子集")
	}
	if containsAll([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("containsAll 应拒绝缺项")
	}
	if !sameStringSlice([]string{"a", "b"}, []string{"a", "b"}) || sameStringSlice([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("sameStringSlice 语义错误")
	}
}
