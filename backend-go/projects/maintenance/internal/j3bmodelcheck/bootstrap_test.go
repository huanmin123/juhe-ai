package j3bmodelcheck

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

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

func TestJ3bSQLiteBackfillCopiesFactsIdempotently(t *testing.T) {
	root := t.TempDir()
	target, err := OpenSQLite(root + "/target.db")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := RunSQLite(context.Background(), target, true); err != nil {
		t.Fatal(err)
	}
	dataset, err := OpenSQLite(root + "/dataset.db")
	if err != nil {
		t.Fatal(err)
	}
	defer dataset.Close()
	if _, err := RunSQLite(context.Background(), dataset, true); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_check_input_versions", "model_check_inputs", "model_check_execution_claims", "model_check_outcomes", "model_check_scheduler_tasks"} {
		if _, err := dataset.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatalf("remove Go-only legacy source table %s: %v", table, err)
		}
	}
	stats, err := OpenSQLite(root + "/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	if _, err := RunSQLite(context.Background(), stats, true); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',90,100,'ok','{}','{}','{"threshold":70}','{}','legacy-node-v1','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO model_check_items(id,run_id,item_key,item_type,status,score,max_score,evidence_summary_json,created_at,updated_at) VALUES ('item-1','run-1','basic','core','passed',90,100,'{}','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO model_check_observations(id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at) VALUES ('obs-1','run-1','sys','acct','openai','gpt-5.6','gpt-5.6','core','passed','verified','verified','passed',1,'2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.Exec(`INSERT INTO account_quality_health_hourly(account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,updated_at) VALUES ('acct','sys','openai','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','run-1','gpt-5.6','quick',90,70,'success','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.Exec(`INSERT INTO model_token_intercept_baseline_versions(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,version_status,evidence_status,independent_source_count,retained_source_count,excluded_source_count,q90_intercept,strong_gate_enabled,first_observed_at,last_observed_at,updated_at) VALUES ('hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','gpt-5.6','o200k_base@1','probe-v1',1,'calibration_pending','stable',10,10,0,120,0,'2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := dataset.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stats.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := BackfillSQLite(context.Background(), target, root+"/dataset.db", root+"/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	if report.InsertedRows["model_check_runs"] != 1 || report.InsertedRows["model_check_items"] != 1 || report.InsertedRows["model_check_observations"] != 1 || report.InsertedRows["account_quality_health_hourly"] != 1 || report.InsertedRows["model_token_intercept_baseline_versions"] != 1 {
		t.Fatalf("report=%+v", report)
	}
	if report.SourceDigest["model_check_runs"] == "" || report.SourceDigest["model_check_runs"] != report.TargetDigest["model_check_runs"] {
		t.Fatalf("source and target digest must match after first backfill: report=%+v", report)
	}
	if report.SourceRows["model_check_inputs"] != 0 || report.InsertedRows["model_check_outcomes"] != 0 || report.TargetRows["model_check_scheduler_tasks"] != 0 {
		t.Fatalf("Go-only legacy absence must be reported as empty, report=%+v", report)
	}
	report, err = BackfillSQLite(context.Background(), target, root+"/dataset.db", root+"/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	if report.InsertedRows["model_check_runs"] != 0 || report.TargetRows["model_check_runs"] != 1 {
		t.Fatalf("idempotent report=%+v", report)
	}
	if report.SourceDigest["model_check_runs"] != report.TargetDigest["model_check_runs"] {
		t.Fatalf("idempotent backfill changed digest: report=%+v", report)
	}
	if _, err := target.Exec(`INSERT INTO model_check_input_versions(identity_key,next_version,updated_at) VALUES(?,?,?)`, "go-only-input", 1, "2026-08-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	report, err = BackfillSQLite(context.Background(), target, root+"/dataset.db", root+"/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceRows["model_check_input_versions"] != 0 || report.InsertedRows["model_check_input_versions"] != 0 || report.TargetRows["model_check_input_versions"] != 1 {
		t.Fatalf("missing legacy Go-only table must preserve actual target count, report=%+v", report)
	}
	conflict, err := sql.Open("sqlite", "file:"+root+"/dataset.db?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conflict.Exec(`UPDATE model_check_runs SET score=1 WHERE id='run-1'`); err != nil {
		t.Fatal(err)
	}
	if err := conflict.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillSQLite(context.Background(), target, root+"/dataset.db", root+"/stats.db"); err == nil {
		t.Fatal("conflicting durable row must fail closed")
	}
	source, err := OpenSQLite(root + "/dataset.db")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`UPDATE model_check_runs SET score=90 WHERE id='run-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`ALTER TABLE model_check_runs ADD COLUMN legacy_node_extra TEXT`); err != nil {
		t.Fatal(err)
	}
	readback, err := VerifySQLiteBackfill(context.Background(), root+"/target.db", root+"/dataset.db", root+"/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	if readback.Ready || readback.ProjectionComplete || readback.Complete {
		t.Fatalf("source-only mandatory column must fail every readback gate: %+v", readback)
	}
	if got := readback.IgnoredSourceColumns["model_check_runs"]; len(got) != 1 || got[0] != "legacy_node_extra" {
		t.Fatalf("source-only column evidence missing: %+v", readback.IgnoredSourceColumns)
	}
}

func TestJ3bSQLiteBackfillHandlesLegacyExtraColumnsAndMultipleRows(t *testing.T) {
	root := t.TempDir()
	targetPath, datasetPath, statsPath := root+"/target.db", root+"/dataset.db", root+"/stats.db"
	target, err := OpenSQLite(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunSQLite(context.Background(), target, true); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	dataset, err := OpenSQLite(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunSQLite(context.Background(), dataset, true); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	// A source-only column must fail before any partial projection is committed.
	if _, err := dataset.Exec(`ALTER TABLE model_check_runs ADD COLUMN legacy_node_extra TEXT`); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	insertRun := `INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at,legacy_node_extra) VALUES (` + strings.TrimSuffix(strings.Repeat("?,", 24), ",") + ")"
	values := func(id string, score int, extra string) []any {
		return []any{id, "sys", "actor", "openai", "account", "acct", "acct", "gpt-5.6", "quick", "manual", "completed", "success", score, 100, "ok", "{}", "{}", `{}`,
			`{}`, "legacy-node-v1", "2026-08-27T10:00:00Z", "2026-08-27T10:00:00Z", "2026-08-27T10:00:00Z", extra}
	}
	// Insert in reverse order; the copier must impose primary-key order rather
	// than depending on SQLite's incidental insertion order.
	run2 := values("run-2", 80, "legacy-b")
	if _, err := dataset.Exec(insertRun, run2...); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	run1 := values("run-1", 90, "legacy-a")
	if _, err := dataset.Exec(insertRun, run1...); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	if err := dataset.Close(); err != nil {
		t.Fatal(err)
	}
	stats, err := OpenSQLite(statsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunSQLite(context.Background(), stats, true); err != nil {
		stats.Close()
		t.Fatal(err)
	}
	if _, err := stats.Exec(`INSERT INTO account_quality_health_hourly(account_id,system_account_id,provider_code,stat_hour,observed_at,model_check_run_id,model,profile,score,threshold,level,updated_at) VALUES ('acct','sys','openai','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','run-1','gpt-5.6','quick',90,70,'success','2026-08-27T10:00:00Z')`); err != nil {
		stats.Close()
		t.Fatal(err)
	}
	if _, err := stats.Exec(`INSERT INTO model_token_intercept_baseline_versions(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,version_status,evidence_status,independent_source_count,retained_source_count,excluded_source_count,q90_intercept,strong_gate_enabled,first_observed_at,last_observed_at,updated_at) VALUES ('hmac-sha256-v1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','gpt-5.6','o200k_base@1','probe-v1',1,'calibration_pending','stable',10,10,0,120,0,'2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		stats.Close()
		t.Fatal(err)
	}
	if err := stats.Close(); err != nil {
		t.Fatal(err)
	}
	target, err = OpenSQLite(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	report, err := BackfillSQLite(context.Background(), target, datasetPath, statsPath)
	if err == nil || !strings.Contains(err.Error(), "unmapped source columns") {
		t.Fatalf("source-only column must fail before partial write, report=%+v err=%v", report, err)
	}
	var rows int
	if err := target.QueryRow(`SELECT COUNT(*) FROM model_check_runs`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("source-only rejection must leave target unchanged, rows=%d", rows)
	}
}

func TestJ3bSQLiteBackfillReadbackDetectsDriftAndSharedPaths(t *testing.T) {
	root := t.TempDir()
	targetPath, datasetPath, statsPath := root+"/target.db", root+"/dataset.db", root+"/stats.db"
	for _, path := range []string{targetPath, datasetPath, statsPath} {
		db, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RunSQLite(context.Background(), db, true); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	dataset, err := OpenSQLite(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,probe_set_version,started_at,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',90,100,'ok','{}','{}','{}','{}','probe-v1','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`DROP TABLE model_check_input_versions`); err != nil {
		dataset.Close()
		t.Fatal(err)
	}
	if err := dataset.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := OpenSQLite(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillSQLite(context.Background(), target, datasetPath, statsPath); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if _, err := target.Exec(`INSERT INTO model_check_input_versions(identity_key,next_version,updated_at) VALUES('go-owned',1,'2026-08-28T00:00:00Z')`); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := VerifySQLiteBackfill(context.Background(), targetPath, datasetPath, statsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || !report.ProjectionComplete || !report.Complete || !report.SourceReadOnly || !report.StatsReadOnly || !report.TargetReadOnly || report.Tables["model_check_runs"] != "match" || report.Tables["model_check_input_versions"] != "optional source absent; target retained" || report.TargetRows["model_check_input_versions"] != 1 {
		t.Fatalf("expected readback match, report=%+v", report)
	}
	drift, err := OpenSQLite(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drift.Exec(`UPDATE model_check_runs SET score=1 WHERE id='run-1'`); err != nil {
		drift.Close()
		t.Fatal(err)
	}
	drift.Close()
	report, err = VerifySQLiteBackfill(context.Background(), targetPath, datasetPath, statsPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.Tables["model_check_runs"] != "drift" {
		t.Fatalf("expected readback drift, report=%+v", report)
	}
	shared, err := VerifySQLiteBackfill(context.Background(), targetPath, targetPath, statsPath)
	if err != nil {
		t.Fatal(err)
	}
	if shared.Ready || shared.PathsDistinct {
		t.Fatalf("shared paths must fail closed, report=%+v", shared)
	}
	sharedTarget, err := OpenSQLite(datasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillSQLite(context.Background(), sharedTarget, datasetPath, statsPath); err == nil {
		sharedTarget.Close()
		t.Fatal("backfill must reject a target that shares the dataset physical file")
	}
	if err := sharedTarget.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSQLiteBackfillPathsAllowsNewTargetAndRejectsSharedSource(t *testing.T) {
	root := t.TempDir()
	datasetPath, statsPath := root+"/dataset.db", root+"/stats.db"
	for _, path := range []string{datasetPath, statsPath} {
		db, err := OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RunSQLite(context.Background(), db, true); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateSQLiteBackfillPaths(root+"/new-target.db", datasetPath, statsPath); err != nil {
		t.Fatalf("new target should be allowed before OpenSQLite creates it: %v", err)
	}
	if err := ValidateSQLiteBackfillPaths(datasetPath, datasetPath, statsPath); err == nil {
		t.Fatal("target and dataset must be rejected when they share a path")
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
