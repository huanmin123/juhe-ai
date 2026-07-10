import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { EligibleOpenAIGroupAccountSelection, OpenAIGroupAccountSelectionRow } from '../../storage/openai-account-selector.types.js'

type OrderGatewayDispatchCandidateRowsForDispatch =
  typeof import('../../storage/gateway-dispatch-candidate-window.repository.js')['orderGatewayDispatchCandidateRowsForDispatch']

const dispatchCandidateLimit = 256
const dispatchCandidateScanLimit = dispatchCandidateLimit * 2
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

const [databaseModule, repositories, candidateWindowRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/gateway-dispatch-candidate-window.repository.js')
])

try {
  assertCandidateWindowQualityTieBreak(candidateWindowRepository.orderGatewayDispatchCandidateRowsForDispatch)

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '调度候选窗口回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const allowedSchedule = {
    enabled: true,
    timezone: 'UTC',
    mode: 'allow_windows',
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
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `调度窗口普通账号 ${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-dispatch-window-normal-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      groupId: group.id,
      priority: index + 10
    }, access)
    if (index < dispatchCandidateLimit) {
      expectedAlwaysAvailableIds.push(account.id)
    }
  }

  const cooledAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '调度窗口冷却中账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-cooling',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    priority: 1
  }, access)
  const scheduledAllowedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '调度窗口允许计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-scheduled-allowed',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    priority: 3,
    availabilitySchedule: allowedSchedule
  }, access)
  const scheduledDeniedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '调度窗口停用计划账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-dispatch-window-scheduled-denied',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    priority: 2,
    availabilitySchedule: futureSchedule
  }, access)

  const database = databaseModule.getBusinessDatabase()
  database
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run('2999-01-01T00:00:00.000Z', cooledAccount.id)

  assertModelAwareCandidateWindowCanPullLateDeterministicAccount(repositories, access)

  const candidatePlans = explainDispatchCandidateWindowQueries(group.id, access.systemAccountId)
  assert(candidatePlans.length === 1, '调度候选应使用一条已状态化的候选窗口查询')
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
    assert(result.diagnostics, '调度候选窗口应返回轻量诊断字段')
    assert.equal(result.diagnostics.scanLimit, dispatchCandidateScanLimit, '诊断应记录候选扫描窗口上限')
    assert.equal(result.diagnostics.finalLimit, dispatchCandidateLimit, '诊断应记录最终候选上限')
    assert.equal(result.diagnostics.candidateRowCount, dispatchCandidateLimit + 65, '诊断应记录派生状态过滤后的候选窗口行数')
    assert.equal(result.diagnostics.scannedRowCount, dispatchCandidateLimit + 65, '诊断应记录候选窗口扫描总数')
    assert.equal(result.diagnostics.eligibleRowCount, dispatchCandidateLimit + 65, '诊断应记录运行态过滤后的可用候选数')
    assert.equal(result.diagnostics.hydrationBatchCount, 1, '首批 256 个候选成功水合时不应继续扩大水合窗口')
    assert.equal(result.diagnostics.hydratedAccountCount, dispatchCandidateLimit, '诊断应记录成功水合账号数')
    assert.equal(result.diagnostics.hydrationDroppedCount, 0, '首批候选凭据完整时不应出现水合丢弃')
    assert.equal(result.diagnostics.finalAccountCount, dispatchCandidateLimit, '诊断应记录最终返回给网关的候选数')
    assert.equal(result.diagnostics.scanLimitReached, false, '候选行数未触达 512 时不应标记扫描窗口截断')
    for (const accountId of expectedAlwaysAvailableIds.slice(0, dispatchCandidateLimit - 1)) {
      assert(ids.has(accountId), `无计划主窗口账号应稳定进入候选：${accountId}`)
    }

    assert.equal(capturedCalls.length, 1, '调度候选读取应使用一条有界 SQL，不应按账号逐条查询或按计划分桶')
    assert.equal(supportedModelHydrationCalls.length, 1, '候选水合应只读取最终 256 个主候选的模型列表，不能按两个窗口扩大到 512 个')

    const refillGroup = repositories.createGroup({
      name: '调度候选补齐回归分组',
      providerCode: 'gpt',
      enabled: true
    }, access)
    const brokenAccountIds: string[] = []
    for (let index = 0; index < dispatchCandidateLimit; index += 1) {
      const account = repositories.createAccount({
        providerCode: 'gpt',
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: `调度窗口损坏凭据账号 ${String(index).padStart(3, '0')}`,
        type: 'api_key',
        credentials: {
          api_key: `sk-dispatch-window-broken-${index}`,
          base_url: 'https://api.openai.com/v1'
        },
        status: 'active',
        groupId: refillGroup.id,
        priority: index
      }, access)
      brokenAccountIds.push(account.id)
    }
    const refillAccountIds: string[] = []
    for (let index = 0; index < 8; index += 1) {
      const account = repositories.createAccount({
        providerCode: 'gpt',
        providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
        name: `调度窗口补齐可用账号 ${String(index).padStart(3, '0')}`,
        type: 'api_key',
        credentials: {
          api_key: `sk-dispatch-window-refill-${index}`,
          base_url: 'https://api.openai.com/v1'
        },
        status: 'active',
        groupId: refillGroup.id,
        priority: dispatchCandidateLimit + index
      }, access)
      refillAccountIds.push(account.id)
    }
    database
      .prepare(`UPDATE accounts SET credentials_encrypted = ? WHERE id IN (${brokenAccountIds.map(() => '?').join(', ')})`)
      .run('broken-credentials', ...brokenAccountIds)

    const capturedCallsBeforeRefill = capturedCalls.length
    const hydrationCallsBeforeRefill = supportedModelHydrationCalls.length
    const refillResult = repositories.listOpenAIAccountsForGroupResult(refillGroup.id, access.systemAccountId)
    const refillIds = new Set(refillResult.accounts.map((account) => account.id))

    assert.equal(refillResult.accounts.length, refillAccountIds.length, '前 256 个候选水合失败后，应继续扫描后续窗口补齐可用账号')
    for (const accountId of refillAccountIds) {
      assert(refillIds.has(accountId), `后续窗口可用账号应进入最终候选：${accountId}`)
    }
    for (const accountId of brokenAccountIds) {
      assert.equal(refillIds.has(accountId), false, `凭据损坏账号不应进入最终候选：${accountId}`)
    }
    assert.equal(capturedCalls.length - capturedCallsBeforeRefill, 1, '补齐场景仍应只读取一条有界 SQL')
    assert.equal(supportedModelHydrationCalls.length - hydrationCallsBeforeRefill, 2, '补齐场景应先水合失败窗口，再水合后续补齐窗口')
    assert(refillResult.diagnostics, '补齐场景应返回轻量诊断字段')
    assert.equal(refillResult.diagnostics.scanLimit, dispatchCandidateScanLimit, '补齐诊断应记录候选扫描窗口上限')
    assert.equal(refillResult.diagnostics.finalLimit, dispatchCandidateLimit, '补齐诊断应记录最终候选上限')
    assert.equal(refillResult.diagnostics.candidateRowCount, dispatchCandidateLimit + refillAccountIds.length, '补齐诊断应记录候选窗口行数')
    assert.equal(refillResult.diagnostics.scannedRowCount, dispatchCandidateLimit + refillAccountIds.length, '补齐诊断应记录候选扫描总数')
    assert.equal(refillResult.diagnostics.eligibleRowCount, dispatchCandidateLimit + refillAccountIds.length, '补齐诊断应记录运行态可用候选数')
    assert.equal(refillResult.diagnostics.hydrationBatchCount, 2, '补齐场景应记录两批水合')
    assert.equal(refillResult.diagnostics.hydratedAccountCount, refillAccountIds.length, '补齐诊断应记录成功水合账号数')
    assert.equal(refillResult.diagnostics.hydrationDroppedCount, dispatchCandidateLimit, '补齐诊断应记录凭据损坏导致的水合丢弃数')
    assert.equal(refillResult.diagnostics.finalAccountCount, refillAccountIds.length, '补齐诊断应记录最终候选数')
    assert.equal(refillResult.diagnostics.scanLimitReached, false, '补齐候选行数未触达 512 时不应标记扫描窗口截断')
  } finally {
    database.prepare = originalPrepare
  }

  for (const call of capturedCalls) {
    assert(/\bLIMIT\s+\?/i.test(call.sql), '调度候选 SQL 必须带参数化 LIMIT')
    assert.equal(call.params.at(-1), dispatchCandidateScanLimit, '调度候选 SQL 扫描窗口参数应为 512')
    assert(call.rowCount <= dispatchCandidateScanLimit, '单条调度候选 SQL 返回行数不应超过扫描窗口上限')
  }
  for (const params of supportedModelHydrationCalls) {
    assert(params.length <= dispatchCandidateLimit, '模型水合 IN 参数不应超过调度候选上限')
  }

  console.log('网关调度候选窗口回归通过：分组候选按 512 扫描窗口读取、最终候选保持 256 上限，冷却和计划派生状态前置过滤，水合失败时可继续补齐后续可用账号，查询计划不使用临时排序树，后续水合不扩大候选集')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertCandidateWindowQualityTieBreak(
  orderRows: OrderGatewayDispatchCandidateRowsForDispatch
): void {
  const orderedIds = orderRows([
    candidateWindowRow('same-bucket-slow', { priority: 0, superPriorityEnabled: true, qualityScore: 500 }),
    candidateWindowRow('fallback-fast', { priority: 0, fallbackEnabled: true, qualityScore: 1 }),
    candidateWindowRow('same-bucket-fast', { priority: 0, superPriorityEnabled: true, qualityScore: 100 }),
    candidateWindowRow('better-priority-slower', { priority: -1, superPriorityEnabled: true, qualityScore: 900 }),
    candidateWindowRow('same-bucket-no-quality', { priority: 0, superPriorityEnabled: true }),
    candidateWindowRow('normal-fast', { priority: 0, qualityScore: 1 })
  ]).map((item) => item.row.id)

  assert.deepEqual(
    orderedIds,
    [
      'better-priority-slower',
      'same-bucket-fast',
      'same-bucket-slow',
      'same-bucket-no-quality',
      'normal-fast',
      'fallback-fast'
    ],
    '调度候选窗口应仅在同一 fallback/super/priority 桶内按质量分优先，不能让质量分越过业务优先级'
  )
}

function candidateWindowRow(
  id: string,
  options: {
    priority: number
    superPriorityEnabled?: boolean
    fallbackEnabled?: boolean
    qualityScore?: number
  }
): EligibleOpenAIGroupAccountSelection {
  const row: OpenAIGroupAccountSelectionRow = {
    account_id: id,
    binding_system_account_id: 'sys_admin',
    group_id: 'candidate-window-regression',
    account_authorization_id: null,
    local_priority: options.priority,
    local_super_priority_enabled: options.superPriorityEnabled ? 1 : 0,
    local_fallback_enabled: options.fallbackEnabled ? 1 : 0,
    id,
    system_account_id: 'sys_admin',
    provider_code: 'gpt',
    provider_protocol_profile_id: 'gpt-openai-v1',
    protocol_code: 'openai',
    protocol_version: 'v1',
    name: id,
    type: 'api_key',
    status: 'active',
    schedulable: 1,
    concurrency_limit: 20,
    priority: options.priority,
    super_priority_enabled: options.superPriorityEnabled ? 1 : 0,
    fallback_enabled: options.fallbackEnabled ? 1 : 0,
    client_compatibility: 'openai_standard',
    credentials_encrypted: '{}',
    proxy_profile_id: null,
    cooldown_until: null,
    last_error_message: null,
    stream_failure_count: 0,
    stream_failure_window_started_at: null,
    account_expires_at: null,
    default_test_model: null,
    quality_score: options.qualityScore ?? null,
    quality_state: null,
    quality_ewma_first_token_ms: null
  }
  return {
    row,
    accountAccess: {
      accountAccessType: 'owner',
      accountOwnerSystemAccountId: 'sys_admin'
    }
  }
}

function assertModelAwareCandidateWindowCanPullLateDeterministicAccount(
  repositories: typeof import('../../storage/repositories.js'),
  access: { systemAccountId: string; role: 'admin' }
): void {
  const group = repositories.createGroup({
    name: '模型感知候选窗口回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  for (let index = 0; index < dispatchCandidateScanLimit + 8; index += 1) {
    repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `模型窗口不匹配账号 ${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-model-window-unsupported-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      status: 'active',
      groupId: group.id,
      priority: index,
      supportedModels: ['gpt-5.4']
    }, access)
  }
  const deterministicAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '模型窗口显式命中账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-model-window-deterministic',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    priority: dispatchCandidateScanLimit + 99,
    supportedModels: ['gpt-5.5']
  }, access)

  const result = repositories.listOpenAIAccountsForGroupResult(group.id, access.systemAccountId, {
    requestedModel: 'gpt-5.5'
  })

  assert.equal(result.accounts[0]?.id, deterministicAccount.id, '请求带模型时，显式支持该模型的账号即使排在普通候选窗口之后也应被拉入候选首位')
}

function explainDispatchCandidateWindowQueries(groupId: string, systemAccountId: string): string[] {
  const now = new Date().toISOString()
  return [explainDispatchCandidateWindowQuery(groupId, systemAccountId, now)]
}

function explainDispatchCandidateWindowQuery(
  groupId: string,
  systemAccountId: string,
  now: string
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
        AND accounts.provider_code = 'gpt'
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.cooldown_until IS NULL OR accounts.cooldown_until <= ?)
        AND (
          (accounts.authorization_instance_authorization_id IS NULL AND accounts.type IN ('api_key', 'oauth'))
          OR (
            accounts.authorization_instance_authorization_id IS NOT NULL
            AND source_accounts.type IN ('api_key', 'oauth')
          )
        )
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY
        group_accounts.local_fallback_enabled ASC,
        group_accounts.local_super_priority_enabled DESC,
        group_accounts.local_priority ASC,
        group_accounts.created_at ASC,
        group_accounts.account_id ASC
      LIMIT ?
    `)
    .all(groupId, systemAccountId, now, now, dispatchCandidateScanLimit) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').join('\n')
}
