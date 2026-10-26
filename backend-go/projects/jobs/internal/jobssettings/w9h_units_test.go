package jobssettings

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w9hSettingsDB 打开 pgtestkit 隔离 PG 测试库并只建 juhe_business schema
// 命名空间；system_settings 表由用例按需自建（缺表臂先执行，再建表覆盖
// populated 臂）。
func w9hSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_business`)
		return err
	})
}

func TestW9HDefaultNumberArms(t *testing.T) {
	// 未知键没有数值默认。
	if _, ok := DefaultNumber("w9h-unknown-key", 1, 10); ok {
		t.Fatal("unknown key must report ok=false")
	}
	// 已知默认键被夹进范围。
	value, ok := DefaultNumber("statsAggregationIntervalSeconds", 999, 100000)
	if !ok || value != 999 {
		t.Fatalf("clamped low value=%d ok=%v", value, ok)
	}
	value, ok = DefaultNumber("statsAggregationIntervalSeconds", 1, 5)
	if !ok || value != 5 {
		t.Fatalf("clamped high value=%d ok=%v", value, ok)
	}
	value, ok = DefaultNumber("statsAggregationIntervalSeconds", 1, 100000)
	if !ok || value <= 5 {
		t.Fatalf("in-range value=%d ok=%v", value, ok)
	}
}

func TestW9HSourceArms(t *testing.T) {
	ctx := context.Background()
	warnings := []string{}
	db := w9hSettingsDB(t)

	// system_settings 表缺失：PG 模式按快照刷新失败降级默认值并告警。
	source := NewSource(Options{DB: db, Mode: Postgres, Warn: func(event string, _ map[string]any, message string) {
		warnings = append(warnings, event+"|"+message)
	}, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }})
	value, err := source.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000)
	if err != nil || value <= 0 {
		t.Fatalf("missing table value=%d err=%v", value, err)
	}
	if len(warnings) != 1 || warnings[0] != "background_job_settings_snapshot_refresh_failed|后台任务系统设置快照刷新失败，将临时使用默认设置" {
		t.Fatalf("warnings=%v", warnings)
	}
	// 时钟冻结下第二次读取由 60s 快照窗口承接，不触库、不新增告警
	// （missingOnce 去重属 SQLite 分支语义，PG 路径的去重来自快照窗口）。
	if _, err := source.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("snapshot window must dedupe: %v", warnings)
	}

	// nil Warn 也不得崩（缺表降级分支）。
	quiet := NewSource(Options{DB: db, Mode: Postgres})
	if _, err := quiet.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000); err != nil {
		t.Fatal(err)
	}

	// 表存在：行缺失回落默认；非法 JSON 报错。
	if _, err := db.Exec(`CREATE TABLE juhe_business.system_settings (system_account_id text NOT NULL, key text NOT NULL, value_json text NOT NULL, updated_at text NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	// 每次读取推进一分钟，避免 60s 快照窗口吞掉后续行变更。
	tick := 0
	clock := func() time.Time { tick++; return time.Date(2026, 1, 1, 0, tick, 0, 0, time.UTC) }
	populated := NewSource(Options{DB: db, Mode: Postgres, Warn: func(event string, fields map[string]any, message string) {
		t.Fatalf("populated table must not warn: %s %v %s", event, fields, message)
	}, Now: clock})
	if value, err := populated.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000); err != nil || value <= 0 {
		t.Fatalf("default fallback value=%d err=%v", value, err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'statsAggregationIntervalSeconds', 'not-json', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := populated.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000); err == nil {
		t.Fatal("invalid json must fail")
	}
	// 合法 JSON 数值走快照缓存。
	if _, err := db.Exec(`UPDATE juhe_business.system_settings SET value_json = '42' WHERE key = 'statsAggregationIntervalSeconds'`); err != nil {
		t.Fatal(err)
	}
	if value, err := populated.Number(ctx, "statsAggregationIntervalSeconds", 1, 100000); err != nil || value != 42 {
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
