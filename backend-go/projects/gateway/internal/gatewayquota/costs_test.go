package gatewayquota

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStatsStoreLoadCosts(t *testing.T) {
	db := newTestDB(t, "loadcosts")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	location := time.UTC
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

	seedCost(t, db, "usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", 12.5})
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-04", 3.25})
	seedCost(t, db, "usage_stats_weekly", []string{"system_account_id", "scope_type", "scope_id", "stat_week", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-08-31", 7})
	seedCost(t, db, "usage_stats_monthly", []string{"system_account_id", "scope_type", "scope_id", "stat_month", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09", 9.75})
	seedCost(t, db, "usage_quota_hourly_windows", []string{"system_account_id", "scope_type", "scope_id", "window_hours", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", 3, 1.5})

	costs, err := stats.LoadCosts(context.Background(), CostInput{
		SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: now, HourlyWindowHours: 3, HasHourlyWindow: true,
	}, location)
	if err != nil {
		t.Fatalf("LoadCosts: %v", err)
	}
	want := RequestQuotaCosts{Hourly: 1.5, Daily: 3.25, Weekly: 7, Monthly: 9.75, Total: 12.5}
	if costs != want {
		t.Fatalf("LoadCosts = %+v, want %+v", costs, want)
	}

	// Without the hourly window the hourly column stays 0 while the rest load.
	costs, err = stats.LoadCosts(context.Background(), CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: now}, location)
	if err != nil {
		t.Fatalf("LoadCosts (no hourly): %v", err)
	}
	if costs.Hourly != 0 {
		t.Fatalf("hourly must stay 0 without window, got %v", costs.Hourly)
	}

	// Unknown scope yields all zeros (missing rows -> COALESCE-less miss).
	costs, err = stats.LoadCosts(context.Background(), CostInput{SystemAccountID: "other", ScopeType: "api_key", ScopeID: "x", Now: now}, location)
	if err != nil {
		t.Fatalf("LoadCosts (unknown): %v", err)
	}
	if costs != EmptyRequestQuotaCosts() {
		t.Fatalf("unknown scope must be zero, got %+v", costs)
	}
}

func TestStatsStoreLoadCostsBatch(t *testing.T) {
	db := newTestDB(t, "batch")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	location := time.UTC
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

	seedCost(t, db, "usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id", "total_cost_usd"},
		[]any{"sys", "api_key", "ak1", 1})
	seedCost(t, db, "usage_stats_totals", []string{"system_account_id", "scope_type", "scope_id", "total_cost_usd"},
		[]any{"sys", "api_key", "ak2", 2})
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sys", "api_key", "ak1", "2026-09-04", 4})
	seedCost(t, db, "usage_quota_hourly_windows", []string{"system_account_id", "scope_type", "scope_id", "window_hours", "total_cost_usd"},
		[]any{"sys", "api_key", "ak2", 6, 0.5})

	inputs := []CostInput{
		{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak1", Now: now},
		{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak2", Now: now, HourlyWindowHours: 6, HasHourlyWindow: true},
		// duplicate of the second input (different window normalization is
		// identity here, so dedupe collapses to one request)
		{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak2", Now: now, HourlyWindowHours: 6, HasHourlyWindow: true},
	}
	byKey, err := stats.LoadCostsBatch(context.Background(), inputs, location)
	if err != nil {
		t.Fatalf("LoadCostsBatch: %v", err)
	}
	if len(byKey) != 2 {
		t.Fatalf("batch must dedupe to 2 requests, got %d", len(byKey))
	}
	first := byKey[CostKey(inputs[0], location)]
	if first.Total != 1 || first.Daily != 4 || first.Hourly != 0 {
		t.Fatalf("ak1 costs = %+v", first)
	}
	second := byKey[CostKey(inputs[1], location)]
	if second.Total != 2 || second.Hourly != 0.5 || second.Daily != 0 {
		t.Fatalf("ak2 costs = %+v", second)
	}
}

func TestStatsStorePostgreSQLBindingAndQualification(t *testing.T) {
	stats, err := NewStatsStore(nil, false)
	if err == nil {
		t.Fatal("nil db must be rejected")
	}
	_ = stats
	query := bindPlaceholders(true, "SELECT * FROM t WHERE a = ? AND b = ? OR (c = ?)")
	if strings.Contains(query, "?") {
		t.Fatalf("postgres binding must rewrite every placeholder: %q", query)
	}
	if query != "SELECT * FROM t WHERE a = $1 AND b = $2 OR (c = $3)" {
		t.Fatalf("unexpected binding: %q", query)
	}
	if bindPlaceholders(false, query) != query {
		t.Fatal("sqlite binding must be a no-op")
	}
	if statsTable(true, "usage_stats_daily") != "juhe_stats.usage_stats_daily" {
		t.Fatal("postgres stats tables must be schema-qualified")
	}
	if statsBusinessTable(true, "resource_authorizations") != "juhe_business.resource_authorizations" {
		t.Fatal("postgres business tables must be schema-qualified")
	}
}

func TestWindowResetSemantics(t *testing.T) {
	db := newTestDB(t, "reset")
	statsSchema(t, db)
	stats, err := NewStatsStore(db, false)
	if err != nil {
		t.Fatalf("NewStatsStore: %v", err)
	}
	location := time.UTC
	// A daily row for 2026-09-03 must not count at 2026-09-04 00:00 UTC.
	seedCost(t, db, "usage_stats_daily", []string{"system_account_id", "scope_type", "scope_id", "stat_date", "total_cost_usd"},
		[]any{"sys", "api_key", "ak", "2026-09-03", 100})

	before, err := stats.LoadCosts(context.Background(), CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: time.Date(2026, 9, 3, 23, 59, 59, 0, time.UTC)}, location)
	if err != nil {
		t.Fatalf("LoadCosts before reset: %v", err)
	}
	if before.Daily != 100 {
		t.Fatalf("daily before reset = %v, want 100", before.Daily)
	}
	after, err := stats.LoadCosts(context.Background(), CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)}, location)
	if err != nil {
		t.Fatalf("LoadCosts after reset: %v", err)
	}
	if after.Daily != 0 {
		t.Fatalf("daily after reset = %v, want 0", after.Daily)
	}

	// Timezone-aware reset: 2026-09-03 20:00 UTC is already 2026-09-04 in
	// Shanghai (+8), so the Shanghai daily key must be 0.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Shanghai: %v", err)
	}
	zoned, err := stats.LoadCosts(context.Background(), CostInput{SystemAccountID: "sys", ScopeType: "api_key", ScopeID: "ak", Now: time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC)}, shanghai)
	if err != nil {
		t.Fatalf("LoadCosts zoned: %v", err)
	}
	if zoned.Daily != 0 {
		t.Fatalf("zoned daily = %v, want 0 (Shanghai already next day)", zoned.Daily)
	}
}

func TestDBTimezoneSource(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, *fakeClock) {
		db := newTestDB(t, "tz")
		if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT, key TEXT, value_json TEXT)`); err != nil {
			t.Fatalf("create system_settings: %v", err)
		}
		return db, newFakeClock(time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC))
	}
	setSetting := func(t *testing.T, db *sql.DB, value string) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM system_settings`); err != nil {
			t.Fatalf("clear settings: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'usageStatsTimezone', ?)`, value); err != nil {
			t.Fatalf("insert setting: %v", err)
		}
	}

	t.Run("missing setting errors", func(t *testing.T) {
		db, clock := setup(t)
		source, err := NewDBTimezoneSource(db, false, clock.Now)
		if err != nil {
			t.Fatalf("NewDBTimezoneSource: %v", err)
		}
		_, err = source.StatsTimezone(context.Background())
		if err == nil || err.Error() != "系统设置缺少 usageStatsTimezone" {
			t.Fatalf("missing setting error = %v", err)
		}
	})

	t.Run("empty json errors", func(t *testing.T) {
		db, clock := setup(t)
		setSetting(t, db, "")
		source, _ := NewDBTimezoneSource(db, false, clock.Now)
		_, err := source.StatsTimezone(context.Background())
		if err == nil || err.Error() != "系统设置缺少 usageStatsTimezone" {
			t.Fatalf("empty setting error = %v", err)
		}
	})

	t.Run("invalid json errors", func(t *testing.T) {
		db, clock := setup(t)
		setSetting(t, db, "{not json")
		source, _ := NewDBTimezoneSource(db, false, clock.Now)
		_, err := source.StatsTimezone(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "系统设置 usageStatsTimezone 无效：") {
			t.Fatalf("invalid json error = %v", err)
		}
	})

	t.Run("unknown zone errors", func(t *testing.T) {
		db, clock := setup(t)
		setSetting(t, db, `"Mars/Olympus"`)
		source, _ := NewDBTimezoneSource(db, false, clock.Now)
		_, err := source.StatsTimezone(context.Background())
		if err == nil || err.Error() != "系统设置 usageStatsTimezone 无效：统计时区不存在：Mars/Olympus" {
			t.Fatalf("unknown zone error = %v", err)
		}
	})

	t.Run("caches success for 60s", func(t *testing.T) {
		db, clock := setup(t)
		setSetting(t, db, `"UTC"`)
		source, _ := NewDBTimezoneSource(db, false, clock.Now)
		ctx := context.Background()
		first, err := source.StatsTimezone(ctx)
		if err != nil {
			t.Fatalf("first load: %v", err)
		}
		// Change the stored value; the cached window must still serve it.
		setSetting(t, db, `"Asia/Shanghai"`)
		cached, err := source.StatsTimezone(ctx)
		if err != nil {
			t.Fatalf("cached load: %v", err)
		}
		if cached != first {
			t.Fatal("60s TTL window must serve the cached zone")
		}
		clock.Advance(61 * time.Second)
		refreshed, err := source.StatsTimezone(ctx)
		if err != nil {
			t.Fatalf("refreshed load: %v", err)
		}
		if refreshed.String() != "Asia/Shanghai" {
			t.Fatalf("refreshed zone = %v, want Asia/Shanghai", refreshed)
		}
	})
}

func TestStatKeysAcrossTimezones(t *testing.T) {
	tests := []struct {
		name      string
		zone      string
		utc       time.Time
		wantDate  string
		wantWeek  string
		wantMonth string
	}{
		{name: "utc thursday", zone: "UTC", utc: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), wantDate: "2026-09-04", wantWeek: "2026-08-31", wantMonth: "2026-09"},
		{name: "shanghai rollover", zone: "Asia/Shanghai", utc: time.Date(2026, 9, 4, 16, 0, 0, 0, time.UTC), wantDate: "2026-09-05", wantWeek: "2026-08-31", wantMonth: "2026-09"},
		{name: "new york dst march", zone: "America/New_York", utc: time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), wantDate: "2026-03-08", wantWeek: "2026-03-02", wantMonth: "2026-03"},
		{name: "sunday belongs to previous monday week", zone: "UTC", utc: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC), wantDate: "2026-09-06", wantWeek: "2026-08-31", wantMonth: "2026-09"},
		{name: "monday starts new week", zone: "UTC", utc: time.Date(2026, 9, 7, 0, 30, 0, 0, time.UTC), wantDate: "2026-09-07", wantWeek: "2026-09-07", wantMonth: "2026-09"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatalf("load zone %s: %v", tt.zone, err)
			}
			if got := dateKey(tt.utc, location); got != tt.wantDate {
				t.Fatalf("dateKey = %s, want %s", got, tt.wantDate)
			}
			if got := weekKey(tt.utc, location); got != tt.wantWeek {
				t.Fatalf("weekKey = %s, want %s", got, tt.wantWeek)
			}
			if got := monthKey(tt.utc, location); got != tt.wantMonth {
				t.Fatalf("monthKey = %s, want %s", got, tt.wantMonth)
			}
		})
	}
}

// silence unused helpers in builds without all tests.
var (
	_ = sync.Mutex{}
	_ = fmtAny
)
