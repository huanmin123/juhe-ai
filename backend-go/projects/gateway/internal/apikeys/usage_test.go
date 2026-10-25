package apikeys

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	_ "modernc.org/sqlite"
)

// fakeUsageSource records the scopes the store requested and serves a fixed
// summary map, so the hydration join (rowKey → item.Usage) and the zero
// degradation arm can both be asserted.
type fakeUsageSource struct {
	mu            sync.Mutex
	scopes        []UsageScope
	summary       map[string]ListUsageSummary
	fullSummaries map[string]AccountUsageSummary
	err           error
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

func (f *fakeUsageSource) ApiKeyUsageSummaries(_ context.Context, scopes []UsageScope) (map[string]AccountUsageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopes = append(f.scopes, scopes...)
	if f.err != nil {
		return nil, f.err
	}
	return f.fullSummaries, nil
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

	// Source errors FAIL the list (Node: only the missing-resource SQLite arm
	// degrades inside the loader; every other error propagates out of
	// loadApiKeyListUsageSummariesForScopesAsync).
	source.mu.Lock()
	source.err = fmt.Errorf("stats database unavailable")
	source.mu.Unlock()
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys", "")
	if status != http.StatusInternalServerError {
		t.Fatalf("usage source error must fail the list: %d %v", status, payload)
	}
}

func newStatsDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE TABLE juhe_stats.usage_stats_totals (
	system_account_id text NOT NULL, scope_type text NOT NULL, scope_id text NOT NULL,
	request_count bigint NOT NULL DEFAULT 0, input_tokens bigint NOT NULL DEFAULT 0,
	output_tokens bigint NOT NULL DEFAULT 0, cache_read_tokens bigint NOT NULL DEFAULT 0,
	cache_read_cost_usd double precision NOT NULL DEFAULT 0, cache_write_tokens bigint,
	cache_write_1h_tokens bigint, cache_write_cost_usd double precision, thinking_tokens bigint,
	input_image_tokens bigint, output_image_tokens bigint, total_cost_usd double precision NOT NULL DEFAULT 0,
	success_cost_usd double precision NOT NULL DEFAULT 0, last_used_at text,
	PRIMARY KEY (system_account_id, scope_type, scope_id));
INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, thinking_tokens, total_cost_usd, success_cost_usd, last_used_at)
VALUES ('sysacc_1', 'api_key', 'key_1', 3, 10, 20, 4, 0.25, 5, 1.5, 1.5, '2026-09-01T08:30:00.000Z')`)
		return err
	})
	return db
}

func newEmptyStatsDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`)
		return err
	})
	return db
}

func TestStatsUsageSourceFullSummaryLocksIn(t *testing.T) {
	source, err := NewStatsUsageSource(newStatsDB(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	summaries, err := source.ApiKeyUsageSummaries(ctx, []UsageScope{
		{RowKey: "key_1", SystemAccountID: "sysacc_1", ScopeID: "key_1"},
		{RowKey: "key_missing", SystemAccountID: "sysacc_1", ScopeID: "key_missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := summaries["key_1"]
	if got.RequestCount != 3 || got.InputTokens != 10 || got.OutputTokens != 20 || got.TotalTokens != 30 ||
		got.CacheReadTokens != 4 || got.CacheReadCost != 0.25 || got.ThinkingTokens != 5 || got.TotalCost != 1.5 {
		t.Fatalf("full summary mismatch: %+v", got)
	}
	if got.LastUsedAt == nil || *got.LastUsedAt != "2026-09-01T08:30:00.000Z" {
		t.Fatalf("last used at: %v", got.LastUsedAt)
	}
	// Missing stats rows fall back to the zero summary with lastUsedAt omitted.
	zero := summaries["key_missing"]
	if zero != (AccountUsageSummary{}) || zero.LastUsedAt != nil {
		t.Fatalf("missing row must render the zero summary: %+v", zero)
	}

	// Detail hydration: FindDetail renders the full summary; a nil source keeps
	// the all-zero object (never null).
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, adminID, "rs-usage-detail")
	status, payload := env.do(t, http.MethodPost, "/__aisys__/api/api-keys", `{"name":"detail-usage"}`)
	if status != http.StatusCreated {
		t.Fatalf("create failed: %d %v", status, payload)
	}
	id := dataMap(t, payload)["id"].(string)
	detailSource := &fakeUsageSource{fullSummaries: map[string]AccountUsageSummary{
		id: {RequestCount: 9, InputTokens: 11, OutputTokens: 22, TotalTokens: 33, TotalCost: 0.75,
			CacheReadCost: 0.1},
	}}
	env.store.SetUsageSource(detailSource)
	status, payload = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("detail failed: %d %v", status, payload)
	}
	usage := dataMap(t, payload)["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 9 || usage["totalTokens"].(float64) != 33 ||
		usage["inputTokens"].(float64) != 11 || usage["outputTokens"].(float64) != 22 ||
		usage["cacheReadCost"].(float64) != 0.1 {
		t.Fatalf("detail usage not hydrated with the full summary: %v", usage)
	}
	// Detail source errors fail the detail read like the list.
	detailSource.mu.Lock()
	detailSource.err = context.DeadlineExceeded
	detailSource.mu.Unlock()
	if status, _ = env.do(t, http.MethodGet, "/__aisys__/api/api-keys/"+id, ""); status != http.StatusInternalServerError {
		t.Fatalf("detail usage error must fail the read: %d", status)
	}
}
