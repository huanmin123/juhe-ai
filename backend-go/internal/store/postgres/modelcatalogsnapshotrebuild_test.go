package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestModelCatalogSnapshotRebuildQueriesUseGenerationFence(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_model_catalog_snapshot_rebuild.sql")
	if err != nil {
		t.Fatalf("read snapshot rebuild queries: %v", err)
	}
	sql := string(raw)
	for _, required := range []string{
		"ON CONFLICT (scope, system_account_id) DO UPDATE SET",
		"generation = juhe_business.model_catalog_snapshot_rebuild_requests.generation + 1",
		"AND generation = sqlc.arg(generation)::bigint",
		"ORDER BY CASE WHEN scope = 'all' THEN 0 ELSE 1 END",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("snapshot rebuild queries missing %q", required)
		}
	}
}

func TestDeleteCustomProviderModelMarksSnapshotDirtyAtomically(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_provider_models.sql")
	if err != nil {
		t.Fatalf("read provider model queries: %v", err)
	}
	sql := string(raw)
	start := strings.Index(sql, "-- name: DeleteManagementCustomProviderModel :one")
	if start < 0 {
		t.Fatal("delete custom provider model query boundary missing")
	}
	end := strings.Index(sql[start:], "-- name: GetManagementCustomProviderModelBindingSummary :one")
	if end < 0 {
		t.Fatal("delete custom provider model query boundary missing")
	}
	section := sql[start : start+end]
	for _, required := range []string{
		"WITH deleted AS (",
		"INSERT INTO juhe_business.model_catalog_snapshot_rebuild_requests",
		"FROM deleted",
		"SELECT COUNT(*)::bigint AS deleted_count",
	} {
		if !strings.Contains(section, required) {
			t.Fatalf("delete query missing %q", required)
		}
	}
}
