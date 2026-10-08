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

// TestStatsUsageSourceAccountListLocksIn drives the StatsUsageSource VALUES
// join against an in-memory SQLite stats database: the totals arm reads
// usage_stats_totals, the statDate arm reads usage_stats_daily at that key,
// and both render the projection with the split input/cache-read columns
// (totalTokens 语义保持 input+output；cacheReadRate 按样本门装配).
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
			output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			total_cost_usd REAL NOT NULL DEFAULT 0,
			success_cost_usd REAL NOT NULL DEFAULT 0, last_used_at TEXT)`,
		`CREATE TABLE usage_stats_daily (
			system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
			stat_date TEXT NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0, cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			total_cost_usd REAL NOT NULL DEFAULT 0,
			success_cost_usd REAL NOT NULL DEFAULT 0, last_used_at TEXT)`,
		// total_cost_usd 与 success_cost_usd 刻意不同：断言读的是成功口径列。
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-1', 90, 400, 500, 120, 12.5, 10)`,
		`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, success_cost_usd)
			VALUES ('owner-1', 'account_authorization', 'auth-9', 7, 70, 35, 70, 1.25, 1)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-1', '2026-09-06', 5, 40, 80, 30, 0.25, 0.2)`,
		// 样本门与异常行覆盖：acc-ok 达门且 cache<=input（有效值）、acc-gate 门下、
		// acc-over cache_read>input 异常、acc-zero input=0。totalTokens 恒 input+output。
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, cache_read_tokens, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-ok', '2026-09-06', 3, 200000, 50, 100000, 1.5)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, cache_read_tokens, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-gate', '2026-09-06', 2, 99999, 10, 50000, 0.1)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, cache_read_tokens, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-over', '2026-09-06', 1, 200000, 10, 250000, 0.2)`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, cache_read_tokens, success_cost_usd)
			VALUES ('owner-1', 'account', 'acc-zero', '2026-09-06', 1, 0, 5, 0, 0)`,
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
	// 累计臂：totalTokens=input+output 语义不变；新列原样投影（rate 由样本门
	// 判 nil——400 与 70 都低于 100_000），累计 DTO 的三字段收敛由
	// TestListPageUsageHydrationLocksIn 在 hydrate 层锁定。
	if got := totals["acc-1"]; got != (TodayUsageSummary{RequestCount: 90, TotalTokens: 900, TotalCost: 10, InputTokens: 400, CacheReadTokens: 120}) {
		t.Fatalf("acc-1 totals mismatch: %+v", got)
	}
	if got, ok := totals["acc-2"]; !ok || got != (TodayUsageSummary{}) {
		t.Fatalf("acc-2 must join with zeroed totals: %+v ok=%v", got, ok)
	}
	if _, ok := totals["acc-bad"]; ok {
		t.Fatal("invalid scope must not produce a row")
	}

	daily, err := source.AccountListUsageSummaries(ctx, scopes[:1], "2026-09-06")
	if err != nil {
		t.Fatal(err)
	}
	if got := daily["acc-1"]; got != (TodayUsageSummary{RequestCount: 5, TotalTokens: 120, TotalCost: 0.2, InputTokens: 40, CacheReadTokens: 30}) {
		t.Fatalf("acc-1 daily mismatch: %+v", got)
	}
	// A different stat date bucket is empty.
	other, err := source.AccountListUsageSummaries(ctx, scopes[:1], "2026-09-05")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := other["acc-1"]; !ok || got != (TodayUsageSummary{}) {
		t.Fatalf("other-day bucket must join zeroed: %+v ok=%v", got, ok)
	}

	// 今日行的缓存率样本门与异常臂（设计 9.1 / 第 10 节）。
	rateRows, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-ok", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-ok"},
		{RowKey: "acc-gate", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-gate"},
		{RowKey: "acc-over", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-over"},
		{RowKey: "acc-zero", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccount, ScopeID: "acc-zero"},
	}, "2026-09-06")
	if err != nil {
		t.Fatal(err)
	}
	if got := rateRows["acc-ok"]; got.CacheReadRate == nil || *got.CacheReadRate != 0.5 ||
		got.InputTokens != 200_000 || got.CacheReadTokens != 100_000 || got.TotalTokens != 200_050 {
		t.Fatalf("acc-ok must render the valid 0.5 rate: %+v", got)
	}
	if got := rateRows["acc-gate"]; got.CacheReadRate != nil {
		t.Fatalf("below-gate input must render null rate: %+v", got)
	}
	if got := rateRows["acc-over"]; got.CacheReadRate != nil {
		t.Fatalf("cache_read > input must render null rate: %+v", got)
	}
	if got := rateRows["acc-zero"]; got.CacheReadRate != nil || got.InputTokens != 0 {
		t.Fatalf("zero input must render null rate: %+v", got)
	}

	// The authorized-instance scope reads the account_authorization rows.
	authorized, err := source.AccountListUsageSummaries(ctx, []UsageScope{
		{RowKey: "acc-3", SystemAccountID: "owner-1", ScopeType: usageScopeTypeAccountAuthorization, ScopeID: "auth-9"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := authorized["acc-3"]; got != (TodayUsageSummary{RequestCount: 7, TotalTokens: 105, TotalCost: 1, InputTokens: 70, CacheReadTokens: 70}) {
		t.Fatalf("authorized scope mismatch: %+v", got)
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
