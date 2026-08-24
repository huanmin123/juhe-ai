import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const usageStatsSource = source('src/storage/usage-stats.repository.ts')
const statsWriterSource = source('src/modules/background/background-stats-writer.ts')
const usageRangeWindowsSource = source('src/storage/usage-range-windows.repository.ts')
const apiKeyScheduleSyncSource = source('src/storage/api-key-schedule-status-sync.repository.ts')
const accountScheduleSyncSource = source('src/storage/account-availability-schedule-status-sync.repository.ts')
const resourceAuthorizationWriteSource = source('src/storage/resource-authorization-write.repository.ts')
const dbServiceHandlersSource = source('src/modules/db-service/db-service-handlers.ts')
const apiKeyQuotaServiceSource = source('src/modules/gateway/quota/api-key-quota.service.ts')
const authorizationQuotaServiceSource = source('src/modules/gateway/quota/authorization-quota.service.ts')
const requestQuotaCheckerSource = source('src/storage/request-quota-checker.ts')
const backgroundJobsSource = source('src/modules/background/background-jobs.ts')
const businessSchemaSource = source('src/storage/schema/business-schema.ts')
const routeStrategyAvailabilityGuardSource = source('src/storage/route-strategy-availability-guard.ts')
const groupWriteRepositorySource = source('src/storage/group-write.repository.ts')
const routeStrategyRepositorySource = source('src/storage/route-strategy.repository.ts')
const publicApiLogCaptureSource = source('src/modules/public-api-logs/public-api-log-capture.middleware.ts')
const runtimeLogsSource = source('src/storage/runtime-log-query.repository.ts')
const rebuildUsageStatsSource = source('src/scripts/maintenance/rebuild-usage-stats.ts')
const usageStatsRuntimeHelpersSource = source('src/storage/usage-stats-runtime-helpers.ts')

const watermarkSource = sourceBetween(usageStatsSource, 'function usageRankSnapshotSourceWatermark', 'function usageRankSnapshotRefreshJobState')
assert.doesNotMatch(watermarkSource, /COUNT\s*\(\s*\*\s*\)/i, '排行快照水位不能为了删除感知对统计表执行 COUNT(*)')
assert.doesNotMatch(usageStatsSource, /deletionAwareSourceTables/, '排行快照 stage 不应保留基于全表计数的删除感知字段')
assert.doesNotMatch(usageRangeWindowsSource, /rangeWindowSourceWatermarkRowCount/, '范围窗口刷新不应继续解析旧 rowCount 水位')
assert.match(
  rebuildUsageStatsSource,
  /runtimeConfig\.databaseDriver === 'postgres'[\s\S]*resetUsageStatsCacheAsync\(\)[\s\S]*aggregateUsageStatsBatchAsync\(options\.batchSize\)/,
  '用量统计重建脚本在 PostgreSQL 模式必须清空 juhe_stats 并从 juhe_usage 异步重建，不能回落 SQLite usage shard'
)
assert.match(
  rebuildUsageStatsSource,
  /--confirm-offline[\s\S]*PostgreSQL 模式会清空 juhe_stats 派生表，从 juhe_usage\.usage_records 重建统计/,
  '用量统计重建脚本 help 必须说明 PostgreSQL 离线重建边界'
)
assert.match(
  usageStatsRuntimeHelpersSource,
  /latestUsageStatsLagSeconds\(\)[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*latestUsageStatsLagSecondsForRuntime/,
  '同步 stats lag helper 在 PostgreSQL 模式必须 fail-fast，避免请求路径回读 SQLite stats DB'
)

const aggregateUsageStatsSource = sourceBetween(statsWriterSource, 'async function aggregateUsageStats', 'async function aggregateClientIpStats')
assert.match(
  aggregateUsageStatsSource,
  /if\s*\(\s*processed\s*>\s*0\s*\)\s*\{[\s\S]*refreshUsageQuotaHourlyWindowsCache\(\)[\s\S]*sendGatewayQuotaSnapshotToServer/,
  '无新增用量记录时不应空跑 quota 小时窗口重建'
)

for (const [name, fileSource] of [
  ['API Key', apiKeyScheduleSyncSource],
  ['账户', accountScheduleSyncSource]
] as const) {
  assert.match(fileSource, /const availabilityScheduleStatusSyncBatchLimit = 500/, `${name} 时间计划同步每轮扫描必须有固定窗口上限`)
  assert.match(fileSource, /availability_schedule_next_check_at <= \?/, `${name} 时间计划同步必须只读取到期检查点`)
  assert.match(fileSource, /ORDER BY availability_schedule_next_check_at IS NOT NULL ASC, availability_schedule_next_check_at ASC, id ASC\s+LIMIT \?/s, `${name} 时间计划同步查询必须按 next_check_at/id 命中窗口索引`)
  assert.doesNotMatch(fileSource, /ScheduleStatusSyncCursor|updated_at > \?/, `${name} 时间计划同步不能使用滚动 updated_at 游标延迟边界切换`)
  assert.doesNotMatch(fileSource, /\.all\(\)\s+as unknown as Scheduled/, `${name} 时间计划同步不能无参数 .all() 拉取全部计划行`)
  assert.match(fileSource, /export async function sync.*AvailabilityScheduleStatusesAsync/, `${name} 时间计划同步必须提供 PostgreSQL async 入口`)
  assert.match(fileSource, /runtimeConfig\.databaseDriver !== 'postgres'[\s\S]+return sync.*AvailabilityScheduleStatuses\(now\)/, `${name} 时间计划 async 入口必须保留 SQLite standalone 分支`)
  assert.match(fileSource, /createPostgresDatabaseClient\(await getPostgresPool\(\)\)/, `${name} 时间计划 async 入口必须使用 PostgreSQL client`)
  assert.match(fileSource, /await client\.transaction\(async \(tx\) => \{[\s\S]+ON CONFLICT\(event_key\) DO NOTHING/, `${name} 时间计划 async 状态切换必须在事务内写事件去重`)
  assert.match(fileSource, /client\.dialect\.qualifyTable\(businessSchemaName, tableName\)/, `${name} 时间计划 async SQL 必须限定 juhe_business schema`)
}

const dbServiceDispatchSource = sourceBetween(dbServiceHandlersSource, 'async function handleDbServiceOperationDispatch', 'async function handleAccountTestTaskMaintenanceAsync')
assert.match(
  dbServiceDispatchSource,
  /case 'sync_api_key_availability_schedule_statuses': \{[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+await syncApiKeyAvailabilityScheduleStatusesAsync\(\)[\s\S]+return handleDbServiceOperationSync\(operation\)/,
  'PG 模式 DB service 必须使用 async API Key 时间计划同步，SQLite 模式才回退同步分支'
)
assert.match(
  dbServiceDispatchSource,
  /case 'sync_account_availability_schedule_statuses': \{[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+await syncAccountAvailabilityScheduleStatusesAsync\(\)[\s\S]+return handleDbServiceOperationSync\(operation\)/,
  'PG 模式 DB service 必须使用 async 账户时间计划同步，SQLite 模式才回退同步分支'
)
assert.match(
  resourceAuthorizationWriteSource,
  /export async function expireDueResourceAuthorizationsAsync[\s\S]+runtimeConfig\.databaseDriver !== 'postgres'[\s\S]+return expireDueResourceAuthorizations\(limit\)/,
  '资源授权过期扫描必须提供 PostgreSQL async 入口，并保留 SQLite standalone 分支'
)
assert.match(
  resourceAuthorizationWriteSource,
  /await client\.transaction\(async \(tx\) => \{[\s\S]+resource_authorization_grants[\s\S]+ORDER BY expires_at ASC, updated_at ASC, id ASC[\s\S]+LIMIT \?[\s\S]+FOR UPDATE SKIP LOCKED[\s\S]+await syncResourceAuthorizationGrantRuntimeAsync/,
  '资源授权过期 async 扫描必须在事务内按固定窗口锁定读取并同步运行态授权'
)
assert.match(
  resourceAuthorizationWriteSource,
  /client\.dialect\.qualifyTable\(businessSchemaName, tableName\)/,
  '资源授权过期 async SQL 必须限定 juhe_business schema'
)
assert.match(
  dbServiceDispatchSource,
  /case 'expire_due_resource_authorizations': \{[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+await expireDueResourceAuthorizationsAsync\(\)[\s\S]+return handleDbServiceOperationSync\(operation\)/,
  'PG 模式 DB service 必须使用 async 资源授权过期扫描，SQLite 模式才回退同步分支'
)
assert.match(
  dbServiceHandlersSource,
  /case 'check_api_key_quota':[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+checkGatewayApiKeyQuotaExactAsync/,
  'PG 模式 DB service API Key 额度检查必须使用 exact async 入口'
)
assert.match(
  dbServiceHandlersSource,
  /case 'check_authorization_quota':[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+checkGatewayAuthorizationQuotaByIdsExactAsync/,
  'PG 模式 DB service 授权额度检查必须使用 exact async 入口'
)
assert.match(
  dbServiceHandlersSource,
  /case 'check_authorization_quota_batch':[\s\S]+runtimeConfig\.databaseDriver === 'postgres'[\s\S]+checkGatewayAuthorizationQuotaBatchByIdsExactAsync/,
  'PG 模式 DB service 批量授权额度检查必须使用 exact async 入口'
)
assert.match(
  apiKeyQuotaServiceSource,
  /export async function checkGatewayApiKeyQuotaExactAsync[\s\S]+loadRequestQuotaCostsBatchAsync/,
  'API Key exact async 额度检查必须直接读取 PostgreSQL 统计窗口'
)
assert.match(
  authorizationQuotaServiceSource,
  /export async function checkGatewayAuthorizationQuotaByIdsExactAsync[\s\S]+checkGatewayAuthorizationQuotaBatchByIdsExactAsync[\s\S]+loadRequestQuotaCostsBatchAsync/,
  '授权 exact async 额度检查必须直接读取 PostgreSQL 统计窗口'
)
assert.match(
  requestQuotaCheckerSource,
  /export async function requestQuotaCostKeyAsync[\s\S]+await usageStatsTimezoneAsync\(\)/,
  'PG quota 成本 key 必须使用 async 时区配置，避免 Redis/PG 模式回读 SQLite 设置'
)
const opsWorkerScheduleSource = sourceBetween(backgroundJobsSource, "case 'ops-worker':", '    default:')
assert.doesNotMatch(opsWorkerScheduleSource, /if\s*\(\s*isPostgresHighPerformanceMode\(\)\s*\)/, 'ops-worker 已迁移运维任务不应再被 PG 高性能模式早退跳过')
for (const jobName of [
  'api-key-availability-schedule-status-sync',
  'account-availability-schedule-status-sync',
  'resource-authorization-expiry-sweep',
  'expired-deleted-account-cleanup',
  'account-api-key-cooldown-retest',
  'proxy-latency-refresh',
  'openai-oauth-access-token-refresh'
] as const) {
  const jobIndex = opsWorkerScheduleSource.indexOf(`backgroundScheduledJobName('${jobName}')`)
  assert(jobIndex >= 0, `ops-worker 必须注册 ${jobName}`)
}
assert.match(
  businessSchemaSource,
  /availability_schedule_next_check_at TEXT[\s\S]+idx_accounts_availability_schedule_next_check[\s\S]+ON accounts\(availability_schedule_next_check_at ASC, id ASC\)[\s\S]+WHERE availability_schedule_json IS NOT NULL AND deleted_at IS NULL/,
  '账户时间计划同步必须有 next_check_at 字段和部分索引'
)
assert.match(
  businessSchemaSource,
  /availability_schedule_next_check_at TEXT[\s\S]+idx_api_keys_availability_schedule_next_check[\s\S]+ON api_keys\(availability_schedule_next_check_at ASC, id ASC\)[\s\S]+WHERE availability_schedule_json IS NOT NULL/,
  'API Key 时间计划同步必须有 next_check_at 字段和部分索引'
)
assert.match(
  businessSchemaSource,
  /idx_route_strategy_groups_group_strategy[\s\S]+ON route_strategy_groups\(group_id, route_strategy_id\)/,
  '策略路由分组反查必须有 group_id 前导索引，避免停用/删除分组和 FK cascade 扫描 route_strategy_groups'
)
assert.match(
  routeStrategyAvailabilityGuardSource,
  /maxRouteStrategyAvailabilityLossCandidates \+ 1/,
  '策略路由分组可用性 guard 必须限制单次受影响策略数量，避免管理写请求前置检查无界展开'
)
assert.match(
  routeStrategyAvailabilityGuardSource,
  /chunkValues\(uniqueIds,\s*500\)[\s\S]+GROUP BY route_strategy_groups\.route_strategy_id/,
  '策略路由分组可用性 guard 必须按 routeStrategyId 分块聚合，不能对每条策略做 N+1 可用分组计数'
)
assert.match(
  sourceBetween(groupWriteRepositorySource, 'export function deleteGroup', 'export async function deleteGroupAsync'),
  /beginDatabaseTransaction\(database\)[\s\S]+findGroupDeleteLocator\(database, id, access\)[\s\S]+preserveRouteStrategiesBeforeGroupDelete\(database, id, current\.name\)/,
  '同步删除分组必须在写事务内窄定位目标并完成策略路由可用性 guard，避免 guard 与删除之间出现并发绑定窗口'
)
assert.match(
  sourceBetween(groupWriteRepositorySource, 'export async function deleteGroupAsync', 'function preserveRouteStrategiesBeforeGroupDelete'),
  /await client\.transaction\(async \(tx\) => \{[\s\S]+await findGroupDeleteLocatorAsync\(tx, id, access\)[\s\S]+await preserveRouteStrategiesBeforeGroupDeleteAsync\(tx, id, current\.name\)/,
  '异步删除分组必须在事务内按 owner 锁定窄目标后完成策略路由可用性 guard，避免 PostgreSQL TOCTOU'
)
assert.doesNotMatch(
  groupWriteRepositorySource,
  /affectedApiKeyRoutes|loadDeletedGroupApiKeyRouteChanges|FROM\s+\$\{?[^\n]*api_keys/i,
  '删除分组不得查询或返回没有消费者的 API Key 影响明细'
)
const routeStrategyBindingNormalizeSource = sourceBetween(routeStrategyRepositorySource, 'function normalizeRouteStrategyGroupBindings', 'function normalizeRouteStrategyGroupBindingBasics')
assert.match(
  routeStrategyBindingNormalizeSource,
  /const groups = loadRouteStrategyBindableGroups\([\s\S]+Number\(group\.can_bind\) === 1/,
  '同步策略路由绑定校验必须复用批量加载的 can_bind 结果，不能逐分组回查授权关系'
)
assert.match(
  routeStrategyBindingNormalizeSource,
  /const groups = await loadRouteStrategyBindableGroupsAsync\([\s\S]+Number\(group\.can_bind\) === 1/,
  '异步策略路由绑定校验必须复用批量加载的 can_bind 结果，避免 PostgreSQL 20 个分组产生 N+1 round trip'
)
assert.doesNotMatch(
  routeStrategyRepositorySource,
  /canBindRouteStrategyGroupAsync|canBindApiKeyGroup\(binding\.groupId/,
  '策略路由绑定校验不能保留逐分组 canBind 查询'
)
assert.match(
  routeStrategyRepositorySource,
  /sort\(\(left, right\) => left\.localeCompare\(right\)\)[\s\S]+FOR UPDATE OF groups/,
  '异步策略路由绑定校验必须按固定分组 id 顺序锁行，降低并发绑定/停用时的死锁风险'
)
assert.match(
  routeStrategyRepositorySource,
  /normalizeRouteStrategyGroupBindingsAsync\(bindingInputs, systemAccountId, tx, true\)/,
  '异步策略路由创建必须在事务内重新校验并锁定分组绑定'
)
assert.match(
  routeStrategyRepositorySource,
  /lockRouteStrategyMutationRowAsync\(tx, id, systemAccountId\)[\s\S]+routeStrategyApiKeyCountAsync\(tx, id, systemAccountId\)/,
  '异步删除策略路由必须在事务内锁定策略行后统计 API Key 引用'
)

assert.match(publicApiLogCaptureSource, /function boundedSnapshotValue/, '公开接口日志快照必须使用预算式克隆')
assert.doesNotMatch(
  sourceBetween(publicApiLogCaptureSource, 'function boundedSnapshot', 'function isSnapshotEmpty'),
  /safeJsonStringify\(data\)/,
  '公开接口日志不能对原始快照对象完整 JSON.stringify'
)
assert.doesNotMatch(
  sourceBetween(publicApiLogCaptureSource, 'function estimatePayloadSizeBytes', 'function safeJsonStringify'),
  /safeJsonStringify\(value\)/,
  '公开接口日志响应大小估算不能对原始响应对象完整 JSON.stringify'
)

assert.match(runtimeLogsSource, /const runtimeLogKeywordDefaultWindowHours = 6/, '运行日志 keyword 无时间范围时必须有默认窗口')
assert.match(
  runtimeLogsSource,
  /options\.keyword\?\.trim\(\)\s*&&\s*!startAt\s*&&\s*!endAt[\s\S]+rl\.time >= \?/,
  '运行日志 keyword 无时间范围时必须追加时间下界，避免扫完整保留窗口'
)

assert.match(rebuildUsageStatsSource, /--confirm-offline/, '用量统计重建脚本必须要求显式离线确认')
assert.match(rebuildUsageStatsSource, /maxBatches/, '用量统计重建脚本必须有最大批次数')
assert.match(rebuildUsageStatsSource, /await yieldToEventLoop\(\)/, '用量统计重建脚本每批之间必须让出事件循环')
assert.match(rebuildUsageStatsSource, /refreshUsageRankSnapshotsInStages/, '用量统计重建脚本刷新快照时必须使用分阶段入口')

console.log('SQLite 高数据量守卫回归通过：周期任务、日志快照、运行日志搜索、统计水位和离线重建均有边界')

function source(path: string): string {
  return readFileSync(resolve(path), 'utf8')
}

function sourceBetween(value: string, start: string, end: string): string {
  const startIndex = value.indexOf(start)
  assert.notEqual(startIndex, -1, `缺少源码片段起点：${start}`)
  const endIndex = value.indexOf(end, startIndex + start.length)
  assert.notEqual(endIndex, -1, `缺少源码片段终点：${end}`)
  return value.slice(startIndex, endIndex)
}
