package schema

// M4a 媒体计量维度列测试（统计指标与分层聚合设计·媒体维度）：usage_stats
// 六层、usage_model 五层与 usage_scope_range_windows 基线即含五列；legacy
// 旧表经 ensureSQLiteStatsMediaColumns 的 PRAGMA 守卫 ALTER 原位补列；重复
// ensure 幂等。PG 侧 60 条 media-stats-pg-columns ALTER 由 golden 计数与
// 幂等守卫断言（pg_schema_test.go）覆盖。
import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// mediaStatsColumnTables 与 sqliteStatsMediaColumnTables 同集：生产守卫
// 清单改坏（漏表/多表）时本测试跟随失败。
var mediaStatsColumnTables = []string{
	"usage_stats_totals",
	"usage_stats_minute",
	"usage_stats_hourly",
	"usage_stats_daily",
	"usage_stats_weekly",
	"usage_stats_monthly",
	"usage_model_minute",
	"usage_model_hourly",
	"usage_model_daily",
	"usage_model_weekly",
	"usage_model_monthly",
	"usage_scope_range_windows",
}

var mediaStatsColumns = []string{
	"input_audio_tokens",
	"output_audio_tokens",
	"tts_input_chars",
	"audio_input_seconds",
	"output_video_seconds",
}

func TestMediaStatsColumnsFreshDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", "file:media-stats-fresh?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	// 重复 ensure 幂等：新库两轮均成功。
	for i := 0; i < 2; i++ {
		if _, err := EnsureAllSQLite(context.Background(), db); err != nil {
			t.Fatalf("EnsureAllSQLite (round %d): %v", i+1, err)
		}
	}
	for _, table := range mediaStatsColumnTables {
		for _, column := range mediaStatsColumns {
			if !sqliteTableHasColumn(t, db, table, column) {
				t.Fatalf("%s missing media column %s", table, column)
			}
		}
	}
}

func TestMediaStatsColumnsLegacyTableGuardedAlter(t *testing.T) {
	db, err := sql.Open("sqlite", "file:media-stats-legacy?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	// legacy 形态：按加列前的真实旧形态建 usage_stats_totals（含
	// success_cost_usd，缺五个媒体列；其余表缺席，缺表路径由
	// ensureSQLiteTableColumn 的 tableExists 分支跳过）。
	if _, err := db.ExecContext(ctx, `CREATE TABLE usage_stats_totals (
		system_account_id TEXT NOT NULL,
		scope_type TEXT NOT NULL,
		scope_id TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		success_count INTEGER NOT NULL DEFAULT 0,
		error_count INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0,
		success_cost_usd REAL NOT NULL DEFAULT 0,
		last_used_at TEXT,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (system_account_id, scope_type, scope_id)
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, request_count, total_cost_usd, updated_at)
		VALUES ('sa_1', 'api_key', 'key_1', 3, 0.5, '2026-10-04T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// 全量 ensure：stats 基线 DDL 因 IF NOT EXISTS 不动 legacy 表，媒体守卫
	// 走 PRAGMA 检测 + ALTER ADD COLUMN；重复执行第二轮必须无操作幂等。
	for i := 0; i < 2; i++ {
		if _, err := EnsureAllSQLite(ctx, db); err != nil {
			t.Fatalf("EnsureAllSQLite legacy (round %d): %v", i+1, err)
		}
	}
	for _, column := range mediaStatsColumns {
		if !sqliteTableHasColumn(t, db, "usage_stats_totals", column) {
			t.Fatalf("legacy usage_stats_totals missing media column %s", column)
		}
	}
	// 既有行默认 0 且原值不丢（无回填语义，从新记录起累计）。
	var requestCount int
	var audioTokens float64
	if err := db.QueryRowContext(ctx,
		`SELECT request_count, input_audio_tokens FROM usage_stats_totals WHERE scope_id = 'key_1'`).
		Scan(&requestCount, &audioTokens); err != nil {
		t.Fatalf("scan legacy row: %v", err)
	}
	if requestCount != 3 || audioTokens != 0 {
		t.Fatalf("legacy row mutated: request_count=%d input_audio_tokens=%v", requestCount, audioTokens)
	}
}

func TestMediaStatsPGColumnsCoverAllGuardTables(t *testing.T) {
	// PG 基线不带媒体列（纯 ALTER 模式）：60 条 media-stats-pg-columns ALTER
	// 必须恰好覆盖 12 张守卫表 × 5 列，防止清单漂移。
	alterText := ""
	for _, statement := range postgresSchemaStats {
		if statement.Source != "media-stats-pg-columns" {
			continue
		}
		alterText += statement.SQL + "\n"
	}
	for _, table := range mediaStatsColumnTables {
		for _, column := range mediaStatsColumns {
			needle := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + column
			if !strings.Contains(alterText, needle) {
				t.Fatalf("missing PG media ALTER: %s", needle)
			}
		}
	}
	mediaAlters := 0
	for _, statement := range postgresSchemaStats {
		if statement.Source == "media-stats-pg-columns" {
			mediaAlters++
		}
	}
	if mediaAlters != len(mediaStatsColumnTables)*len(mediaStatsColumns) {
		t.Fatalf("media ALTER count = %d, want %d", mediaAlters, len(mediaStatsColumnTables)*len(mediaStatsColumns))
	}
}
