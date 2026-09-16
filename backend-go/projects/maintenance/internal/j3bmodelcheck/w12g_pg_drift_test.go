package j3bmodelcheck

// w12g 波次：VerifyPostgresBackfill 的投影漂移、主键不匹配、证据与越界分支
// （基于 wmPGCatalog 构造形状漂移的 legacy 表）。

import (
	"context"
	"strings"
	"testing"
)

func TestW12GVerifyPostgresReadbackShapeDrift(t *testing.T) {
	ctx := context.Background()

	t.Run("projection drift reported", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
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
		// model_check_runs 带 legacy 额外列 → 投影校验失败写入表状态。
		drifted := append(wmLegacyColumnsOf(catalog, "model_check_runs"), wmPGColumnSpec{name: "w12g_legacy_extra", dataType: "text", udtName: "text"})
		catalog.wmAddLegacyTable("juhe_dataset", "model_check_runs", drifted, []string{"id"})
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || !strings.Contains(report.Tables["model_check_runs"], "unmapped legacy source column") {
			t.Fatalf("投影漂移必须写入表状态: %+v", report.Tables["model_check_runs"])
		}
	})

	t.Run("primary key mismatch reported", func(t *testing.T) {
		catalog := newWMpgCatalog()
		wmContractTargetCatalog(catalog)
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
		catalog.wmAddLegacyTable("juhe_dataset", "model_check_runs", wmLegacyColumnsOf(catalog, "model_check_runs"), []string{"score"})
		db := openWMFakePG(catalog)
		defer db.Close()
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables["model_check_runs"] != "primary key projection mismatch" {
			t.Fatalf("主键漂移必须写入表状态: %+v", report.Tables["model_check_runs"])
		}
	})

	t.Run("trust drift reported after tamper", func(t *testing.T) {
		db, catalog := wmReadyJ3bDB(t, 1)
		if _, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{}); err != nil {
			t.Fatal(err)
		}
		// 篡改 target 侧 trust 游标 → trust 状态 drift。
		for _, row := range catalog.schemas[SchemaName][trustAggregationStateTable].rows {
			if text, ok := row["cursor_id"].(string); ok {
				row["cursor_id"] = text + "-tampered"
			}
		}
		report, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if report.Ready || report.Tables[trustAggregationStateTable] != "drift" {
			t.Fatalf("游标篡改必须报告 drift: %+v", report.Tables)
		}
	})
}
