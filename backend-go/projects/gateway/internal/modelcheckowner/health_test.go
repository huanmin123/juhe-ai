package modelcheckowner

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCompareLatestWinsObservedAtThenRunID(t *testing.T) {
	base := HealthFact{AccountID: "a", StatHour: "hour", RunID: "run-1", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC)}
	newer := base
	newer.ObservedAt = base.ObservedAt.Add(time.Second)
	if got, err := CompareLatestWins(newer, base); err != nil || got != 1 {
		t.Fatalf("newer compare=%d err=%v", got, err)
	}
	tie := base
	tie.RunID = "run-2"
	if got, err := CompareLatestWins(tie, base); err != nil || got != 1 {
		t.Fatalf("tie compare=%d err=%v", got, err)
	}
	if got, err := CompareLatestWins(base, tie); err != nil || got != -1 {
		t.Fatalf("older tie compare=%d err=%v", got, err)
	}
}

func TestQualityProjectorDeniesUnformedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quality.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-1','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}}
	err = projector.Project(context.Background(), "run-1", EvidenceAggregate{}, HealthFact{})
	if err == nil {
		t.Fatal("unformed evidence must be rejected")
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("health sync state=%q, want failed", state)
	}
}

func TestQualityProjectorMarksRetryWhenCallerContextIsCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canceled-quality.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-canceled','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}}
	if err := projector.Project(ctx, "run-canceled", EvidenceAggregate{}, HealthFact{}); err == nil {
		t.Fatal("unformed evidence must be rejected")
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-canceled'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("canceled caller must still persist retry state=%q err=%v", state, err)
	}
}

type recordingEnforcement struct{ calls int }

func (r *recordingEnforcement) Apply(_ context.Context, enforcement QualityEnforcement) error {
	r.calls++
	if enforcement.Action != "quality_isolate" || enforcement.Score >= enforcement.Threshold {
		return errors.New("invalid enforcement request")
	}
	return nil
}

type countingEnforcement struct{ calls int }

func (r *countingEnforcement) Apply(_ context.Context, _ QualityEnforcement) error {
	r.calls++
	return nil
}

func TestQualityProjectorTreatsSuspiciousAsHardFailureAboveThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suspicious-health.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,quality_health_sync_status,updated_at) VALUES ('run-suspicious','pending','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	enforcement := &countingEnforcement{}
	store := &Store{db: db, mode: "sqlite"}
	projector := &QualityProjector{Store: store, Enforcement: enforcement}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", StatHour: "2026-08-27T10:00:00Z", RunID: "run-suspicious", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 96, Threshold: 70, Level: "suspicious", PenaltyAction: "quality_isolate", EnforcementAllowed: true}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
		t.Fatal(err)
	}
	if enforcement.calls != 1 {
		t.Fatalf("suspicious hard failure enforcement calls=%d, want 1", enforcement.calls)
	}
	var level string
	if err := db.QueryRow(`SELECT level FROM account_quality_health_hourly WHERE account_id='acct'`).Scan(&level); err != nil || level != "suspicious" {
		t.Fatalf("health level=%q err=%v, want suspicious", level, err)
	}
}

func TestQualityProjectorRequiresEnforcementForFormedFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enforcement.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,quality_health_sync_status,updated_at) VALUES ('run-failure','pending_retry','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite"}
	projector := &QualityProjector{Store: store}
	fact := HealthFact{AccountID: "acct-1", SystemAccountID: "sys-1", StatHour: "2026-08-27T10:00:00Z", RunID: "run-failure", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 30, Threshold: 70, Level: "failure", EnforcementAllowed: true}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "enforcement") {
		t.Fatalf("missing enforcement err=%v", err)
	}
	recorder := &recordingEnforcement{}
	projector.Enforcement = recorder
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
		t.Fatal(err)
	}
	if recorder.calls != 1 {
		t.Fatalf("enforcement calls=%d", recorder.calls)
	}
}

func TestQualityProjectorPublishesDiagnosticManualFailureWithoutEnforcement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic-health.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,quality_health_sync_status,updated_at) VALUES ('run-diagnostic','pending_retry','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEnforcement{}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}, Enforcement: recorder}
	fact := HealthFact{AccountID: "acct-1", SystemAccountID: "sys-1", StatHour: "2026-08-27T10:00:00Z", RunID: "run-diagnostic", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 30, Threshold: 70, Level: "failure", EnforcementAllowed: false}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
		t.Fatal(err)
	}
	if recorder.calls != 0 {
		t.Fatalf("diagnostic manual failure must not enforce, calls=%d", recorder.calls)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-diagnostic'`).Scan(&state); err != nil || state != "applied" {
		t.Fatalf("health fact must still publish: state=%q err=%v", state, err)
	}
}

func TestQualityProjectorRejectsRunIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-a','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", StatHour: "2026-08-27T10:00:00Z", RunID: "run-b", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 90, Threshold: 70, Level: "success"}
	if err := projector.Project(context.Background(), "run-a", EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("mismatch must fail closed, err=%v", err)
	}
}

func TestQualityProjectorUnformedIdentityMismatchDoesNotMutateRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unformed-identity.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-a','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}}
	fact := HealthFact{RunID: "run-b"}
	if err := projector.Project(context.Background(), "run-a", EvidenceAggregate{}, fact); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("err=%v", err)
	}
	var state sql.NullString
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-a'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state.Valid {
		t.Fatalf("identity mismatch must not mutate unrelated run: %q", state.String)
	}
}

func TestQualityProjectorUnavailableNeverEnforces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unavailable-enforcement.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,quality_health_sync_status,updated_at) VALUES ('run-u','pending_retry','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEnforcement{}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}, Enforcement: recorder}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", StatHour: "2026-08-27T10:00:00Z", RunID: "run-u", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 0, Threshold: 70, Level: "unavailable", EnforcementAllowed: true}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err != nil {
		t.Fatal(err)
	}
	if recorder.calls != 0 {
		t.Fatalf("unavailable fact must not enforce, calls=%d", recorder.calls)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-u'`).Scan(&state); err != nil || state != "applied" {
		t.Fatalf("unavailable health fact must publish, state=%q err=%v", state, err)
	}
	var level string
	if err := db.QueryRow(`SELECT level FROM account_quality_health_hourly WHERE account_id='acct' AND stat_hour='2026-08-27T10:00:00Z'`).Scan(&level); err != nil || level != "unavailable" {
		t.Fatalf("unavailable health fact level=%q err=%v", level, err)
	}
}

func TestQualityProjectorPublishesUnformedQuickFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quick-unformed-health.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,quality_health_sync_status,updated_at) VALUES ('run-quick-unformed','pending_retry','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEnforcement{}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}, Enforcement: recorder}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", StatHour: "2026-08-27T10:00:00Z", RunID: "run-quick-unformed", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 30, Threshold: 70, Level: "uncertain", PenaltyAction: "quality_isolate", EnforcementAllowed: true}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: false, TrustFormed: false}, fact); err != nil {
		t.Fatal(err)
	}
	if recorder.calls != 1 {
		t.Fatalf("quick quality failure should follow Node enforcement path, calls=%d", recorder.calls)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-quick-unformed'`).Scan(&state); err != nil || state != "applied" {
		t.Fatalf("quick unformed health state=%q err=%v", state, err)
	}
}

func TestQualityProjectorRejectsSuccessfulHealthFact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "successful-health.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-success','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", StatHour: "2026-08-27T10:00:00Z", RunID: "run-success", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 90, Threshold: 70, Level: "success"}
	if err := projector.Project(context.Background(), fact.RunID, EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "quality failure") {
		t.Fatalf("successful health fact must be rejected, err=%v", err)
	}
	var state sql.NullString
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-success'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state.Valid {
		t.Fatalf("successful fact must not poison retry state, got %q", state.String)
	}
}

func TestQualityProjectorRejectsInvalidStatHourBeforeEnforcement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat-hour.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,updated_at) VALUES ('run-a','2026-08-27T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	enforcement := &recordingEnforcement{}
	projector := &QualityProjector{Store: &Store{db: db, mode: "sqlite"}, Enforcement: enforcement}
	fact := HealthFact{AccountID: "acct", SystemAccountID: "sys", ProviderCode: "openai", Model: "gpt-5.6", Profile: "quick", StatHour: "2026-08-27T10:15:00Z", RunID: "run-a", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), Score: 20, Threshold: 70, Level: "failure"}
	if err := projector.Project(context.Background(), "run-a", EvidenceAggregate{Formed: true, TrustFormed: true}, fact); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("invalid stat hour must fail closed, err=%v", err)
	}
	if enforcement.calls != 0 {
		t.Fatalf("invalid health fact must not trigger enforcement, calls=%d", enforcement.calls)
	}
}

func TestHealthSyncRetryExecutorRequiresRunIDPayload(t *testing.T) {
	executor := &HealthSyncRetryExecutor{Projector: &QualityProjector{Store: &Store{}}}
	if err := executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("health retry without runId must fail closed")
	}
}

func TestHealthSyncRetryExecutorReplaysFailedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retry.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,status TEXT,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,quality_health_sync_status TEXT,updated_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	finished := "2026-08-27T10:15:00Z"
	if _, err := db.Exec(`INSERT INTO model_check_runs(id,status,account_id,system_account_id,provider_code,model,profile,level,score,schedule_id,policy_snapshot_json,quality_decision_json,request_summary_json,finished_at,quality_health_sync_status,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"run-retry", "completed", "acct-1", "sys-1", "openai", "gpt-5.6-sol", "quick", "failure", 30, nil,
		`{"revision":"policy-7","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`,
		`{"evidenceFormed":true,"trustFormed":true,"enforcementAllowed":true}`,
		`{"configRevision":"3"}`,
		finished, "failed", finished); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite", HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	enforcement := &recordingEnforcement{}
	executor := &HealthSyncRetryExecutor{Projector: &QualityProjector{Store: store, Enforcement: enforcement}}
	err = executor.Execute(context.Background(), ScheduleTask{Kind: SchedulerHealthRetry, Payload: []byte(`{"runId":"run-retry"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-retry'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "applied" {
		t.Fatalf("health sync state=%q, want applied", state)
	}
	if enforcement.calls != 1 {
		t.Fatalf("health retry enforcement calls=%d, want 1", enforcement.calls)
	}
	retries, err := (&Store{db: db, mode: "sqlite", HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}).ListHealthSyncRetries(context.Background(), 10)
	if err != nil || len(retries) != 0 {
		t.Fatalf("completed run should not remain retryable: retries=%#v err=%v", retries, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_quality_health_hourly WHERE account_id='acct-1' AND stat_hour='2026-08-27T18'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("health row count=%d, want 1", count)
	}
}

func TestCompareLatestWinsRejectsScopeMismatch(t *testing.T) {
	base := HealthFact{AccountID: "a", StatHour: "hour", RunID: "run", ObservedAt: time.Now()}
	other := base
	other.AccountID = "b"
	if _, err := CompareLatestWins(other, base); err == nil {
		t.Fatal("scope mismatch must fail closed")
	}
}
