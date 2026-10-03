package groups

// 分组列表/详情 usage hydrate 契约回归（fake UsageSource，不用真 juhe_stats
// 句柄）：today/total 两值注水、授权 scope 三元组构造、nil source 零值降级、
// source 错误 warn+空投影、行存在时 two key 非 nil。
//
// 修复前必红：WithUsageSource / hydrateListUsage / hydrateDetailUsage 不存在，
// ListPage/FindDetail 的 TodayUsage/Usage 从未被真实 hydrate（"用量(日)"列
// 恒 0），本文件无法编译，等价于全红。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// fakeGroupUsageSource records the requested scopes/statDates and serves the
// fixed today/totals maps.
type fakeGroupUsageSource struct {
	scopes    [][]UsageScope
	statDates []string
	todays    map[string]UsageSummary
	totals    map[string]UsageSummary
	err       error
}

func (f *fakeGroupUsageSource) GroupListUsageSummaries(_ context.Context, scopes []UsageScope, statDate string) (map[string]UsageSummary, error) {
	f.scopes = append(f.scopes, append([]UsageScope(nil), scopes...))
	f.statDates = append(f.statDates, statDate)
	if f.err != nil {
		return nil, f.err
	}
	if statDate != "" {
		return f.todays, nil
	}
	return f.totals, nil
}

// newUsageHydrateFixture builds the minimal business schema (providers +
// system_accounts + groups, mirroring stats_hydration_test.go).
func newUsageHydrateFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:groups-usage-hydrate-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE group_accounts (system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, account_id TEXT NOT NULL, account_authorization_id TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (group_id, account_id))`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, deleted_at TEXT)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, status TEXT NOT NULL)`,
		`INSERT INTO providers (code, enabled) VALUES ('openai', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// TestListPageHydratesUsageSummaries locks the list half of the usage wiring:
// every list row receives the fake today/totals summaries, the recorded scope
// is the owner group triple and the today call carries today's stat date key
// while the totals call passes none.
func TestListPageHydratesUsageSummaries(t *testing.T) {
	db := newUsageHydrateFixture(t)
	source := &fakeGroupUsageSource{}
	store, err := NewStore(db, false, nil, nil, nil, WithUsageSource(source))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("usage-group"), ProviderCode: ptrString("openai"),
	}, AccessScope{ViewerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	source.todays = map[string]UsageSummary{
		created.ID: {RequestCount: 12, InputTokens: 100, OutputTokens: 40, TotalCost: 1.5},
	}
	source.totals = map[string]UsageSummary{
		created.ID: {RequestCount: 99, TotalCost: 42.25},
	}

	page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1"}, 1, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected one row, got %v", page.Items)
	}
	stats := page.Items[0].AccountStats
	today, ok := stats.TodayUsage.(UsageSummary)
	if !ok || today.RequestCount != 12 || today.TotalTokens != 140 || today.TotalCost != 1.5 {
		t.Fatalf("todayUsage not hydrated: %#v", stats.TodayUsage)
	}
	usage, ok := stats.Usage.(UsageSummary)
	if !ok || usage.RequestCount != 99 || usage.TotalCost != 42.25 {
		t.Fatalf("usage not hydrated: %#v", stats.Usage)
	}

	// scope 三元组：自有行 → group scope，scope_id = 分组 ID，
	// system_account_id = 分组所有者（owner-1）。
	if len(source.scopes) != 2 {
		t.Fatalf("expected today+totals reads, got %d: %+v", len(source.scopes), source.scopes)
	}
	first := source.scopes[0]
	if len(first) != 1 || first[0] != (UsageScope{RowKey: created.ID, SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: created.ID}) {
		t.Fatalf("unexpected scope triple: %+v", first)
	}
	// statDate：today 调用带当日 key（fixture 无 system_settings → UTC 回退），
	// totals 调用为空。
	if _, err := time.Parse("2006-01-02", source.statDates[0]); err != nil {
		t.Fatalf("today call must carry a YYYY-MM-DD stat date: %q", source.statDates[0])
	}
	if source.statDates[1] != "" {
		t.Fatalf("totals call must pass no stat date: %q", source.statDates[1])
	}
}

// TestFindDetailHydratesUsage locks the detail half: findGroupSummary loads
// the same summaries (Node semantics — FindDetail hydrates too).
func TestFindDetailHydratesUsage(t *testing.T) {
	db := newUsageHydrateFixture(t)
	source := &fakeGroupUsageSource{}
	store, err := NewStore(db, false, nil, nil, nil, WithUsageSource(source))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("usage-detail"), ProviderCode: ptrString("openai"),
	}, AccessScope{ViewerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	source.todays = map[string]UsageSummary{
		created.ID: {RequestCount: 3, TotalTokens: 7},
	}
	source.totals = map[string]UsageSummary{
		created.ID: {RequestCount: 8, TotalCost: 0.5},
	}

	detail, err := store.FindDetail(context.Background(), created.ID, AccessScope{ViewerID: "owner-1"})
	if err != nil || detail == nil {
		t.Fatalf("find detail: %v/%v", detail, err)
	}
	today, ok := detail.AccountStats.TodayUsage.(UsageSummary)
	if !ok || today.RequestCount != 3 || today.TotalTokens != 7 {
		t.Fatalf("detail todayUsage not hydrated: %#v", detail.AccountStats.TodayUsage)
	}
	usage, ok := detail.AccountStats.Usage.(UsageSummary)
	if !ok || usage.RequestCount != 8 || usage.TotalCost != 0.5 {
		t.Fatalf("detail usage not hydrated: %#v", detail.AccountStats.Usage)
	}
}

// TestListPageUsageNilSourceKeepsEmptyShape locks the nil-source degradation:
// the two usage keys stay the emptyAccountUsageSummary 13-key shape (non-nil)
// with all-zero values — the create payload (8 counters, no keys) contract of
// groups_test.go is untouched.
func TestListPageUsageNilSourceKeepsEmptyShape(t *testing.T) {
	db := newUsageHydrateFixture(t)
	store, err := NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("usage-nil"), ProviderCode: ptrString("openai"),
	}, AccessScope{ViewerID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1"}, 1, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	stats := page.Items[0].AccountStats
	today, ok := stats.TodayUsage.(map[string]any)
	if !ok || len(today) != 13 || today["requestCount"] != 0 {
		t.Fatalf("nil source must keep the empty 13-key shape: %#v", stats.TodayUsage)
	}
	usage, ok := stats.Usage.(map[string]any)
	if !ok || len(usage) != 13 || usage["requestCount"] != 0 {
		t.Fatalf("nil source must keep the empty 13-key shape: %#v", stats.Usage)
	}
}

// TestListPageUsageSourceErrorDegrades locks the failure degradation: a usage
// source error keeps the empty projection instead of failing the page (D-38
// hydrate precedent; 账户面 fail-fast 是账户面的策略，分组面有意偏差).
func TestListPageUsageSourceErrorDegrades(t *testing.T) {
	db := newUsageHydrateFixture(t)
	source := &fakeGroupUsageSource{err: sql.ErrConnDone}
	store, err := NewStore(db, false, nil, nil, nil, WithUsageSource(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("usage-err"), ProviderCode: ptrString("openai"),
	}, AccessScope{ViewerID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1"}, 1, 50, "")
	if err != nil {
		t.Fatalf("usage source error must not fail the page: %v", err)
	}
	stats := page.Items[0].AccountStats
	today, ok := stats.TodayUsage.(map[string]any)
	if !ok || len(today) != 13 {
		t.Fatalf("source error must keep the empty shape: %#v", stats.TodayUsage)
	}
}

// TestGroupUsageScopeMapping locks the scope construction: authorized rows
// with an authorization id read the group_authorization scope, everything
// else reads the group scope; system_account_id is always the group owner.
func TestGroupUsageScopeMapping(t *testing.T) {
	authorized := groupUsageScope("grp_1", "owner-1", "authorized", "ra_9")
	if authorized != (UsageScope{RowKey: "grp_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroupAuthorization, ScopeID: "ra_9"}) {
		t.Fatalf("authorized scope: %+v", authorized)
	}
	authorizedNoID := groupUsageScope("grp_1", "owner-1", "authorized", "")
	if authorizedNoID != (UsageScope{RowKey: "grp_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_1"}) {
		t.Fatalf("authorized without authorization id must fall back to the group scope: %+v", authorizedNoID)
	}
	owner := groupUsageScope("grp_1", "owner-1", "owner", "")
	if owner != (UsageScope{RowKey: "grp_1", SystemAccountID: "owner-1", ScopeType: usageScopeTypeGroup, ScopeID: "grp_1"}) {
		t.Fatalf("owner scope: %+v", owner)
	}
}

// TestAccountStatsFromGroupRowKeepsEmptyUsageShape locks the base projection:
// a group_account_stats row renders with non-nil TodayUsage/Usage (the
// emptyAccountUsageSummary shape) — 修复前 accountStatsFromGroupRow 整体替换
// emptyAccountStats，行存在时连零值 key 都缺失（本断言修复前必红）。
func TestAccountStatsFromGroupRowKeepsEmptyUsageShape(t *testing.T) {
	stats := accountStatsFromGroupRow(groupAccountStatsRow{
		GroupID: "grp_1", Total: 3, Available: 2, CurrentConcurrency: 1, ConcurrencyLimit: 10,
	})
	if stats.Total != 3 || stats.Available != 2 || stats.CurrentConcurrency != 1 || stats.ConcurrencyLimit != 10 {
		t.Fatalf("counters mismatch: %+v", stats)
	}
	today, ok := stats.TodayUsage.(map[string]any)
	if !ok || len(today) != 13 {
		t.Fatalf("TodayUsage must stay non-nil empty shape: %#v", stats.TodayUsage)
	}
	usage, ok := stats.Usage.(map[string]any)
	if !ok || len(usage) != 13 {
		t.Fatalf("Usage must stay non-nil empty shape: %#v", stats.Usage)
	}
}
