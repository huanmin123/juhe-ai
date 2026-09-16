package jobssettings

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func w9hSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "settings.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestW9HDefaultNumberArms(t *testing.T) {
	// 未知键没有数值默认。
	if _, ok := DefaultNumber("w9h-unknown-key", 1, 10); ok {
		t.Fatal("unknown key must report ok=false")
	}
	// 已知默认键被夹进范围。
	value, ok := DefaultNumber("systemMetricsSampleIntervalSeconds", 999, 100000)
	if !ok || value != 999 {
		t.Fatalf("clamped low value=%d ok=%v", value, ok)
	}
	value, ok = DefaultNumber("systemMetricsSampleIntervalSeconds", 1, 5)
	if !ok || value != 5 {
		t.Fatalf("clamped high value=%d ok=%v", value, ok)
	}
	value, ok = DefaultNumber("systemMetricsSampleIntervalSeconds", 1, 100000)
	if !ok || value <= 5 {
		t.Fatalf("in-range value=%d ok=%v", value, ok)
	}
}

func TestW9HSourceArms(t *testing.T) {
	ctx := context.Background()
	warnings := []string{}
	db := w9hSettingsDB(t)

	// system_settings 表缺失：sqlite 模式一次 warn 后回落默认值。
	source := NewSource(Options{DB: db, Mode: SQLite, Warn: func(event string, _ map[string]any, message string) {
		warnings = append(warnings, event+"|"+message)
	}, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }})
	value, err := source.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000)
	if err != nil || value <= 0 {
		t.Fatalf("missing table value=%d err=%v", value, err)
	}
	if len(warnings) != 1 || warnings[0] != "background_job_settings_table_missing_default|后台任务启动时系统设置表尚未初始化，将临时使用默认设置" {
		t.Fatalf("warnings=%v", warnings)
	}
	// 第二次读取不再重复告警（missingOnce）。
	if _, err := source.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("missingOnce must dedupe: %v", warnings)
	}

	// nil Warn 也不得崩（sqlite 缺表分支）。
	quiet := NewSource(Options{DB: db, Mode: SQLite})
	if _, err := quiet.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000); err != nil {
		t.Fatal(err)
	}

	// 表存在：行缺失回落默认；非法 JSON 报错。
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	// 每次读取推进一分钟，避免 60s 快照窗口吞掉后续行变更。
	tick := 0
	clock := func() time.Time { tick++; return time.Date(2026, 1, 1, 0, tick, 0, 0, time.UTC) }
	populated := NewSource(Options{DB: db, Mode: SQLite, Warn: func(event string, fields map[string]any, message string) {
		t.Fatalf("populated table must not warn: %s %v %s", event, fields, message)
	}, Now: clock})
	if value, err := populated.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000); err != nil || value <= 0 {
		t.Fatalf("default fallback value=%d err=%v", value, err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json) VALUES ('sys_admin', 'systemMetricsSampleIntervalSeconds', 'not-json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := populated.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000); err == nil {
		t.Fatal("invalid json must fail")
	}
	// 合法 JSON 数值走快照缓存。
	if _, err := db.Exec(`UPDATE system_settings SET value_json = '42' WHERE key = 'systemMetricsSampleIntervalSeconds'`); err != nil {
		t.Fatal(err)
	}
	if value, err := populated.Number(ctx, "systemMetricsSampleIntervalSeconds", 1, 100000); err != nil || value != 42 {
		t.Fatalf("stored value=%d err=%v", value, err)
	}
}

func TestW9HNewSourceDefaults(t *testing.T) {
	// nil Now 与非正 TTL 使用默认值。
	source := NewSource(Options{})
	if source == nil {
		t.Fatal("nil source")
	}
}
