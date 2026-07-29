import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-health-monitor-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'ai-health-monitor-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository, healthMonitorRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/account-health-monitor.repository.js')
])
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const monitorSource = readFileSync(resolve('src/storage/account-health-monitor.repository.ts'), 'utf8')
  const routesSource = readFileSync(resolve('src/modules/stats/stats.routes.ts'), 'utf8')
  const workerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
  assert.doesNotMatch(monitorSource, /\busage_records\b/i, '健康监控请求路径不得扫描使用记录明细')
  assert.match(monitorSource, /FROM account_health_hourly/, '健康监控必须查询小时预聚合表')
  assert.match(monitorSource, /SELECT account_id, stat_hour, status, source_order/, '健康列表只应读取小时槽状态字段')
  assert.match(monitorSource, /accounts\.last_used_at DESC[\s\S]+accounts\.name ASC/, '健康监控应按最近使用时间和名称稳定排序')
  assert.doesNotMatch(monitorSource, /listAccountItemsPage/, '健康监控不得复用 AI 账户管理宽列表组装')
  assert.match(routesSource, /statsRouter\.get\('\/ai-health\/hour-detail'/, '管理与自助统计路由必须提供单点小时详情')
  assert.match(workerSource, /case 'get_ai_health_hour_detail_read_only'/, 'SQLite read worker 必须承接小时详情读取')
  assert.doesNotMatch(monitorSource, /field: 'recentRequestCount'/, '健康监控不得借用管理列表的质量统计排序')

  const group = repositories.createGroup({ name: 'AI 健康监控回归分组', providerCode: 'gpt' }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    name: 'AI 健康监控回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-ai-health-monitor-regression', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1'],
    healthCheckModel: 'gpt-5.1',
    groupId: group.id
  }, access)
  const currentHour = new Date(Date.now() - 60 * 60_000)
  currentHour.setMinutes(0, 0, 0)
  const previousHourAt = new Date(currentHour.getTime() - 55 * 60_000).toISOString()
  const currentSuccessAt = new Date(currentHour.getTime() + 5 * 60_000).toISOString()
  const currentFailureAt = new Date(currentHour.getTime() + 10 * 60_000).toISOString()

  repositories.createUsageRecordsBatch([
    healthRecord('ai_health_previous_success', previousHourAt, true),
    healthRecord('ai_health_current_success', currentSuccessAt, true, 200),
    healthRecord('ai_health_current_failure', currentFailureAt, false, 503, 'upstream_unavailable', '上游暂时不可用')
  ])
  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(20, new Date(Date.now() + 60_000).toISOString()), 3)

  const rows = databaseModule.getStatsDatabase().prepare(`
    SELECT status, last_record_id, status_code, error_code
    FROM account_health_hourly
    WHERE account_id = ?
    ORDER BY stat_hour ASC
  `).all(account.id) as unknown as Array<{ status: string; last_record_id: string; status_code: number | null; error_code: string | null }>
  assert.equal(rows.length, 2, '同账户同小时只保留最后结果')
  assert.equal(rows[0]?.status, 'success')
  assert.equal(rows[0]?.status_code, null)
  assert.equal(rows[1]?.status, 'failure')
  assert.equal(rows[1]?.last_record_id, 'ai_health_current_failure')
  assert.equal(rows[1]?.status_code, 503)
  assert.equal(rows[1]?.error_code, 'upstream_unavailable')

  const result = healthMonitorRepository.getAiHealthList(access, { hours: 3, keyword: '健康监控回归', page: 1, pageSize: 20 })
  assert.equal(result.total, 1, '账户名搜索应命中')
  assert.equal(result.items[0]?.hours.length, 3)
  assert.equal(result.items[0]?.successHours, 1)
  assert.equal(result.items[0]?.failureHours, 1)
  assert.equal(result.items[0]?.unknownHours, 1)
  assert.equal(result.items[0]?.latestStatus, 'failure')
  assert.equal(result.items[0]?.healthRate, 50)
  assert.deepEqual(
    Object.keys(result.items[0]?.hours.find((hour) => hour.status === 'failure') ?? {}).sort(),
    ['statHour', 'status'],
    '健康列表小时槽不得携带只在点击详情使用的状态码和错误正文'
  )
  assert.deepEqual(
    Object.keys(result).sort(),
    ['hasMore', 'items', 'page', 'pageSize', 'total'],
    '健康列表不得返回页面未使用的时区和范围元数据'
  )
  const selfResult = healthMonitorRepository.getAiHealthList({ systemAccountId: 'sys_admin', role: 'user' }, { hours: 3, keyword: '健康监控回归', page: 1, pageSize: 20 })
  assert.equal(selfResult.items.length, 1)
  assert.equal('systemAccountName' in selfResult.items[0], false, '自助健康列表不得返回只供管理视图展示的所属用户字段')
  const failureSlot = result.items[0]?.hours.find((hour) => hour.status === 'failure')
  assert.ok(failureSlot)
  const detail = healthMonitorRepository.getAiHealthHourDetail(access, account.id, failureSlot.statHour)
  assert.deepEqual(detail, {
    statHour: failureSlot.statHour,
    status: 'failure',
    lastObservedAt: currentFailureAt,
    statusCode: 503,
    errorCode: 'upstream_unavailable',
    errorMessage: '上游暂时不可用'
  }, '点击单个小时槽后才应返回该小时详情')
  const unknownSlot = result.items[0]?.hours.find((hour) => hour.status === 'unknown')
  assert.ok(unknownSlot)
  assert.deepEqual(
    healthMonitorRepository.getAiHealthHourDetail(access, account.id, unknownSlot.statHour),
    { statHour: unknownSlot.statHour, status: 'unknown' },
    '无记录小时的详情应保持最小响应'
  )
  assert.equal(
    healthMonitorRepository.getAiHealthHourDetail({ systemAccountId: 'sys_other', role: 'user' }, account.id, failureSlot.statHour),
    undefined,
    '小时详情必须在读取统计详情前按账户所有权拒绝跨用户访问'
  )
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO account_quality_health_hourly (
      account_id, system_account_id, provider_code, stat_hour, observed_at, model_check_run_id,
      model, profile, score, threshold, level, error_code, error_message, updated_at
    ) VALUES (?, 'sys_admin', 'gpt', ?, ?, 'run_ai_health_quality',
      'gpt-5.1', 'quick', 35, 70, 'suspicious', 'quality_failed', '质量检查失败', ?)
  `).run(account.id, failureSlot.statHour, currentFailureAt, currentFailureAt)
  assert.deepEqual(
    healthMonitorRepository.getAiHealthHourDetail(access, account.id, failureSlot.statHour),
    {
      statHour: failureSlot.statHour,
      status: 'failure',
      lastObservedAt: currentFailureAt,
      errorCode: 'model_quality_failed',
      errorMessage: '模型质量检查不达标：35 分，阈值 70 分'
    },
    '普通健康和质量健康同小时并存时，详情必须保持质量失败优先'
  )

  const bounded = healthMonitorRepository.getAiHealthList(access, { hours: 9999, pageSize: 20 })
  assert.equal(bounded.items.find((item) => item.id === account.id)?.hours.length, 31 * 24)

  const highRecent = createSortAccount('AI 健康排序-高频较新', 'recent')
  const highOlder = createSortAccount('AI 健康排序-高频较旧', 'older')
  const lowLatest = createSortAccount('AI 健康排序-低频最新', 'latest')
  const now = Date.now()
  const highRecentAt = new Date(now - 60 * 60_000).toISOString()
  const highOlderAt = new Date(now - 2 * 24 * 60 * 60_000).toISOString()
  const lowLatestAt = new Date(now).toISOString()
  const updateLastUsed = databaseModule.getBusinessDatabase().prepare('UPDATE accounts SET last_used_at = ? WHERE id = ?')
  updateLastUsed.run(highRecentAt, highRecent.id)
  updateLastUsed.run(highOlderAt, highOlder.id)
  updateLastUsed.run(lowLatestAt, lowLatest.id)
  const qualityWindowStart = new Date(now - 10 * 60_000).toISOString()
  const qualityWindowEnd = new Date(now).toISOString()
  const insertQuality = databaseModule.getStatsDatabase().prepare(`
    INSERT INTO account_quality_scores (
      account_id, system_account_id, provider_code, recent_request_count,
      window_started_at, window_ended_at, updated_at
    ) VALUES (?, 'sys_admin', 'gpt', ?, ?, ?, ?)
  `)
  insertQuality.run(highRecent.id, 50, qualityWindowStart, qualityWindowEnd, highRecentAt)
  insertQuality.run(highOlder.id, 50, qualityWindowStart, qualityWindowEnd, highOlderAt)
  insertQuality.run(lowLatest.id, 5, qualityWindowStart, qualityWindowEnd, lowLatestAt)

  const sorted = healthMonitorRepository.getAiHealthList(access, { hours: 3, keyword: 'AI 健康排序-', page: 1, pageSize: 20 })
  assert.deepEqual(
    sorted.items.map((item) => item.id),
    [lowLatest.id, highRecent.id, highOlder.id],
    '健康监控必须按最后使用时间降序，不应为列表排序额外读取质量统计'
  )
  console.log('AI 健康监控回归通过')

  function createSortAccount(name: string, keySuffix: string) {
    return repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name,
      type: 'api_key',
      credentials: { api_key: `sk-ai-health-sort-${keySuffix}`, base_url: 'https://api.openai.com/v1' },
      supportedModels: ['gpt-5.1'],
      healthCheckModel: 'gpt-5.1',
      groupId: group.id
    }, access)
  }

  function healthRecord(id: string, createdAt: string, success: boolean, statusCode?: number, errorCode?: string, errorMessage?: string) {
    return {
      id,
      traceId: `trace-${id}`,
      trafficSource: 'account_health_check' as const,
      systemAccountId: 'sys_admin',
      accountId: account.id,
      groupId: group.id,
      providerCode: 'gpt',
      endpoint: '/v1/responses',
      model: 'gpt-5.1',
      stream: false,
      success,
      statusCode,
      errorCode,
      errorMessage,
      durationMs: 10,
      createdAt
    }
  }
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
