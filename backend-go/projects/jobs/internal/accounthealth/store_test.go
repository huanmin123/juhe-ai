package accounthealth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresSchemaDoesNotCreateDatabaseSchema(t *testing.T) {
	if strings.Contains(strings.ToUpper(postgresSchema), "CREATE SCHEMA") {
		t.Fatal("PostgreSQL jobs role must not require database-level CREATE to initialize juhe_jobs")
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

func TestStoreLoadDirectInputSuppressionsReturnsOnlyActiveFences(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	now := time.Date(2030, 8, 16, 12, 0, 0, 0, time.UTC)
	future := now.Add(5 * time.Minute)
	past := now.Add(-time.Minute)
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "direct-suppression-future", RequestID: "direct-suppression-future", AccountID: "direct-suppression-future-account", Outcome: OutcomeTaskFailed, ObservedAt: now, InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, ErrorCode: "direct_input_invalid", NextDueAt: &future, FailureCount: 1})
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "direct-suppression-past", RequestID: "direct-suppression-past", AccountID: "direct-suppression-past-account", Outcome: OutcomeTaskFailed, ObservedAt: now, InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, ErrorCode: "direct_input_invalid", NextDueAt: &past, FailureCount: 1})
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "direct-suppression-other", RequestID: "direct-suppression-other", AccountID: "direct-suppression-other-account", Outcome: OutcomeTaskFailed, ObservedAt: now, InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, ErrorCode: "other_error", NextDueAt: &future, FailureCount: 1})
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "direct-suppression-newer-state", RequestID: "direct-suppression-newer-state", AccountID: "direct-suppression-old-state-account", Outcome: OutcomeSuccess, ObservedAt: now, InputVersion: 9, ConfigRevision: 9, DispatchRevision: 9, AccountStatus: "active"})
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "direct-suppression-old-state", RequestID: "direct-suppression-old-state", AccountID: "direct-suppression-old-state-account", Outcome: OutcomeTaskFailed, ObservedAt: now.Add(time.Second), InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, ErrorCode: "direct_input_invalid", NextDueAt: &future, FailureCount: 1})
	suppressions, err := store.LoadDirectInputSuppressions(context.Background(), now)
	if err != nil || len(suppressions) != 2 {
		t.Fatalf("active direct-input suppressions = %#v err=%v", suppressions, err)
	}
	foundFenced := false
	for _, suppression := range suppressions {
		if suppression.AccountID == "direct-suppression-future-account" && suppression.NextDueAt.UTC() == future {
			foundFenced = true
		}
		if suppression.AccountID == "direct-suppression-old-state-account" && suppression.InputVersion != 4 {
			t.Fatalf("suppression must retain the malformed generation despite newer current state: %#v", suppression)
		}
	}
	if !foundFenced {
		t.Fatalf("missing future suppression: %#v", suppressions)
	}
}

// This is intentionally opt-in because the configured URL must point to an
// isolated jobs schema. It exercises PostgreSQL's ON CONFLICT and null-safe
// cooldown-fence predicates rather than assuming SQLite's syntax is enough.
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
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: unique + "-baseline", RequestID: unique + "-baseline-request", AccountID: revisionAccount, Outcome: OutcomeSuccess, ObservedAt: observed, InputVersion: 5, ConfigRevision: 8, DispatchRevision: 13, AccountStatus: "active"})
	late := Outcome{OutcomeID: unique + "-revision", RequestID: unique + "-revision-request", AccountID: revisionAccount, Outcome: OutcomeUpstreamFailed, ObservedAt: observed.Add(time.Second), InputVersion: 5, ConfigRevision: 9, DispatchRevision: 13, AccountStatus: "temporary_unavailable"}
	appendStoreOutcome(t, store, lease, late)
	state, found, err := store.LoadCurrentState(ctx, revisionAccount)
	if err != nil || !found || state.OutcomeID != unique+"-baseline" || state.ConfigRevision != 8 {
		t.Fatalf("PostgreSQL revision CAS miss must preserve state: found=%t state=%#v err=%v", found, state, err)
	}

	cooldownAccount := unique + "-cooldown"
	oldFence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: unique + "-generation-old"}
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: unique + "-cooldown-baseline", RequestID: unique + "-cooldown-baseline-request", AccountID: cooldownAccount, Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 7, ConfigRevision: 11, DispatchRevision: 17, AccountStatus: "temporary_unavailable", CooldownFence: oldFence})
	mismatch := cooldownCASOutcome(unique+"-generation-mismatch", unique+"-generation-mismatch-request", cooldownAccount, observed.Add(time.Second), &CooldownFence{ObservationStartedAt: oldFence.ObservationStartedAt, Generation: unique + "-generation-new"})
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

func TestSQLiteStoreOwnerLeaseAndIdempotentOutcome(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("second owner must be blocked: acquired=%t err=%v", acquired, err)
	}
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("lease renewal must succeed: renewed=%t err=%v", renewed, err)
	}
	observed := time.Now().UTC()
	fence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: "generation-1"}
	outcome := Outcome{OutcomeID: "outcome-1", RequestID: "request-1", AccountID: "account-1", Outcome: OutcomeSuccess, ObservedAt: observed, InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, StatusCode: 200, AccountStatus: "temporary_unavailable", CooldownFence: fence}
	inserted, err := store.AppendOutcome(ctx, lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("append outcome inserted=%t err=%v", inserted, err)
	}
	inserted, err = store.AppendOutcome(ctx, lease, outcome)
	if err != nil || inserted {
		t.Fatalf("same request must be idempotent: inserted=%t err=%v", inserted, err)
	}
	state, found, err := store.LoadCurrentState(ctx, "account-1")
	if err != nil || !found || state.CooldownFence == nil || state.CooldownFence.Generation != fence.Generation || !state.CooldownFence.ObservationStartedAt.Equal(fence.ObservationStartedAt) {
		t.Fatalf("cooldown fence must survive current-state read: found=%t state=%#v err=%v", found, state, err)
	}
	if _, err := store.AppendOutcome(ctx, OwnerLease{OwnerID: "owner-a", FenceToken: lease.FenceToken + 1}, Outcome{OutcomeID: "outcome-2", RequestID: "request-2", AccountID: "account-1", Outcome: OutcomeSuccess, ObservedAt: time.Now().UTC(), InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale lease must fail: %v", err)
	}
}

func TestRenewOwnerLeaseWriteGateHonorsDeadline(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "deadline-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	if err := store.lockWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer store.unlockWrite()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); renewed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("renew under held shared write gate = renewed:%t err:%v, want deadline", renewed, err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("renewal must not wait past its context deadline, elapsed=%s", elapsed)
	}
}

func TestSQLiteCurrentStateCASRejectsRevisionMismatchButKeepsOutcome(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	observed := time.Now().UTC().Round(0)
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "baseline", RequestID: "baseline-request", AccountID: "account-revision", Outcome: OutcomeSuccess, ObservedAt: observed, InputVersion: 5, ConfigRevision: 8, DispatchRevision: 13, AccountStatus: "active"})

	late := projectedHealthCASOutcome("revision-mismatch", "revision-mismatch-request", "account-revision", observed.Add(time.Second), 5, 9, 13)
	appendStoreOutcome(t, store, lease, late)
	dispatchMismatch := projectedHealthCASOutcome("dispatch-mismatch", "dispatch-mismatch-request", "account-revision", observed.Add(2*time.Second), 5, 8, 14)
	appendStoreOutcome(t, store, lease, dispatchMismatch)

	state, found, err := store.LoadCurrentState(ctx, late.AccountID)
	if err != nil || !found || state.OutcomeID != "baseline" || state.ConfigRevision != 8 || state.AccountStatus != "active" {
		t.Fatalf("revision-mismatched outcome must not overwrite current state: found=%t state=%#v err=%v", found, state, err)
	}
	if exists, err := store.HasRequest(ctx, late.RequestID); err != nil || !exists {
		t.Fatalf("CAS-missed outcome must remain auditable: exists=%t err=%v", exists, err)
	}
	if exists, err := store.HasRequest(ctx, dispatchMismatch.RequestID); err != nil || !exists {
		t.Fatalf("dispatch CAS-missed outcome must remain auditable: exists=%t err=%v", exists, err)
	}
	assertStoredOutcomeProjectionStripped(t, store, late.RequestID)
	assertStoredOutcomeProjectionStripped(t, store, dispatchMismatch.RequestID)
}

func TestSQLiteTaskFailureWithoutProjectionCannotResetCurrentState(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	observed := time.Now().UTC().Round(0)
	fence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: "task-failure-generation"}
	appendStoreOutcome(t, store, lease, Outcome{
		OutcomeID: "task-baseline", RequestID: "task-baseline-request", AccountID: "account-task-failure",
		Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6,
		AccountStatus: "temporary_unavailable", FailureCount: 7, NextDueAt: ptrTime(observed.Add(time.Minute)), CooldownFence: fence,
	})
	appendStoreOutcome(t, store, lease, Outcome{
		OutcomeID: "task-failure", RequestID: "task-failure-request", AccountID: "account-task-failure",
		Outcome: OutcomeTaskFailed, ObservedAt: observed.Add(time.Second), InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6,
		ErrorCode: "request_deadline_elapsed",
	})
	state, found, err := store.LoadCurrentState(ctx, "account-task-failure")
	if err != nil || !found || state.OutcomeID != "task-baseline" || state.AccountStatus != "temporary_unavailable" || state.FailureCount != 7 || !sameCooldownFence(state.CooldownFence, fence) {
		t.Fatalf("task failure without projection must preserve current state: found=%t state=%#v err=%v", found, state, err)
	}
}

func TestSQLiteTaskFailureWithDueOnlyReschedulesWithoutChangingState(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	observed := time.Now().UTC().Round(0)
	fence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: "task-retry-generation"}
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "retry-baseline", RequestID: "retry-baseline-request", AccountID: "account-task-retry", Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, AccountStatus: "active", FailureCount: 3, CooldownFence: fence})
	due := observed.Add(5 * time.Minute)
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "retry-failure", RequestID: "retry-failure-request", AccountID: "account-task-retry", Outcome: OutcomeTaskFailed, ObservedAt: observed.Add(time.Second), InputVersion: 4, ConfigRevision: 5, DispatchRevision: 6, ErrorCode: "upstream_unavailable", ErrorMessage: "probe transport failed", NextDueAt: &due})
	state, found, err := store.LoadCurrentState(ctx, "account-task-retry")
	if err != nil || !found || state.OutcomeID != "retry-failure" || state.AccountStatus != "active" || state.FailureCount != 3 || state.NextDueAt == nil || !state.NextDueAt.Equal(due) || !sameCooldownFence(state.CooldownFence, fence) {
		t.Fatalf("task failure retry must only advance due/error receipt: found=%t state=%#v err=%v", found, state, err)
	}
}

func TestSQLiteCurrentStateCooldownCASRejectsGenerationMismatchAndMissingState(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	observed := time.Now().UTC().Round(0)
	sourceRevision := int64(23)
	oldFence := &CooldownFence{ObservationStartedAt: observed.Add(-time.Minute), Generation: "generation-old", SourceConfigRevision: &sourceRevision}
	appendStoreOutcome(t, store, lease, Outcome{OutcomeID: "cooldown-baseline", RequestID: "cooldown-baseline-request", AccountID: "account-cooldown", Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 7, ConfigRevision: 11, DispatchRevision: 17, AccountStatus: "temporary_unavailable", CooldownFence: oldFence})

	wrongFence := &CooldownFence{ObservationStartedAt: oldFence.ObservationStartedAt, Generation: "generation-new", SourceConfigRevision: &sourceRevision}
	mismatch := cooldownCASOutcome("cooldown-generation-mismatch", "cooldown-generation-mismatch-request", "account-cooldown", observed.Add(time.Second), wrongFence)
	appendStoreOutcome(t, store, lease, mismatch)
	wrongSourceRevision := sourceRevision + 1
	sourceMismatch := cooldownCASOutcome("cooldown-source-revision-mismatch", "cooldown-source-revision-mismatch-request", "account-cooldown", observed.Add(2*time.Second), &CooldownFence{ObservationStartedAt: oldFence.ObservationStartedAt, Generation: oldFence.Generation, SourceConfigRevision: &wrongSourceRevision})
	appendStoreOutcome(t, store, lease, sourceMismatch)
	state, found, err := store.LoadCurrentState(ctx, mismatch.AccountID)
	if err != nil || !found || state.OutcomeID != "cooldown-baseline" || state.CooldownFence == nil || state.CooldownFence.Generation != oldFence.Generation || state.CooldownFence.SourceConfigRevision == nil || *state.CooldownFence.SourceConfigRevision != sourceRevision {
		t.Fatalf("generation mismatch must leave cooldown state unchanged: found=%t state=%#v err=%v", found, state, err)
	}
	if exists, err := store.HasRequest(ctx, mismatch.RequestID); err != nil || !exists {
		t.Fatalf("generation CAS miss must keep immutable outcome: exists=%t err=%v", exists, err)
	}
	if exists, err := store.HasRequest(ctx, sourceMismatch.RequestID); err != nil || !exists {
		t.Fatalf("cooldown source revision CAS miss must keep immutable outcome: exists=%t err=%v", exists, err)
	}
	assertStoredOutcomeProjectionStripped(t, store, mismatch.RequestID)
	assertStoredOutcomeProjectionStripped(t, store, sourceMismatch.RequestID)

	missing := cooldownCASOutcome("cooldown-missing-state", "cooldown-missing-state-request", "account-without-current-state", observed.Add(3*time.Second), oldFence)
	appendStoreOutcome(t, store, lease, missing)
	if state, found, err := store.LoadCurrentState(ctx, missing.AccountID); err != nil || found || state != (CurrentState{}) {
		t.Fatalf("cooldown CAS without current state must be outcome-only stale: found=%t state=%#v err=%v", found, state, err)
	}
	if exists, err := store.HasRequest(ctx, missing.RequestID); err != nil || !exists {
		t.Fatalf("missing-state CAS miss must keep immutable outcome: exists=%t err=%v", exists, err)
	}
	assertStoredOutcomeProjectionStripped(t, store, missing.RequestID)
}

func TestSQLiteCooldownTerminalPreservesFenceAndRejectsMismatchedOutputFence(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	observed := time.Now().UTC().Round(0)
	fence := &CooldownFence{ObservationStartedAt: observed.Add(-7 * 24 * time.Hour), Generation: "terminal-generation"}
	appendStoreOutcome(t, store, lease, Outcome{
		OutcomeID: "terminal-baseline", RequestID: "terminal-baseline-request", AccountID: "account-terminal",
		Outcome: OutcomeNeutral, ObservedAt: observed, InputVersion: 3, ConfigRevision: 5, DispatchRevision: 7,
		AccountStatus: "temporary_unavailable", FailureCount: 1, CooldownFence: fence,
	})
	terminal := Outcome{
		OutcomeID: "terminal", RequestID: "terminal-request", AccountID: "account-terminal",
		Outcome: OutcomeUpstreamFailed, ObservedAt: observed.Add(time.Second), InputVersion: 3, ConfigRevision: 5, DispatchRevision: 7,
		AccountStatus: "error", FailureCount: 2, CooldownFence: fence,
		Projection: &Projection{
			TargetAccountID: "account-terminal", TransitionKind: "cooldown_error", InputVersion: 3, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "temporary_unavailable", ExpectedCooldownFence: fence, CooldownFence: fence,
		},
	}
	appendStoreOutcome(t, store, lease, terminal)
	state, found, err := store.LoadCurrentState(ctx, terminal.AccountID)
	if err != nil || !found || state.AccountStatus != "error" || state.NextDueAt != nil || state.FailureCount != 2 || !sameCooldownFence(state.CooldownFence, fence) {
		t.Fatalf("terminal current state must retain final cooldown audit fence: found=%t state=%#v err=%v", found, state, err)
	}
	wrongFence := &CooldownFence{ObservationStartedAt: fence.ObservationStartedAt, Generation: "wrong-terminal-generation"}
	invalid := terminal
	invalid.OutcomeID = "terminal-invalid"
	invalid.RequestID = "terminal-invalid-request"
	invalid.Projection = &Projection{
		TargetAccountID: "account-terminal", TransitionKind: "cooldown_error", InputVersion: 3, ConfigRevision: 5, DispatchRevision: 7,
		ExpectedAccountStatus: "error", ExpectedCooldownFence: fence, CooldownFence: wrongFence,
	}
	if _, err := store.AppendOutcome(ctx, lease, invalid); err == nil {
		t.Fatal("terminal cooldown output fence mismatch must be rejected")
	}
}

func openSQLiteStoreWithLease(t *testing.T) (*Store, OwnerLease) {
	t.Helper()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "account-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "current-state-cas-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	return store, lease
}

func appendStoreOutcome(t *testing.T, store *Store, lease OwnerLease, outcome Outcome) {
	t.Helper()
	inserted, err := store.AppendOutcome(context.Background(), lease, outcome)
	if err != nil || !inserted {
		t.Fatalf("append outcome %q: inserted=%t err=%v", outcome.OutcomeID, inserted, err)
	}
}

func cooldownCASOutcome(outcomeID, requestID, accountID string, observed time.Time, expectedFence *CooldownFence) Outcome {
	return Outcome{
		OutcomeID:        outcomeID,
		RequestID:        requestID,
		AccountID:        accountID,
		Outcome:          OutcomeNeutral,
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

func projectedHealthCASOutcome(outcomeID, requestID, accountID string, observed time.Time, inputVersion, configRevision, dispatchRevision int64) Outcome {
	return Outcome{
		OutcomeID: outcomeID, RequestID: requestID, AccountID: accountID, Outcome: OutcomeUpstreamFailed, ObservedAt: observed,
		InputVersion: inputVersion, ConfigRevision: configRevision, DispatchRevision: dispatchRevision, AccountStatus: "temporary_unavailable",
		Projection: &Projection{
			TargetAccountID: accountID, TransitionKind: "temporary_unavailable", InputVersion: inputVersion, ConfigRevision: configRevision, DispatchRevision: dispatchRevision,
			ExpectedAccountStatus: "active",
		},
	}
}

func assertStoredOutcomeProjectionStripped(t *testing.T, store *Store, requestID string) {
	t.Helper()
	var payload []byte
	if err := store.db.QueryRowContext(context.Background(), `SELECT payload FROM account_health_outcomes WHERE request_id=?`, requestID).Scan(&payload); err != nil {
		t.Fatalf("read CAS-missed outcome payload %q: %v", requestID, err)
	}
	var stored Outcome
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatalf("decode CAS-missed outcome payload %q: %v", requestID, err)
	}
	if stored.Projection != nil {
		t.Fatalf("CAS-missed outcome %q must not retain a Node-applicable projection: %#v", requestID, stored.Projection)
	}
}
