import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runAccountBalanceRefresh } from '../../../../backend/src/modules/background/account-balance-refresh.job'
import { WorkerScheduler } from '../../../../backend/src/modules/background/worker-scheduler'
import { logger } from '../../../../backend/src/shared/logger'
import { formatDateTime, parseStrictDatePickerValue, serverDateTimeTimestamp } from '../../shared/formatters'
import type { SystemMetricsRuntimeOverview } from '../../types/domain'
import { auditLogEmptyDescription } from '../../views/audit-logs/auditLogRetentionText'
import {
  backgroundJobStatusColor,
  backgroundJobStatusText
} from '../../views/stats/statsBackgroundJobs'
import {
  backgroundQueueActiveCount,
  backgroundQueueStatusColor,
  backgroundQueueStatusText,
  buildBackgroundQueueRows,
  type BackgroundQueueRow
} from '../../views/stats/statsBackgroundQueues'

const goNanosecondTime = '2026-07-14T12:34:56.123456789Z'
logger.level = 'silent'
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

assert.equal(backgroundJobStatusText(backgroundJobState({ queuedForLane: true })), '等待资源')
assert.equal(backgroundJobStatusText(backgroundJobState({ pending: true })), '待补跑')
assert.equal(backgroundJobStatusText(backgroundJobState({ leaseState: 'busy', lastOutcome: 'skipped' })), '其他实例执行')
assert.equal(backgroundJobStatusText(backgroundJobState({ leaseState: 'lost' })), '租约丢失')
assert.equal(backgroundJobStatusColor(backgroundJobState({ leaseState: 'lost' })), 'error')
assert.equal(backgroundJobStatusText(backgroundJobState({ lastOutcome: 'timeout' })), '上次超时')

await testAccountBalancePartialAndRecoveryAcrossRuntimeDto()

assert.equal(auditLogEmptyDescription(undefined), '暂无审计日志。')
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 6, successSampleRate: 0.025 })), /最近 6 小时.*2\.5%/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 0, successSampleRate: 0 })), /成功请求当前不记录/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 0, successSampleRate: 0.1, successRetentionDays: 0 })), /成功请求当前不记录/)
assert.match(auditLogEmptyDescription(auditSettings({ successHotRetentionHours: 1, successSampleRate: 0.1, successRetentionDays: 0 })), /最近 1 小时.*不长期保留/)

const jobsCardSource = readFileSync(new URL('../../views/stats/StatsBackgroundJobsCard.vue', import.meta.url), 'utf8')
const systemMetricsViewSource = readFileSync(new URL('../../views/stats/SystemMetricsStatsView.vue', import.meta.url), 'utf8')
const statsApiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const frontendDomainSource = readFileSync(new URL('../../types/domain/usage-stats.ts', import.meta.url), 'utf8')
const statsRoutesSource = readFileSync(new URL('../../../../backend/src/modules/stats/stats.routes.ts', import.meta.url), 'utf8')
const dbServiceTypesSource = readFileSync(new URL('../../../../backend/src/modules/db-service/db-service-types.ts', import.meta.url), 'utf8')
const mockBackgroundRuntimeSource = readFileSync(new URL('../../../../backend/src/modules/stats/mock-background-runtime.ts', import.meta.url), 'utf8')
assert.match(statsRoutesSource, /statsRouter\.get\('\/system-metrics'/, '跨层门禁必须绑定真实 system-metrics 接口')
assert.match(statsApiSource, /systemMetricsRuntime:[\s\S]*\/stats\/system-metrics\/runtime/, '后台运行态必须使用独立 API')
assert.match(systemMetricsViewSource, /void loadRuntimeData\(\)[\s\S]*return loadData\(\)/, '运行态卡片加载不得阻塞趋势首屏')
assert.match(systemMetricsViewSource, /function loadPageData\(\)[\s\S]*void loadUsageStatsWindow\(\)[\s\S]*return loadData\(\)/, '窗口配置不得阻塞趋势业务请求')
assert.doesNotMatch(systemMetricsViewSource, /loadUsageStatsWindow\(\{\s*force:\s*true\s*\}\)/, '系统指标页不得每次强制绕过窗口缓存')
assert.match(systemMetricsViewSource, /if \(!dateRangeExplicit\.value\) return \{\}/, '未显式选日期时不得提交浏览器本地日期')
assert.match(
  statsRoutesSource,
  /function backgroundJobsFromSnapshot[\s\S]+\.map\(\(job\) => \(\{ \.\.\.job, workerRole: snapshot\.workerRole \}\)\)/,
  'system-metrics DTO 必须完整保留 scheduler job snapshot 的部分失败与恢复字段'
)
assert(jobsCardSource.includes('backgroundJobStatusText'), '后台任务组件必须复用已验证的状态格式化函数')
assert(jobsCardSource.includes("title: '部分失败（本进程）'"), '后台任务必须单独展示部分失败次数')
assert(jobsCardSource.includes("title: '累计失败（本进程）'"), '后台任务历史失败必须明确计数作用域')
assert(jobsCardSource.includes("title: '最近失败'"), '后台任务必须展示最近失败时间以区分当前异常和历史计数')
assert(jobsCardSource.includes("title: '任务跳过 / 合并 / 超时'"), '后台任务必须区分任务主动跳过、合并补跑和超时次数')
assert(jobsCardSource.includes("title: '下次运行'"), '后台任务必须展示 scheduler 计算的下次运行时间')
for (const field of ['stablePhaseOffsetMs', 'scheduleMode', 'overlapPolicy', 'pending', 'queuedForLane', 'overdueMs', 'nextRunAt', 'lastOutcome', 'leaseState', 'taskSkippedCount', 'coalescedCount', 'timedOutCount']) {
  assert(statsRoutesSource.includes(field), `system-metrics DTO 必须声明 scheduler 字段 ${field}`)
  assert(dbServiceTypesSource.includes(field), `DB service runtime DTO 必须声明 scheduler 字段 ${field}`)
  assert(frontendDomainSource.includes(field), `前端 runtime DTO 必须声明 scheduler 字段 ${field}`)
}
assert.match(mockBackgroundRuntimeSource, /runCount:[^\n]+taskSkippedCount/, 'mock runCount 只能包含真实任务执行结果，不能把调度跳过次数算成执行次数')
assert.doesNotMatch(mockBackgroundRuntimeSource, /runCount:[^\n]+skippedCount/, 'mock runCount 不得包含 scheduler overlap skippedCount')

const queuesCardSource = readFileSync(new URL('../../views/stats/StatsBackgroundQueuesCard.vue', import.meta.url), 'utf8')
assert(queuesCardSource.includes("if (row.nextRunAt) return '下次运行'"), '定时队列时间必须明确标注为下次运行')
assert(queuesCardSource.includes("if (row.flushLastSuccessAt) return '最近写入成功'"), '写入队列时间必须明确标注为最近写入成功')
assert(!queuesCardSource.includes("{ title: '时间', key: 'nextOrSuccessAt'"), '后台队列不得继续使用无语义的时间列名')
assert(queuesCardSource.includes("{ title: '容量 / 处理', key: 'processingMetrics'"), '异构队列适用指标必须合并展示，避免大量空列')
assert(queuesCardSource.includes("{ title: '异常累计', key: 'problemMetrics'"), '队列异常必须按真实来源合并展示')
assert(!queuesCardSource.includes("{ title: '已完成', key: 'completedCount'"), '不应为仅少数队列适用的指标保留固定空列')

console.log('运行状态展示契约回归通过：RFC3339Nano、任务失败、队列历史失败和审计动态空态符合预期')

async function testAccountBalancePartialAndRecoveryAcrossRuntimeDto(): Promise<void> {
  const scheduler = new WorkerScheduler()
  let refreshAttempt = 0
  const neverSettles = new Promise<never>(() => undefined)
  scheduler.schedule({
    name: 'account-balance-refresh',
    intervalMs: 100,
    task: () => runAccountBalanceRefresh({
      listRecoveryCandidates: async () => [],
      listDueCandidates: async () => [{
        id: 'account-balance-partial-contract',
        systemAccountId: 'sys_admin',
        configRevision: 1,
        credentials: { api_key: 'sk-contract', base_url: 'https://relay.example/v1' },
        config: { adapter: 'builtin', intervalMinutes: 5 },
        nextRefreshAt: new Date().toISOString(),
        stateUpdatedAt: new Date().toISOString()
      }],
      refreshCandidate: async (_candidate, context) => {
        refreshAttempt += 1
        if (refreshAttempt === 1) {
          await Promise.race([
            neverSettles,
            new Promise<never>((_resolve, reject) => {
              context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true })
            })
          ])
        }
      },
      runBudgetMs: 20,
      candidateTimeoutMs: 5
    })
  })
  try {
    await waitFor(() => scheduler.snapshots()[0]?.partialCount === 1)
    const partialDto = systemMetricsJobDto(scheduler.snapshots()[0]!)
    const partialRow = partialDto.backgroundJobs![0]!
    assert.equal(partialRow.name, 'account-balance-refresh')
    assert.equal(partialRow.failureCount, 0, '候选超时经 system-metrics DTO 后不能变成整项失败')
    assert.equal(partialRow.partialCount, 1)
    assert.equal(backgroundJobStatusText(partialRow), '部分失败')
    assert.equal(backgroundJobStatusColor(partialRow), 'warning')

    await waitFor(() => scheduler.snapshots()[0]?.successCount === 1)
    const recoveredDto = systemMetricsJobDto(scheduler.snapshots()[0]!)
    const recoveredRow = recoveredDto.backgroundJobs![0]!
    assert.equal(recoveredRow.lastWarning, undefined, '完整成功后 DTO 不应保留当前部分失败原因')
    assert.ok(recoveredRow.lastWarningAt, 'DTO 必须保留历史部分失败时间用于识别恢复')
    assert.ok(recoveredRow.lastSuccessAt)
    assert.equal(backgroundJobStatusText(recoveredRow), '已恢复')
    assert.equal(backgroundJobStatusColor(recoveredRow), 'success')
  } finally {
    scheduler.stop()
  }
}

function systemMetricsJobDto(
  snapshot: ReturnType<WorkerScheduler['snapshots']>[number]
): Pick<SystemMetricsRuntimeOverview, 'backgroundJobsAvailable' | 'backgroundJobs'> {
  return {
    backgroundJobsAvailable: true,
    backgroundJobs: [{ ...snapshot, workerRole: 'ops-worker' }]
  }
}

function backgroundJobState(overrides: Partial<NonNullable<SystemMetricsRuntimeOverview['backgroundJobs']>[number]> = {}): NonNullable<SystemMetricsRuntimeOverview['backgroundJobs']>[number] {
  return {
    name: 'runtime-contract',
    intervalMs: 60_000,
    running: false,
    runCount: 0,
    successCount: 0,
    failureCount: 0,
    partialCount: 0,
    skippedCount: 0,
    ...overrides
  }
}

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 1000
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待后台任务运行态快照超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

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
