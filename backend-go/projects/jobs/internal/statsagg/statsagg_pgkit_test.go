package statsagg

// statsagg_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z 测试迁移切片）：
//   - newPGTestEnv 经 pgtestkit 隔离 PG 测试库装配 Dialect{}
//     的 testEnv；DDL 对照 maintenance 生产 PG 权威 schema 逐表复刻
//     （juhe_stats：pg_schema_stats.go，含 success_cost_usd 与媒体计量列的
//     ALTER 追加；juhe_usage.usage_records：pg_schema_usage.go 的聚合 46 列
//     形状；juhe_business 最小授权/绑定表）。maintenance 属独立模块且
//     internal 包不可跨模块引用，故无法走其建表路径（cleanuprepo /
//     circuitstore 先例同风格）。
//   - 与生产 DDL 的已知偏差（均为测试最小形状，circuitstore/cleanuprepo
//     先例同风格）：usage_records 不复刻 PARTITION BY RANGE 分区（PK 保持
//     生产的 (created_at, id)）；juhe_business 两表只保留授权链查找消费的
//     列，FK/其余 NOT NULL 不复刻；usage_stats 表族的
//     account_list_availability_* 触发器族不复刻（依赖 juhe_business 函数，
//     与聚合/窗口刷新写入语义无关，共享 cover 库臂由 w10c_pg_gate 覆盖）。
//   - 组A 迁移面之外的保留项（本切片不动）：组B（SQLite-only 生产臂测试：
//     配额全量重建、SQLite standalone 镜像链、单写者并发竞争等，随生产
//     sqlite 臂删除批次退役，各文件注释披露）；scripted 假驱动（w13g5 注入
//     族，包装 modernc.org/sqlite 的错误注入机制不动）；env-gated 共享
//     cover 库门禁（w10c_pg_gate / w14j / authorization_write_pg，按 PLAN
//     §4.2 与 taskruns w12b_pg_gate 先例保持现状，同批统一裁决）。
//   - 测试体内手写 SQL 按 PG 方言书写（juhe_ 前缀 schema 限定 + $n 占位
//     符）；双模式共享的测试 helper（seedUsageRecord / quota 域 helper）
//     经生产同款 Dialect.bind / StatsTable / BusinessTable 生成 SQL，两层
//     互不干扰。连接串内容不进入任何日志或断言。

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// pgPlaceholders 生成 $1..$n 序号占位符（测试体内多行 VALUES 等手写 SQL 用；
// 生产路径的 ? → $n 改写统一走 Dialect.bind）。
func pgPlaceholders(count int) string {
	result := ""
	for index := 1; index <= count; index++ {
		if index > 1 {
			result += ", "
		}
		result += "$" + strconv.Itoa(index)
	}
	return result
}

// kitBusinessBindingDDL 是配额增量编排消费的 juhe_business 绑定表（生产
// pg_schema_business_tables.go 形状，CHECK/UNIQUE 保留）；openKitStatsOnly
// 装配的测试在需要时于测试体内补建（失败可见性臂先缺表后补建）。
const kitBusinessBindingDDL = `CREATE TABLE IF NOT EXISTS juhe_business.request_quota_hourly_window_scope_bindings (
      system_account_id text NOT NULL,
      scope_type text NOT NULL,
      scope_id text NOT NULL,
      source_type text NOT NULL,
      source_id text NOT NULL,
      window_hours integer NOT NULL,
      created_at text NOT NULL,
      updated_at text NOT NULL,
      CHECK (window_hours BETWEEN 1 AND 720),
      CHECK (scope_type IN ('api_key', 'account_authorization', 'group_authorization', 'account_authorization_team', 'group_authorization_team')),
      CHECK (source_type IN ('api_key', 'resource_authorization_grant')),
      PRIMARY KEY (system_account_id, scope_type, scope_id),
      UNIQUE (source_type, source_id, system_account_id, scope_type, scope_id)
    )`

// kitStatsDDL 是 juhe_stats schema 的生产对齐 DDL：CREATE TABLE 逐表对照
// pg_schema_stats.go（含 usage_scope_range_windows 的 GENERATED window_key
// 列），随后按生产顺序追加 success_cost_usd（success-cost-usd-pg-columns）
// 与媒体计量列（media-stats-pg-columns）的 ALTER 语句。
var kitStatsDDL = kitStatsStatements()

// kitStatsStatements 组装 kitStatsDDL（表 CREATE + success_cost ALTER 字面量，
// 媒体计量 ALTER 由 kitMediaColumnDDL 展开追加）。
func kitStatsStatements() []string {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS juhe_stats`,
		`CREATE SCHEMA IF NOT EXISTS juhe_usage`,
		`CREATE SCHEMA IF NOT EXISTS juhe_business`,
		// ---- usage 记录源表（pg_schema_usage.go usage-records 组，聚合 46 列形状；
		// 生产为 PARTITION BY RANGE (created_at) 分区表，测试取非分区最小形状，
		// PK 保持生产的 (created_at, id)）----
		`CREATE TABLE IF NOT EXISTS juhe_usage.usage_records (
      id text NOT NULL,
      system_account_id text NOT NULL,
      trace_id text NOT NULL,
      traffic_source text NOT NULL,
      client_ip text,
      api_key_id text,
      group_id text,
      account_id text,
      endpoint text,
      provider_code text,
      provider_protocol_profile_id text,
      model text,
      status_code integer,
      success integer NOT NULL DEFAULT 0,
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
      input_image_tokens bigint,
      output_image_tokens bigint,
      input_audio_tokens integer,
      output_audio_tokens integer,
      tts_input_chars bigint NOT NULL DEFAULT 0,
      audio_input_seconds double precision NOT NULL DEFAULT 0,
      output_video_seconds double precision NOT NULL DEFAULT 0,
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
    )`,
		// ---- usage_stats 六层（pg_schema_stats.go 基础列；success_cost_usd 与
		// 媒体计量列由下方 ALTER 追加，与生产执行序一致）----
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_totals (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        )`,
		kitStatsBucketDDL("usage_stats_minute", "stat_minute"),
		kitStatsBucketDDL("usage_stats_hourly", "stat_hour"),
		kitStatsBucketDDL("usage_stats_daily", "stat_date"),
		kitStatsBucketDDL("usage_stats_weekly", "stat_week"),
		kitStatsBucketDDL("usage_stats_monthly", "stat_month"),
		// ---- 授权日摘要（授权明细读端的直读数据源；range 窗口表族已停用不入 kit）----
		`CREATE TABLE IF NOT EXISTS juhe_stats.authorization_team_usage_summary_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          row_count bigint NOT NULL DEFAULT 0,
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.authorization_user_usage_summary_daily (
          system_account_id text NOT NULL,
          stat_date text NOT NULL,
          team_filter_id text NOT NULL DEFAULT '',
          grantee_filter_system_account_id text NOT NULL DEFAULT '',
          resource_filter_type text NOT NULL DEFAULT 'all',
          resource_filter_id text NOT NULL DEFAULT '',
          row_count bigint NOT NULL DEFAULT 0,
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id)
        )`,
		// ---- usage_model 五层（媒体计量列由 ALTER 追加）----
		kitStatsModelDDL("usage_model_minute", "stat_minute"),
		kitStatsModelDDL("usage_model_hourly", "stat_hour"),
		kitStatsModelDDL("usage_model_daily", "stat_date"),
		kitStatsModelDDL("usage_model_weekly", "stat_week"),
		kitStatsModelDDL("usage_model_monthly", "stat_month"),
		// ---- usage_error 五层 ----
		kitStatsErrorDDL("usage_error_minute", "stat_minute"),
		kitStatsErrorDDL("usage_error_hourly", "stat_hour"),
		kitStatsErrorDDL("usage_error_daily", "stat_date"),
		kitStatsErrorDDL("usage_error_weekly", "stat_week"),
		kitStatsErrorDDL("usage_error_monthly", "stat_month"),
		// ---- usage_latency 五层 ----
		kitStatsLatencyDDL("usage_latency_minute", "stat_minute"),
		kitStatsLatencyDDL("usage_latency_hourly", "stat_hour"),
		kitStatsLatencyDDL("usage_latency_daily", "stat_date"),
		kitStatsLatencyDDL("usage_latency_weekly", "stat_week"),
		kitStatsLatencyDDL("usage_latency_monthly", "stat_month"),
		// ---- 排行快照与派生窗口 ----
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_rank_snapshots (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          window_key text NOT NULL,
          metric text NOT NULL,
          snapshot_at text NOT NULL,
          rank integer NOT NULL,
          scope_id text NOT NULL,
          metric_value double precision NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_overview_summary_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_used_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_overview_trend_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          bucket_key text NOT NULL,
          request_count bigint NOT NULL DEFAULT 0,
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, bucket_key)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_model_rank_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          rank integer NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
          request_count bigint NOT NULL DEFAULT 0,
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, model)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_error_rank_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          rank integer NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key, rank, provider_code, error_code, status_code)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.ai_performance_summary_windows (
          system_account_id text NOT NULL,
          window_key text NOT NULL,
          start_date text NOT NULL DEFAULT '',
          end_date text NOT NULL DEFAULT '',
          request_count bigint NOT NULL DEFAULT 0,
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, window_key)
        )`,
		// ---- 配额小时窗与脏标记 ----
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_windows (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          window_hours integer NOT NULL,
          total_cost_usd double precision NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, window_hours)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_window_dirty_scopes (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_overview_dirty_scopes (
          system_account_id text PRIMARY KEY,
          scope_id text NOT NULL,
          min_changed_date text NOT NULL,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.ai_performance_summary_dirty_system_accounts (
          system_account_id text PRIMARY KEY,
          min_stat_date text NOT NULL,
          max_stat_date text NOT NULL,
          generation bigint NOT NULL DEFAULT 1,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
		// ---- 范围窗口快照（window_key 为 GENERATED STORED，生产形状）----
		`CREATE TABLE IF NOT EXISTS juhe_stats.usage_scope_range_windows (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          start_date text NOT NULL,
          end_date text NOT NULL,
          window_key text GENERATED ALWAYS AS (start_date || ':' || end_date) STORED,
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          active_days integer NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date)
        )`,
		// ---- 账户质量/健康 ----
		`CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_minute_stats (
          account_id text NOT NULL,
          system_account_id text NOT NULL,
          provider_code text NOT NULL,
          stat_minute text NOT NULL,
          request_count bigint NOT NULL DEFAULT 0,
          success_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          last_sample_at text,
          last_success_at text,
          last_error_at text,
          last_error_message text,
          updated_at text NOT NULL,
          PRIMARY KEY (account_id, stat_minute)
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.account_quality_dirty_accounts (
          account_id text PRIMARY KEY,
          first_dirty_at text NOT NULL,
          updated_at text NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS juhe_stats.account_health_hourly (
          account_id text NOT NULL,
          system_account_id text NOT NULL,
          provider_code text NOT NULL,
          stat_hour text NOT NULL,
          status text NOT NULL CHECK (status IN ('success', 'failure')),
          last_observed_at text NOT NULL,
          last_record_id text NOT NULL,
          status_code integer,
          error_code text,
          error_message text,
          updated_at text NOT NULL,
          PRIMARY KEY (account_id, stat_hour)
        )`,
		// ---- job state ----
		`CREATE TABLE IF NOT EXISTS juhe_stats.stats_job_state (
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
        )`,
		// ---- juhe_business 最小授权链表（授权查找消费列；FK/其余 NOT NULL 不复刻，
		// circuitstore 先例同风格）----
		`CREATE TABLE IF NOT EXISTS juhe_business.resource_authorizations (
      id text PRIMARY KEY,
      resource_type text NOT NULL,
      resource_id text,
      grantee_system_account_id text
    )`,
		`CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      system_account_id text,
      authorization_instance_authorization_id text
    )`,
		kitBusinessBindingDDL,
		// ---- success_cost_usd（success-cost-usd-pg-columns，六表）----
		`ALTER TABLE juhe_stats.usage_stats_totals ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.usage_stats_minute ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.usage_stats_hourly ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.usage_stats_daily ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.usage_stats_weekly ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.usage_stats_monthly ADD COLUMN IF NOT EXISTS success_cost_usd double precision NOT NULL DEFAULT 0`,
	}
	// ---- 媒体计量列（media-stats-pg-columns：usage_stats 六层 + usage_model
	// 五层 + usage_scope_range_windows）----
	for _, table := range []string{
		"usage_stats_totals", "usage_stats_minute", "usage_stats_hourly", "usage_stats_daily", "usage_stats_weekly", "usage_stats_monthly",
		"usage_model_minute", "usage_model_hourly", "usage_model_daily", "usage_model_weekly", "usage_model_monthly",
		"usage_scope_range_windows",
	} {
		statements = append(statements, kitMediaColumnDDL(table)...)
	}
	return statements
}

// kitStatsBucketDDL 生成 usage_stats 时间桶表的基础 DDL（pg_schema_stats.go
// 形状；success_cost_usd 与媒体列经 ALTER 追加）。
func kitStatsBucketDDL(tableName, timeColumn string) string {
	return `CREATE TABLE IF NOT EXISTS juhe_stats.` + tableName + ` (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          ` + timeColumn + ` text NOT NULL,
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
          duration_ms_sum bigint NOT NULL DEFAULT 0,
          duration_ms_count bigint NOT NULL DEFAULT 0,
          duration_ms_max bigint NOT NULL DEFAULT 0,
          first_token_ms_sum bigint NOT NULL DEFAULT 0,
          first_token_ms_count bigint NOT NULL DEFAULT 0,
          first_token_ms_max bigint NOT NULL DEFAULT 0,
          last_used_at text,
          last_error_at text,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, ` + timeColumn + `)
        )`
}

// kitStatsModelDDL 生成 usage_model 表的基础 DDL（媒体列经 ALTER 追加）。
func kitStatsModelDDL(tableName, timeColumn string) string {
	return `CREATE TABLE IF NOT EXISTS juhe_stats.` + tableName + ` (
          system_account_id text NOT NULL,
          ` + timeColumn + ` text NOT NULL,
          provider_code text NOT NULL DEFAULT 'unknown',
          model text NOT NULL DEFAULT 'unknown',
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
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, ` + timeColumn + `, provider_code, model)
        )`
}

// kitStatsErrorDDL 生成 usage_error 表的基础 DDL。
func kitStatsErrorDDL(tableName, timeColumn string) string {
	return `CREATE TABLE IF NOT EXISTS juhe_stats.` + tableName + ` (
          system_account_id text NOT NULL,
          ` + timeColumn + ` text NOT NULL,
          error_group text NOT NULL DEFAULT 'unknown',
          provider_code text NOT NULL DEFAULT 'unknown',
          error_code text NOT NULL DEFAULT 'unknown',
          status_code integer NOT NULL DEFAULT 0,
          error_message text,
          request_count bigint NOT NULL DEFAULT 0,
          error_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, ` + timeColumn + `, error_group, provider_code, error_code, status_code)
        )`
}

// kitStatsLatencyDDL 生成 usage_latency 表的基础 DDL。
func kitStatsLatencyDDL(tableName, timeColumn string) string {
	return `CREATE TABLE IF NOT EXISTS juhe_stats.` + tableName + ` (
          system_account_id text NOT NULL,
          scope_type text NOT NULL,
          scope_id text NOT NULL DEFAULT '',
          metric_type text NOT NULL,
          ` + timeColumn + ` text NOT NULL,
          bucket_upper_bound_ms integer NOT NULL,
          sample_count bigint NOT NULL DEFAULT 0,
          updated_at text NOT NULL,
          PRIMARY KEY (system_account_id, scope_type, scope_id, metric_type, ` + timeColumn + `, bucket_upper_bound_ms)
        )`
}

// kitMediaColumnDDL 生成单表的五列媒体计量 ALTER 追加（media-stats-pg-columns
// 列型：bigint / double precision，与生产逐列一致）。
func kitMediaColumnDDL(tableName string) []string {
	return []string{
		`ALTER TABLE juhe_stats.` + tableName + ` ADD COLUMN IF NOT EXISTS input_audio_tokens BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.` + tableName + ` ADD COLUMN IF NOT EXISTS output_audio_tokens BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.` + tableName + ` ADD COLUMN IF NOT EXISTS tts_input_chars BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.` + tableName + ` ADD COLUMN IF NOT EXISTS audio_input_seconds DOUBLE PRECISION NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_stats.` + tableName + ` ADD COLUMN IF NOT EXISTS output_video_seconds DOUBLE PRECISION NOT NULL DEFAULT 0`,
	}
}

// kitPrepareAll 在隔离测试库上建全部三 schema（juhe_stats / juhe_usage /
// juhe_business）与全部表。
func kitPrepareAll(ctx context.Context, db *sql.DB) error {
	return kitExecDDL(ctx, db, kitStatsDDL)
}

// kitPrepareStats 只建 juhe_stats / juhe_usage（juhe_business 绑定表缺失，
// 供失败可见性臂先失败后补建 kitBusinessBindingDDL）。
func kitPrepareStats(ctx context.Context, db *sql.DB) error {
	for _, statement := range kitStatsDDL {
		if statement == kitBusinessBindingDDL {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// kitExecDDL 逐条执行 DDL（任一失败即中止）。
func kitExecDDL(ctx context.Context, db *sql.DB, statements []string) error {
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// openKitPostgresDB 打开 pgtestkit 隔离 PG 测试库并按生产对齐 DDL 建表；
// 测试结束自动删库。连接串内容不进入任何日志或断言。
func openKitPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, kitPrepareAll)
}

// openKitStatsOnlyPostgresDB 打开不含 juhe_business 绑定表的隔离 PG 测试库
// （D-229 失败可见性臂：先缺表失败，再补 kitBusinessBindingDDL 恢复）。
func openKitStatsOnlyPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, kitPrepareStats)
}

// newPGTestEnv 打开 PG 承载的测试环境（生产方言）：库结构为生产对齐 DDL，
// 时间源与 SQLite 载体的 newTestEnv 相同固定时钟。测试结束自动删库。
func newPGTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newPGTestEnvWithDB(t, openKitPostgresDB(t))
}

// newPGTestEnvWithDB 以外部打开的 PG 句柄装配测试环境（kit 变体 prepare 用，
// 如 openKitStatsOnlyPostgresDB 的缺绑定表场景）。
func newPGTestEnvWithDB(t *testing.T, db *sql.DB) *testEnv {
	t.Helper()
	zone, err := LoadStatsTimezone("UTC")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	return &testEnv{t: t, db: db, dialect: Dialect{}, now: now, zone: zone}
}
