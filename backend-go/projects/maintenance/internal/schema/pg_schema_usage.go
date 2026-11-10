// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaUsage holds the juhe_usage DDL statements in execution
// order.
var postgresSchemaUsage = []PGStatement{
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL: `CREATE TABLE IF NOT EXISTS usage_records (
      id text NOT NULL,
      system_account_id text NOT NULL,
      trace_id text NOT NULL,
      traffic_source text NOT NULL,
      client_ip text,
      api_key_id text,
      group_id text,
      route_group_id text,
      compaction_expected integer NOT NULL DEFAULT 0,
      security_policy_applied integer NOT NULL DEFAULT 0,
      pii_hits_json text,
      account_id text,
      endpoint text,
      provider_code text,
      provider_protocol_profile_id text,
      usage_semantic text,
      model text,
      upstream_model text,
      upstream_response_model text,
      pricing_model text,
      requested_service_tier text NOT NULL DEFAULT 'default',
      effective_service_tier text NOT NULL DEFAULT 'default',
      reported_service_tier text,
      billed_service_tier text NOT NULL DEFAULT 'default',
      requested_reasoning_effort text,
      effective_reasoning_effort text,
      cost_breakdown_snapshot_json text,
      model_mapping_applied integer NOT NULL DEFAULT 0,
      model_mapping_source text,
      source_endpoint_family text,
      upstream_endpoint_family text,
      stream integer NOT NULL DEFAULT 0,
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
      output_image_count integer,
      tts_input_chars bigint NOT NULL DEFAULT 0,
      audio_input_seconds double precision NOT NULL DEFAULT 0,
      usage_missing integer NOT NULL DEFAULT 0,
      output_video_seconds double precision NOT NULL DEFAULT 0,
      cost_usd double precision,
      error_code text,
      error_message text,
      request_snapshot_json text,
      response_snapshot_json text,
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
    ) PARTITION BY RANGE (created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "upstream-response-model-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-m1-audio-metering-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS tts_input_chars bigint NOT NULL DEFAULT 0`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-m1-audio-metering-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS audio_input_seconds double precision NOT NULL DEFAULT 0`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-m1-audio-metering-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS usage_missing integer NOT NULL DEFAULT 0`,
	},
	{
		// 超级分组双层身份（超级分组设计 §4.3）：route_group_id 记路由目标
		// （超级分组），group_id 记实际服务成员组。
		SchemaName: "juhe_usage",
		Source:     "usage-records-super-group-route-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS route_group_id text`,
	},
	{
		// 压缩请求标记（使用记录延迟健康分档设计 §压缩请求识别）：存量库幂等
		// 补列，模式同 route_group_id。
		SchemaName: "juhe_usage",
		Source:     "usage-records-compaction-expected",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS compaction_expected integer NOT NULL DEFAULT 0`,
	},
	{
		// 安全策略计量两列（API Key 安全策略与出站内容脱敏设计 §8，批A 存储）：
		// security_policy_applied = 该请求绑定了非默认安全策略（1/0，与是否命中
		// 无关）；pii_hits_json = 仅命中数 > 0 时由网关写入（按规则 label 计数，
		// 无任何原文）。存量库幂等补列，模式同 compaction_expected/route_group_id。
		SchemaName: "juhe_usage",
		Source:     "usage-records-security-policy",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS security_policy_applied integer NOT NULL DEFAULT 0`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-security-policy",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS pii_hits_json text`,
	},
	{
		// M2 视频计量列（媒体设计 §10）：视频任务输出秒数，任务终态回填；
		// 幂等模式同 M1 音频计量列。
		SchemaName: "juhe_usage",
		Source:     "usage-records-m2-video-metering-pg-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS output_video_seconds double precision NOT NULL DEFAULT 0`,
	},
	{
		// 统计消费水位（BUG-0345 根治，2026-11-03）：`usage_stats_aggregated_at`
		// / `client_ip_stats_aggregated_at` 分别记录该行被两个统计聚合器消费的
		// 时刻，NULL = 尚未聚合。聚合器由此从「按 (created_at, id) 顺序游标消费」
		// 改为「消费未标记行」，消费幂等且不再依赖任何时序假设——记录晚到多久
		// 都不会被游标永久越过。
		//
		// 两个聚合器消费同一张源表、各自有独立游标，因此各需一个水位列；共用一个
		// 列会让先跑的聚合器把行藏起来，另一个永久看不到。
		//
		// 为什么不能只调窗口：`created_at` 是请求终态时刻（逻辑时间），落库是
		// 物理写入；两者差值上限约等于请求墙钟预算（270s）加投递余量，生产实测
		// 最大 299,163ms 对 300,000ms 缓冲窗口只剩 837ms 余量。任何固定窗口都
		// 只能降低失败概率，机制仍在。
		//
		// 存量语义：加列后存量行全为 NULL（视作待聚合）。聚合器首次运行自动结算
		// 存量——按旧游标把 <= 该水位的行标记为已消费（它们已进过统计），之后的
		// 行正常消费；统计写入是增量累加，缺少这一步会把历史行整体重算一遍。见
		// jobs/internal/statsagg/consumption.go 的 BootstrapUsageRecordConsumption。
		SchemaName: "juhe_usage",
		Source:     "usage-records-stats-consumption-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS usage_stats_aggregated_at text`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-stats-consumption-columns",
		SQL:        `ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS client_ip_stats_aggregated_at text`,
	},
	{
		// 待聚合扫描的部分索引（BUG-0345 根治）：只索引未标记的行，索引大小与
		// 实际积压成正比而非与表大小成正比。排序键 (created_at, id) 让「取最旧
		// 未聚合批」直接走索引顺序，无需排序；MIN(created_at) 也能走索引首端。
		SchemaName: "juhe_usage",
		Source:     "usage-records-stats-consumption-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_stats_pending ON usage_records(created_at, id) WHERE usage_stats_aggregated_at IS NULL`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-stats-consumption-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_client_ip_stats_pending ON usage_records(created_at, id) WHERE client_ip_stats_aggregated_at IS NULL`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_group_real_usage`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_group_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_first_token_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_duration_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_cost_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_first_token_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_duration_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_cost_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_api_key_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_account_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_trace_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_model_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_model_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_traffic_source_created`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_client_ip_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_system_account_client_ip_created_sort`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `DROP INDEX IF EXISTS idx_usage_records_provider_protocol_profile_created_at`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_created_sort ON usage_records(system_account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_trace_created_sort ON usage_records(system_account_id, trace_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_group_created_sort ON usage_records(system_account_id, group_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_api_key_created_sort ON usage_records(system_account_id, api_key_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_account_account_created_sort ON usage_records(system_account_id, account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_account_owner ON usage_records(account_owner_system_account_id, account_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_group_owner ON usage_records(group_owner_system_account_id, group_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_account_authorization ON usage_records(account_authorization_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_group_authorization ON usage_records(group_authorization_id, created_at)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_stats_cursor ON usage_records(created_at, id)`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_account_shape ON usage_records(account_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_group_shape ON usage_records(group_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''`,
	},
	{
		SchemaName: "juhe_usage",
		Source:     "usage-records-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_usage_records_system_trace_c_created_sort ON usage_records(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC)`,
	},
}
