package groups

// 运行时并发 hydrate 回归（Node→Go 移植缺口：分组列表“当前并发”恒 0）：
// ListPage 在 accountStats 合并后按分组成员实时求和网关进程内 tracker 读数，
// 覆盖 stats 表 current_concurrency（jobs 写侧当前硬编码 0）。Node 原语义是
// 列表请求时实时求和（group-read-loaders → shared/account-concurrency）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeConcurrencyReader records the requested account ids and serves a fixed
// concurrency map.
type fakeConcurrencyReader struct {
	requested [][]string
	currents  map[string]int
	err       error
}

func (f *fakeConcurrencyReader) LoadCurrentConcurrencyByID(_ context.Context, accountIDs []string) (map[string]int, error) {
	f.requested = append(f.requested, append([]string(nil), accountIDs...))
	if f.err != nil {
		return nil, f.err
	}
	return f.currents, nil
}

// statsFuncReader adapts a function to the StatsReader port.
type statsFuncReader func(ctx context.Context, groupIDs []string) (map[string]AccountStats, error)

func (f statsFuncReader) ReadGroupAccountStats(ctx context.Context, groupIDs []string) (map[string]AccountStats, error) {
	return f(ctx, groupIDs)
}

// newRuntimeConcurrencyFixture builds the minimal business schema (members +
// accounts + authorizations joins behind groupAccountIDsByGroupIDs) and seeds
// two groups: one with enabled members acc-1/acc-2 and one with no enabled
// members.
func newRuntimeConcurrencyFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", "file:groups-runtime-concurrency-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
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
	store, err := NewStore(db, false, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	access := AccessScope{ViewerID: "owner-1", IsAdmin: true}
	grp, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("concurrency-group"), ProviderCode: ptrString("openai"),
	}, access)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("empty-group"), ProviderCode: ptrString("openai"),
	}, access)
	if err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []string{"acc-1", "acc-2", "acc-orphan-disabled"} {
		if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id) VALUES (?, 'owner-1')`, accountID); err != nil {
			t.Fatal(err)
		}
	}
	// grp members acc-1/acc-2; empty only holds a disabled binding, which the
	// member query must keep out of the sum.
	for _, binding := range []struct{ groupID, accountID, enabled string }{
		{grp.ID, "acc-1", "1"}, {grp.ID, "acc-2", "1"}, {empty.ID, "acc-orphan-disabled", "0"},
	} {
		if _, err := db.Exec(`INSERT INTO group_accounts (system_account_id, group_id, account_id, enabled, created_at, updated_at)
			VALUES ('owner-1', ?, ?, ?, '2026-09-27T00:00:00.000Z', '2026-09-27T00:00:00.000Z')`,
			binding.groupID, binding.accountID, binding.enabled); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// TestListPageHydratesRuntimeConcurrency locks the overlay contract: the
// per-member live sum replaces the stats-table current_concurrency for every
// row (multi-group, multi-member), the empty-member group renders 0, and the
// reader receives the deduplicated enabled member id set.
func TestListPageHydratesRuntimeConcurrency(t *testing.T) {
	store := newRuntimeConcurrencyFixture(t)
	reader := &fakeConcurrencyReader{currents: map[string]int{"acc-1": 2, "acc-2": 3, "acc-orphan-disabled": 9}}
	store.concurrency = reader

	page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1", IsAdmin: true}, 1, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string]int{}
	for _, item := range page.Items {
		sums[item.Name] = item.AccountStats.CurrentConcurrency
	}
	if sums["concurrency-group"] != 5 {
		t.Fatalf("member sum must be 2+3=5, got %v", sums)
	}
	if sums["empty-group"] != 0 {
		t.Fatalf("empty-member group must render 0 (disabled binding stays out), got %v", sums)
	}
	// The reader sees the deduplicated enabled member ids; the disabled
	// binding's account never reaches it.
	if len(reader.requested) != 1 {
		t.Fatalf("expected one reader call, got %v", reader.requested)
	}
	if len(reader.requested[0]) != 2 {
		t.Fatalf("expected deduplicated member ids [acc-1 acc-2], got %v", reader.requested[0])
	}
}

// TestListPageRuntimeConcurrencyReplacesStatsValue pins the replace (not add)
// semantics: the stats-reader-served column (stale non-zero) is overwritten by
// the live sum, and a nil port keeps the stats value untouched.
func TestListPageRuntimeConcurrencyReplacesStatsValue(t *testing.T) {
	store := newRuntimeConcurrencyFixture(t)
	store.stats = statsFuncReader(func(_ context.Context, groupIDs []string) (map[string]AccountStats, error) {
		out := map[string]AccountStats{}
		for _, id := range groupIDs {
			out[id] = AccountStats{CurrentConcurrency: 7, Total: 2}
		}
		return out, nil
	})
	sums := func() map[string]int {
		page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1", IsAdmin: true}, 1, 50, "")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, item := range page.Items {
			out[item.Name] = item.AccountStats.CurrentConcurrency
		}
		return out
	}
	// Live sum replaces the stale stats column even when smaller.
	store.concurrency = &fakeConcurrencyReader{currents: map[string]int{"acc-1": 1, "acc-2": 2}}
	if got := sums()["concurrency-group"]; got != 3 {
		t.Fatalf("live sum must replace the stats column, got %v", got)
	}
	// nil port keeps the stats value.
	store.concurrency = nil
	if got := sums()["concurrency-group"]; got != 7 {
		t.Fatalf("nil port must keep the stats value, got %v", got)
	}
}

// TestListPageRuntimeConcurrencyReaderErrorKeepsStatsValue locks the
// degradation contract: a tracker failure never fails the page and keeps the
// stats-table value instead of zeroing it.
func TestListPageRuntimeConcurrencyReaderErrorKeepsStatsValue(t *testing.T) {
	store := newRuntimeConcurrencyFixture(t)
	store.stats = statsFuncReader(func(_ context.Context, groupIDs []string) (map[string]AccountStats, error) {
		out := map[string]AccountStats{}
		for _, id := range groupIDs {
			out[id] = AccountStats{CurrentConcurrency: 4, Total: 2}
		}
		return out, nil
	})
	store.concurrency = &fakeConcurrencyReader{err: errors.New("tracker down")}

	page, err := store.ListPage(context.Background(), AccessScope{ViewerID: "owner-1", IsAdmin: true}, 1, 50, "")
	if err != nil {
		t.Fatalf("tracker failure must not fail the page: %v", err)
	}
	for _, item := range page.Items {
		if item.AccountStats.CurrentConcurrency != 4 {
			t.Fatalf("stats value must survive the tracker failure: %+v", item.AccountStats)
		}
	}
}
