package groups

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeGroupStatsReader records the requested group ids and serves a fixed
// stats map, so the ListPage accountStats merge (BUG-0175 D-126 分组面) can be
// asserted without the real juhe_stats handle.
type fakeGroupStatsReader struct {
	requested [][]string
	stats     map[string]AccountStats
	err       error
}

func (f *fakeGroupStatsReader) ReadGroupAccountStats(_ context.Context, groupIDs []string) (map[string]AccountStats, error) {
	f.requested = append(f.requested, append([]string(nil), groupIDs...))
	if f.err != nil {
		return nil, f.err
	}
	return f.stats, nil
}

// TestListPageHydratesAccountStats locks in the ListPage half of the D-38/D-126
// groups stats read chain: every list row merges the juhe_stats
// group_account_stats projection (counters plus the reader-owned
// TodayUsage/Usage payloads) into its accountStats, and a nil reader (or a
// reader error) keeps the empty projection instead of failing the page.
func TestListPageHydratesAccountStats(t *testing.T) {
	db, err := sql.Open("sqlite", "file:groups-stats-hydrate-"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE providers (code TEXT PRIMARY KEY, enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL DEFAULT 'personal', scheduling_policy_json TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO providers (code, enabled) VALUES ('openai', 1)`); err != nil {
		t.Fatal(err)
	}

	reader := &fakeGroupStatsReader{stats: map[string]AccountStats{
		// Filled below once the group id exists.
	}}
	store, err := NewStore(db, false, nil, nil, nil, WithStatsReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), MutationInput{
		Name: ptrString("stats-group"), ProviderCode: ptrString("openai"),
	}, AccessScope{ViewerID: "owner-1"})
	if err != nil {
		t.Fatal(err)
	}
	reader.stats[created.ID] = AccountStats{
		Total: 3, Available: 2, Active: 2, Disabled: 1,
		CurrentConcurrency: 1, ConcurrencyLimit: 10,
		TodayUsage: map[string]any{"requestCount": 12},
		Usage:      map[string]any{"requestCount": 99},
	}

	access := AccessScope{ViewerID: "owner-1", IsAdmin: true}
	page, err := store.ListPage(context.Background(), access, 1, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected one row, got %v", page.Items)
	}
	stats := page.Items[0].AccountStats
	if stats.Total != 3 || stats.Available != 2 || stats.Active != 2 || stats.Disabled != 1 ||
		stats.CurrentConcurrency != 1 || stats.ConcurrencyLimit != 10 {
		t.Fatalf("accountStats counters not hydrated: %+v", stats)
	}
	if usage, ok := stats.TodayUsage.(map[string]any); !ok || usage["requestCount"] != 12 {
		t.Fatalf("todayUsage not hydrated: %#v", stats.TodayUsage)
	}
	if usage, ok := stats.Usage.(map[string]any); !ok || usage["requestCount"] != 99 {
		t.Fatalf("usage not hydrated: %#v", stats.Usage)
	}
	if len(reader.requested) != 1 || len(reader.requested[0]) != 1 || reader.requested[0][0] != created.ID {
		t.Fatalf("unexpected stats reader requests: %+v", reader.requested)
	}

	// Reader errors keep the empty projection (the D-38 merge precedent).
	reader.err = sql.ErrConnDone
	page, err = store.ListPage(context.Background(), access, 1, 50, "")
	if err != nil {
		t.Fatalf("reader error must not fail the page: %v", err)
	}
	if page.Items[0].AccountStats.Total != 0 {
		t.Fatalf("reader error must keep the empty stats: %+v", page.Items[0].AccountStats)
	}
}
