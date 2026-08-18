package accountbalance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type balanceTestHTTP struct {
	body string
	err  error
	path []string
}

func (c *balanceTestHTTP) Do(request *http.Request) (*http.Response, error) {
	c.path = append(c.path, request.URL.Path)
	if c.err != nil {
		return nil, c.err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func TestStoreSQLiteLeaseFenceAndSnapshotCAS(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\balance.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	owner, acquired, err := store.AcquireOwnerLease(ctx, "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner: %v %t", err, acquired)
	}
	if _, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("active owner lease must fence second owner: %v %t", err, acquired)
	}
	account, acquired, err := store.AcquireAccountLease(ctx, owner, "acct-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire account: %v %t", err, acquired)
	}
	if _, acquired, err := store.AcquireAccountLease(ctx, owner, "acct-1", time.Minute); !errors.Is(err, ErrAccountLeaseHeld) || acquired {
		t.Fatalf("active account lease must not be reacquired by same owner: %v %t", err, acquired)
	}
	otherOwner := OwnerLease{OwnerID: "owner-b", FenceToken: owner.FenceToken}
	if _, acquired, err := store.AcquireAccountLease(ctx, otherOwner, "acct-1", time.Minute); err == nil || acquired {
		t.Fatalf("active account lease must not be stolen by another owner: %v %t", err, acquired)
	}
	secret := "store-test-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	input := Input{AccountID: "acct-1", SystemAccountID: "sys-1", InputVersion: 1, ConfigRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5}, APIKey: credential, Trigger: TriggerPeriodic, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	snapshot := Snapshot{Status: StatusFresh, RemainingUSD: "1.25", LastSuccessAt: now.Format(time.RFC3339Nano)}
	inserted, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-1", RequestID: "request-1", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: snapshot})
	if err != nil || !inserted {
		t.Fatalf("append outcome: %v %t", err, inserted)
	}
	duplicate, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-1", RequestID: "request-1", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now.Add(time.Second), Snapshot: Snapshot{Status: StatusFailed}})
	if err != nil || duplicate {
		t.Fatalf("duplicate outcome must be idempotent: %v %t", err, duplicate)
	}
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-conflict", RequestID: "request-1", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: Snapshot{Status: StatusFailed}}); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("request ID collision with a different outcome must be stale: %v", err)
	}
	// A later refresh advances the mutable snapshot, but retrying the original
	// request must still resolve through its immutable outcome row.
	later := now.Add(2 * time.Minute)
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-later", RequestID: "request-later", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: later, Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "2.50"}, NextRefreshAt: &later}); err != nil {
		t.Fatalf("later outcome: %v", err)
	}
	if duplicate, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-1", RequestID: "request-1", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: snapshot}); err != nil || duplicate {
		t.Fatalf("original request replay after later refresh must remain idempotent: %v %t", err, duplicate)
	}
	record, found, err := store.LoadSnapshot(ctx, "acct-1")
	if err != nil || !found || record.Snapshot.RemainingUSD != "2.50" {
		t.Fatalf("load snapshot: %#v %t %v", record, found, err)
	}
	outcomeRecord, found, err := store.LoadOutcome(ctx, "outcome-1")
	if err != nil || !found || outcomeRecord.OutcomeID != "outcome-1" || outcomeRecord.Snapshot.RemainingUSD != "1.25" {
		t.Fatalf("load immutable outcome: %#v %t %v", outcomeRecord, found, err)
	}
	stale := input
	stale.ConfigRevision = 0
	if _, err := store.WriteSnapshotCAS(ctx, owner, account, SnapshotMutation{Input: stale, Snapshot: Snapshot{Status: StatusFailed}}); err == nil {
		t.Fatal("invalid stale input must fail closed")
	}
	wrongExpected := now.Add(-time.Minute)
	nextRefresh := now.Add(time.Minute)
	staleOutcome := Outcome{OutcomeID: "outcome-stale", RequestID: "request-stale", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerManual, ObservedAt: now, Snapshot: Snapshot{Status: StatusFailed}, NextRefreshAt: &nextRefresh, ExpectedSnapshotNextRefreshAt: &wrongExpected}
	if _, err := store.AppendOutcome(ctx, owner, account, staleOutcome); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("first stale outcome must be marked stale: %v", err)
	}
	if _, err := store.AppendOutcome(ctx, owner, account, staleOutcome); !errors.Is(err, ErrOutcomeStale) {
		t.Fatalf("duplicate stale outcome must remain stale: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendOutcome(ctx, owner, account, Outcome{OutcomeID: "outcome-3", RequestID: "request-3", AccountID: "acct-1", InputVersion: 1, ConfigRevision: 1, Trigger: TriggerPeriodic, ObservedAt: now, Snapshot: snapshot}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale owner must be fenced, got %v", err)
	}
	ownerB, acquired, err := store.AcquireOwnerLease(ctx, "owner-b", time.Minute)
	if err != nil || !acquired || ownerB.FenceToken <= owner.FenceToken {
		t.Fatalf("takeover owner lease: %#v %t %v", ownerB, acquired, err)
	}
	accountB, acquired, err := store.AcquireAccountLease(ctx, ownerB, "acct-1", time.Minute)
	if err != nil || !acquired || accountB.FenceToken <= account.FenceToken {
		t.Fatalf("takeover account lease: %#v %t %v", accountB, acquired, err)
	}
}

func TestRunnerPeriodicDirectHTTPAndFirstProbe(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "\\runner.sqlite"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := "runner-test-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	client := &balanceTestJSONHTTP{body: `{"unit":"USD","remaining":"12.5"}`}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "runner", CredentialSecret: secret, HTTPClient: client, MaxConcurrent: 2})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	candidate := Candidate{AccountID: "acct-periodic", SystemAccountID: "sys-periodic", InputVersion: 1, ConfigRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, BalanceEnabled: true, APIKeyCount: 1, APIKey: credential, BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5}, IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due}
	report, err := runner.RunPeriodic(context.Background(), []Candidate{candidate})
	if err != nil || report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("periodic report: %#v %v", report, err)
	}
	periodic, found, err := store.LoadSnapshot(context.Background(), "acct-periodic")
	if err != nil || !found || periodic.Snapshot.Status != StatusFresh || periodic.Snapshot.RemainingUSD != "12.500000" {
		t.Fatalf("periodic snapshot: %#v %t %v", periodic, found, err)
	}
	first := candidate
	first.AccountID = "acct-first"
	first.BalanceEnabled = false
	first.FirstProbe = true
	first.Config = QueryConfig{}
	report, err = runner.RunFirstProbe(context.Background(), []Candidate{first})
	if err != nil || report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("first probe report: %#v %v", report, err)
	}
	if len(client.paths) == 0 || client.paths[0] != "/v1/usage" {
		t.Fatalf("first builtin path should be /v1/usage, got %v", client.paths)
	}
	recovery := candidate
	recovery.AccountID = "acct-recovery"
	recovery.NextRefreshAt = nil
	recovery.Recovery = true
	report, err = runner.RunPeriodic(context.Background(), []Candidate{recovery})
	if err != nil || report.Executed != 1 || report.Stale != 0 || len(report.Errors) != 0 {
		t.Fatalf("missing-schedule recovery report: %#v %v", report, err)
	}
	recovered, found, err := store.LoadSnapshot(context.Background(), recovery.AccountID)
	if err != nil || !found || recovered.NextRefreshAt == nil || recovered.Snapshot.Status != StatusFresh {
		t.Fatalf("recovery snapshot: %#v %t %v", recovered, found, err)
	}
	manual := candidate
	manual.AccountID = "acct-periodic"
	manual.NextRefreshAt = &due
	input, err := manual.ToInput(TriggerManual, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manualReport, err := runner.RunManual(context.Background(), input)
	if err != nil || manualReport.Stale != 0 || manualReport.Executed != 1 {
		t.Fatalf("manual refresh must not apply the scheduled due fence: %#v %v", manualReport, err)
	}
	manualOutcome, found, err := store.LoadOutcome(context.Background(), OutcomeIDForInput(input))
	if err != nil || !found || manualOutcome.ExpectedNextRefreshSet || manualOutcome.ExpectedNextRefreshAt != nil {
		t.Fatalf("manual outcome must omit expected next-refresh fence: %#v %t %v", manualOutcome, found, err)
	}
	recoveryInput, err := recovery.ToInput(TriggerPeriodic, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recoveryOutcome, found, err := store.LoadOutcome(context.Background(), OutcomeIDForInput(recoveryInput))
	if err != nil || !found || !recoveryOutcome.ExpectedNextRefreshSet || recoveryOutcome.ExpectedNextRefreshAt != nil {
		t.Fatalf("periodic recovery outcome must preserve an explicit null due fence: %#v %t %v", recoveryOutcome, found, err)
	}
	encoded, err := json.Marshal(recoveryOutcome)
	if err != nil || !strings.Contains(string(encoded), `"expected_next_refresh_at":null`) {
		t.Fatalf("recovery outcome must serialize null due fence: %s %v", encoded, err)
	}
}

func TestOutcomeKeepsJobsSnapshotFenceOutOfNodePayload(t *testing.T) {
	now := time.Now().UTC()
	next := now.Add(time.Minute)
	outcome := Outcome{
		OutcomeID: "outcome-local-fence", RequestID: "request-local-fence", AccountID: "acct-local-fence",
		SystemAccountID: "sys-local-fence", InputVersion: 9, ConfigRevision: 11, Trigger: TriggerPeriodic,
		ObservedAt: now, Snapshot: Snapshot{Status: StatusFresh}, NextRefreshAt: &next,
		ExpectedSnapshotInput: 7, ExpectedSnapshotConfig: 8,
	}
	encoded, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"expected_input"`) || strings.Contains(string(encoded), `"expected_config"`) {
		t.Fatalf("jobs-local snapshot fence leaked into Node outcome payload: %s", encoded)
	}
}

type balanceTestJSONHTTP struct {
	body  string
	paths []string
}

func (c *balanceTestJSONHTTP) Do(request *http.Request) (*http.Response, error) {
	c.paths = append(c.paths, request.URL.Path)
	return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{Reader: strings.NewReader(c.body)}}, nil
}

type ioNopCloser struct{ Reader *strings.Reader }

func (c ioNopCloser) Read(p []byte) (int, error) { return c.Reader.Read(p) }
func (c ioNopCloser) Close() error               { return nil }
