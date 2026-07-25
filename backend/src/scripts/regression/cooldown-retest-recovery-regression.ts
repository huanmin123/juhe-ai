import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES } from '../../domain/account-health-check-endpoint-mode.js'
import { ANTHROPIC_ANTHROPIC_V1_PROFILE_ID, GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { installWorkerParentIpcHarness } from '../shared/worker-parent-ipc-harness.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-cooldown-retest-recovery-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'cooldown-retest-recovery-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const cooldownRetestRepositorySource = readFileSync(resolve('src/storage/account-cooldown-retest.repository.ts'), 'utf8')
const cooldownRetestServiceSource = readFileSync(resolve('src/modules/background/cooldown-account-retest.service.ts'), 'utf8')
assert.match(
  cooldownRetestRepositorySource,
  /runInDatabaseTransaction\(\(\) => recordCooldownAccountRetestFailureInSqliteTransaction/,
  'SQLite 冷却复测失败读改写必须由 BEGIN IMMEDIATE 事务串行化'
)
assert.match(
  cooldownRetestRepositorySource,
  /client\.transaction\(async \(tx\)[\s\S]+queryAccountCooldownRetestStateAsync\(tx, id, \{ forUpdate: true \}\)[\s\S]+UPDATE \$\{cooldownRetestTable\(tx, 'accounts'\)\}/,
  'PostgreSQL 冷却复测必须在同一事务内锁定读取、决策并更新'
)
assert.match(
  cooldownRetestRepositorySource,
  /FOR UPDATE OF accounts/,
  'PostgreSQL 冷却复测锁定读取必须明确锁 accounts 行'
)
assert.match(cooldownRetestRepositorySource, /accounts\.health_check_endpoint_mode IN/, '冷却复测候选必须按可执行生成检查 mode 筛选')
assert.doesNotMatch(cooldownRetestRepositorySource, /listOpenAIProtocolProfileIds/, '冷却复测候选不得继续只允许 OpenAI profile')
assert.doesNotMatch(
  cooldownRetestRepositorySource,
  /AND \(\? IS NULL OR (?:config_revision|cooldown_retest_observation_started_at) = \?\)/,
  '冷却复测写回不得使用 PostgreSQL 无法推断参数类型的 nullable guard'
)
assert.match(
  cooldownRetestServiceSource,
  /errorLogFields\(event\.error,/,
  '冷却复测队列耗尽日志必须保留结构化错误上下文'
)

const restoreWorkerParentIpc = installWorkerParentIpcHarness()

const [databaseModule, repositories, gatewayRuntimeCache, cooldownRetestService, { closeSqliteReadWorkerPool }] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/background/cooldown-account-retest.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])
const { cooldownAccountRetestQueueAvailableSlots } = await import('../../modules/background/account-probe-jobs.js')
const {
  backgroundFullDiagnosticConcurrency,
  backgroundFullDiagnosticQueueConcurrency,
  backgroundProbeDbServiceTimeoutMs,
  cooldownAccountRetestStartupDelayMs,
  runWithBackgroundFullDiagnosticSlot
} = await import('../../modules/background/account-probe-limits.js')

const neutralBackoffStartedAtMs = Date.parse('2026-07-25T00:00:00.000Z')
const neutralBackoffIdentity = {
  accountId: 'account-neutral-backoff-regression',
  generation: 'cooldown:neutral-backoff-regression',
  observationStartedAt: new Date(neutralBackoffStartedAtMs).toISOString()
}
const initialNeutralDelay = cooldownRetestService.cooldownRetestNeutralDeferDelaySeconds({
  ...neutralBackoffIdentity,
  nowMs: neutralBackoffStartedAtMs
})
const repeatedInitialNeutralDelay = cooldownRetestService.cooldownRetestNeutralDeferDelaySeconds({
  ...neutralBackoffIdentity,
  nowMs: neutralBackoffStartedAtMs
})
const laterNeutralDelay = cooldownRetestService.cooldownRetestNeutralDeferDelaySeconds({
  ...neutralBackoffIdentity,
  nowMs: neutralBackoffStartedAtMs + 210_000
})
const boundedNeutralDelay = cooldownRetestService.cooldownRetestNeutralDeferDelaySeconds({
  ...neutralBackoffIdentity,
  nowMs: neutralBackoffStartedAtMs + 24 * 60 * 60_000
})
assert.equal(initialNeutralDelay, repeatedInitialNeutralDelay, '冷却复测未知结果退避抖动必须按账户代次确定，避免同一任务随机漂移')
assert(initialNeutralDelay >= 24 && initialNeutralDelay <= 36, '冷却复测未知结果首轮应在 30 秒附近抖动，不得固定每 10 秒探测')
assert(laterNeutralDelay > initialNeutralDelay, '冷却复测未知结果持续存在时必须指数放大延迟')
assert(boundedNeutralDelay >= 12 * 60 && boundedNeutralDelay <= 15 * 60, '冷却复测未知结果退避必须在 15 分钟内封顶并保留抖动')

assertSqliteCooldownCandidatePlan()
assertSqliteCooldownAccountIdLookupPlans()

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

type CooldownRetestGuard = {
  expectedConfigRevision: number
  expectedDispatchRevision: number
  expectedObservationStartedAt: string
  expectedGeneration: string
  expectedSourceConfigRevision?: number
}

function currentCooldownRetestGuard(accountId: string): CooldownRetestGuard {
  // This call is intentionally part of the helper: production candidate reads
  // atomically repair legacy NULL/empty observation or generation state.
  repositories.findAccountForCooldownRetest(accountId)
  const current = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT accounts.config_revision,
             accounts.dispatch_revision,
             accounts.cooldown_retest_observation_started_at,
             accounts.cooldown_retest_generation,
             accounts.authorization_instance_source_account_id,
             source_accounts.config_revision AS source_config_revision
      FROM accounts
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
       AND source_accounts.deleted_at IS NULL
      WHERE accounts.id = ? AND accounts.deleted_at IS NULL
    `)
    .get(accountId) as unknown as {
      config_revision?: number | null
      dispatch_revision?: number | null
      cooldown_retest_observation_started_at?: string | null
      cooldown_retest_generation?: string | null
      authorization_instance_source_account_id?: string | null
      source_config_revision?: number | null
    } | undefined
  assert(current, `冷却复测账户 ${accountId} 必须存在`)
  assert(current.cooldown_retest_observation_started_at, `冷却复测账户 ${accountId} 必须具有观察起点`)
  const generation = current.cooldown_retest_generation?.trim()
  assert(generation, `冷却复测账户 ${accountId} 必须具有唯一代际令牌`)
  const guard: CooldownRetestGuard = {
    expectedConfigRevision: Number(current.config_revision ?? 1),
    expectedDispatchRevision: Number(current.dispatch_revision ?? 1),
    expectedObservationStartedAt: current.cooldown_retest_observation_started_at,
    expectedGeneration: generation
  }
  if (current.authorization_instance_source_account_id) {
    assert(Number.isInteger(current.source_config_revision) && Number(current.source_config_revision) > 0, `授权实例 ${accountId} 必须具有来源配置版本`)
    guard.expectedSourceConfigRevision = Number(current.source_config_revision)
  }
  return guard
}

function cloneBusinessDatabaseRow(
  table: 'accounts' | 'resource_authorizations' | 'group_accounts',
  whereSql: string,
  whereParams: SQLInputValue[],
  overrides: Record<string, SQLInputValue>
): void {
  const database = databaseModule.getBusinessDatabase()
  const source = database.prepare(`SELECT * FROM "${table}" WHERE ${whereSql} LIMIT 1`)
    .get(...whereParams) as Record<string, SQLInputValue> | undefined
  assert(source, `克隆测试夹具失败：${table} 来源行不存在`)
  const clone = { ...source, ...overrides }
  const columns = Object.keys(clone)
  assert(columns.length > 0, `克隆测试夹具失败：${table} 无可写列`)
  const quotedColumns = columns.map((column) => {
    assert(/^[a-z_][a-z0-9_]*$/i.test(column), `克隆测试夹具列名非法：${column}`)
    return `"${column}"`
  })
  database.prepare(`
    INSERT INTO "${table}" (${quotedColumns.join(', ')})
    VALUES (${columns.map(() => '?').join(', ')})
  `).run(...columns.map((column) => clone[column] ?? null))
}

function cooldownRetestWriteSnapshot(accountId: string): Record<string, unknown> {
  const snapshot = databaseModule.getBusinessDatabase().prepare(`
    SELECT status, schedulable, account_expires_at, cooldown_until,
           last_error_code, last_error_message, last_error_trace_id,
           cooldown_retest_failure_count, cooldown_retest_observation_started_at,
           cooldown_retest_generation, cooldown_retest_last_at,
           cooldown_retest_last_status_code, stream_failure_count,
           stream_failure_window_started_at, config_revision,
           dispatch_revision, updated_at
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
  `).get(accountId) as Record<string, unknown> | undefined
  assert(snapshot, `冷却复测写回快照账户 ${accountId} 必须存在`)
  return snapshot
}

function assertCooldownRetestWritesRejected(
  accountId: string,
  guard: CooldownRetestGuard,
  label: string,
  options: { allowExpiredAccountMaintenance?: boolean } = {}
): void {
  const before = cooldownRetestWriteSnapshot(accountId)
  const success = repositories.recordCooldownAccountRetestSuccess(accountId, guard)
  assert.equal(success.changed, false, `${label}时 success 写回必须拒绝`)
  assert.deepEqual(cooldownRetestWriteSnapshot(accountId), before, `${label}时 success 不得改变目标账户冷却状态`)
  const defer = repositories.deferCooldownAccountRetest(accountId, {
    ...guard,
    delaySeconds: 60
  })
  assert.equal(defer.changed, false, `${label}时 defer 写回必须拒绝`)
  assert.deepEqual(cooldownRetestWriteSnapshot(accountId), before, `${label}时 defer 不得改变目标账户冷却状态`)
  const failure = repositories.recordCooldownAccountRetestFailure(accountId, {
    ...guard,
    statusCode: 503,
    errorMessage: `${label}下的迟到失败`
  })
  assert.equal(failure.changed, false, `${label}时 failure 写回必须拒绝`)
  assert.equal(failure.action, 'discard', `${label}时 failure 必须明确丢弃`)
  const afterFailure = cooldownRetestWriteSnapshot(accountId)
  if (options.allowExpiredAccountMaintenance) {
    assert.equal(afterFailure.status, 'disabled', `${label}时 failure 只允许统一过期维护停用账户`)
    assert.equal(afterFailure.schedulable, 0, `${label}时统一过期维护必须关闭调度`)
    assert.equal(afterFailure.last_error_code, 'account_expired', `${label}时统一过期维护必须记录 account_expired`)
    assert.equal(afterFailure.cooldown_until, null, `${label}时统一过期维护必须清理冷却时间`)
    assert.equal(afterFailure.cooldown_retest_generation, null, `${label}时统一过期维护必须清理冷却代际`)
    return
  }
  assert.deepEqual(afterFailure, before, `${label}时 failure 不得改变目标账户任何冷却状态`)
}

let mockOpenAIServer: http.Server | undefined
let mockOpenAIResponseHitCount = 0
let mockOpenAIResponseGate: Promise<void> | undefined

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('冷却复测恢复 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`
  const gptSupportedModels = ['gpt-5.4']

  const group = repositories.createGroup({
    name: '冷却复测回归分组',
    providerCode: 'gpt'
  }, access)
  const anthropicGroup = repositories.createGroup({
    name: 'Anthropic 冷却复测回归分组',
    providerCode: 'anthropic'
  }, access)
  const workerGatewaySettings = await gatewayRuntimeCache.readCachedGatewaySettingsAsync()
  assert.equal(typeof workerGatewaySettings.defaultTemporaryUnschedulableMinutes, 'number', 'worker 角色应能本地读取网关设置，不能误走 DB service IPC')
  const workerGroupAccess = await gatewayRuntimeCache.resolveCachedGroupUsageAccessMetadataAsync(group.id, access.systemAccountId)
  assert.equal(workerGroupAccess?.groupOwnerSystemAccountId, access.systemAccountId, 'worker 角色应能本地读取分组访问元数据，不能误走 DB service IPC')
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(account.id)
  assert(repositories.setAccountGroup(account.id, group.id, access), '冷却复测观察窗口账号应能绑定分组')
  const cooled = repositories.markAccountTemporaryUnavailable(account.id, '模拟临时不可调用', undefined, 'trace-cooldown-initial')
  assert.equal(cooled?.status, 'temporary_unavailable', '临时不可调用应进入恢复通道')
  assert.ok(cooled?.cooldownRetestObservationStartedAt, '进入临时不可调用时应记录自动恢复观察起点')
  assert.ok(Date.parse(cooled.cooldownUntil ?? '') - Date.now() <= 10_000, '临时不可调用首次暂停应走秒级快速恢复')

  const anthropicAccount = repositories.createAccount({
    providerCode: 'anthropic',
    providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
    name: 'Anthropic 冷却复测候选回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ant-cooldown-retest-recovery',
      base_url: 'https://api.anthropic.com/v1'
    },
    status: 'active',
    groupId: anthropicGroup.id,
    supportedModels: ['claude-sonnet-4-5'],
    healthCheckModel: 'claude-sonnet-4-5',
    healthCheckEndpointMode: 'messages_sse'
  }, access)
  activateTestAccount(anthropicAccount.id)
  assert(repositories.setAccountGroup(anthropicAccount.id, anthropicGroup.id, access), 'Anthropic 冷却复测账号应能绑定分组')
  repositories.markAccountTemporaryUnavailable(anthropicAccount.id, '模拟 Anthropic 临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), anthropicAccount.id)
  assert(
    repositories.listAccountsDueForCooldownRetest(100).some((item) => item.id === anthropicAccount.id),
    'Anthropic Messages 生成协议账户到期后必须进入冷却复测候选'
  )
  assert.equal(repositories.findAccountForCooldownRetest(anthropicAccount.id)?.id, anthropicAccount.id, 'Anthropic 单账号冷却复测读取不得被 OpenAI profile 限制')
  repositories.updateAccount(anthropicAccount.id, { status: 'disabled' }, access)

  const expiredObservationStartedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_generation = NULL,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(expiredObservationStartedAt, new Date(Date.now() - 1000).toISOString(), account.id)

  const longRecovering = repositories.recordCooldownAccountRetestFailure(account.id, {
    ...currentCooldownRetestGuard(account.id),
    traceId: 'trace-cooldown-retest-latest',
    statusCode: 401,
    errorMessage: '仍然不可用',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(longRecovering.action, 'long_term_cooldown', '超过观察窗口后应进入长期不可用低频恢复')
  assert.equal(longRecovering.recoveryStage, 'long_term', '超过观察窗口后应标记长期恢复阶段')
  assert.equal(longRecovering.account?.status, 'temporary_unavailable', '超过观察窗口后账号仍应保持临时不可调用')
  assert.equal(longRecovering.account?.schedulable, true, '长期不可用账号应保留后台可恢复调度标记')
  assert.ok(longRecovering.account?.cooldownUntil, '长期不可用账号应写入下一次低频复测时间')
  assert.equal(longRecovering.account?.lastErrorCode, 'cooldown_retest_long_term_unavailable', '超过观察窗口后应写入长期不可用原因码')
  assert.equal(longRecovering.account?.lastErrorTraceId, 'trace-cooldown-retest-latest', '冷却复测的新错误摘要必须覆盖为本次探针 traceId')
  assert.equal(longRecovering.longTermIntervalSeconds, 60 * 60, '长期不可用必须固定每 1 小时复测，不受旧设置值放大')
  assert.match(longRecovering.errorMessage, /进入长期不可用每 1 小时复测/, '失败摘要应说明每 1 小时自动复测')
  assert(!repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '长期不可用账号在下次复测时间前不应进入候选')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), account.id)
  assert(repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '长期不可用账号到达下次复测时间后仍应进入后台复测候选')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = NULL, cooldown_until = ? WHERE id = ?')
    .run(
      new Date(Date.now() - 8 * 24 * 60 * 60_000).toISOString(),
      new Date(Date.now() - 1000).toISOString(),
      account.id
    )
  const terminalGuard = currentCooldownRetestGuard(account.id)
  const terminalFailure = repositories.recordCooldownAccountRetestFailure(account.id, {
    ...terminalGuard,
    statusCode: 401,
    errorCode: 'invalid_api_key',
    errorMessage: '持续不可用超过七天',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(terminalFailure.action, 'error', '观察起点满 7 天仍失败必须终止自动冷却恢复')
  assert.equal(terminalFailure.recoveryStage, 'terminal', '满 7 天失败必须进入终止阶段')
  assert.equal(terminalFailure.transitionedToError, true, '满 7 天失败必须原子转为异常')
  assert.equal(terminalFailure.account?.status, 'error', '满 7 天失败后账户状态必须为 error')
  assert.equal(terminalFailure.account?.schedulable, false, '满 7 天失败后账户必须不可调度')
  assert.equal(terminalFailure.account?.cooldownUntil, undefined, '转为异常后必须清空冷却时间')
  assert.equal(terminalFailure.errorCode, 'cooldown_retest_observation_timeout', '满 7 天失败必须写入明确错误码')
  assert.match(terminalFailure.errorMessage, /已持续 7 天仍未恢复/, '满 7 天失败必须写入明确错误原因')
  assert(!repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '异常账户不得继续进入冷却复测候选')
  const lateTerminalSuccess = repositories.recordCooldownAccountRetestSuccess(account.id, terminalGuard)
  const lateTerminalFailure = repositories.recordCooldownAccountRetestFailure(account.id, {
    ...terminalGuard,
    statusCode: 503,
    errorMessage: '终态后的迟到失败'
  })
  const lateTerminalDefer = repositories.deferCooldownAccountRetest(account.id, {
    ...terminalGuard,
    delaySeconds: 60
  })
  assert.equal(lateTerminalSuccess.changed, false, '账户进入异常终态后迟到成功不得恢复账户')
  assert.equal(lateTerminalFailure.changed, false, '账户进入异常终态后迟到失败不得再次写入')
  assert.equal(lateTerminalDefer.changed, false, '账户进入异常终态后迟到延迟不得重置冷却')
  assert.equal(repositories.findAccountSummary(account.id, access)?.status, 'error', '终态迟到写回不得改变异常状态')
  const terminalRecovered = repositories.clearAccountFailureState(account.id, access)
  assert.equal(terminalRecovered?.status, 'pending_test', '长期不可用终态的异常恢复只能进入待检查')
  assert.equal(terminalRecovered?.schedulable, false, '长期不可用终态恢复后后台检查通过前不得调度')
  assert.equal(terminalRecovered?.cooldownRetestObservationStartedAt, undefined, '异常恢复必须清空长期观察起点')

  const freshAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测未超观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery-fresh',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(freshAccount.id)
  assert(repositories.setAccountGroup(freshAccount.id, group.id, access), '冷却复测未超观察窗口账号应能绑定分组')
  repositories.markAccountTemporaryUnavailable(freshAccount.id, '模拟临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 5,
          cooldown_retest_observation_started_at = ?,
          cooldown_retest_generation = NULL,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(new Date().toISOString(), new Date(Date.now() - 1000).toISOString(), freshAccount.id)

  const stillRecovering = repositories.recordCooldownAccountRetestFailure(freshAccount.id, {
    ...currentCooldownRetestGuard(freshAccount.id),
    traceId: 'trace-cooldown-retest-quota',
    statusCode: 403,
    errorCode: 'insufficient_quota',
    errorMessage: '余额和订阅额度均不足，请充值后再使用 (request id: upstream-request-id-should-display)',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(stillRecovering.recoveryStage, 'slow', '超过快速阈值后应进入慢速恢复')
  assert.notEqual(stillRecovering.action, 'long_term_cooldown', '未超过观察阈值时不应进入长期不可用')
  const freshAfterRetest = repositories.findAccountSummary(freshAccount.id, access)
  assert.equal(freshAfterRetest?.status, 'temporary_unavailable', '未超过观察窗口时账号应继续恢复')
  assert.equal(freshAfterRetest?.lastErrorCode, 'insufficient_quota', '后台复测应把上游真实错误码写入账户状态')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '后台复测状态原因应保留真实上游错误摘要')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /traceId trace-cooldown-retest-quota/, '后台复测状态原因应写入本地 traceId 作为追踪主键')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /request id: upstream-request-id-should-display/, '后台复测状态原因应保留上游 request id')

  const serializedAccount = createActiveCoolingAccount('冷却复测事务串行回归', 'sk-cooldown-retest-serialized', group.id)
  const serializedFirst = repositories.recordCooldownAccountRetestFailure(serializedAccount.id, {
    ...currentCooldownRetestGuard(serializedAccount.id),
    statusCode: 503,
    errorMessage: '事务串行第一次失败',
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  const serializedSecond = repositories.recordCooldownAccountRetestFailure(serializedAccount.id, {
    ...currentCooldownRetestGuard(serializedAccount.id),
    statusCode: 503,
    errorMessage: '事务串行第二次失败',
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  assert.equal(serializedFirst.failureCount, 1, 'SQLite 事务内首次失败应从 0 累加到 1')
  assert.equal(serializedSecond.failureCount, 2, 'SQLite 后续失败必须读取前一事务提交值并累加到 2')
  assert.equal(
    repositories.findAccountSummary(serializedAccount.id, access)?.cooldownRetestFailureCount,
    2,
    'SQLite 连续复测失败不得丢失已提交计数'
  )

  const exactBackoffAccount = createActiveCoolingAccount('冷却复测精确退避边界', 'sk-cooldown-exact-backoff', group.id)
  const exactBackoffGuard = currentCooldownRetestGuard(exactBackoffAccount.id)
  const exactBackoffInput = {
    ...exactBackoffGuard,
    statusCode: 503,
    errorMessage: '精确退避边界失败',
    initialBackoffSeconds: 3,
    backoffMultiplier: 2,
    fastThresholdSeconds: 60,
    maxPauseMinutes: 1,
    maxRecoveryHours: 1
  }
  const exactBackoffFirst = repositories.recordCooldownAccountRetestFailure(exactBackoffAccount.id, exactBackoffInput)
  const exactBackoffSecond = repositories.recordCooldownAccountRetestFailure(exactBackoffAccount.id, exactBackoffInput)
  assert.equal(exactBackoffFirst.failureCount, 1, '首次复测失败计数必须为 1')
  assert.equal(exactBackoffFirst.backoffSeconds, 3, '首次复测失败必须使用 initial 3 秒退避')
  assert.equal(exactBackoffFirst.maxedFailureCount, 0, '首次失败不得计入达到 maxPause 的次数')
  assert.equal(exactBackoffSecond.failureCount, 2, '第二次复测失败计数必须为 2')
  assert.equal(exactBackoffSecond.backoffSeconds, 6, '第二次复测失败必须按倍率进入 6 秒退避')
  assert.equal(exactBackoffSecond.maxedFailureCount, 0, '第二次失败不得计入达到 maxPause 的次数')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET cooldown_retest_failure_count = 4, cooldown_until = ?
    WHERE id = ?
  `).run(new Date(Date.now() - 1000).toISOString(), exactBackoffAccount.id)
  const exactBackoffBeforeCap = repositories.recordCooldownAccountRetestFailure(exactBackoffAccount.id, exactBackoffInput)
  const exactBackoffAtCap = repositories.recordCooldownAccountRetestFailure(exactBackoffAccount.id, exactBackoffInput)
  assert.equal(exactBackoffBeforeCap.failureCount, 5, '达到 maxPause 前一档 failureCount 必须为 5')
  assert.equal(exactBackoffBeforeCap.backoffSeconds, 48, '达到 maxPause 前一档必须保持 48 秒未封顶退避')
  assert.equal(exactBackoffBeforeCap.maxedFailureCount, 0, '48 秒未封顶档不得计入 maxedFailureCount')
  assert.equal(exactBackoffAtCap.failureCount, 6, '首次达到 maxPause 的 failureCount 必须为 6')
  assert.equal(exactBackoffAtCap.backoffSeconds, 60, 'failureCount=6 时必须首次封顶到 maxPause 60 秒')
  assert.equal(exactBackoffAtCap.maxedFailureCount, 1, '首次达到 maxPause 时 maxedFailureCount 必须从 1 开始')
  repositories.updateAccount(exactBackoffAccount.id, { status: 'disabled' }, access)

  const restored = repositories.clearAccountFailureState(freshAccount.id, access)
  assert.equal(restored?.cooldownRetestObservationStartedAt, undefined, '恢复正常时应清理自动恢复观察起点')

  const disabledCleanupAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '停用清理过期失败原因回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-disable-clear-expired-error',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(disabledCleanupAccount.id)
  repositories.markAccountTemporaryUnavailable(disabledCleanupAccount.id, '过期冷却错误')
  const disabledCleanup = repositories.updateAccount(disabledCleanupAccount.id, { status: 'disabled' }, access)
  assert.equal(disabledCleanup?.status, 'disabled', '冷却账号应允许手动停用')
  assert.equal(disabledCleanup?.lastErrorCode, undefined, '手动停用应清理既有错误码')
  assert.equal(disabledCleanup?.lastErrorMessage, undefined, '手动停用应清理既有失败原因，避免停用状态展示过期冷却错误')
  assert.equal(disabledCleanup?.cooldownUntil, undefined, '手动停用应清理既有冷却结束时间')

  const rateLimitedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '限流后台复测回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-rate-limited',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(rateLimitedAccount.id)
  assert(repositories.setAccountGroup(rateLimitedAccount.id, group.id, access), '限流复测账号应能绑定分组')
  const limited = repositories.markAccountCooldown(rateLimitedAccount.id, new Date(Date.now() - 1000).toISOString(), '模拟限流', 'rate_limited')
  assert.equal(limited?.status, 'rate_limited', '限流状态应进入同一自动恢复通道')
  assert.ok(limited?.cooldownRetestObservationStartedAt, '进入限流时应记录自动恢复观察起点')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), rateLimitedAccount.id)
  const dueIds = repositories.listAccountsDueForCooldownRetest(20).map((item) => item.id)
  assert(dueIds.includes(rateLimitedAccount.id), '限流到期账号应进入后台复测候选')
  const limitedStillRecovering = repositories.recordCooldownAccountRetestFailure(rateLimitedAccount.id, {
    ...currentCooldownRetestGuard(rateLimitedAccount.id),
    statusCode: 429,
    errorMessage: '仍然限流',
    maxRecoveryHours: 1,
    maxPauseMinutes: 10
  })
  assert.equal(limitedStillRecovering.action, 'retry_immediately', '限流首次复测失败应走快速恢复通道')
  assert.equal(repositories.findAccountSummary(rateLimitedAccount.id, access)?.status, 'rate_limited', '限流复测失败后应保持限流状态等待下次自动恢复')

  const boundedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '有界持续恢复探活回归',
    type: 'api_key',
    credentials: { api_key: 'sk-cooldown-bounded', base_url: 'https://api.openai.com/v1' },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels,
    temporaryUnavailableContinuousProbeEnabled: false
  }, access)
  assert.equal(repositories.findAccountSummary(boundedAccount.id, access)?.temporaryUnavailableContinuousProbeEnabled, false, 'SQLite 账户读取必须保留持续恢复探活关闭值')
  activateTestAccount(boundedAccount.id)
  repositories.markAccountTemporaryUnavailable(boundedAccount.id, '有界模式临时不可调用')
  const boundedStartedAt = new Date(Date.now() - 9 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = NULL, cooldown_until = ? WHERE id = ?
  `).run(boundedStartedAt, new Date(Date.now() - 1000).toISOString(), boundedAccount.id)
  const boundedRetry = repositories.recordCooldownAccountRetestFailure(boundedAccount.id, {
    ...currentCooldownRetestGuard(boundedAccount.id),
    statusCode: 503, errorMessage: '有界复测仍失败', maxPauseMinutes: 1440, maxRecoveryHours: 24
  })
  assert.notEqual(boundedRetry.recoveryStage, 'long_term', '十分钟窗口内有界账户不得进入长期每小时复测')
  assert.ok(
    Date.parse(boundedRetry.cooldownUntil ?? '') <= Date.parse(boundedStartedAt) + 10 * 60 * 1000,
    '有界模式下一次复测不得越过十分钟最终探测截止时间'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = NULL, cooldown_until = ? WHERE id = ?
  `).run(new Date(Date.now() - 11 * 60 * 1000).toISOString(), new Date(Date.now() - 1000).toISOString(), boundedAccount.id)
  const boundedTerminal = repositories.recordCooldownAccountRetestFailure(boundedAccount.id, {
    ...currentCooldownRetestGuard(boundedAccount.id),
    statusCode: 503, errorMessage: '有界最终探测失败', maxPauseMinutes: 1440, maxRecoveryHours: 24
  })
  assert.equal(boundedTerminal.action, 'error', '十分钟到期后的真实失败必须转异常')
  assert.equal(boundedTerminal.errorCode, 'cooldown_retest_limited_probe_timeout', '有界终态必须使用独立错误码')
  assert.match(boundedTerminal.errorMessage, /已持续 10 分钟仍未恢复/, '有界终态必须显示真实十分钟观察窗口')
  assert.equal(boundedTerminal.account?.status, 'error', '有界最终失败后必须停止冷却复测')

  const boundedRateLimited = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '有界开关不影响限流回归', type: 'api_key',
    credentials: { api_key: 'sk-cooldown-bounded-rate', base_url: 'https://api.openai.com/v1' },
    status: 'active', groupId: group.id, supportedModels: gptSupportedModels, temporaryUnavailableContinuousProbeEnabled: false
  }, access)
  activateTestAccount(boundedRateLimited.id)
  repositories.markAccountCooldown(boundedRateLimited.id, new Date(Date.now() - 1000).toISOString(), '有界开关下的限流', 'rate_limited')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = NULL, cooldown_until = ? WHERE id = ?
  `).run(new Date(Date.now() - 11 * 60 * 1000).toISOString(), new Date(Date.now() - 1000).toISOString(), boundedRateLimited.id)
  const rateStillContinuous = repositories.recordCooldownAccountRetestFailure(boundedRateLimited.id, {
    ...currentCooldownRetestGuard(boundedRateLimited.id),
    statusCode: 429, errorMessage: '限流未恢复', maxPauseMinutes: 10, maxRecoveryHours: 24
  })
  assert.notEqual(rateStillContinuous.action, 'error', '持续恢复探活开关不能把 rate_limited 提前转异常')

  const guardedRestore = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '有界探针旧结果守卫回归', type: 'api_key',
    credentials: { api_key: 'sk-cooldown-guarded', base_url: 'https://api.openai.com/v1' },
    status: 'active', groupId: group.id, supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(guardedRestore.id)
  const guardedCooling = repositories.markAccountTemporaryUnavailable(guardedRestore.id, '旧探针守卫')
  assert(guardedCooling?.cooldownRetestObservationStartedAt)
  const guardedRestoreGuard = currentCooldownRetestGuard(guardedRestore.id)
  const staleRestore = repositories.recordCooldownAccountRetestSuccess(guardedRestore.id, {
    ...guardedRestoreGuard,
    expectedConfigRevision: guardedRestoreGuard.expectedConfigRevision + 1
  })
  assert.equal(staleRestore.changed, false, '配置版本变化后的旧成功探针不得恢复账户')
  const currentRestore = repositories.recordCooldownAccountRetestSuccess(guardedRestore.id, {
    ...guardedRestoreGuard
  })
  assert.equal(currentRestore.changed, true, '当前代次的成功探针必须恢复账户')

  const staleFailureAccount = createActiveCoolingAccount('冷却复测旧失败代次守卫', 'sk-cooldown-stale-failure', group.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_retest_generation = NULL, cooldown_until = ? WHERE id = ?
  `).run(new Date(Date.now() - 60_000).toISOString(), new Date(Date.now() - 1000).toISOString(), staleFailureAccount.id)
  const staleFailureQueued = repositories.findAccountForCooldownRetest(staleFailureAccount.id)
  assert(staleFailureQueued?.cooldownRetestObservationStartedAt)
  assert(staleFailureQueued.cooldownRetestGeneration)
  repositories.clearAccountFailureState(staleFailureAccount.id, access)
  const nextFailureGeneration = repositories.markAccountTemporaryUnavailable(staleFailureAccount.id, '进入新一轮冷却观察')
  assert(nextFailureGeneration?.cooldownRetestObservationStartedAt)
  assert.notEqual(nextFailureGeneration.cooldownRetestObservationStartedAt, staleFailureQueued.cooldownRetestObservationStartedAt, '新一轮冷却必须生成新的观察代次')
  const staleFailureGuard = {
    ...currentCooldownRetestGuard(staleFailureAccount.id),
    expectedConfigRevision: staleFailureQueued.configRevision ?? 1,
    expectedDispatchRevision: staleFailureQueued.cooldownRetestDispatchRevision ?? 1,
    expectedObservationStartedAt: staleFailureQueued.cooldownRetestObservationStartedAt,
    expectedGeneration: staleFailureQueued.cooldownRetestGeneration ?? ''
  }
  const staleFailure = repositories.recordCooldownAccountRetestFailure(staleFailureAccount.id, {
    ...staleFailureGuard,
    statusCode: 503,
    errorMessage: '上一轮迟到失败'
  })
  assert.equal(staleFailure.changed, false, '上一轮迟到失败不得写入新的观察代次')
  assert.equal(repositories.findAccountSummary(staleFailureAccount.id, access)?.cooldownRetestFailureCount, 0, '上一轮迟到失败不得累计到新一轮')
  repositories.updateAccount(staleFailureAccount.id, { status: 'disabled' }, access)

  const legacyNullAccount = createActiveCoolingAccount('冷却复测旧 NULL 状态自愈', 'sk-cooldown-legacy-null', group.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET cooldown_retest_observation_started_at = NULL,
        cooldown_retest_generation = NULL,
        cooldown_until = ?
    WHERE id = ?
  `).run(new Date(Date.now() - 1000).toISOString(), legacyNullAccount.id)
  const legacyNullCandidate = repositories.listAccountsDueForCooldownRetest(100)
    .find((item) => item.id === legacyNullAccount.id)
  assert(legacyNullCandidate?.cooldownRetestObservationStartedAt, 'list 应原子补齐旧 NULL 观察起点')
  assert(Number.isFinite(Date.parse(legacyNullCandidate.cooldownRetestObservationStartedAt)), '自愈后的观察起点必须是有效时间')
  assert(legacyNullCandidate.cooldownRetestGeneration?.trim(), 'list 应原子补齐旧 NULL 代际令牌')
  const legacyNullGuard = currentCooldownRetestGuard(legacyNullAccount.id)
  assert.equal(legacyNullGuard.expectedObservationStartedAt, legacyNullCandidate.cooldownRetestObservationStartedAt, 'find 不得重复改写 list 已修复的观察起点')
  assert.equal(legacyNullGuard.expectedGeneration, legacyNullCandidate.cooldownRetestGeneration, 'find 不得重复生成 list 已修复的代际令牌')
  const legacyNullFoundAgain = repositories.findAccountForCooldownRetest(legacyNullAccount.id)
  assert.equal(legacyNullFoundAgain?.cooldownRetestObservationStartedAt, legacyNullGuard.expectedObservationStartedAt, '重复 find 必须保持已修复观察起点稳定')
  assert.equal(legacyNullFoundAgain?.cooldownRetestGeneration, legacyNullGuard.expectedGeneration, '重复 find 必须保持已修复代际令牌稳定')
  repositories.updateAccount(legacyNullAccount.id, { status: 'disabled' }, access)

  const legacyWhitespaceAccount = createActiveCoolingAccount('冷却复测旧空白代际自愈', 'sk-cooldown-legacy-whitespace', group.id)
  const offsetObservation = '2024-02-03T04:05:06.789+08:00'
  const canonicalOffsetObservation = new Date(offsetObservation).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET cooldown_retest_observation_started_at = ?,
        cooldown_retest_generation = '   ',
        cooldown_until = ?
    WHERE id = ?
  `).run(`  ${offsetObservation}  `, new Date(Date.now() - 1000).toISOString(), legacyWhitespaceAccount.id)
  const legacyWhitespaceCandidate = repositories.findAccountForCooldownRetest(legacyWhitespaceAccount.id)
  assert.equal(legacyWhitespaceCandidate?.cooldownRetestObservationStartedAt, canonicalOffsetObservation, '旧空白/offset 观察起点应规范化为 canonical ISO')
  assert(legacyWhitespaceCandidate.cooldownRetestGeneration?.trim(), '空白代际必须替换为非空新令牌')
  const legacyWhitespaceGuard = currentCooldownRetestGuard(legacyWhitespaceAccount.id)
  assert.equal(legacyWhitespaceGuard.expectedObservationStartedAt, canonicalOffsetObservation, '规范化观察起点必须可作为 current CAS guard')
  const legacyWhitespaceDefer = repositories.deferCooldownAccountRetest(legacyWhitespaceAccount.id, {
    ...legacyWhitespaceGuard,
    delaySeconds: 60
  })
  assert.equal(legacyWhitespaceDefer.changed, true, '规范化后的 current guard 必须能原子写回')
  repositories.updateAccount(legacyWhitespaceAccount.id, { status: 'disabled' }, access)

  const legacyInvalidDateAccount = createActiveCoolingAccount('冷却复测语义非法日期自愈', 'sk-cooldown-legacy-invalid-date', group.id)
  const legacyInvalidDateOriginalGuard = currentCooldownRetestGuard(legacyInvalidDateAccount.id)
  const invalidCanonicalShapedObservation = '2026-13-01T00:00:00.000Z'
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET cooldown_retest_observation_started_at = ?, cooldown_until = ?
    WHERE id = ?
  `).run(invalidCanonicalShapedObservation, new Date(Date.now() - 1000).toISOString(), legacyInvalidDateAccount.id)
  const invalidDateRepairStartedAt = Date.now()
  const legacyInvalidDateCandidate = repositories.findAccountForCooldownRetest(legacyInvalidDateAccount.id)
  assert(legacyInvalidDateCandidate?.cooldownRetestObservationStartedAt, '外形 canonical 但语义非法的观察起点必须被自愈')
  const repairedInvalidDateObservationMs = Date.parse(legacyInvalidDateCandidate.cooldownRetestObservationStartedAt)
  assert(Number.isFinite(repairedInvalidDateObservationMs), '语义非法日期必须替换为有效 ISO')
  assert.equal(new Date(repairedInvalidDateObservationMs).toISOString(), legacyInvalidDateCandidate.cooldownRetestObservationStartedAt, '语义非法日期必须替换为 canonical ISO')
  assert(repairedInvalidDateObservationMs >= invalidDateRepairStartedAt - 1000 && repairedInvalidDateObservationMs <= Date.now() + 1000, '语义非法日期必须替换为当前观察时间')
  assert.notEqual(legacyInvalidDateCandidate.cooldownRetestGeneration, legacyInvalidDateOriginalGuard.expectedGeneration, '语义非法日期修复必须轮换 generation 使旧队列项失效')
  repositories.updateAccount(legacyInvalidDateAccount.id, { status: 'disabled' }, access)

  const controlWhitespaceGenerationCases = [
    { label: 'tab', value: '\t' },
    { label: 'LF', value: '\n' },
    { label: 'CRLF', value: '\r\n' },
    { label: '前后混合空白', value: ' \t\r\n ' }
  ] as const
  for (const [caseIndex, whitespaceCase] of controlWhitespaceGenerationCases.entries()) {
    const whitespaceAccount = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `冷却复测 ${whitespaceCase.label} generation 自愈`,
      type: 'api_key',
      credentials: {
        api_key: `sk-cooldown-whitespace-generation-${caseIndex}`,
        base_url: mockBaseUrl
      },
      status: 'active',
      healthCheckEndpointMode: 'chat_json',
      groupId: group.id,
      supportedModels: gptSupportedModels
    }, access)
    activateTestAccount(whitespaceAccount.id)
    assert(repositories.setAccountGroup(whitespaceAccount.id, group.id, access), `${whitespaceCase.label} generation 测试账户应能绑定分组`)
    repositories.markAccountTemporaryUnavailable(whitespaceAccount.id, `模拟 ${whitespaceCase.label} legacy generation`)
    const originalWhitespaceGuard = currentCooldownRetestGuard(whitespaceAccount.id)
    databaseModule.getBusinessDatabase().prepare(`
      UPDATE accounts
      SET cooldown_retest_generation = ?, cooldown_until = ?
      WHERE id = ?
    `).run(whitespaceCase.value, new Date(Date.now() - 1000).toISOString(), whitespaceAccount.id)
    const repairedWhitespaceCandidate = repositories.findAccountForCooldownRetest(whitespaceAccount.id)
    assert(repairedWhitespaceCandidate, `${whitespaceCase.label} generation 必须原子修复并进入候选`)
    assert(repairedWhitespaceCandidate.cooldownRetestGeneration?.trim(), `${whitespaceCase.label} generation 必须原子修复为非空 token`)
    assert.equal(repairedWhitespaceCandidate.cooldownRetestObservationStartedAt, originalWhitespaceGuard.expectedObservationStartedAt, `${whitespaceCase.label} generation 修复不得重置有效观察起点`)
    assert.notEqual(repairedWhitespaceCandidate.cooldownRetestGeneration, originalWhitespaceGuard.expectedGeneration, `${whitespaceCase.label} generation 修复必须轮换 token`)
    const repairedWhitespaceGuard = currentCooldownRetestGuard(whitespaceAccount.id)
    const whitespaceProbeHitBefore = mockOpenAIResponseHitCount
    assert(cooldownRetestService.enqueueCooldownAccountRetest(repairedWhitespaceCandidate, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1
    }), `${whitespaceCase.label} 修复后的 generation 必须可入队`)
    const whitespaceDefer = repositories.deferCooldownAccountRetest(whitespaceAccount.id, {
      ...repairedWhitespaceGuard,
      delaySeconds: 60
    })
    assert.equal(whitespaceDefer.changed, true, `${whitespaceCase.label} 修复后的 generation 必须可写回`)
    await waitForRetestQueueCompletion()
    assert.equal(mockOpenAIResponseHitCount, whitespaceProbeHitBefore, `${whitespaceCase.label} 入队后被 defer 的任务必须在预检丢弃且不访问上游`)
    repositories.updateAccount(whitespaceAccount.id, { status: 'disabled' }, access)
  }

  const sameMillisecondAccount = createActiveCoolingAccount('冷却复测同毫秒代际隔离', 'sk-cooldown-same-millisecond', group.id)
  const sameMillisecondObservation = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?
  `).run(sameMillisecondObservation, new Date(Date.now() - 1000).toISOString(), sameMillisecondAccount.id)
  const firstEpisodeGuard = currentCooldownRetestGuard(sameMillisecondAccount.id)
  assert.equal(repositories.recordCooldownAccountRetestSuccess(sameMillisecondAccount.id, firstEpisodeGuard).changed, true, '第一轮冷却应能正常恢复')
  const secondEpisode = repositories.markAccountTemporaryUnavailable(sameMillisecondAccount.id, '同毫秒进入第二轮冷却')
  assert.equal(secondEpisode?.status, 'temporary_unavailable', '恢复后应能进入第二轮冷却')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?
  `).run(sameMillisecondObservation, new Date(Date.now() - 1000).toISOString(), sameMillisecondAccount.id)
  const secondEpisodeGuard = currentCooldownRetestGuard(sameMillisecondAccount.id)
  assert.equal(secondEpisodeGuard.expectedConfigRevision, firstEpisodeGuard.expectedConfigRevision, '同毫秒代际测试必须保持配置版本不变以隔离 generation 围栏')
  assert.equal(secondEpisodeGuard.expectedDispatchRevision, firstEpisodeGuard.expectedDispatchRevision, '同毫秒代际测试必须保持分发版本不变以隔离 generation 围栏')
  assert.equal(secondEpisodeGuard.expectedObservationStartedAt, firstEpisodeGuard.expectedObservationStartedAt, '两轮冷却应被强制为同一毫秒观察起点')
  const cooldownGenerationPattern = /^cooldown:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
  assert.match(firstEpisodeGuard.expectedGeneration, cooldownGenerationPattern, '第一轮代际必须使用 UUID 令牌')
  assert.match(secondEpisodeGuard.expectedGeneration, cooldownGenerationPattern, '第二轮代际必须使用 UUID 令牌')
  assert.notEqual(secondEpisodeGuard.expectedGeneration, firstEpisodeGuard.expectedGeneration, '两轮同毫秒冷却必须生成不同 UUID 代际')
  const staleSameMillisecondSuccess = repositories.recordCooldownAccountRetestSuccess(sameMillisecondAccount.id, firstEpisodeGuard)
  const staleSameMillisecondFailure = repositories.recordCooldownAccountRetestFailure(sameMillisecondAccount.id, {
    ...firstEpisodeGuard,
    statusCode: 503,
    errorMessage: '第一轮同毫秒迟到失败'
  })
  const staleSameMillisecondDefer = repositories.deferCooldownAccountRetest(sameMillisecondAccount.id, {
    ...firstEpisodeGuard,
    delaySeconds: 60
  })
  assert.equal(staleSameMillisecondSuccess.changed, false, '旧 UUID 成功写回不得命中同毫秒新代际')
  assert.equal(staleSameMillisecondFailure.changed, false, '旧 UUID 失败写回不得命中同毫秒新代际')
  assert.equal(staleSameMillisecondDefer.changed, false, '旧 UUID 延迟写回不得命中同毫秒新代际')
  const sameMillisecondAfterStaleWrites = currentCooldownRetestGuard(sameMillisecondAccount.id)
  assert.equal(sameMillisecondAfterStaleWrites.expectedGeneration, secondEpisodeGuard.expectedGeneration, '迟到三类写回不得替换当前 UUID 代际')
  assert.equal(repositories.findAccountSummary(sameMillisecondAccount.id, access)?.cooldownRetestFailureCount, 0, '迟到失败不得污染新代际失败计数')
  repositories.updateAccount(sameMillisecondAccount.id, { status: 'disabled' }, access)

  const failureThenDeferAccount = createActiveCoolingAccount('冷却复测失败后延迟不缩短', 'sk-cooldown-failure-then-defer', group.id)
  const failureThenDeferGuard = currentCooldownRetestGuard(failureThenDeferAccount.id)
  const longerFailure = repositories.recordCooldownAccountRetestFailure(failureThenDeferAccount.id, {
    ...failureThenDeferGuard,
    statusCode: 503,
    errorMessage: '先写入较长失败退避',
    initialBackoffSeconds: 60,
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  assert.equal(longerFailure.changed, true, '失败退避应成功写入')
  assert(longerFailure.cooldownUntil, '失败退避必须返回冷却截止时间')
  const shorterDefer = repositories.deferCooldownAccountRetest(failureThenDeferAccount.id, {
    ...failureThenDeferGuard,
    delaySeconds: 3
  })
  assert.equal(shorterDefer.changed, false, '较短 defer 不得覆盖已存在的较长失败退避')
  assert(Date.parse(repositories.findAccountSummary(failureThenDeferAccount.id, access)?.cooldownUntil ?? '') >= Date.parse(longerFailure.cooldownUntil), 'failure→defer 的最终 TTL 不得缩短')
  repositories.updateAccount(failureThenDeferAccount.id, { status: 'disabled' }, access)

  const deferThenFailureAccount = createActiveCoolingAccount('冷却复测延迟后失败不缩短', 'sk-cooldown-defer-then-failure', group.id)
  const deferThenFailureGuard = currentCooldownRetestGuard(deferThenFailureAccount.id)
  const longerDefer = repositories.deferCooldownAccountRetest(deferThenFailureAccount.id, {
    ...deferThenFailureGuard,
    delaySeconds: 60
  })
  assert.equal(longerDefer.changed, true, '较长 defer 应成功写入')
  assert(longerDefer.cooldownUntil, 'defer 必须返回冷却截止时间')
  const shorterFailure = repositories.recordCooldownAccountRetestFailure(deferThenFailureAccount.id, {
    ...deferThenFailureGuard,
    statusCode: 503,
    errorMessage: '后到的较短失败退避',
    initialBackoffSeconds: 3,
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  assert.equal(shorterFailure.changed, true, '后到失败仍应记录失败证据')
  assert(Date.parse(repositories.findAccountSummary(deferThenFailureAccount.id, access)?.cooldownUntil ?? '') >= Date.parse(longerDefer.cooldownUntil), 'defer→failure 的最终 TTL 不得缩短')
  repositories.updateAccount(deferThenFailureAccount.id, { status: 'disabled' }, access)

  const ineligibleFailureAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '后台探针配置失败也推进退避回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-ineligible-failure',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(ineligibleFailureAccount.id)
  repositories.markAccountTemporaryUnavailable(ineligibleFailureAccount.id, '模拟后台探针配置失败前冷却态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET health_check_model = ?, cooldown_until = ? WHERE id = ?')
    .run('not-in-supported-models', new Date(Date.now() - 1000).toISOString(), ineligibleFailureAccount.id)
  const dueIneligibleFailure = repositories.findAccountForCooldownRetest(ineligibleFailureAccount.id)
  assert(dueIneligibleFailure, '后台探针配置失败账号应可读取')
  assert(cooldownRetestService.enqueueCooldownAccountRetest(dueIneligibleFailure, {
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  }), '后台探针配置失败账号应能入队')
  await waitForRetestQueueCompletion()
  const recordedIneligibleFailure = repositories.findAccountSummary(ineligibleFailureAccount.id, access)
  assert.equal(recordedIneligibleFailure?.cooldownRetestFailureCount ?? 0, 0, '未形成可归因上游失败的探针不得累计账户失败次数')
  assert.equal(recordedIneligibleFailure?.status, 'temporary_unavailable', '探针任务失败必须保留原冷却状态')

  const probeAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '后台探针通过恢复回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-probe-success',
      base_url: mockBaseUrl
    },
    status: 'active',
    healthCheckEndpointMode: 'chat_json',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(probeAccount.id)
  repositories.markAccountTemporaryUnavailable(probeAccount.id, '模拟后台探针恢复前失败态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), probeAccount.id)
  const dueProbeAccount = repositories.findAccountForCooldownRetest(probeAccount.id)
  assert.equal(dueProbeAccount?.status, 'temporary_unavailable', '后台探针恢复前账号应为临时不可调用')
  assert(dueProbeAccount?.cooldownUntil, '后台探针恢复前应有冷却时间')
  assert(cooldownRetestService.enqueueCooldownAccountRetest(dueProbeAccount, {
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  }), '后台探针恢复账号应能入队')
  const restoredByProbe = await waitForAccountStatus(probeAccount.id, 'active')
  assert(restoredByProbe, '后台探针测试通过后应能读取恢复后的账号')
  assert.equal(restoredByProbe.schedulable, true, '后台探针测试通过后应恢复调度')
  assert.equal(restoredByProbe.cooldownUntil, undefined, '后台探针测试通过后应清理冷却时间')
  assert.equal(restoredByProbe.lastErrorMessage, undefined, '后台探针测试通过后应清理错误原因')

  await waitForRetestQueueCompletion()
  const generationFollowUpAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测新代际队列接续回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-generation-follow-up',
      base_url: mockBaseUrl
    },
    status: 'active',
    healthCheckEndpointMode: 'chat_json',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(generationFollowUpAccount.id)
  assert(repositories.setAccountGroup(generationFollowUpAccount.id, group.id, access), '新代际队列接续账户应能绑定分组')
  repositories.markAccountTemporaryUnavailable(generationFollowUpAccount.id, '第一轮冷却队列代际')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), generationFollowUpAccount.id)
  const firstGenerationCandidate = repositories.findAccountForCooldownRetest(generationFollowUpAccount.id)
  assert(firstGenerationCandidate?.cooldownRetestGeneration, '第一轮冷却必须具有队列 generation')
  const generationProbeHitBefore = mockOpenAIResponseHitCount
  let releaseHeldMockResponse: () => void = () => undefined
  mockOpenAIResponseGate = new Promise<void>((resolvePromise) => {
    releaseHeldMockResponse = resolvePromise
  })
  try {
    assert(cooldownRetestService.enqueueCooldownAccountRetest(firstGenerationCandidate, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1
    }), '第一轮 generation 应能进入队列')
    await waitForMockOpenAIResponseHitCount(generationProbeHitBefore + 1)
    assert.equal(cooldownRetestService.getCooldownAccountRetestQueueSnapshot().runningCount, 1, '首个 generation 必须保持 running 以验证接续行为')
    assert.equal(cooldownRetestService.enqueueCooldownAccountRetest(firstGenerationCandidate, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1
    }), false, '相同 generation 重复投递必须拒绝且不得重复探针')

    const secondGenerationCooling = repositories.markAccountTemporaryUnavailable(generationFollowUpAccount.id, '首任务运行时进入新冷却代际')
    assert.equal(secondGenerationCooling?.status, 'temporary_unavailable', '运行中切换必须保持账户处于新冷却代际')
    databaseModule.getBusinessDatabase()
      .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
      .run(new Date(Date.now() - 1000).toISOString(), generationFollowUpAccount.id)
    const secondGenerationCandidate = repositories.findAccountForCooldownRetest(generationFollowUpAccount.id)
    assert(secondGenerationCandidate?.cooldownRetestGeneration, '第二轮 generation 必须成为当前候选')
    assert.notEqual(secondGenerationCandidate.cooldownRetestGeneration, firstGenerationCandidate.cooldownRetestGeneration, '运行中重开冷却必须轮换 generation')
    assert(cooldownRetestService.enqueueCooldownAccountRetest(secondGenerationCandidate, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1
    }), '不同 generation 必须替换 pending 或登记 running follow-up')
    assert.equal(cooldownRetestService.enqueueCooldownAccountRetest(secondGenerationCandidate, {
      maxPauseMinutes: 10,
      maxRecoveryHours: 1
    }), false, '新 generation 已登记后重复投递仍必须拒绝')
  } finally {
    mockOpenAIResponseGate = undefined
    releaseHeldMockResponse()
  }
  const restoredByGenerationFollowUp = await waitForAccountStatus(generationFollowUpAccount.id, 'active')
  await waitForRetestQueueCompletion()
  assert.equal(mockOpenAIResponseHitCount, generationProbeHitBefore + 2, '旧 generation 与新 follow-up 应各执行一次且不得产生重复探针')
  assert.equal(restoredByGenerationFollowUp.cooldownRetestGeneration, undefined, '新 generation follow-up 成功后必须清理当前代际')

  const owner = repositories.createSystemAccount({
    username: 'cooldown_auth_owner',
    displayName: '冷却复测授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'cooldown_auth_grantee',
    displayName: '冷却复测被授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '冷却复测授权来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '冷却复测授权目标分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const sourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测授权来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-authorized',
      base_url: mockBaseUrl
    },
    status: 'active',
    healthCheckEndpointMode: 'chat_json',
    groupId: ownerGroup.id,
    supportedModels: gptSupportedModels
  }, ownerAccess)
  activateTestAccount(sourceAccount.id)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '冷却复测授权实例恢复回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccount.id)
  assert(authorizedInstance, '授权后应创建被授权方本地账号实例')
  const authorizedTestAccount = repositories.findAccountForTest(authorizedInstance.id, granteeAccess)
  assert.equal(authorizedTestAccount?.accessType, 'authorized', '被授权方测试对象应保持授权视角')
  assert.equal(authorizedTestAccount?.schedulable, true, '授权实例初始应可调度')
  const authorizedCooled = repositories.markAccountTestTemporaryUnavailable(authorizedTestAccount, '模拟授权实例临时不可调用', granteeAccess)
  assert.equal(authorizedCooled?.status, 'temporary_unavailable', '授权实例应进入本地临时不可调用状态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), authorizedInstance.id)
  const authorizedRetestFailure = repositories.recordCooldownAccountRetestFailure(authorizedInstance.id, {
    ...currentCooldownRetestGuard(authorizedInstance.id),
    statusCode: 503,
    errorMessage: '授权实例仍然不可用',
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  assert.equal(authorizedRetestFailure.failureCount, 1, '授权实例后台复测失败应按本地实例累计失败次数')
  assert.equal(authorizedRetestFailure.action, 'retry_immediately', '授权实例首次后台复测失败应继续快速恢复')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), authorizedInstance.id)
  const authorizedCandidate = repositories.listAccountsDueForCooldownRetest(20)
    .find((item) => item.id === authorizedInstance.id)
  assert(authorizedCandidate, '授权实例冷却到期后应进入后台复测候选')
  assert.equal(authorizedCandidate.temporaryUnavailableContinuousProbeEnabled, true, 'SQLite 授权实例复测必须读取来源账户默认开启策略')
  assert.equal(authorizedCandidate.accessType, 'authorized', '后台复测候选应保留授权实例视角，不能伪装成普通账户')
  assert.equal(authorizedCandidate.schedulable, true, '后台复测候选应读取本地实例原始可恢复调度状态')
  assert.equal(authorizedCandidate.cooldownRetestFailureCount, 1, '后台复测候选应读取授权实例本地失败次数')
  assert.equal(authorizedCandidate.bindingSystemAccountId, grantee.id, '后台复测候选应保留被授权方本地绑定系统账户')
  assert.equal(authorizedCandidate.boundGroupId, granteeGroup.id, '后台复测候选应保留被授权方本地分组绑定')
  assert(authorizedCandidate.accountAuthorizationId, '后台复测候选应保留账号授权 ID')
  const authorizedIntegrityAuthorizationId = authorizedCandidate.accountAuthorizationId
  const authorizationIntegrityRow = databaseModule.getBusinessDatabase().prepare(`
    SELECT resource_id, resource_owner_system_account_id, grantee_system_account_id,
           status, expires_at
    FROM resource_authorizations
    WHERE id = ?
  `).get(authorizedIntegrityAuthorizationId) as {
    resource_id: string
    resource_owner_system_account_id: string
    grantee_system_account_id: string
    status: string
    expires_at?: string | null
  } | undefined
  assert(authorizationIntegrityRow, '授权完整性矩阵必须读取原始授权记录')
  const authorizationCandidateIntegrityCases = [
    {
      label: 'authorization resource_id 与来源账户失配',
      sql: 'UPDATE resource_authorizations SET resource_id = ? WHERE id = ?',
      invalidValue: authorizedInstance.id,
      originalValue: authorizationIntegrityRow.resource_id
    },
    {
      label: 'authorization grantee 与目标账户失配',
      sql: 'UPDATE resource_authorizations SET grantee_system_account_id = ? WHERE id = ?',
      invalidValue: owner.id,
      originalValue: authorizationIntegrityRow.grantee_system_account_id
    },
    {
      label: 'authorization owner 与来源账户失配',
      sql: 'UPDATE resource_authorizations SET resource_owner_system_account_id = ? WHERE id = ?',
      invalidValue: grantee.id,
      originalValue: authorizationIntegrityRow.resource_owner_system_account_id
    }
  ] as const
  for (const integrityCase of authorizationCandidateIntegrityCases) {
    const statement = databaseModule.getBusinessDatabase().prepare(integrityCase.sql)
    statement.run(integrityCase.invalidValue, authorizedIntegrityAuthorizationId)
    assert.equal(repositories.findAccountForCooldownRetest(authorizedInstance.id), undefined, `${integrityCase.label}时 find 不得返回候选`)
    assert(!repositories.listAccountsDueForCooldownRetest(100).some((item) => item.id === authorizedInstance.id), `${integrityCase.label}时 list 不得返回候选`)
    statement.run(integrityCase.originalValue, authorizedIntegrityAuthorizationId)
    assert.equal(repositories.findAccountForCooldownRetest(authorizedInstance.id)?.id, authorizedInstance.id, `${integrityCase.label}恢复后当前候选必须重新可见`)
  }

  const authorizedWriteFenceTargetOriginal = databaseModule.getBusinessDatabase().prepare(`
    SELECT schedulable, account_expires_at
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
  `).get(authorizedInstance.id) as { schedulable: number; account_expires_at?: string | null } | undefined
  assert(authorizedWriteFenceTargetOriginal, '授权写回完整性矩阵必须读取目标账户原始状态')
  const expiredWriteFenceAt = new Date(Date.now() - 60_000).toISOString()
  const authorizationWriteIntegrityCases = [
    {
      label: '目标账户 schedulable=0',
      sql: 'UPDATE accounts SET schedulable = ? WHERE id = ?',
      targetId: authorizedInstance.id,
      invalidValue: 0,
      originalValue: authorizedWriteFenceTargetOriginal.schedulable,
      allowExpiredAccountMaintenance: false
    },
    {
      label: '目标账户已过期',
      sql: 'UPDATE accounts SET account_expires_at = ? WHERE id = ?',
      targetId: authorizedInstance.id,
      invalidValue: expiredWriteFenceAt,
      originalValue: authorizedWriteFenceTargetOriginal.account_expires_at ?? null,
      allowExpiredAccountMaintenance: true
    },
    {
      label: 'authorization 已撤销',
      sql: 'UPDATE resource_authorizations SET status = ? WHERE id = ?',
      targetId: authorizedIntegrityAuthorizationId,
      invalidValue: 'revoked',
      originalValue: authorizationIntegrityRow.status,
      allowExpiredAccountMaintenance: false
    },
    {
      label: 'authorization 已过期',
      sql: 'UPDATE resource_authorizations SET expires_at = ? WHERE id = ?',
      targetId: authorizedIntegrityAuthorizationId,
      invalidValue: expiredWriteFenceAt,
      originalValue: authorizationIntegrityRow.expires_at ?? null,
      allowExpiredAccountMaintenance: false
    }
  ] as const
  for (const integrityCase of authorizationWriteIntegrityCases) {
    const currentGuard = currentCooldownRetestGuard(authorizedInstance.id)
    const statement = databaseModule.getBusinessDatabase().prepare(integrityCase.sql)
    statement.run(integrityCase.invalidValue, integrityCase.targetId)
    assertCooldownRetestWritesRejected(authorizedInstance.id, currentGuard, integrityCase.label, {
      allowExpiredAccountMaintenance: integrityCase.allowExpiredAccountMaintenance
    })
    statement.run(integrityCase.originalValue, integrityCase.targetId)
    if (integrityCase.allowExpiredAccountMaintenance) {
      const reactivated = repositories.updateAuthorizedAccountBindingDispatch(authorizedInstance.id, {
        status: 'active',
        clearFailureState: true
      }, granteeAccess)
      assert.equal(reactivated?.status, 'active', '目标过期维护后授权实例必须能恢复测试基线')
      const reactivatedTestAccount = repositories.findAccountForTest(authorizedInstance.id, granteeAccess)
      assert(reactivatedTestAccount, '目标过期维护后必须重新读取授权测试账户')
      const recooled = repositories.markAccountTestTemporaryUnavailable(reactivatedTestAccount, '恢复授权完整性矩阵冷却基线', granteeAccess)
      assert.equal(recooled?.status, 'temporary_unavailable', '目标过期维护后必须恢复冷却测试基线')
      databaseModule.getBusinessDatabase()
        .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
        .run(new Date(Date.now() - 1000).toISOString(), authorizedInstance.id)
    }
    assert.equal(repositories.findAccountForCooldownRetest(authorizedInstance.id)?.id, authorizedInstance.id, `${integrityCase.label}恢复后当前候选必须重新可见`)
  }

  const sourceRuntimeBeforeFences = repositories.findAccountSummary(sourceAccount.id, ownerAccess)
  const sourceRevisionGuard = currentCooldownRetestGuard(authorizedInstance.id)
  assert(sourceRevisionGuard.expectedSourceConfigRevision, '授权实例 guard 必须携带来源配置版本')
  const rotatedSource = repositories.updateAccount(sourceAccount.id, {
    name: '冷却复测授权来源账户（配置已轮换）'
  }, ownerAccess)
  assert(rotatedSource, '来源账户配置轮换应成功')
  assert((rotatedSource.configRevision ?? 1) > sourceRevisionGuard.expectedSourceConfigRevision, '来源账户配置轮换必须推进 config revision')
  const staleSourceSuccess = repositories.recordCooldownAccountRetestSuccess(authorizedInstance.id, sourceRevisionGuard)
  const staleSourceFailure = repositories.recordCooldownAccountRetestFailure(authorizedInstance.id, {
    ...sourceRevisionGuard,
    statusCode: 503,
    errorMessage: '来源配置轮换前的迟到失败'
  })
  const staleSourceDefer = repositories.deferCooldownAccountRetest(authorizedInstance.id, {
    ...sourceRevisionGuard,
    delaySeconds: 60
  })
  assert.equal(staleSourceSuccess.changed, false, '来源配置轮换后旧成功不得恢复授权实例')
  assert.equal(staleSourceFailure.changed, false, '来源配置轮换后旧失败不得污染授权实例')
  assert.equal(staleSourceDefer.changed, false, '来源配置轮换后旧 defer 不得延长授权实例')

  const localDispatchGuard = currentCooldownRetestGuard(authorizedInstance.id)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET dispatch_revision = dispatch_revision + 1 WHERE id = ?')
    .run(authorizedInstance.id)
  const staleDispatchSuccess = repositories.recordCooldownAccountRetestSuccess(authorizedInstance.id, localDispatchGuard)
  const staleDispatchFailure = repositories.recordCooldownAccountRetestFailure(authorizedInstance.id, {
    ...localDispatchGuard,
    statusCode: 503,
    errorMessage: '本地分发版本推进前的迟到失败'
  })
  const staleDispatchDefer = repositories.deferCooldownAccountRetest(authorizedInstance.id, {
    ...localDispatchGuard,
    delaySeconds: 60
  })
  assert.equal(staleDispatchSuccess.changed, false, '本地 dispatch revision 推进后旧成功不得恢复授权实例')
  assert.equal(staleDispatchFailure.changed, false, '本地 dispatch revision 推进后旧失败不得污染授权实例')
  assert.equal(staleDispatchDefer.changed, false, '本地 dispatch revision 推进后旧 defer 不得延长授权实例')

  const invalidRelationGuard = currentCooldownRetestGuard(authorizedInstance.id)
  const relationChangedAt = new Date().toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE group_accounts
    SET enabled = 0, updated_at = ?
    WHERE account_id = ?
      AND system_account_id = ?
      AND account_authorization_id = ?
  `).run(relationChangedAt, authorizedInstance.id, grantee.id, authorizedCandidate.accountAuthorizationId)
  const invalidRelationSuccess = repositories.recordCooldownAccountRetestSuccess(authorizedInstance.id, invalidRelationGuard)
  const invalidRelationFailure = repositories.recordCooldownAccountRetestFailure(authorizedInstance.id, {
    ...invalidRelationGuard,
    statusCode: 503,
    errorMessage: '授权绑定失效后的迟到失败'
  })
  const invalidRelationDefer = repositories.deferCooldownAccountRetest(authorizedInstance.id, {
    ...invalidRelationGuard,
    delaySeconds: 60
  })
  assert.equal(invalidRelationSuccess.changed, false, '授权绑定关系失效后成功写回必须拒绝')
  assert.equal(invalidRelationFailure.changed, false, '授权绑定关系失效后失败写回必须拒绝')
  assert.equal(invalidRelationDefer.changed, false, '授权绑定关系失效后 defer 写回必须拒绝')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE group_accounts
    SET enabled = 1, updated_at = ?
    WHERE account_id = ?
      AND system_account_id = ?
      AND account_authorization_id = ?
  `).run(new Date().toISOString(), authorizedInstance.id, grantee.id, authorizedCandidate.accountAuthorizationId)
  const sourceRuntimeAfterFences = repositories.findAccountSummary(sourceAccount.id, ownerAccess)
  assert.equal(sourceRuntimeAfterFences?.status, sourceRuntimeBeforeFences?.status, '授权实例围栏拒绝不得改变来源账户运行状态')
  assert.equal(sourceRuntimeAfterFences?.cooldownUntil, sourceRuntimeBeforeFences?.cooldownUntil, '授权实例围栏拒绝不得给来源账户写入冷却')
  assert.equal(sourceRuntimeAfterFences?.cooldownRetestFailureCount, sourceRuntimeBeforeFences?.cooldownRetestFailureCount, '授权实例围栏拒绝不得累计来源账户失败次数')
  const currentAuthorizedCandidate = repositories.listAccountsDueForCooldownRetest(20)
    .find((item) => item.id === authorizedInstance.id)
  assert(currentAuthorizedCandidate, '围栏拒绝并恢复绑定后授权实例仍应进入当前候选')
  const authorizedProbeHitBefore = mockOpenAIResponseHitCount
  assert(cooldownRetestService.enqueueCooldownAccountRetest(currentAuthorizedCandidate, {
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  }), '授权实例冷却复测候选应能入队')
  const restoredAuthorized = await waitForAccountStatus(authorizedInstance.id, 'active', granteeAccess)
  assert.equal(restoredAuthorized.status, 'active', '后台探针测试通过后授权实例应恢复为正常')
  assert.equal(restoredAuthorized.schedulable, true, '后台探针测试通过后授权实例应恢复调度')
  assert.equal(restoredAuthorized.cooldownUntil, undefined, '后台探针测试通过后授权实例应清理冷却时间')
  assert.equal(mockOpenAIResponseHitCount, authorizedProbeHitBefore + 1, '授权实例后台复测应真实调用上游探针')
  const sourceAfterAuthorizedRetest = repositories.findAccountSummary(sourceAccount.id, ownerAccess)
  assert.equal(sourceAfterAuthorizedRetest?.status, 'active', '授权实例恢复不应修改授权方原账户状态')

  const quotaOwner = repositories.createSystemAccount({
    username: 'cooldown_quota_auth_owner',
    displayName: '冷却复测额度授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const quotaGrantee = repositories.createSystemAccount({
    username: 'cooldown_quota_auth_grantee',
    displayName: '冷却复测额度被授权方',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const quotaOwnerAccess = { systemAccountId: quotaOwner.id, role: 'user' as const }
  const quotaGranteeAccess = { systemAccountId: quotaGrantee.id, role: 'user' as const }
  const quotaOwnerGroup = repositories.createGroup({
    name: '冷却复测额度来源分组',
    providerCode: 'gpt'
  }, quotaOwnerAccess)
  const quotaGranteeGroup = repositories.createGroup({
    name: '冷却复测额度目标分组',
    providerCode: 'gpt'
  }, quotaGranteeAccess)
  const quotaSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测额度来源账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-quota-limited',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: quotaOwnerGroup.id,
    supportedModels: gptSupportedModels
  }, quotaOwnerAccess)
  activateTestAccount(quotaSourceAccount.id)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: quotaSourceAccount.id,
    granteeType: 'system_account',
    granteeId: quotaGrantee.id,
    targetGroupId: quotaGranteeGroup.id,
    remark: '冷却复测额度耗尽授权实例回归',
    limits: { total: { enabled: true, limit: 1 } }
  }, quotaOwnerAccess)
  const quotaLimitedAuthorization = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(quotaSourceAccount.id, quotaGrantee.id) as { id?: string } | undefined
  assert(quotaLimitedAuthorization?.id, '额度授权应写入运行时授权记录')
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, total_cost_usd, updated_at)
      VALUES (?, 'account_authorization', ?, 1, 1, ?)
    `)
    .run(quotaGrantee.id, quotaLimitedAuthorization.id, new Date().toISOString())
  const quotaLimitedInstance = repositories.listAccounts(quotaGranteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === quotaSourceAccount.id)
  assert(quotaLimitedInstance, '额度授权实例应创建本地账号实例')
  const quotaLimitedTestAccount = repositories.findAccountForTest(quotaLimitedInstance.id, quotaGranteeAccess)
  assert(quotaLimitedTestAccount, '额度授权实例应能读取测试对象')
  repositories.markAccountTestTemporaryUnavailable(quotaLimitedTestAccount, '模拟额度耗尽授权实例临时不可调用', quotaGranteeAccess)
  const scanWindowBaseMs = Date.parse('2000-01-01T00:00:00.000Z')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(scanWindowBaseMs).toISOString(), quotaLimitedInstance.id)
  const scanWindowOwnerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '冷却复测扫描窗口普通账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-scan-window',
      base_url: mockBaseUrl
    },
    status: 'active',
    groupId: group.id,
    supportedModels: gptSupportedModels
  }, access)
  activateTestAccount(scanWindowOwnerAccount.id)
  repositories.markAccountTemporaryUnavailable(scanWindowOwnerAccount.id, '模拟扫描窗口普通账户临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(scanWindowBaseMs + 1).toISOString(), scanWindowOwnerAccount.id)
  const scanWindowCandidates = repositories.listAccountsDueForCooldownRetest(1)
  assert(!scanWindowCandidates.some((item) => item.id === quotaLimitedInstance.id), '授权额度耗尽的授权实例不应进入后台复测候选')
  assert(scanWindowCandidates.some((item) => item.id === scanWindowOwnerAccount.id), '无效授权实例不应占满扫描窗口导致后续普通候选被挡住')
  repositories.updateAccount(scanWindowOwnerAccount.id, { status: 'disabled' }, access)

  const fairnessFirstAccount = createActiveCoolingAccount('冷却复测公平游标账户一', 'sk-cooldown-retest-fairness-1', group.id)
  const fairnessSecondAccount = createActiveCoolingAccount('冷却复测公平游标账户二', 'sk-cooldown-retest-fairness-2', group.id)
  const fairnessBaseMs = scanWindowBaseMs + 2
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(fairnessBaseMs).toISOString(), fairnessFirstAccount.id)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(fairnessBaseMs + 1).toISOString(), fairnessSecondAccount.id)
  const firstFairnessPage = repositories.listAccountsDueForCooldownRetestPage(1)
  assert(firstFairnessPage.nextCursor, '冷却复测分页必须返回复合游标')
  const secondFairnessPage = repositories.listAccountsDueForCooldownRetestPage(100, firstFairnessPage.nextCursor)
  assert(secondFairnessPage.accounts.some((item) => item.id === fairnessSecondAccount.id), '游标后的到期账户必须能进入后续扫描窗口')

  const rawCursorFilteredCount = 201
  const rawCursorBaseMs = Date.parse('1990-01-01T00:00:00.000Z')
  const rawCursorFilteredAccountIds: string[] = []
  const rawCursorAuthorizationIds: string[] = []
  databaseModule.runInDatabaseTransaction(() => {
    for (let index = 0; index < rawCursorFilteredCount; index += 1) {
      const sourceId = `account_cooldown_raw_cursor_source_${index}`
      const authorizationId = `authorization_cooldown_raw_cursor_${index}`
      const accountId = `account_cooldown_raw_cursor_filtered_${index}`
      const timestamp = new Date(rawCursorBaseMs + index).toISOString()
      cloneBusinessDatabaseRow('accounts', 'id = ?', [quotaSourceAccount.id], {
        id: sourceId,
        name: `冷却复测 raw cursor 来源 ${index}`,
        created_at: timestamp,
        updated_at: timestamp
      })
      cloneBusinessDatabaseRow('resource_authorizations', 'id = ?', [quotaLimitedAuthorization.id], {
        id: authorizationId,
        resource_id: sourceId,
        created_at: timestamp,
        updated_at: timestamp
      })
      cloneBusinessDatabaseRow('accounts', 'id = ?', [quotaLimitedInstance.id], {
        id: accountId,
        name: `冷却复测 raw cursor 额度过滤 ${index}`,
        authorization_instance_authorization_id: authorizationId,
        authorization_instance_source_account_id: sourceId,
        cooldown_until: timestamp,
        cooldown_retest_generation: `cooldown:raw-cursor-${index}`,
        created_at: timestamp,
        updated_at: timestamp
      })
      cloneBusinessDatabaseRow('group_accounts', 'group_id = ? AND account_id = ?', [quotaGranteeGroup.id, quotaLimitedInstance.id], {
        account_id: accountId,
        account_authorization_id: authorizationId,
        created_at: timestamp,
        updated_at: timestamp
      })
      rawCursorFilteredAccountIds.push(accountId)
      rawCursorAuthorizationIds.push(authorizationId)
    }
  })
  databaseModule.runInDatabaseTransaction(() => {
    const insertQuotaUsage = databaseModule.getStatsDatabase().prepare(`
      INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, total_cost_usd, updated_at)
      VALUES (?, 'account_authorization', ?, 1, 1, ?)
    `)
    for (let index = 0; index < rawCursorAuthorizationIds.length; index += 1) {
      insertQuotaUsage.run(quotaGrantee.id, rawCursorAuthorizationIds[index], new Date(rawCursorBaseMs + index).toISOString())
    }
  }, databaseModule.getStatsDatabase())
  const rawCursorHealthyOwner = createActiveCoolingAccount('冷却复测 raw cursor 健康 owner', 'sk-cooldown-raw-cursor-owner', group.id)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(rawCursorBaseMs + rawCursorFilteredCount + 1).toISOString(), rawCursorHealthyOwner.id)
  const rawCursorFirstPage = repositories.listAccountsDueForCooldownRetestPage(1)
  assert.equal(rawCursorFirstPage.accounts.length, 0, '前 200 条额度耗尽授权实例在 summary 后过滤时第一页必须可为空')
  assert(rawCursorFirstPage.nextCursor, '第一页 summary 为空时必须仍返回 raw cursor')
  assert.equal(rawCursorFirstPage.nextCursor.id, rawCursorFilteredAccountIds[199], '空第一页 cursor 必须推进到第 200 条原始记录')
  const rawCursorSecondPage = repositories.listAccountsDueForCooldownRetestPage(1, rawCursorFirstPage.nextCursor)
  assert.equal(rawCursorSecondPage.accounts[0]?.id, rawCursorHealthyOwner.id, '跨过 >scanLimit 过滤记录后健康 owner 必须最终可达')
  repositories.updateAccount(rawCursorHealthyOwner.id, { status: 'disabled' }, access)

  assert.equal(cooldownAccountRetestQueueAvailableSlots(10, { pendingCount: 6, runningCount: 3 }), 1, '冷却复测每轮查询数量必须扣除队列已有占用')
  assert.equal(cooldownAccountRetestQueueAvailableSlots(10, { pendingCount: 8, runningCount: 2 }), 0, '冷却复测队列达到 batch 上限后不得继续扫描入队')
  assert.equal(backgroundFullDiagnosticConcurrency, 3, '完整后台诊断并发必须保持小上限，不能随 batch 放大到 10')
  assert.equal(backgroundFullDiagnosticQueueConcurrency(10), 3, '批量为 10 时完整后台诊断实际队列并发仍必须限制为 3')
  assert.equal(backgroundFullDiagnosticQueueConcurrency(1), 1, '批量为 1 时完整后台诊断不应人为放大并发')
  let sharedDiagnosticRunningCount = 0
  let sharedDiagnosticMaxRunningCount = 0
  await Promise.all(Array.from({ length: 8 }, () => runWithBackgroundFullDiagnosticSlot(async () => {
    sharedDiagnosticRunningCount += 1
    sharedDiagnosticMaxRunningCount = Math.max(sharedDiagnosticMaxRunningCount, sharedDiagnosticRunningCount)
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 20))
    sharedDiagnosticRunningCount -= 1
  })))
  assert.equal(sharedDiagnosticMaxRunningCount, 3, '同一 worker 内不同完整诊断队列必须共享最多 3 路门禁')
  assert.equal(backgroundProbeDbServiceTimeoutMs, 30_000, '后台探针 DB service 超时应覆盖启动期统计刷新窗口')
  assert.equal(cooldownAccountRetestStartupDelayMs, 60_000, '冷却复测不得在 worker 启动 2 秒时与统计初始化争抢 DB service')

  console.log('cooldown retest recovery regression passed')
} finally {
  await closeServer(mockOpenAIServer)
  restoreWorkerParentIpc()
  await closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function createMockOpenAIServer(): http.Server {
  return http.createServer((req, res) => {
    const requestPath = req.url?.split('?', 1)[0]
    if (req.method !== 'POST' || (requestPath !== '/v1/responses' && requestPath !== '/v1/chat/completions')) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }
    req.on('end', async () => {
      const responseGate = mockOpenAIResponseGate
      mockOpenAIResponseHitCount += 1
      if (responseGate) await responseGate
      if (requestPath === '/v1/chat/completions') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'chatcmpl_cooldown_retest_probe_success',
          object: 'chat.completion',
          choices: [{
            index: 0,
            message: { role: 'assistant', content: 'OK' },
            finish_reason: 'stop'
          }],
          usage: {
            prompt_tokens: 1,
            completion_tokens: 1,
            total_tokens: 2
          }
        }))
        return
      }
      const completedEvent = {
        type: 'response.completed',
        response: {
          id: 'resp_cooldown_retest_probe_success',
          object: 'response',
          status: 'completed',
          output: [
            {
              type: 'message',
              content: [{ type: 'output_text', text: 'OK' }]
            }
          ],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2
          }
        }
      }
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
      res.end(`event: response.completed\ndata: ${JSON.stringify(completedEvent)}\n\n`)
    })
    req.resume()
  })
}

function activateTestAccount(accountId: string): void {
  assert(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), `测试账号 ${accountId} 应能通过后台健康检查激活`)
}

function createActiveCoolingAccount(name: string, apiKey: string, groupId: string) {
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId,
    supportedModels: ['gpt-5.4']
  }, access)
  activateTestAccount(account.id)
  assert(repositories.setAccountGroup(account.id, groupId, access), `测试账号 ${account.id} 应能绑定分组`)
  const cooled = repositories.markAccountTemporaryUnavailable(account.id, '模拟冷却复测公平扫描')
  assert.equal(cooled?.status, 'temporary_unavailable', `测试账号 ${account.id} 应进入冷却态`)
  return account
}

async function waitForAccountStatus(
  accountId: string,
  status: string,
  accountAccess: AccessScope = access
): Promise<NonNullable<ReturnType<typeof repositories.findAccountSummary>>> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const account = repositories.findAccountSummary(accountId, accountAccess)
    if (account?.status === status) {
      return account
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  throw new Error(`等待账号 ${accountId} 恢复为 ${status} 超时`)
}

async function waitForAccountRetestFailure(accountId: string): Promise<NonNullable<ReturnType<typeof repositories.findAccountSummary>>> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const account = repositories.findAccountSummary(accountId, access)
    if (account && (account.cooldownRetestFailureCount ?? 0) > 0) {
      return account
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  throw new Error(`等待账号 ${accountId} 记录后台复测失败超时`)
}

async function waitForRetestQueueCompletion(): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    const snapshot = cooldownRetestService.getCooldownAccountRetestQueueSnapshot()
    if (Date.now() - startedAt >= 100 && snapshot.pendingCount === 0 && snapshot.runningCount === 0) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 50))
  }
  throw new Error('等待冷却账户复测队列完成超时')
}

async function waitForMockOpenAIResponseHitCount(expectedHitCount: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt < 5000) {
    if (mockOpenAIResponseHitCount >= expectedHitCount) return
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25))
  }
  throw new Error(`等待 mock 上游请求次数达到 ${expectedHitCount} 超时`)
}

function assertSqliteCooldownCandidatePlan(): void {
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  const plan = databaseModule.getBusinessDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT accounts.id
    FROM accounts INDEXED BY idx_accounts_cooldown_retest_candidate_order
    WHERE accounts.health_check_endpoint_mode IN (${endpointModes.map(() => '?').join(', ')})
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('temporary_unavailable', 'rate_limited')
      AND accounts.schedulable = 1
      AND accounts.cooldown_until IS NOT NULL
      AND accounts.cooldown_until <= ?
    ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
    LIMIT 20
  `).all(...endpointModes, new Date().toISOString()) as Array<{ detail?: string }>
  const details = plan.map((row) => row.detail ?? '').join('\n')
  assert.match(details, /idx_accounts_cooldown_retest_candidate_order/, 'SQLite 冷却复测候选必须命中谓词与排序一致的 partial index')
  assert.doesNotMatch(details, /USE TEMP B-TREE FOR ORDER BY/, 'SQLite 冷却复测候选不得为稳定排序创建临时 B-Tree')
}

function assertSqliteCooldownAccountIdLookupPlans(): void {
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  const now = new Date().toISOString()
  const accountId = 'account-cooldown-plan-probe'
  const legacyRepairPlan = databaseModule.getBusinessDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT 1
    FROM accounts
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('temporary_unavailable', 'rate_limited')
      AND accounts.schedulable = 1
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND accounts.cooldown_until IS NOT NULL
      AND accounts.cooldown_until <= ?
      AND (
        accounts.cooldown_retest_observation_started_at IS NULL
        OR TRIM(accounts.cooldown_retest_observation_started_at) = ''
        OR accounts.cooldown_retest_generation IS NULL
        OR TRIM(accounts.cooldown_retest_generation) = ''
        OR strftime('%s', TRIM(accounts.cooldown_retest_observation_started_at)) IS NULL
      )
    LIMIT 1
  `).all(accountId, now) as Array<{ detail?: string }>
  assertSqliteAccountIdLookupPlan(legacyRepairPlan, 'SQLite accountId legacy repair')

  const candidatePlan = databaseModule.getBusinessDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT accounts.id
    FROM accounts
    LEFT JOIN resource_authorizations ra
      ON ra.id = accounts.authorization_instance_authorization_id
    LEFT JOIN accounts source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
     AND source_accounts.deleted_at IS NULL
    WHERE accounts.id = ?
      AND accounts.health_check_endpoint_mode IN (${endpointModes.map(() => '?').join(', ')})
      AND accounts.type IN ('api_key', 'oauth', 'google_oauth')
      AND accounts.deleted_at IS NULL
      AND accounts.status IN ('temporary_unavailable', 'rate_limited')
      AND accounts.schedulable = 1
      AND accounts.cooldown_until IS NOT NULL
      AND accounts.cooldown_until <= ?
      AND accounts.cooldown_retest_observation_started_at IS NOT NULL
      AND TRIM(accounts.cooldown_retest_observation_started_at) <> ''
      AND accounts.cooldown_retest_generation IS NOT NULL
      AND TRIM(accounts.cooldown_retest_generation) <> ''
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR (
          ra.id IS NOT NULL
          AND ra.resource_type = 'account'
          AND ra.resource_id = accounts.authorization_instance_source_account_id
          AND ra.resource_owner_system_account_id = source_accounts.system_account_id
          AND ra.grantee_system_account_id = accounts.system_account_id
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
        )
      )
      AND EXISTS (
        SELECT 1
        FROM group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = accounts.system_account_id
          AND group_accounts.enabled = 1
      )
    ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
    LIMIT 1
  `).all(accountId, ...endpointModes, now, now, now) as Array<{ detail?: string }>
  assertSqliteAccountIdLookupPlan(candidatePlan, 'SQLite accountId cooldown candidate')
}

function assertSqliteAccountIdLookupPlan(plan: Array<{ detail?: string }>, label: string): void {
  const details = plan.map((row) => row.detail ?? '').join('\n')
  assert.match(details, /SEARCH accounts USING INDEX sqlite_autoindex_accounts_\d+ \(id=\?\)/, `${label} 必须命中 accounts 主键`)
  assert.doesNotMatch(details, /SEARCH accounts USING INDEX idx_accounts_cooldown_retest_candidate_order/, `${label} 不得扫描 due partial index`)
}

async function onceListening(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: http.Server): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
    server.closeIdleConnections?.()
  })
}
