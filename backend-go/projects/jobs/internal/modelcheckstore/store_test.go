package modelcheckstore

import (
	"context"
	"database/sql"
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
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM juhe_dataset.model_check_runs WHERE id=$1`, runID).Scan(&status); err != nil || status != string(RunCompleted) {
		t.Fatalf("completed J3b PostgreSQL run status=%q err=%v", status, err)
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
