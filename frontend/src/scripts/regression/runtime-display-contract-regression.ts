import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { formatDateTime, parseStrictDatePickerValue, serverDateTimeTimestamp } from '../../shared/formatters'
import { auditLogEmptyDescription } from '../../views/audit-logs/auditLogRetentionText'
import {
  backgroundQueueActiveCount,
  backgroundQueueStatusColor,
  backgroundQueueStatusText,
  buildBackgroundQueueRows,
  type BackgroundQueueRow
} from '../../views/stats/statsBackgroundQueues'

const goNanosecondTime = '2026-07-14T12:34:56.123456789Z'
assert.notEqual(formatDateTime(goNanosecondTime), '时间格式异常', 'Go RFC3339Nano 时间必须可显示')
assert.equal(serverDateTimeTimestamp('2026-07-14T20:34:56.123456+08:00'), Date.parse('2026-07-14T12:34:56.123456Z'))
assert(parseStrictDatePickerValue('2026-07-14T12:34:56.1Z'), '1 位小数 RFC3339 时间必须可用于时间选择器')
for (const invalid of [
  '2026-07-14T12:34:56',
  '2026-02-30T12:34:56Z',
  '2026-07-14T25:00:00Z',
  '2026-07-14 12:34:56Z',
  '2026-07-14T12:34:56.1234567890Z'
]) {
  assert.equal(serverDateTimeTimestamp(invalid), undefined, `非法或无时区时间必须拒绝：${invalid}`)
}

const recoveredQueue: BackgroundQueueRow = {
  key: 'recovered',
  name: '已恢复队列',
  queueType: 'local',
  queueLength: 0,
  failedCount: 2,
  droppedCount: 1,
  flushFailureCount: 3
}
assert.equal(backgroundQueueStatusText(recoveredQueue), '曾失败', '累计历史失败不能伪装成当前异常')
assert.equal(backgroundQueueStatusColor(recoveredQueue), 'warning', '已恢复队列应降为历史警告')
assert.equal(backgroundQueueStatusText({ ...recoveredQueue, lastError: '仍在失败' }), '异常')
assert.equal(backgroundQueueStatusColor({ ...recoveredQueue, lastError: '仍在失败' }), 'error')
assert.equal(backgroundQueueStatusText({ ...recoveredQueue, failedCount: 0, droppedCount: 0, flushFailureCount: 0, queueLength: 2 }), '积压')
assert.equal(backgroundQueueStatusText({ ...recoveredQueue, failedCount: 0, droppedCount: 0, flushFailureCount: 0 }), '空闲')

const optionalMetricRows = buildBackgroundQueueRows({
  backgroundJobs: [{
    name: 'DB service 请求队列',
    intervalMs: 0,
    running: true,
    runCount: 0,
    successCount: 0,
    failureCount: 0,
    skippedCount: 0,
    localQueue: {
      name: 'DB service 请求队列',
      queueType: 'request',
      queueLength: 2,
      runningCount: 1
    }
  }]
} as never)
assert.equal(optionalMetricRows.length, 1)
assert.equal(optionalMetricRows[0]?.consumers, undefined, '非 Redis 队列缺失 consumers 时必须保留为不适用')
assert.equal(optionalMetricRows[0]?.queueBytes, undefined, '缺失的可选指标不能伪造成 0')
assert.equal(optionalMetricRows[0]?.completedCount, undefined, '不适用的已完成指标必须显示为 -')
assert.equal(backgroundQueueActiveCount(optionalMetricRows[0]!), 1, '缺失 consumers 时活跃数必须回退到真实 runningCount')
const noActiveMetricRows = buildBackgroundQueueRows({
  backgroundJobs: [{
    name: '无活跃来源队列', intervalMs: 0, running: false, runCount: 0, successCount: 0, failureCount: 0, skippedCount: 0,
    localQueue: { name: '无活跃来源队列', queueType: 'local', queueLength: 0 }
  }]
} as never)
assert.equal(backgroundQueueActiveCount(noActiveMetricRows[0]!), undefined, '没有任何活跃来源时必须显示不适用而不是 0')

assert.equal(auditLogEmptyDescription(undefined), '暂无审计日志。')
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 6, successSampleRate: 0.025 })), /最近 6 小时.*2\.5%/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 0, successSampleRate: 0 })), /成功请求当前不记录/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 0, successSampleRate: 0.1, successRetentionDays: 0 })), /成功请求当前不记录/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 1, successSampleRate: 0.1, successRetentionDays: 0 })), /最近 1 小时.*不长期保留/)

const jobsCardSource = readFileSync(new URL('../../views/stats/StatsBackgroundJobsCard.vue', import.meta.url), 'utf8')
assert(jobsCardSource.includes("if (row.lastError) return '上次失败'"), '后台任务最近失败时不能显示为空闲')
assert(jobsCardSource.includes("if (row.lastWarning) return '部分失败'"), '候选级部分失败必须与整项后台任务失败区分')
assert(jobsCardSource.includes("title: '部分失败（本进程）'"), '后台任务必须单独展示部分失败次数')
assert(jobsCardSource.includes("return '已恢复'"), '后台任务后续成功后必须与当前失败区分')
assert(jobsCardSource.includes("title: '累计失败（本进程）'"), '后台任务历史失败必须明确计数作用域')
assert(jobsCardSource.includes("title: '最近失败'"), '后台任务必须展示最近失败时间以区分当前异常和历史计数')

const queuesCardSource = readFileSync(new URL('../../views/stats/StatsBackgroundQueuesCard.vue', import.meta.url), 'utf8')
assert(queuesCardSource.includes("if (row.nextRunAt) return '下次运行'"), '定时队列时间必须明确标注为下次运行')
assert(queuesCardSource.includes("if (row.flushLastSuccessAt) return '最近写入成功'"), '写入队列时间必须明确标注为最近写入成功')
assert(!queuesCardSource.includes("{ title: '时间', key: 'nextOrSuccessAt'"), '后台队列不得继续使用无语义的时间列名')
assert(queuesCardSource.includes("{ title: '容量 / 处理', key: 'processingMetrics'"), '异构队列适用指标必须合并展示，避免大量空列')
assert(queuesCardSource.includes("{ title: '异常累计', key: 'problemMetrics'"), '队列异常必须按真实来源合并展示')
assert(!queuesCardSource.includes("{ title: '已完成', key: 'completedCount'"), '不应为仅少数队列适用的指标保留固定空列')

console.log('运行状态展示契约回归通过：RFC3339Nano、任务失败、队列历史失败和审计动态空态符合预期')

function auditSettings(overrides: Partial<NonNullable<Parameters<typeof auditLogEmptyDescription>[0]>> = {}): NonNullable<Parameters<typeof auditLogEmptyDescription>[0]> {
  return {
    enabled: true,
    successSampleRate: 0.1,
    flushIntervalSeconds: 5,
    batchSize: 500,
    queueMaxItems: 50000,
    queueMaxBytes: 256 * 1024 * 1024,
    activeCaptureMaxBytes: 64 * 1024 * 1024,
    successHotRetentionHours: 1,
    successRetentionDays: 3,
    problemRetentionDays: 7,
    successFullBodyLimitBytes: 512 * 1024,
    problemFullBodyLimitBytes: 2 * 1024 * 1024,
    ...overrides
  }
}
