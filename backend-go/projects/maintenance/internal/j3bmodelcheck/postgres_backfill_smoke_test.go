package j3bmodelcheck

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresBackfillReadbackSmoke exercises the actual PostgreSQL writer
// against an operator-provided disposable database. It intentionally creates
// synthetic legacy dataset/stats facts, not cutover evidence for a real Node
// owner. The database-name fence prevents accidental execution on the shared
// development database.
func TestPostgresBackfillReadbackSmoke(t *testing.T) {
	if os.Getenv("J3B_POSTGRES_BACKFILL_SMOKE") != "1" {
		t.Skip("set J3B_POSTGRES_BACKFILL_SMOKE=1 to run the isolated J3b PostgreSQL backfill/readback smoke")
	}
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_J3B_POSTGRES_BACKFILL_SMOKE_URL"))
	if rawURL == "" {
		t.Fatal("JUHE_AI_J3B_POSTGRES_BACKFILL_SMOKE_URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	database := strings.Trim(parsed.Path, "/")
	if !strings.HasPrefix(database, "juhe_ai_sub2api_dev_j3b_") {
		t.Fatalf("J3b PostgreSQL backfill smoke requires a disposable j3b dev database, got %q", database)
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	ctx := context.Background()
	cleanupPostgresBackfillSmoke(t, db)
	defer cleanupPostgresBackfillSmoke(t, db)
	for _, schema := range []string{SchemaName, "juhe_dataset", "juhe_stats"} {
		if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+postgresIdent(schema)); err != nil {
			t.Fatal(err)
		}
	}
	if report, err := Run(ctx, db, true); err != nil || !report.Ready() || !report.Applied {
		t.Fatalf("bootstrap report=%+v err=%v", report, err)
	}
	if err := createPostgresBackfillSmokeSources(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := seedPostgresBackfillSmokeSources(ctx, db); err != nil {
		t.Fatal(err)
	}
	first, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 10, MaxBytesPerTable: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresBackfillSmokeRows(t, first, 1, 0)
	readback, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 10})
	if err != nil || !readback.Ready || !readback.TransactionReadOnly {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
	for _, item := range postgresLegacyJ3bFactTables {
		if readback.Tables[item.name] != "match" || readback.SourceRows[item.name] != 1 || readback.TargetRows[item.name] != 1 {
			t.Fatalf("readback table %s=%q source=%d target=%d", item.name, readback.Tables[item.name], readback.SourceRows[item.name], readback.TargetRows[item.name])
		}
	}
	if readback.Tables[trustAggregationStateTable] != "match" || readback.SourceRows[trustAggregationStateTable] != 1 || readback.TargetRows[trustAggregationStateTable] != 1 {
		t.Fatalf("trust cursor readback=%q source=%d target=%d", readback.Tables[trustAggregationStateTable], readback.SourceRows[trustAggregationStateTable], readback.TargetRows[trustAggregationStateTable])
	}
	second, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 10, MaxBytesPerTable: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	assertPostgresBackfillSmokeRows(t, second, 0, 1)
}

// TestPostgresNodeGoBackfillFixture verifies a Node repository writer to Go
// maintenance reader/writer path against an explicitly prepared disposable
// database. Unlike the synthetic smoke above, its source rows are created by
// the current Node storage APIs. It is deliberately opt-in and is not
// cutover evidence: the caller must still satisfy the CLI's drain, backup,
// owner-epoch, and active-path-zero gates before any real migration.
func TestPostgresNodeGoBackfillFixture(t *testing.T) {
	if os.Getenv("J3B_POSTGRES_NODE_GO_BACKFILL_FIXTURE") != "1" {
		t.Skip("set J3B_POSTGRES_NODE_GO_BACKFILL_FIXTURE=1 to run the Node-to-Go disposable PostgreSQL fixture")
	}
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_J3B_NODE_GO_BACKFILL_FIXTURE_URL"))
	if rawURL == "" {
		t.Fatal("JUHE_AI_J3B_NODE_GO_BACKFILL_FIXTURE_URL is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	database := strings.Trim(parsed.Path, "/")
	if !strings.HasPrefix(database, "juhe_ai_sub2api_dev_j3bnode") {
		t.Fatalf("Node-to-Go fixture requires a disposable j3bnode dev database, got %q", database)
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	ctx := context.Background()
	expectedRows := map[string]int64{
		"model_check_runs":                        1,
		"model_check_items":                       1,
		"model_check_observations":                1,
		"account_quality_health_hourly":           1,
		"model_account_trust_results":             1,
		trustAggregationStateTable:                1,
		"model_token_intercept_baseline_versions": 0,
		"model_trust_latest_dirty_accounts":       0,
		"model_trust_observation_receipts":        0,
	}
	before, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 10})
	if err != nil {
		t.Fatal(err)
	}
	for table, sourceRows := range expectedRows {
		if before.SourceRows[table] != sourceRows || before.TargetRows[table] != 0 || before.Tables[table] != "drift" {
			t.Fatalf("pre-backfill Node source table %s=%q source=%d target=%d", table, before.Tables[table], before.SourceRows[table], before.TargetRows[table])
		}
	}
	first, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 10, MaxBytesPerTable: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if first.TransactionIsolation != "serializable" {
		t.Fatalf("backfill isolation=%q", first.TransactionIsolation)
	}
	for table, sourceRows := range expectedRows {
		if result := first.Tables[table]; result.SourceRows != sourceRows || result.InsertedRows != sourceRows || result.SkippedRows != 0 {
			t.Fatalf("first Node-to-Go backfill table %s=%+v", table, result)
		}
	}
	after, err := VerifyPostgresBackfill(ctx, db, PostgresReadbackOptions{MaxRowsPerTable: 10})
	if err != nil || !after.Ready || !after.TransactionReadOnly {
		t.Fatalf("post-backfill readback=%+v err=%v", after, err)
	}
	for table, sourceRows := range expectedRows {
		if after.Tables[table] != "match" || after.SourceRows[table] != sourceRows || after.TargetRows[table] != sourceRows {
			t.Fatalf("post-backfill Node-to-Go table %s=%q source=%d target=%d", table, after.Tables[table], after.SourceRows[table], after.TargetRows[table])
		}
	}
	second, err := BackfillPostgres(ctx, db, PostgresBackfillOptions{MaxRowsPerTable: 10, MaxBytesPerTable: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	for table, sourceRows := range expectedRows {
		if result := second.Tables[table]; result.SourceRows != sourceRows || result.InsertedRows != 0 || result.SkippedRows != sourceRows {
			t.Fatalf("idempotent Node-to-Go backfill table %s=%+v", table, result)
		}
	}
}

func createPostgresBackfillSmokeSources(ctx context.Context, db *sql.DB) error {
	for _, item := range postgresLegacyJ3bFactTables {
		statement := fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", postgresQualifiedIdent(item.sourceSchema, item.name), postgresQualifiedIdent(SchemaName, item.name))
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create J3b PostgreSQL smoke source %s.%s: %w", item.sourceSchema, item.name, err)
		}
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE juhe_stats.stats_job_state (
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		job_name TEXT NOT NULL,
		cursor_created_at TEXT,
		cursor_id TEXT,
		last_success_at TEXT,
		last_error_message TEXT,
		lag_seconds INTEGER,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (scope_type,scope_id,job_name)
	)`)
	return err
}

func seedPostgresBackfillSmokeSources(ctx context.Context, db *sql.DB) error {
	const at = "2026-08-31T12:00:00Z"
	statements := []string{
		`INSERT INTO juhe_dataset.model_check_runs (id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,model,started_at,created_at,updated_at) VALUES ('backfill-run','sys','actor','gpt','account','acct','gpt-5.6', '` + at + `','` + at + `','` + at + `')`,
		`INSERT INTO juhe_dataset.model_check_items (id,run_id,item_key,item_type,status,created_at,updated_at) VALUES ('backfill-item','backfill-run','protocol','probe','passed','` + at + `','` + at + `')`,
		`INSERT INTO juhe_dataset.model_check_observations (id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,created_at) VALUES ('backfill-observation','backfill-run','sys','acct','gpt','gpt-5.6','gpt-5.6','protocol_basic','complete','verified','matched','passed','` + at + `')`,
		`INSERT INTO juhe_stats.account_quality_health_hourly (account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,updated_at) VALUES ('acct','sys','gpt','2026-08-31T12:00:00Z','` + at + `','backfill-run','gpt-5.6','quick',100,70,'success','` + at + `')`,
		`INSERT INTO juhe_stats.model_token_intercept_baseline_versions (cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,first_observed_at,last_observed_at,updated_at) VALUES ('cohort','gpt-5.6','tokenizer-v1','probe-v1',1,'` + at + `','` + at + `','` + at + `')`,
		`INSERT INTO juhe_stats.model_account_trust_results (system_account_id,account_id,requested_model,updated_at) VALUES ('sys','acct','gpt-5.6','` + at + `')`,
		`INSERT INTO juhe_stats.model_trust_latest_dirty_accounts (system_account_id,account_id,requested_model,dirty_reason,updated_at) VALUES ('sys','acct','gpt-5.6','smoke','` + at + `')`,
		`INSERT INTO juhe_stats.model_trust_observation_receipts (observation_id,observation_created_at,processed_at) VALUES ('backfill-observation','` + at + `','` + at + `')`,
		`INSERT INTO juhe_stats.stats_job_state (scope_type,scope_id,job_name,cursor_created_at,cursor_id,last_success_at,last_error_message,lag_seconds,updated_at) VALUES ('global','','model-trust-observation-aggregation','` + at + `','backfill-observation','` + at + `',NULL,0,'` + at + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("seed J3b PostgreSQL backfill smoke: %w", err)
		}
	}
	return nil
}

func assertPostgresBackfillSmokeRows(t *testing.T, report PostgresBackfillReport, inserted, skipped int64) {
	t.Helper()
	if report.TransactionIsolation != "serializable" {
		t.Fatalf("backfill isolation=%q", report.TransactionIsolation)
	}
	for _, item := range append(append([]postgresBackfillTable(nil), postgresLegacyJ3bFactTables...), postgresBackfillTable{name: trustAggregationStateTable}) {
		result, found := report.Tables[item.name]
		if !found || result.SourceRows != 1 || result.InsertedRows != inserted || result.SkippedRows != skipped {
			t.Fatalf("backfill table %s=%+v found=%t", item.name, result, found)
		}
	}
}

func cleanupPostgresBackfillSmoke(t *testing.T, db *sql.DB) {
	t.Helper()
	if db == nil {
		return
	}
	for _, schema := range []string{"juhe_dataset", "juhe_stats", SchemaName} {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + postgresIdent(schema) + " CASCADE"); err != nil {
			t.Errorf("cleanup J3b PostgreSQL backfill smoke schema %s: %v", schema, err)
		}
	}
}

func postgresIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
