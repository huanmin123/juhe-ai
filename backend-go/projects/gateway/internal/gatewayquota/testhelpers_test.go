package gatewayquota

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB opens an isolated in-memory SQLite database per test.
func newTestDB(t *testing.T, label string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:gatewayquota-"+label+"-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode=MEMORY`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	return db
}

// statsSchema creates the five usage projections the quota reads.
func statsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE usage_stats_totals (system_account_id TEXT, scope_type TEXT, scope_id TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_daily (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_date TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_weekly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_week TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_stats_monthly (system_account_id TEXT, scope_type TEXT, scope_id TEXT, stat_month TEXT, total_cost_usd REAL)`,
		`CREATE TABLE usage_quota_hourly_windows (system_account_id TEXT, scope_type TEXT, scope_id TEXT, window_hours INTEGER, total_cost_usd REAL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create stats schema: %v", err)
		}
	}
}

// seedCost inserts one cost row.
func seedCost(t *testing.T, db *sql.DB, table string, columns []string, values []any) {
	t.Helper()
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := db.Exec(query, values...); err != nil {
		t.Fatalf("seed %s: %v", table, err)
	}
}

// fakeClock is a manual clock for deterministic TTL/cache behaviour.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// manualTimerQueue records scheduled timers so tests fire them explicitly.
type manualTimerQueue struct {
	mu     sync.Mutex
	timers []*manualTimer
}

type manualTimer struct {
	delay   time.Duration
	fn      func()
	stopped bool
}

func (q *manualTimerQueue) schedule(delay time.Duration, fn func()) (stop func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	timer := &manualTimer{delay: delay, fn: fn}
	q.timers = append(q.timers, timer)
	return func() { timer.stopped = true }
}

// fireAll runs every pending timer whose delay is <= elapsed, oldest first,
// and reports how many fired.
func (q *manualTimerQueue) fireAll() int {
	q.mu.Lock()
	pending := append([]*manualTimer(nil), q.timers...)
	q.mu.Unlock()
	fired := 0
	for _, timer := range pending {
		if !timer.stopped {
			timer.fn()
			fired++
		}
	}
	return fired
}

func (q *manualTimerQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.timers)
}

func fmtAny(v any) string { return fmt.Sprintf("%v", v) }
