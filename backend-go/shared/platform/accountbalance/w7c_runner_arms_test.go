package accountbalance

// w7c runner/service contract tests: drive the in-process runner pipeline
// (candidate eligibility, io/db worker fan-out, prepare/persist arms) and the
// service manual bridge against the isolated SQLite store, plus direct table
// coverage for applyQueryResult and the small RunReport helpers.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type w7cScriptedHTTP struct {
	behavior func(*http.Request) (int, string)
	paths    []string
}

func (c *w7cScriptedHTTP) Do(request *http.Request) (*http.Response, error) {
	c.paths = append(c.paths, request.URL.Path)
	status, body := c.behavior(request)
	return &http.Response{StatusCode: status, Body: ioNopCloser{Reader: strings.NewReader(body)}}, nil
}

func w7cFreshBehavior(*http.Request) (int, string) {
	return http.StatusOK, `{"unit":"USD","remaining":"12.5"}`
}

func w7cBalanceCandidate(t *testing.T, store *Store, accountID string, mutate func(*Candidate)) Candidate {
	t.Helper()
	credential, err := NewCredentialEnvelope("w7c-balance-secret", "api_key", map[string]string{"api_key": "sk-w7c"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	candidate := Candidate{
		AccountID: accountID, SystemAccountID: "w7c-sys-" + accountID, InputVersion: 1, ConfigRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		BalanceEnabled: true, APIKeyCount: 1, APIKey: credential, BaseURL: "https://example.test",
		Config:   QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
		IssuedAt: now, ExpiresAt: now.Add(time.Minute), NextRefreshAt: &due,
	}
	if mutate != nil {
		mutate(&candidate)
	}
	return candidate
}

func w7cNewRunner(t *testing.T, store *Store, client HTTPDoer, mutate func(*RunnerConfig)) *Runner {
	t.Helper()
	config := RunnerConfig{
		Store: store, OwnerID: "w7c-runner", CredentialSecret: "w7c-balance-secret",
		HTTPClient: client, MaxConcurrent: 2,
	}
	if mutate != nil {
		mutate(&config)
	}
	runner, err := NewRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestW7CRunDispatchRejectsManualCandidates(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, nil)

	if _, err := runner.Run(context.Background(), TriggerManual, []Candidate{w7cBalanceCandidate(t, store, "w7c-dispatch", nil)}); err == nil || !strings.Contains(err.Error(), "仅接受 periodic 或 first_probe") {
		t.Fatalf("manual via Run must be rejected: %v", err)
	}
	if _, err := runner.Run(context.Background(), Trigger("other"), nil); err == nil {
		t.Fatal("unknown trigger must be rejected")
	}
}

func TestW7CCandidateEligibilityMatrix(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, nil)

	report, err := runner.RunPeriodic(context.Background(), []Candidate{
		w7cBalanceCandidate(t, store, "", nil),                                                         // empty id
		w7cBalanceCandidate(t, store, "w7c-del", func(c *Candidate) { c.Deleted = true }),              // deleted
		w7cBalanceCandidate(t, store, "w7c-auth", func(c *Candidate) { c.Authorized = true }),          // authorized
		w7cBalanceCandidate(t, store, "w7c-type", func(c *Candidate) { c.Type = "oauth" }),             // wrong type
		w7cBalanceCandidate(t, store, "w7c-keys", func(c *Candidate) { c.APIKeyCount = 2 }),            // multi key
		w7cBalanceCandidate(t, store, "w7c-off", func(c *Candidate) { c.BalanceEnabled = false }),      // periodic disabled
		w7cBalanceCandidate(t, store, "w7c-pending", func(c *Candidate) { c.Status = "pending_test" }), // not active
		w7cBalanceCandidate(t, store, "w7c-unsched", func(c *Candidate) { c.Schedulable = false }),     // not schedulable
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Seen != 8 || report.Executed != 0 || len(report.Errors) != 8 {
		t.Fatalf("eligibility report: %#v", report)
	}

	first := w7cBalanceCandidate(t, store, "w7c-first", func(c *Candidate) { c.FirstProbe = false; c.BalanceEnabled = false })
	report, err = runner.RunFirstProbe(context.Background(), []Candidate{first})
	if err != nil || len(report.Errors) != 1 {
		t.Fatalf("first-probe eligibility: %#v %v", report, err)
	}
}

func TestW7CRunInputsSkipsWhenOwnerLeaseHeld(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx := context.Background()

	// A competing runner holds the owner lease first.
	holder := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, func(c *RunnerConfig) { c.OwnerID = "w7c-holder" })
	w7cOwner(t, store, "w7c-holder")

	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, func(c *RunnerConfig) { c.OwnerID = "w7c-loser" })
	candidates := []Candidate{w7cBalanceCandidate(t, store, "w7c-skip-a", nil), w7cBalanceCandidate(t, store, "w7c-skip-b", nil)}
	report, err := holder.RunPeriodic(ctx, candidates)
	if err != nil {
		t.Fatal(err)
	}
	_ = runner
	_ = report
	// Now the loser cannot acquire the lease and everything is skipped.
	loser := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, func(c *RunnerConfig) { c.OwnerID = "w7c-loser" })
	skipped, err := loser.RunPeriodic(ctx, candidates)
	if err != nil || skipped.Skipped != 2 || skipped.Executed != 0 {
		t.Fatalf("fenced runner must skip all: %#v %v", skipped, err)
	}
}

func TestW7CRunInputsCanceledContext(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, nil)
	report, err := runner.RunPeriodic(ctx, []Candidate{w7cBalanceCandidate(t, store, "w7c-canceled", nil)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
	if report.Seen != 1 {
		t.Fatalf("report seen: %#v", report)
	}
}

func TestW7CRunnerQueryFailureRecordsError(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	// The upstream responds with a non-JSON garbage body on every adapter
	// endpoint; the query finishes as an adapter diagnostic, and the runner
	// still persists an unsupported snapshot.
	client := &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusNotFound, "nope" }}
	runner := w7cNewRunner(t, store, client, nil)
	candidate := w7cBalanceCandidate(t, store, "w7c-http404", nil)
	report, err := runner.RunPeriodic(context.Background(), []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("upstream diagnostics must not fail the batch: %#v %v", report, err)
	}
	snapshot, found, err := store.LoadSnapshot(context.Background(), candidate.AccountID)
	if err != nil || !found || snapshot.Snapshot.Status != StatusUnsupported {
		t.Fatalf("unsupported snapshot: %#v %t %v", snapshot, found, err)
	}
}

func TestW7CRunnerManualRunRecordsError(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	input := w7cValidBalanceInput("w7c-manual-err")
	input.Trigger = TriggerManual
	input.IssuedAt = now
	input.ExpiresAt = now.Add(time.Minute)
	input.NextRefreshAt = &due
	input.Config.IntervalMinutes = 5

	// A runner with the wrong credential secret fails local envelope
	// decryption, which is a per-account error (not an upstream diagnostic).
	runner := w7cNewRunner(t, store, &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusOK, "not-json" }}, func(c *RunnerConfig) {
		c.CredentialSecret = "w7c-wrong-secret"
	})
	report, err := runner.RunManual(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || len(report.Errors) != 1 {
		t.Fatalf("manual decryption error: %#v %v", report, err)
	}
}

func TestW7CRunnerInputExpiringMidFlightIsRejected(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	base := time.Now().UTC()
	advanced := base
	input := w7cValidBalanceInput("w7c-expiring")
	input.Trigger = TriggerManual
	input.IssuedAt = base
	input.ExpiresAt = base.Add(30 * time.Second)

	// The scripted upstream advances the injected clock while serving the
	// request, so the input expires between query and persistence.
	client := &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) {
		advanced = advanced.Add(time.Minute)
		return http.StatusOK, `{"unit":"USD","remaining":"12.5"}`
	}}
	runner := w7cNewRunner(t, store, client, func(c *RunnerConfig) { c.Now = func() time.Time { return advanced } })
	report, err := runner.RunManual(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || len(report.Errors) != 1 || !strings.Contains(report.Errors[input.AccountID].Error(), "已过期") {
		t.Fatalf("mid-flight expiry: %#v %v", report, err)
	}
	if _, found, err := store.LoadSnapshot(context.Background(), input.AccountID); err != nil || found {
		t.Fatalf("expired result must not persist: %t %v", found, err)
	}
}

func TestW7CApplyQueryResultBranchMatrix(t *testing.T) {
	now := time.Now().UTC()
	prior := SnapshotRecord{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "5", LastSuccessAt: now.Format(time.RFC3339Nano), ConsecutiveTransientFails: 1}}

	fresh := applyQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusFresh}}, prior, true, TriggerPeriodic, now)
	if fresh.Status != StatusFresh || fresh.ConsecutiveTransientFails != 0 || fresh.LastSuccessAt == "" {
		t.Fatalf("fresh branch: %#v", fresh)
	}
	unlimited := applyQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusUnlimited}}, SnapshotRecord{}, false, TriggerPeriodic, now)
	if unlimited.Status != StatusUnlimited || unlimited.LastTransientErrorMessage != "" {
		t.Fatalf("unlimited branch: %#v", unlimited)
	}
	unsupported := applyQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusUnsupported}}, SnapshotRecord{}, false, TriggerPeriodic, now)
	if unsupported.Status != StatusUnsupported || unsupported.LastAttemptAt == "" {
		t.Fatalf("unsupported branch: %#v", unsupported)
	}
	manual := applyQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusPending, RemainingUSD: "1"}}, SnapshotRecord{}, false, TriggerManual, now)
	if manual.Status != StatusFailed || manual.RemainingUSD != "" || manual.RawRemaining != "" {
		t.Fatalf("manual branch: %#v", manual)
	}
	retained := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, prior, true, TriggerPeriodic, now)
	if retained.Status != StatusFresh || retained.ConsecutiveTransientFails != 2 || retained.LastTransientErrorMessage != "timeout" || retained.RemainingUSD != "5" {
		t.Fatalf("retained transient branch: %#v", retained)
	}
	pending := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, SnapshotRecord{}, false, TriggerFirstProbe, now)
	if pending.Status != StatusPending || pending.ConsecutiveTransientFails != 1 {
		t.Fatalf("pending transient branch: %#v", pending)
	}
	three := prior
	three.Snapshot.ConsecutiveTransientFails = 2
	failed := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, three, true, TriggerPeriodic, now)
	if failed.Status != StatusFailed || failed.ConsecutiveTransientFails != 3 || failed.ErrorMessage != "timeout" {
		t.Fatalf("third transient branch: %#v", failed)
	}
	capped := prior
	capped.Snapshot.ConsecutiveTransientFails = 9
	cappedFailed := applyQueryResult(QueryResult{ErrorMessage: "timeout"}, capped, true, TriggerPeriodic, now)
	if cappedFailed.Status != StatusFailed || cappedFailed.ConsecutiveTransientFails != 3 {
		t.Fatalf("capped transient branch: %#v", cappedFailed)
	}
	manualDraft := ApplyManualQueryResult(QueryResult{Snapshot: Snapshot{Status: StatusPending, RemainingUSD: "2"}}, now)
	if manualDraft.Status != StatusFailed {
		t.Fatalf("manual draft: %#v", manualDraft)
	}
}

func TestW7CIdentityHelpersAreDeterministic(t *testing.T) {
	input := w7cValidBalanceInput("w7c-id")
	first := OutcomeIDForInput(input)
	if first != OutcomeIDForInput(input) || first == OutcomeIDForInput(w7cValidBalanceInput("w7c-other")) {
		t.Fatal("outcome identity must be deterministic per input")
	}
	if RequestIDForInput(input) == first || RequestIDForInput(input) != RequestIDForInput(input) {
		t.Fatal("request identity must be deterministic and distinct")
	}
	if !strings.HasPrefix(first, "account-balance-outcome-") {
		t.Fatalf("identity prefix: %s", first)
	}
}

func TestW7CNewRunnerValidationArms(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	if _, err := NewRunner(RunnerConfig{CredentialSecret: "s"}); err == nil {
		t.Fatal("missing store/owner must be rejected")
	}
	if _, err := NewRunner(RunnerConfig{Store: store, OwnerID: "o", CredentialSecret: ""}); err == nil {
		t.Fatal("missing secret must be rejected")
	}
	if _, err := NewRunner(RunnerConfig{Store: store, OwnerID: "o", CredentialSecret: "s", IOConcurrency: maxBalanceRunnerConcurrency + 1}); err == nil || !strings.Contains(err.Error(), "最大并发(IO)") {
		t.Fatalf("io bound: %v", err)
	}
	if _, err := NewRunner(RunnerConfig{Store: store, OwnerID: "o", CredentialSecret: "s", DBConcurrency: maxBalanceRunnerConcurrency + 1}); err == nil || !strings.Contains(err.Error(), "最大并发(DB)") {
		t.Fatalf("db bound: %v", err)
	}
	if _, err := NewRunner(RunnerConfig{Store: store, OwnerID: "o", CredentialSecret: "s", DBQueueSize: maxAccountBalanceWorkItems + 1}); err == nil || !strings.Contains(err.Error(), "DB 队列") {
		t.Fatalf("queue bound: %v", err)
	}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "o", CredentialSecret: "s", DBQueueSize: maxAccountBalanceWorkItems})
	if err != nil || runner.dbQueueSize != maxAccountBalanceWorkItems {
		t.Fatalf("queue at bound: %d %v", runner.dbQueueSize, err)
	}
}

func TestW7CRunReportAddErrorInitializesMap(t *testing.T) {
	var report RunReport
	report.addError("acct", errors.New("boom"))
	if report.Errors["acct"] == nil {
		t.Fatal("lazy map must be initialized")
	}
	report.addError("acct", nil)
	if len(report.Errors) != 1 {
		t.Fatal("nil error must not overwrite")
	}
}

func TestW7CServiceGuardsWithoutInfrastructure(t *testing.T) {
	if _, err := NewService(RuntimeConfig{Enabled: false}, nil); err == nil || !strings.Contains(err.Error(), "未启用") {
		t.Fatalf("disabled service: %v", err)
	}
	var nilService *Service
	if nilService.Ready() {
		t.Fatal("nil service must not be ready")
	}
	if err := nilService.Close(); err != nil {
		t.Fatalf("nil service close: %v", err)
	}
	if _, _, err := nilService.RunManual(context.Background(), Input{}); err == nil || !strings.Contains(err.Error(), "未就绪") {
		t.Fatalf("nil run manual: %v", err)
	}
	if err := nilService.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "未就绪") {
		t.Fatalf("nil run: %v", err)
	}
	empty := &Service{}
	if empty.Ready() {
		t.Fatal("unstarted service must not be ready")
	}
	if _, _, err := empty.RunManual(context.Background(), Input{}); err == nil {
		t.Fatal("unstarted manual must fail")
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("empty service close: %v", err)
	}
}
