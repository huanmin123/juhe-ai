package j3bmodelcheck

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresBackfillRejectsNilDBAndInvalidBounds(t *testing.T) {
	if _, err := BackfillPostgres(context.Background(), nil, PostgresBackfillOptions{}); err == nil {
		t.Fatal("nil database must be rejected before any connection work")
	}
	for _, options := range []PostgresBackfillOptions{
		{MaxRowsPerTable: -1},
		{MaxRowsPerTable: MaximumPostgresBackfillMaxRows + 1},
		{MaxBytesPerTable: -1},
		{MaxBytesPerTable: MaximumPostgresBackfillMaxBytes + 1},
	} {
		if _, err := normalizePostgresBackfillOptions(options); err == nil {
			t.Fatalf("invalid options must be rejected: %+v", options)
		}
	}
	got, err := normalizePostgresBackfillOptions(PostgresBackfillOptions{})
	if err != nil || got.maxRows != DefaultPostgresBackfillMaxRows || got.maxBytes != DefaultPostgresBackfillMaxBytes {
		t.Fatalf("defaults got=%+v err=%v", got, err)
	}
}

func TestPostgresBackfillUsesOnlyWhitelistedFacts(t *testing.T) {
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
		t.Fatalf("whitelist count=%d want=%d", len(postgresLegacyJ3bFactTables), len(want))
	}
	for _, item := range postgresLegacyJ3bFactTables {
		if want[item.name] != item.sourceSchema {
			t.Fatalf("unexpected whitelist entry %+v", item)
		}
	}
}

func TestPostgresBackfillProjectionIsStableAndRejectsUnknownSourceColumns(t *testing.T) {
	column := func(name, dataType, udt string, nullable, defaulted bool) postgresBackfillColumn {
		return postgresBackfillColumn{Name: name, DataType: dataType, UdtName: udt, Nullable: nullable, HasDefault: defaulted}
	}
	source := map[string]postgresBackfillColumn{
		"id":         column("id", "text", "text", false, false),
		"score":      column("score", "integer", "int4", false, false),
		"created_at": column("created_at", "text", "text", false, false),
	}
	target := map[string]postgresBackfillColumn{
		"created_at": column("created_at", "text", "text", false, false),
		"id":         column("id", "text", "text", false, false),
		"score":      column("score", "integer", "int4", false, false),
		"trace_id":   column("trace_id", "text", "text", true, false),
	}
	projection, err := postgresBackfillProjection(source, target, "model_check_runs")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(projection, ","), "created_at,id,score"; got != want {
		t.Fatalf("projection=%q want=%q", got, want)
	}
	source["legacy_extra"] = column("legacy_extra", "text", "text", true, false)
	if _, err := postgresBackfillProjection(source, target, "model_check_runs"); err == nil || !strings.Contains(err.Error(), "unmapped legacy source column legacy_extra") {
		t.Fatalf("source-only column must fail closed: %v", err)
	}
}

func TestPostgresBackfillProjectionRejectsRequiredTargetGapAndTypeDrift(t *testing.T) {
	base := func() (map[string]postgresBackfillColumn, map[string]postgresBackfillColumn) {
		column := func(name, dataType, udt string, nullable, defaulted bool) postgresBackfillColumn {
			return postgresBackfillColumn{Name: name, DataType: dataType, UdtName: udt, Nullable: nullable, HasDefault: defaulted}
		}
		return map[string]postgresBackfillColumn{"id": column("id", "text", "text", false, false)}, map[string]postgresBackfillColumn{
			"id":           column("id", "text", "text", false, false),
			"required":     column("required", "text", "text", false, false),
			"with_default": column("with_default", "text", "text", false, true),
		}
	}
	source, target := base()
	if _, err := postgresBackfillProjection(source, target, "model_check_runs"); err == nil || !strings.Contains(err.Error(), "required but absent") {
		t.Fatalf("required target gap must fail closed: %v", err)
	}
	source, target = base()
	target["id"] = postgresBackfillColumn{Name: "id", DataType: "bigint", UdtName: "int8"}
	delete(target, "required")
	if _, err := postgresBackfillProjection(source, target, "model_check_runs"); err == nil || !strings.Contains(err.Error(), "type/nullability mismatch") {
		t.Fatalf("column type drift must fail closed: %v", err)
	}
}

func TestPostgresBackfillSQLIsParameterizedAndNeverUpdatesOrDeletes(t *testing.T) {
	if got, want := postgresPlaceholders(3), "$1,$2,$3"; got != want {
		t.Fatalf("placeholders=%q want=%q", got, want)
	}
	if got, want := postgresPrimaryKeyPredicate([]string{"tenant_id", "id"}), `"tenant_id"=$1 AND "id"=$2`; got != want {
		t.Fatalf("predicate=%q want=%q", got, want)
	}
	query := "INSERT INTO " + postgresQualifiedIdent(SchemaName, "model_check_runs") + " (" + joinPostgresQuoted([]string{"id", "score"}) + ") VALUES (" + postgresPlaceholders(2) + ")"
	if strings.Contains(strings.ToUpper(query), "UPDATE") || strings.Contains(strings.ToUpper(query), "DELETE") || !strings.Contains(query, "$1,$2") {
		t.Fatalf("writer SQL must be insert-only and parameterized: %q", query)
	}
}

func TestPostgresBackfillValueComparisonAndByteGuard(t *testing.T) {
	if !postgresBackfillValuesEqual([]byte("same"), "same") {
		t.Fatal("byte and string representations should compare by canonical value")
	}
	if postgresBackfillValuesEqual("one", "two") || !postgresBackfillValuesEqual(nil, nil) || postgresBackfillValuesEqual(nil, "") {
		t.Fatal("value comparison must detect drift and nil differences")
	}
	if got := postgresBackfillRowBytes([]any{"abc", []byte("de")}); got != 5 {
		t.Fatalf("row byte estimate=%d want=5", got)
	}
}
