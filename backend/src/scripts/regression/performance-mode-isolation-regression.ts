import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

const apiKeyRoutesSource = source('../../modules/api-keys/api-keys.routes.ts')
const serverSource = source('../../server.ts')
assert.match(serverSource, /runtimeConfig\.runtimeMode === 'performance'[\s\S]*background_worker_waiting_for_db_service_ready/, '高性能模式 DB service 未 ready 时不能兜底启动后台 worker')

assert.match(apiKeyRoutesSource, /if \(deleteResult\.cleanupTarget\) \{\s*submitApiKeyRelatedCleanup\(deleteResult\.cleanupTarget\)/, '高性能模式删除 API Key 后也必须投递关联记录清理任务')
assert.doesNotMatch(apiKeyRoutesSource, /cleanupTarget\s*&&\s*runtimeConfig\.databaseDriver\s*!==\s*'postgres'/, 'API Key 删除路由不能在 PostgreSQL 模式跳过清理投递')

const apiKeyCleanupServiceSource = source('../../modules/api-keys/api-key-cleanup.service.ts')
assert.doesNotMatch(apiKeyCleanupServiceSource, /postgres_record_cleanup_not_supported|api_key_related_cleanup_postgres_deferred/, 'API Key 清理提交不能保留 PostgreSQL 不支持分支')
assert.match(apiKeyCleanupServiceSource, /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]*registerDeletedApiKeyRecordCleanupTarget/, '单机模式才登记 SQLite API Key 清理目标')

const accountCleanupServiceSource = source('../../modules/accounts/account-cleanup.service.ts')
assert.match(accountCleanupServiceSource, /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]*registerDeletedAccountRecordCleanupTarget/, '单机模式才登记 SQLite AI 账户清理目标')

const recordMaintenanceSource = source('../../modules/record-maintenance/record-maintenance-queue.service.ts')
assert.match(recordMaintenanceSource, /cleanupProcessedUsageRecordsBeforeWithResultAsync/, '数据维护 usage 清理必须调用 PG-aware 异步入口')
assert.doesNotMatch(recordMaintenanceSource, /cleanupProcessedUsageRecordsBeforeWithResult\(/, '数据维护队列不能直接调用同步 SQLite usage 清理入口')

const dataRetentionCleanupSource = source('../../modules/background/data-retention-cleanup.service.ts')
assert.match(dataRetentionCleanupSource, /cleanupProcessedUsageRecordsBeforeWithResultAsync/, '数据保留 worker usage 清理必须调用 PG-aware 异步入口')
assert.doesNotMatch(dataRetentionCleanupSource, /cleanupProcessedUsageRecordsBeforeWithResult\(/, '数据保留 worker 不能直接调用同步 SQLite usage 清理入口')
assert.doesNotMatch(dataRetentionCleanupSource, /data_retention_cleanup_skipped_postgres_mode/, '高性能模式数据保留 worker 不能静默跳过')
assert.match(dataRetentionCleanupSource, /runtimeConfig\.databaseDriver === 'postgres'[\s\S]*throw new Error/, '高性能模式数据保留 worker 不能返回空清理结果，必须 fail-fast')

const dataRetentionHardCleanupSource = source('../../storage/data-retention-hard-cleanup.ts')
assert.doesNotMatch(dataRetentionHardCleanupSource, /sqlite_schema|discoverHardCleanupTableRules|hardCleanupRuleForTable|hardCleanupPreferredRuleByTable/, '非业务硬清理不能运行时探测 SQLite schema 后只清理已发现表')
assert.match(dataRetentionHardCleanupSource, /nonBusinessCleanupTablesByRole/, '非业务硬清理必须按当前 schema 固定清单执行')

const backgroundStatsWriterSource = source('../../modules/background/background-stats-writer.ts')
assert.doesNotMatch(backgroundStatsWriterSource, /skippedPostgres|background_stats_writer_postgres_operation_skipped|高性能模式暂跳过|return \[\]/, 'PG 模式 stats-writer 不能对未实现统计维护操作静默跳过或返回模拟成功结果')
assert.match(backgroundStatsWriterSource, /case 'refresh_account_quality':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*refreshAccountQualityAsync\(operation\.windowMinutes, operation\.failureCandidateLimit\)/, 'PG 模式账户质量刷新必须走 PostgreSQL async 入口，不能回落 SQLite 或 fail-fast')
assert.match(backgroundStatsWriterSource, /case 'check_usage_stats_consistency':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*checkUsageStatsConsistencyAsync\(operation\.limit\)/, 'PG 模式统计一致性校验必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'collect_table_storage_snapshot':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*collectTableStorageSnapshotAsync\(operation\.sampledAt, operation\.options\)/, 'PG 模式表容量采样必须走 PostgreSQL async 入口，不能回落 SQLite')
assert.match(backgroundStatsWriterSource, /case 'cleanup_usage_stats_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupUsageStatsBucketsBeforeAsync\(operation\.input\)/, 'PG 模式统计保留清理必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'cleanup_system_metrics_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupSystemMetricsBeforeAsync\(operation\.input\)/, 'PG 模式系统指标保留清理必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'cleanup_table_storage_snapshots_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupTableStorageSnapshotsBeforeAsync\(operation\.cutoffIso, operation\.limit\)/, 'PG 模式表容量快照保留清理必须走 PostgreSQL async 入口，不能回落 SQLite')
assert.match(backgroundStatsWriterSource, /case 'upsert_account_usage_snapshots':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*upsertAccountUsageSnapshotsAsync/, 'PG 模式账号用量快照必须写入 PostgreSQL stats 表，不能回落到 SQLite 快照实现')

const sqliteDatabaseSource = source('../../storage/database.ts')
assert.doesNotMatch(sqliteDatabaseSource, /import\s+\{[^}]*DatabaseSync[^}]*\}\s+from ['"]node:sqlite['"]/, '高性能模式可导入的 database.ts 不能顶层加载 node:sqlite')
assert.match(sqliteDatabaseSource, /require\('node:sqlite'\)/, 'database.ts 必须只在实际打开 SQLite 时懒加载 node:sqlite')

const usageRecordShardSource = source('../../storage/usage-record-shards.ts')
assert.doesNotMatch(usageRecordShardSource, /import\s+\{[^}]*DatabaseSync[^}]*\}\s+from ['"]node:sqlite['"]/, '高性能模式可导入的 usage shard 模块不能顶层加载 node:sqlite')
assert.match(usageRecordShardSource, /require\('node:sqlite'\)/, 'usage shard 必须只在实际打开 SQLite 分片库时懒加载 node:sqlite')
assert.match(usageRecordShardSource, /function assertSqliteUsageRecordShardAccess[\s\S]*runtimeConfig\.databaseDriver !== 'sqlite'[\s\S]*juhe_usage\.usage_records/, '使用记录 SQLite 分片入口必须在 PostgreSQL 模式 fail-fast')
assert.match(usageRecordShardSource, /export function getUsageRecordShardDatabase[\s\S]*assertSqliteUsageRecordShardAccess\('getUsageRecordShardDatabase'\)/, 'PG 模式不能直接打开 SQLite usage shard')
assert.match(usageRecordShardSource, /export function writeUsageRecordShardRows[\s\S]*assertSqliteUsageRecordShardAccess\('writeUsageRecordShardRows'\)/, 'PG 模式不能写 SQLite usage shard')
assert.match(usageRecordShardSource, /function usageRecordShardDatabaseIfOpenOrExists[\s\S]*assertSqliteUsageRecordShardAccess\('usageRecordShardDatabaseIfOpenOrExists'\)/, 'PG 模式不能通过存在性检查打开 SQLite usage shard')

const dbServiceHandlersSource = source('../../modules/db-service/db-service-handlers.ts')
assert.doesNotMatch(dbServiceHandlersSource, /assertCodexContextStateSqliteOnlyOperation|PostgreSQL 模式暂未接入 Codex context state/, 'Codex context state 已接入 PostgreSQL，不能保留旧 fail-fast 断言或文案')
assert.match(dbServiceHandlersSource, /case 'save_codex_context_response_state':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*saveCodexContextResponseStateIndexAsync\(operation\.input\)[\s\S]*saveCodexContextResponseStateIndexWithWriterPool\(operation\.input\)/, 'PG 模式 Codex context response state 必须走 PostgreSQL async 入口，SQLite 才能走 writer pool')
assert.match(dbServiceHandlersSource, /case 'cleanup_expired_codex_context_states':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupExpiredCodexContextStatesAsync\(\{[\s\S]*expiredBefore: operation\.expiredBefore[\s\S]*limit: operation\.limit[\s\S]*cleanupExpiredCodexContextStatesWithWriterPool\(\{[\s\S]*expiredBefore: operation\.expiredBefore[\s\S]*limit: operation\.limit/, 'PG 模式 Codex context cleanup 必须走 PostgreSQL async 入口，SQLite 才能走 writer pool')
assert.match(dbServiceHandlersSource, /case 'persist_openai_codex_usage_headers':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*persistOpenAICodexUsageHeaders/, 'PG 模式 Codex usage headers 必须有显式分支，不能隐式落入同步 handler')

const codexWriterPoolSource = source('../../storage/codex-context-state-writer-pool.ts')
assert.match(codexWriterPoolSource, /codexContextStateWriterPoolEnabled\(\)[\s\S]*runtimeConfig\.databaseDriver === 'sqlite'/, 'Codex context SQLite writer pool 只能在 SQLite 模式启用')

const usageRecordWriterPoolSource = source('../../storage/usage-record-writer-pool.ts')
assert.match(usageRecordWriterPoolSource, /usageRecordWriterPoolEnabled\(\)[\s\S]*runtimeConfig\.databaseDriver === 'sqlite'/, 'usage record SQLite writer pool 只能在 SQLite 模式启用')

for (const filePath of sourceFiles(new URL('../..', import.meta.url))) {
  const fileSource = readFileSync(filePath, 'utf8')
  assert.doesNotMatch(fileSource, /ANY\(\?\)/, `${filePath} 中 PostgreSQL 数组参数必须写明 cast，例如 ANY(?::text[])`)
}

const accountRecordCleanupSource = source('../../storage/account-record-cleanup.ts')
assert.match(accountRecordCleanupSource, /cleanupDeletedAccountRelatedRecordDataCorePostgresAsync/, 'AI 账户记录清理必须提供 PostgreSQL 实现')
assert.match(accountRecordCleanupSource, /juhe_dataset\.account_record_cleanup_targets/, 'AI 账户清理目标必须落 PostgreSQL dataset schema')
assert.match(accountRecordCleanupSource, /juhe_usage\.usage_records/, 'AI 账户清理必须处理 PostgreSQL usage 记录')
assert.match(accountRecordCleanupSource, /juhe_stats\./, 'AI 账户清理必须处理 PostgreSQL stats 记录')
assert.match(accountRecordCleanupSource, /assertSqliteAccountRecordCleanup/, 'AI 账户同步清理入口必须在 PostgreSQL 模式 fail-fast')

const apiKeyRecordCleanupSource = source('../../storage/api-key-record-cleanup.ts')
assert.match(apiKeyRecordCleanupSource, /cleanupDeletedApiKeyRelatedRecordDataCorePostgresAsync/, 'API Key 记录清理必须提供 PostgreSQL 实现')
assert.match(apiKeyRecordCleanupSource, /juhe_dataset\.api_key_record_cleanup_targets/, 'API Key 清理目标必须落 PostgreSQL dataset schema')
assert.match(apiKeyRecordCleanupSource, /juhe_usage\.usage_records/, 'API Key 清理必须处理 PostgreSQL usage 记录')
assert.match(apiKeyRecordCleanupSource, /juhe_stats\./, 'API Key 清理必须处理 PostgreSQL stats 记录')
assert.match(apiKeyRecordCleanupSource, /assertSqliteApiKeyRecordCleanup/, 'API Key 同步清理入口必须在 PostgreSQL 模式 fail-fast')

const auditPayloadSource = source('../../storage/audit-log-payload-blobs.ts')
assert.match(auditPayloadSource, /cleanupUnreferencedAuditPayloadBlobsPostgresAsync/, '审计 payload blob 清理必须提供 PostgreSQL 异步实现')
assert.match(auditPayloadSource, /cleanupAuditPayloadBlobsBeforePostgresAsync/, '审计 payload blob 保留清理必须提供 PostgreSQL 异步实现')
assert.match(auditPayloadSource, /assertSqliteAuditPayloadBlobCleanup/, '审计 payload blob 同步清理入口必须在 PostgreSQL 模式 fail-fast')

const auditRetentionSource = source('../../storage/audit-log-retention.repository.ts')
assert.match(auditRetentionSource, /deleteAuditLogsByWhereAsync/, '审计日志保留清理必须提供 PostgreSQL 异步删除实现')
assert.match(auditRetentionSource, /cleanupAuditErrorGroupsBeforeAsync/, '审计错误分组保留清理必须提供 PostgreSQL 异步删除实现')
assert.match(auditRetentionSource, /assertSqliteAuditLogRetention/, '审计日志同步保留清理入口必须在 PostgreSQL 模式 fail-fast')

console.log('performance-mode-isolation-regression passed')

function sourceFiles(root: URL): string[] {
  const paths: string[] = []
  for (const entry of readdirSync(root)) {
    if (entry === 'dist' || entry === 'node_modules') continue
    const entryUrl = new URL(`${entry}`, root)
    const fullPath = entryUrl.pathname
    const stat = statSync(fullPath)
    if (stat.isDirectory()) {
      paths.push(...sourceFiles(new URL(`${entry}/`, root)))
      continue
    }
    if (entry.endsWith('.ts')) {
      paths.push(fullPath)
    }
  }
  return paths
}
