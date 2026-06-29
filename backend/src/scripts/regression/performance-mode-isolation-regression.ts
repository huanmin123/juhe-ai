import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

const apiKeyRoutesSource = source('../../modules/api-keys/api-keys.routes.ts')
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
assert.match(dataRetentionCleanupSource, /data_retention_cleanup_skipped_postgres_mode/, '高性能模式必须跳过单机数据保留 worker，避免误触发 SQLite 清理链路')

const sqliteDatabaseSource = source('../../storage/database.ts')
assert.doesNotMatch(sqliteDatabaseSource, /import\s+\{[^}]*DatabaseSync[^}]*\}\s+from ['"]node:sqlite['"]/, '高性能模式可导入的 database.ts 不能顶层加载 node:sqlite')
assert.match(sqliteDatabaseSource, /require\('node:sqlite'\)/, 'database.ts 必须只在实际打开 SQLite 时懒加载 node:sqlite')

const usageRecordShardSource = source('../../storage/usage-record-shards.ts')
assert.doesNotMatch(usageRecordShardSource, /import\s+\{[^}]*DatabaseSync[^}]*\}\s+from ['"]node:sqlite['"]/, '高性能模式可导入的 usage shard 模块不能顶层加载 node:sqlite')
assert.match(usageRecordShardSource, /require\('node:sqlite'\)/, 'usage shard 必须只在实际打开 SQLite 分片库时懒加载 node:sqlite')

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
