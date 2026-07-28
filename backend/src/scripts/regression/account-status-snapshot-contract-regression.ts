import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import {
  getAccountStatusSnapshot,
  hydrateAccountListPage,
  parseAccountStatusSnapshotAccountIds
} from '../../modules/accounts/account-status-snapshot.service.js'
import {
  accountRuntimeStatusCandidateSourceOptions,
  initialAccountRuntimeStatusCandidateWindow,
  listAccountsPageWithRuntimeStatusFilter,
  nextAccountRuntimeStatusCandidateWindow
} from '../../modules/accounts/account-list-runtime-status-filter.js'
import { logger } from '../../shared/logger.js'
import { todayDateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.deepEqual(
  parseAccountStatusSnapshotAccountIds(' account_b,account_a,account_b '),
  ['account_b', 'account_a'],
  '状态快照 ID 应去空白、去重并保持首次出现顺序'
)
assert.throws(() => parseAccountStatusSnapshotAccountIds(''), /至少选择 1 个账户/)
assert.throws(
  () => parseAccountStatusSnapshotAccountIds(Array.from({ length: 101 }, (_, index) => `account_${index}`).join(',')),
  /最多查询 100 个账户/
)
const fullLengthAccountIds = Array.from({ length: 100 }, (_, index) => `acc_${String(index).padStart(4, '0')}_${'x'.repeat(31)}`)
assert.equal(
  parseAccountStatusSnapshotAccountIds(fullLengthAccountIds.join(',')).length,
  100,
  '状态快照必须接受 100 个真实长度账户 ID'
)
assert.throws(() => parseAccountStatusSnapshotAccountIds(`account_${'x'.repeat(8190)}`), /查询参数过长/)

assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ status: 'active', page: 1, pageSize: 20 }),
  { status: 'active', schedulable: 'all' },
  '正常状态筛选只能从数据库有效状态为 active 的保守候选集读取'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ status: 'temporary_unavailable', page: 1, pageSize: 20 }),
  { status: 'temporary_unavailable,active', schedulable: 'all' },
  '临时不可用可能由 active 运行态降级，候选集必须同时保留 active'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ status: 'rate_limited', schedulable: 'cooling' }),
  { status: undefined, schedulable: 'all' },
  '授权额度可覆盖任意持久状态，限流和冷却筛选不得错误下推后漏结果'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ schedulable: 'enabled' }),
  { status: undefined, schedulable: 'enabled' },
  '可调度筛选可以下推数据库可调度条件，运行态只会继续降级该候选集'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ schedulable: 'disabled' }),
  { status: undefined, schedulable: 'all' },
  '停调筛选必须保留全候选，原始冷却字段可能被更高优先级的非 cooling 状态覆盖'
)

const denseCandidateWindow = initialAccountRuntimeStatusCandidateWindow({
  page: 1,
  pageSize: 20,
  status: 'active'
})
assert.deepEqual(denseCandidateWindow, { page: 1, pageSize: 21 }, '20 行首屏只应读取 21 个候选，不得固定放大四倍')
assert.equal(nextAccountRuntimeStatusCandidateWindow(denseCandidateWindow, {
  requiredMatchCount: 21,
  totalMatchedCount: 21,
  latestCandidateCount: 21,
  latestMatchedCount: 21,
  prefixQueryCount: 1
}), undefined, '首批已获得 pageSize + 1 个命中时必须立即停止扩窗')

const sparseCandidateWindows: Array<{ page: number; pageSize: number }> = []
let sparseWindow: { page: number; pageSize: number } | undefined = denseCandidateWindow
let sparseCoveredRows = 0
let sparsePrefixQueryCount = 0
while (sparseWindow) {
  sparseCandidateWindows.push(sparseWindow)
  const latestCandidateCount = sparseWindow.page === 1
    ? sparseWindow.pageSize - sparseCoveredRows
    : sparseWindow.pageSize
  sparseCoveredRows += latestCandidateCount
  if (sparseWindow.page === 1) sparsePrefixQueryCount += 1
  sparseWindow = nextAccountRuntimeStatusCandidateWindow(sparseWindow, {
    requiredMatchCount: 21,
    totalMatchedCount: 0,
    latestCandidateCount,
    latestMatchedCount: 0,
    prefixQueryCount: sparsePrefixQueryCount
  })
}
assert.deepEqual(sparseCandidateWindows, [
  { page: 1, pageSize: 21 },
  { page: 1, pageSize: 200 },
  { page: 2, pageSize: 200 },
  { page: 3, pageSize: 200 },
  { page: 4, pageSize: 200 },
  { page: 5, pageSize: 200 }
], '零命中极端场景必须快速扩到有界批量，查询数不能随 20 行小页膨胀到数十次')
assert.equal(sparseCoveredRows, 1000, '运行态筛选最多检查管理列表既有的 1000 行窗口')
assert.equal(Math.max(...sparseCandidateWindows.map((window) => window.pageSize)), 200, '单次候选查询批量不得超过 200')

const nearDenseNextWindow = nextAccountRuntimeStatusCandidateWindow(denseCandidateWindow, {
  requiredMatchCount: 21,
  totalMatchedCount: 20,
  latestCandidateCount: 21,
  latestMatchedCount: 20,
  prefixQueryCount: 1
})
assert.deepEqual(nearDenseNextWindow, { page: 1, pageSize: 23 }, '高命中率只应补取估算缺口，不应直接过取 50 或 80 条')
assert.deepEqual(
  initialAccountRuntimeStatusCandidateWindow({ page: 50, pageSize: 20, status: 'active' }),
  { page: 1, pageSize: 200 },
  '深分页首批也必须受 200 行内存边界约束'
)

const repositorySource = readFileSync(resolve('src/storage/account-status-snapshot.repository.ts'), 'utf8')
const runtimeStatusFilterSource = readFileSync(resolve('src/modules/accounts/account-list-runtime-status-filter.ts'), 'utf8')
assert.doesNotMatch(repositorySource, /usage_records/, '状态快照不得扫描使用记录明细')
assert.doesNotMatch(repositorySource, /credentials_encrypted|credential_mask/, '状态快照不得读取凭据或凭据摘要')
assert.match(repositorySource, /loadAccountManagementListUsageAsync/, '今日用量必须来自三字段列表统计读取器')
assert.doesNotMatch(repositorySource, /loadAccountApiKeyRuntime|usage-summary-loaders|AccountUsageSummary/, '状态快照不得回流 API Key 运行态或完整用量读取器')
assert.match(repositorySource, /authorization_effective_source_team_id/, '状态快照必须保留团队授权额度来源字段')
assert.match(repositorySource, /list_account_status_snapshots_read_only/, 'SQLite 状态投影必须投递到 read worker')
assert.doesNotMatch(runtimeStatusFilterSource, /pageSize\s*\*\s*4/, '运行态筛选不得恢复固定四倍候选批量')
assert.match(runtimeStatusFilterSource, /seenCandidateIds/, '前缀扩窗必须去重，重复查询的候选不得重复 hydrate')
assert.match(
  runtimeStatusFilterSource,
  /listOptions\.page\s*<\s*pageUpperBoundForWindow\(pageSize\)/,
  '运行态筛选到达全局可访问页上限后不得继续返回不可访问的 hasMore'
)
assert.match(
  runtimeStatusFilterSource,
  /maxRuntimeStatusHydrationBatchSize\s*=\s*100[\s\S]*chunkValues\(page\.items, maxRuntimeStatusHydrationBatchSize\)/,
  '运行态 hydrate 必须按运行态快照的 100 ID 边界分批，200 候选时不得漏掉后一半运行态'
)
const readWorkerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
assert.match(readWorkerSource, /case 'list_account_status_snapshots_read_only'/, 'SQLite read worker 必须实现状态投影 operation')

const tempRoot = resolve(tmpdir(), `juhe-ai-account-status-snapshot-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-status-snapshot-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, statusSnapshotRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-status-snapshot.repository.js')
])

try {
  const user = repositories.createSystemAccount({
    username: 'account_status_snapshot_user',
    displayName: '账户状态快照用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userAccess = { systemAccountId: user.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({ name: '账户快照回归分组', providerCode: 'gpt' }, userAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户快照回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-status-snapshot', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.4-mini'],
    status: 'active',
    groupId: group.id
  }, userAccess)
  const foreignGroup = repositories.createGroup({ name: '账户快照管理员分组', providerCode: 'gpt' }, adminAccess)
  const foreignAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '账户快照管理员账户',
    type: 'api_key',
    credentials: { api_key: 'sk-account-status-snapshot-admin', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.4-mini'],
    status: 'active',
    groupId: foreignGroup.id
  }, adminAccess)
  const lastUsedAt = '2026-07-16T02:03:04.000Z'
  databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET status = 'active', schedulable = 1, last_used_at = CASE WHEN id = ? THEN ? ELSE last_used_at END WHERE id IN (?, ?)")
    .run(account.id, lastUsedAt, account.id, foreignAccount.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET last_health_check_at = ?, next_health_check_at = ?, last_health_check_status_code = ?,
        last_health_check_error_code = ?, last_health_check_error_message = ?, last_health_check_trace_id = ?
    WHERE id = ?
  `).run(
    '2026-07-20T00:30:00.000Z',
    '2026-07-20T12:30:00.000Z',
    503,
    'model_not_found',
    '测试探针失败',
    'trace-snapshot-latest',
    account.id
  )
  const today = todayDateKey(await usageStatsTimezoneAsync())
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count,
      first_token_ms_max, last_used_at, updated_at
    ) VALUES (?, 'account', ?, ?, 7, 6, 1, 70, 14, 3, 0.001, 0.07, 700, 7, 140, 210, 7, 40, ?, ?)
  `).run(user.id, account.id, today, lastUsedAt, lastUsedAt)
  const result = await getAccountStatusSnapshot(userAccess, [foreignAccount.id, account.id])
  assert.deepEqual(result.items.map((item) => item.id), [account.id], '用户快照必须省略无权查看的账户 ID')
  assert.equal(result.items[0]?.effectiveAvailability.label, '可调度')
  assert.equal(result.items[0]?.todayUsage.requestCount, 7, '状态快照必须读取今日账户预聚合用量')
  assert.deepEqual(Object.keys(result.items[0]?.todayUsage ?? {}).sort(), ['requestCount', 'totalCost', 'totalTokens'])
  assert.equal(result.items[0]?.lastUsedAt, lastUsedAt, '状态快照必须返回账户最近使用时间')
  assert.equal(result.items[0]?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-snapshot-latest', '状态快照必须返回最近检查 traceId')
  assert.equal(result.items[0]?.availabilityPresentation?.probe?.schedule.nextAttemptAt, '2026-07-20T12:30:00.000Z', '状态快照必须返回下次检查时间')
  assert.equal('credentials' in (result.items[0] ?? {}), false, '状态快照响应不得包含凭据')

  const grantee = repositories.createSystemAccount({
    username: 'account_status_snapshot_grantee',
    displayName: '账户状态快照被授权用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeGroup = repositories.createGroup({ name: '账户快照授权目标分组', providerCode: 'gpt' }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '状态快照来源探针窄投影回归'
  }, userAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === account.id)
  assert(authorizedInstance, '账户授权应创建被授权用户作用域内的实例')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'rate_limited', schedulable = 0,
        last_error_code = 'rate_limit_exceeded', last_error_message = '来源账户仍受限流',
        last_error_trace_id = 'trace-source-cooldown', cooldown_retest_last_at = ?,
        cooldown_retest_last_status_code = 429
    WHERE id = ?
  `).run('2026-07-20T01:00:00.000Z', account.id)
  const authorizedProjection = (await statusSnapshotRepository.listAccountStatusProjectionsReadOnly(
    granteeAccess,
    [authorizedInstance.id]
  ))[0]
  assert.equal(authorizedProjection?.sourceAccountProbe?.lastObservation?.traceId, 'trace-source-cooldown', '仓储状态投影应把来源原始诊断列压缩成单个探针事实')
  for (const field of [
    'authorizationInstanceSourceAccountLastErrorTraceId',
    'authorizationInstanceSourceAccountCooldownRetestLastAt',
    'authorizationInstanceSourceAccountCooldownRetestLastStatusCode',
    'authorizationInstanceSourceAccountLastHealthCheckAt',
    'authorizationInstanceSourceAccountNextHealthCheckAt',
    'authorizationInstanceSourceAccountLastHealthCheckStatusCode',
    'authorizationInstanceSourceAccountLastHealthCheckErrorCode',
    'authorizationInstanceSourceAccountLastHealthCheckErrorMessage',
    'authorizationInstanceSourceAccountLastHealthCheckTraceId'
  ]) {
    assert.equal(field in (authorizedProjection ?? {}), false, `仓储状态投影不得向 service 传递来源原始诊断字段 ${field}`)
  }
  const authorizedSnapshot = await getAccountStatusSnapshot(granteeAccess, [authorizedInstance.id])
  const authorizedStatus = authorizedSnapshot.items[0]
  assert.equal(authorizedStatus?.effectiveAvailability.status, 'source_rate_limited')
  assert.equal(authorizedStatus?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-source-cooldown', '来源探针必须在仓储内压缩后保留展示所需 traceId')
  assert.equal(authorizedStatus?.availabilityPresentation?.probe?.lastObservation?.httpStatus, 429, '来源探针必须保留展示所需状态码')
  for (const field of [
    'sourceAccountProbe',
    'authorizationInstanceSourceAccountLastErrorTraceId',
    'authorizationInstanceSourceAccountCooldownRetestLastAt',
    'authorizationInstanceSourceAccountCooldownRetestLastStatusCode',
    'authorizationInstanceSourceAccountLastHealthCheckAt',
    'authorizationInstanceSourceAccountNextHealthCheckAt',
    'authorizationInstanceSourceAccountLastHealthCheckStatusCode',
    'authorizationInstanceSourceAccountLastHealthCheckErrorCode',
    'authorizationInstanceSourceAccountLastHealthCheckErrorMessage',
    'authorizationInstanceSourceAccountLastHealthCheckTraceId'
  ]) {
    assert.equal(field in (authorizedStatus ?? {}), false, `状态快照响应不得泄露来源内部探针字段 ${field}`)
  }
  assert(authorizedInstance.accountAuthorizationId, '授权实例必须保留授权 ID')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE group_accounts
    SET account_authorization_id = NULL
    WHERE account_id = ? AND system_account_id = ?
  `).run(authorizedInstance.id, grantee.id)
  const invalidBindingSnapshot = await getAccountStatusSnapshot(granteeAccess, [authorizedInstance.id])
  assert.equal(
    invalidBindingSnapshot.items[0]?.effectiveAvailability.status,
    'authorization_unavailable',
    '授权实例绑定缺少 authorization ID 时必须与候选 SQL 一致判为失效'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE group_accounts
    SET account_authorization_id = ?
    WHERE account_id = ? AND system_account_id = ?
  `).run(authorizedInstance.accountAuthorizationId, authorizedInstance.id, grantee.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'pending_test', schedulable = 0,
        last_health_check_at = ?, next_health_check_at = ?, last_health_check_status_code = 503,
        last_health_check_error_code = 'model_not_found', last_health_check_error_message = '来源模型不存在',
        last_health_check_trace_id = 'trace-source-health'
    WHERE id = ?
  `).run('2026-07-20T02:00:00.000Z', '2026-07-20T14:00:00.000Z', account.id)
  const sourceHealthSnapshot = await getAccountStatusSnapshot(granteeAccess, [authorizedInstance.id])
  assert.equal(sourceHealthSnapshot.items[0]?.effectiveAvailability.status, 'source_pending_test')
  assert.equal(sourceHealthSnapshot.items[0]?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-source-health', '来源健康探针压缩后必须保留 traceId')
  assert.equal(sourceHealthSnapshot.items[0]?.availabilityPresentation?.probe?.schedule.nextAttemptAt, '2026-07-20T14:00:00.000Z', '来源健康探针压缩后必须保留下次检查时间')
  databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET status = 'rate_limited', schedulable = 0 WHERE id = ?")
    .run(account.id)

  for (let index = 0; index < 105; index += 1) {
    repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `账户快照批量回归账户 ${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-account-status-snapshot-bulk-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      status: 'active',
      groupId: group.id
    }, userAccess)
  }
  const largeBasePage = await repositories.listAccountManagementItemsPageReadOnly(userAccess, {
    page: 1,
    pageSize: 200
  })
  assert(largeBasePage.items.length > 100, '回归夹具必须形成 pageSize > 100 的真实账户页')
  const largeHydratedPage = await hydrateAccountListPage(userAccess, largeBasePage)
  assert.equal(largeHydratedPage.items.length, largeBasePage.items.length, '列表状态投影不得静默截断 100 条之后的账户')
  assert.equal(
    largeHydratedPage.items.every((item) => Boolean(item.effectiveAvailability && item.availabilityPresentation && item.todayUsage)),
    true,
    'pageSize > 100 时每一行都必须完成状态、展示和今日用量投影'
  )
  const snapshotBatchDatabase = databaseModule.getBusinessDatabase()
  const originalSnapshotPrepare = snapshotBatchDatabase.prepare.bind(snapshotBatchDatabase) as typeof snapshotBatchDatabase.prepare
  const statusProjectionBatchSizes: number[] = []
  snapshotBatchDatabase.prepare = ((sql: string) => {
    const statement = originalSnapshotPrepare(sql)
    if (/SELECT\s+accounts\.id,\s*accounts\.system_account_id,\s*accounts\.status,\s*accounts\.schedulable/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        statusProjectionBatchSizes.push(Math.max(0, params.length - 1))
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof snapshotBatchDatabase.prepare
  let largeRuntimeFilteredPage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  try {
    largeRuntimeFilteredPage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 1,
      pageSize: 120,
      status: 'pending_test'
    })
  } finally {
    snapshotBatchDatabase.prepare = originalSnapshotPrepare
  }
  assert(largeRuntimeFilteredPage, '运行态状态筛选必须返回分页结果')
  assert.deepEqual(statusProjectionBatchSizes, [100, 5], '105 个候选必须按 100 ID 运行态快照边界执行两次 hydrate')
  assert.equal(largeRuntimeFilteredPage.items.length, 105, 'pageSize > 100 的运行态状态筛选不得漏掉第 101 条之后的匹配账户')
  assert.equal(
    largeRuntimeFilteredPage.items.every((item) => item.effectiveAvailability.status === 'instance_pending_test'),
    true,
    '运行态筛选返回的每一行都必须使用完整状态投影'
  )
  const disabledPrecedenceAccount = largeBasePage.items.find((item) => item.name.startsWith('账户快照批量回归账户 '))
  assert(disabledPrecedenceAccount, '回归夹具必须包含可用于停调优先级检查的账户')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'error', schedulable = 0, cooldown_until = '2099-01-01T00:00:00.000Z'
    WHERE id = ?
  `).run(disabledPrecedenceAccount.id)
  const disabledPrecedencePage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
    ids: [disabledPrecedenceAccount.id],
    page: 1,
    pageSize: 20,
    schedulable: 'disabled'
  })
  assert(disabledPrecedencePage, '停调运行态筛选必须返回分页结果')
  assert.deepEqual(
    disabledPrecedencePage.items.map((item) => item.id),
    [disabledPrecedenceAccount.id],
    'error 的优先级高于残留 cooldown，停调筛选不得因下推原始 cooling 字段漏掉该账户'
  )
  assert.equal(disabledPrecedencePage.items[0]?.effectiveAvailability.status, 'instance_error')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, cooldown_until = NULL
    WHERE system_account_id = ? AND name LIKE '账户快照批量回归账户 %'
  `).run(user.id)
  const businessDatabase = databaseModule.getBusinessDatabase()
  const originalPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const candidateQueries: Array<{ limit: number; offset: number; status: unknown }> = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/WITH\s+account_rows\s+AS\s*\(/i.test(sql) && /ranked_group_bindings/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        candidateQueries.push({
          limit: Number(params.at(-2)),
          offset: Number(params.at(-1)),
          status: params.at(-3)
        })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare
  let denseActivePage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  try {
    denseActivePage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 1,
      pageSize: 20,
      status: 'active'
    })
  } finally {
    businessDatabase.prepare = originalPrepare
  }
  assert(denseActivePage, '正常状态运行态筛选必须返回分页结果')
  assert.deepEqual(candidateQueries, [
    { limit: 22, offset: 0, status: 'active' }
  ], 'active 首屏候选 SQL 只能执行一次，并只读取 21 个候选加一条 lookahead')
  assert.equal(denseActivePage.items.length, 20, '正常状态首屏必须返回完整 20 行')
  assert.equal(denseActivePage.hasMore, true, '21 个候选命中时必须保留下一页提示')
  assert.equal(
    denseActivePage.items.every((item) => item.effectiveAvailability.available),
    true,
    'active 候选下推后仍必须以完整运行态投影做最终判定'
  )
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('账户状态快照契约回归通过：公开 ID 有界、内部大页完整、来源探针窄投影和可调度状态均正确')
