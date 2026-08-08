import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

const apiKeyRoutesSource = source('../../modules/api-keys/api-keys.routes.ts')
const serverSource = source('../../server.ts')
assert.match(serverSource, /runtimeConfig\.runtimeMode === 'performance'[\s\S]*background_worker_waiting_for_db_service_ready/, '高性能模式 DB service 未 ready 时不能兜底启动后台 worker')

assert.match(apiKeyRoutesSource, /if \(deleteResult\.cleanupTarget\) \{\s*await submitApiKeyRelatedCleanupAsync\(deleteResult\.cleanupTarget\)/, '高性能模式删除 API Key 后也必须等待投递关联记录清理任务')
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
const maintenanceCleanupJobsSource = source('../../modules/background/maintenance-cleanup-jobs.ts')
assert.match(maintenanceCleanupJobsSource, /runDataRetentionCleanup\([^)]*\)[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*enqueuePostgresDataRetentionMaintenanceJobs/, 'PG 高性能 data-retention 定时入口必须投递 record-maintenance 任务，不能直接跑单机清理链路')
assert.match(maintenanceCleanupJobsSource, /enqueuePostgresDataRetentionMaintenanceJobs[\s\S]*getSettingsAsync[\s\S]*readAuditLogSettings\(\)[\s\S]*enqueueRecordMaintenanceJobAsync\(\{[\s\S]*type: 'usage_records_cleanup'[\s\S]*enqueueRecordMaintenanceJobAsync\(\{[\s\S]*type: 'audit_retained_data_cleanup'[\s\S]*successHotRetentionHours[\s\S]*successSampleBucketThreshold/, 'PG 高性能 data-retention 必须分别按系统设置和审计设置投递 usage 与审计保留维护任务')
assert.match(maintenanceCleanupJobsSource, /cleanupPostgresDatasetRetainedData[\s\S]*cleanupOperationLogsBeforeAsync[\s\S]*cleanupPublicApiLogsBeforeAsync[\s\S]*cleanupModelCheckRunsBeforeAsync/, 'PG 高性能 data-retention 必须按保留设置清理非审计 dataset 日志和模型检测历史')
assert.doesNotMatch(maintenanceCleanupJobsSource, /enqueuePostgresDataRetentionMaintenanceJobs[\s\S]*type: 'non_business_data_cleanup'/, 'PG 高性能定时保留清理不能用 usage cutoff 投递通用非业务硬清理，避免误删审计数据')
assert.doesNotMatch(source('../../modules/background/background-jobs.ts'), /if \(!isPostgresHighPerformanceMode\(\)\) \{[\s\S]*backgroundScheduledJobName\('data-retention-cleanup'\)/, 'PG 高性能 ingest-worker 不能跳过 data-retention-cleanup 调度')

const dataRetentionHardCleanupSource = source('../../storage/data-retention-hard-cleanup.ts')
assert.doesNotMatch(dataRetentionHardCleanupSource, /sqlite_schema|discoverHardCleanupTableRules|hardCleanupRuleForTable|hardCleanupPreferredRuleByTable/, '非业务硬清理不能运行时探测 SQLite schema 后只清理已发现表')
assert.match(dataRetentionHardCleanupSource, /nonBusinessCleanupTablesByRole/, '非业务硬清理必须按当前 schema 固定清单执行')
assert.doesNotMatch(dataRetentionHardCleanupSource, /tableName: 'audit_(payload_refs|log_attempts|logs|error_groups)'/, '通用非业务硬清理不能包含审计主表，审计只能按专用保留策略清理')
assert.doesNotMatch(dataRetentionHardCleanupSource, /tableName: 'stats_job_state'/, '非业务硬清理不能删除 stats_job_state，避免聚合游标丢失后重复累计')
assert.doesNotMatch(dataRetentionHardCleanupSource, /tableName: 'client_ip_policies'/, '非业务硬清理不能删除客户端 IP 策略配置')
assert.match(dataRetentionHardCleanupSource, /client_ip_range_window_dirty_ips[\s\S]*client_ip_account_stats_daily[\s\S]*client_ip_account_usage_range_windows[\s\S]*client_ip_account_range_window_dirty_ips/, '非业务硬清理必须覆盖全局与账号维度 client IP 统计派生表')
assert.match(dataRetentionHardCleanupSource, /hardCleanupCutoffsAsync[\s\S]*usageStatsTimezoneAsync/, 'PG 非业务硬清理 cutoff 必须通过异步统计时区读取，不能打开 SQLite business DB')
const dataRetentionRepositorySource = source('../../storage/data-retention.repository.ts')
assert.match(dataRetentionRepositorySource, /cleanupNonBusinessDataBeforeWithResultPostgres[\s\S]*await hardCleanupCutoffsAsync\(input\.cutoffAt\)/, 'PG 非业务硬清理必须使用 hardCleanupCutoffsAsync')
assert.doesNotMatch(dataRetentionRepositorySource, /tableName: 'audit_(payload_refs|log_attempts|logs|error_groups)'/, 'PG 通用非业务硬清理不能包含审计主表，审计只能按专用保留策略清理')
assert.doesNotMatch(dataRetentionRepositorySource, /tableName: 'stats_job_state'/, 'PG 通用非业务硬清理不能删除 stats_job_state，避免聚合游标丢失后重复累计')
assert.doesNotMatch(dataRetentionRepositorySource, /tableName: 'client_ip_policies'/, 'PG 通用非业务硬清理不能删除客户端 IP 策略配置')
assert.match(dataRetentionRepositorySource, /cleanupNonBusinessDataBeforeWithResult[\s\S]*cleanupProcessedUsageRecordsBeforeWithResult\(cutoffs\.iso, batchLimit\)/, 'SQLite 非业务硬清理清 usage 明细也必须等待统计安全游标')
assert.match(dataRetentionRepositorySource, /clientIpRangeWindowDirtyIps[\s\S]*client_ip_range_window_dirty_ips[\s\S]*clientIpAccountStatsDaily[\s\S]*client_ip_account_stats_daily[\s\S]*clientIpAccountUsageRangeWindows[\s\S]*client_ip_account_usage_range_windows[\s\S]*clientIpAccountRangeWindowDirtyIps[\s\S]*client_ip_account_range_window_dirty_ips/, '常规统计保留清理必须覆盖全局与账号维度 client IP 表')

const backgroundStatsWriterSource = source('../../modules/background/background-stats-writer.ts')
assert.doesNotMatch(backgroundStatsWriterSource, /skippedPostgres|background_stats_writer_postgres_operation_skipped|高性能模式暂跳过|return \[\]/, 'PG 模式 stats-writer 不能对未实现统计维护操作静默跳过或返回模拟成功结果')
assert.match(backgroundStatsWriterSource, /case 'refresh_account_quality':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*refreshAccountQualityAsync\(operation\.windowMinutes, operation\.failureCandidateLimit, operation\.failureCandidateOffset/, 'PG 模式账户质量刷新必须走 PostgreSQL async 入口并传递公平分页游标，不能回落 SQLite 或 fail-fast')
assert.match(backgroundStatsWriterSource, /case 'check_usage_stats_consistency':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*checkUsageStatsConsistencyAsync\(operation\.limit\)/, 'PG 模式统计一致性校验必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'collect_table_storage_snapshot':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*collectTableStorageSnapshotAsync\(operation\.sampledAt, operation\.options, requiredPostgresScheduledLease\(operation\)\)/, 'PG 模式表容量采样必须走 PostgreSQL async 入口并传递调度租约，不能回落 SQLite')
assert.match(backgroundStatsWriterSource, /case 'cleanup_usage_stats_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupUsageStatsBucketsBeforeAsync\(operation\.input\)/, 'PG 模式统计保留清理必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'cleanup_system_metrics_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupSystemMetricsBeforeAsync\(operation\.input\)/, 'PG 模式系统指标保留清理必须走 PostgreSQL async 入口')
assert.match(backgroundStatsWriterSource, /case 'cleanup_table_storage_snapshots_retention':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupTableStorageSnapshotsBeforeAsync\(operation\.cutoffIso, operation\.limit\)/, 'PG 模式表容量快照保留清理必须走 PostgreSQL async 入口，不能回落 SQLite')
assert.match(backgroundStatsWriterSource, /case 'upsert_account_usage_snapshots':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*upsertAccountUsageSnapshotsAsync/, 'PG 模式账号用量快照必须写入 PostgreSQL stats 表，不能回落到 SQLite 快照实现')

const usageStatsRepositorySource = source('../../storage/usage-stats.repository.ts')
assert.match(usageStatsRepositorySource, /export async function subtractPostgresUsageStatsRows/, 'PG 记录清理删除 usage 明细前必须提供 PostgreSQL 统计扣减入口')
assert.match(usageStatsRepositorySource, /subtractPostgresUsageStatsRows[\s\S]*subtractAuthorizationUsageReportRowsAsync[\s\S]*subtractPostgresUsageStatsTotals[\s\S]*subtractPostgresUsageLatencyEntries[\s\S]*subtractPostgresUsageModelEntries[\s\S]*subtractPostgresUsageErrorEntries[\s\S]*subtractPostgresAccountQualityEntries/, 'PG 统计扣减必须覆盖主统计、授权日报、延迟、模型、错误和账户质量派生表')
assert.match(usageStatsRepositorySource, /function postgresStatsJobState[\s\S]*ensurePostgresStatsJobStateRow[\s\S]*FOR UPDATE/, 'PG usage_stats_aggregation 全局 cursor 必须加行级锁，避免多 worker 重复聚合同一批 usage')
assert.match(source('../../storage/client-ip-stats-aggregation.repository.ts'), /function postgresClientIpStatsJobState[\s\S]*ensurePostgresClientIpStatsJobStateRow[\s\S]*FOR UPDATE/, 'PG client_ip_stats_aggregation 全局 cursor 必须加行级锁，避免多 worker 重复聚合同一批 usage')

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
assert.doesNotMatch(dbServiceHandlersSource, /assertCodexContextStateSqliteOnlyOperation|PostgreSQL 模式暂未接入 Codex context state/, 'Responses 桥接状态索引已接入 PostgreSQL，不能保留旧 fail-fast 断言或文案')
assert.match(dbServiceHandlersSource, /case 'save_codex_context_response_state':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*saveCodexContextResponseStateIndexAsync\(operation\.input\)[\s\S]*saveCodexContextResponseStateIndexWithWriterPool\(operation\.input\)/, 'PG 模式 Responses 桥接 response 状态必须走 PostgreSQL async 入口，SQLite 才能走 writer pool')
assert.match(dbServiceHandlersSource, /case 'cleanup_expired_codex_context_states':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*cleanupExpiredCodexContextStatesAsync\(\{[\s\S]*expiredBefore: operation\.expiredBefore[\s\S]*limit: operation\.limit[\s\S]*cleanupExpiredCodexContextStatesWithWriterPool\(\{[\s\S]*expiredBefore: operation\.expiredBefore[\s\S]*limit: operation\.limit/, 'PG 模式 Responses 桥接状态清理必须走 PostgreSQL async 入口，SQLite 才能走 writer pool')
assert.match(dbServiceHandlersSource, /case 'persist_openai_codex_usage_headers':[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*persistOpenAICodexUsageHeaders/, 'PG 模式 Codex usage headers 必须有显式分支，不能隐式落入同步 handler')

const codexWriterPoolSource = source('../../storage/codex-context-state-writer-pool.ts')
assert.match(codexWriterPoolSource, /codexContextStateWriterPoolEnabled\(\)[\s\S]*runtimeConfig\.databaseDriver === 'sqlite'/, 'Responses 桥接状态 SQLite writer pool 只能在 SQLite 模式启用')

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
assert.match(accountRecordCleanupSource, /deletePostgresAccountUsageDataBatch[\s\S]*SELECT \$\{USAGE_STATS_RECORD_SELECT_COLUMNS\}[\s\S]*subtractPostgresAccountUsageRowsOnce[\s\S]*subtractPostgresUsageStatsRows[\s\S]*markPostgresUsageCleanupRowsDeleted/, 'PG AI 账户记录清理删除 usage 前必须读取完整行、幂等扣减统计并标记 deletion deduction')
assert.match(accountRecordCleanupSource, /deletePostgresAccountUsageDataBatch[\s\S]*await client\.transaction\(async \(tx\) => \{[\s\S]*subtractPostgresAccountUsageRowsOnce\(tx[\s\S]*deletePostgresUsageRecordCatalogRowsByUsageIds\(tx[\s\S]*markPostgresUsageCleanupRowsDeleted\(tx/, 'PG AI 账户 usage 删除、统计扣减和 deduction 标记必须在同一个事务里完成')
assert.match(accountRecordCleanupSource, /SELECT stats_subtracted_at[\s\S]*FOR UPDATE/, 'PG AI 账户记录清理必须锁定 deduction 行，避免并发重复扣减统计')
assert.match(accountRecordCleanupSource, /assertSqliteAccountRecordCleanup/, 'AI 账户同步清理入口必须在 PostgreSQL 模式 fail-fast')

const apiKeyRecordCleanupSource = source('../../storage/api-key-record-cleanup.ts')
assert.match(apiKeyRecordCleanupSource, /cleanupDeletedApiKeyRelatedRecordDataCorePostgresAsync/, 'API Key 记录清理必须提供 PostgreSQL 实现')
assert.match(apiKeyRecordCleanupSource, /juhe_dataset\.api_key_record_cleanup_targets/, 'API Key 清理目标必须落 PostgreSQL dataset schema')
assert.match(apiKeyRecordCleanupSource, /juhe_usage\.usage_records/, 'API Key 清理必须处理 PostgreSQL usage 记录')
assert.match(apiKeyRecordCleanupSource, /juhe_stats\./, 'API Key 清理必须处理 PostgreSQL stats 记录')
assert.match(apiKeyRecordCleanupSource, /deletePostgresApiKeyUsageDataBatch[\s\S]*SELECT \$\{USAGE_STATS_RECORD_SELECT_COLUMNS\}[\s\S]*subtractPostgresApiKeyUsageRowsOnce[\s\S]*subtractPostgresUsageStatsRows[\s\S]*markPostgresUsageCleanupRowsDeleted/, 'PG API Key 记录清理删除 usage 前必须读取完整行、幂等扣减统计并标记 deletion deduction')
assert.match(apiKeyRecordCleanupSource, /deletePostgresApiKeyUsageDataBatch[\s\S]*await client\.transaction\(async \(tx\) => \{[\s\S]*subtractPostgresApiKeyUsageRowsOnce\(tx[\s\S]*deletePostgresUsageRecordCatalogRowsByUsageIds\(tx[\s\S]*markPostgresUsageCleanupRowsDeleted\(tx/, 'PG API Key usage 删除、统计扣减和 deduction 标记必须在同一个事务里完成')
assert.match(apiKeyRecordCleanupSource, /SELECT stats_subtracted_at[\s\S]*FOR UPDATE/, 'PG API Key 记录清理必须锁定 deduction 行，避免并发重复扣减统计')
assert.match(apiKeyRecordCleanupSource, /assertSqliteApiKeyRecordCleanup/, 'API Key 同步清理入口必须在 PostgreSQL 模式 fail-fast')

const auditPayloadSource = source('../../storage/audit-log-payload-blobs.ts')
assert.match(auditPayloadSource, /cleanupUnreferencedAuditPayloadBlobsPostgresAsync/, '审计 payload blob 清理必须提供 PostgreSQL 异步实现')
assert.match(auditPayloadSource, /cleanupAuditPayloadBlobsBeforePostgresAsync/, '审计 payload blob 保留清理必须提供 PostgreSQL 异步实现')
assert.match(auditPayloadSource, /listAuditPayloadBlobRowsBefore[\s\S]*NOT EXISTS[\s\S]*audit_payload_refs[\s\S]*headers_blob_id = b\.id OR r\.body_blob_id = b\.id/, '审计 payload 按时间清理时必须跳过仍被 audit_payload_refs 引用的 blob')
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
    const fullPath = fileURLToPath(entryUrl)
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
