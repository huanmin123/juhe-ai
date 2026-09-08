package accounts

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeListUsageSource records the requested scopes/statDates and serves fixed
// summary maps, so the hydration join, the scope construction and the error
// path can all be asserted without a real stats database.
type fakeListUsageSource struct {
	mu        sync.Mutex
	requests  []UsageScope
	statDates []string
	today     map[string]UsageSummary
	totals    map[string]UsageSummary
	err       error
}

func (f *fakeListUsageSource) AccountListUsageSummaries(_ context.Context, scopes []UsageScope, statDate string) (map[string]UsageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, scopes...)
	f.statDates = append(f.statDates, statDate)
	if f.err != nil {
		return nil, f.err
	}
	if statDate != "" {
		return f.today, nil
	}
	return f.totals, nil
}

func (f *fakeListUsageSource) recorded() ([]UsageScope, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]UsageScope(nil), f.requests...), append([]string(nil), f.statDates...)
}

// TestListPageUsageHydrationLocksIn mirrors Node
// hydrateAccountManagementStatusSeedsDirect: the list rows carry the daily
// (today) bucket in todayUsage and the totals aggregate in usage, the scope
// request uses {rowKey: id, systemAccountId, scopeType, scopeId}, missing
// entries keep the zero summary and a source error fails the list.
func TestListPageUsageHydrationLocksIn(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	env.seedAccount(t, "acc-usage-1", adminID, "usage-account", "active")

	source := &fakeListUsageSource{
		today:  map[string]UsageSummary{"acc-usage-1": {RequestCount: 5, TotalTokens: 120, TotalCost: 0.25}},
		totals: map[string]UsageSummary{"acc-usage-1": {RequestCount: 90, TotalTokens: 9000, TotalCost: 12.5}},
	}
	env.store.SetUsageSource(source)

	code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("list failed: %d %v", code, payload)
	}
	items := dataMap(t, payload)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %v", payload)
	}
	item := items[0].(map[string]any)
	today := item["todayUsage"].(map[string]any)
	if today["requestCount"].(float64) != 5 || today["totalTokens"].(float64) != 120 || today["totalCost"].(float64) != 0.25 {
		t.Fatalf("todayUsage not hydrated: %v", today)
	}
	usage := item["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 90 || usage["totalTokens"].(float64) != 9000 || usage["totalCost"].(float64) != 12.5 {
		t.Fatalf("usage not hydrated: %v", usage)
	}
	requests, statDates := source.recorded()
	if len(requests) != 2 || len(statDates) != 2 {
		t.Fatalf("expected two reads (daily+totals), got %v / %v", requests, statDates)
	}
	if statDates[0] == "" || statDates[1] != "" {
		t.Fatalf("first read must be the daily bucket, second the totals: %v", statDates)
	}
	for _, scope := range requests {
		if scope.RowKey != "acc-usage-1" || scope.SystemAccountID != adminID ||
			scope.ScopeType != usageScopeTypeAccount || scope.ScopeID != "acc-usage-1" {
			t.Fatalf("unexpected usage scope request: %+v", scope)
		}
	}

	// Missing map entries keep the zero summary.
	source.mu.Lock()
	source.today = map[string]UsageSummary{}
	source.totals = map[string]UsageSummary{}
	source.mu.Unlock()
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK {
		t.Fatalf("second list failed: %d %v", code, payload)
	}
	item = dataMap(t, payload)["items"].([]any)[0].(map[string]any)
	usage = item["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 0 || usage["totalTokens"].(float64) != 0 || usage["totalCost"].(float64) != 0 {
		t.Fatalf("missing usage must degrade to zero, got %v", usage)
	}

	// Source errors FAIL the list (Node rethrows out of the hydrate).
	source.mu.Lock()
	source.err = fmt.Errorf("stats database unavailable")
	source.mu.Unlock()
	if code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts", ""); code != http.StatusInternalServerError {
		t.Fatalf("usage source error must fail the list: %d", code)
	}
}

// TestStatsUsageSourceAccountListLocksIn drives the StatsUsageSource VALUES
// join against an in-memory SQLite stats database: the totals arm reads
// usage_stats_totals, the statDate arm reads usage_stats_daily at that key,
// and both render the bounded three-field projection.
func TestStatsUsageSourceAccountListLocksIn(t *testing.T) {
	db, err := sql.Open("sqlite", "file:accounts-stats-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE usage_stats_totals (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0, total_cost_usd REAL NOT NULL DEFAULT 0, last_used_at TEXT)`,
		`CREATE TABLE usage_stats_daily (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
			stat_date TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0, total_cost_usd REAL NOT NULL DEFAULT 0, last_used_at TEXT)`,
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('owner-1', 'account', 'acc-1', 90, 400, 500, 12.5)`,
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('owner-1', 'account_authorization', 'auth-9', 7, 70, 35, 1.25)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('owner-1', 'account', 'acc-1', '2026-09-06', 5, 40, 80, 0.25)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewStatsUsageSource(db, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scopes := []UsageScope{
		{RowKey: "acc-1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-1"},
		{RowKey: "acc-2", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-2"},
		{RowKey: "acc-1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-1"}, // duplicate
		{RowKey: "acc-bad", SystemAccountID: "", ScopeType: usageScopeTypeAccount, ScopeID: "acc-1"},      // invalid
	}

	totals, err := source.AccountListUsageSummaries(ctx, scopes, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := totals["acc-1"]; got != (UsageSummary{RequestCount: 90, TotalTokens: 900, TotalCost: 12.5}) {
		t.Fatalf("acc-1 totals mismatch: %+v", got)
	}
	if got, ok := totals["acc-2"]; !ok || got != (UsageSummary{}) {
		t.Fatalf("acc-2 must join with zeroed totals: %+v ok=%v", got, ok)
	}
	if _, ok := totals["acc-bad"]; ok {
		t.Fatal("invalid scope must not produce a row")
	}

	daily, err := source.AccountListUsageSummaries(ctx, scopes[:1], "2026-09-06")
	if err != nil {
		t.Fatal(err)
	}
	if got := daily["acc-1"]; got != (UsageSummary{RequestCount: 5, TotalTokens: 120, TotalCost: 0.25}) {
		t.Fatalf("acc-1 daily mismatch: %+v", got)
	}
	// A different stat date bucket is empty.
	other, err := source.AccountListUsageSummaries(ctx, scopes[:1], "2026-09-05")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := other["acc-1"]; !ok || got != (UsageSummary{}) {
		t.Fatalf("other-day bucket must join zeroed: %+v ok=%v", got, ok)
	}

	// The authorized-instance scope reads the account_authorization rows.
	authorized, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-3", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccountAuthorization, ScopeID: "auth-9"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := authorized["acc-3"]; got != (UsageSummary{RequestCount: 7, TotalTokens: 105, TotalCost: 1.25}) {
		t.Fatalf("authorized scope mismatch: %+v", got)
	}
}
