import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const dispatchCandidateLimit = 256
const tempRoot = resolve(tmpdir(), `juhe-ai-gateway-dispatch-candidate-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'gateway-dispatch-candidate-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '调度候选窗口回归分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const allowedSchedule = {
    enabled: true,
    timezone: 'UTC',
    windows: [
      { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '00:00', end: '23:59' },
      { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '23:59', end: '00:00' }
    ]
  }
  const futureSchedule = {
    ...allowedSchedule,
    dateRange: { startDate: '2999-01-01' }
  }

  const expectedAlwaysAvailableIds: string[] = []
  for (let index = 0; index < dispatchCandidateLimit + 64; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `调度窗口普通账号 ${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-dispatch-window-normal-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id,
      priority: index
    }, access)
    if (index < dispatchCandidateLimit) {
      expectedAlwaysAvailableIds.push(account.id)
    }
  }

  const cooledAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '调度窗口冷却中账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-cooling',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    priority: -100
  }, access)
  const scheduledAllowedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '调度窗口允许计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-scheduled-allowed',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    priority: -50,
    availabilitySchedule: allowedSchedule
  }, access)
  const scheduledDeniedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '调度窗口停用计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-scheduled-denied',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    priority: -75,
    availabilitySchedule: futureSchedule
  }, access)

  const database = databaseModule.getBusinessDatabase()
  database
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run('2999-01-01T00:00:00.000Z', cooledAccount.id)

  const candidatePlans = explainDispatchCandidateWindowQueries(group.id, access.systemAccountId)
  assert(candidatePlans.length >= 2, '回归应覆盖有计划和无计划两条候选窗口查询')
  for (const plan of candidatePlans) {
    assert(plan.includes('idx_group_accounts_dispatch_candidate_window'), `调度候选窗口应使用覆盖排序索引，实际计划：${plan}`)
    assert(!/USE TEMP B-TREE/i.test(plan), `调度候选窗口不应使用临时排序树，实际计划：${plan}`)
  }

  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[]; rowCount: number }> = []
  const supportedModelHydrationCalls: SQLInputValue[][] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+group_accounts\s+INDEXED\s+BY\s+idx_group_accounts_dispatch_candidate_window\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const rows = originalAll(...params)
        capturedCalls.push({ sql, params, rowCount: rows.length })
        return rows
      }) as typeof statement.all
    }
    if (/^\s*SELECT\s+account_id,\s+model\b/i.test(sql) && /\bFROM\s+account_supported_models\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        supportedModelHydrationCalls.push(params)
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const result = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId)
    const ids = new Set(result.accounts.map((account) => account.id))

    assert.equal(result.accounts.length, dispatchCandidateLimit, '调度候选返回给网关的账号数组应保持 256 上限')
    assert.equal(ids.has(cooledAccount.id), false, '仍在冷却中的账号应在 SQL 窗口阶段被过滤')
    assert.equal(ids.has(scheduledAllowedAccount.id), true, '带计划但当前允许的账号不能因无计划窗口 LIMIT 被误漏')
    assert.equal(ids.has(scheduledDeniedAccount.id), false, '带计划但当前停用的账号不应进入调度候选')
    assert.equal(result.hasAccountAvailabilitySchedule, true, '候选窗口包含计划账号时应保留计划重校验标记')
    for (const accountId of expectedAlwaysAvailableIds.slice(0, dispatchCandidateLimit - 1)) {
      assert(ids.has(accountId), `无计划主窗口账号应稳定进入候选：${accountId}`)
    }
  } finally {
    database.prepare = originalPrepare
  }

  assert.equal(capturedCalls.length, 2, '调度候选读取应拆成有计划/无计划两条有界 SQL，不应按账号逐条查询')
  for (const call of capturedCalls) {
    assert(/\bLIMIT\s+\?/i.test(call.sql), '调度候选 SQL 必须带参数化 LIMIT')
    assert.equal(call.params.at(-1), dispatchCandidateLimit, '调度候选 SQL LIMIT 参数应为 256')
    assert(call.rowCount <= dispatchCandidateLimit, '单条调度候选 SQL 返回行数不应超过 256')
  }
  assert.equal(supportedModelHydrationCalls.length, 1, '候选水合应只读取最终 256 个主候选的模型列表，不能按两个窗口扩大到 512 个')
  assert(supportedModelHydrationCalls[0].length <= dispatchCandidateLimit, '模型水合 IN 参数不应超过调度候选上限')

  console.log('网关调度候选窗口回归通过：分组候选读取按 256 有界窗口返回，冷却前置过滤，计划账号不被无计划窗口误漏，查询计划不使用临时排序树，后续水合不扩大候选集')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function explainDispatchCandidateWindowQueries(groupId: string, systemAccountId: string): string[] {
  const now = new Date().toISOString()
  return [
    'AND accounts.availability_schedule_json IS NULL',
    'AND accounts.availability_schedule_json IS NOT NULL'
  ].map((scheduleClause) => explainDispatchCandidateWindowQuery(groupId, systemAccountId, now, scheduleClause))
}

function explainDispatchCandidateWindowQuery(
  groupId: string,
  systemAccountId: string,
  now: string,
  scheduleClause: string
): string {
  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT group_accounts.account_id
      FROM group_accounts INDEXED BY idx_group_accounts_dispatch_candidate_window
      INNER JOIN accounts ON accounts.id = group_accounts.account_id
      LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE group_accounts.group_id = ?
        AND group_accounts.system_account_id = ?
        AND group_accounts.enabled = 1
        AND accounts.provider_code = 'openai'
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        ${scheduleClause}
        AND COALESCE(source_accounts.type, accounts.type) IN ('api_key', 'oauth')
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        group_accounts.local_fallback_enabled ASC,
        group_accounts.local_super_priority_enabled DESC,
        group_accounts.local_priority ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
      LIMIT ?
    `)
    .all(groupId, systemAccountId, now, now, dispatchCandidateLimit) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').join('\n')
}
