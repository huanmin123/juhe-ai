import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

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

assertSqliteCooldownCandidatePlan()

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

let mockOpenAIServer: http.Server | undefined
let mockOpenAIResponseHitCount = 0

try {
  mockOpenAIServer = createMockOpenAIServer()
  mockOpenAIServer.listen(0, '127.0.0.1')
  await onceListening(mockOpenAIServer)
  const mockAddress = mockOpenAIServer.address()
  if (!mockAddress || typeof mockAddress === 'string') {
    throw new Error('冷却复测恢复 mock 上游地址不可用')
  }
  const mockBaseUrl = `http://127.0.0.1:${mockAddress.port}`

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
    groupId: group.id
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
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(expiredObservationStartedAt, new Date(Date.now() - 1000).toISOString(), account.id)

  const longRecovering = repositories.recordCooldownAccountRetestFailure(account.id, {
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
    .prepare('UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?')
    .run(
      new Date(Date.now() - 8 * 24 * 60 * 60_000).toISOString(),
      new Date(Date.now() - 1000).toISOString(),
      account.id
    )
  const terminalFailure = repositories.recordCooldownAccountRetestFailure(account.id, {
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
    groupId: group.id
  }, access)
  activateTestAccount(freshAccount.id)
  assert(repositories.setAccountGroup(freshAccount.id, group.id, access), '冷却复测未超观察窗口账号应能绑定分组')
  repositories.markAccountTemporaryUnavailable(freshAccount.id, '模拟临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(new Date().toISOString(), new Date(Date.now() - 1000).toISOString(), freshAccount.id)

  const stillRecovering = repositories.recordCooldownAccountRetestFailure(freshAccount.id, {
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
    statusCode: 503,
    errorMessage: '事务串行第一次失败',
    maxPauseMinutes: 10,
    maxRecoveryHours: 1
  })
  const serializedSecond = repositories.recordCooldownAccountRetestFailure(serializedAccount.id, {
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
    groupId: group.id
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
    groupId: group.id
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
    temporaryUnavailableContinuousProbeEnabled: false
  }, access)
  assert.equal(repositories.findAccountSummary(boundedAccount.id, access)?.temporaryUnavailableContinuousProbeEnabled, false, 'SQLite 账户读取必须保留持续恢复探活关闭值')
  activateTestAccount(boundedAccount.id)
  repositories.markAccountTemporaryUnavailable(boundedAccount.id, '有界模式临时不可调用')
  const boundedStartedAt = new Date(Date.now() - 9 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?
  `).run(boundedStartedAt, new Date(Date.now() - 1000).toISOString(), boundedAccount.id)
  const boundedRetry = repositories.recordCooldownAccountRetestFailure(boundedAccount.id, {
    statusCode: 503, errorMessage: '有界复测仍失败', maxPauseMinutes: 1440, maxRecoveryHours: 24
  })
  assert.notEqual(boundedRetry.recoveryStage, 'long_term', '十分钟窗口内有界账户不得进入长期每小时复测')
  assert.ok(
    Date.parse(boundedRetry.cooldownUntil ?? '') <= Date.parse(boundedStartedAt) + 10 * 60 * 1000,
    '有界模式下一次复测不得越过十分钟最终探测截止时间'
  )
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?
  `).run(new Date(Date.now() - 11 * 60 * 1000).toISOString(), new Date(Date.now() - 1000).toISOString(), boundedAccount.id)
  const boundedTerminal = repositories.recordCooldownAccountRetestFailure(boundedAccount.id, {
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
    status: 'active', groupId: group.id, temporaryUnavailableContinuousProbeEnabled: false
  }, access)
  activateTestAccount(boundedRateLimited.id)
  repositories.markAccountCooldown(boundedRateLimited.id, new Date(Date.now() - 1000).toISOString(), '有界开关下的限流', 'rate_limited')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ?, cooldown_until = ? WHERE id = ?
  `).run(new Date(Date.now() - 11 * 60 * 1000).toISOString(), new Date(Date.now() - 1000).toISOString(), boundedRateLimited.id)
  const rateStillContinuous = repositories.recordCooldownAccountRetestFailure(boundedRateLimited.id, {
    statusCode: 429, errorMessage: '限流未恢复', maxPauseMinutes: 10, maxRecoveryHours: 24
  })
  assert.notEqual(rateStillContinuous.action, 'error', '持续恢复探活开关不能把 rate_limited 提前转异常')

  const guardedRestore = repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '有界探针旧结果守卫回归', type: 'api_key',
    credentials: { api_key: 'sk-cooldown-guarded', base_url: 'https://api.openai.com/v1' },
    status: 'active', groupId: group.id
  }, access)
  activateTestAccount(guardedRestore.id)
  const guardedCooling = repositories.markAccountTemporaryUnavailable(guardedRestore.id, '旧探针守卫')
  assert(guardedCooling?.cooldownRetestObservationStartedAt)
  const staleRestore = repositories.recordCooldownAccountRetestSuccess(guardedRestore.id, {
    expectedConfigRevision: (guardedCooling.configRevision ?? 1) + 1,
    expectedObservationStartedAt: guardedCooling.cooldownRetestObservationStartedAt
  })
  assert.equal(staleRestore.changed, false, '配置版本变化后的旧成功探针不得恢复账户')
  const currentRestore = repositories.recordCooldownAccountRetestSuccess(guardedRestore.id, {
    expectedConfigRevision: guardedCooling.configRevision ?? 1,
    expectedObservationStartedAt: guardedCooling.cooldownRetestObservationStartedAt
  })
  assert.equal(currentRestore.changed, true, '当前代次的成功探针必须恢复账户')

  const staleFailureAccount = createActiveCoolingAccount('冷却复测旧失败代次守卫', 'sk-cooldown-stale-failure', group.id)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts SET cooldown_retest_observation_started_at = ? WHERE id = ?
  `).run(new Date(Date.now() - 60_000).toISOString(), staleFailureAccount.id)
  const staleFailureQueued = repositories.findAccountSummary(staleFailureAccount.id, access)
  assert(staleFailureQueued?.cooldownRetestObservationStartedAt)
  repositories.clearAccountFailureState(staleFailureAccount.id, access)
  const nextFailureGeneration = repositories.markAccountTemporaryUnavailable(staleFailureAccount.id, '进入新一轮冷却观察')
  assert(nextFailureGeneration?.cooldownRetestObservationStartedAt)
  assert.notEqual(nextFailureGeneration.cooldownRetestObservationStartedAt, staleFailureQueued.cooldownRetestObservationStartedAt, '新一轮冷却必须生成新的观察代次')
  const staleFailure = repositories.recordCooldownAccountRetestFailure(staleFailureAccount.id, {
    statusCode: 503,
    errorMessage: '上一轮迟到失败',
    expectedConfigRevision: staleFailureQueued.configRevision ?? 1,
    expectedObservationStartedAt: staleFailureQueued.cooldownRetestObservationStartedAt
  })
  assert.equal(staleFailure.changed, false, '上一轮迟到失败不得写入新的观察代次')
  assert.equal(repositories.findAccountSummary(staleFailureAccount.id, access)?.cooldownRetestFailureCount, 0, '上一轮迟到失败不得累计到新一轮')

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
    groupId: group.id
  }, access)
  activateTestAccount(ineligibleFailureAccount.id)
  repositories.markAccountTemporaryUnavailable(ineligibleFailureAccount.id, '模拟后台探针配置失败前冷却态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET health_check_model = ?, cooldown_until = ? WHERE id = ?')
    .run('not-in-supported-models', new Date(Date.now() - 1000).toISOString(), ineligibleFailureAccount.id)
  const dueIneligibleFailure = repositories.findAccountSummary(ineligibleFailureAccount.id, access)
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
    groupId: group.id
  }, access)
  activateTestAccount(probeAccount.id)
  repositories.markAccountTemporaryUnavailable(probeAccount.id, '模拟后台探针恢复前失败态')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), probeAccount.id)
  const dueProbeAccount = repositories.findAccountSummary(probeAccount.id, access)
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
    groupId: ownerGroup.id
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
  const authorizedProbeHitBefore = mockOpenAIResponseHitCount
  assert(cooldownRetestService.enqueueCooldownAccountRetest(authorizedCandidate, {
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
    groupId: quotaOwnerGroup.id
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
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 10_000).toISOString(), quotaLimitedInstance.id)
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
    groupId: group.id
  }, access)
  activateTestAccount(scanWindowOwnerAccount.id)
  repositories.markAccountTemporaryUnavailable(scanWindowOwnerAccount.id, '模拟扫描窗口普通账户临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), scanWindowOwnerAccount.id)
  const scanWindowCandidates = repositories.listAccountsDueForCooldownRetest(1)
  assert(!scanWindowCandidates.some((item) => item.id === quotaLimitedInstance.id), '授权额度耗尽的授权实例不应进入后台复测候选')
  assert(scanWindowCandidates.some((item) => item.id === scanWindowOwnerAccount.id), '无效授权实例不应占满扫描窗口导致后续普通候选被挡住')

  const fairnessFirstAccount = createActiveCoolingAccount('冷却复测公平游标账户一', 'sk-cooldown-retest-fairness-1', group.id)
  const fairnessSecondAccount = createActiveCoolingAccount('冷却复测公平游标账户二', 'sk-cooldown-retest-fairness-2', group.id)
  const fairnessBaseMs = Date.now() - 60_000
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
    req.on('end', () => {
      mockOpenAIResponseHitCount += 1
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
    groupId
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

function assertSqliteCooldownCandidatePlan(): void {
  const endpointModes = [...ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES]
  const plan = databaseModule.getBusinessDatabase().prepare(`
    EXPLAIN QUERY PLAN
    SELECT accounts.id
    FROM accounts INDEXED BY idx_accounts_cooldown_retest_candidate_order
    WHERE accounts.health_check_endpoint_mode IN (${endpointModes.map(() => '?').join(', ')})
      AND accounts.type IN ('api_key', 'oauth')
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
