// Read-model tests for the jobs system_settings source: stored-value wins,
// missing rows fall back to DEFAULT_SYSTEM_SETTINGS, the integer/bounds
// guards reproduce the Node error messages, the failed-read degradation
// warns, and the 60s window shields repeat reads. 夹具走 pgtestkit 隔离
// PG 测试库 + juhe_business.system_settings（生产 schema 前缀），Source
// 统一以 Mode: Postgres 生产模式装配。
//
// PG 模式失败语义（settings.go 包注释）：任何设置读失败（含表缺失）都
// 告警 background_job_settings_snapshot_refresh_failed 并回落默认值；
// SQLite 分支的"非缺表读错误上抛"与 missingOnce 去重属已退役方言语义，
// 不再有对应断言（原 TestSourceMissingTableWarnsOnceAndDegrades 与
// TestSourceOtherReadErrorsPropagate 随夹具迁移重钉/删除）。
package jobssettings

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
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

// newSettingsDB 打开 pgtestkit 隔离 PG 测试库并按生产 schema 前缀建
// juhe_business.system_settings（生产列形：text 列 + (system_account_id,
// key) 主键；指向 system_accounts 的外键属跨表管理面契约，非本读模型行为
// 面，夹具不复刻）。withTable=false 只建 schema 命名空间，模拟设置表未
// 初始化。
func newSettingsDB(t *testing.T, withTable bool) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_business`); err != nil {
			return err
		}
		if !withTable {
			return nil
		}
		_, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.system_settings (system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL, PRIMARY KEY (system_account_id, key))`)
		return err
	})
}

// insertSetting 以 PG upsert 写入设置行（sqlite 夹具的 INSERT OR REPLACE
// 改写为 ON CONFLICT DO UPDATE；updated_at 不被读模型消费，定值即可）。
func insertSetting(t *testing.T, db *sql.DB, key, valueJSON string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', $1, $2, '2026-09-04T00:00:00.000Z') ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`, key, valueJSON); err != nil {
		t.Fatal(err)
	}
}

func newTestSource(db *sql.DB, warn *warnCollector, now func() time.Time) *Source {
	return NewSource(Options{DB: db, Mode: Postgres, Warn: warn.warn, Now: now})
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

func TestSourceMissingTableDegradesToDefaultsWithWarn(t *testing.T) {
	db := newSettingsDB(t, false)
	warn := newWarnCollector()
	current := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	source := newTestSource(db, warn, func() time.Time { return current })
	// 表缺失：PG 模式按快照刷新失败降级默认值并告警。
	value, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
	if err != nil || value != 2000 {
		t.Fatalf("missing-table degrade: value=%d err=%v", value, err)
	}
	// 60s 窗口内的重复读取由快照承接，不触库、不新增告警。
	if value, err = source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err != nil || value != 2000 {
		t.Fatalf("cached degrade: value=%d err=%v", value, err)
	}
	// 窗口过期后重新读库：PG 模式每次刷新失败都重新告警（missingOnce 去重
	// 属 SQLite 分支语义，PG 路径与 Node 快照刷新失败语义一致）。
	current = current.Add(SettingsSnapshotTTL)
	if value, err = source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000); err != nil || value != 2000 {
		t.Fatalf("expired-window degrade: value=%d err=%v", value, err)
	}
	count, events := warn.snapshot()
	if count != 2 || len(events) != 2 {
		t.Fatalf("expected exactly two degrade warns: %d %v", count, events)
	}
	for _, event := range events {
		if event != "background_job_settings_snapshot_refresh_failed" {
			t.Fatalf("unexpected degrade events: %v", events)
		}
	}
}

func TestSourceCacheWindowShieldsReads(t *testing.T) {
	db := newSettingsDB(t, true)
	insertSetting(t, db, "statsAggregationBatchSize", "3500")
	current := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	warn := newWarnCollector()
	source := newTestSource(db, warn, func() time.Time { return current })
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
	// Past the window the PG mode degrades the failed refresh to the defaults
	// with a warn（SQLite 分支的"读错误上抛"语义不再适用）。
	current = current.Add(SettingsSnapshotTTL)
	value, err = source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
	if err != nil || value != 2000 {
		t.Fatalf("expired cache must fall back to default: value=%d err=%v", value, err)
	}
	if count, events := warn.snapshot(); count != 1 || len(events) != 1 || events[0] != "background_job_settings_snapshot_refresh_failed" {
		t.Fatalf("expired cache must warn refresh failure: %d %v", count, events)
	}
}
