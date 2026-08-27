package modelcheckstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunItemObservationLifecycleAndTerminalFence(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.CreateRun(ctx, RunInput{ID: "run-1", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "gpt", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, ItemInput{ID: "item-1", RunID: "run-1", ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendObservation(ctx, ObservationInput{ID: "obs-1", RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "gpt", ProviderProtocolProfileID: "profile_gpt_openai_v1", EndpointFamily: "responses", RequestedModel: "gpt-5.6-sol", MappedUpstreamModel: "gpt-5.6-sol", UpstreamBucketHMAC: "u", CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k", ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t1", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "run-1", RunCompleted, "likely", 10, 10, "ok", now.Add(time.Second), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, ItemInput{ID: "late-item", RunID: "run-1", ItemKey: "late", ItemType: "late", Status: ItemSkipped, Score: 0, MaxScore: 0}); err == nil {
		t.Fatal("terminal run must reject a late item")
	}
	if err := store.AppendObservation(ctx, ObservationInput{ID: "late-observation", RunID: "run-1", SystemAccountID: "sys", AccountID: "acct", ProviderCode: "gpt", ProviderProtocolProfileID: "profile_gpt_openai_v1", EndpointFamily: "responses", RequestedModel: "gpt-5.6-sol", MappedUpstreamModel: "gpt-5.6-sol", UpstreamBucketHMAC: "u", CohortKeyHMAC: "c", PopulationKeyHMAC: "p", ProbeKeyHMAC: "k", ProbeFamily: "basic", ProbeSetVersion: "v1", TokenizerVersion: "t1", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now}); err == nil {
		t.Fatal("terminal run must reject a late observation")
	}
	if err := store.FinishRun(ctx, "run-1", RunFailed, "unavailable", 0, 10, "late", now.Add(2*time.Second), nil, nil); err == nil {
		t.Fatal("terminal run must not be overwritten")
	}
	var status string
	var duration int64
	if err := db.QueryRow(`SELECT status, duration_ms FROM model_check_runs WHERE id='run-1'`).Scan(&status, &duration); err != nil || status != string(RunCompleted) || duration != 1000 {
		t.Fatalf("status=%q duration=%d err=%v", status, duration, err)
	}
}

func TestInvalidJSONIsNeutralizedAndDuplicateItemIDIsRejected(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewStore(db)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, RunInput{ID: "r", SystemAccountID: "s", ActorSystemAccountID: "a", ProviderCode: "gpt", TargetType: "account", TargetID: "x", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now, RequestSummary: []byte("not-json")}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, ItemInput{ID: "i", RunID: "r", ItemKey: "k", ItemType: "t", Status: ItemSkipped, Score: 0, MaxScore: 0, EvidenceSummary: []byte("bad")}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, ItemInput{ID: "i", RunID: "r", ItemKey: "second-key", ItemType: "t", Status: ItemSkipped, Score: 0, MaxScore: 0}); err == nil {
		t.Fatal("duplicate item ID must be rejected")
	}
	var payload string
	if err := db.QueryRow(`SELECT request_summary_json FROM model_check_runs WHERE id='r'`).Scan(&payload); err != nil || payload != "{}" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}

func TestProjectOutcomeIsAtomicAndTerminalReplayIsStrict(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	if err := store.CreateRun(ctx, RunInput{ID: "project-run", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	projection := OutcomeProjection{
		RunID: "project-run", Status: RunCompleted, Level: "likely", Score: 18, MaxScore: 20, Message: "完成",
		FinishedAt: finished, ResultSummary: []byte(`{"items":2}`), QualityDecision: []byte(`{"enforcement":"none"}`),
		Items: []ItemInput{
			{ID: "project-item-a", RunID: "project-run", ItemKey: "basic", ItemType: "responses_basic", Status: ItemPassed, Score: 10, MaxScore: 10, DurationMS: ptrInt64(100), EvidenceSummary: []byte(`{"ok":true}`)},
			{ID: "project-item-b", RunID: "project-run", ItemKey: "structured", ItemType: "structured_output", Status: ItemWarning, Score: 8, MaxScore: 10, DurationMS: ptrInt64(200), ErrorMessage: "证据不足"},
		},
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_check_items WHERE run_id='project-run'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("projected item count=%d err=%v", count, err)
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("identical terminal replay must be idempotent: %v", err)
	}
	conflict := projection
	conflict.Score = 19
	if err := store.ProjectOutcome(ctx, conflict); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("different terminal replay must fail closed, err=%v", err)
	}
	bad := projection
	bad.RunID = "project-run"
	bad.Items = append([]ItemInput{{ID: "project-item-c", RunID: "project-run", ItemKey: "new", ItemType: "new", Status: ItemPassed, Score: 1, MaxScore: 1}}, bad.Items...)
	if err := store.ProjectOutcome(ctx, bad); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("terminal item-set drift must fail closed, err=%v", err)
	}
}

func TestUpdateQualityDecisionIsTerminalCASAndIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := store.CreateRun(ctx, RunInput{ID: "decision-run", SystemAccountID: "sys", ActorSystemAccountID: "actor", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "v1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	item := ItemInput{ID: "decision-item", RunID: "decision-run", ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10}
	if err := store.ProjectOutcome(ctx, OutcomeProjection{RunID: "decision-run", Items: []ItemInput{item}, Status: RunCompleted, Level: "likely", Score: 10, MaxScore: 10, Message: "ok", FinishedAt: now.Add(time.Second), ResultSummary: []byte(`{"score":10}`)}); err != nil {
		t.Fatal(err)
	}
	decision := []byte(`{"version":1,"outcomeDigest":"a","policyDigest":"b","evidenceDigest":"","decision":{"result":"not_triggered"}}`)
	update := QualityDecisionUpdate{RunID: "decision-run", Status: RunCompleted, ResultSummary: []byte(`{"score":10}`), PolicySnapshot: []byte(`{}`), Decision: decision}
	if err := store.UpdateQualityDecision(ctx, update); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateQualityDecision(ctx, update); err != nil {
		t.Fatalf("identical quality replay: %v", err)
	}
	conflict := update
	conflict.Decision = []byte(`{"different":true}`)
	if err := store.UpdateQualityDecision(ctx, conflict); !errors.Is(err, ErrQualityDecisionConflict) {
		t.Fatalf("quality drift must fail closed: %v", err)
	}
	wrongTerminal := update
	wrongTerminal.ResultSummary = []byte(`{"score":9}`)
	if err := store.UpdateQualityDecision(ctx, wrongTerminal); !errors.Is(err, ErrQualityDecisionConflict) {
		t.Fatalf("terminal summary drift must fail closed: %v", err)
	}
	missing := update
	missing.RunID = "missing"
	if err := store.UpdateQualityDecision(ctx, missing); err == nil {
		t.Fatal("missing run must fail")
	}
}

func ptrInt64(value int64) *int64 { return &value }

func TestPostgresBindQualifiesTablesAndNumbersPlaceholders(t *testing.T) {
	store := &Store{mode: StorePostgres}
	got := store.bind("SELECT * FROM model_check_runs WHERE id=? AND status=?")
	want := "SELECT * FROM juhe_dataset.model_check_runs WHERE id=$1 AND status=$2"
	if got != want {
		t.Fatalf("bind=%q want=%q", got, want)
	}
}

func TestPostgresRunStateQueriesTakeARowLock(t *testing.T) {
	store := &Store{mode: StorePostgres}
	got := store.lockRunQuery("SELECT status FROM model_check_runs WHERE id=?")
	want := "SELECT status FROM juhe_dataset.model_check_runs WHERE id=$1 FOR UPDATE"
	if got != want {
		t.Fatalf("lock query=%q want=%q", got, want)
	}
}

func TestCheckSchemaRejectsMissingRequiredSQLiteIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX idx_model_check_items_run_key`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "missing index idx_model_check_items_run_key") {
		t.Fatalf("missing index must fail closed, err=%v", err)
	}
}

func TestPostgresModelCheckSchemaContractSmoke(t *testing.T) {
	if os.Getenv("J3B_MODEL_CHECK_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3B_MODEL_CHECK_POSTGRES_SMOKE=1 to verify the configured development PostgreSQL dataset schema")
	}
	dsn := strings.TrimSpace(os.Getenv("JUHE_AI_MODEL_CHECK_POSTGRES_URL"))
	if dsn == "" {
		t.Fatal("JUHE_AI_MODEL_CHECK_POSTGRES_URL is required for the opt-in smoke")
	}
	store, err := OpenPostgres(dsn, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("J3b PostgreSQL schema contract: %v", err)
	}
}

func TestPostgresModelCheckWriterSmoke(t *testing.T) {
	if os.Getenv("J3B_MODEL_CHECK_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3B_MODEL_CHECK_POSTGRES_SMOKE=1 to verify the configured development PostgreSQL writer")
	}
	dsn := strings.TrimSpace(os.Getenv("JUHE_AI_MODEL_CHECK_POSTGRES_URL"))
	if dsn == "" {
		t.Fatal("JUHE_AI_MODEL_CHECK_POSTGRES_URL is required for the opt-in smoke")
	}
	store, err := OpenPostgres(dsn, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	runID := "j3b-pg-smoke-" + now.Format("20060102150405.000")
	defer func() {
		if _, cleanupErr := store.db.ExecContext(context.Background(), `DELETE FROM juhe_dataset.model_check_runs WHERE id=$1`, runID); cleanupErr != nil {
			t.Errorf("cleanup J3b PostgreSQL smoke run: %v", cleanupErr)
		}
	}()
	if err := store.CreateRun(ctx, RunInput{ID: runID, SystemAccountID: "j3b-smoke-system", ActorSystemAccountID: "j3b-smoke-actor", ProviderCode: "openai", TargetType: "account", TargetID: "j3b-smoke-account", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "j3b-smoke-v1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendItem(ctx, ItemInput{ID: runID + "-item", RunID: runID, ItemKey: "basic", ItemType: "basic", Status: ItemPassed, Score: 10, MaxScore: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendObservation(ctx, ObservationInput{ID: runID + "-observation", RunID: runID, SystemAccountID: "j3b-smoke-system", AccountID: "j3b-smoke-account", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1", EndpointFamily: "responses", RequestedModel: "gpt-5.6-sol", MappedUpstreamModel: "gpt-5.6-sol", UpstreamBucketHMAC: "smoke-upstream", CohortKeyHMAC: "smoke-cohort", PopulationKeyHMAC: "smoke-population", ProbeKeyHMAC: "smoke-probe", ProbeFamily: "basic", ProbeSetVersion: "j3b-smoke-v1", TokenizerVersion: "smoke-tokenizer", ObservationStatus: "complete", IdentityStatus: "unknown", MappingStatus: "exact", ProtocolStatus: "passed", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, runID, RunCompleted, "likely", 10, 10, "smoke", now.Add(time.Second), nil, nil); err != nil {
		t.Fatal(err)
	}
	decision := []byte(`{"version":1,"outcomeDigest":"smoke-outcome","policyDigest":"smoke-policy","evidenceDigest":"smoke-evidence","decision":{"result":"not_triggered"}}`)
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: runID, Status: RunCompleted, ResultSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`), Decision: decision}); err != nil {
		t.Fatalf("write PostgreSQL quality decision: %v", err)
	}
	if err := store.UpdateQualityDecision(ctx, QualityDecisionUpdate{RunID: runID, Status: RunCompleted, ResultSummary: []byte(`{}`), PolicySnapshot: []byte(`{}`), Decision: decision}); err != nil {
		t.Fatalf("replay PostgreSQL quality decision: %v", err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM juhe_dataset.model_check_runs WHERE id=$1`, runID).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("completed J3b PostgreSQL run status=%q err=%v", status, err)
	}
}

func TestPostgresModelCheckAtomicProjectionSmoke(t *testing.T) {
	if os.Getenv("J3B_MODEL_CHECK_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3B_MODEL_CHECK_POSTGRES_SMOKE=1 to verify the configured development PostgreSQL atomic projector")
	}
	dsn := strings.TrimSpace(os.Getenv("JUHE_AI_MODEL_CHECK_POSTGRES_URL"))
	if dsn == "" {
		t.Fatal("JUHE_AI_MODEL_CHECK_POSTGRES_URL is required for the opt-in smoke")
	}
	store, err := OpenPostgres(dsn, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Truncate(time.Microsecond)
	runID := "j3b-pg-project-" + started.Format("20060102150405.000000")
	defer func() {
		if _, cleanupErr := store.db.ExecContext(context.Background(), `DELETE FROM juhe_dataset.model_check_runs WHERE id=$1`, runID); cleanupErr != nil {
			t.Errorf("cleanup J3b PostgreSQL atomic projector run: %v", cleanupErr)
		}
	}()
	if err := store.CreateRun(ctx, RunInput{ID: runID, SystemAccountID: "j3b-project-system", ActorSystemAccountID: "j3b-project-actor", ProviderCode: "openai", TargetType: "account", TargetID: "j3b-project-account", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "j3b-project-v1", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(1500 * time.Millisecond)
	projection := OutcomeProjection{RunID: runID, Items: []ItemInput{{ID: runID + "-item", RunID: runID, ItemKey: "basic", ItemType: "responses_basic", Status: ItemPassed, Score: 10, MaxScore: 10, EvidenceSummary: []byte(`{"ok":true}`)}}, Status: RunCompleted, Level: "likely", Score: 10, MaxScore: 10, Message: "smoke", FinishedAt: finished, ResultSummary: []byte(`{"items":1}`), QualityDecision: []byte(`{"enforcement":"none"}`)}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectOutcome(ctx, projection); err != nil {
		t.Fatalf("identical PostgreSQL terminal replay must be idempotent: %v", err)
	}
	conflict := projection
	conflict.Score = 9
	if err := store.ProjectOutcome(ctx, conflict); !errors.Is(err, ErrProjectionConflict) {
		t.Fatalf("different PostgreSQL terminal replay must fail closed, err=%v", err)
	}
	var runCount, itemCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.model_check_runs WHERE id=$1`, runID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_dataset.model_check_items WHERE run_id=$1`, runID).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || itemCount != 1 {
		t.Fatalf("atomic PostgreSQL projection counts run=%d item=%d", runCount, itemCount)
	}
}

func TestPostgresModelCheckTerminalFenceConcurrentSmoke(t *testing.T) {
	if os.Getenv("J3B_MODEL_CHECK_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3B_MODEL_CHECK_POSTGRES_SMOKE=1 to verify PostgreSQL terminal fencing under a concurrent append")
	}
	dsn := strings.TrimSpace(os.Getenv("JUHE_AI_MODEL_CHECK_POSTGRES_URL"))
	if dsn == "" {
		t.Fatal("JUHE_AI_MODEL_CHECK_POSTGRES_URL is required for the opt-in smoke")
	}
	store, err := OpenPostgres(dsn, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	runID := "j3b-pg-fence-" + now.Format("20060102150405.000")
	defer func() {
		if _, cleanupErr := store.db.ExecContext(context.Background(), `DELETE FROM juhe_dataset.model_check_runs WHERE id=$1`, runID); cleanupErr != nil {
			t.Errorf("cleanup J3b PostgreSQL terminal fence smoke run: %v", cleanupErr)
		}
	}()
	if err := store.CreateRun(ctx, RunInput{ID: runID, SystemAccountID: "j3b-smoke-system", ActorSystemAccountID: "j3b-smoke-actor", ProviderCode: "openai", TargetType: "account", TargetID: "j3b-smoke-account", Model: "gpt-5.6-sol", Profile: "quick", Trigger: TriggerManual, ProbeSetVersion: "j3b-smoke-v1", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM juhe_dataset.model_check_runs WHERE id=$1 FOR UPDATE`, runID).Scan(&status); err != nil || status != string(RunRunning) {
		t.Fatalf("lock running run status=%q err=%v", status, err)
	}
	appendDone := make(chan error, 1)
	go func() {
		appendDone <- store.AppendItem(ctx, ItemInput{ID: runID + "-late-item", RunID: runID, ItemKey: "late", ItemType: "late", Status: ItemSkipped, Score: 0, MaxScore: 0})
	}()
	select {
	case appendErr := <-appendDone:
		t.Fatalf("append completed before the run row lock was released: %v", appendErr)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := tx.ExecContext(ctx, `UPDATE juhe_dataset.model_check_runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case appendErr := <-appendDone:
		if appendErr == nil || !strings.Contains(appendErr.Error(), "not running") {
			t.Fatalf("terminal append error=%v", appendErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("append did not resolve after the terminal transaction committed")
	}
}
