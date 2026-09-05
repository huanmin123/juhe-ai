// Read-model tests for the jobs system_settings source: stored-value wins,
// missing rows fall back to DEFAULT_SYSTEM_SETTINGS, the integer/bounds
// guards reproduce the Node error messages, the missing-table degradation
// warns once, and the 60s window shields repeat reads.
package jobssettings

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type warnCollector struct {
	mutex  sync.Mutex
	events []string
	count  int
}

func newWarnCollector() *warnCollector {
	return &warnCollector{}
}

func (c *warnCollector) warn(event string, _ map[string]any, _ string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.events = append(c.events, event)
	c.count++
}

func (c *warnCollector) snapshot() (int, []string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.count, append([]string(nil), c.events...)
}

func newSettingsDB(t *testing.T, withTable bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:jobssettings-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if withTable {
		if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertSetting(t *testing.T, db *sql.DB, key, valueJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', ?, ?, '2026-09-04T00:00:00.000Z')`, key, valueJSON); err != nil {
		t.Fatal(err)
	}
}

func newTestSource(db *sql.DB, warn *warnCollector, now func() time.Time) *Source {
	return NewSource(Options{DB: db, Mode: SQLite, Warn: warn.warn, Now: now})
}

func TestSourceStoredValueWins(t *testing.T) {
	db := newSettingsDB(t, true)
	insertSetting(t, db, "statsAggregationBatchSize", "3500")
	source := newTestSource(db, newWarnCollector(), time.Now)
	value, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
	if err != nil {
		t.Fatalf("stored value read: %v", err)
	}
	if value != 3500 {
		t.Fatalf("stored value mismatch: %d", value)
	}
}

func TestSourceMissingRowFallsBackToDefault(t *testing.T) {
	db := newSettingsDB(t, true)
	source := newTestSource(db, newWarnCollector(), time.Now)
	value, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
	if err != nil {
		t.Fatalf("default fallback: %v", err)
	}
	if value != 2000 {
		t.Fatalf("DEFAULT_SYSTEM_SETTINGS fallback mismatch: %d", value)
	}
	// The default travels through the bounds guard too.
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 5000, 10000); err == nil {
		t.Fatalf("default must respect the bounds guard")
	}
}

func TestSourceIntegerAndBoundsErrors(t *testing.T) {
	db := newSettingsDB(t, true)
	insertSetting(t, db, "statsAggregationBatchSize", `"not-a-number"`)
	source := newTestSource(db, newWarnCollector(), time.Now)
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err == nil || err.Error() != "系统设置 statsAggregationBatchSize 必须是整数" {
		t.Fatalf("non-integer error mismatch: %v", err)
	}
	// A fractional JSON number is not an integer.
	insertSetting(t, db, "statsAggregationMaxBatchesPerRun", "1.5")
	if _, err := source.Number(context.Background(), "statsAggregationMaxBatchesPerRun", 1, 100); err == nil || err.Error() != "系统设置 statsAggregationMaxBatchesPerRun 必须是整数" {
		t.Fatalf("fractional error mismatch: %v", err)
	}
	insertSetting(t, db, "statsAggregationBatchSize", "99999")
	// A fresh source stands in for the next job round (Node's settingsNumber
	// failure already fails the current task; the value itself is cached like
	// the Node settings cache).
	source = newTestSource(db, newWarnCollector(), time.Now)
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 5000, 10000); err == nil || err.Error() != "系统设置 statsAggregationBatchSize 必须在 5000 到 10000 之间" {
		t.Fatalf("bounds error mismatch: %v", err)
	}
}

func TestSourceMissingTableWarnsOnceAndDegrades(t *testing.T) {
	db := newSettingsDB(t, false)
	warn := newWarnCollector()
	source := newTestSource(db, warn, time.Now)
	for index := 0; index < 2; index++ {
		value, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
		if err != nil {
			t.Fatalf("missing-table degrade %d: %v", index, err)
		}
		if value != 2000 {
			t.Fatalf("missing-table fallback mismatch: %d", value)
		}
	}
	count, events := warn.snapshot()
	if count != 1 || len(events) != 1 || events[0] != "background_job_settings_table_missing_default" {
		t.Fatalf("missing-table must warn exactly once: %d %v", count, events)
	}
}

func TestSourceOtherReadErrorsPropagate(t *testing.T) {
	db := newSettingsDB(t, true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	source := newTestSource(db, newWarnCollector(), time.Now)
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err == nil {
		t.Fatalf("non-missing-table errors must propagate")
	}
}

func TestSourceCacheWindowShieldsReads(t *testing.T) {
	db := newSettingsDB(t, true)
	insertSetting(t, db, "statsAggregationBatchSize", "3500")
	current := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	source := newTestSource(db, newWarnCollector(), func() time.Time { return current })
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// Close the DB: the 60s window keeps serving the cached value.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	current = current.Add(30 * time.Second)
	value, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
	if err != nil || value != 3500 {
		t.Fatalf("cached read mismatch: %d %v", value, err)
	}
	// Past the window the closed DB surfaces the read error.
	current = current.Add(SettingsSnapshotTTL)
	if _, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err == nil {
		t.Fatalf("expired cache must re-read")
	}
}
