import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AccountSummary } from '../../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'
import type { GroupUsageAccessMetadata } from '../../storage/openai-account-selector.types.js'
import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import type { GatewayUsageContext } from '../../modules/gateway/usage/records.js'
import type { AccountErrorHandlingRule, AccountErrorHandlingRuleAction } from '../../modules/accounts/account-error-policy-validation.js'
import type { OpenAIGatewayClientStrategyContext } from '../../modules/gateway/client-profiles/strategy.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-precheck-runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'precheck-runtime-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  gatewaySideEffects,
  accountConcurrency,
  accountPreparation,
  dispatchPreparation,
  databaseModule,
  usageRecordQueue,
  repositories,
  { handleDbServiceOperation }
] = await Promise.all([
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../shared/account-concurrency.js'),
  import('../../modules/gateway/dispatch/account-preparation.js'),
  import('../../modules/gateway/dispatch/preparation.js'),
  import('../../storage/database.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
const gatewaySettings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 5,
  temporaryUnschedulableRetryIntervalSeconds: 0,
  temporaryUnschedulableRetryAttempts: 0,
  streamCircuitBreakerEnabled: false,
  streamRequestTimeoutSeconds: 120,
  streamIdleTimeoutSeconds: 30,
  streamClientTotalWaitTimeoutSeconds: 270,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

try {
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  testPrecheckSummaryMapperBoundary()
  testLocalSuppressionStoreBoundary()
  testRuntimeDegradationOrderingAndSuccessRecovery()
  await testRuntimeDegradationDispatchPreparationFallback()
  testLocalSuppressionHalfOpenEscalation()
  testUnavailableProxyPreparationEscalatesAfterHalfOpenSequence()
  await testRuntimePrecheckPendingAndSuccessRecovery()
  await testPersistedAccountErrorClearsRuntimeAvailability()
  await testStalePrecheckAfterManualRestoreIsSkipped()
  await testFailedUsageDoesNotMakePrecheckStale()
  await testFreshPrecheckStillMarksTemporaryUnavailable()
  await testPrecheckWaitsForInFlightConcurrencyBeforeMarking()

  console.log('网关账号事前确认运行态与过期写回保护回归通过')
} finally {
  accountConcurrency.clearAccountConcurrency()
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function testPrecheckSummaryMapperBoundary(): void {
  const sideEffectsSource = readFileSync(resolve('src/modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
  const mapperSource = readFileSync(resolve('src/modules/gateway/runtime/account-precheck-summary.mapper.ts'), 'utf8')

  assert(
    sideEffectsSource.includes("from './account-precheck-summary.mapper.js'"),
    '账号事前确认运行态服务应从专用 mapper 导入测试摘要适配器'
  )
  assert(
    sideEffectsSource.includes('accountSummaryFromGatewayPrecheckAccount(state.account, state)'),
    '事前确认探针应通过专用 mapper 构造测试账户摘要'
  )
  assert(
    !sideEffectsSource.includes('function accountSummaryFromUpstreamAccount'),
    '事前确认测试摘要 mapper 不应继续内联在运行态副作用服务'
  )
  assert(
    !sideEffectsSource.includes('function gatewayAccountSummarySystemAccountId'),
    '授权绑定系统账户解析不应继续内联在运行态副作用服务'
  )
  assert(
    mapperSource.includes('export function accountSummaryFromGatewayPrecheckAccount'),
    '应导出事前确认测试摘要专用 mapper'
  )
  assert(mapperSource.includes('schedulable: true'), '事前确认测试摘要应保持强制探针可调度语义')
  assert(
    mapperSource.includes('permissions:') && mapperSource.includes('canUse: true'),
    '事前确认测试摘要应保持可用于测试的权限语义'
  )
  assert(
    mapperSource.includes('bindingSystemAccountId') && mapperSource.includes('gatewayAccountSummarySystemAccountId(account)'),
    '授权账户测试摘要应使用绑定系统账户维度'
  )
  assert(
    mapperSource.includes('boundGroupId') && mapperSource.includes('gatewayAccountSummaryBoundGroupId(account)'),
    '授权账户测试摘要应使用绑定分组维度'
  )
}

function testLocalSuppressionStoreBoundary(): void {
  const sideEffectsSource = readFileSync(resolve('src/modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
  const storeSource = readFileSync(resolve('src/modules/gateway/runtime/account-local-suppression-store.ts'), 'utf8')

  assert(
    sideEffectsSource.includes("from './account-local-suppression-store.js'"),
    '账号运行态服务应从本地 suppression store 导入本地避让能力'
  )
  assert(
    sideEffectsSource.includes('snapshotLocalAccountRuntimeAvailability(isPrecheckRuntimeBlocking)'),
    '运行态快照应由服务组合本地避让快照和 precheck 状态'
  )
  assert(
    sideEffectsSource.includes('filterLocalAccountSuppressions(accounts, isPrecheckRuntimeBlocking, options)'),
    '候选过滤应委托本地 suppression store，同时保留 precheck 阻断谓词'
  )
  assert.doesNotMatch(sideEffectsSource, /const localAccountSuppressions = new Map/)
  assert.doesNotMatch(sideEffectsSource, /function acquireLocalHalfOpenLease/)
  assert.doesNotMatch(sideEffectsSource, /function isLocalSuppressionBlocking/)
  assert.match(storeSource, /const localAccountSuppressions = new Map/)
  assert.match(storeSource, /export function suppressLocalAccountForGatewayFailure/)
  assert.match(storeSource, /export function filterLocalAccountSuppressions/)
  assert.match(storeSource, /export function snapshotLocalAccountRuntimeAvailability/)
  assert.match(storeSource, /getAccountCurrentConcurrency/)
}

function testRuntimeDegradationOrderingAndSuccessRecovery(): void {
  const primary = createRuntimeAccount('runtime-degraded-primary')
  const backup = createRuntimeAccount('runtime-normal-backup')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  const degraded = gatewaySideEffects.degradeGatewayAccountForRuntimeFailure(primary, '模拟近期上游失败')
  assert.equal(degraded.status, 'degraded', '运行态失败应先写入调度降级，而不是直接处罚账号')
  assert.equal(degraded.failureCount, 1, '首次运行态降级应记录失败次数')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[primary.id]?.status,
    'degraded',
    '调度降级应进入运行态快照供前端展示'
  )

  const ordered = gatewaySideEffects.orderGatewayAccountsByRuntimeDegradation([primary, backup])
  assert.equal(ordered.applied, true, '存在降级账号时应应用调度排序')
  assert.equal(ordered.bypassedAllDegraded, false, '仍有正常账号时不应进入全降级兜底')
  assert.deepEqual(
    ordered.accounts.map((account) => account.id),
    [backup.id, primary.id],
    '正常候选应优先于运行态降级候选'
  )
  assert.deepEqual(ordered.degradedAccountIds, [primary.id], '降级排序结果应报告被降级账号')

  gatewaySideEffects.degradeGatewayAccountForRuntimeFailure(backup, '模拟备选账号近期失败')
  const allDegraded = gatewaySideEffects.orderGatewayAccountsByRuntimeDegradation([primary, backup])
  assert.equal(allDegraded.applied, false, '全部账号降级时不应再重排候选')
  assert.equal(allDegraded.bypassedAllDegraded, true, '全部候选均降级时应允许兜底选择')
  assert.deepEqual(
    allDegraded.accounts.map((account) => account.id),
    [primary.id, backup.id],
    '全部降级时应保持原顺序交给后续调度策略兜底'
  )

  gatewaySideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account: primary,
    input: { success: true }
  })
  const afterSuccess = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()
  assert.equal(afterSuccess[primary.id], undefined, '账号真实成功后应解除运行态调度降级')
  assert.equal(afterSuccess[backup.id]?.status, 'degraded', '其他账号的运行态降级不应被误清理')
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
}

async function testRuntimeDegradationDispatchPreparationFallback(): Promise<void> {
  const primary = createRuntimeAccount('runtime-degraded-prepare-primary')
  const backup = createRuntimeAccount('runtime-degraded-prepare-backup')
  const metadataLabels: string[] = []

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.degradeGatewayAccountForRuntimeFailure(primary, '模拟主账号近期失败')
  gatewaySideEffects.degradeGatewayAccountForRuntimeFailure(backup, '模拟备账号近期失败')

  const fallbackReasons: string[] = []
  const readyResult = await dispatchPreparation.prepareOpenAIGatewayDispatchAccounts({
    req: buildRuntimeGatewayRequest(),
    res: { writableEnded: false },
    auditCapture: {
      startAttempt: () => '',
      completeAttempt: () => undefined,
      addGatewayMetadata: (input: { label: string }) => {
        metadataLabels.push(input.label)
      }
    },
    usageContext: buildRuntimeGatewayUsageContext('trace-runtime-degraded-prepare-ready'),
    startedAt: Date.now(),
    candidateAccounts: [primary, backup],
    modelPriority: { rankByAccountId: new Map() },
    groupAccess: runtimePreparationGroupAccess(),
    systemAccountId: 'sys_admin',
    apiKeyId: 'key-runtime-degraded-prepare',
    groupId: 'group-runtime-degraded-prepare',
    clientIp: '127.0.0.1',
    clientStrategy: {} as OpenAIGatewayClientStrategyContext,
    requestLane: 'text',
    attemptFallback: async (reason: string) => {
      fallbackReasons.push(reason)
      return { attempted: false }
    }
  } as unknown as Parameters<typeof dispatchPreparation.prepareOpenAIGatewayDispatchAccounts>[0])

  assert.deepEqual(fallbackReasons, ['runtime_degraded'], '当前分组全降级时应以 runtime_degraded 原因尝试后备号池')
  assert.equal(readyResult.outcome, 'ready', '没有可用后备号池时，全降级当前组仍应作为最后兜底继续调度')
  if (readyResult.outcome === 'ready') {
    assert.deepEqual(
      readyResult.accounts.map((account) => account.id),
      [primary.id, backup.id],
      '全降级且没有后备号池时应保持当前候选顺序'
    )
    readyResult.releaseClientIpConcurrency()
  }
  assert(metadataLabels.includes('runtime_account_degradation'), '全降级调度准备应写入运行态降级审计 metadata')

  const fallbackResult = await dispatchPreparation.prepareOpenAIGatewayDispatchAccounts({
    req: buildRuntimeGatewayRequest(),
    res: { writableEnded: false },
    auditCapture: {
      startAttempt: () => '',
      completeAttempt: () => undefined,
      addGatewayMetadata: () => undefined
    },
    usageContext: buildRuntimeGatewayUsageContext('trace-runtime-degraded-prepare-fallback'),
    startedAt: Date.now(),
    candidateAccounts: [primary, backup],
    modelPriority: { rankByAccountId: new Map() },
    groupAccess: runtimePreparationGroupAccess(),
    systemAccountId: 'sys_admin',
    apiKeyId: 'key-runtime-degraded-prepare',
    groupId: 'group-runtime-degraded-prepare',
    clientIp: '127.0.0.1',
    clientStrategy: {} as OpenAIGatewayClientStrategyContext,
    requestLane: 'text',
    attemptFallback: async (reason: string) => {
      assert.equal(reason, 'runtime_degraded', '全降级后备切换原因应保持 runtime_degraded')
      return { attempted: true }
    }
  } as unknown as Parameters<typeof dispatchPreparation.prepareOpenAIGatewayDispatchAccounts>[0])
  assert.equal(fallbackResult.outcome, 'fallback', '后备号池可用时，全降级当前组应切换到后备上下文')
  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
}

async function testRuntimePrecheckPendingAndSuccessRecovery(): Promise<void> {
  const account = createRuntimeAccount('precheck-runtime-account')
  for (let index = 0; index < 4; index += 1) {
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
      systemAccountId: 'sys_admin',
      groupId: 'group-a',
      apiKeyId: 'key-a',
      clientIp: '10.0.0.1',
      endpoint: '/v1/responses',
      reason: '模拟网关短窗口失败'
    })
  }
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '未达到阈值前不应产生账号运行态避让')

  gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
    systemAccountId: 'sys_admin',
    groupId: 'group-a',
    apiKeyId: 'key-b',
    clientIp: '10.0.0.2',
    endpoint: '/v1/responses',
    reason: '模拟网关短窗口失败'
  })
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '达到数量和多 IP 阈值但观察时间不足时不应立即进入事前确认')

  await delay(2100)
  gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
    systemAccountId: 'sys_admin',
    groupId: 'group-a',
    apiKeyId: 'key-b',
    clientIp: '10.0.0.2',
    endpoint: '/v1/responses',
    reason: '模拟网关短窗口持续失败'
  })

  const runtime = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert(runtime, '达到短窗口多来源阈值后应写入账号运行态缓存')
  assert.equal(runtime.status, 'precheck_pending')
  assert.equal(runtime.failureCount, 6)
  assert.equal(runtime.distinctClientIpCount, 2)
  assert.equal(runtime.distinctApiKeyCount, 2)
  assert.equal(runtime.precheckAttemptCount, 0)
  assert(runtime.reason?.includes('等待事前确认'), '运行态原因应说明这是事前确认前的短暂状态')

  gatewaySideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account,
    input: { success: true }
  })
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '真实成功回写应清理账号本地 suppression、failure storm 和待执行预检查'
  )

  for (let index = 0; index < 4; index += 1) {
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
      systemAccountId: 'sys_admin',
      groupId: 'group-a',
      apiKeyId: 'key-a',
      clientIp: '10.0.0.1',
      endpoint: '/v1/responses',
      reason: '模拟近期成功后的网关短窗口失败'
    })
  }
  await delay(2100)
  gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
    systemAccountId: 'sys_admin',
    groupId: 'group-a',
    apiKeyId: 'key-b',
    clientIp: '10.0.0.2',
    endpoint: '/v1/responses',
    reason: '模拟近期成功后的网关短窗口失败'
  })
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '近期真实成功后，即使短窗口失败达到数量阈值，也不应立即升级事前确认'
  )

  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000)
  assert(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], '测试本地屏蔽应存在')
  const clearSuppressedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability(account)
  assert.equal(clearSuppressedResult.cleared, true, '手动恢复入口应报告已清理本地运行态屏蔽')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '手动恢复入口应能清理账号本地运行态屏蔽'
  )

  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000, '模拟探针失败避让', 'precheck_failed')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.status, 'precheck_failed', '应能构造探针失败运行态')
  const clearFailedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability({ accountId: account.id })
  assert.equal(clearFailedResult.cleared, true, '手动恢复入口应能清理探针失败运行态')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '探针失败运行态清理后不应残留')

  const authorizedAccount = createRuntimeAuthorizedAccount('precheck-runtime-authorized-account')
  for (let index = 0; index < 5; index += 1) {
    if (index === 4) {
      await delay(2100)
    }
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest(authorizedAccount, undefined, {
      systemAccountId: 'sys_grantee',
      groupId: 'group-authorized',
      apiKeyId: index < 4 ? 'key-authorized-a' : 'key-authorized-b',
      clientIp: index < 4 ? '10.0.1.1' : '10.0.1.2',
      endpoint: '/v1/responses',
      reason: '模拟授权账号短窗口失败'
    })
  }
  const authorizedRuntimeKey = `${authorizedAccount.id}:authorized:sys_grantee:group-authorized:auth-account-a`
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[authorizedRuntimeKey]?.status, 'precheck_pending', '授权账号运行态应按本地绑定维度隔离')
  const clearAuthorizedResult = gatewaySideEffects.clearGatewayAccountRuntimeAvailability({
    accountId: authorizedAccount.id,
    authorizedBinding: {
      systemAccountId: 'sys_grantee',
      groupId: 'group-authorized',
      accountAuthorizationId: 'auth-account-a'
    }
  })
  assert.equal(clearAuthorizedResult.cleared, true, '手动恢复入口应能清理授权账号绑定维度运行态')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[authorizedRuntimeKey], undefined, '授权账号绑定维度运行态清理后不应残留')
}

function testLocalSuppressionHalfOpenEscalation(): void {
  const account = createRuntimeAccount('local-suppression-half-open-account')
  const first = gatewaySideEffects.suppressGatewayAccountLocally(account, undefined, '第一轮上游失败')
  assert.equal(first.action, 'suppressed', '首次上游失败应进入短暂避让')
  assert.equal(first.localFailureCount, 1, '首次短暂避让应记录第 1 轮')
  assert.equal(first.delayMs, 3_000, '首次短暂避让应从 3 秒开始')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.localFailureCount,
    1,
    '运行态快照应展示短暂避让轮次'
  )

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第一轮到期', 'local_suppressed', { localFailureCount: 1 })
  const firstHalfOpen = gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  assert.equal(firstHalfOpen.accounts.length, 1, '短暂避让到期后应允许一个真实请求半开探测')
  assert.equal(firstHalfOpen.acquiredHalfOpenLeases.length, 1, '半开探测放行时应返回本次请求持有的租约')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.status, 'half_open', '获得租约后运行态应显示半开探测')
  const blockedDuringHalfOpen = gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  assert.equal(blockedDuringHalfOpen.allSuppressed, true, '半开探测进行中应阻止其他请求同时打回该账号')

  const second = gatewaySideEffects.suppressGatewayAccountLocally(account, undefined, '第一轮半开失败')
  assert.equal(second.action, 'suppressed', '第一轮半开失败后仍应留在短暂避让')
  assert.equal(second.localFailureCount, 2, '第一轮半开失败后应进入第 2 轮')
  assert.equal(second.delayMs, 5_000, '第二轮短暂避让应为 5 秒')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第二轮到期', 'local_suppressed', { localFailureCount: 2 })
  gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  const third = gatewaySideEffects.suppressGatewayAccountLocally(account, undefined, '第二轮半开失败')
  assert.equal(third.action, 'suppressed', '第二轮半开失败后仍应留在短暂避让')
  assert.equal(third.localFailureCount, 3, '第二轮半开失败后应进入第 3 轮')
  assert.equal(third.delayMs, 10_000, '第三轮短暂避让应为 10 秒')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第三轮到期', 'local_suppressed', { localFailureCount: 3 })
  gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  const precheck = gatewaySideEffects.suppressGatewayAccountLocally(account, undefined, '第三轮半开失败')
  assert.equal(precheck.action, 'precheck_required', '第三轮半开失败后应要求进入事前确认')
  assert.equal(precheck.localFailureCount, 4, '触发事前确认时应记录已超过短暂避让阶梯')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '半开租约到期但请求仍在途', 'half_open', {
    localFailureCount: 1,
    halfOpenLeaseUntilMs: Date.now() - 1_000
  })
  const inFlightSlot = accountConcurrency.tryAcquireAccountConcurrency(account.id, 10)
  assert.equal(inFlightSlot.acquired, true, '半开租约在途回归前应先占用账号并发槽')
  const blockedByInFlightHalfOpen = gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  assert.equal(blockedByInFlightHalfOpen.allSuppressed, true, '半开租约 TTL 已过但请求仍在途时，不应放行第二个半开请求')
  inFlightSlot.release()
  const reacquiredAfterInFlightDone = gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  assert.equal(reacquiredAfterInFlightDone.accounts.length, 1, '半开在途请求结束后，过期租约应允许新的探测请求接管')
  assert.equal(reacquiredAfterInFlightDone.acquiredHalfOpenLeases.length, 1, '重新接管半开探测时应返回新的租约')
  assert.equal(reacquiredAfterInFlightDone.acquiredHalfOpenLeases[0]?.release(), true, '半开探测完成后应能按租约释放')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '释放未失败的半开租约后不应继续展示活跃屏蔽')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
}

function testUnavailableProxyPreparationEscalatesAfterHalfOpenSequence(): void {
  const account = {
    ...createRuntimeAccount('preparation-proxy-half-open-account'),
    proxyProfileId: 'proxy-profile-broken',
    proxyProfileUnavailable: true,
    proxyProfileErrorMessage: '代理配置不可用：回归测试'
  }
  const failedProxyDispatchKeys = new Map<string, string>()
  const req = buildRuntimeGatewayRequest()

  accountPreparation.handleUnavailableProxyProfile(
    req,
    buildRuntimeGatewayUsageContext('preparation-proxy-trace-1'),
    account,
    gatewaySettings,
    failedProxyDispatchKeys,
    true,
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest
  )
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.localFailureCount,
    1,
    '代理准备失败首次应进入第 1 轮短暂避让'
  )

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第一轮代理避让到期', 'local_suppressed', { localFailureCount: 1 })
  gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  accountPreparation.handleUnavailableProxyProfile(
    req,
    buildRuntimeGatewayUsageContext('preparation-proxy-trace-2'),
    account,
    gatewaySettings,
    failedProxyDispatchKeys,
    true,
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest
  )
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.localFailureCount,
    2,
    '代理准备失败第一轮半开失败后应进入第 2 轮短暂避让'
  )

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第二轮代理避让到期', 'local_suppressed', { localFailureCount: 2 })
  gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  accountPreparation.handleUnavailableProxyProfile(
    req,
    buildRuntimeGatewayUsageContext('preparation-proxy-trace-3'),
    account,
    gatewaySettings,
    failedProxyDispatchKeys,
    true,
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest
  )
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.localFailureCount,
    3,
    '代理准备失败第二轮半开失败后应进入第 3 轮短暂避让'
  )

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 0, '第三轮代理避让到期', 'local_suppressed', { localFailureCount: 3 })
  gatewaySideEffects.filterLocallySuppressedGatewayAccounts([account], { acquireHalfOpenLease: true })
  accountPreparation.handleUnavailableProxyProfile(
    req,
    buildRuntimeGatewayUsageContext('preparation-proxy-trace-4'),
    account,
    gatewaySettings,
    failedProxyDispatchKeys,
    true,
    gatewaySideEffects.recordGatewayAccountFailureForPrecheckForTest
  )
  const runtime = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert.equal(runtime?.status, 'precheck_pending', '代理准备失败第三轮半开失败后应进入事前确认阶段')
  assert.match(runtime?.reason ?? '', /短暂避让半开探测连续失败/, '代理准备失败升级原因应保留短暂避让连续失败语义')

  gatewaySideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
}

async function testPersistedAccountErrorClearsRuntimeAvailability(): Promise<void> {
  const { account, gatewayAccount } = createGatewayAccount('落库错误清理运行态', [
    accountErrorRule('测试 529 冷却', [529], 'temp_unschedulable')
  ])
  gatewaySideEffects.suppressGatewayAccountLocallyForTest(account.id, 60_000, '写库前临时避让')
  assert.equal(gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]?.status, 'local_suppressed', '写库前应允许运行态短避让')

  gatewaySideEffects.enqueueGatewayAccountErrorHandlingSideEffect({
    type: 'apply_account_error_handling',
    account: gatewayAccount,
    input: {
      success: false,
      statusCode: 529,
      bodyText: '{"error":{"code":"overloaded","message":"模拟 529 失败"}}',
      trafficSource: 'manual_account_test'
    }
  })
  await withDbServiceRole(() => gatewaySideEffects.flushGatewayAccountSideEffectsForTest())

  const latest = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(latest?.status, 'temporary_unavailable', '账户错误处理落库后账号应进入临时不可调用')
  assert.match(latest?.lastErrorMessage ?? '', /模拟 529 失败/, '落库后的最后错误应保留策略命中的真实摘要')
  assert.equal(
    gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id],
    undefined,
    '错误已经落库后不应再保留同一账号运行态错误，避免前端出现双来源原因'
  )
}

async function testStalePrecheckAfterManualRestoreIsSkipped(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查过期写回手动恢复')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()
  await delay(5)
  const cooled = repositories.markAccountTemporaryUnavailable(account.id, '模拟较早预检查先写入冷却')
  assert.equal(cooled?.status, 'temporary_unavailable', '测试账号应先被写入临时不可调用')
  const restored = repositories.clearAccountFailureState(account.id, adminAccess)
  assert.equal(restored?.status, 'active', '测试账号应已手动恢复正常')

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '较早预检查失败不应覆盖手动恢复',
    precheckStartedAt
  })
  assert.equal(result.updated, false, '手动恢复后的过期预检查写回不应再次改状态')
  assert.equal(result.skippedReason, 'stale_account_updated', '过期预检查应被识别为账号状态已更新')
  assertActiveAccount(account.id, '手动恢复后的过期预检查不应把账号改回临时不可调用')
  assert.equal(group.providerCode, 'gpt', '测试分组应为 GPT 分组')
}

async function testFailedUsageDoesNotMakePrecheckStale(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查失败用量不算恢复')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()
  await delay(5)
  repositories.createUsageRecordsBatch([{
    traceId: 'precheck-failed-usage',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: group.id,
    accountId: account.id,
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 502,
    success: false,
    durationMs: 10,
    createdAt: new Date().toISOString()
  }])
  const afterFailedUsage = repositories.findAccountSummary(account.id, adminAccess)
  assert(afterFailedUsage?.lastUsedAt && afterFailedUsage.lastUsedAt > precheckStartedAt, '失败使用记录应刷新 lastUsedAt 以覆盖误判风险')

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '失败使用记录不能伪装成恢复；HTTP 403；insufficient_quota；余额和订阅额度均不足',
    precheckStartedAt
  })
  assert.equal(result.updated, true, '仅有失败使用记录时，预检查仍应能写入临时不可调用')
  const afterPrecheck = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterPrecheck?.status, 'temporary_unavailable', '失败使用记录不应阻止预检查降级')
  assert.match(afterPrecheck?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '预检查写库应保留探针传入的真实上游错误摘要')
}

async function testFreshPrecheckStillMarksTemporaryUnavailable(): Promise<void> {
  const { account, gatewayAccount } = createGatewayAccount('预检查正常写回')
  await delay(5)
  const precheckStartedAt = new Date().toISOString()

  const result = await handleDbServiceOperation({
    type: 'mark_account_precheck_temporary_unavailable',
    account: gatewayAccount,
    reason: '模拟当前预检查失败；HTTP 403；insufficient_quota；余额和订阅额度均不足',
    precheckStartedAt
  })
  assert.equal(result.updated, true, '没有更新状态介入时，预检查仍应写入临时不可调用')
  const afterPrecheck = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterPrecheck?.status, 'temporary_unavailable', '当前预检查失败应能降级账号')
  assert.match(afterPrecheck?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '当前预检查失败应按传入真实错误摘要写入最近错误')
}

async function testPrecheckWaitsForInFlightConcurrencyBeforeMarking(): Promise<void> {
  const { account, group, gatewayAccount } = createGatewayAccount('预检查等待并发归零')
  const concurrencySlot = accountConcurrency.tryAcquireAccountConcurrency(account.id, 10)
  assert.equal(concurrencySlot.acquired, true, '测试应能先占用账号并发槽')

  await withDbServiceRole(() => gatewaySideEffects.completeGatewayAccountPrecheckForTest(gatewayAccount, undefined, {
    systemAccountId: 'sys_admin',
    groupId: group.id,
    apiKeyId: 'precheck-concurrency-key',
    clientIp: '10.20.30.40',
    endpoint: '/v1/responses',
    reason: '模拟探针失败但仍有存量请求'
  }))

  assertActiveAccount(account.id, '账号仍有在途并发时，事前确认失败不能立刻写入临时不可调用')
  const runtime = gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert.equal(runtime?.status, 'precheck_pending', '等待并发归零期间应保持事前确认运行态')
  assert.match(runtime?.reason ?? '', /等待 1 个在途请求结束/, '运行态原因应说明正在等待并发归零')

  await withDbServiceRole(async () => {
    concurrencySlot.release()
    await waitFor(() => repositories.findAccountSummary(account.id, adminAccess)?.status === 'temporary_unavailable', 1000)
  })

  const afterRelease = repositories.findAccountSummary(account.id, adminAccess)
  assert.equal(afterRelease?.status, 'temporary_unavailable', '账号并发归零后，探针失败状态应允许写入临时不可调用')
  assert.match(afterRelease?.lastErrorMessage ?? '', /模拟探针失败但仍有存量请求/, '并发归零后写库应保留探针失败原因')
}

function createGatewayAccount(name: string, errorHandlingRules: AccountErrorHandlingRule[] = []): {
  account: AccountSummary
  group: ReturnType<typeof repositories.createGroup>
  gatewayAccount: OpenAIAccountSecret
} {
  const group = repositories.createGroup({
    name: `${name}分组-${Math.random().toString(16).slice(2, 8)}`,
    providerCode: 'gpt'
  }, adminAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: `${name}-${Math.random().toString(16).slice(2, 8)}`,
    type: 'api_key',
    groupId: group.id,
    credentials: {
      api_key: `sk-${Math.random().toString(16).slice(2)}`,
      base_url: 'https://api.openai.com/v1',
      error_handling_rules: errorHandlingRules
    },
    status: 'active',
    schedulable: true
  }, adminAccess)
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取到测试网关账号对象')
  assert.equal(gatewayAccount.status, 'active', '测试网关账号初始应为正常状态')
  return { account, group, gatewayAccount }
}

function accountErrorRule(name: string, statusCodes: number[], action: AccountErrorHandlingRuleAction): AccountErrorHandlingRule {
  return {
    enabled: true,
    name,
    priority: 1,
    status_codes: statusCodes,
    action
  }
}

function assertActiveAccount(accountId: string, message: string): void {
  const account = repositories.findAccountSummary(accountId, adminAccess)
  assert.equal(account?.status, 'active', `${message}：实际状态 ${account?.status}`)
  assert.equal(account?.cooldownUntil, undefined, `${message}：实际冷却时间 ${account?.cooldownUntil}`)
}

function createRuntimeAccount(id: string): OpenAIAccountSecret {
  return {
    id,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: '事前确认运行态账号',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.5'],
    currentConcurrency: 0,
    baseUrl: 'https://api.openai.com/v1',
    apiKey: 'sk-precheck-runtime',
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-precheck-runtime',
      base_url: 'https://api.openai.com/v1'
    }
  }
}

function createRuntimeAuthorizedAccount(id: string): OpenAIAccountSecret {
  return {
    ...createRuntimeAccount(id),
    systemAccountId: 'sys_owner',
    accountOwnerSystemAccountId: 'sys_owner',
    groupOwnerSystemAccountId: 'sys_grantee',
    bindingSystemAccountId: 'sys_grantee',
    accountAccessType: 'account_authorized',
    boundGroupId: 'group-authorized',
    accountAuthorizationId: 'auth-account-a',
    name: '授权事前确认运行态账号'
  }
}

function buildRuntimeGatewayRequest(): Parameters<typeof accountPreparation.handleUnavailableProxyProfile>[0] {
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/responses',
    body: {
      model: 'gpt-5.5',
      input: 'hi'
    },
    headers: { 'content-type': 'application/json' },
    socket: { remoteAddress: '127.0.0.1' },
    ip: '127.0.0.1'
  } as Parameters<typeof accountPreparation.handleUnavailableProxyProfile>[0]
}

function runtimePreparationGroupAccess(): GroupUsageAccessMetadata {
  return {
    groupOwnerSystemAccountId: 'sys_admin',
    providerCode: 'gpt',
    groupAccessType: 'owner',
    groupType: 'personal'
  }
}

function buildRuntimeGatewayUsageContext(traceId: string): GatewayUsageContext {
  return {
    traceId,
    trafficSource: 'gateway',
    clientIp: '10.30.40.50',
    systemAccountId: 'sys_admin',
    apiKeyId: 'key-preparation-proxy',
    groupId: 'group-preparation-proxy',
    endpoint: 'POST /v1/responses',
    requestSnapshot: {
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      traceId,
      headers: {},
      body: { model: 'gpt-5.5', input: 'hi' }
    }
  }
}

async function withDbServiceRole<T>(action: () => Promise<T>): Promise<T> {
  const previousRole = runtimeConfig.processRole
  runtimeConfig.processRole = 'db-service'
  try {
    return await action()
  } finally {
    runtimeConfig.processRole = previousRole
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}

async function waitFor(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const startedAt = Date.now()
  while (Date.now() - startedAt <= timeoutMs) {
    if (predicate()) {
      return
    }
    await delay(20)
  }
  assert.fail(`等待条件超时 ${timeoutMs}ms`)
}
