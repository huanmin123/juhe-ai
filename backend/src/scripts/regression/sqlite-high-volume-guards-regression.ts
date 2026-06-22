import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const usageStatsSource = source('src/storage/usage-stats.repository.ts')
const statsWriterSource = source('src/modules/background/background-stats-writer.ts')
const usageRangeWindowsSource = source('src/storage/usage-range-windows.repository.ts')
const apiKeyScheduleSyncSource = source('src/storage/api-key-schedule-status-sync.repository.ts')
const accountScheduleSyncSource = source('src/storage/account-availability-schedule-status-sync.repository.ts')
const businessSchemaSource = source('src/storage/schema/business-schema.ts')
const publicApiLogCaptureSource = source('src/modules/public-api-logs/public-api-log-capture.middleware.ts')
const runtimeLogsSource = source('src/storage/runtime-logs.repository.ts')
const rebuildUsageStatsSource = source('src/scripts/maintenance/rebuild-usage-stats.ts')

const watermarkSource = sourceBetween(usageStatsSource, 'function usageRankSnapshotSourceWatermark', 'function usageRankSnapshotRefreshJobState')
assert.doesNotMatch(watermarkSource, /COUNT\s*\(\s*\*\s*\)/i, '排行快照水位不能为了删除感知对统计表执行 COUNT(*)')
assert.doesNotMatch(usageStatsSource, /deletionAwareSourceTables/, '排行快照 stage 不应保留基于全表计数的删除感知字段')
assert.doesNotMatch(usageRangeWindowsSource, /rangeWindowSourceWatermarkRowCount/, '范围窗口刷新不应继续解析旧 rowCount 水位')

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
