package accounthealth

import (
	"context"
	"fmt"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostgresSchemaDoesNotCreateDatabaseSchema(t *testing.T) {
	if strings.Contains(strings.ToUpper(postgresSchema), "CREATE SCHEMA") {
		t.Fatal("PostgreSQL jobs role must not require database-level CREATE to initialize juhe_jobs")
	}
}

func TestPostgresSchemaDropsAiHealthOutcomeFatPayloadIndex(t *testing.T) {
	// The fat INCLUDE (payload) partial index served the gateway's legacy
	// per-hour LATERAL probe read path; since 2026-10-29 the gateway reads
	// entity columns through one narrow ranged SELECT, so the schema must
	// drop the fat index on existing deployments and keep the plain range
	// index that serves the new read.
	if strings.Contains(postgresSchema, "idx_account_health_outcomes_account_observed_non_stale") &&
		!strings.Contains(postgresSchema, "DROP INDEX IF EXISTS juhe_jobs.idx_account_health_outcomes_account_observed_non_stale") {
		t.Fatal("fat payload index must not be created anymore; it may only be dropped for existing deployments")
	}
	if strings.Contains(postgresSchema, "INCLUDE (payload)") {
		t.Fatal("fat payload index must be dropped: it duplicated payload into the index for a retired read path")
	}
	if !strings.Contains(postgresSchema, "DROP INDEX IF EXISTS juhe_jobs.idx_account_health_outcomes_account_observed_non_stale") {
		t.Fatal("PostgreSQL J1 jobs schema must actively drop the retired fat index on existing deployments")
	}
	if !strings.Contains(postgresSchema, "CREATE INDEX IF NOT EXISTS idx_account_health_outcomes_account_observed ON juhe_jobs.account_health_outcomes(account_id, observed_at DESC, outcome_id DESC)") {
		t.Fatal("plain account/time range index must remain for the ranged AI health read")
	}
}

func TestPostgresTaskFailureBaselineReusesObservedAtForUpdatedAt(t *testing.T) {
	source, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$4) ON CONFLICT (account_id) DO NOTHING") {
		t.Fatal("PostgreSQL task-failure baseline must reuse observed_at for updated_at because currentStateArgs has 17 parameters")
	}
}

func TestOpenStoreRejectsInvalidPostgresPoolShape(t *testing.T) {
	for name, config := range map[string]StoreConfig{
		"non-positive-open": {Mode: StorePostgres, PostgresURL: "postgres://jobs-output", PostgresMaxOpenConns: -1, PostgresMaxIdleConns: 1},
		"idle-above-open":   {Mode: StorePostgres, PostgresURL: "postgres://jobs-output", PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 5},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenStore(config); err == nil {
				t.Fatal("invalid PostgreSQL pool shape must fail before opening the database")
			}
		})
	}
}

// This is intentionally opt-in because the configured URL must point to an
// isolated jobs schema. It exercises PostgreSQL's ON CONFLICT and null-safe
// cooldown-fence predicates.
func TestPostgresCurrentStateCASRegression(t *testing.T) {
	postgresURL := os.Getenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL")
	if postgresURL == "" {
		t.Skip("requires isolated J1 PostgreSQL jobs-store smoke environment")
	}
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: postgresURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	unique := fmt.Sprintf("current-state-cas-%d", time.Now().UnixNano())
	lease, acquired, err := store.AcquireOwnerLease(ctx, unique+"-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("isolated PostgreSQL CAS test requires available owner lease: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(context.Background(), lease) })

	observed := time.Now().UTC().Round(0)
	revisionAccount := unique + "-revision"
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: unique + "-baseline", RequestID: unique + "-baseline-request", AccountID: revisionAccount, Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: observed, InputVersion: 5, ConfigRevision: 8, DispatchRevision: 13, AccountStatus: "active"})
	late := Outcome{OutcomeID: unique + "-revision", RequestID: unique + "-revision-request", AccountID: revisionAccount, Outcome: exactkeyprobe.OutcomeUpstreamFailed, ObservedAt: observed.Add(time.Second), InputVersion: 5, ConfigRevision: 9, DispatchRevision: 13, AccountStatus: "temporary_unavailable"}
	appendStoreOutcome(t, store, lease, late)
	state, found, err := store.LoadCurrentState(ctx, revisionAccount)
	if err != nil || !found || state.OutcomeID != late.OutcomeID || state.ConfigRevision != 9 || state.DispatchRevision != 13 {
		t.Fatalf("PostgreSQL newer revision must replace current state: found=%t state=%#v err=%v", found, state, err)
	}

	cooldownAccount := unique + "-cooldown"
	oldFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: unique + "-generation-old"}
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: unique + "-cooldown-baseline", RequestID: unique + "-cooldown-baseline-request", AccountID: cooldownAccount, Outcome: exactkeyprobe.OutcomeNeutral, ObservedAt: observed, InputVersion: 7, ConfigRevision: 11, DispatchRevision: 17, AccountStatus: "temporary_unavailable", CooldownFence: oldFence})
	mismatch := cooldownCASOutcome(unique+"-generation-mismatch", unique+"-generation-mismatch-request", cooldownAccount, observed.Add(time.Second), &exactkeyprobe.CooldownFence{ObservationStartedAt: oldFence.ObservationStartedAt, Generation: unique + "-generation-new"})
	appendStoreOutcome(t, store, lease, mismatch)
	state, found, err = store.LoadCurrentState(ctx, cooldownAccount)
	if err != nil || !found || state.OutcomeID != unique+"-cooldown-baseline" || state.CooldownFence == nil || state.CooldownFence.Generation != oldFence.Generation {
		t.Fatalf("PostgreSQL cooldown generation CAS miss must preserve state: found=%t state=%#v err=%v", found, state, err)
	}

	missing := cooldownCASOutcome(unique+"-missing", unique+"-missing-request", unique+"-missing-account", observed.Add(2*time.Second), oldFence)
	appendStoreOutcome(t, store, lease, missing)
	if state, found, err := store.LoadCurrentState(ctx, missing.AccountID); err != nil || found || state != (CurrentState{}) {
		t.Fatalf("PostgreSQL cooldown missing state must remain outcome-only stale: found=%t state=%#v err=%v", found, state, err)
	}
}

func appendStoreOutcome(t *testing.T, store *Store, lease OwnerLease, outcome Outcome) {
	t.Helper()
	inserted, err := store.AppendOutcome(context.Background(), lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("append outcome %q: inserted=%t err=%v", outcome.OutcomeID, inserted, err)
	}
}

func cooldownCASOutcome(outcomeID, requestID, accountID string, observed time.Time, expectedFence *exactkeyprobe.CooldownFence) Outcome {
	return Outcome{
		OutcomeID:        outcomeID,
		RequestID:        requestID,
		AccountID:        accountID,
		Outcome:          exactkeyprobe.OutcomeNeutral,
		ObservedAt:       observed,
		InputVersion:     7,
		ConfigRevision:   11,
		DispatchRevision: 17,
		AccountStatus:    "temporary_unavailable",
		CooldownFence:    expectedFence,
		Projection: &Projection{
			TargetAccountID:       accountID,
			TransitionKind:        "cooldown_defer",
			InputVersion:          7,
			ConfigRevision:        11,
			DispatchRevision:      17,
			SourceRevision:        expectedFence.SourceConfigRevision,
			ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: expectedFence,
			CooldownFence:         expectedFence,
		},
	}
}
