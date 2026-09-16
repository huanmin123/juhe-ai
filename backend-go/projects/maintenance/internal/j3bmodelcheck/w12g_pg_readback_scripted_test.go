package j3bmodelcheck

// w12g 波次：VerifyPostgresBackfill 全链脚本化（事务配置/SHOW/inspect/
// 存在性/投影/主键/证据/越界/drift/trust 链）与 postgresTableEvidence 的
// 错误分支。不可达（w12g 登记）：postgres_backfill_readback.go 中以 []any
// 为 Scan 目标的错误分支（277-279 附近）与 database/sql 在 EOF 时内部关闭
// 行集导致的显式 rows.Close 错误分支。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
)

func w12gPgExistsScript(exists bool) w12gFakeQuery {
	value := "false"
	if exists {
		value = "true"
	}
	return w12gFakeQuery{match: "information_schema.tables WHERE table_schema=$1 AND table_name=$2", rows: &w12gFakeRows{columns: []string{"exists"}, rows: [][]driver.Value{{value == "true"}}}}
}

func w12gPgColumnsScript(columns ...string) w12gFakeQuery {
	rows := make([][]driver.Value, 0, len(columns))
	for _, name := range columns {
		rows = append(rows, []driver.Value{name, "text", "text", "NO", nil, "NO"})
	}
	return w12gFakeQuery{match: "information_schema.columns", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: rows}}
}

func w12gPgPKScript(keys ...string) w12gFakeQuery {
	rows := make([][]driver.Value, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, []driver.Value{key})
	}
	return w12gFakeQuery{match: "pg_index", rows: &w12gFakeRows{columns: []string{"attname"}, rows: rows}}
}

func w12gPgIdentity() w12gFakeQuery {
	return w12gFakeQuery{match: "current_database()", rows: &w12gFakeRows{columns: []string{"db", "user"}, rows: [][]driver.Value{{"db", "role"}}}}
}

func TestW12GVerifyPostgresBackfillFullChain(t *testing.T) {
	ctx := context.Background()

	t.Run("begin fails", func(t *testing.T) {
		conn := &w12gFakeConn{beginErr: true}
		if _, err := VerifyPostgresBackfill(ctx, w12gFakeDB(t, conn), PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "begin J3b PostgreSQL readback") {
			t.Fatalf("begin 失败必须被包装: %v", err)
		}
	})

	t.Run("configure fails", func(t *testing.T) {
		conn := &w12gFakeConn{execErr: errW12GDriver}
		if _, err := VerifyPostgresBackfill(ctx, w12gFakeDB(t, conn), PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "configure J3b PostgreSQL readback transaction") {
			t.Fatalf("事务配置失败必须被包装: %v", err)
		}
	})

	t.Run("show read only fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "SHOW transaction_read_only", err: errW12GDriver}}}
		if _, err := VerifyPostgresBackfill(ctx, w12gFakeDB(t, conn), PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL readback transaction mode") {
			t.Fatalf("SHOW 失败必须被包装: %v", err)
		}
	})

	t.Run("writable transaction reported", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		catalog.wmPopulateAllLegacySources(1)
		db := sql.OpenDB(wmFakeConnector{catalog: catalog, ignoreReadOnly: true})
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["__transaction__"] != "transaction is writable" {
			t.Fatalf("可写事务必须报告: %+v", report.Tables)
		}
	})

	t.Run("inspect fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		catalog.injectedFails = append(catalog.injectedFails, "current_database()")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "verify J3b PostgreSQL readback target schema") {
			t.Fatalf("inspect 失败必须被包装: %v", err)
		}
	})

	t.Run("source absent reported", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
		// 仅 legacy 事实表缺失；trust 游标源表仍在（形状须与校验期望一致）。
		catalog.wmAddLegacyTable("juhe_stats", "stats_job_state", []wmPGColumnSpec{
			{name: "scope_type", dataType: "text", udtName: "text"},
			{name: "scope_id", dataType: "text", udtName: "text"},
			{name: "job_name", dataType: "text", udtName: "text"},
			{name: "cursor_created_at", dataType: "text", udtName: "text", nullable: true},
			{name: "cursor_id", dataType: "text", udtName: "text", nullable: true},
			{name: "last_success_at", dataType: "text", udtName: "text", nullable: true},
			{name: "last_error_message", dataType: "text", udtName: "text", nullable: true},
			{name: "lag_seconds", dataType: "integer", udtName: "int4", nullable: true},
			{name: "updated_at", dataType: "text", udtName: "text"},
		}, []string{"scope_type", "scope_id", "job_name"})
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "mandatory source table absent" {
			t.Fatalf("源表缺失必须报告: %+v", report.Tables)
		}
	})

	t.Run("source columns probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "ORDER BY ordinal_position")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL columns") {
			t.Fatalf("源列探测失败必须传播: %v", err)
		}
	})

	t.Run("primary key probe fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "FROM pg_index AS idx")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL primary key") {
			t.Fatalf("主键探测失败必须传播: %v", err)
		}
	})

	t.Run("evidence read fails", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		catalog.injectedFails = append(catalog.injectedFails, "to_jsonb")
		if _, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{}); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL") {
			t.Fatalf("证据读取失败必须传播: %v", err)
		}
	})

	t.Run("source exceeded row limit", func(t *testing.T) {
		db, _ := wmReadyJ3bDB(t, 2)
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 1})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || !report.SourceExceededRowLimit["model_check_runs"] {
			t.Fatalf("源超限必须报告: %+v", report.SourceExceededRowLimit)
		}
	})

	t.Run("drift reported after tamper", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		// 篡改 target 行值 → readback 报告 drift。
		for _, table := range catalog.schemas[SchemaName] {
			for _, row := range table.rows {
				for name, value := range row {
					if text, ok := value.(string); ok {
						row[name] = text + "-tampered"
					}
				}
			}
		}
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "drift" {
			t.Fatalf("篡改后必须报告 drift: %+v", report.Tables)
		}
	})
}

func TestW12GPostgresTableEvidenceScripted(t *testing.T) {
	ctx := context.Background()

	t.Run("iterate error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "FROM", rows: &w12gFakeRows{columns: []string{"c1"}, rows: [][]driver.Value{{"v"}}, nextErr: errW12GDriver}}}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := postgresTableEvidence(ctx, tx, "s", "t", []string{"id"}, []string{"id"}, 10); err == nil || !strings.Contains(err.Error(), "iterate J3b PostgreSQL table") {
			t.Fatalf("证据迭代失败必须被包装: %v", err)
		}
	})

	t.Run("read error", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{{match: "FROM", err: errW12GDriver}}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := postgresTableEvidence(ctx, tx, "s", "t", []string{"id"}, []string{"id"}, 10); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL table") {
			t.Fatalf("证据读取失败必须被包装: %v", err)
		}
	})
}

func TestW12GVerifyTrustStateScripted(t *testing.T) {
	ctx := context.Background()
	// source 期望 9 列、target 期望 7 列，两次列查询分别命中。
	sourceShape := func() w12gFakeQuery {
		return w12gFakeQuery{match: "ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"scope_type", "text", "text", "NO", nil, "NO"},
			{"scope_id", "text", "text", "NO", nil, "NO"},
			{"job_name", "text", "text", "NO", nil, "NO"},
			{"cursor_created_at", "text", "text", "YES", nil, "NO"},
			{"cursor_id", "text", "text", "YES", nil, "NO"},
			{"last_success_at", "text", "text", "YES", nil, "NO"},
			{"last_error_message", "text", "text", "YES", nil, "NO"},
			{"lag_seconds", "integer", "int4", "YES", nil, "NO"},
			{"updated_at", "text", "text", "NO", nil, "NO"},
		}}}
	}
	targetShape := func() w12gFakeQuery {
		return w12gFakeQuery{match: "ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6"}, rows: [][]driver.Value{
			{"scope_key", "text", "text", "NO", nil, "NO"},
			{"cursor_created_at", "text", "text", "YES", nil, "NO"},
			{"cursor_id", "text", "text", "YES", nil, "NO"},
			{"last_success_at", "text", "text", "YES", nil, "NO"},
			{"last_error_message", "text", "text", "YES", nil, "NO"},
			{"lag_seconds", "integer", "int4", "YES", nil, "NO"},
			{"updated_at", "text", "text", "NO", nil, "NO"},
		}}}
	}

	t.Run("missing source fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{w12gPgExistsScript(false)}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := verifyPostgresTrustAggregationState(ctx, tx, 10); err == nil || !strings.Contains(err.Error(), "juhe_stats.stats_job_state is missing") {
			t.Fatalf("缺失游标源必须拒绝: %v", err)
		}
	})

	t.Run("validate fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			w12gPgExistsScript(true),
			w12gFakeQuery{match: "ORDER BY ordinal_position", err: errW12GDriver},
			w12gFakeQuery{match: "ORDER BY ordinal_position", rows: &w12gFakeRows{columns: []string{"c"}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := verifyPostgresTrustAggregationState(ctx, tx, 10); err == nil {
			t.Fatal("形状校验失败必须上抛")
		}
	})

	t.Run("source evidence fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			w12gPgExistsScript(true),
			sourceShape(), targetShape(),
			sourceShape(), targetShape(),
			{match: "FROM juhe_stats.stats_job_state WHERE", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := verifyPostgresTrustAggregationState(ctx, tx, 10); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL trust aggregation state") {
			t.Fatalf("源证据读取失败必须被包装: %v", err)
		}
	})

	t.Run("target evidence fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			w12gPgExistsScript(true),
			sourceShape(), targetShape(),
			sourceShape(), targetShape(),
			{match: "FROM juhe_stats.stats_job_state WHERE", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "c", "3", "c"}}}},
			{match: "FROM juhe_j3b.model_trust_aggregation_state WHERE", err: errW12GDriver},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := verifyPostgresTrustAggregationState(ctx, tx, 10); err == nil || !strings.Contains(err.Error(), "read J3b PostgreSQL trust aggregation state") {
			t.Fatalf("目标证据读取失败必须被包装: %v", err)
		}
	})

	t.Run("evidence iterate fails", func(t *testing.T) {
		conn := &w12gFakeConn{queries: []w12gFakeQuery{
			w12gPgExistsScript(true),
			sourceShape(), targetShape(),
			sourceShape(), targetShape(),
			{match: "FROM juhe_stats.stats_job_state WHERE", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "c", "3", "c"}}, nextErr: errW12GDriver}},
			{match: "FROM juhe_j3b.model_trust_aggregation_state WHERE", rows: &w12gFakeRows{columns: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}, rows: [][]driver.Value{{"cur", "c", "c", "c", "c", "3", "c"}}}},
		}}
		_, tx := w12gBeginTx(t, conn)
		if _, err := postgresTrustAggregationStateEvidence(ctx, tx, true, 10); err == nil || !strings.Contains(err.Error(), "iterate J3b PostgreSQL trust aggregation state") {
			t.Fatalf("证据迭代失败必须被包装: %v", err)
		}
	})
}
