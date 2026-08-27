package j3bmodelcheck

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "modernc.org/sqlite"
)

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
	stats, err := OpenSQLite(root + "/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	if _, err := RunSQLite(context.Background(), stats, true); err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Exec(`INSERT INTO model_check_runs(id,system_account_id,actor_system_account_id,provider_code,target_type,target_id,account_id,model,profile,trigger_kind,status,level,score,max_score,message,request_summary_json,result_summary_json,policy_snapshot_json,quality_decision_json,created_at,updated_at) VALUES ('run-1','sys','actor','openai','account','acct','acct','gpt-5.6','quick','manual','completed','success',90,100,'ok','{}','{}','{"threshold":70}','{}','2026-08-27T10:00:00Z','2026-08-27T10:00:00Z')`); err != nil {
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
	if report.InsertedRows["model_check_runs"] != 1 || report.InsertedRows["model_check_items"] != 1 || report.InsertedRows["model_check_observations"] != 1 || report.InsertedRows["account_quality_health_hourly"] != 1 {
		t.Fatalf("report=%+v", report)
	}
	report, err = BackfillSQLite(context.Background(), target, root+"/dataset.db", root+"/stats.db")
	if err != nil {
		t.Fatal(err)
	}
	if report.InsertedRows["model_check_runs"] != 0 || report.TargetRows["model_check_runs"] != 1 {
		t.Fatalf("idempotent report=%+v", report)
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
	for _, forbidden := range []string{"juhe_business", "goose_db_version", "drop ", "alter table"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("DDL must not touch %q", forbidden)
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
