package accountbalance

import (
	"context"
	"testing"
	"time"
)

// TestRunManualSkipsWhenOwnerLeaseHeld covers the gateway manual-bridge
// conflict path: while the periodic owner holds the single owner lease, a
// manual RunManual from another owner must resolve to Skipped (never execute)
// so the caller maps it to ErrAccountLeaseHeld (route 409 lease_busy).
func TestRunManualSkipsWhenOwnerLeaseHeld(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\manual-conflict.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	secret := "manual-conflict-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	periodicOwner, acquired, err := store.AcquireOwnerLease(ctx, "periodic-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("periodic owner lease: %v %t", err, acquired)
	}
	defer func() { _ = store.ReleaseOwnerLease(context.Background(), periodicOwner) }()
	manualRunner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "manual-owner", CredentialSecret: secret, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	input := Input{
		AccountID: "acct-manual", SystemAccountID: "sys-manual", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	report, err := manualRunner.RunManual(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Executed != 0 || report.Stale != 0 || len(report.Errors) != 0 {
		t.Fatalf("held owner lease must skip manual input: %#v", report)
	}
	// The caller-side mapping (Service.RunManual / the gateway adapter)
	// turns Skipped>0 || Executed==0 into ErrAccountLeaseHeld, which the
	// route renders as the 409 lease_busy branch.
}

// TestRunManualStaleSnapshotReportsStale covers the snapshot CAS path: a
// manual input older than the persisted snapshot must land in report.Stale so
// the caller maps it to ErrOutcomeStale (route 409 配置已变化).
func TestRunManualStaleSnapshotReportsStale(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\manual-stale.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	secret := "manual-stale-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "stale-owner", CredentialSecret: secret, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := Input{
		AccountID: "acct-stale", SystemAccountID: "sys-stale", InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		APIKey: credential, Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	// Seed a newer committed snapshot (input_version=2) through the same lease
	// machinery the runner uses.
	owner, acquired, err := store.AcquireOwnerLease(ctx, "seeder", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("seeder owner lease: %v %t", err, acquired)
	}
	account, acquired, err := store.AcquireAccountLease(ctx, owner, base.AccountID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("seeder account lease: %v %t", err, acquired)
	}
	newer := base
	newer.InputVersion = 2
	newer.Trigger = TriggerPeriodic
	newer.NextRefreshAt = cloneTime(&now)
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{
		OutcomeID: "seed-outcome", RequestID: "seed-request", AccountID: base.AccountID,
		InputVersion: 2, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now,
		Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "9.99"}, NextRefreshAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseAccountLease(ctx, owner, account); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// The stale manual input (input_version=1 < 2) must be rejected by the
	// snapshot CAS inside the outcome append.
	report, err := runner.RunManual(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stale != 1 || report.Executed != 0 || len(report.Errors) != 0 {
		t.Fatalf("stale manual input must report stale: %#v", report)
	}
}

// TestApplyManualQueryResultManualDiagnostics pins the non-persisted draft
// mapping: transient failures map to failed immediately, fresh keeps amounts
// and success stamps, unsupported-without-temporary stays unsupported.
func TestApplyManualQueryResultManualDiagnostics(t *testing.T) {
	now := time.Now().UTC()
	fresh := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.25"}}, now)
	if fresh.Status != StatusFresh || fresh.LastSuccessAt == "" {
		t.Fatalf("fresh draft: %#v", fresh)
	}
	failed := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusPending, ErrorMessage: "余额上游返回 HTTP 502"}, Temporary: true, ErrorMessage: "余额上游返回 HTTP 502"}, now)
	if failed.Status != StatusFailed || failed.ErrorMessage != "余额上游返回 HTTP 502" || failed.RemainingUSD != "" {
		t.Fatalf("transient draft must map to failed: %#v", failed)
	}
	unsupported := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusUnsupported, ErrorMessage: "没有可用的内置余额适配器"}}, now)
	if unsupported.Status != StatusUnsupported {
		t.Fatalf("permanent unsupported draft stays unsupported: %#v", unsupported)
	}
}
