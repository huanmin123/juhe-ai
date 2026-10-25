package schema

// M4a 媒体计量维度列测试（统计指标与分层聚合设计·媒体维度）：PG 侧 60 条
// media-stats-pg-columns ALTER 由 golden 计数与幂等守卫断言（pg_schema_test.go）
// 覆盖。SQLite PRAGMA 守卫臂随 SQLite 存储形态退役删除
// （PLAN-20261024T051925945Z B4）。
import (
	"strings"
	"testing"
)

// mediaStatsColumnTables 与生产 PG 守卫清单同集：生产守卫清单改坏（漏表/
// 多表）时本测试跟随失败。
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
