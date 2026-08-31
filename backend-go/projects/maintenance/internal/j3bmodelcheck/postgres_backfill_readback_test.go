package j3bmodelcheck

import (
	"strings"
	"testing"
)

func TestPostgresReadbackRowLimitIsExplicitAndBounded(t *testing.T) {
	if got, err := normalizePostgresReadbackMaxRows(0); err != nil || got != DefaultPostgresReadbackMaxRows {
		t.Fatalf("default limit got=%d err=%v", got, err)
	}
	for _, value := range []int64{-1, MaximumPostgresReadbackMaxRows + 1} {
		if _, err := normalizePostgresReadbackMaxRows(value); err == nil {
			t.Fatalf("limit %d must be rejected", value)
		}
	}
	if got, err := normalizePostgresReadbackMaxRows(MaximumPostgresReadbackMaxRows); err != nil || got != MaximumPostgresReadbackMaxRows {
		t.Fatalf("maximum limit got=%d err=%v", got, err)
	}
}

func TestPostgresReadbackMapsOnlyLegacyJ3bFacts(t *testing.T) {
	want := map[string]string{
		"model_check_runs":                        "juhe_dataset",
		"model_check_items":                       "juhe_dataset",
		"model_check_observations":                "juhe_dataset",
		"account_quality_health_hourly":           "juhe_stats",
		"model_token_intercept_baseline_versions": "juhe_stats",
		"model_account_trust_results":             "juhe_stats",
		"model_trust_latest_dirty_accounts":       "juhe_stats",
		"model_trust_observation_receipts":        "juhe_stats",
	}
	if len(postgresLegacyJ3bFactTables) != len(want) {
		t.Fatalf("fact mapping count=%d want=%d", len(postgresLegacyJ3bFactTables), len(want))
	}
	for _, table := range postgresLegacyJ3bFactTables {
		if got, ok := want[table.name]; !ok || got != table.sourceSchema {
			t.Fatalf("unexpected mapping %+v", table)
		}
	}
}

func TestPostgresReadbackQueryUsesStableProjectionAndBoundedLimit(t *testing.T) {
	columns := []string{"updated_at", "id", "score"}
	primaryKeys := []string{"id"}
	projection := append([]string(nil), columns...)
	// postgresTableEvidence sorts the projection but must preserve the
	// declared primary-key order independently for deterministic row order.
	query := "SELECT " + postgresJSONProjection(projection) + " FROM " + postgresQualifiedIdent("juhe_dataset", "model_check_runs") + " ORDER BY " + joinQuotedInOrder(primaryKeys) + " LIMIT $1"
	if want := `SELECT to_jsonb("id")::text,to_jsonb("score")::text,to_jsonb("updated_at")::text FROM "juhe_dataset"."model_check_runs" ORDER BY "id" LIMIT $1`; query != want {
		t.Fatalf("query=%q want=%q", query, want)
	}
	if !strings.Contains(query, "LIMIT $1") || strings.Contains(query, "OFFSET") {
		t.Fatalf("readback query must remain a bounded head query: %q", query)
	}
}

func TestPostgresReadbackRequiresEquivalentKeyProjection(t *testing.T) {
	if !containsAll([]string{"id", "score", "updated_at"}, []string{"id"}) {
		t.Fatal("primary key inside public projection must be accepted")
	}
	if containsAll([]string{"score", "updated_at"}, []string{"id"}) {
		t.Fatal("readback must reject a projection that omits its primary key")
	}
	if sameStringSlice([]string{"account_id", "stat_hour"}, []string{"stat_hour", "account_id"}) {
		t.Fatal("composite primary-key order is part of the digest contract")
	}
}

func TestPostgresReadbackRejectsUnmappedLegacyColumns(t *testing.T) {
	if _, err := postgresReadbackProjection([]string{"id", "legacy_only"}, []string{"id"}, "model_check_runs"); err == nil {
		t.Fatal("readback must reject source-only columns")
	}
	if got, err := postgresReadbackProjection([]string{"id"}, []string{"id", "target_extra"}, "model_check_runs"); err != nil || len(got) != 1 || got[0] != "id" {
		t.Fatalf("optional target-only column should be omitted: got=%v err=%v", got, err)
	}
}

func TestPostgresReadbackSchemaAllowsDedicatedReadOnlyRole(t *testing.T) {
	if !postgresReadbackSchemaReady(Report{Schema: SchemaName, CurrentRole: "j3b_readback", SchemaOwner: "gateway_owner", OwnerMismatch: true}) {
		t.Fatal("readback must allow a SELECT-only maintenance role when target structure is valid")
	}
	if postgresReadbackSchemaReady(Report{Schema: SchemaName, MissingSchema: true}) {
		t.Fatal("missing target schema must fail readback")
	}
	if postgresReadbackSchemaReady(Report{Schema: SchemaName, InvalidIndexes: []string{"idx_model_check_runs_created"}}) {
		t.Fatal("target index drift must fail readback")
	}
}
