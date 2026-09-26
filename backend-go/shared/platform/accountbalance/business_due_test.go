package accountbalance

// business_due tests: the periodic runner must advance the business due cursor
// (juhe_business.accounts.balance_query_next_refresh_at) only after a committed
// successful balance result, with the frozen candidate due as the write fence.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type bdueAdvanceCall struct {
	accountID      string
	configRevision int64
	expected       *time.Time
	next           *time.Time
}

type bdueFakeAdvancer struct {
	mu     sync.Mutex
	calls  []bdueAdvanceCall
	err    error
	result bool
}

func (f *bdueFakeAdvancer) AdvancePeriodicDue(_ context.Context, accountID string, configRevision int64, expected, next *time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, bdueAdvanceCall{accountID: accountID, configRevision: configRevision, expected: expected, next: next})
	if f.err != nil {
		return false, f.err
	}
	return f.result, nil
}

func (f *bdueFakeAdvancer) recorded() []bdueAdvanceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bdueAdvanceCall(nil), f.calls...)
}

func bdueRunner(t *testing.T, store *Store, client HTTPDoer, advancer BusinessDueAdvancer, now func() time.Time) *Runner {
	t.Helper()
	runner, err := NewRunner(RunnerConfig{
		Store: store, OwnerID: "bdue-runner", CredentialSecret: "w7c-balance-secret",
		HTTPClient: client, MaxConcurrent: 2, DueAdvancer: advancer, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestBusinessDuePeriodicSuccessAdvancesDueCursor(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	fixed := time.Now().UTC().Truncate(time.Second)
	advancer := &bdueFakeAdvancer{result: true}
	due := fixed.Add(-time.Minute)
	candidate := w7cBalanceCandidate(t, store, "bdue-success", func(c *Candidate) {
		c.IssuedAt = fixed
		c.NextRefreshAt = &due
	})
	runner := bdueRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, advancer, func() time.Time { return fixed })

	report, err := runner.RunPeriodic(context.Background(), []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("periodic report: %#v %v", report, err)
	}
	calls := advancer.recorded()
	if len(calls) != 1 {
		t.Fatalf("success must advance due exactly once, got %d calls", len(calls))
	}
	call := calls[0]
	if call.accountID != candidate.AccountID || call.configRevision != candidate.ConfigRevision {
		t.Fatalf("advance identity: %#v", call)
	}
	if call.expected == nil || !call.expected.UTC().Equal(due) {
		t.Fatalf("advance fence must use the frozen candidate due: %#v", call.expected)
	}
	if call.next == nil {
		t.Fatal("advance must carry the next refresh time")
	}
	lower, upper := fixed.Add(4*time.Minute+29*time.Second), fixed.Add(5*time.Minute+31*time.Second)
	if call.next.Before(lower) || call.next.After(upper) {
		t.Fatalf("next refresh must equal interval 5m plus jitter, got %s", call.next)
	}
}

func TestBusinessDueUnsupportedOutcomeDoesNotAdvance(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	advancer := &bdueFakeAdvancer{result: true}
	// Non-JSON upstream body: the outcome is committed as an unsupported
	// diagnostic, which is not a balance result and must not advance due.
	client := &w7cScriptedHTTP{behavior: func(*http.Request) (int, string) { return http.StatusNotFound, "nope" }}
	runner := bdueRunner(t, store, client, advancer, nil)

	report, err := runner.RunPeriodic(context.Background(), []Candidate{w7cBalanceCandidate(t, store, "bdue-unsupported", nil)})
	if err != nil || report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("unsupported report: %#v %v", report, err)
	}
	if calls := advancer.recorded(); len(calls) != 0 {
		t.Fatalf("unsupported outcome must not advance due: %#v", calls)
	}
}

func TestBusinessDueUpstreamErrorDoesNotAdvance(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	advancer := &bdueFakeAdvancer{result: true}
	client := &balanceTestHTTP{err: errors.New("dial upstream failed")}
	runner := bdueRunner(t, store, client, advancer, nil)

	report, err := runner.RunPeriodic(context.Background(), []Candidate{w7cBalanceCandidate(t, store, "bdue-transport", nil)})
	if err != nil || report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("transport error report: %#v %v", report, err)
	}
	snapshot, found, err := store.LoadSnapshot(context.Background(), "bdue-transport")
	if err != nil || !found || snapshot.Snapshot.Status == StatusFresh || snapshot.Snapshot.Status == StatusUnlimited {
		t.Fatalf("transport error must commit a non-success snapshot: %#v %t %v", snapshot, found, err)
	}
	if calls := advancer.recorded(); len(calls) != 0 {
		t.Fatalf("failed upstream query must not advance due: %#v", calls)
	}
}

func TestBusinessDueFirstProbeAndManualDoNotAdvance(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	advancer := &bdueFakeAdvancer{result: true}
	runner := bdueRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, advancer, nil)

	// First probe belongs to the worker_balance_detect enable chain.
	first := w7cBalanceCandidate(t, store, "bdue-first-probe", func(c *Candidate) {
		c.BalanceEnabled = false
		c.FirstProbe = true
	})
	report, err := runner.RunFirstProbe(context.Background(), []Candidate{first})
	if err != nil || report.Executed != 1 || len(report.Errors) != 0 {
		t.Fatalf("first probe report: %#v %v", report, err)
	}
	// Manual refresh scheduling belongs to the gateway.
	manual := w7cValidBalanceInput("bdue-manual")
	manual.Trigger = TriggerManual
	manualReport, err := runner.RunManual(context.Background(), manual)
	if err != nil || manualReport.Executed != 1 {
		t.Fatalf("manual report: %#v %v", manualReport, err)
	}
	if calls := advancer.recorded(); len(calls) != 0 {
		t.Fatalf("first probe/manual must not advance due: %#v", calls)
	}
}

func TestBusinessDueIdempotentReplayAdvancesOnce(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	advancer := &bdueFakeAdvancer{result: true}
	fixed := time.Now().UTC()
	runner := bdueRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, advancer, func() time.Time { return fixed })

	owner := w7cOwner(t, store, "bdue-replay-owner")
	lease := w7cAccount(t, store, owner, "bdue-replay")
	input := w7cValidBalanceInput("bdue-replay")
	input.IssuedAt = fixed
	input.ExpiresAt = fixed.Add(time.Minute)

	if _, err := runner.persistInput(context.Background(), owner, input, lease, QueryResult{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "1.25"}, Adapter: Adapter("builtin")}); err != nil {
		t.Fatalf("first persist: %v", err)
	}
	// Same input identity replays the committed outcome and must not advance again.
	state, err := runner.persistInput(context.Background(), owner, input, lease, QueryResult{Snapshot: Snapshot{Status: StatusFresh, RemainingUSD: "9.99"}, Adapter: Adapter("builtin")})
	if err != nil || state != runStateExecuted {
		t.Fatalf("replay persist: %v %v", state, err)
	}
	if calls := advancer.recorded(); len(calls) != 1 {
		t.Fatalf("replay must not advance twice: %d calls", len(calls))
	}
}

func TestBusinessDueAdvancerErrorStaysPerAccount(t *testing.T) {
	store := w7cNewSQLiteStore(t)
	advancer := &bdueFakeAdvancer{err: errors.New("business due update failed")}
	runner := bdueRunner(t, store, &w7cScriptedHTTP{behavior: w7cFreshBehavior}, advancer, nil)

	report, err := runner.RunPeriodic(context.Background(), []Candidate{w7cBalanceCandidate(t, store, "bdue-adv-err", nil)})
	if err != nil {
		t.Fatalf("advancer error must stay per-account: %v", err)
	}
	itemErr, ok := report.Errors["bdue-adv-err"]
	if !ok || !strings.Contains(itemErr.Error(), "business due update failed") {
		t.Fatalf("advancer error must keep the original message: %#v", report.Errors)
	}
}

// TestPostgresBusinessDueAdvanceSmoke verifies the real PostgreSQL statement
// (text due column, timestamptz fence, eligibility guard) against a throwaway
// dev database. Opt-in via JUHE_AI_J2_PG_SMOKE_ADMIN_URL, like the jobs store
// smoke; it never touches shared databases.
func TestPostgresBusinessDueAdvanceSmoke(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv("JUHE_AI_J2_PG_SMOKE_ADMIN_URL"))
	if adminURL == "" {
		t.Skip("JUHE_AI_J2_PG_SMOKE_ADMIN_URL 未设置")
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("juhe_ai_sub2api_dev_j2_due_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) }()
	targetURL, err := replaceDatabase(adminURL, name)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := sql.Open("pgx", targetURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(context.Background(), `
CREATE SCHEMA juhe_business;
CREATE TABLE juhe_business.accounts (
  id text PRIMARY KEY,
  config_revision bigint NOT NULL,
  status text NOT NULL,
  schedulable integer NOT NULL,
  type text NOT NULL,
  balance_query_enabled integer NOT NULL,
  balance_query_next_refresh_at text,
  deleted_at timestamptz,
  authorization_instance_authorization_id text,
  updated_at text NOT NULL
)`); err != nil {
		_ = bootstrap.Close()
		t.Fatal(err)
	}
	_ = bootstrap.Close()
	dueDB, err := sql.Open("pgx", targetURL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewBusinessDueStore(dueDB, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	next := time.Now().UTC().Add(5 * time.Minute)

	// RFC3339 millisecond due text, matching the historical Node/gateway writes.
	dueText := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond).Format(time.RFC3339Nano)
	seed := func(id string, due *string, status string, enabled int) {
		t.Helper()
		var dueValue any
		if due != nil {
			dueValue = *due
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO juhe_business.accounts
(id, config_revision, status, schedulable, type, balance_query_enabled, balance_query_next_refresh_at, deleted_at, authorization_instance_authorization_id, updated_at)
VALUES ($1, 7, $2, 1, 'api_key', $3, $4, NULL, NULL, '')`, id, status, enabled, dueValue); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := time.Parse(time.RFC3339Nano, dueText)
	if err != nil {
		t.Fatal(err)
	}

	seed("bdue-pg-hit", &dueText, "active", 1)
	advanced, err := store.AdvancePeriodicDue(ctx, "bdue-pg-hit", 7, &expected, &next)
	if err != nil || !advanced {
		t.Fatalf("fence-matched advance: %v %t", err, advanced)
	}
	var stored string
	if err := store.db.QueryRowContext(ctx, `SELECT balance_query_next_refresh_at FROM juhe_business.accounts WHERE id='bdue-pg-hit'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != balanceDueText(next) {
		t.Fatalf("stored due %s must equal canonical text %s", stored, balanceDueText(next))
	}
	if _, err := time.Parse(time.RFC3339Nano, stored); err != nil {
		t.Fatalf("stored due must stay RFC3339 parseable for the direct reader: %v", err)
	}

	// A different frozen due must not clobber the advanced schedule.
	staleExpected := expected.Add(-30 * time.Second)
	advanced, err = store.AdvancePeriodicDue(ctx, "bdue-pg-hit", 7, &staleExpected, &next)
	if err != nil || advanced {
		t.Fatalf("stale fence must miss: %v %t", err, advanced)
	}

	// Recovery rows carry a NULL schedule until the first successful refresh.
	seed("bdue-pg-recovery", nil, "active", 1)
	advanced, err = store.AdvancePeriodicDue(ctx, "bdue-pg-recovery", 7, nil, &next)
	if err != nil || !advanced {
		t.Fatalf("recovery advance: %v %t", err, advanced)
	}

	// Inactive or disabled rows stay untouched by the eligibility guard.
	seed("bdue-pg-inactive", &dueText, "inactive", 1)
	seed("bdue-pg-disabled", &dueText, "active", 0)
	for _, id := range []string{"bdue-pg-inactive", "bdue-pg-disabled"} {
		advanced, err = store.AdvancePeriodicDue(ctx, id, 7, &expected, &next)
		if err != nil || advanced {
			t.Fatalf("%s must be guarded: %v %t", id, err, advanced)
		}
	}
}
