package apikeys

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeUsageSource records the scopes the store requested and serves a fixed
// summary map, so the hydration join (rowKey → item.Usage) and the zero
// degradation arm can both be asserted.
type fakeUsageSource struct {
	mu      sync.Mutex
	scopes  []UsageScope
	summary map[string]ListUsageSummary
	err     error
}

func (f *fakeUsageSource) ApiKeyListUsageSummaries(_ context.Context, scopes []UsageScope) (map[string]ListUsageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopes = append(f.scopes, scopes...)
	if f.err != nil {
		return nil, f.err
	}
	return f.summary, nil
}

func (f *fakeUsageSource) recorded() []UsageScope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]UsageScope(nil), f.scopes...)
}

// TestApiKeyListUsageHydrationLocksIn mirrors Node
// apiKeyListItemsFromRowsAndUsage: rows present in the stats map carry the
// summary, missing rows fall back to the zero summary, and the scope request
// uses { rowKey: id, systemAccountId, scopeId: id }.
func TestApiKeyListUsageHydrationLocksIn(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-usage-default")

	status, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"usage-key"}`)
	if status != http.StatusCreated {
		t.Fatalf("create failed: %d %v", status, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	source := &fakeUsageSource{summary: map[string]ListUsageSummary{
		id: {RequestCount: 7, TotalTokens: 1234, TotalCost: 0.5678},
	}}
	env.store.SetUsageSource(source)

	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if status != http.StatusOK {
		t.Fatalf("list failed: %d %v", status, payload)
	}
	items := dataMap(t, payload)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %v", payload)
	}
	usage := items[0].(map[string]any)["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 7 || usage["totalTokens"].(float64) != 1234 || usage["totalCost"].(float64) != 0.5678 {
		t.Fatalf("usage not hydrated: %v", usage)
	}
	scopes := source.recorded()
	if len(scopes) != 1 || scopes[0].RowKey != id || scopes[0].ScopeID != id || scopes[0].SystemAccountID != adminID {
		t.Fatalf("unexpected usage scope request: %+v", scopes)
	}

	// Missing map entry degrades to the zero summary (Node
	// `usage.get(row.id) ?? emptyApiKeyListUsageSummary`).
	source.mu.Lock()
	source.summary = map[string]ListUsageSummary{}
	source.mu.Unlock()
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	items = dataMap(t, payload)["items"].([]any)
	usage = items[0].(map[string]any)["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 0 || usage["totalTokens"].(float64) != 0 || usage["totalCost"].(float64) != 0 {
		t.Fatalf("missing usage must degrade to zero, got %v", usage)
	}

	// Source errors degrade too (J5 stats availability is advisory).
	source.mu.Lock()
	source.err = fmt.Errorf("stats database unavailable")
	source.mu.Unlock()
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if status != http.StatusOK {
		t.Fatalf("usage source error must not fail the list: %d %v", status, payload)
	}
	items = dataMap(t, payload)["items"].([]any)
	usage = items[0].(map[string]any)["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 0 {
		t.Fatalf("degraded usage must stay zero, got %v", usage)
	}
}

func newStatsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:apikeys-stats-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE usage_stats_totals (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES ('sysacc_1', 'api_key', 'key_1', 3, 10, 20, 1.5)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newEmptyStatsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:apikeys-stats-empty-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// TestStatsUsageSourceSQLLocksIn drives StatsUsageSource against an in-memory
// SQLite stats database and asserts the VALUES-join projection (including the
// input+output token sum and the missing-table degradation).
func TestStatsUsageSourceSQLLocksIn(t *testing.T) {
	source, err := NewStatsUsageSource(newStatsDB(t), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	summaries, err := source.ApiKeyListUsageSummaries(ctx, []UsageScope{
		{RowKey: "key_1", SystemAccountID: "sysacc_1", ScopeID: "key_1"},
		{RowKey: "key_2", SystemAccountID: "sysacc_2", ScopeID: "key_2"},
		{RowKey: "key_1", SystemAccountID: "sysacc_1", ScopeID: "key_1"}, // duplicate scope
		{RowKey: "", SystemAccountID: "sysacc_1", ScopeID: "key_1"},      // invalid scope
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := summaries["key_1"]; got != (ListUsageSummary{RequestCount: 3, TotalTokens: 10 + 20, TotalCost: 1.5}) {
		t.Fatalf("key_1 summary mismatch: %+v", got)
	}
	if got, ok := summaries["key_2"]; !ok || got != (ListUsageSummary{}) {
		t.Fatalf("key_2 must join with zeroed totals: %+v ok=%v", got, ok)
	}
	if _, ok := summaries[""]; ok {
		t.Fatal("invalid scope must not produce a row")
	}

	missing, err := NewStatsUsageSource(newEmptyStatsDB(t), false)
	if err != nil {
		t.Fatal(err)
	}
	degraded, err := missing.ApiKeyListUsageSummaries(ctx, []UsageScope{{RowKey: "key_1", SystemAccountID: "sysacc_1", ScopeID: "key_1"}})
	if err != nil {
		t.Fatalf("missing stats table must degrade, got error: %v", err)
	}
	if len(degraded) != 0 {
		t.Fatalf("degraded source must return an empty map, got %v", degraded)
	}
}
