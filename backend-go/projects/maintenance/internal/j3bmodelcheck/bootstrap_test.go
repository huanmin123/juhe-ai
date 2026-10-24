package j3bmodelcheck

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

// OpenSQLite 必须与 maintenance/bootstrap.OpenSQLiteFile 先例保持同一
// journal 形态：新库文件落在 WAL（rollback journal 下写事务独占会阻塞
// 读池）；对已存在的 rollback journal 文件重设是安全幂等转换。
func TestOpenSQLiteJournalModeWAL(t *testing.T) {
	db, err := OpenSQLite(filepath.Join(t.TempDir(), "j3b-wal-shape.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestQuotedColumnHelpersSeparateProjectionAndPrimaryKeyOrder(t *testing.T) {
	primaryKeys := []string{"tenant_id", "account_id"}
	if got, want := joinQuoted(primaryKeys), `"account_id","tenant_id"`; got != want {
		t.Fatalf("digest projection should be canonicalized: got %q want %q", got, want)
	}
	if got, want := joinQuotedInOrder(primaryKeys), `"tenant_id","account_id"`; got != want {
		t.Fatalf("primary-key order must preserve SQLite declaration: got %q want %q", got, want)
	}
}

func TestJ3bSQLiteBootstrapCheckAndApply(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/j3b.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	report, err := RunSQLite(context.Background(), db, false)
	if err != nil || report.Ready() {
		t.Fatalf("initial report=%+v err=%v", report, err)
	}
	report, err = RunSQLite(context.Background(), db, true)
	if err != nil || !report.Ready() || !report.Applied {
		t.Fatalf("apply report=%+v err=%v", report, err)
	}
	report, err = RunSQLite(context.Background(), db, false)
	if err != nil || !report.Ready() || report.Applied {
		t.Fatalf("recheck report=%+v err=%v", report, err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,schedule_id,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,trace_id,created_at,updated_at) VALUES ('run-contract','sys','sys','openai','account','acct','acct','gpt-5.6-sol','quick','scheduled','schedule-1','running','unavailable',0,100,'','{}','{}','{}','{}','probe-v1','2026-08-28T00:00:00Z','trace-1','2026-08-28T00:00:00Z','2026-08-28T00:00:00Z')`); err != nil {
		t.Fatalf("Gateway runtime run contract must be insertable after bootstrap: %v", err)
	}
}

func TestJ3bSQLiteBootstrapRollsBackFailedApply(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/j3b-fail.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := sqliteSchemaStatements
	sqliteSchemaStatements = append(append([]string(nil), original...), "CREATE TABLE broken (")
	defer func() { sqliteSchemaStatements = original }()
	if _, err := RunSQLite(context.Background(), db, true); err == nil {
		t.Fatal("invalid schema statement must fail")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='model_check_runs'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed apply left partial schema, count=%d", count)
	}
}

func TestJ3bSQLiteBootstrapUpgradesLegacyRunColumnsAtomically(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/j3b-legacy.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,actor_system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,account_id TEXT,model TEXT NOT NULL,profile TEXT NOT NULL,trigger_kind TEXT NOT NULL,status TEXT NOT NULL,level TEXT NOT NULL,score INTEGER NOT NULL,max_score INTEGER NOT NULL,message TEXT NOT NULL,request_summary_json TEXT NOT NULL,result_summary_json TEXT NOT NULL,policy_snapshot_json TEXT NOT NULL,quality_decision_json TEXT NOT NULL,quality_health_sync_status TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,finished_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs VALUES ('legacy-run','sys','sys','openai','account','acct','acct','gpt-5.6-sol','quick','manual','completed','success',100,100,'ok','{}','{}','{}','{}',NULL,'2026-08-27T00:00:00Z','2026-08-27T00:00:00Z',NULL)`); err != nil {
		t.Fatal(err)
	}
	report, err := RunSQLite(context.Background(), db, true)
	if err != nil || !report.Ready() || !report.Applied {
		t.Fatalf("legacy upgrade report=%+v err=%v", report, err)
	}
	var scheduleID, probeSet, startedAt, traceID sql.NullString
	if err := db.QueryRow(`SELECT schedule_id,probe_set_version,started_at,trace_id FROM model_check_runs WHERE id='legacy-run'`).Scan(&scheduleID, &probeSet, &startedAt, &traceID); err != nil {
		t.Fatal(err)
	}
	if scheduleID.Valid || traceID.Valid || probeSet.String != "openai-model-check-v1" || startedAt.String != "2026-08-27T00:00:00Z" {
		t.Fatalf("legacy upgraded fields schedule=%+v probe=%+v started=%+v trace=%+v", scheduleID, probeSet, startedAt, traceID)
	}
}

func TestJ3bSQLiteBootstrapReadinessRejectsPrimaryKeyAndIndexDrift(t *testing.T) {
	db, err := OpenSQLite(t.TempDir() + "/j3b-structure.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := RunSQLite(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_model_check_runs_quality_health_sync_retry`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE malformed_model_check_runs (id TEXT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	// Replace the required table with a structurally invalid relation while
	// preserving its name; readiness must report both defects explicitly.
	if _, err := db.Exec(`ALTER TABLE model_check_runs RENAME TO model_check_runs_valid`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE malformed_model_check_runs RENAME TO model_check_runs`); err != nil {
		t.Fatal(err)
	}
	report, err := RunSQLite(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready() || len(report.InvalidPrimaryKeys) == 0 || len(report.MissingIndexes) == 0 {
		t.Fatalf("structural drift must fail closed: %+v", report)
	}
}

func TestJ3bSQLiteBootstrapReadinessRejectsRuntimeProjectionColumnDrift(t *testing.T) {
	db, err := OpenSQLite(t.TempDir() + "/j3b-columns.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := RunSQLite(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE account_quality_health_hourly RENAME TO account_quality_health_hourly_valid`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`); err != nil {
		t.Fatal(err)
	}
	report, err := RunSQLite(context.Background(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready() || !containsString(report.MissingColumns, "account_quality_health_hourly.error_code") || !containsString(report.MissingColumns, "account_quality_health_hourly.error_message") {
		t.Fatalf("missing health error projection columns must fail readiness: %+v", report)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func createLegacyTrustAggregationState(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL,scope_id TEXT NOT NULL DEFAULT '',job_name TEXT NOT NULL,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT NOT NULL,PRIMARY KEY(scope_type,scope_id,job_name))`); err != nil {
		t.Fatal(err)
	}
}

func TestJ3bBootstrapDDLIsScopedAndComplete(t *testing.T) {
	for _, table := range contracts.J3BModelCheckTables {
		if !strings.Contains(postgresSchema, "juhe_j3b."+table) {
			t.Fatalf("DDL missing table %q", table)
		}
	}
	for index := range contracts.J3BModelCheckIndexes {
		if !strings.Contains(postgresSchema, index) {
			t.Fatalf("DDL missing index %q", index)
		}
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(postgresSchema), " "))
	for _, forbidden := range []string{"juhe_business", "goose_db_version", "drop ", "delete from", "truncate "} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("DDL must not touch %q", forbidden)
		}
	}
	for _, allowed := range []string{
		"alter table juhe_j3b.model_check_runs add column if not exists schedule_id text",
		"alter table juhe_j3b.model_check_runs add column if not exists probe_set_version text not null default 'openai-model-check-v1'",
		"alter table juhe_j3b.model_check_runs add column if not exists started_at text",
		"alter table juhe_j3b.model_check_runs add column if not exists trace_id text",
	} {
		if !strings.Contains(normalized, allowed) {
			t.Fatalf("DDL missing approved forward migration %q", allowed)
		}
	}
	if got := len(requiredIndexNames()); got != len(contracts.J3BModelCheckIndexes) {
		t.Fatalf("index count=%d want=%d", got, len(contracts.J3BModelCheckIndexes))
	}
}

// TestJ3bBootstrapTriggerKindCheckContract pins the four-value
// model_check_runs.trigger_kind CHECK ('schedule_now' added for on-demand
// schedule execution) in both the bootstrap DDL and the guarded legacy
// migration. The migration runs outside postgresSchema on every apply
// (ready databases skip the DDL), so the two constants are asserted
// separately; the DO block itself needs a live PostgreSQL and is only
// checked textually here (its LIKE patterns follow pg_get_constraintdef's
// normalized rendering, verified against a real catalog).
func TestJ3bBootstrapTriggerKindCheckContract(t *testing.T) {
	if !strings.Contains(postgresSchema, "CHECK (trigger_kind IN ('manual','scheduled','quality_recovery','schedule_now'))") {
		t.Fatal("bootstrap DDL must declare the four-value trigger_kind CHECK")
	}
	if strings.Count(postgresSchema, "schedule_now") != 1 {
		t.Fatalf("bootstrap DDL should mention schedule_now exactly once (the trigger_kind CHECK), got %d", strings.Count(postgresSchema, "schedule_now"))
	}
	for _, fragment := range []string{
		"to_regclass('juhe_j3b.model_check_runs') IS NOT NULL",
		"pg_get_constraintdef(c.oid) LIKE '%trigger_kind%'",
		"pg_get_constraintdef(c.oid) NOT LIKE '%schedule_now%'",
		"a.attname = 'trigger_kind'",
		"DROP CONSTRAINT %I",
		"ADD CONSTRAINT model_check_runs_trigger_kind_check CHECK (trigger_kind IN (''manual'',''scheduled'',''quality_recovery'',''schedule_now''))",
	} {
		if !strings.Contains(postgresTriggerKindCheckMigration, fragment) {
			t.Fatalf("trigger_kind migration missing fragment %q", fragment)
		}
	}
	if strings.Contains(postgresSchema, "DROP CONSTRAINT") {
		t.Fatal("migration must stay outside postgresSchema (the DDL completeness test forbids DROP inside it)")
	}
}

func TestJ3bReportReadinessRejectsMissingOrMalformedObjects(t *testing.T) {
	if (Report{Schema: SchemaName, MissingSchema: true}).Ready() {
		t.Fatal("missing schema must fail readiness")
	}
	if (Report{Schema: SchemaName, InvalidTables: []string{"model_check_inputs.input_digest"}}).Ready() {
		t.Fatal("invalid table must fail readiness")
	}
	if !(Report{Schema: SchemaName, CurrentRole: "jobs", SchemaOwner: "jobs"}).Ready() {
		t.Fatal("empty valid report should be ready")
	}
}
