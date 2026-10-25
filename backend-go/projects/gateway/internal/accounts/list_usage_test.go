package accounts

import (
	"context"
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
	today     map[string]TodayUsageSummary
	totals    map[string]TodayUsageSummary
	err       error
}

func (f *fakeListUsageSource) AccountListUsageSummaries(_ context.Context, scopes []UsageScope, statDate string) (map[string]TodayUsageSummary, error) {
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

// todayCacheRate is the test helper for the non-omitempty CacheReadRate
// pointer (nil stays nil and serializes as "no valid sample").
func todayCacheRate(rate float64) *float64 { return &rate }

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
		today: map[string]TodayUsageSummary{"acc-usage-1": {
			RequestCount: 5, TotalTokens: 120, TotalCost: 0.25,
			InputTokens: 200_000, CacheReadTokens: 100_000, CacheReadRate: todayCacheRate(0.5),
		}},
		totals: map[string]TodayUsageSummary{"acc-usage-1": {
			RequestCount: 90, TotalTokens: 9000, TotalCost: 12.5,
			InputTokens: 400_000, CacheReadTokens: 200_000, CacheReadRate: todayCacheRate(0.5),
		}},
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
	// 设计 9.1：todayUsage 携带新字段（cacheReadRate 无 omitempty，nil 出 null）。
	if today["inputTokens"].(float64) != 200_000 || today["cacheReadTokens"].(float64) != 100_000 {
		t.Fatalf("todayUsage cache projection not hydrated: %v", today)
	}
	if rate, ok := today["cacheReadRate"].(float64); !ok || rate != 0.5 {
		t.Fatalf("todayUsage cacheReadRate must render the valid ratio: %v", today)
	}
	usage := item["usage"].(map[string]any)
	if usage["requestCount"].(float64) != 90 || usage["totalTokens"].(float64) != 9000 || usage["totalCost"].(float64) != 12.5 {
		t.Fatalf("usage not hydrated: %v", usage)
	}
	// 累计 usage 三字段形状锁：今日字段（含今日才有的 cacheReadRate）禁止泄漏
	// 进累计对象，即使共享读口结果携带了新字段。
	for _, leaked := range []string{"inputTokens", "cacheReadTokens", "cacheReadRate"} {
		if _, present := usage[leaked]; present {
			t.Fatalf("usage must keep the three-field shape, leaked %q: %v", leaked, usage)
		}
	}
	if len(usage) != 3 {
		t.Fatalf("usage must serialize exactly requestCount/totalTokens/totalCost: %v", usage)
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
	source.today = map[string]TodayUsageSummary{}
	source.totals = map[string]TodayUsageSummary{}
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
	// 今日零值形状：cacheReadRate 无 omitempty，缺失数据序列化为 null。
	zeroToday := item["todayUsage"].(map[string]any)
	if value, present := zeroToday["cacheReadRate"]; !present || value != nil {
		t.Fatalf("zero todayUsage must serialize cacheReadRate as null: %v", zeroToday)
	}

	// Source errors FAIL the list (Node rethrows out of the hydrate).
	source.mu.Lock()
	source.err = fmt.Errorf("stats database unavailable")
	source.mu.Unlock()
	if code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts", ""); code != http.StatusInternalServerError {
		t.Fatalf("usage source error must fail the list: %d", code)
	}
}

// TestTodayCacheReadRateGate locks the cacheReadRate sample gate（设计 9.1 /
// 第 10 节）：input 未达 CacheMinSampleInputTokens 或 cache_read > input 一律
// nil；达门样本 rate = cache_read / input（含 0% 与 100% 边界）。
func TestTodayCacheReadRateGate(t *testing.T) {
	cases := []struct {
		name            string
		inputTokens     int
		cacheReadTokens int
		wantNil         bool
		want            float64
	}{
		{"valid sample", 200_000, 100_000, false, 0.5},
		{"exactly at the gate", CacheMinSampleInputTokens, 60_000, false, 0.6},
		{"zero read at the gate", CacheMinSampleInputTokens, 0, false, 0},
		{"full read", CacheMinSampleInputTokens, CacheMinSampleInputTokens, false, 1},
		{"below the gate", CacheMinSampleInputTokens - 1, 50_000, true, 0},
		{"over-read invalid", 200_000, 250_000, true, 0},
		{"zero input", 0, 0, true, 0},
	}
	for _, tc := range cases {
		got := cacheReadRate(tc.inputTokens, tc.cacheReadTokens)
		if tc.wantNil {
			if got != nil {
				t.Fatalf("%s: expected nil rate, got %v", tc.name, *got)
			}
			continue
		}
		if got == nil || *got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}
