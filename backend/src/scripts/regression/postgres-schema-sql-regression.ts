import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { buildPostgresSchemaSql, collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

const statements = collectPostgresSchemaStatements()
const sql = buildPostgresSchemaSql()
const goPublicAccountsMigration = readFileSync('../backend-go/db/migrations/000005_w1b_public_accounts.sql', 'utf8')
const schemaNames = new Set(statements.map((statement) => statement.schemaName))
const usageRecordsCreateSql = statements.find((statement) => statement.schemaName === 'juhe_usage' && /^CREATE TABLE IF NOT EXISTS usage_records\b/i.test(statement.sql))?.sql ?? ''

assert.ok(statements.length > 100, 'PostgreSQL schema 应从现有 SQLite DDL 收集到完整建表和索引语句')
assert.deepEqual(
  [...schemaNames].sort(),
  ['juhe_business', 'juhe_codex_context', 'juhe_dataset', 'juhe_stats', 'juhe_usage'].sort(),
  'PostgreSQL schema 应覆盖现有业务、数据集、使用记录、统计和 Responses 桥接状态索引存储边界'
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

for (const schemaName of schemaNames) {
  assert.match(sql, new RegExp(`CREATE SCHEMA IF NOT EXISTS "${schemaName}"`), `${schemaName} 应生成 CREATE SCHEMA`)
}

assert.match(sql, /CREATE TABLE IF NOT EXISTS system_accounts/, '应包含业务库 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS audit_logs/, '应包含数据集库 schema')
assert.match(sql, /audit_logs[\s\S]+model_mapping_applied integer NOT NULL DEFAULT 0[\s\S]+model_mapping_source text[\s\S]+source_endpoint_family text[\s\S]+upstream_endpoint_family text/, 'PG 审计日志必须包含模型映射可观测字段')
assert.match(sql, /audit_log_attempts[\s\S]+attempt_model_mapping_applied integer NOT NULL DEFAULT 0[\s\S]+attempt_model_mapping_source text[\s\S]+attempt_source_endpoint_family text[\s\S]+attempt_upstream_endpoint_family text/, 'PG 审计尝试表必须包含每次尝试的模型映射可观测字段')
assert.match(sql, /audit_logs[\s\S]+raw_payload_bytes bigint NOT NULL DEFAULT 0[\s\S]+compressed_payload_bytes bigint NOT NULL DEFAULT 0[\s\S]+compression_saved_bytes bigint NOT NULL DEFAULT 0/, 'PG 审计日志 payload 字节累计字段必须使用 bigint')
assert.match(sql, /audit_payload_blobs[\s\S]+raw_size_bytes bigint NOT NULL DEFAULT 0[\s\S]+compressed_size_bytes bigint NOT NULL DEFAULT 0/, 'PG 审计 payload blob 大小字段必须使用 bigint')
assert.match(sql, /public_api_logs[\s\S]+request_size_bytes bigint NOT NULL DEFAULT 0[\s\S]+response_size_bytes bigint NOT NULL DEFAULT 0/, 'PG 公开接口日志请求/响应大小字段必须使用 bigint')
assert.match(sql, /runtime_logs[\s\S]+log_offset bigint[\s\S]+line_number integer/, 'PG 运行日志文件 offset 必须使用 bigint，行号保留 integer')
assert.match(sql, /runtime_log_file_cursors[\s\S]+cursor_offset bigint NOT NULL DEFAULT 0[\s\S]+line_number integer NOT NULL DEFAULT 0[\s\S]+file_size bigint NOT NULL DEFAULT 0[\s\S]+file_mtime_ms bigint/, 'PG 运行日志游标 offset、文件大小和 mtime 必须使用 bigint')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_records/, '应包含使用记录主表 schema')
assert.match(usageRecordsCreateSql, /PRIMARY KEY \(created_at, id\)[\s\S]+\) PARTITION BY RANGE \(created_at\)/, 'PG 使用记录主表必须按 created_at 日范围分区，主键必须包含分区键')
assert.doesNotMatch(usageRecordsCreateSql, /\bid text PRIMARY KEY\b/, 'PG 使用记录分区父表不能保留只包含 id 的主键')
assert.match(sql, /usage_records[\s\S]+failure_attribution text/, '使用记录主表建表语句应直接包含失败归因字段')
assert.match(sql, /usage_records[\s\S]+model_mapping_applied integer NOT NULL DEFAULT 0[\s\S]+model_mapping_source text[\s\S]+source_endpoint_family text[\s\S]+upstream_endpoint_family text/, 'PG 使用记录主表必须包含模型映射可观测字段')
assert.match(sql, /usage_records[\s\S]+input_audio_tokens integer[\s\S]+output_audio_tokens integer[\s\S]+output_image_count integer/, '使用记录主表建表语句应包含音频 token 和输出图片数量字段')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_stats_totals/, '应包含统计库 schema')
assert.match(sql, /usage_stats_totals[\s\S]+request_count bigint NOT NULL DEFAULT 0[\s\S]+input_tokens bigint NOT NULL DEFAULT 0[\s\S]+duration_ms_sum bigint NOT NULL DEFAULT 0/, 'PG 统计累计字段必须使用 bigint，避免生产聚合溢出')
assert.match(sql, /usage_scope_range_windows[\s\S]+request_count bigint NOT NULL DEFAULT 0[\s\S]+first_token_ms_sum bigint NOT NULL DEFAULT 0/, 'PG 范围窗口累计字段必须使用 bigint')
assert.match(sql, /usage_scope_range_windows[\s\S]+window_key text GENERATED ALWAYS AS \(start_date \|\| ':' \|\| end_date\) STORED/, 'PG usage scope 范围窗口必须生成 window_key')
assert.match(sql, /CREATE TABLE IF NOT EXISTS usage_range_window_requests/, 'PG 统计库应包含按需范围窗口请求表')
assert.match(sql, /usage_range_window_requests[\s\S]+window_key text GENERATED ALWAYS AS \(start_date \|\| ':' \|\| end_date\) STORED/, 'PG 按需范围窗口请求表必须生成 window_key')
assert.match(sql, /CREATE TABLE IF NOT EXISTS data_archive_manifests/, 'PG 统计库应包含数据归档 manifest 表')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_data_archive_manifests_domain_time/, 'PG 数据归档 manifest 应支持按域查看历史')
assert.match(sql, /CREATE TABLE IF NOT EXISTS codex_context_sessions/, '应包含 Responses 桥接状态索引 schema')
assert.match(sql, /codex_context_sessions[\s\S]+storage_offset_bytes bigint NOT NULL[\s\S]+raw_size_bytes bigint NOT NULL[\s\S]+compressed_size_bytes bigint NOT NULL/, 'PG Responses 桥接状态文件 offset/大小字段必须使用 bigint')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategies/, '应包含策略路由表 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS route_strategy_groups/, '应包含策略路由分组绑定表 schema')
assert.match(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+default_test_model text/, 'AI 账户新建 schema 应直接包含账户级默认测试模型字段')
assert.doesNotMatch(sql, /CREATE TABLE IF NOT EXISTS accounts[\s\S]+last_successful_test_model text/, 'AI 账户新建 schema 不应继续包含旧的最后成功测试模型字段')
assert.match(goPublicAccountsMigration, /CREATE TABLE IF NOT EXISTS juhe_business\.accounts[\s\S]+default_test_model text/, 'Go 公开账户 baseline 应包含账户级默认测试模型字段')
assert.doesNotMatch(goPublicAccountsMigration, /CREATE TABLE IF NOT EXISTS juhe_business\.accounts[\s\S]+last_successful_test_model text/, 'Go 公开账户 baseline 不应包含旧的最后成功测试模型字段')
assert.match(sql, /route_strategy_id text NOT NULL/, 'api_keys 建表语句应强制绑定 route_strategy_id')
assert.match(sql, /api_keys[\s\S]+is_default integer NOT NULL DEFAULT 0/, 'api_keys 建表语句应包含默认 API Key 标识')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique ON api_keys\(route_strategy_id\) WHERE is_default = 1/, '默认 API Key 应按路由策略保持唯一')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_default_updated ON api_keys\(system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC\)/, 'API Key 按默认项置顶的账户列表排序应有匹配索引')
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
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_audit_logs_system_trace_c_created_sort ON audit_logs\(system_account_id, \(trace_id COLLATE "C"\), created_at DESC, id DESC\)/, 'PG 审计日志用户范围 trace 前缀查询必须有 owner + C collation 前缀索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_audit_logs_system_client_ip_c_created_sort ON audit_logs\(system_account_id, \(client_ip COLLATE "C"\), created_at DESC, id DESC\)/, 'PG 审计日志用户范围 client IP 前缀查询必须有 owner + C collation 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_audit_logs_trace_c_created_sort\b/, 'PG 审计日志不应再创建全局 trace 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_audit_logs_client_ip_c_created_sort\b/, 'PG 审计日志不应再创建全局 client IP 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_public_api_logs_trace_c_created_sort\b/, 'PG 公开接口日志不应再创建全局 trace 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_public_api_logs_client_ip_c_created_sort\b/, 'PG 公开接口日志不应再创建全局 client IP 前缀索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_runtime_logs_trace_c_time ON runtime_logs\(\(trace_id COLLATE "C"\), time DESC, id DESC\)/, 'PG 运行日志 trace 前缀查询必须有 C collation 前缀索引')
assert.match(sql, /CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_c_created ON operation_logs\(\(trace_id COLLATE "C"\), created_at DESC, id DESC\)/, 'PG 操作日志 trace 前缀查询必须有 C collation 前缀索引')
assert.doesNotMatch(sql, /CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_system_traffic_created_sort\b/, 'PG 使用记录列表不应再依赖目录表来源索引')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups\(system_account_id, provider_code\) WHERE is_default = 1/, '默认分组应按 provider_code 保持唯一')
assert.doesNotMatch(sql, /idx_groups_owner_protocol_profile_default_unique|groups\(system_account_id, provider_protocol_profile_id\)/, '默认分组不应再按 provider protocol profile 限制唯一')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique\s+ON custom_provider_models\(provider_code, system_account_id, model\)\s+WHERE scope = 'personal'/, '自定义模型唯一索引应保留模型 ID 大小写语义')
assert.match(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique\s+ON custom_provider_models\(provider_code, model\)\s+WHERE scope = 'global'/, '全局自定义模型应按 provider + model 保持唯一')
assert.match(sql, /scope IN \('personal', 'global'\)/, '自定义模型 scope 应支持个人和全局作用域')
assert.doesNotMatch(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique_lower[\s\S]+lower\(model\)/, '自定义模型不应再用 lower(model) 限制唯一')
assert.doesNotMatch(sql, /idx_provider_default_test_models_model\s+ON provider_default_test_models\(provider_code, lower\(model\), system_account_id\)/, '默认测试模型索引不应再折叠模型 ID 大小写')
assert.doesNotMatch(sql, /CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_default_unique ON route_strategies\(system_account_id\) WHERE is_default = 1/, '默认策略路由不应再限制为每个系统账户唯一')
assert.doesNotMatch(sql, /client_profile text NOT NULL DEFAULT 'auto'/, 'api_keys 建表语句不应再包含 client_profile 字段')
assert.doesNotMatch(sql, /explicit_hybrid_route_rules_json text/, 'api_keys 建表语句不应再包含 explicit_hybrid_route_rules_json 字段')
assert.match(sql, /container_id text/, 'openai_compatible_files 建表语句应包含 container_id 字段')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS route_strategy_id text/, 'PostgreSQL schema 不应再为 api_keys.route_strategy_id 生成历史补列语句')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS client_profile text NOT NULL DEFAULT 'auto'/, 'PostgreSQL schema 不应再补 client_profile')
assert.doesNotMatch(sql, /ALTER TABLE IF EXISTS api_keys ADD COLUMN IF NOT EXISTS explicit_hybrid_route_rules_json text/, 'PostgreSQL schema 不应再补 explicit_hybrid_route_rules_json')
assert.doesNotMatch(sql, /ALTER TABLE openai_compatible_files ADD COLUMN container_id\b/, 'PostgreSQL schema 不应重复为 openai_compatible_files.container_id 补列')
assert.doesNotMatch(sql, /\bALTER TABLE\b[\s\S]+\bADD COLUMN\b/i, 'PostgreSQL schema 不应包含运行时补列语句')
assert.match(sql, /DROP INDEX IF EXISTS idx_usage_record_shard_entries_trace_c_created_sort/, 'PostgreSQL schema 应清理旧 usage catalog trace 索引')

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
