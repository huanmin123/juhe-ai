package statsverify

// T93-6 / C2-H1 钉住测试：statsverify 按 usage_records 重算最近整小时窗口并与
// usage_stats_hourly 对比，发现人为制造的单边缺失（记录侧有、统计侧缺；或
// 统计侧有、记录侧缺）时差异不静默。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// t936ReconcileFixtureDDL 按生产列集覆盖对账路径消费的表：
// juhe_usage.usage_records（statsagg 聚合 SELECT 全列）、
// juhe_stats.usage_stats_hourly（含 success_cost_usd ALTER 列）、
// juhe_stats.stats_job_state（聚合游标）与 juhe_business.system_settings（时区）。
const t936ReconcileFixtureDDL = `
CREATE SCHEMA IF NOT EXISTS juhe_usage;
CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_usage.usage_records (
  id text NOT NULL,
  system_account_id text NOT NULL,
  trace_id text,
  traffic_source text,
  client_ip text,
  api_key_id text,
  group_id text,
  account_id text,
  endpoint text,
  provider_code text,
  provider_protocol_profile_id text,
  model text,
  status_code integer,
  success integer,
  failure_attribution text,
  first_token_ms integer,
  duration_ms integer,
  input_tokens bigint,
  output_tokens bigint,
  cache_read_tokens bigint,
  cache_read_cost_usd double precision,
  cache_write_tokens bigint,
  cache_write_1h_tokens bigint,
  cache_write_cost_usd double precision,
  thinking_tokens bigint,
  input_image_tokens integer,
  output_image_tokens integer,
  input_audio_tokens integer,
  output_audio_tokens integer,
  tts_input_chars integer,
  audio_input_seconds integer,
  output_video_seconds integer,
  cost_usd double precision,
  error_code text,
  error_message text,
  account_owner_system_account_id text,
  group_owner_system_account_id text,
  account_access_type text,
  group_access_type text,
  account_authorization_id text,
  account_authorization_source_type text,
  account_authorization_source_team_id text,
  group_authorization_id text,
  group_authorization_source_type text,
  group_authorization_source_team_id text,
  created_at text NOT NULL,
  PRIMARY KEY (created_at, id)
);
CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_hourly (
  system_account_id text NOT NULL,
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  stat_hour text NOT NULL,
  request_count bigint NOT NULL DEFAULT 0,
  success_count bigint NOT NULL DEFAULT 0,
  error_count bigint NOT NULL DEFAULT 0,
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_read_cost_usd double precision NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cache_write_1h_tokens bigint NOT NULL DEFAULT 0,
  cache_write_cost_usd double precision NOT NULL DEFAULT 0,
  thinking_tokens bigint NOT NULL DEFAULT 0,
  input_image_tokens bigint NOT NULL DEFAULT 0,
  output_image_tokens bigint NOT NULL DEFAULT 0,
  total_cost_usd double precision NOT NULL DEFAULT 0,
  success_cost_usd double precision NOT NULL DEFAULT 0,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
);
CREATE TABLE IF NOT EXISTS juhe_stats.stats_job_state (
  scope_type text NOT NULL,
  scope_id text NOT NULL DEFAULT '',
  job_name text NOT NULL,
  cursor_created_at text,
  cursor_id text,
  last_success_at text,
  last_error_message text,
  lag_seconds integer,
  updated_at text NOT NULL,
  PRIMARY KEY (scope_type, scope_id, job_name)
);
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key)
);
`

func t936ReconcileStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, t936ReconcileFixtureDDL)
		return err
	})
	store, err := OpenStore(StoreConfig{
		PostgresURL:          testDB.DSN,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("隔离 PG 测试库 OpenStore 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
		VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"', $1)`, NowIso(time.Now())); err != nil {
		t.Fatalf("写入统计时区失败: %v", err)
	}
	return store, store.db
}

func t936InsertAggregationCursor(t *testing.T, db *sql.DB, cursorCreatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_stats.stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, lag_seconds, updated_at)
		VALUES ('global', '', 'usage_stats_aggregation', $1, 'rec-zz', $1, 0, $1)`, cursorCreatedAt); err != nil {
		t.Fatalf("写入聚合游标失败: %v", err)
	}
}

func t936InsertRecord(t *testing.T, db *sql.DB, id, systemAccountID string, success int, inputTokens int64, cost float64, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_usage.usage_records
		(id, system_account_id, trace_id, traffic_source, success, input_tokens, output_tokens, cost_usd, created_at)
		VALUES ($1, $2, $3, 'gateway', $4, $5, 0, $6, $7)`,
		id, systemAccountID, "trace-"+id, success, inputTokens, cost, createdAt); err != nil {
		t.Fatalf("写入 usage_records 失败: %v", err)
	}
}

func t936InsertHourly(t *testing.T, db *sql.DB, systemAccountID, scopeID, statHour string, request, success, errorCount, inputTokens int64, totalCost, successCost float64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_stats.usage_stats_hourly
		(system_account_id, scope_type, scope_id, stat_hour, request_count, success_count, error_count,
		 input_tokens, total_cost_usd, success_cost_usd, updated_at)
		VALUES ($1, 'system_account', $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		systemAccountID, scopeID, statHour, request, success, errorCount, inputTokens, totalCost, successCost, NowIso(time.Now())); err != nil {
		t.Fatalf("写入 usage_stats_hourly 失败: %v", err)
	}
}

// t936SeedBalancedWindow 写入 2026-09-07 09:00-10:00 UTC 的三条记录与对应的
// caller/global 统计行（alice 成功+失败、bob 成功）。
func t936SeedBalancedWindow(t *testing.T, db *sql.DB) {
	t.Helper()
	t936InsertRecord(t, db, "rec-alice-success", "alice", 1, 10, 0.5, "2026-09-07T09:10:00.000Z")
	t936InsertRecord(t, db, "rec-alice-error", "alice", 0, 3, 0.25, "2026-09-07T09:20:00.000Z")
	t936InsertRecord(t, db, "rec-bob-success", "bob", 1, 7, 0.1, "2026-09-07T09:30:00.000Z")
	t936InsertHourly(t, db, "alice", "alice", "2026-09-07T09", 2, 1, 1, 13, 0.75, 0.5)
	t936InsertHourly(t, db, "bob", "bob", "2026-09-07T09", 1, 1, 0, 7, 0.1, 0.1)
	t936InsertHourly(t, db, statsagg.GlobalStatsSystemAccountID, statsagg.GlobalStatsScopeID, "2026-09-07T09", 3, 2, 1, 20, 0.85, 0.6)
	t936InsertAggregationCursor(t, db, "2026-09-07T10:05:00.000Z")
}

func TestT936ReconcileMatchesBalancedWindow(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936SeedBalancedWindow(t, db)
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now: time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if result.Skipped {
		t.Fatalf("有游标时不应跳过: %s", result.SkipReason)
	}
	if result.IssueCount != 0 {
		t.Fatalf("口径一致窗口不应有差异: %+v", result.Issues)
	}
	if result.CheckedRecords != 3 || result.CheckedBuckets != 3 || result.CheckedHours != 1 {
		t.Fatalf("对账覆盖不符: records=%d buckets=%d hours=%d", result.CheckedRecords, result.CheckedBuckets, result.CheckedHours)
	}
	if result.WindowStartHour != "2026-09-07T09" || result.WindowEndHour != "2026-09-07T10" {
		t.Fatalf("窗口=%s~%s", result.WindowStartHour, result.WindowEndHour)
	}
}

// 单边缺失方向一：记录侧有、统计侧缺（人为删除 global 作用域小时行）。
func TestT936ReconcileDetectsMissingStatsSide(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936SeedBalancedWindow(t, db)
	if _, err := db.Exec(`DELETE FROM juhe_stats.usage_stats_hourly
		WHERE system_account_id = $1 AND scope_type = 'system_account' AND stat_hour = '2026-09-07T09'`, statsagg.GlobalStatsSystemAccountID); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now: time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if result.IssueCount == 0 {
		t.Fatal("统计侧缺失必须被对账发现（不得静默）")
	}
	var requestIssue *UsageStatsReconciliationIssue
	for index := range result.Issues {
		issue := result.Issues[index]
		if issue.SystemAccountID == statsagg.GlobalStatsSystemAccountID && issue.Metric == "request_count" {
			requestIssue = &result.Issues[index]
			break
		}
	}
	if requestIssue == nil {
		t.Fatalf("缺少 global request_count 差异: %+v", result.Issues)
	}
	if requestIssue.RecordsValue != 3 || requestIssue.StatsValue != 0 {
		t.Fatalf("global request_count 差异值不符: %+v", requestIssue)
	}
}

// 单边缺失方向二：统计侧有、记录侧缺（人为写入一条无记录支撑的小时行）。
func TestT936ReconcileDetectsMissingRecordsSide(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936SeedBalancedWindow(t, db)
	t936InsertHourly(t, db, "carol", "carol", "2026-09-07T09", 1, 1, 0, 5, 0.05, 0.05)
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now: time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	var carolIssue *UsageStatsReconciliationIssue
	for index := range result.Issues {
		if result.Issues[index].SystemAccountID == "carol" && result.Issues[index].Metric == "request_count" {
			carolIssue = &result.Issues[index]
			break
		}
	}
	if carolIssue == nil {
		t.Fatalf("统计侧多余行必须被对账发现: %+v", result.Issues)
	}
	if carolIssue.RecordsValue != 0 || carolIssue.StatsValue != 1 {
		t.Fatalf("carol request_count 差异值不符: %+v", carolIssue)
	}
}

// 游标所在小时（可能只聚合了一部分）不参与比较，避免未完成小时的假差异。
func TestT936ReconcileExcludesCursorHour(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936SeedBalancedWindow(t, db)
	// 游标小时 10 内尚未聚合的记录：不进入对账窗口，也不产生差异。
	t936InsertRecord(t, db, "rec-late", "alice", 1, 100, 9.9, "2026-09-07T10:20:00.000Z")
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now: time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if result.CheckedRecords != 3 || result.IssueCount != 0 {
		t.Fatalf("游标小时不应参与对账: records=%d issues=%+v", result.CheckedRecords, result.Issues)
	}
	if result.WindowEndHour != "2026-09-07T10" {
		t.Fatalf("窗口上界应为游标小时起点: %s", result.WindowEndHour)
	}
}

// 可配置窗口：Window=2h 覆盖两个整小时。
func TestT936ReconcileWindowIsConfigurable(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936SeedBalancedWindow(t, db)
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now:    time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
		Window: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("对账失败: %v", err)
	}
	if result.WindowStartHour != "2026-09-07T08" || result.CheckedHours != 2 {
		t.Fatalf("2h 窗口不符: start=%s hours=%d", result.WindowStartHour, result.CheckedHours)
	}
	if result.IssueCount != 0 {
		t.Fatalf("空小时两侧都为 0，不应有差异: %+v", result.Issues)
	}
}

// 无聚合游标（首次部署）时显式跳过并携带原因，不猜测聚合边界。
func TestT936ReconcileSkipsWithoutAggregationCursor(t *testing.T) {
	store, db := t936ReconcileStore(t)
	t936InsertRecord(t, db, "rec-orphan", "alice", 1, 1, 0.01, "2026-09-07T09:10:00.000Z")
	result, err := store.ReconcileUsageStatsHourly(context.Background(), UsageStatsReconciliationOptions{
		Now: time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("缺游标不应报错: %v", err)
	}
	if !result.Skipped || result.SkipReason == "" {
		t.Fatalf("缺游标必须显式跳过并给出原因: %+v", result)
	}
	// Run 入口同样不报错（只记录 Info 跳过）。
	if _, err := store.RunUsageStatsReconciliation(context.Background(), time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC), DefaultUsageStatsReconciliationWindow, nil, nil); err != nil {
		t.Fatalf("RunUsageStatsReconciliation 跳过路径不应报错: %v", err)
	}
}
