package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStoreSchemaCheckFailsClosedWhenMigrationIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckSchema(context.Background()); err == nil {
		t.Fatal("missing J3b schema must fail closed")
	}
}

func TestOpenStoreRejectsConfirmedHandoffWhileNodeWriterIsActive(t *testing.T) {
	_, err := OpenStore(Config{
		Enabled: true, StoreMode: "sqlite", DatabasePath: filepath.Join(t.TempDir(), "j3b.db"),
		BusinessHandoffConfirmed: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true,
	})
	if err == nil || !strings.Contains(err.Error(), "readiness gates") {
		t.Fatalf("confirmed handoff with active Node writer must fail closed, err=%v", err)
	}
}

func TestStoreSchemaCheckDoesNotCreateTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for table, columns := range requiredColumns {
		definitions := make([]string, 0, len(columns))
		for _, column := range columns {
			definitions = append(definitions, column+" TEXT")
		}
		if _, err := seed.Exec(`CREATE TABLE ` + table + ` (` + strings.Join(definitions, ",") + `)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSchemaCheckRejectsMissingRequiredColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for table, columns := range requiredColumns {
		definitions := make([]string, 0, len(columns))
		for _, column := range columns {
			if table == "model_check_runs" && column == "quality_decision_json" {
				continue
			}
			definitions = append(definitions, column+" TEXT")
		}
		if _, err := seed.Exec(`CREATE TABLE ` + table + ` (` + strings.Join(definitions, ",") + `)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "quality_decision_json") {
		t.Fatalf("missing required column must fail closed, err=%v", err)
	}
}

func TestStoreSchemaCheckRejectsRuntimeProjectionColumnDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
		col   string
	}{
		{name: "run item score", table: "model_check_items", col: "score"},
		{name: "run item max score", table: "model_check_items", col: "max_score"},
		{name: "run item duration", table: "model_check_items", col: "duration_ms"},
		{name: "run item trace", table: "model_check_items", col: "trace_id"},
		{name: "run item error code", table: "model_check_items", col: "error_code"},
		{name: "run item error message", table: "model_check_items", col: "error_message"},
		{name: "health error code", table: "account_quality_health_hourly", col: "error_code"},
		{name: "health error message", table: "account_quality_health_hourly", col: "error_message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "j3b-drift.db")
			seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
			if err != nil {
				t.Fatal(err)
			}
			for table, columns := range requiredColumns {
				definitions := make([]string, 0, len(columns))
				for _, column := range columns {
					if table == tc.table && column == tc.col {
						continue
					}
					definitions = append(definitions, column+" TEXT")
				}
				if _, err := seed.Exec(`CREATE TABLE ` + table + ` (` + strings.Join(definitions, ",") + `)`); err != nil {
					seed.Close()
					t.Fatal(err)
				}
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(testSQLiteConfig(path))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			err = store.CheckSchema(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.table) || !strings.Contains(err.Error(), tc.col) {
				t.Fatalf("schema drift must fail closed for %s.%s, err=%v", tc.table, tc.col, err)
			}
		})
	}
}

func TestActivateTokenInterceptBaselineUsesAtomicCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE model_token_intercept_baseline_versions (
		cohort_key_hmac TEXT NOT NULL, requested_model TEXT NOT NULL, tokenizer_version TEXT NOT NULL,
		probe_set_version TEXT NOT NULL, baseline_version INTEGER NOT NULL, version_status TEXT NOT NULL,
		evidence_status TEXT NOT NULL, independent_source_count INTEGER NOT NULL, q90_intercept REAL,
		strong_threshold_intercept REAL, strong_gate_enabled INTEGER NOT NULL, calibration_note TEXT,
		updated_at TEXT NOT NULL, PRIMARY KEY(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version))`)
	if err != nil {
		t.Fatal(err)
	}
	const cohort = "hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, row := range []struct {
		version int
		status  string
		gate    int
	}{
		{version: 1, status: "active", gate: 1},
		{version: 2, status: "calibration_pending", gate: 0},
	} {
		_, err = db.Exec(`INSERT INTO model_token_intercept_baseline_versions (cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version,version_status,evidence_status,independent_source_count,q90_intercept,strong_threshold_intercept,strong_gate_enabled,calibration_note,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, cohort, "gpt-5.6", "o200k_base@1", "probe-v1", row.version, row.status, "stable", 10, 120, nil, row.gate, nil, "2026-08-27T10:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{db: db, mode: "sqlite"}
	input := TokenInterceptBaselineActivation{CohortKeyHMAC: cohort, RequestedModel: "gpt-5.6", TokenizerVersion: "o200k_base@1", ProbeSetVersion: "probe-v1", BaselineVersion: 2, StrongThresholdIntercept: 128, CalibrationNote: "calibrated"}
	if err := store.ActivateTokenInterceptBaseline(context.Background(), input); err != nil {
		t.Fatalf("activate err=%v", err)
	}
	var status1, status2 string
	var gate1, gate2 int
	if err := db.QueryRow(`SELECT version_status,strong_gate_enabled FROM model_token_intercept_baseline_versions WHERE baseline_version=1`).Scan(&status1, &gate1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT version_status,strong_gate_enabled FROM model_token_intercept_baseline_versions WHERE baseline_version=2`).Scan(&status2, &gate2); err != nil {
		t.Fatal(err)
	}
	if status1 != "retired" || gate1 != 0 || status2 != "active" || gate2 != 1 {
		t.Fatalf("activation statuses old=%s/%d new=%s/%d", status1, gate1, status2, gate2)
	}
	if err := store.ActivateTokenInterceptBaseline(context.Background(), input); !errors.Is(err, ErrTokenInterceptBaselineConflict) {
		t.Fatalf("replay must conflict, err=%v", err)
	}
}

func TestActivateTokenInterceptBaselineFailsClosedWhenStorageMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-baseline.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, mode: "sqlite"}
	err = store.ActivateTokenInterceptBaseline(context.Background(), TokenInterceptBaselineActivation{
		CohortKeyHMAC: "hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestedModel: "gpt-5.6", TokenizerVersion: "o200k_base@1", ProbeSetVersion: "probe-v1", BaselineVersion: 1, StrongThresholdIntercept: 100, CalibrationNote: "ok",
	})
	if !errors.Is(err, ErrTokenInterceptBaselineUnavailable) {
		t.Fatalf("missing table must return unavailable, err=%v", err)
	}
}

func TestApplyHealthFactUsesLatestWinsOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL, score INTEGER NOT NULL, threshold INTEGER NOT NULL, level TEXT NOT NULL, error_code TEXT, error_message TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,stat_hour))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := HealthFact{AccountID: "a", SystemAccountID: "sys", ProviderCode: "gpt", StatHour: "2026-08-27T10:00:00Z", ObservedAt: time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC), RunID: "run-2", Model: "gpt-5.6-terra", Profile: "quick", Score: 30, Threshold: 70, Level: "failure"}
	if applied, err := store.ApplyHealthFact(context.Background(), base); err != nil || !applied {
		t.Fatalf("initial applied=%v err=%v", applied, err)
	}
	older := base
	older.RunID = "run-9"
	older.ObservedAt = base.ObservedAt.Add(-time.Minute)
	older.Score = 90
	if applied, err := store.ApplyHealthFact(context.Background(), older); err != nil || applied {
		t.Fatalf("older applied=%v err=%v", applied, err)
	}
	tieOlder := base
	tieOlder.RunID = "run-1"
	tieOlder.Score = 95
	if applied, err := store.ApplyHealthFact(context.Background(), tieOlder); err != nil || applied {
		t.Fatalf("tie older applied=%v err=%v", applied, err)
	}
	tieNewer := base
	tieNewer.RunID = "run-3"
	tieNewer.Score = 40
	if applied, err := store.ApplyHealthFact(context.Background(), tieNewer); err != nil || !applied {
		t.Fatalf("tie newer applied=%v err=%v", applied, err)
	}
	got, found, err := store.ReadHealthFact(context.Background(), base.AccountID, base.StatHour)
	if err != nil || !found {
		t.Fatalf("read health fact found=%v err=%v", found, err)
	}
	if got.RunID != "run-3" || got.Score != 40 || got.Level != "failure" {
		t.Fatalf("read health fact=%+v", got)
	}
	if _, found, err := store.ReadHealthFact(context.Background(), base.AccountID, "2026-08-27T11:00:00Z"); err != nil || found {
		t.Fatalf("missing health fact found=%v err=%v", found, err)
	}
}

func TestListHealthSyncRetriesSkipsInvalidRowsAndPreservesFailureState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health-retry.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,status TEXT,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,quality_health_sync_status TEXT,updated_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	finished := "2026-08-27T10:15:00Z"
	_, err = db.Exec(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "run-1", "completed", "acct-1", "sys-1", "openai", "gpt-5.6", "full", "failure", 12, nil, `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`, `{"evidenceFormed":true,"trustFormed":true}`, `{"configRevision":"3"}`, finished, "failed", finished)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite", HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	retries, err := store.ListHealthSyncRetries(context.Background(), 10)
	if err != nil || len(retries) != 1 {
		t.Fatalf("retries=%#v err=%v", retries, err)
	}
	if retries[0].Threshold != 70 || retries[0].StatHour != "2026-08-27T18" {
		t.Fatalf("retry=%#v", retries[0])
	}
	if !retries[0].EvidenceFormed || !retries[0].TrustFormed {
		t.Fatalf("retry evidence=%#v", retries[0])
	}
	if _, err := db.Exec(`UPDATE model_check_runs SET policy_snapshot_json='{}' WHERE id='run-1'`); err != nil {
		t.Fatal(err)
	}
	retries, err = store.ListHealthSyncRetries(context.Background(), 10)
	if err != nil || len(retries) != 0 {
		t.Fatalf("invalid retry must be skipped without blocking scan: retries=%#v err=%v", retries, err)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-1'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("invalid retry state=%q err=%v, want durable failed", state, err)
	}
}

func TestListHealthSyncRetriesSkipsMalformedRowsBeforeLaterValidRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health-retry-batch.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY,status TEXT,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,quality_health_sync_status TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	finished := "2026-08-27T10:15:00Z"
	validPolicy := `{"revision":"policy-1","threshold":70,"action":"quality_isolate","recoveryIntervalMinutes":10}`
	validDecision := `{"evidenceFormed":true,"trustFormed":true}`
	if _, err := db.Exec(`INSERT INTO model_check_runs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"run-bad", "completed", "acct-bad", "sys-bad", "openai", "gpt-5.6", "full", "failure", 20, nil, `{}`, validDecision, `{"configRevision":"3"}`, finished, "failed", "2026-08-27T10:00:00Z",
		"run-valid", "completed", "acct-valid", "sys-valid", "openai", "gpt-5.6", "full", "failure", 20, nil, validPolicy, validDecision, `{"configRevision":"3"}`, finished, "failed", "2026-08-27T10:01:00Z"); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, mode: "sqlite", HealthStatHour: mustHealthStatHourFunc(t, "Asia/Shanghai")}
	retries, err := store.ListHealthSyncRetries(context.Background(), 1)
	if err != nil || len(retries) != 1 || retries[0].RunID != "run-valid" {
		t.Fatalf("retries=%#v err=%v", retries, err)
	}
	var state string
	if err := db.QueryRow(`SELECT quality_health_sync_status FROM model_check_runs WHERE id='run-bad'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("malformed run state=%q err=%v, want durable failed", state, err)
	}
}

func TestIssueInputIsImmutableAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL, UNIQUE(identity_key,input_version), UNIQUE(identity_key,input_digest))`,
	} {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := InputRecord{InputID: "input-1", IdentityKey: "account:a", TargetID: "a", ConfigRevision: "cfg-1", PolicyRevision: "pol-1", Trigger: "manual", IssuedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2026, 8, 27, 10, 5, 0, 0, time.UTC), Payload: json.RawMessage(`{"model":"gpt-5.6"}`)}
	issued, err := store.IssueInput(context.Background(), base)
	if err != nil || issued.InputDigest == "" {
		t.Fatalf("issue input=%+v err=%v", issued, err)
	}
	replay := base
	replay.Payload = json.RawMessage(`{ "model": "gpt-5.6" }`)
	replayed, err := store.IssueInput(context.Background(), replay)
	if err != nil || replayed.InputDigest != issued.InputDigest {
		t.Fatalf("idempotent replay=%+v err=%v", replayed, err)
	}
	conflict := base
	conflict.Payload = json.RawMessage(`{"model":"gpt-5.6-mini"}`)
	if _, err := store.IssueInput(context.Background(), conflict); !errors.Is(err, ErrInputConflict) {
		t.Fatalf("different immutable payload must conflict, err=%v", err)
	}
	policyConflict := base
	policyConflict.PolicyRevision = "pol-2"
	if _, err := store.IssueInput(context.Background(), policyConflict); !errors.Is(err, ErrInputConflict) {
		t.Fatalf("different immutable policy revision must conflict, err=%v", err)
	}
}

func TestRunLifecycleProjectionIsAtomicAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, account_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL, trigger_kind TEXT NOT NULL, schedule_id TEXT, status TEXT NOT NULL, request_summary_json TEXT NOT NULL, result_summary_json TEXT NOT NULL, policy_snapshot_json TEXT NOT NULL, quality_decision_json TEXT NOT NULL, probe_set_version TEXT NOT NULL, started_at TEXT NOT NULL, trace_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, level TEXT NOT NULL DEFAULT 'unavailable', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 100, message TEXT NOT NULL DEFAULT '', finished_at TEXT, quality_health_sync_status TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(`CREATE TABLE model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	if err := store.CreateRun(context.Background(), RunRecord{ID: "run-1", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "v1", StartedAt: started, RequestSummary: json.RawMessage(`{"model":"gpt-5.6"}`), PolicySnapshot: json.RawMessage(`{"threshold":70}`)}); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(time.Second)
	projection := OutcomeProjection{RunID: "run-1", Status: RunCompleted, Level: "success", Score: 100, MaxScore: 100, FinishedAt: finished, ResultSummary: json.RawMessage(`{"ok":true}`), QualityDecision: json.RawMessage(`{"action":"none"}`), Items: []ItemRecord{{ID: "item-1", RunID: "run-1", ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 100, MaxScore: 100, EvidenceSummary: `{}`}}}
	if err := store.ProjectOutcome(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectOutcome(context.Background(), projection); err != nil {
		t.Fatalf("identical terminal replay should succeed: %v", err)
	}
	conflict := projection
	conflict.Score = 20
	if err := store.ProjectOutcome(context.Background(), conflict); !errors.Is(err, ErrRunProjectionConflict) {
		t.Fatalf("different terminal replay must conflict: %v", err)
	}
	var status string
	var itemCount int
	if err := store.db.QueryRow(`SELECT status FROM model_check_runs WHERE id='run-1'`).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id='run-1'`).Scan(&itemCount); err != nil || itemCount != 1 {
		t.Fatalf("item count=%d err=%v", itemCount, err)
	}
}

func TestClaimAndOutcomeFenceLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE TABLE model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, claim_until TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL)`,
	} {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"model":"gpt-5.6"}`)
	digest := digestPayload(payload)
	if _, err := store.db.Exec(`INSERT INTO model_check_inputs VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "input-1", "account:a", 1, digest, "a", "cfg", "pol", "manual", now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), []byte(payload)); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimInput(context.Background(), "input-1", "token-1", "outcome-1", "owner-1", time.Minute, now)
	if err != nil || first.FenceToken != 1 {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := store.ClaimInput(context.Background(), "input-1", "token-2", "outcome-2", "owner-2", time.Minute, now.Add(10*time.Second)); !errors.Is(err, ErrClaimBusy) {
		t.Fatalf("live competing claim must be busy: %v", err)
	}
	if err := store.CommitOutcome(context.Background(), Outcome{OutcomeID: "outcome-1", InputID: "input-1", InputDigest: digest, Payload: payload}, first, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(context.Background(), Outcome{OutcomeID: "outcome-1", InputID: "input-1", InputDigest: digest, Payload: payload}, first, now.Add(21*time.Second)); err != nil {
		t.Fatalf("identical outcome replay should succeed: %v", err)
	}
	if err := store.ReleaseClaim(context.Background(), first, now.Add(22*time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimInput(context.Background(), "input-1", "token-3", "outcome-3", "owner-3", time.Minute, now.Add(2*time.Minute))
	if err != nil || second.FenceToken != 2 {
		t.Fatalf("expired claim takeover=%+v err=%v", second, err)
	}
	if err := store.ReleaseClaim(context.Background(), first, now.Add(2*time.Minute+time.Second)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("old fence release must fail: %v", err)
	}
}

func TestLoadInputAndListCommittedOutcomesVerifyDigests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j3b.db")
	seed, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE TABLE model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL)`,
	} {
		if _, err := seed.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(testSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	payload := json.RawMessage(`{"a":1}`)
	if _, err := store.IssueInput(context.Background(), InputRecord{InputID: "input-1", IdentityKey: "account:a", TargetID: "a", ConfigRevision: "cfg", PolicyRevision: "pol", Trigger: "manual", IssuedAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadInput(context.Background(), "input-1", now.Add(time.Second))
	if err != nil || string(loaded.Payload) != `{"a":1}` {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	outPayload := json.RawMessage(`{"ok":true}`)
	if _, err := store.db.Exec(`INSERT INTO model_check_outcomes VALUES(?,?,?,?,?,?,?,?,?)`, "out-1", "input-1", loaded.InputDigest, 1, now.Format(time.RFC3339Nano), now.Add(time.Second).Format(time.RFC3339Nano), []byte(outPayload), digestPayload(outPayload), true); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListCommittedOutcomes(context.Background(), OutcomeCursor{}, 10)
	if err != nil || len(rows) != 1 || rows[0].Outcome.OutcomeID != "out-1" {
		t.Fatalf("outcomes=%+v err=%v", rows, err)
	}
	rows, err = store.ListCommittedOutcomes(context.Background(), OutcomeCursor{StoredAt: now.Add(time.Second), OutcomeID: "out-1"}, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("cursor after outcome rows=%+v err=%v", rows, err)
	}
}

func testSQLiteConfig(path string) Config {
	return Config{Enabled: true, StoreMode: "sqlite", DatabasePath: path, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true}
}
