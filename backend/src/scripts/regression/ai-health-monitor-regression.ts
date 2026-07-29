import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'

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

const [databaseModule, repositories, usageStatsRepository, healthMonitorRepository, accountNameSearchRepository, { statsRouter }, auth, { requestContextMiddleware }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/account-health-monitor.repository.js'),
  import('../../storage/account-name-search.repository.js'),
  import('../../modules/stats/stats.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js')
])
const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const app = express()
app.use(requestContextMiddleware)
app.use('/api', auth.requireAuth)
app.use('/api/my-stats', auth.forceSelfAccessScope, statsRouter)
app.use('/api/stats', auth.requireAdmin, statsRouter)
let server: Server | undefined

try {
  const monitorSource = readFileSync(resolve('src/storage/account-health-monitor.repository.ts'), 'utf8')
  const postgresSchemaSql = collectPostgresSchemaStatements()
    .filter((statement) => statement.schemaName === 'juhe_business')
    .map((statement) => statement.sql)
    .join('\n')
  const routesSource = readFileSync(resolve('src/modules/stats/stats.routes.ts'), 'utf8')
  const workerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
  assert.doesNotMatch(monitorSource, /\busage_records\b/i, '健康监控请求路径不得扫描使用记录明细')
  assert.match(monitorSource, /FROM account_health_hourly/, '健康监控必须查询小时预聚合表')
  assert.match(monitorSource, /SELECT account_id, stat_hour, status, source_order/, '健康列表只应读取小时槽状态字段')
  assert.doesNotMatch(monitorSource, /SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message/, '单点详情外层不得返回未消费的账户和小时定位字段')
  assert.match(monitorSource, /SELECT status, last_observed_at, status_code, error_code, error_message\s+FROM \(/, '单点详情外层只应投影抽屉消费字段')
  assert.match(monitorSource, /account_name_search_terms/, '账户名包含搜索必须使用增量维护的搜索候选表')
  assert.doesNotMatch(monitorSource, /instr\(lower\(accounts\.name\)|position\(lower\(\?\) in lower\(accounts\.name\)\)/, '健康列表不得扫描 lower(name) 完成包含搜索')
  assert.match(monitorSource, /\(accounts\.last_used_at IS NULL\) ASC,[\s\S]+accounts\.last_used_at DESC,[\s\S]+accounts\.name ASC/, '健康监控应按最近使用时间和名称稳定排序')
  assert.doesNotMatch(monitorSource, /pagedTotalUpperBound/, '健康列表不得把渐进下界伪装成真实总数')
  assert.match(postgresSchemaSql, /idx_accounts_health_monitor_order[\s\S]*last_used_at IS NULL[\s\S]*last_used_at DESC[\s\S]*name ASC[\s\S]*id ASC/, 'PostgreSQL 管理健康列表必须有等价窄排序索引')
  assert.match(postgresSchemaSql, /idx_accounts_owner_health_monitor_order[\s\S]*system_account_id[\s\S]*last_used_at IS NULL[\s\S]*last_used_at DESC[\s\S]*name ASC[\s\S]*id ASC/, 'PostgreSQL 自助健康列表必须有 owner 前缀窄排序索引')
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
  assert.equal(result.items.length, 1, '账户名搜索应命中')
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
    ['hasMore', 'items', 'page', 'pageSize'],
    '健康列表不得返回伪总数、页面未使用的时区或范围元数据'
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

  for (let index = 0; index < 11; index += 1) {
    createSortAccount(`AI 健康分页-${String(index).padStart(2, '0')}`, `page-${index}`)
  }
  const firstPage = healthMonitorRepository.getAiHealthList(access, { hours: 1, keyword: 'AI 健康分页-', page: 1, pageSize: 10 })
  const secondPage = healthMonitorRepository.getAiHealthList(access, { hours: 1, keyword: 'AI 健康分页-', page: 2, pageSize: 10 })
  assert.equal(firstPage.items.length, 10)
  assert.equal(firstPage.hasMore, true, '首屏只通过 pageSize + 1 哨兵声明存在下一页')
  assert.equal(secondPage.items.length, 1)
  assert.equal(secondPage.hasMore, false, '末页必须明确禁止继续前进')
  assert.equal('total' in firstPage, false, '健康列表不得返回未经 COUNT 证明的 total')

  seedLargeAccountFixture(1_200)
  accountNameSearchRepository.rebuildAccountNameSearchTerms(databaseModule.getBusinessDatabase())
  assertNoTemporarySort(false)
  assertNoTemporarySort(true)
  assertKeywordSearchPlan(false)
  assertKeywordSearchPlan(true)
  const rareKeywordResult = healthMonitorRepository.getAiHealthList(access, { hours: 1, keyword: '计划 1199', page: 1, pageSize: 10 })
  assert.deepEqual(rareKeywordResult.items.map((item) => item.id), ['acc_ai_health_plan_1199'], '稀有包含关键词必须通过搜索候选表准确命中')

  const selfUser = repositories.createSystemAccount({
    username: 'ai_health_http_user',
    displayName: 'AI健康HTTP用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const selfAccess = { systemAccountId: selfUser.id, role: 'user' as const }
  const selfGroup = repositories.createGroup({ name: 'AI 健康 HTTP 用户分组', providerCode: 'gpt' }, selfAccess)
  const selfAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_gpt_openai_v1',
    name: 'AI 健康 HTTP 自有账户',
    type: 'api_key',
    credentials: { api_key: 'sk-ai-health-http-self', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1'],
    healthCheckModel: 'gpt-5.1',
    groupId: selfGroup.id
  }, selfAccess)

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', 'AI 健康 HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}/api`
  const userCookie = sessionCookie(selfUser.id)
  const adminCookie = sessionCookie('sys_admin')
  await assertStatus(`${baseUrl}/my-stats/ai-health`, '', 401, '未登录用户不得读取自助健康列表')
  await assertStatus(`${baseUrl}/stats/ai-health`, userCookie, 403, '普通用户不得调用管理健康列表')
  const selfHttpList = await getData<{ items: Array<{ id: string }>; hasMore: boolean; page: number; pageSize: number }>(
    `${baseUrl}/my-stats/ai-health?systemAccountId=sys_admin&keyword=${encodeURIComponent('AI 健康 HTTP')}`,
    userCookie
  )
  assert.deepEqual(selfHttpList.items.map((item) => item.id), [selfAccount.id], '自助列表必须忽略伪造 owner 并固定当前用户')
  assert.deepEqual(Object.keys(selfHttpList).sort(), ['hasMore', 'items', 'page', 'pageSize'], '真实 HTTP 列表必须保持无 total 的哨兵分页契约')
  const detailHour = failureSlot.statHour
  await assertStatus(`${baseUrl}/my-stats/ai-health/hour-detail?accountId=${account.id}&statHour=${detailHour}`, userCookie, 404, '自助详情访问其他用户账户必须统一返回 404')
  await assertStatus(`${baseUrl}/my-stats/ai-health/hour-detail?accountId=missing&statHour=${detailHour}`, userCookie, 404, '不存在账户必须返回 404')
  await assertStatus(`${baseUrl}/my-stats/ai-health/hour-detail?accountId=${selfAccount.id}&statHour=${detailHour}`, userCookie, 200, '自助详情应允许读取自有账户')
  await assertStatus(`${baseUrl}/stats/ai-health/hour-detail?accountId=${selfAccount.id}&statHour=${detailHour}`, adminCookie, 200, '管理员详情应允许读取管理作用域账户')
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

  function seedLargeAccountFixture(count: number): void {
    const nowIso = new Date().toISOString()
    const insert = databaseModule.getBusinessDatabase().prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credential_mask, credentials_encrypted, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, schedulable, stream_failure_count,
        health_check_model, health_check_endpoint_mode, last_used_at, created_at, updated_at
      ) VALUES (?, 'sys_admin', 'gpt', 'profile_gpt_openai_v1', 'openai', 'v1', ?, 'api_key', 'active', 'sk-***', '{}', 20, 10, 0, 0, 1, 0, 'gpt-5.1', 'responses_sse', ?, ?, ?)
    `)
    databaseModule.getBusinessDatabase().exec('BEGIN')
    try {
      for (let index = 0; index < count; index += 1) {
        const suffix = String(index).padStart(4, '0')
        insert.run(`acc_ai_health_plan_${suffix}`, `AI 健康计划 ${suffix}`, new Date(Date.now() - index * 1_000).toISOString(), nowIso, nowIso)
      }
      databaseModule.getBusinessDatabase().exec('COMMIT')
    } catch (error) {
      databaseModule.getBusinessDatabase().exec('ROLLBACK')
      throw error
    }
  }

  function assertNoTemporarySort(ownerScoped: boolean): void {
    const scopeClause = ownerScoped ? 'AND accounts.system_account_id = ?' : ''
    const systemAccountProjection = ownerScoped
      ? 'NULL AS system_account_name'
      : 'COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id) AS system_account_name'
    const systemAccountJoin = ownerScoped
      ? ''
      : 'LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id'
    const plan = databaseModule.getBusinessDatabase().prepare(`
      EXPLAIN QUERY PLAN
      SELECT
        accounts.id,
        ${systemAccountProjection},
        COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
        accounts.name,
        CASE
          WHEN accounts.authorization_instance_authorization_id IS NOT NULL
            AND authorizations.status <> 'active'
          THEN 'disabled'
          ELSE accounts.status
        END AS status,
        accounts.last_health_check_at,
        accounts.last_health_success_at,
        accounts.next_health_check_at
      FROM accounts
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
        AND source_accounts.deleted_at IS NULL
      LEFT JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      ${systemAccountJoin}
      WHERE accounts.deleted_at IS NULL
        ${scopeClause}
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
      ORDER BY (accounts.last_used_at IS NULL) ASC, accounts.last_used_at DESC, accounts.name ASC, accounts.id ASC
      LIMIT 21 OFFSET 0
    `).all(...(ownerScoped ? ['sys_admin'] : [])) as unknown as Array<{ detail: string }>
    assert.equal(
      plan.some((row) => /USE TEMP B-TREE/i.test(row.detail)),
      false,
      `${ownerScoped ? '自助' : '管理'}健康列表大夹具排序必须由索引承接：${plan.map((row) => row.detail).join(' | ')}`
    )
  }

  function assertKeywordSearchPlan(ownerScoped: boolean): void {
    const keyword = '计划 1199'
    const normalizedKeyword = accountNameSearchRepository.normalizeAccountNameSearchText(keyword)
    const terms = accountNameSearchRepository.accountNameSearchQueryTerms(keyword)
    const ownerClause = ownerScoped ? 'AND accounts.system_account_id = ?' : ''
    const searchOwnerClause = ownerScoped ? 'search.system_account_id = ? AND' : ''
    const params = [
      ...(ownerScoped ? ['sys_admin'] : []),
      ...(ownerScoped ? ['sys_admin'] : []),
      ...terms,
      normalizedKeyword,
      terms.length
    ]
    const plan = databaseModule.getBusinessDatabase().prepare(`
      EXPLAIN QUERY PLAN
      SELECT accounts.id
      FROM accounts
      LEFT JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE accounts.deleted_at IS NULL
        ${ownerClause}
        AND accounts.id IN (
          SELECT search.account_id
          FROM account_name_search_terms search INDEXED BY idx_account_name_search_terms_term_owner
          INNER JOIN account_name_search_documents documents
            ON documents.account_id = search.account_id
            AND documents.system_account_id = search.system_account_id
          WHERE ${searchOwnerClause} search.term IN (${terms.map(() => '?').join(', ')})
            AND instr(documents.normalized_name, ?) > 0
          GROUP BY search.account_id
          HAVING COUNT(DISTINCT search.term) = ?
        )
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
      ORDER BY (accounts.last_used_at IS NULL) ASC, accounts.last_used_at DESC, accounts.name ASC, accounts.id ASC
      LIMIT 21 OFFSET 0
    `).all(...params) as unknown as Array<{ detail: string }>
    const details = plan.map((row) => row.detail).join(' | ')
    assert.match(details, /idx_account_name_search_terms_term_owner/, `关键词候选必须使用词项索引：${details}`)
    assert.doesNotMatch(details, /SCAN accounts(?:\s|$)/i, `关键词查询不得全表扫描账户：${details}`)
  }
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getData<T>(url: string, cookie: string): Promise<T> {
  const response = await fetch(url, { headers: { cookie } })
  const body = await response.text()
  assert.equal(response.ok, true, `${url} HTTP ${response.status}: ${body}`)
  return (JSON.parse(body) as { data: T }).data
}

async function assertStatus(url: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(url, { headers: { cookie } })
  assert.equal(response.status, expectedStatus, `${message}: ${await response.text()}`)
}

async function onceListening(listeningServer: Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: Server): Promise<void> {
  if (!listeningServer) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
