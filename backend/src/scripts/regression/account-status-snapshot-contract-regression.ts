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
  AccountRuntimeStatusFilterScanLimitError,
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
  { status: undefined, schedulable: 'all' },
  '运行态、授权和到期事实可能覆盖持久状态，active 候选不得提前下推'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ status: 'temporary_unavailable', page: 1, pageSize: 20 }),
  { status: undefined, schedulable: 'all' },
  '临时不可用候选不得依赖持久状态推断'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ status: 'rate_limited', schedulable: 'cooling' }),
  { status: undefined, schedulable: 'all' },
  '授权额度可覆盖任意持久状态，限流和冷却筛选不得错误下推后漏结果'
)
assert.deepEqual(
  accountRuntimeStatusCandidateSourceOptions({ schedulable: 'enabled' }),
  { status: undefined, schedulable: 'all' },
  '授权绑定、账户到期和运行态会改变可调度性，enabled 候选不得提前下推'
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
  latestMatchedCount: 21
}), undefined, '首批已获得 pageSize + 1 个命中时必须立即停止扩窗')

const sparseCandidateWindows: Array<{ page: number; pageSize: number }> = []
let sparseWindow: { page: number; pageSize: number } | undefined = denseCandidateWindow
let sparseCoveredRows = 0
for (let iteration = 0; sparseWindow && iteration < 5; iteration += 1) {
  sparseCandidateWindows.push(sparseWindow)
  const latestCandidateCount = sparseWindow.page === 1
    ? sparseWindow.pageSize - sparseCoveredRows
    : sparseWindow.pageSize
  sparseCoveredRows += latestCandidateCount
  sparseWindow = nextAccountRuntimeStatusCandidateWindow(sparseWindow, {
    requiredMatchCount: 21,
    totalMatchedCount: 0,
    latestCandidateCount,
    latestMatchedCount: 0
  })
}
assert.deepEqual(sparseCandidateWindows, [
  { page: 1, pageSize: 21 },
  { page: 1, pageSize: 200 },
  { page: 1, pageSize: 400 },
  { page: 1, pageSize: 800 },
  { page: 1, pageSize: 1600 }
], '零命中极端场景必须使用递增前缀而非 OFFSET 分段，并在 200 行后指数扩容')
assert.equal(sparseCoveredRows, 1600, '递增前缀不得停在旧的 1000 行管理窗口')
assert.equal(sparseCandidateWindows.every((window) => window.page === 1), true, '候选扩容必须始终从有序结果前缀开始')

const nearDenseNextWindow = nextAccountRuntimeStatusCandidateWindow(denseCandidateWindow, {
  requiredMatchCount: 21,
  totalMatchedCount: 20,
  latestCandidateCount: 21,
  latestMatchedCount: 20
})
assert.deepEqual(nearDenseNextWindow, { page: 1, pageSize: 23 }, '高命中率只应补取估算缺口，不应直接过取 50 或 80 条')
assert.deepEqual(
  initialAccountRuntimeStatusCandidateWindow({ page: 50, pageSize: 20, status: 'active' }),
  { page: 1, pageSize: 200 },
  '深分页首批也必须受 200 行内存边界约束'
)
assert.throws(
  () => initialAccountRuntimeStatusCandidateWindow({ page: 501, pageSize: 20, status: 'active' }),
  AccountRuntimeStatusFilterScanLimitError,
  '超过 10000 候选扫描预算的深页必须显式失败，不得回压页码或生成无界 LIMIT'
)
assert.throws(
  () => initialAccountRuntimeStatusCandidateWindow({ page: 1e308, pageSize: 20, status: 'active' }),
  /安全整数范围/,
  '非安全整数页码必须在进入 SQL 前拒绝'
)
assert.equal(nextAccountRuntimeStatusCandidateWindow({ page: 1, pageSize: 10_000 }, {
  requiredMatchCount: 21,
  totalMatchedCount: 0,
  latestCandidateCount: 10_000,
  latestMatchedCount: 0
}), undefined, '达到扫描预算后不得继续扩大候选前缀')

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
assert.doesNotMatch(
  runtimeStatusFilterSource,
  /pageUpperBoundForWindow|defaultListWindowRows|maxCandidateRows/,
  '运行态筛选不得保留 1000 行窗口或页码回压'
)
assert.match(runtimeStatusFilterSource, /listAccountManagementCandidatePrefixAsync/, '运行态筛选必须使用专用递增前缀读取入口')
assert.match(
  runtimeStatusFilterSource,
  /maxRuntimeStatusHydrationBatchSize\s*=\s*100[\s\S]*chunkValues\(page\.items, maxRuntimeStatusHydrationBatchSize\)/,
  '运行态 hydrate 必须按运行态快照的 100 ID 边界分批，200 候选时不得漏掉后一半运行态'
)
const readWorkerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
assert.match(readWorkerSource, /case 'list_account_status_snapshots_read_only'/, 'SQLite read worker 必须实现状态投影 operation')
assert.match(
  readWorkerSource,
  /case 'hydrate_account_management_status_seeds_read_only':[\s\S]*hydrateAccountManagementStatusSeedsReadOnly\(operation\.seeds\)/,
  'SQLite read worker 必须直接水合管理列表最小 seed，不得回退按 ID 重查账户'
)
assert.match(readWorkerSource, /listAccountManagementItemsPageReadOnly\(operation\.access, operation\.options, operation\.candidateLimit\)/, 'SQLite read worker 必须透传内部候选前缀上限')
const readWorkerTypesSource = readFileSync(resolve('src/storage/sqlite-read-worker-pool.types.ts'), 'utf8')
assert.match(readWorkerTypesSource, /type: 'list_account_management_items_page_read_only'[\s\S]*candidateLimit\?: number/, 'SQLite read worker operation 必须声明候选前缀上限')
assert.match(readWorkerTypesSource, /type: 'hydrate_account_management_status_seeds_read_only'[\s\S]*seeds: AccountManagementStatusSeed\[\]/, 'SQLite read worker 必须声明最小状态 seed operation')

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

const [databaseModule, repositories, statusSnapshotRepository, sqliteReadWorkerPool] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-status-snapshot.repository.js'),
  import('../../storage/sqlite-read-worker-pool.js')
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
  const invalidBindingBasePage = await repositories.listAccountManagementItemsPageReadOnly(granteeAccess, {
    ids: [authorizedInstance.id],
    page: 1,
    pageSize: 20,
    status: 'disabled'
  })
  assert.equal(invalidBindingBasePage.items[0]?.groupBindStatus, 'authorization_unavailable', '窄列表与状态快照必须一致地把 NULL binding authorization 判为失效')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE group_accounts
    SET account_authorization_id = ?
    WHERE account_id = ? AND system_account_id = ?
  `).run(authorizedInstance.accountAuthorizationId, authorizedInstance.id, grantee.id)
  databaseModule.getBusinessDatabase().prepare("UPDATE accounts SET status = 'active', schedulable = 1 WHERE id IN (?, ?)")
    .run(account.id, authorizedInstance.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET account_expires_at = '2000-01-01T00:00:00.000Z'
    WHERE id = ?
  `).run(authorizedInstance.id)
  const expiredInstanceBasePage = await repositories.listAccountManagementItemsPageReadOnly(granteeAccess, {
    ids: [authorizedInstance.id],
    page: 1,
    pageSize: 20,
    status: 'disabled'
  })
  assert.deepEqual(expiredInstanceBasePage.items.map((item) => item.id), [authorizedInstance.id], '授权实例自身到期必须进入候选 SQL 的 disabled 判定')
  const expiredInstancePage = await listAccountsPageWithRuntimeStatusFilter(granteeAccess, {
    ids: [authorizedInstance.id],
    page: 1,
    pageSize: 20,
    status: 'disabled'
  })
  assert.equal(expiredInstancePage?.items[0]?.effectiveAvailability.status, 'instance_expired', '授权实例自身到期必须由运行态投影判为 instance_expired')
  databaseModule.getBusinessDatabase().prepare('UPDATE accounts SET account_expires_at = NULL WHERE id = ?')
    .run(authorizedInstance.id)
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
  const sourceHealthListBasePage = await repositories.listAccountManagementItemsPageReadOnly(granteeAccess, {
    ids: [authorizedInstance.id], page: 1, pageSize: 20
  })
  const sourceHealthListPage = await hydrateAccountListPage(granteeAccess, sourceHealthListBasePage)
  assert.equal(sourceHealthListPage.items[0]?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-source-health', 'fast list seed 必须保留来源健康检查 traceId')
  assert.equal(sourceHealthListPage.items[0]?.availabilityPresentation?.probe?.schedule.nextAttemptAt, '2026-07-20T14:00:00.000Z', 'fast list seed 必须保留来源下次检查时间')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'rate_limited', schedulable = 0,
        last_error_code = 'cooldown_retest_failed', last_error_message = '来源冷却复测失败', last_error_trace_id = 'trace-source-cooldown',
        cooldown_retest_last_at = '2026-07-20T03:00:00.000Z', cooldown_retest_last_status_code = 429,
        cooldown_until = '2099-07-20T14:00:00.000Z'
    WHERE id = ?
  `).run(account.id)
  const sourceCooldownListBasePage = await repositories.listAccountManagementItemsPageReadOnly(granteeAccess, {
    ids: [authorizedInstance.id], page: 1, pageSize: 20
  })
  const sourceCooldownListPage = await hydrateAccountListPage(granteeAccess, sourceCooldownListBasePage)
  assert.equal(sourceCooldownListPage.items[0]?.availabilityPresentation?.probe?.lastObservation?.traceId, 'trace-source-cooldown', 'fast list seed 必须保留来源冷却复测 traceId')
  assert.equal(sourceCooldownListPage.items[0]?.availabilityPresentation?.probe?.lastObservation?.httpStatus, 429, 'fast list seed 必须保留来源冷却复测 HTTP 状态')
  assert.deepEqual(sourceCooldownListPage.items[0]?.availabilityPresentation?.probe?.schedule, {
    state: 'scheduled',
    nextAttemptAt: '2099-07-20T14:00:00.000Z'
  }, '来源冷却账户必须展示来源 worker 实际使用的复测时间')

  for (let index = 0; index < 140; index += 1) {
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
    pageSize: 200,
    keyword: '账户快照批量回归账户 '
  })
  assert(largeBasePage.items.length > 100, '回归夹具必须形成 pageSize > 100 的真实账户页')
  const largeHydratedPage = await hydrateAccountListPage(userAccess, largeBasePage)
  assert.equal(largeHydratedPage.items.length, largeBasePage.items.length, '列表状态投影不得静默截断 100 条之后的账户')
  assert.equal(
    largeHydratedPage.items.every((item) => Boolean(item.effectiveAvailability && item.availabilityPresentation && item.todayUsage)),
    true,
    'pageSize > 100 时每一行都必须完成状态、展示和今日用量投影'
  )
  const snapshotBatchDatabase = databaseModule.getStatsDatabase()
  const originalSnapshotPrepare = snapshotBatchDatabase.prepare.bind(snapshotBatchDatabase) as typeof snapshotBatchDatabase.prepare
  const statusProjectionBatchSizes: number[] = []
  snapshotBatchDatabase.prepare = ((sql: string) => {
    const statement = originalSnapshotPrepare(sql)
    if (/WITH\s+requested\(row_key, system_account_id, scope_type, scope_id\)/i.test(sql) && /usage_stats_daily/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        statusProjectionBatchSizes.push(Math.max(0, (params.length - 1) / 4))
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof snapshotBatchDatabase.prepare
  let largeRuntimeFilteredPage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  try {
    largeRuntimeFilteredPage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 1,
      pageSize: 128,
      status: 'pending_test',
      keyword: '账户快照批量回归账户 '
    })
  } finally {
    snapshotBatchDatabase.prepare = originalSnapshotPrepare
  }
  assert(largeRuntimeFilteredPage, '运行态状态筛选必须返回分页结果')
  assert.deepEqual(statusProjectionBatchSizes, [100, 29], '128 行分页加一条 lookahead 必须按 100 seed 运行态快照边界执行两次 hydrate')
  assert.equal(largeRuntimeFilteredPage.items.length, 128, 'pageSize=128 的运行态状态筛选不得漏掉第 101 条之后的匹配账户')
  assert.equal(largeRuntimeFilteredPage.hasMore, true, '第 129 个匹配必须形成准确的 hasMore')
  assert.equal(
    largeRuntimeFilteredPage.items.every((item) => item.effectiveAvailability.status === 'instance_pending_test'),
    true,
    '运行态筛选返回的每一行都必须使用完整状态投影'
  )
  assert.deepEqual(
    largeRuntimeFilteredPage.items.map((item) => item.id),
    largeBasePage.items.slice(0, 128).map((item) => item.id),
    '100 + 29 两个 hydrate 批次合并后必须保持候选 SQL 顺序'
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
  const disabledPrecedenceBasePage = await repositories.listAccountManagementItemsPageReadOnly(userAccess, {
    ids: [disabledPrecedenceAccount.id],
    page: 1,
    pageSize: 20,
    schedulable: 'disabled'
  })
  assert.deepEqual(
    disabledPrecedenceBasePage.items.map((item) => item.id),
    [disabledPrecedenceAccount.id],
    '候选 SQL 的 cooling 判定必须服从有效状态优先级，error + 残留 cooldown 仍属于 disabled'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, cooldown_until = NULL
    WHERE system_account_id = ? AND name LIKE '账户快照批量回归账户 %'
  `).run(user.id)
  const deepTemplate = largeBasePage.items[0]
  assert(deepTemplate, '深分页回归必须有可复制的账户模板')
  const deepPrefix = '运行态深分页回归账户 '
  const deepAccountCount = 1060
  const deepActiveStart = 1030
  const deepDatabase = databaseModule.getBusinessDatabase()
  const accountColumns = deepDatabase.prepare('PRAGMA table_info(accounts)').all() as unknown as Array<{ name: string }>
  const quotedColumns = accountColumns.map(({ name }) => `"${name.replace(/"/g, '""')}"`)
  const cloneExpressions = accountColumns.map(({ name }, index) => {
    if (name === 'id' || name === 'name' || name === 'status' || name === 'priority' || name === 'schedulable' || name === 'created_at' || name === 'updated_at') return '?'
    return quotedColumns[index]
  })
  const cloneAccount = deepDatabase.prepare(`
    INSERT INTO accounts (${quotedColumns.join(', ')})
    SELECT ${cloneExpressions.join(', ')}
    FROM accounts
    WHERE id = ?
  `)
  deepDatabase.exec('BEGIN')
  try {
    for (let index = 0; index < deepAccountCount; index += 1) {
      const suffix = String(index).padStart(4, '0')
      const timestamp = new Date(Date.UTC(2026, 6, 28, 0, 0, 0, index)).toISOString()
      cloneAccount.run(
        `acc_runtime_deep_${suffix}`,
        `${deepPrefix}${suffix}`,
        index < deepActiveStart ? 'error' : 'active',
        100000,
        index < deepActiveStart ? 0 : 1,
        timestamp,
        timestamp,
        deepTemplate.id
      )
    }
    deepDatabase.exec('COMMIT')
  } catch (error) {
    deepDatabase.exec('ROLLBACK')
    throw error
  }
  repositories.updateAccountTags(`acc_runtime_deep_${String(deepActiveStart).padStart(4, '0')}`, ['运行态尾页标签'], userAccess)
  const businessDatabase = databaseModule.getBusinessDatabase()
  const originalPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const runtimeStatsDatabase = databaseModule.getStatsDatabase()
  const originalRuntimeStatsPrepare = runtimeStatsDatabase.prepare.bind(runtimeStatsDatabase) as typeof runtimeStatsDatabase.prepare
  const candidateQueries: Array<{ limit: number; offset: number; sql: string }> = []
  const runtimeHydrationBatchSizes: number[] = []
  const runtimeHydrationAccountIds: string[] = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/WITH\s+account_rows\s+AS\s*\(/i.test(sql) && /ranked_group_bindings/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        candidateQueries.push({
          limit: Number(params.at(-2)),
          offset: Number(params.at(-1)),
          sql
        })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare
  runtimeStatsDatabase.prepare = ((sql: string) => {
    const statement = originalRuntimeStatsPrepare(sql)
    if (/WITH\s+requested\(row_key, system_account_id, scope_type, scope_id\)/i.test(sql) && /usage_stats_daily/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const scopeParams = params.slice(0, -1)
        runtimeHydrationBatchSizes.push(scopeParams.length / 4)
        for (let index = 0; index < scopeParams.length; index += 4) {
          runtimeHydrationAccountIds.push(String(scopeParams[index]))
        }
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof runtimeStatsDatabase.prepare
  let denseActivePage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  let denseCandidateQueries: typeof candidateQueries
  let denseHydrationBatchSizes: number[]
  let deepActivePage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  let deepActiveSecondPage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  let deepCandidateQueries: typeof candidateQueries
  let deepHydrationBatchSizes: number[]
  let deepHydrationAccountIds: string[]
  let deepUnboundedPage: Awaited<ReturnType<typeof listAccountsPageWithRuntimeStatusFilter>>
  let deepUnboundedCandidateQueries: typeof candidateQueries
  try {
    const denseQueryStart = candidateQueries.length
    const denseHydrationStart = runtimeHydrationBatchSizes.length
    denseActivePage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 1,
      pageSize: 20,
      status: 'active',
      keyword: '账户快照批量回归账户 '
    })
    denseCandidateQueries = candidateQueries.slice(denseQueryStart)
    denseHydrationBatchSizes = runtimeHydrationBatchSizes.slice(denseHydrationStart)
    const deepQueryStart = candidateQueries.length
    const deepHydrationStart = runtimeHydrationBatchSizes.length
    const deepHydrationAccountIdStart = runtimeHydrationAccountIds.length
    deepActivePage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 1,
      pageSize: 20,
      status: 'active',
      keyword: deepPrefix,
      sorts: [{ field: 'name', order: 'asc' }]
    })
    deepCandidateQueries = candidateQueries.slice(deepQueryStart)
    deepHydrationBatchSizes = runtimeHydrationBatchSizes.slice(deepHydrationStart)
    deepHydrationAccountIds = runtimeHydrationAccountIds.slice(deepHydrationAccountIdStart)
    deepActiveSecondPage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 2,
      pageSize: 20,
      status: 'active',
      keyword: deepPrefix,
      sorts: [{ field: 'name', order: 'asc' }]
    })
    businessDatabase.prepare(`
      UPDATE accounts
      SET status = 'active', schedulable = 1
      WHERE system_account_id = ? AND name LIKE ?
    `).run(user.id, `${deepPrefix}%`)
    const deepUnboundedQueryStart = candidateQueries.length
    deepUnboundedPage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 53,
      pageSize: 20,
      status: 'active',
      keyword: deepPrefix,
      sorts: [{ field: 'name', order: 'asc' }]
    })
    deepUnboundedCandidateQueries = candidateQueries.slice(deepUnboundedQueryStart)
  } finally {
    runtimeStatsDatabase.prepare = originalRuntimeStatsPrepare
    businessDatabase.prepare = originalPrepare
  }
  assert(denseActivePage, '正常状态运行态筛选必须返回分页结果')
  assert.equal(denseCandidateQueries.length, 1, 'active 高命中首屏必须只执行一次候选查询')
  assert.deepEqual(denseHydrationBatchSizes, [21], 'active 高命中首屏必须只 hydrate pageSize + 1 个候选')
  assert.deepEqual(
    { limit: denseCandidateQueries[0]!.limit, offset: denseCandidateQueries[0]!.offset },
    { limit: 22, offset: 0 },
    'active 首屏必须先读取 21 个候选加一条 lookahead'
  )
  for (const candidateQuery of denseCandidateQueries) {
    assert.doesNotMatch(candidateQuery.sql, /account_rows\.status\s+IN/i, '运行态状态筛选不得把持久状态提前下推到候选 SQL')
  }
  assert.equal(denseActivePage.items.length, 20, '正常状态首屏必须返回完整 20 行')
  assert.equal(denseActivePage.hasMore, true, '21 个候选命中时必须保留下一页提示')
  assert.equal(
    denseActivePage.items.every((item) => item.effectiveAvailability.available),
    true,
    'active 筛选必须以完整运行态投影做最终判定'
  )
  assert(deepActivePage, '超过 1000 个候选后的运行态筛选必须返回分页结果')
  assert.deepEqual(
    deepCandidateQueries.map(({ limit, offset }) => ({ limit, offset })),
    [
      { limit: 22, offset: 0 },
      { limit: 201, offset: 0 },
      { limit: 401, offset: 0 },
      { limit: 801, offset: 0 },
      { limit: 1601, offset: 0 }
    ],
    '稀疏筛选必须按 21/200/400/800/1600 递增前缀读取，且永不使用 OFFSET 分段'
  )
  assert.equal(deepHydrationBatchSizes.every((size) => size > 0 && size <= 100), true, '每个运行态 hydrate 批次必须保持 100 ID 边界')
  assert.equal(deepHydrationBatchSizes.reduce((sum, size) => sum + size, 0), deepAccountCount, '递增前缀只应 hydrate 新候选，每个账户恰好一次')
  assert.deepEqual(
    deepHydrationAccountIds,
    Array.from({ length: deepAccountCount }, (_, index) => `acc_runtime_deep_${String(index).padStart(4, '0')}`),
    '多轮重叠前缀必须去重并按候选顺序恰好 hydrate 每个账户一次'
  )
  assert.deepEqual(
    deepActivePage.items.map((item) => item.name),
    Array.from({ length: 20 }, (_, index) => `${deepPrefix}${String(deepActiveStart + index).padStart(4, '0')}`),
    '超过 1000 个非命中候选后仍必须按请求排序返回第一批命中'
  )
  assert.deepEqual(deepActivePage.items[0]?.tags.map((tag) => tag.name), ['运行态尾页标签'], '候选扩容不得重复加载标签，但最终返回页必须补齐标签')
  assert.equal(deepActivePage.hasMore, true, '尾部仍有第 21 个命中时 hasMore 必须为 true')
  assert(deepActiveSecondPage, '超过 1000 个候选后的第二页必须可访问')
  assert.deepEqual(
    deepActiveSecondPage.items.map((item) => item.name),
    Array.from({ length: 10 }, (_, index) => `${deepPrefix}${String(deepActiveStart + 20 + index).padStart(4, '0')}`),
    '第二页必须接续返回尾部命中且不重复、不漏行'
  )
  assert.equal(deepActiveSecondPage.hasMore, false, '扫描完整候选后 hasMore 必须准确收敛为 false')
  assert(deepUnboundedPage, '旧 1000 行窗口之外的运行态页必须可访问')
  assert.equal(deepUnboundedPage.page, 53, '运行态筛选不得把第 53 页回压到旧的第 50 页上限')
  assert.deepEqual(
    deepUnboundedPage.items.map((item) => item.name),
    Array.from({ length: 20 }, (_, index) => `${deepPrefix}${String(1040 + index).padStart(4, '0')}`),
    '第 53 页必须返回第 1041 至 1060 个匹配账户'
  )
  assert.equal(deepUnboundedPage.hasMore, false, '第 53 页恰好到达尾部时不得返回 hasMore 假阳性')
  assert.deepEqual(
    deepUnboundedCandidateQueries.map(({ limit, offset }) => ({ limit, offset })),
    [{ limit: 201, offset: 0 }, { limit: 401, offset: 0 }, { limit: 801, offset: 0 }, { limit: 1062, offset: 0 }],
    '深页高命中率应最多翻倍扩容到所需匹配数，仍不得使用 OFFSET'
  )
  runtimeConfig.processRole = 'db-service'
  try {
    const workerDeepPage = await listAccountsPageWithRuntimeStatusFilter(userAccess, {
      page: 53,
      pageSize: 20,
      status: 'active',
      keyword: deepPrefix,
      sorts: [{ field: 'name', order: 'asc' }]
    })
    assert.deepEqual(
      workerDeepPage?.items.map((item) => item.name),
      deepUnboundedPage.items.map((item) => item.name),
      '真实 SQLite read-worker 多轮扩窗必须与直接读取返回同一第 53 页'
    )
    assert.deepEqual(workerDeepPage?.items[0]?.tags.map((tag) => tag.name), [], '第 53 页标签补齐必须在 read-worker 路径保持正确')
  } finally {
    await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
    runtimeConfig.processRole = 'worker'
  }
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('账户状态快照契约回归通过：公开 ID 有界、内部大页完整、来源探针窄投影和可调度状态均正确')
