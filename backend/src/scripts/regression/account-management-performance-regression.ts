import assert from 'node:assert/strict'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import type { AccountListItem } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'

const fixtureAccountCount = 20
const maxListResponseBytes = 64 * 1024
const maxEditBasicResponseBytes = 8 * 1024
const maxMutationResponseBytes = 1 * 1024
const expectedListBusinessQueries = 4
const expectedListStatsQueries = 1
const expectedEditBusinessQueries = 3

const expectedListItemKeys = [
  'accessType',
  'availabilityPresentation',
  'boundGroupId',
  'boundGroupName',
  'circuitSummary',
  'clientCompatibility',
  'concurrencyLimit',
  'configRevision',
  'currentConcurrency',
  'effectiveAvailability',
  'fallbackEnabled',
  'groupBindStatus',
  'healthCheckEndpointMode',
  'healthCheckModel',
  'id',
  'lastUsedAt',
  'name',
  'notes',
  'ownerSystemAccountId',
  'ownerSystemAccountName',
  'permissions',
  'priority',
  'protocolCode',
  'protocolVersion',
  'providerCode',
  'providerProtocolProfileId',
  'schedulable',
  'status',
  'superPriorityEnabled',
  'tags',
  'todayUsage',
  'type'
].sort()

const expectedEditBasicKeys = [
  'boundGroupId',
  'boundGroupName',
  'clientCompatibility',
  'concurrencyLimit',
  'configRevision',
  'credentials',
  'fallbackEnabled',
  'healthCheckEndpointMode',
  'healthCheckModel',
  'id',
  'name',
  'notes',
  'ownerSystemAccountId',
  'priority',
  'protocolCode',
  'protocolVersion',
  'providerCode',
  'providerProtocolProfileId',
  'status',
  'superPriorityEnabled',
  'supportedModels',
  'tags',
  'type'
].sort()

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-account-management-performance-'))
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '0'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

const [
  databaseModule,
  repositories,
  listRepository,
  statusSnapshotRepository,
  editBasicRepository,
  patchRepository,
  statusSnapshotService,
  accountCircuitBridge,
  usageStatsHelpers,
  providerProtocol,
  { logger }
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-management-list.repository.js'),
  import('../../storage/account-status-snapshot.repository.js'),
  import('../../storage/account-edit-basic.repository.js'),
  import('../../storage/account-management-patch.repository.js'),
  import('../../modules/accounts/account-status-snapshot.service.js'),
  import('../../modules/gateway/runtime/account-circuit-control-plane-bridge.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../domain/provider-protocol.js'),
  import('../../shared/logger.js')
])

logger.level = 'silent'

const access: AccessScope = { systemAccountId: 'sys_admin', role: 'user' }
const businessDatabase = databaseModule.getBusinessDatabase()
const statsDatabase = databaseModule.getStatsDatabase()

try {
  const group = repositories.createGroup({
    name: '账户按需性能证据分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const target = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: providerProtocol.GPT_OPENAI_V1_PROFILE_ID,
    name: '按需性能账户-01',
    notes: '固定性能备注-01',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-management-performance-01',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    modelMappings: [],
    healthCheckModel: 'gpt-5.4-mini',
    healthCheckEndpointMode: 'responses_sse',
    tags: ['固定标签-01'],
    groupId: group.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, access)
  assert.equal(target.status, 'active', '固定夹具账户必须直接保持 active')
  const accountIds = [target.id]
  for (let index = 1; index < fixtureAccountCount; index += 1) {
    const suffix = String(index + 1).padStart(2, '0')
    const accountId = `acc_2000000000000_${String(index + 1).padStart(8, '0')}`
    cloneAccountFixture(target.id, accountId, suffix)
    accountIds.push(accountId)
  }

  const statDate = usageStatsHelpers.todayDateKey(await usageStatsHelpers.usageStatsTimezoneAsync())
  seedUsageAndUnusedQuality(accountIds, statDate)

  const listCapture = await timedValue(async () => {
    const page = await listRepository.listAccountManagementItemsPageAsync(access, {
      page: 1,
      pageSize: fixtureAccountCount,
      sorts: [{ field: 'priority', order: 'asc' }]
    })
    return statusSnapshotService.hydrateAccountListPage(access, page)
  })
  const listSqlCapture = await captureSql(async () => {
    const page = await listRepository.listAccountManagementItemsPageReadOnly(access, {
      page: 1,
      pageSize: fixtureAccountCount,
      sorts: [{ field: 'priority', order: 'asc' }]
    })
    const projections = await statusSnapshotRepository.listAccountStatusProjectionsReadOnly(
      access,
      page.items.map((item) => item.id)
    )
    assert(projections.every((item) => item.balanceQueryEnabled !== true), '固定夹具不得额外触发余额快照查询')
    await accountCircuitBridge.loadPublicAccountCircuitSummaries(projections.map((item) => item.runtimeKey))
    return { page, projections }
  })
  const listResponse = wireValue({ data: listCapture.value })
  assert.deepEqual(Object.keys(listResponse), ['data'])
  assert.deepEqual(
    Object.keys(listResponse.data).sort(),
    ['generatedAt', 'hasMore', 'items', 'page', 'pageSize', 'total'].sort(),
    '列表响应根字段必须保持精确白名单'
  )
  assert.equal(listResponse.data.items.length, fixtureAccountCount)
  assert.equal(listResponse.data.total, fixtureAccountCount)
  assert.equal(listResponse.data.hasMore, false)
  assert.equal(listResponse.data.page, 1)
  assert.equal(listResponse.data.pageSize, fixtureAccountCount)
  for (const item of listResponse.data.items) assertExactListItem(item)

  const listBusinessQueries = queryCalls(listSqlCapture.calls, 'business')
  const listStatsQueries = queryCalls(listSqlCapture.calls, 'stats')
  assert.equal(
    listBusinessQueries.length,
    expectedListBusinessQueries,
    `20 行列表业务查询预算应固定为 ${expectedListBusinessQueries}，实际 ${listBusinessQueries.length}`
  )
  assert.equal(
    listStatsQueries.length,
    expectedListStatsQueries,
    `20 行列表统计查询预算应固定为 ${expectedListStatsQueries}，实际 ${listStatsQueries.length}`
  )
  const listSql = listSqlCapture.calls.map((call) => call.sql).join('\n')
  assert.doesNotMatch(listSql, /credentials_encrypted|credential_mask/i, '列表不得读取任何凭据列')
  assert.doesNotMatch(listSql, /account_supported_models|account_model_mappings/i, '列表不得读取模型编辑关系')
  assert.doesNotMatch(listSql, /account_quality_scores|account_management_quality/i, '未展示的质量快照不得进入列表查询')
  assert.doesNotMatch(
    listSql,
    /temporary_unavailable_continuous_probe_enabled/i,
    '持续恢复探活属于编辑配置，不得进入列表查询'
  )
  assert.doesNotMatch(listSql, /\bATTACH\s+DATABASE\b/i, '列表不得为未展示字段挂载统计库')

  const listResponseBytes = Buffer.byteLength(JSON.stringify(listResponse), 'utf8')
  assert(
    listResponseBytes <= maxListResponseBytes,
    `20 行列表未压缩 JSON 必须不超过 ${maxListResponseBytes} 字节，实际 ${listResponseBytes}`
  )

  const editCapture = await captureSql(() => editBasicRepository.findAccountEditBasicDetailAsync(target.id, access))
  const editDetail = wireValue(editCapture.value)
  assert(editDetail, 'edit-basic 应返回固定夹具账户')
  assert.deepEqual(Object.keys(editDetail).sort(), expectedEditBasicKeys, 'edit-basic 必须保持精确字段白名单')
  assert.deepEqual(Object.keys(editDetail.credentials).sort(), [
    'api_key',
    'base_url',
    'supported_endpoint_modes'
  ])
  assert.deepEqual(editDetail.supportedModels, ['gpt-5.4-mini'])
  assert.deepEqual(Object.keys(editDetail.tags[0] ?? {}).sort(), ['id', 'name'])
  assert.equal(queryCalls(editCapture.calls, 'business').length, expectedEditBusinessQueries)
  assert.equal(queryCalls(editCapture.calls, 'stats').length, 0, 'edit-basic 不得访问统计库')
  assert.equal(dmlCalls(editCapture.calls).length, 0, 'edit-basic 必须是纯读取')
  const editBasicResponse = wireValue({ data: editDetail })
  const editBasicResponseBytes = Buffer.byteLength(JSON.stringify(editBasicResponse), 'utf8')
  assert(
    editBasicResponseBytes <= maxEditBasicResponseBytes,
    `edit-basic 未压缩 JSON 必须不超过 ${maxEditBasicResponseBytes} 字节，实际 ${editBasicResponseBytes}`
  )

  const noOpCapture = await captureSql(() => patchRepository.patchAccountManagementAsync(target.id, {
    expectedConfigRevision: editDetail.configRevision,
    notes: editDetail.notes
  }, access))
  assert(noOpCapture.value, 'no-op PATCH 应返回当前账户')
  assert.deepEqual(noOpCapture.value.changedFields, [])
  assert.equal(noOpCapture.value.configRevision, editDetail.configRevision)
  assert.deepEqual(dmlCalls(noOpCapture.calls), [], '相同值 PATCH 必须为零 DML')

  const nextNotes = '只修改备注，不触碰关联表'
  const patchCapture = await captureSql(() => patchRepository.patchAccountManagementAsync(target.id, {
    expectedConfigRevision: noOpCapture.value?.configRevision ?? editDetail.configRevision,
    notes: nextNotes
  }, access))
  assert(patchCapture.value, '单字段 PATCH 应返回变更结果')
  assert.deepEqual(patchCapture.value.changedFields, ['notes'])
  assert.equal(patchCapture.value.configRevision, editDetail.configRevision + 1)
  const patchDml = dmlCalls(patchCapture.calls)
  assert.equal(patchDml.length, 1, '单改备注只能执行一条 DML，关联表必须零写入')
  assert.match(patchDml[0]?.sql ?? '', /\bUPDATE\s+"?accounts"?\b/i)
  assert.deepEqual(
    accountUpdateSetColumns(patchDml[0]?.sql ?? ''),
    ['notes', 'config_revision', 'updated_at'],
    '单改备注的 UPDATE SET 只能包含备注、版本和更新时间'
  )
  assert.equal(queryCalls(patchCapture.calls, 'stats').length, 0, '单改备注不得访问统计库')
  const mutationResponse = wireValue({ data: patchCapture.value })
  const mutationResponseBytes = Buffer.byteLength(JSON.stringify(mutationResponse), 'utf8')
  assert(
    mutationResponseBytes <= maxMutationResponseBytes,
    `单标量 PATCH mutation 未压缩 JSON 必须不超过 ${maxMutationResponseBytes} 字节，实际 ${mutationResponseBytes}`
  )
  const stored = businessDatabase.prepare('SELECT notes, config_revision FROM accounts WHERE id = ?').get(target.id) as {
    notes?: string
    config_revision?: number
  } | undefined
  assert.equal(stored?.notes, nextNotes)
  assert.equal(stored?.config_revision, editDetail.configRevision + 1)

  const evidence = {
    fixtureAccounts: fixtureAccountCount,
    list: {
      businessQueries: listBusinessQueries.length,
      statsQueries: listStatsQueries.length,
      jsonBytes: listResponseBytes,
      elapsedMs: roundedMilliseconds(listCapture.elapsedMs)
    },
    editBasic: {
      businessQueries: queryCalls(editCapture.calls, 'business').length,
      statsQueries: queryCalls(editCapture.calls, 'stats').length,
      jsonBytes: editBasicResponseBytes,
      elapsedMs: roundedMilliseconds(editCapture.elapsedMs)
    },
    notesPatch: {
      updateSetColumns: accountUpdateSetColumns(patchDml[0]?.sql ?? ''),
      jsonBytes: mutationResponseBytes,
      dmlCount: patchDml.length,
      relatedTableWrites: 0,
      noOpDmlCount: dmlCalls(noOpCapture.calls).length
    }
  }
  console.log(`account-management-performance-regression passed ${JSON.stringify(evidence)}`)
} finally {
  const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertExactListItem(item: AccountListItem): void {
  assert.deepEqual(Object.keys(item).sort(), expectedListItemKeys, '列表项必须保持固定 20 账户夹具的精确字段白名单')
  assert.deepEqual(Object.keys(item.availabilityPresentation as object).sort(), ['action', 'label', 'status'])
  assert.deepEqual(Object.keys(item.circuitSummary as object).sort(), ['status'])
  assert.deepEqual(Object.keys(item.effectiveAvailability as object).sort(), ['available', 'color', 'label', 'status'])
  assert.equal(item.healthCheckModel, 'gpt-5.4-mini')
  assert.equal(item.healthCheckEndpointMode, 'responses_sse')
  assert.deepEqual(Object.keys(item.todayUsage as object).sort(), ['requestCount', 'totalCost', 'totalTokens'])
  assert.deepEqual(Object.keys(item.permissions as object).sort(), [
    'canAuthorize',
    'canDelete',
    'canEdit',
    'canReturnAuthorization',
    'canUse',
    'canViewCredentials'
  ])
  const tags = item.tags as Array<Record<string, unknown>>
  assert.equal(tags.length, 1)
  assert.deepEqual(Object.keys(tags[0] ?? {}).sort(), ['id', 'name'])
}

function cloneAccountFixture(templateAccountId: string, accountId: string, suffix: string): void {
  const columns = businessDatabase.prepare('PRAGMA table_info(accounts)').all() as Array<{ name?: string }>
  const names = columns.map((column) => column.name).filter((name): name is string => Boolean(name))
  const replacements = new Map<string, unknown>([
    ['id', accountId],
    ['name', `按需性能账户-${suffix}`],
    ['notes', `固定性能备注-${suffix}`],
    ['credential_fingerprint', `account-management-performance-${suffix}`],
    ['credential_mask', `sk-performance-${suffix}`],
    ['created_at', '2026-07-28T00:00:00.000Z'],
    ['updated_at', '2026-07-28T00:00:00.000Z']
  ])
  const params: SQLInputValue[] = []
  const selectExpressions = names.map((name) => {
    if (!replacements.has(name)) return `"${name}"`
    params.push(replacements.get(name) as SQLInputValue)
    return '?'
  })
  params.push(templateAccountId)
  businessDatabase.prepare(`
    INSERT INTO accounts (${names.map((name) => `"${name}"`).join(', ')})
    SELECT ${selectExpressions.join(', ')}
    FROM accounts
    WHERE id = ?
  `).run(...params)
  businessDatabase.prepare(`
    INSERT INTO group_accounts (
      system_account_id, group_id, account_id, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled,
      enabled, created_at, updated_at
    )
    SELECT system_account_id, group_id, ?, account_authorization_id,
      local_priority, local_super_priority_enabled, local_fallback_enabled,
      enabled, created_at, updated_at
    FROM group_accounts
    WHERE account_id = ?
  `).run(accountId, templateAccountId)
  businessDatabase.prepare(`
    INSERT INTO account_tag_bindings (account_id, tag_id, system_account_id, created_at)
    SELECT ?, tag_id, system_account_id, created_at
    FROM account_tag_bindings
    WHERE account_id = ?
  `).run(accountId, templateAccountId)
}

function seedUsageAndUnusedQuality(accountIds: string[], statDate: string): void {
  const timestamp = '2026-07-28T00:00:00.000Z'
  const placeholders = accountIds.map(() => '?').join(', ')
  businessDatabase.prepare(`UPDATE accounts SET last_used_at = ? WHERE id IN (${placeholders})`)
    .run(timestamp, ...accountIds)
  const total = statsDatabase.prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id,
      request_count, input_tokens, output_tokens, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, ?, ?, ?, ?)
  `)
  const daily = statsDatabase.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, input_tokens, output_tokens, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  const quality = statsDatabase.prepare(`
    INSERT INTO account_quality_scores (
      account_id, system_account_id, provider_code, quality_score, quality_state,
      recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
      recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
      window_started_at, window_ended_at, updated_at
    ) VALUES (?, ?, 'gpt', 999, 'fresh', 10, 10, 0, 10, 123, 123, 1, ?, ?, ?)
  `)
  for (let index = 0; index < accountIds.length; index += 1) {
    const accountId = accountIds[index] as string
    const requestCount = index + 1
    total.run(access.systemAccountId, accountId, requestCount, 100 + index, 20 + index, 0.001 + index / 1000, timestamp, timestamp)
    daily.run(access.systemAccountId, accountId, statDate, requestCount, 100 + index, 20 + index, 0.001 + index / 1000, timestamp, timestamp)
    quality.run(accountId, access.systemAccountId, timestamp, timestamp, timestamp)
  }
}

type DatabaseRole = 'business' | 'stats'
type SqlMethod = 'get' | 'all' | 'run' | 'exec'

interface SqlCall {
  database: DatabaseRole
  method: SqlMethod
  sql: string
  params: SQLInputValue[]
}

async function captureSql<T>(action: () => Promise<T>): Promise<{ value: T; calls: SqlCall[]; elapsedMs: number }> {
  const calls: SqlCall[] = []
  const restoreBusiness = installSqlCapture(businessDatabase, 'business', calls)
  const restoreStats = installSqlCapture(statsDatabase, 'stats', calls)
  const startedAt = performance.now()
  try {
    const value = await action()
    return { value, calls, elapsedMs: performance.now() - startedAt }
  } finally {
    restoreStats()
    restoreBusiness()
  }
}

async function timedValue<T>(action: () => Promise<T>): Promise<{ value: T; elapsedMs: number }> {
  const startedAt = performance.now()
  const value = await action()
  return { value, elapsedMs: performance.now() - startedAt }
}

function installSqlCapture(database: DatabaseSync, role: DatabaseRole, calls: SqlCall[]): () => void {
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const originalExec = database.exec.bind(database) as typeof database.exec
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      calls.push({ database: role, method: 'get', sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      calls.push({ database: role, method: 'all', sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      calls.push({ database: role, method: 'run', sql, params })
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  database.exec = ((sql: string) => {
    calls.push({ database: role, method: 'exec', sql, params: [] })
    return originalExec(sql)
  }) as typeof database.exec
  return () => {
    database.prepare = originalPrepare
    database.exec = originalExec
  }
}

function queryCalls(calls: SqlCall[], role: DatabaseRole): SqlCall[] {
  return calls.filter((call) => (
    call.database === role
    && (call.method === 'get' || call.method === 'all')
    && /^(?:SELECT|WITH|PRAGMA|EXPLAIN)\b/i.test(call.sql.trim())
  ))
}

function dmlCalls(calls: SqlCall[]): SqlCall[] {
  return calls.filter((call) => (
    (call.method === 'run' || call.method === 'exec')
    && /\b(?:INSERT(?:\s+OR\s+\w+)?\s+INTO|UPDATE|DELETE\s+FROM|REPLACE\s+INTO)\b/i.test(call.sql)
  ))
}

function accountUpdateSetColumns(sql: string): string[] {
  const match = sql.match(/\bUPDATE\s+"?accounts"?\s+SET\s+([\s\S]*?)\s+WHERE\b/i)
  assert(match, '应捕获 accounts UPDATE SET 子句')
  return [...match[1].matchAll(/(?:^|,)\s*"?([a-z_][a-z0-9_]*)"?\s*=/gi)].map((item) => item[1] as string)
}

function wireValue<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function roundedMilliseconds(value: number): number {
  return Math.round(value * 1000) / 1000
}
