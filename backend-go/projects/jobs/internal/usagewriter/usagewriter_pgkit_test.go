package usagewriter

// usagewriter_pgkit_test.go 是本包真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z 测试迁移切片）：
//   - PostgresShardStore 臂与分区确保链臂经 pgtestkit 每测试隔离 PG 测试库
//     装配；DDL 对照 maintenance 生产 PG 权威 schema 逐列复刻
//     （juhe_usage.usage_records：pg_schema_usage.go 的全列形状 + 分区母表
//     PARTITION BY RANGE (created_at)，PK 保持生产的 (created_at, id)；
//     juhe_business.accounts：pg_schema_business_tables.go 中副作用消费列）。
//     maintenance 属独立模块且 internal 包不可跨模块引用，故无法走其建表
//     路径（statsagg / taskruns / cleanuprepo 先例同风格）。
//   - 与生产 DDL 的已知偏差（均为测试最小形状，statsagg 先例同风格）：
//     usage_records 的索引族 DDL 不复刻（写入与分区存在性断言不消费索引）；
//     accounts 只保留 id / last_used_at / updated_at / deleted_at 四个副作用
//     消费列（生产全为 text），FK 与其余 NOT NULL 不复刻；存量库幂等补列的
//     ALTER 序列不复刻（隔离库按当前生产形状新建，列已齐备）。
//   - 组A 迁移面之外的保留项（本切片不动，随生产 sqlite 臂删除批次退役，
//     各文件注释披露）：组B SqliteShardStore / 分片文件臂
//     （store_sqlite_test.go、wo_snapshot_store_writer_test.go、
//     wo_store_freeze_extra_test.go、w9g_gaps_test.go 的 sqlite 臂）；
//     scripted 假驱动（partitions_test.go 的 partitionDDLRecorder 注入族）；
//     内存 mock（writer_test.go 的 mockStore 族）。
//   - 测试体内手写 SQL 按 PG 方言书写（juhe_ 前缀 schema 限定 + $n 占位
//     符）。连接串内容不进入任何日志或断言。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// kitUsageRecordsPartitionedDDL 是 juhe_usage.usage_records 的生产对齐 DDL：
// 逐列对照 pg_schema_usage.go（usage-records 组，含 route_group_id /
// compaction_expected / M1 音频计量 / M2 视频计量列的当前全形状），分区母表
// PARTITION BY RANGE (created_at)，每日分区由被测 ensure 链按批内 createdAt
// 创建（生产自适应分区路径，测试不复刻分区 DDL）。
const kitUsageRecordsPartitionedDDL = `CREATE TABLE IF NOT EXISTS juhe_usage.usage_records (
      id text NOT NULL,
      system_account_id text NOT NULL,
      trace_id text NOT NULL,
      traffic_source text NOT NULL,
      client_ip text,
      api_key_id text,
      group_id text,
      route_group_id text,
      compaction_expected integer NOT NULL DEFAULT 0,
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
    ) PARTITION BY RANGE (created_at)`

// kitBusinessAccountsDDL 是 PostgresShardStore 副作用消费的 juhe_business
// 账户表最小形状（生产 pg_schema_business_tables.go 中四列均为 text；
// FK 与其余 NOT NULL 不复刻，statsagg 先例同风格）。
const kitBusinessAccountsDDL = `CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      last_used_at text,
      updated_at text,
      deleted_at text
    )`

// kitPrepareAll 在隔离测试库上建 juhe_usage / juhe_business schema、分区
// 母表与账户副作用表。
func kitPrepareAll(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		`CREATE SCHEMA IF NOT EXISTS juhe_usage`,
		`CREATE SCHEMA IF NOT EXISTS juhe_business`,
		kitUsageRecordsPartitionedDDL,
		kitBusinessAccountsDDL,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// openKitPostgresDB 打开 pgtestkit 隔离 PG 测试库并按生产对齐 DDL 建分区
// 母表与副作用表；测试结束自动删库。连接串内容不进入任何日志或断言。
func openKitPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, kitPrepareAll)
}
