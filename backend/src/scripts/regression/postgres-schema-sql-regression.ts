import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { DatabaseSync } from 'node:sqlite'

import { buildPostgresSchemaSql, collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

const statements = collectPostgresSchemaStatements()
const sql = buildPostgresSchemaSql()
const healthCheckEndpointModeOfflineMigration = readFileSync(
  'src/scripts/maintenance/account-health-check-endpoint-mode-migration.ts',
  'utf8'
)
const usageRecordUpstreamResponseModelMigration = readFileSync(
  'src/scripts/maintenance/migrate-usage-record-upstream-response-model.ts',
  'utf8'
)
const customProviderModelRepositorySource = readFileSync('src/storage/custom-provider-models.repository.ts', 'utf8')
const providerModelCatalogRepositorySource = readFileSync('src/storage/provider-model-catalog.repository.ts', 'utf8')
const postgresSeedDefaultsSource = readFileSync('src/storage/postgres-seed-defaults.ts', 'utf8')
const dataRetentionSource = readFileSync('src/storage/data-retention.repository.ts', 'utf8')
const usagePartitionSource = readFileSync('src/storage/postgres-usage-record-partitions.ts', 'utf8')
const tableMonitorSource = readFileSync('src/storage/table-monitor.repository.ts', 'utf8')
const tableMonitorRoutesSource = readFileSync('src/modules/table-monitor/table-monitor.routes.ts', 'utf8')
const clientIPStatsNodeWriterFixtureSource = readFileSync(
  'src/scripts/regression/client-ip-stats-node-writer-go-reader-fixture.ts',
  'utf8'
)
const clientIPStatsAccountInsertMatch = clientIPStatsNodeWriterFixtureSource.match(
  /INSERT INTO "juhe_business"\."accounts"\s*\(([\s\S]*?)\)\s*VALUES\s*\(([\s\S]*?)\)\s*ON CONFLICT\(id\)/i
)
assert.ok(clientIPStatsAccountInsertMatch, '必须提取客户端 IP Node writer -> Go reader fixture 的账户 INSERT')
const clientIPStatsAccountInsertColumns = clientIPStatsAccountInsertMatch[1]
const clientIPStatsAccountInsertValues = clientIPStatsAccountInsertMatch[2]
const postgresAccountFixtureSources = [
  'src/scripts/regression/account-list-postgres-smoke.ts',
  'src/scripts/regression/authorization-usage-range-postgres-smoke.ts',
  'src/scripts/regression/client-ip-stats-postgres-smoke.ts',
  'src/scripts/regression/client-ip-stats-node-writer-go-reader-fixture.ts',
  'src/scripts/regression/usage-record-list-postgres-smoke.ts'
].map((path) => ({ path, source: readFileSync(path, 'utf8') }))
const schemaNames = new Set(statements.map((statement) => statement.schemaName))
const usageRecordsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_usage' && /^CREATE TABLE IF NOT EXISTS usage_records\b/i.test(statement.sql))?.sql ?? ''
const providerModelCatalogCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS provider_model_catalog\b/i.test(statement.sql)
)?.sql ?? ''
const customProviderModelsCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS custom_provider_models\b/i.test(statement.sql)
)?.sql ?? ''
const proxyProfilesCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS proxy_profiles\b/i.test(statement.sql)
)?.sql ?? ''
const accountTestTasksCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS account_test_tasks\b/i.test(statement.sql)
)?.sql ?? ''
const accountTestSessionsCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS account_test_sessions\b/i.test(statement.sql)
)?.sql ?? ''
const accountTestSessionTasksCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_business' && /^CREATE TABLE IF NOT EXISTS account_test_session_tasks\b/i.test(statement.sql)
)?.sql ?? ''
const accountUsageSnapshotsCreateSql = statements.find(
  (statement) => statement.schemaName === 'juhe_stats' && /^CREATE TABLE IF NOT EXISTS account_usage_snapshots\b/i.test(statement.sql)
)?.sql ?? ''
const listBuiltInProviderModelsAsyncStart = providerModelCatalogRepositorySource.indexOf('export async function listBuiltInProviderModelsAsync')
const listBuiltInProviderModelsAsyncEnd = providerModelCatalogRepositorySource.indexOf(
  'export async function findBuiltInProviderModelByIdAsync',
  listBuiltInProviderModelsAsyncStart
)
assert.notEqual(listBuiltInProviderModelsAsyncStart, -1, '必须找到 listBuiltInProviderModelsAsync')
assert.notEqual(listBuiltInProviderModelsAsyncEnd, -1, '必须找到 listBuiltInProviderModelsAsync 的函数边界')
const listBuiltInProviderModelsAsyncSource = providerModelCatalogRepositorySource.slice(
  listBuiltInProviderModelsAsyncStart,
  listBuiltInProviderModelsAsyncEnd
)
const listBuiltInProviderModelsAsyncSqlMatch = listBuiltInProviderModelsAsyncSource.match(
  /const rows = await client\.query<ProviderModelCatalogRow>\(`([\s\S]*?)`, \[providerCodes\]\)/
)
assert.ok(listBuiltInProviderModelsAsyncSqlMatch, '必须精确提取 listBuiltInProviderModelsAsync 的 PostgreSQL SQL 模板')
const listBuiltInProviderModelsAsyncSql = listBuiltInProviderModelsAsyncSqlMatch[1]
const listBuiltInProviderModelsAsyncAvailabilityFilterMatch = listBuiltInProviderModelsAsyncSource.match(
  /const availabilityFilter = options\.includeInactive \? '' : `([\s\S]*?)`\s+const rows = await client\.query<ProviderModelCatalogRow>/
)
assert.ok(listBuiltInProviderModelsAsyncAvailabilityFilterMatch, '必须提取 listBuiltInProviderModelsAsync 的 PostgreSQL 可用性过滤条件')
const listBuiltInProviderModelsAsyncAvailabilityFilter = listBuiltInProviderModelsAsyncAvailabilityFilterMatch[1]
assert.match(listBuiltInProviderModelsAsyncSql, /\$\{availabilityFilter\}/, 'Node PG 模型目录查询必须插入可用性过滤条件')

assertFreshSqliteAllowsGeminiInteractionsHealthModes()

assert.ok(statements.length > 100, 'PostgreSQL schema 应从现有 SQLite DDL 收集到完整建表和索引语句')
assert.deepEqual(
  [...schemaNames].sort(),
  ['juhe_business', 'juhe_chat', 'juhe_codex_context', 'juhe_dataset', 'juhe_stats', 'juhe_usage'].sort(),
  'PostgreSQL schema 应覆盖现有业务、聊天、数据集、使用记录、统计和 Responses 桥接状态索引存储边界'
)
assert.equal(
  statements.some((statement) => statement.schemaName === 'juhe_usage' && statement.source === 'usage-catalog'),
  true,
  'juhe_usage schema 应包含使用记录目录表'
)
assert.equal(
  statements.some((statement) => statement.schemaName === 'juhe_usage' && statement.source === 'usage-records'),
  true,
  'juhe_usage schema 应包含高性能模式使用记录主表'
)
assert.equal(
  statements.some((statement) => /\bsupplemental\b/.test(statement.source)),
  false,
  'PostgreSQL schema 不应再生成补列类 supplemental 语句'
)
assertPostgresCreateTableOrder(statements)
assert.match(accountTestTasksCreateSql, /cancel_requested boolean NOT NULL DEFAULT false/, 'PG 账户测试任务取消标记必须使用 boolean')
assert.match(accountTestTasksCreateSql, /queued_at timestamptz NOT NULL[\s\S]+started_at timestamptz[\s\S]+finished_at timestamptz[\s\S]+created_at timestamptz NOT NULL[\s\S]+updated_at timestamptz NOT NULL/, 'PG 账户测试任务时间必须使用 timestamptz')
assert.match(accountTestSessionsCreateSql, /last_heartbeat_at timestamptz NOT NULL[\s\S]+cancel_requested_at timestamptz[\s\S]+finished_at timestamptz[\s\S]+created_at timestamptz NOT NULL[\s\S]+updated_at timestamptz NOT NULL/, 'PG 账户测试会话时间必须使用 timestamptz')
assert.match(accountTestSessionTasksCreateSql, /created_at timestamptz NOT NULL/, 'PG 账户测试会话任务关联时间必须使用 timestamptz')
assert.match(accountUsageSnapshotsCreateSql, /last_attempt_at timestamptz[\s\S]+last_success_at timestamptz[\s\S]+next_refresh_after timestamptz[\s\S]+updated_at timestamptz NOT NULL[\s\S]+created_at timestamptz NOT NULL/, 'PG 账户用量快照时间必须使用 timestamptz')

for (const schemaName of schemaNames) {
  assert.match(sql, new RegExp(`CREATE SCHEMA IF NOT EXISTS "${schemaName}"`), `${schemaName} 应生成 CREATE SCHEMA`)
}

assert.match(sql, /CREATE TABLE IF NOT EXISTS system_accounts/, '应包含业务库 schema')
assert.match(sql, /account_api_key_runtime_states[\s\S]+last_trace_id text/, 'Key 运行态 PostgreSQL schema 必须包含最近失败 traceId')
assert.match(sql, /CREATE TABLE IF NOT EXISTS account_api_key_runtime_states[\s\S]+credential_revision text[\s\S]+last_trace_id text[\s\S]+updated_at text NOT NULL/, 'Node PG schema 必须为 fresh PostgreSQL 创建当前完整 Key 运行态表')
assert.match(sql, /idx_account_api_key_runtime_unique[\s\S]+idx_account_api_key_runtime_status[\s\S]+idx_account_api_key_runtime_probe[\s\S]+idx_account_api_key_runtime_owner/, 'Node PG schema 必须创建 Key 运行态索引')
assert.match(sql, /account_api_key_runtime_states[\s\S]+probe_claim_token text[\s\S]+probe_claimed_until text/, 'Key 运行态 PostgreSQL schema 必须包含探针 claim')
assert.match(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+oauth_access_token_expires_at text[\s\S]+oauth_refresh_token_present integer NOT NULL DEFAULT 0/, 'Node PG accounts schema 必须保留 OAuth 刷新运行态字段及 SQLite 兼容类型')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due\s+ON accounts\(provider_code, type, oauth_refresh_token_present, oauth_access_token_expires_at, status, id\)/, 'Node PG accounts schema 必须创建通用 OAuth 刷新候选索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due\s+ON accounts\(provider_protocol_profile_id, type, oauth_refresh_token_present, \(oauth_access_token_expires_at IS NOT NULL\), oauth_access_token_expires_at ASC, updated_at ASC, id ASC\)\s+WHERE authorization_instance_authorization_id IS NULL AND deleted_at IS NULL/, 'Node PG accounts schema 必须创建 OAuth 刷新 PG due partial 索引')
assert.doesNotMatch(sql, /oauth_refresh_token_present boolean|oauth_refresh_token_present[^\n]*CHECK/i, 'Node PG schema 不得擅自改变 OAuth refresh-token 的 SQLite 兼容存储语义')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_balance_auto_detect_due\s+ON accounts\s*\(balance_query_next_refresh_at ASC, id ASC\)\s+WHERE status = 'active'\s+AND schedulable = 1\s+AND type = 'api_key'\s+AND balance_query_enabled = 0\s+AND balance_query_config_json = '\{\}'\s+AND deleted_at IS NULL\s+AND authorization_instance_authorization_id IS NULL/, 'Node PG schema 必须为首次余额探测恢复扫描保留 SQLite 兼容的精确 partial due 索引')
assert.match(providerModelCatalogCreateSql, /long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false(?=\s|,|\)|;|$)/, 'Node PG 长上下文阈值边界字段必须与 Go migration 保持 boolean')
assert.match(providerModelCatalogCreateSql, /supports_prompt_caching boolean NOT NULL DEFAULT false(?=\s|,|\)|;|$)/, 'Node PG prompt caching 字段必须与 Go migration 保持 boolean')
assert.match(providerModelCatalogCreateSql, /catalog_visible boolean NOT NULL DEFAULT true(?=\s|,|\)|;|$)/, 'Node PG 模型目录可见性字段必须与 Go migration 保持 boolean')
assert.match(customProviderModelsCreateSql, /catalog_visible boolean NOT NULL DEFAULT true/, 'Node PG 自定义模型发布字段必须与 Goose 59 保持 boolean')
assert.match(proxyProfilesCreateSql, /enabled boolean NOT NULL DEFAULT true/, 'Node PG 代理启用字段必须与 Goose 6 保持 boolean')
assert.match(proxyProfilesCreateSql, /last_tested_at timestamptz[\s\S]+created_at timestamptz NOT NULL[\s\S]+updated_at timestamptz NOT NULL/, 'Node PG 代理检测与配置时间必须与 Goose 6 保持 timestamptz')
assert.match(listBuiltInProviderModelsAsyncSql, /FROM juhe_business\.provider_model_catalog\b/, '必须提取 Node PG 模型目录查询的目标 SQL 模板')
assert.match(
  listBuiltInProviderModelsAsyncAvailabilityFilter,
  /catalog_visible = TRUE(?=\s|,|\)|;|$)/,
  'Node PG 模型目录可用性过滤必须对 boolean 可见性字段使用 boolean 谓词'
)
assert.doesNotMatch(
  `${listBuiltInProviderModelsAsyncSql}\n${listBuiltInProviderModelsAsyncAvailabilityFilter}`,
  /catalog_visible = 1\b/,
  'Node PG 模型目录查询及其可用性过滤不得对 boolean 字段使用整数谓词'
)
assert.match(
  postgresSeedDefaultsSource,
  /model\.longContextInputTokenThreshold \?\? null,\s*model\.longContextInputTokenThresholdInclusive === true,\s*model\.longContextInputCostMultiplier \?\? null/,
  'Node PG 模型目录 seed 必须在长上下文阈值后写入 inclusive boolean'
)
assert.match(
  postgresSeedDefaultsSource,
  /long_context_input_token_threshold, long_context_input_token_threshold_inclusive, long_context_input_cost_multiplier/,
  'Node PG 模型目录 INSERT 必须包含长上下文阈值 inclusive 列'
)
assert.match(postgresSeedDefaultsSource, /Array\.from\(\{ length: 39 \}/, 'Node PG 模型目录 seed 必须为 39 个参数生成占位符')
assert.match(postgresSeedDefaultsSource, /model\.supportsPromptCaching === true,\s*model\.catalogVisible !== false,/, 'Node PG 模型目录 seed 必须向 boolean 字段传递 boolean，不能传 0/1')
assert.match(sql, /health_check_endpoint_mode text NOT NULL CHECK \(health_check_endpoint_mode IN \([^)]*'interactions_json', 'interactions_sse'\)\)/, 'PG 当前 accounts schema 必须允许 Gemini Interactions 健康检查模式')
assert.match(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+type text NOT NULL/, 'PG 当前 accounts schema 必须保留账户认证类型字段')
assert.match(sql, /idx_accounts_health_check_candidate_order[\s\S]+type IN \('api_key', 'oauth', 'google_oauth'\)/, 'PG 当前健康检查候选索引必须覆盖当前认证类型')
assert.match(sql, /CREATE TABLE IF NOT EXISTS account_balance_projection_cursors[\s\S]+consumer_key text PRIMARY KEY[\s\S]+observed_at text[\s\S]+outcome_id text[\s\S]+updated_at timestamptz NOT NULL/, 'PG J2 outcome projector 必须有独立持久游标表')
assert.doesNotMatch(sql, /CREATE TABLE IF NOT EXISTS audit_(?:logs|log_attempts|payload_blobs|payload_refs|error_groups)\b/, 'Node PostgreSQL schema 不得重新创建 F3 审计表')
assert.match(sql, /public_api_logs[\s\S]+request_size_bytes bigint NOT NULL DEFAULT 0[\s\S]+response_size_bytes bigint NOT NULL DEFAULT 0/, 'PG 公开接口日志请求/响应大小字段必须使用 bigint')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_records/, '应包含使用记录主表 schema')
assert.match(usageRecordsCreateSql, /PRIMARY KEY \(created_at, id\)[\s\S]+\) PARTITION BY RANGE \(created_at\)/, 'PG 使用记录主表必须按 created_at 日范围分区，主键必须包含分区键')
assert.doesNotMatch(usageRecordsCreateSql, /\bid text PRIMARY KEY\b/, 'PG 使用记录分区父表不能保留只包含 id 的主键')
assert.match(sql, /usage_records[\s\S]+failure_attribution text/, '使用记录主表建表语句应直接包含失败归因字段')
assert.match(usageRecordsCreateSql, /requested_service_tier text NOT NULL DEFAULT 'default'[\s\S]+effective_service_tier text NOT NULL DEFAULT 'default'[\s\S]+reported_service_tier text[\s\S]+billed_service_tier text NOT NULL DEFAULT 'default'/, 'PG 使用记录主表必须包含请求、实际上游、上游报告和计费服务档位')
assert.match(usageRecordsCreateSql, /requested_reasoning_effort text[\s\S]+effective_reasoning_effort text[\s\S]+cost_breakdown_snapshot_json text/, 'PG 使用记录主表必须包含请求/实际上游思考级别与不可变计价快照')
assert.match(sql, /usage_records[\s\S]+model_mapping_applied integer NOT NULL DEFAULT 0[\s\S]+model_mapping_source text[\s\S]+source_endpoint_family text[\s\S]+upstream_endpoint_family text/, 'PG 使用记录主表必须包含模型映射可观测字段')
assert.match(sql, /usage_records[\s\S]+input_audio_tokens integer[\s\S]+output_audio_tokens integer[\s\S]+output_image_count integer/, '使用记录主表建表语句应包含音频 token 和输出图片数量字段')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_stats_totals/, '应包含统计库 schema')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_usage_stats_totals_scope_seed\s+ON usage_stats_totals\(scope_type, system_account_id, scope_id\)/, 'PG usage rollover seed 必须有 scope_type 前导覆盖索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_usage_overview_dirty_first_dirty\s+ON usage_overview_dirty_scopes\(first_dirty_at, system_account_id\)/, 'PG overview dirty 必须创建 first_dirty 公平索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_ai_performance_summary_dirty_first_dirty\s+ON ai_performance_summary_dirty_system_accounts\(first_dirty_at, system_account_id\)/, 'PG AI performance dirty 必须创建 first_dirty 公平索引')
assert.match(sql, /CREATE TABLE IF NOT EXISTS model_token_integrity_windows/, 'PG 统计库应包含模型 Token 可信窗口')
assert.match(sql, /CREATE TABLE IF NOT EXISTS model_token_intercept_baseline_versions/, 'PG 统计库应包含固定截距基线版本')
assert.match(sql, /CREATE TABLE IF NOT EXISTS model_account_trust_results/, 'PG 统计库应包含账号模型可信最新结果')
assert.match(sql, /CREATE TABLE IF NOT EXISTS model_trust_latest_dirty_accounts/, 'PG 统计库应包含模型可信 latest 可重试脏队列')
assert.match(sql, /CREATE TABLE IF NOT EXISTS model_trust_observation_receipts/, 'PG 统计库应包含模型可信 observation 跨提交防重收据')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_model_trust_latest_dirty_updated ON model_trust_latest_dirty_accounts\(updated_at, system_account_id, account_id, requested_model\)/, 'PG 模型可信脏队列必须有有界续跑索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation\s+ON model_check_observations\(created_at, id\)\s+WHERE aggregation_completed_at IS NULL/, 'PG 未聚合 observation 必须有部分索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_model_token_integrity_windows_activation ON model_token_integrity_windows\(cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, account_id\)/, 'PG 固定截距激活物化必须有匹配索引')
assert.match(sql, /usage_stats_totals[\s\S]+request_count bigint NOT NULL DEFAULT 0[\s\S]+input_tokens bigint NOT NULL DEFAULT 0[\s\S]+duration_ms_sum bigint NOT NULL DEFAULT 0/, 'PG 统计累计字段必须使用 bigint，避免生产聚合溢出')
assert.match(sql, /usage_scope_range_windows[\s\S]+request_count bigint NOT NULL DEFAULT 0[\s\S]+first_token_ms_sum bigint NOT NULL DEFAULT 0/, 'PG 范围窗口累计字段必须使用 bigint')
assert.match(sql, /usage_scope_range_windows[\s\S]+window_key text GENERATED ALWAYS AS \(start_date \|\| ':' \|\| end_date\) STORED/, 'PG usage scope 范围窗口必须生成 window_key')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_range_window_requests/, 'PG 统计库应包含按需范围窗口请求表')
assert.match(sql, /usage_range_window_requests[\s\S]+window_key text GENERATED ALWAYS AS \(start_date \|\| ':' \|\| end_date\) STORED/, 'PG 按需范围窗口请求表必须生成 window_key')
assert.doesNotMatch(sql, /data_archive_manifests/, 'PG 当前 schema 不应保留没有消费方的归档 manifest 表')
assert.doesNotMatch(dataRetentionSource, /recordDataArchiveManifest|archivedPartitions|juhe_archive/, '使用记录保留链路不应再写同库冷归档')
assert.match(usagePartitionSource, /DETACH PARTITION[\s\S]*DROP TABLE/, '到期整日分区必须在安全游标后直接 DETACH 并 DROP')
assert.doesNotMatch(usagePartitionSource, /CREATE SCHEMA IF NOT EXISTS juhe_archive|SET SCHEMA juhe_archive/, '分区工具不应创建或写入 juhe_archive')
assert.doesNotMatch(tableMonitorSource, /juhe_archive|role:\s*'archive'/, '表监控不应生成 postgres:juhe_archive 占位项')
assert.match(
  tableMonitorSource,
  /rowsByRole\s*=\s*await\s+Promise\.all\(monitoredDatabaseRoles\.map[\s\S]+WHERE database_role = \?/,
  'PG 表监控概览必须使用受控角色集合过滤已移除的历史角色快照'
)
assert.doesNotMatch(tableMonitorRoutesSource, /'archive'/, '表监控接口不应再接受 archive 角色')
assert.match(sql, /CREATE TABLE IF NOT EXISTS codex_context_sessions/, '应包含 Responses 桥接状态索引 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_conversations/, '应包含 AI 问答会话 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_message_idempotency/, '应包含 AI 问答发送幂等登记 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_user_storage_windows/, '应包含 AI 问答用户容量窗口 schema')
assert.match(sql, /storage_reserved_bytes bigint NOT NULL DEFAULT 0/, 'PG 助手消息 reservation 必须使用 bigint')
assert.match(sql, /reserved_bytes bigint NOT NULL DEFAULT 0/, 'PG 容量窗口 reservation 必须使用 bigint')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_context_checkpoints/, '应包含 AI 问答模型上下文 checkpoint schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_context_entries/, '应包含 AI 问答模型上下文 entry schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_assets/, '应包含 AI 问答私有图片资产 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_asset_references/, '应包含 AI 问答消息资产引用 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS chat_image_generations/, '应包含 AI 问答图片生成谱系 schema')
const chatMessagesCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_messages\b/i.test(statement.sql))?.sql ?? ''
const chatConversationsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_conversations\b/i.test(statement.sql))?.sql ?? ''
const chatCheckpointsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_context_checkpoints\b/i.test(statement.sql))?.sql ?? ''
const chatEntriesCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_context_entries\b/i.test(statement.sql))?.sql ?? ''
const chatAssetsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_assets\b/i.test(statement.sql))?.sql ?? ''
const chatAssetReferencesCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_asset_references\b/i.test(statement.sql))?.sql ?? ''
const chatImageGenerationsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_image_generations\b/i.test(statement.sql))?.sql ?? ''
const chatAssetUsageCreateSql = statements.find((statement) => statement.schemaName === 'juhe_chat' && /^CREATE TABLE IF NOT EXISTS chat_user_asset_usage\b/i.test(statement.sql))?.sql ?? ''
assert.match(chatMessagesCreateSql, /PRIMARY KEY \(created_at, id\)[\s\S]+\) PARTITION BY RANGE \(created_at\)/, 'PG AI 问答消息主表必须按 created_at 日范围分区')
assert.match(chatConversationsCreateSql, /next_sequence_no bigint NOT NULL DEFAULT 1[\s\S]+context_revision bigint NOT NULL DEFAULT 0[\s\S]+compacted_through_sequence bigint NOT NULL DEFAULT 0/, 'PG AI 问答会话的上下文版本与消息序号必须使用 bigint')
assert.match(chatConversationsCreateSql, /user_turn_count bigint NOT NULL DEFAULT 0[\s\S]+CHECK \(user_turn_count >= 0\)/, 'PG AI 问答会话必须持久化非负用户轮次计数')
assert.match(chatConversationsCreateSql, /default_image_model text NOT NULL DEFAULT 'gpt-image-2'/, 'PG AI 问答会话必须持久化默认图像模型')
assert.match(chatCheckpointsCreateSql, /source_revision bigint NOT NULL[\s\S]+source_from_sequence bigint NOT NULL[\s\S]+source_through_sequence bigint NOT NULL[\s\S]+request_body_bytes bigint NOT NULL/, 'PG checkpoint 来源序号和请求字节必须使用 bigint')
assert.match(chatEntriesCreateSql, /sequence bigint NOT NULL[\s\S]+content_bytes bigint NOT NULL[\s\S]+token_count bigint/, 'PG checkpoint entry 序号、字节和 token 必须使用 bigint')
assert.match(chatAssetsCreateSql, /original_bytes bigint NOT NULL[\s\S]+processed_bytes bigint/, 'PG 图片资产原图和处理后字节数必须使用 bigint')
assert.match(chatAssetsCreateSql, /source_kind text NOT NULL DEFAULT 'user_upload'[\s\S]+UNIQUE \(id, conversation_id\)[\s\S]+source_kind IN \('user_upload', 'assistant_generated'\)/, 'PG 图片资产必须保存带上传默认值的来源类型和会话复合候选键')
assert.match(chatAssetsCreateSql, /processed_mime_type IN \('image\/jpeg', 'image\/png', 'image\/webp'\)/, 'PG 图片资产必须允许 JPEG、PNG 和 WebP 处理结果')
assert.match(chatAssetReferencesCreateSql, /FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE[\s\S]+UNIQUE \(message_id, content_order\)[\s\S]+reference_kind IN \('user_input', 'assistant_output'\)/, 'PG 消息资产引用必须按会话绑定资产，并约束消息内容顺序和引用类型')
assert.match(chatImageGenerationsCreateSql, /asset_id text PRIMARY KEY[\s\S]+source_asset_ids_json text NOT NULL DEFAULT '\[\]'[\s\S]+FOREIGN KEY \(asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE[\s\S]+FOREIGN KEY \(root_asset_id, conversation_id\) REFERENCES chat_assets\(id, conversation_id\) ON DELETE CASCADE[\s\S]+operation IN \('generate', 'edit'\)/, 'PG 图片生成谱系必须约束输出、根资产、来源数组与操作类型')
assert.match(chatAssetUsageCreateSql, /asset_bytes bigint NOT NULL DEFAULT 0[\s\S]+asset_count integer NOT NULL DEFAULT 0/, 'PG 用户图片额度字节必须使用 bigint，数量计数保留 integer')
assert.match(chatAssetsCreateSql, /observation_status text NOT NULL DEFAULT 'not_requested'[\s\S]+cleanup_status text NOT NULL DEFAULT 'active'/, 'PG 图片资产必须持久化隐藏说明与对象清理状态')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_messages_compaction_source\s+ON chat_messages\(conversation_id, system_account_id, status, sequence_no\)/, 'PG 压缩来源分页必须有匹配索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_context_checkpoints_cleanup\s+ON chat_context_checkpoints\(expires_at, status, id\)/, 'PG checkpoint 到期清理必须有匹配索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_assets_cleanup\s+ON chat_assets\(cleanup_status, cleanup_retry_at, expires_at, id\)/, 'PG 图片资产清理必须有匹配索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_asset_references_message\s+ON chat_asset_references\(conversation_id, message_id, content_order\)/, 'PG 消息资产引用查询必须有匹配索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_asset_references_asset_valid\s+ON chat_asset_references\(asset_id, expires_at\)/, 'PG 资产有效引用判断必须有匹配索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_chat_asset_references_cleanup\s+ON chat_asset_references\(expires_at, asset_id, message_id\)/, 'PG 过期资产引用清理必须有匹配索引')
assert.match(sql, /codex_context_sessions[\s\S]+storage_offset_bytes bigint NOT NULL[\s\S]+raw_size_bytes bigint NOT NULL[\s\S]+compressed_size_bytes bigint NOT NULL/, 'PG Responses 桥接状态文件 offset/大小字段必须使用 bigint')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategies/, '应包含策略路由表 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategy_groups/, '应包含策略路由分组绑定表 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+health_check_model text NOT NULL/, 'AI 账户新建 schema 应直接包含账户检查模型字段')
assert.match(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+health_check_endpoint_mode text NOT NULL[\s\S]+CHECK \(health_check_endpoint_mode IN \('images_json', 'chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse'\)\)/, 'AI 账户新建 schema 应直接包含受约束的健康检查请求形态')
assert.match(sql, /CREATE TABLE IF NOT EXISTS account_api_key_pool_probe_cursors[\s\S]+PRIMARY KEY \(account_id, purpose\)[\s\S]+FOREIGN KEY \(account_id\) REFERENCES accounts\(id\) ON DELETE CASCADE/, 'PostgreSQL schema 必须包含带账户级清理约束的 API Key 探针游标表')
assert.doesNotMatch(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+last_successful_test_model text/, 'AI 账户新建 schema 不应继续包含旧的最后成功测试模型字段')
assert.match(healthCheckEndpointModeOfflineMigration, /LOCK TABLE juhe_business\.accounts IN ACCESS EXCLUSIVE MODE/, '历史字段切换必须通过停服离线事务锁定账户表')
assert.match(healthCheckEndpointModeOfflineMigration, /RENAME COLUMN health_check_endpoint_family TO health_check_endpoint_mode/, '离线迁移必须直接替换旧列，不能双字段兼容')
assert.match(healthCheckEndpointModeOfflineMigration, /if \(input\.providerCode === 'gpt'\) return input\.accountType\.trim\(\) === 'oauth' \? 'responses_json' : 'responses_sse'/, '离线迁移必须让 OAuth GPT 使用 Responses JSON，API Key GPT 使用 Responses Streaming')
assert.match(healthCheckEndpointModeOfflineMigration, /const expectedGptMode = row\.type\.trim\(\) === 'oauth' \? 'responses_json' : 'responses_sse'/, '离线迁移校验必须与 OAuth/API Key 的 J1 endpoint mode 契约一致')
assert.match(healthCheckEndpointModeOfflineMigration, /legacyFamilyGenerationModes[\s\S]+chat_completions: \['chat_json', 'chat_sse'\][\s\S]+messages: \['messages_json', 'messages_sse'\][\s\S]+generate_content: \['generate_content_json', 'generate_content_sse'\]/, '离线迁移必须按历史协议族从加密能力中选择 JSON 或 Streaming')
assert.match(healthCheckEndpointModeOfflineMigration, /migrationGenerationModeFallbackOrder[\s\S]+'chat_json'[\s\S]+'responses_json'[\s\S]+'messages_json'[\s\S]+'generate_content_json'[\s\S]+'chat_sse'/, '历史 family 与真实能力错配时必须按稳定顺序从全部生成 mode 回退，且 JSON 优先于 Streaming')
assert.match(healthCheckEndpointModeOfflineMigration, /decryptJson\(encrypted\)/, '离线迁移必须通过应用层 codec 解密全部账户 supported endpoint modes')
assert.match(healthCheckEndpointModeOfflineMigration, /encryptJson\(normalized\.credentials\)/, '离线迁移必须通过应用层 codec 重新加密 GPT supported endpoint modes')
assert.match(
  clientIPStatsAccountInsertColumns,
  /health_check_model,\s*health_check_endpoint_mode,\s*created_at,\s*updated_at/,
  '客户端 IP Node writer -> Go reader fixture 必须写入当前健康检查请求形态字段'
)
assert.match(
  clientIPStatsAccountInsertValues,
  /'gpt-5\.5',\s*'responses_sse'/,
  '客户端 IP Node writer -> Go reader fixture 的 GPT 账户必须使用精确 Responses Streaming 请求形态'
)
assert.doesNotMatch(
  clientIPStatsAccountInsertColumns,
  /health_check_endpoint_family/,
  '客户端 IP Node writer -> Go reader fixture 不得恢复旧健康检查协议族字段'
)
for (const fixture of postgresAccountFixtureSources) {
  const accountInsertMatches = [...fixture.source.matchAll(
    /INSERT INTO (?:"juhe_business"\."accounts"|juhe_business\.accounts)\s*\(([\s\S]*?)\)\s*(?:VALUES|SELECT)/gi
  )]
  assert.ok(accountInsertMatches.length > 0, `${fixture.path} 必须至少包含一条可检查的账户 INSERT fixture`)
  for (const match of accountInsertMatches) {
    assert.match(match[1], /\bhealth_check_model\b/, `${fixture.path} 的账户 fixture 必须写入 health_check_model`)
    assert.match(match[1], /\bhealth_check_endpoint_mode\b/, `${fixture.path} 的账户 fixture 必须写入 health_check_endpoint_mode`)
  }
}
assert.match(sql, /route_strategy_id text NOT NULL/, 'api_keys 建表语句应强制绑定 route_strategy_id')
assert.match(sql, /api_keys[\s\S]+is_default integer NOT NULL DEFAULT 0/, 'api_keys 建表语句应包含默认 API Key 标识')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique ON api_keys\(route_strategy_id\) WHERE is_default = 1/, '默认 API Key 应按路由策略保持唯一')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_default_updated ON api_keys\(system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC\)/, 'API Key 按默认项置顶的账户列表排序应有匹配索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_api_keys_system_account\b/, 'API Key 不应保留仅按账户筛选的冗余索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_updated\b/, 'API Key 不应保留被默认项主排序覆盖的冗余索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_c_lookup ON api_keys\(system_account_id, \(name COLLATE "C"\), id\)/, 'PG API Key 名称前缀查询必须有 owner + C collation 表达式索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_owner_list_order\s+ON accounts\(system_account_id, priority ASC, created_at ASC, id ASC\)\s+WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL/, 'PG 自有账户列表默认排序必须有 owner/order 部分索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_lookup\s+ON accounts\(system_account_id, name, id\)\s+WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL/, 'PG 自有账户名称精确/前缀查询必须有 owner/name 部分索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup ON accounts\(name, id\) WHERE deleted_at IS NULL/, 'PG 全局账户名称前缀搜索必须有 name 索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_name_c_lookup ON accounts\(\(name COLLATE "C"\), id\) WHERE deleted_at IS NULL/, 'PG 全局账户名称前缀搜索必须有 C collation 表达式索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_c_lookup ON accounts\(system_account_id, \(name COLLATE "C"\), id\) WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL/, 'PG 自有账户名称前缀搜索必须有 owner + C collation 表达式索引')
assert.doesNotMatch(sql, /idx_accounts_(?:owner_)?name_lower_lookup|lower\(name\).*accounts/, 'PG AI 账户名称索引不应再折叠 name 大小写')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_owner_term\s+ON account_name_search_terms\(system_account_id, term, account_id\)/, 'PG AI 账户名称包含候选查询必须有 owner/term/account 覆盖索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_group_accounts_owner_group_enabled ON group_accounts\(system_account_id, group_id, enabled, account_id\)/, 'PG AI 账户分组筛选必须有 owner/group/enabled 覆盖索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag_owner ON account_tag_bindings\(tag_id, system_account_id, account_id\)/, 'PG AI 账户标签筛选必须有 tag/owner/account 覆盖索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_account_shape\s+ON usage_records\(account_id, created_at DESC, id DESC, provider_code\)\s+WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway'[\s\S]+endpoint IS NOT NULL AND btrim\(endpoint\) <> ''/, 'PG 最近 OpenAI 请求形状账户回查必须有账号维度部分索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_group_shape\s+ON usage_records\(group_id, created_at DESC, id DESC, provider_code\)\s+WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway'[\s\S]+endpoint IS NOT NULL AND btrim\(endpoint\) <> ''/, 'PG 最近 OpenAI 请求形状分组回查必须有分组维度部分索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_usage_records_system_trace_c_created_sort ON usage_records\(system_account_id, \(trace_id COLLATE "C"\), created_at DESC, id DESC\)/, 'PG 使用记录 trace 前缀查询必须有用户范围 + C collation 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_trace_c_created_sort\b/, 'PG 使用记录不应再给目录表创建全局 trace 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_client_ip_c_created_sort\b/, 'PG 使用记录不应再给目录表创建全局 client IP 前缀索引')
assert.doesNotMatch(sql, /idx_audit_logs_/, 'Node PostgreSQL schema 不得保留 F3 审计索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_public_api_logs_trace_c_created_sort\b/, 'PG 公开接口日志不应再创建全局 trace 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_public_api_logs_client_ip_c_created_sort\b/, 'PG 公开接口日志不应再创建全局 client IP 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_traffic_created_sort\b/, 'PG 使用记录列表不应再依赖目录表来源索引')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups\(system_account_id, provider_code\) WHERE is_default = 1/, '默认分组应按 provider_code 保持唯一')
assert.doesNotMatch(sql, /idx_groups_owner_protocol_profile_default_unique|groups\(system_account_id, provider_protocol_profile_id\)/, '默认分组不应再按 provider protocol profile 限制唯一')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique\s+ON custom_provider_models\(provider_code, system_account_id, model\)\s+WHERE scope = 'personal'/, '自定义模型唯一索引应保留模型 ID 大小写语义')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique\s+ON custom_provider_models\(provider_code, model\)\s+WHERE scope = 'global'/, '全局自定义模型应按 provider + model 保持唯一')
assert.match(sql, /scope IN \('personal', 'global'\)/, '自定义模型 scope 应支持个人和全局作用域')
assert.match(sql, /CREATE TABLE IF NOT EXISTS custom_provider_models[\s\S]+supported_service_tiers_json text NOT NULL DEFAULT '\[\]'[\s\S]+supported_reasoning_efforts_json text NOT NULL DEFAULT '\[\]'[\s\S]+default_reasoning_effort text/, '自定义模型 schema 应包含 GPT 服务等级和思考能力字段')
assert.match(customProviderModelRepositorySource, /supported_service_tiers_json[\s\S]+supported_reasoning_efforts_json[\s\S]+default_reasoning_effort/, '自定义模型 SQLite/PostgreSQL repository 必须同时读写 GPT 能力字段')
assert.doesNotMatch(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique_lower[\s\S]+lower\(model\)/, '自定义模型不应再用 lower(model) 限制唯一')
assert.doesNotMatch(sql, /idx_provider_default_health_check_models_model\s+ON provider_default_health_check_models\(provider_code, lower\(model\), system_account_id\)/, '默认检查模型索引不应再折叠模型 ID 大小写')
assert.doesNotMatch(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_default_unique ON route_strategies\(system_account_id\) WHERE is_default = 1/, '默认策略路由不应再限制为每个系统账户唯一')
assert.doesNotMatch(sql, /client_profile text NOT NULL DEFAULT 'auto'/, 'api_keys 建表语句不应再包含 client_profile 字段')
assert.doesNotMatch(sql, /explicit_hybrid_route_rules_json text/, 'api_keys 建表语句不应再包含 explicit_hybrid_route_rules_json 字段')
assert.match(sql, /container_id text/, 'openai_compatible_files 建表语句应包含 container_id 字段')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS route_strategy_id text/, 'PostgreSQL schema 不应再为 api_keys.route_strategy_id 生成历史补列语句')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS client_profile text NOT NULL DEFAULT 'auto'/, 'PostgreSQL schema 不应再补 client_profile')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS explicit_hybrid_route_rules_json text/, 'PostgreSQL schema 不应再补 explicit_hybrid_route_rules_json')
assert.doesNotMatch(sql, /ALTER TABLE openai_compatible_files ADD COLUMN container_id\b/, 'PostgreSQL schema 不应重复为 openai_compatible_files.container_id 补列')
assert.doesNotMatch(sql, /ALTER TABLE (?:audit_logs|audit_payload_refs)\b/, 'Node PostgreSQL schema 不得通过运行时 DDL 补齐 F3 审计表')
assert.match(sql, /CREATE TABLE IF NOT EXISTS client_ip_range_window_dirty_ips[\s\S]*generation bigint NOT NULL DEFAULT 1[\s\S]*first_dirty_at text NOT NULL/, '客户端 IP 范围窗口 dirty 表必须包含 generation 和首次标脏时间')
assert.match(sql, /CREATE TABLE IF NOT EXISTS client_ip_account_range_window_dirty_ips[\s\S]*generation bigint NOT NULL DEFAULT 1[\s\S]*first_dirty_at text NOT NULL/, '客户端 IP 账号范围窗口 dirty 表必须包含 generation 和首次标脏时间')
assert.match(sql, /ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text/, 'PostgreSQL 使用记录当前迁移必须保留上游响应模型列')
assert.match(sql, /ALTER TABLE account_test_tasks ADD COLUMN IF NOT EXISTS queued_deadline_at timestamptz/, 'PostgreSQL 账户测试任务必须保留排队截止时间列')
assert.match(usageRecordUpstreamResponseModelMigration, /if \(!offlineConfirmed\)/, '上游响应模型列迁移必须要求显式离线确认')
assert.match(usageRecordUpstreamResponseModelMigration, /ALTER TABLE juhe_usage\.usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text/, '上游响应模型列迁移必须包含幂等 PostgreSQL DDL')
assert.match(usageRecordUpstreamResponseModelMigration, /listUsageRecordShardLocations\(\)/, '上游响应模型列迁移必须枚举 SQLite 已注册 usage shard')
const retiredPostgresSchemaPatterns = [
  /ALTER TABLE provider_model_catalog ADD COLUMN IF NOT EXISTS cache_storage_usd_per_1m_per_hour double precision/,
  /ALTER TABLE custom_provider_models ADD COLUMN IF NOT EXISTS cache_storage_usd_per_1m_per_hour double precision/,
  /ALTER TABLE model_check_runs ADD COLUMN IF NOT EXISTS (?:trigger_kind|schedule_id|policy_snapshot_json|quality_decision_json|quality_health_sync_status)/,
  /ALTER TABLE model_check_observations ADD COLUMN IF NOT EXISTS aggregation_completed_at text/,
  /ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS (?:conversation_key|session_id|session_client_type) text/,
  /ALTER TABLE client_ip_(?:account_)?range_window_dirty_ips ADD COLUMN IF NOT EXISTS (?:generation|first_dirty_at)/,
  /ALTER TABLE background_job_leases ADD COLUMN IF NOT EXISTS fencing_token bigint NOT NULL DEFAULT 0/,
  /account_circuit_confirmation_(?:failures_required|failure_count|evidence_json)_check/
]
for (const pattern of retiredPostgresSchemaPatterns) {
  assert.doesNotMatch(sql, pattern, `PostgreSQL schema 不应残留已结束兼容窗口的运行时 DDL：${pattern}`)
}
assert.match(sql, /DROP INDEX IF EXISTS idx_usage_records_created_at/, 'PostgreSQL 使用记录分区迁移必须清理已替代的历史索引')
const schemaWithoutCurrentRuntimeUpgrades = sql
  .replace(/ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text;/g, '')
  .replace(/ALTER TABLE account_test_tasks ADD COLUMN IF NOT EXISTS queued_deadline_at timestamptz;/g, '')
  .replace(/ALTER TABLE system_accounts ADD COLUMN IF NOT EXISTS ai_account_limit integer CHECK \(ai_account_limit BETWEEN 0 AND 1000000\);/g, '')
  .replace(/ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS lifecycle_status text NOT NULL DEFAULT 'finalized';/g, '')
  .replace(/ALTER TABLE audit_payload_refs ADD COLUMN IF NOT EXISTS drop_reason text;/g, '')
assert.doesNotMatch(schemaWithoutCurrentRuntimeUpgrades, /\bALTER TABLE\b[\s\S]+\bADD COLUMN\b/i, 'PostgreSQL schema 不应包含截止线前的运行时补列语句')

assert.doesNotMatch(sql, /\bPRAGMA\b/i, 'PostgreSQL SQL 不应残留 SQLite PRAGMA')
assert.doesNotMatch(sql, /COLLATE\s+NOCASE/i, 'PostgreSQL SQL 不应残留 SQLite NOCASE collation')
assert.doesNotMatch(sql, /\bjson_valid\b|\bjson_type\b/i, 'PostgreSQL SQL 不应残留 SQLite JSON 函数')
assert.match(sql, /jsonb_typeof\(match_json::jsonb\) = 'object'/, 'SQLite JSON object 校验应映射到 PostgreSQL jsonb_typeof')
assert.match(sql, /lower\(name\)/, 'SQLite NOCASE 索引应映射为 lower 表达式索引')
assert.match(sql, /\bdouble precision\b/, 'SQLite REAL 类型应映射为 PostgreSQL double precision')

for (const statement of statements) {
  assert.equal(statement.sql.endsWith(';'), false, `单条语句不应带尾部分号：${statement.source}`)
}

console.log('postgres-schema-sql-regression passed')

function assertFreshSqliteAllowsGeminiInteractionsHealthModes(): void {
  const database = new DatabaseSync(':memory:')
  try {
    applyBusinessSchema(database)
    database.exec(`
      INSERT INTO system_accounts (
        id, username, display_name, password_hash, created_at, updated_at
      ) VALUES (
        'system-gemini-interactions', 'gemini-interactions', 'Gemini Interactions', 'test-hash', '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
      );
      INSERT INTO providers (
        id, code, name, created_at, updated_at
      ) VALUES (
        'provider-gemini-interactions', 'gemini', 'Gemini', '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
      );
      INSERT INTO protocols (
        id, code, version, name, created_at, updated_at
      ) VALUES (
        'protocol-gemini-interactions', 'gemini', 'v1beta', 'Gemini v1beta', '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
      );
      INSERT INTO provider_protocol_profiles (
        id, provider_code, name, protocol_code, protocol_version, base_url,
        default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
      ) VALUES (
        'profile-gemini-interactions', 'gemini', 'Gemini Interactions', 'gemini', 'v1beta',
        'https://generativelanguage.googleapis.com', 'gemini-3.5-flash', '["api_key"]', '["interactions"]',
        '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
      );
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, credentials_encrypted, health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES
        (
          'account-gemini-interactions-json', 'system-gemini-interactions', 'gemini', 'profile-gemini-interactions', 'gemini', 'v1beta',
          'Gemini Interactions JSON', 'api_key', 'encrypted-json', 'gemini-3.5-flash', 'interactions_json',
          '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
        ),
        (
          'account-gemini-interactions-sse', 'system-gemini-interactions', 'gemini', 'profile-gemini-interactions', 'gemini', 'v1beta',
          'Gemini Interactions SSE', 'api_key', 'encrypted-sse', 'gemini-3.5-flash', 'interactions_sse',
          '2026-07-18T00:00:00.000Z', '2026-07-18T00:00:00.000Z'
        );
    `)
    assert.equal(database.prepare("SELECT count(*) AS count FROM accounts WHERE health_check_endpoint_mode IN ('interactions_json', 'interactions_sse')").get()?.count, 2)
  } finally {
    database.close()
  }
}

function assertPostgresCreateTableOrder(input: typeof statements): void {
  const tableNamesBySchema = new Map<string, Set<string>>()
  for (const statement of input) {
    const tableName = extractCreatedTableName(statement.sql)
    if (!tableName) continue
    const tableNames = tableNamesBySchema.get(statement.schemaName) ?? new Set<string>()
    tableNames.add(tableName)
    tableNamesBySchema.set(statement.schemaName, tableNames)
  }

  const seenBySchema = new Map<string, Set<string>>()
  for (const statement of input) {
    const tableName = extractCreatedTableName(statement.sql)
    if (!tableName) continue
    const schemaTables = tableNamesBySchema.get(statement.schemaName) ?? new Set<string>()
    const seen = seenBySchema.get(statement.schemaName) ?? new Set<string>()
    for (const referencedTableName of extractReferencedTableNames(statement.sql)) {
      if (referencedTableName === tableName || !schemaTables.has(referencedTableName)) continue
      assert.equal(
        seen.has(referencedTableName),
        true,
        `${statement.schemaName}.${tableName} 的外键引用 ${referencedTableName}，但被引用表尚未创建`
      )
    }
    seen.add(tableName)
    seenBySchema.set(statement.schemaName, seen)
  }
}

function extractCreatedTableName(sql: string): string | undefined {
  const match = /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/i.exec(sql.trim())
  return match?.[1]?.toLowerCase()
}

function extractReferencedTableNames(sql: string): string[] {
  const tableNames: string[] = []
  for (const match of sql.matchAll(/\bREFERENCES\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/gi)) {
    tableNames.push(match[1].toLowerCase())
  }
  return tableNames
}
