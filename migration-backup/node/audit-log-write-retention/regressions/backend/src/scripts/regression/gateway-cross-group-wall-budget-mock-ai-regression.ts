import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  GatewayRequestWallBudget,
  defaultGatewayFinalResponseReserveMs,
  defaultGatewayRequestWallBudgetMs
} from '../../modules/gateway/routing/route-coordination.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  accountCircuitDispatchRevision,
  gatewayAccountProtocolModelScope
} from '../../modules/gateway/runtime/account-circuit.service.js'
import { accountCircuitSuspectConfirmationIntervalMs } from '../../modules/gateway/runtime/account-circuit-store.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot,
  waitForHighConcurrencyGroupCapacity
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import {
  clearSpeedFirstBodyAdmissionsForTest,
  speedFirstBodyAdmissionSnapshot
} from '../../modules/gateway/runtime/speed-first-body-admission.service.js'
import {
  clearSpeedFirstCutoverReservationsForTest,
  speedFirstCutoverBudgetSnapshot
} from '../../modules/gateway/runtime/speed-first-cutover-reservation.service.js'
import {
  clearAccountConcurrency,
  snapshotAccountConcurrency,
  tryAcquireAccountConcurrency,
  type AccountConcurrencySlot
} from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const model = 'gpt-5.5'
const clientIp = '198.51.100.214'
const capacityPolicy: GroupSchedulingPolicy = {
  defaultSoftConcurrency: 1,
  maxQueueWaitMs: 300_000,
  clientIpConcurrencyLimit: 16,
  clientIpConcurrencyOverflowMode: 'queue',
  imageLaneMaxConcurrency: 0
}
const mainKeys = {
  capacity: 'sk-cross-wall-capacity',
  multi1: 'sk-cross-wall-multi-1',
  multi2: 'sk-cross-wall-multi-2',
  backup: 'sk-cross-wall-backup',
  tail: 'sk-cross-wall-tail'
} as const
const confirmationKeys = {
  first: 'sk-cross-confirmation-1',
  sibling: 'sk-cross-confirmation-2',
  backup: 'sk-cross-confirmation-backup'
} as const
const postcommitKeys = {
  primary: 'sk-cross-postcommit-primary',
  backup: 'sk-cross-postcommit-backup'
} as const

const tempRoot = resolve(
  tmpdir(),
  `juhe-ai-gateway-cross-group-wall-${Date.now()}-${Math.random().toString(16).slice(2)}`
)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'gateway-cross-group-wall-budget-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.runtimeMode = 'standalone'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.runtimeStateDriver = 'memory'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  accountCircuit,
  hotQuality,
  apiKeyFailureGuard,
  clientIpAvoidance,
  clientIpErrorCircuit,
  proxyHealth,
  usageRecordQueue,
  auditLogQueue,
  readWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js'),
  import('../../modules/gateway/runtime/client-ip-error-circuit.service.js'),
  import('../../modules/gateway/runtime/proxy-health.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

interface UpstreamHit {
  requestId: string
  key: string
}

interface MainScenario {
  apiKey: string
  capacityGroupId: string
  capacityAccountId: string
  multiGroupId: string
  multiAccountId: string
  backupGroupId: string
  backupAccountId: string
  tailAccountId: string
  accountIds: string[]
}

interface ConfirmationScenario {
  apiKey: string
  primaryGroupId: string
  primaryAccountId: string
  backupGroupId: string
  backupAccountId: string
  accountIds: string[]
}

interface PostcommitScenario {
  apiKey: string
  primaryGroupId: string
  primaryAccountId: string
  backupGroupId: string
  backupAccountId: string
  accountIds: string[]
}

interface HttpResult {
  status: number
  text: string
}

interface PartialHttpResult {
  statusCode: number
  text: string
  terminated: boolean
}

interface ConfirmationProbe {
  store: ReturnType<typeof accountCircuit.getGatewayAccountCircuitStore>
  scope: ReturnType<typeof gatewayAccountProtocolModelScope>
}

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const originalDateNow = Date.now.bind(Date)
let fakeNowMs = originalDateNow()
let mainRequestAcceptedAtMs: number | undefined
let confirmationProbe: ConfirmationProbe | undefined
let confirmationLeaseObserved = false
const hits: UpstreamHit[] = []
const groupIdByAccountId = new Map<string, string>()
const failures: string[] = []
let upstreamServer: http.Server | undefined
let gatewayServer: http.Server | undefined

try {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: 0,
    noAvailableAccountWaitTimeoutSeconds: 300,
    textFirstResponseTimeoutSeconds: 30,
    textStreamIdleTimeoutSeconds: 30,
    textUncommittedAttemptMaxLifetimeSeconds: 300,
    accountCircuitConfirmationFailuresRequired: 2
  })
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  clearHighConcurrencyGroupQueues()
  clearSpeedFirstBodyAdmissionsForTest()
  clearSpeedFirstCutoverReservationsForTest()
  clearAccountConcurrency()

  upstreamServer = createMockUpstream()
  await listen(upstreamServer)
  const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`
  const mainScenario = createMainScenario(upstreamBaseUrl)
  const confirmationScenario = createConfirmationScenario(upstreamBaseUrl)
  const postcommitScenario = createPostcommitScenario(upstreamBaseUrl)

  gatewayServer = createGatewayServer()
  await listen(gatewayServer)
  const gatewayBaseUrl = `http://127.0.0.1:${serverAddress(gatewayServer).port}`
  await accountCircuit.ensureGatewayAccountCircuitRuntimeStateReady()
  Date.now = () => fakeNowMs

  const mainResult = await runMainCrossGroupRequest(gatewayBaseUrl, mainScenario)
  await recordCheck('跨组共享墙钟主链路', () => assertMainResult(mainResult))
  await recordCheck('跨组共享墙钟归因边界', () => assertMainAttribution(mainScenario))
  await recordCheck('跨组共享墙钟资源归零', () => assertRuntimeResourcesReleased(mainScenario.accountIds))

  const confirmationResult = await runSuspectMultiKeyConfirmation(gatewayBaseUrl, confirmationScenario)
  await recordCheck('SUSPECT confirmation 后同账户 sibling Key rotation', () => (
    assertConfirmationResult(confirmationScenario, confirmationResult)
  ))
  await recordCheck('SUSPECT confirmation 资源归零', () => (
    assertRuntimeResourcesReleased(confirmationScenario.accountIds)
  ))

  const postcommitResult = await runPostcommitRequest(gatewayBaseUrl, postcommitScenario)
  await recordCheck('已提交下游后禁止进入后备组', () => (
    assertPostcommitResult(postcommitResult)
  ))
  await recordCheck('已提交下游场景资源归零', () => (
    assertRuntimeResourcesReleased(postcommitScenario.accountIds)
  ))

  const summary = {
    message: failures.length === 0
      ? 'gateway cross-group wall budget mock ai regression passed'
      : 'gateway cross-group wall budget mock ai regression failed',
    mainHitOrder: hitKeys('cross-wall-main'),
    confirmationHitOrder: hitKeys('cross-wall-confirmation'),
    postcommitHitOrder: hitKeys('cross-wall-postcommit'),
    confirmationLeaseObserved,
    mainStatus: mainResult.response.status,
    mainWallRemainingMs: mainResult.wallRemainingMs,
    fifoQueueSizes: mainResult.fifoQueueSizes,
    postcommitTerminated: postcommitResult.terminated,
    failures
  }
  console.log(JSON.stringify(summary))
  assert.deepEqual(failures, [], failures.join('\n'))
} finally {
  Date.now = originalDateNow
  await closeServer(gatewayServer)
  await closeServer(upstreamServer)
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  usageRecordQueue.flushAllUsageRecordQueue()
  auditLogQueue.flushAllAuditLogQueue()
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
  clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
  proxyHealth.clearGatewayProxyHealthForTest()
  clearHighConcurrencyGroupQueues()
  clearSpeedFirstBodyAdmissionsForTest()
  clearSpeedFirstCutoverReservationsForTest()
  clearAccountConcurrency()
  hotQuality.resetGatewayHotQualityRuntimeForTest()
  accountCircuit.resetGatewayAccountCircuitStoreForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  await readWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.getBusinessDatabase().close()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runMainCrossGroupRequest(
  baseUrl: string,
  scenario: MainScenario
): Promise<{ response: HttpResult; wallRemainingMs: number; fifoQueueSizes: number[] }> {
  const heldSlot = tryAcquireAccountConcurrency(scenario.capacityAccountId, 1, { lane: 'text' })
  assert.equal(heldSlot.acquired, true, 'FIFO 测试必须先占满容量账户')
  const aheadController = new AbortController()
  const behindController = new AbortController()
  let aheadSlot: AccountConcurrencySlot | undefined
  const wakeOrder: string[] = []
  const fifoQueueSizes: number[] = []
  let behindWait: ReturnType<typeof waitForHighConcurrencyGroupCapacity> | undefined
  const aheadWait = waitForHighConcurrencyGroupCapacity({
    systemAccountId: access.systemAccountId,
    groupId: scenario.capacityGroupId,
    apiKeyId: 'cross-wall-fifo-ahead',
    accountIds: [scenario.capacityAccountId],
    accountConcurrencyLimits: { [scenario.capacityAccountId]: 1 },
    lane: 'text',
    policy: capacityPolicy,
    maxWaitMs: 300_000,
    signal: aheadController.signal
  }).then((result) => {
    wakeOrder.push('ahead')
    return result
  })
  try {
    await waitUntil(() => totalHighConcurrencyQueueSize() === 1, 2_000)
    fifoQueueSizes.push(totalHighConcurrencyQueueSize())
    assert.equal(fifoQueueSizes.at(-1), 1, '受控头部 waiter 必须先进入 FIFO')

    behindWait = waitForHighConcurrencyGroupCapacity({
      systemAccountId: access.systemAccountId,
      groupId: scenario.capacityGroupId,
      apiKeyId: 'cross-wall-fifo-behind',
      accountIds: [scenario.capacityAccountId],
      accountConcurrencyLimits: { [scenario.capacityAccountId]: 1 },
      lane: 'text',
      policy: capacityPolicy,
      maxWaitMs: 300_000,
      signal: behindController.signal
    }).then((result) => {
      wakeOrder.push('behind')
      return result
    })
    await waitUntil(() => totalHighConcurrencyQueueSize() === 2, 2_000)
    fifoQueueSizes.push(totalHighConcurrencyQueueSize())
    assert.equal(fifoQueueSizes.at(-1), 2, '两个受控 waiter 必须依次进入同一 FIFO')

    heldSlot.release()
    const aheadResult = await aheadWait
    assert.equal(aheadResult.ready, true, '释放首槽后只能先唤醒 FIFO 头部 waiter')
    assert.deepEqual(wakeOrder, ['ahead'], 'FIFO 尾部 waiter 不得越过头部')
    aheadSlot = tryAcquireAccountConcurrency(scenario.capacityAccountId, 1, { lane: 'text' })
    assert.equal(aheadSlot.acquired, true, 'FIFO 头部 waiter 唤醒后必须取得受控槽')
    fifoQueueSizes.push(totalHighConcurrencyQueueSize())
    assert.equal(fifoQueueSizes.at(-1), 1, 'FIFO 头部出队后尾部 waiter 必须仍在队列中')

    aheadSlot.release()
    aheadSlot = undefined
    const behindResult = await behindWait
    assert.equal(behindResult.ready, true, '第二次释放后 FIFO 尾部 waiter 必须获准继续')
    assert.deepEqual(wakeOrder, ['ahead', 'behind'], '容量队列必须保持严格 FIFO 唤醒顺序')
    fifoQueueSizes.push(totalHighConcurrencyQueueSize())

    mainRequestAcceptedAtMs = fakeNowMs
    const response = await postChat(baseUrl, scenario.apiKey, 'cross-wall-main', false)
    const wall = new GatewayRequestWallBudget({
      requestAcceptedAtMs: mainRequestAcceptedAtMs,
      budgetMs: defaultGatewayRequestWallBudgetMs,
      now: () => fakeNowMs
    })
    return { response, wallRemainingMs: wall.remainingMs(), fifoQueueSizes }
  } finally {
    heldSlot.release()
    aheadSlot?.release()
    aheadController.abort('cross-wall-fifo-cleanup')
    behindController.abort('cross-wall-fifo-cleanup')
    await aheadWait.catch(() => undefined)
    await behindWait?.catch(() => undefined)
    mainRequestAcceptedAtMs = undefined
  }
}

function assertMainResult(input: {
  response: HttpResult
  wallRemainingMs: number
  fifoQueueSizes: number[]
}): void {
  assert.equal(input.response.status, 503, `final reserve 不足后必须返回网关 503：${input.response.text}`)
  assert.match(input.response.text, /upstream_retryable_error|网关请求处理时间已到/u)
  assert.doesNotMatch(input.response.text, /cross-wall-upstream-private|cross-wall-tail-success/u)
  assert.deepEqual(
    hitKeys('cross-wall-main'),
    [mainKeys.capacity, mainKeys.multi1, mainKeys.multi2, mainKeys.backup],
    '一个客户端请求必须按容量组、多 Key、后备组顺序命中，尾号不得派发'
  )
  assert.equal(hitKeys('cross-wall-main').includes(mainKeys.tail), false, 'final reserve 不足后尾部候选必须零派发')
  assert.deepEqual(input.fifoQueueSizes, [1, 2, 1, 0], '容量队列必须按 FIFO 依次收口')
  assert.equal(input.wallRemainingMs, defaultGatewayFinalResponseReserveMs - 500)
  const wall = new GatewayRequestWallBudget({
    requestAcceptedAtMs: fakeNowMs - (defaultGatewayRequestWallBudgetMs - input.wallRemainingMs),
    budgetMs: defaultGatewayRequestWallBudgetMs,
    now: () => fakeNowMs
  })
  assert.equal(wall.handoffRequired({ finalResponseReserveMs: defaultGatewayFinalResponseReserveMs }), true)
}

async function assertMainAttribution(scenario: MainScenario): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  const metadata = await auditMetadataForTrace('trace-cross-wall-main')
  const attemptBlocked = metadata.find((item) => item.label === 'gateway_upstream_attempt_blocked_wall_budget')
  const clientHandoff = metadata.find((item) => item.label === 'gateway_request_client_handoff')
  assert.equal(attemptBlocked?.metadata.reason, 'gateway_request_wall_budget_exhausted')
  assert.equal(clientHandoff?.metadata.reason, 'gateway_request_wall_budget_exhausted')

  for (const accountId of scenario.accountIds) {
    const summary = repositories.findAccountForTest(accountId, access)
    assert.equal(summary?.status, 'active', `${accountId} 墙钟不得写死账户`)
    assert.equal(summary?.schedulable, true, `${accountId} 墙钟不得取消调度`)
    assert.equal(summary?.cooldownUntil, undefined, `${accountId} 墙钟不得写冷却`)
    assert.equal(summary?.lastErrorMessage, undefined, `${accountId} 墙钟不得持久化上游错误`)
    assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, `${accountId} 墙钟不得写 Key 临时不可用`)
    assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, `${accountId} 墙钟不得写 Key 池全不可用`)
    assert.equal(accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[accountId], undefined)
  }
  assert.equal(
    apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()
      .filter((entry) => scenario.accountIds.includes(entry.accountId)).length,
    0,
    '墙钟及不可信 HTTP 响应不得残留 Key failure guard'
  )
  assert.deepEqual(clientIpAvoidance.getClientIpAccountAvoidanceSnapshotForTest(), [], '墙钟不得写 IP×账户避让')
  assert.deepEqual(clientIpErrorCircuit.getGatewayClientIpSecuritySnapshotForTest(), {
    preAuth: [],
    clientIpErrors: []
  }, '墙钟不得写客户端 IP 错误电路')

  const dispatchAccounts = [
    requireDispatchAccount(scenario.capacityGroupId, scenario.capacityAccountId),
    requireDispatchAccount(scenario.multiGroupId, scenario.multiAccountId),
    requireDispatchAccount(scenario.backupGroupId, scenario.backupAccountId),
    requireDispatchAccount(scenario.backupGroupId, scenario.tailAccountId)
  ]
  const proxyOrder = proxyHealth.orderOpenAIAccountsByGatewayProxyHealth(dispatchAccounts)
  assert.equal(proxyOrder.applied, false, '墙钟不得污染 proxy/upstream bucket 排序')
  assert.deepEqual(proxyOrder.avoidedAccountIds, [], '墙钟不得产生 proxy/upstream bucket 避让')
  for (const account of dispatchAccounts.slice(0, 3)) {
    const quality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get({
      accountRuntimeKey: gatewayAccountRuntimeKey(account),
      protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
      requestLane: 'text',
      modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
    })
    assert(quality, `${account.id} 必须留下中性诊断 attempt`)
    assert.equal(quality.window5m.qualityAttempts, 0, `${account.id} 墙钟/普通失败不得进入共享质量分母`)
    assert.equal(quality.window5m.localTransportFailures, 0, `${account.id} 未经独立 confirmation 不得写共享 transport 失败`)
  }
}

async function runSuspectMultiKeyConfirmation(
  baseUrl: string,
  scenario: ConfirmationScenario
): Promise<HttpResult> {
  fakeNowMs = originalDateNow()
  const account = requireDispatchAccount(scenario.primaryGroupId, scenario.primaryAccountId)
  const scope = gatewayAccountProtocolModelScope(account, 'text', model)
  const store = accountCircuit.getGatewayAccountCircuitStore()
  const seeded = await store.suspect({
    scope,
    dispatchRevision: accountCircuitDispatchRevision(account),
    transitionId: 'cross-wall-confirmation-seed',
    reason: 'independent seeded transport fact',
    confirmationFailuresRequired: 2,
    failureEvidenceKey: '1'.repeat(64),
    nowMs: fakeNowMs - accountCircuitSuspectConfirmationIntervalMs - 1
  })
  assert.equal(seeded.status, 'applied', 'confirmation 子场景必须先建立 SUSPECT')
  confirmationLeaseObserved = false
  confirmationProbe = { store, scope }
  try {
    return await postChat(baseUrl, scenario.apiKey, 'cross-wall-confirmation', false)
  } finally {
    confirmationProbe = undefined
  }
}

async function assertConfirmationResult(
  scenario: ConfirmationScenario,
  response: HttpResult
): Promise<void> {
  assert.equal(confirmationLeaseObserved, true, 'SUSPECT 请求必须在真实上游派发前取得 confirmation lease')
  assert.equal(response.status, 200, response.text)
  assert.match(response.text, /cross-confirmation-sibling-success/u)
  assert.deepEqual(
    hitKeys('cross-wall-confirmation'),
    [confirmationKeys.first, confirmationKeys.sibling],
    'confirmation 第一 Key transport 失败后必须允许同账户 sibling Key 完成有界 rotation'
  )
  assert.equal(
    hitKeys('cross-wall-confirmation').includes(confirmationKeys.backup),
    false,
    '同账户 sibling Key 成功后不得进入后备组'
  )
  const account = requireDispatchAccount(scenario.primaryGroupId, scenario.primaryAccountId)
  const state = await accountCircuit.getGatewayAccountCircuitStore().get(
    gatewayAccountProtocolModelScope(account, 'text', model),
    fakeNowMs
  )
  assert.equal(state.phase, 'CLOSED', 'sibling Key framing 必须关闭本请求确认失败留下的 SUSPECT')
  assert.equal(state.lease, undefined, 'confirmation lease 必须归零')
}

async function runPostcommitRequest(
  baseUrl: string,
  scenario: PostcommitScenario
): Promise<PartialHttpResult> {
  const body = JSON.stringify({
    model,
    messages: [{ role: 'user', content: 'committed downstream must not enter backup group' }],
    stream: true
  })
  return await rawHttpPost(`${baseUrl}/v1/chat/completions?cross_request_id=cross-wall-postcommit`, body, {
    authorization: `Bearer ${scenario.apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
    'x-forwarded-for': clientIp,
    'x-session-id': 'cross-wall-postcommit-session',
    'x-trace-id': 'trace-cross-wall-postcommit'
  })
}

function assertPostcommitResult(result: PartialHttpResult): void {
  assert.equal(result.statusCode, 200, '已提交场景必须先向客户端提交上游 200')
  assert.equal(result.terminated, true, '上游截断后必须终止下游，不能伪造完整成功')
  assert.match(result.text, /cross-postcommit-partial/u)
  assert.doesNotMatch(result.text, /cross-postcommit-backup-must-not-run|upstream_retryable_error|service_unavailable/u)
  assert.deepEqual(
    hitKeys('cross-wall-postcommit'),
    [postcommitKeys.primary],
    '已下游提交后不得重放、切号或进入后备组'
  )
}

async function assertRuntimeResourcesReleased(accountIds: string[]): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  await waitUntil(() => Object.keys(snapshotAccountConcurrency()).length === 0, 2_000)
  await waitUntil(() => highConcurrencyGroupQueueSnapshot().length === 0, 2_000)
  assert.deepEqual(snapshotAccountConcurrency(), {}, 'account slots 必须全部归零')
  assert.deepEqual(highConcurrencyGroupQueueSnapshot(), [], 'FIFO waiter、timer 与账户索引必须全部归零')
  assert.deepEqual(
    accountSideEffects.recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    '恢复等待 scope、waiter 与 timer 必须全部归零'
  )
  assert.deepEqual(speedFirstCutoverBudgetSnapshot(), [], 'speed-first cutover reservation 必须归零')
  assert.deepEqual(speedFirstBodyAdmissionSnapshot(), [], 'speed-first body admission reservation 必须归零')
  const sideEffects = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(sideEffects.queueLength, 0, '账户副作用队列必须归零')
  assert.equal(sideEffects.processing, false, '账户副作用 drain 必须停止')
  assert.equal(sideEffects.precheckPendingAccountCount, 0, 'precheck 任务必须归零')
  assert.equal(sideEffects.recoveryProbePendingAccountCount, 0, 'recovery probe 任务必须归零')
  assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, 'precheck lease 必须归零')

  const store = accountCircuit.getGatewayAccountCircuitStore()
  for (const accountId of accountIds) {
    const account = findDispatchAccount(accountId)
    const protocolState = await store.get(gatewayAccountProtocolModelScope(account, 'text', model), fakeNowMs)
    const parentState = await store.get({
      kind: 'account',
      accountRuntimeKey: gatewayAccountRuntimeKey(account)
    }, fakeNowMs)
    assert.equal(protocolState.lease, undefined, `${accountId} protocol/model confirmation lease 必须归零`)
    assert.equal(parentState.lease, undefined, `${accountId} parent account lease 必须归零`)
  }
}

function createMainScenario(upstreamBaseUrl: string): MainScenario {
  const capacityGroup = createGroup('跨组墙钟容量 FIFO 组', 'high_concurrency', capacityPolicy)
  const multiGroup = createGroup('跨组墙钟多 Key 组')
  const backupGroup = createGroup('跨组墙钟后备组')
  const capacity = createAccount(capacityGroup.id, '跨组墙钟容量账户', mainKeys.capacity, upstreamBaseUrl, {
    concurrencyLimit: 1
  })
  const multi = createAccount(multiGroup.id, '跨组墙钟多 Key 账户', mainKeys.multi1, upstreamBaseUrl, {
    apiKeys: [mainKeys.multi1, mainKeys.multi2],
    priority: 0
  })
  const backup = createAccount(backupGroup.id, '跨组墙钟后备账户', mainKeys.backup, upstreamBaseUrl, {
    priority: 0
  })
  const tail = createAccount(backupGroup.id, '跨组墙钟尾部账户', mainKeys.tail, upstreamBaseUrl, {
    priority: 1,
    fallbackEnabled: true
  })
  const apiKey = createRouteKey('跨组墙钟网关 Key', [capacityGroup.id, multiGroup.id, backupGroup.id])
  return {
    apiKey,
    capacityGroupId: capacityGroup.id,
    capacityAccountId: capacity.id,
    multiGroupId: multiGroup.id,
    multiAccountId: multi.id,
    backupGroupId: backupGroup.id,
    backupAccountId: backup.id,
    tailAccountId: tail.id,
    accountIds: [capacity.id, multi.id, backup.id, tail.id]
  }
}

function createConfirmationScenario(upstreamBaseUrl: string): ConfirmationScenario {
  const primaryGroup = createGroup('跨组墙钟 SUSPECT confirmation 组')
  const backupGroup = createGroup('跨组墙钟 confirmation 后备组')
  const primary = createAccount(
    primaryGroup.id,
    '跨组墙钟 SUSPECT 多 Key 账户',
    confirmationKeys.first,
    upstreamBaseUrl,
    { apiKeys: [confirmationKeys.first, confirmationKeys.sibling] }
  )
  const backup = createAccount(
    backupGroup.id,
    '跨组墙钟 confirmation 后备账户',
    confirmationKeys.backup,
    upstreamBaseUrl
  )
  return {
    apiKey: createRouteKey('跨组墙钟 confirmation 网关 Key', [primaryGroup.id, backupGroup.id]),
    primaryGroupId: primaryGroup.id,
    primaryAccountId: primary.id,
    backupGroupId: backupGroup.id,
    backupAccountId: backup.id,
    accountIds: [primary.id, backup.id]
  }
}

function createPostcommitScenario(upstreamBaseUrl: string): PostcommitScenario {
  const primaryGroup = createGroup('跨组墙钟已提交主组')
  const backupGroup = createGroup('跨组墙钟已提交后备组')
  const primary = createAccount(
    primaryGroup.id,
    '跨组墙钟已提交主账户',
    postcommitKeys.primary,
    upstreamBaseUrl
  )
  const backup = createAccount(
    backupGroup.id,
    '跨组墙钟已提交后备账户',
    postcommitKeys.backup,
    upstreamBaseUrl
  )
  return {
    apiKey: createRouteKey('跨组墙钟已提交网关 Key', [primaryGroup.id, backupGroup.id]),
    primaryGroupId: primaryGroup.id,
    primaryAccountId: primary.id,
    backupGroupId: backupGroup.id,
    backupAccountId: backup.id,
    accountIds: [primary.id, backup.id]
  }
}

function createGroup(name: string, groupType?: 'high_concurrency', schedulingPolicy?: GroupSchedulingPolicy) {
  return repositories.createGroup({
    name,
    providerCode: GPT_VENDOR_CODE,
    enabled: true,
    ...(groupType ? { groupType } : {}),
    ...(schedulingPolicy ? { schedulingPolicy } : {})
  }, access)
}

function createAccount(
  groupId: string,
  name: string,
  apiKey: string,
  upstreamBaseUrl: string,
  options: {
    apiKeys?: string[]
    concurrencyLimit?: number
    priority?: number
    fallbackEnabled?: boolean
  } = {}
) {
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      ...(options.apiKeys ? { api_keys: options.apiKeys, api_key_strategy: 'round_robin' } : {}),
      base_url: upstreamBaseUrl
    },
    groupId,
    status: 'active',
    schedulable: true,
    concurrencyLimit: options.concurrencyLimit ?? 4,
    priority: options.priority ?? 0,
    fallbackEnabled: options.fallbackEnabled ?? false,
    supportedModels: [model]
  }, access)
  groupIdByAccountId.set(account.id, groupId)
  activate(account.id)
  return account
}

function createRouteKey(name: string, groupIds: string[]): string {
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name,
    groupBindings: groupIds.map((groupId, index) => ({
      groupId,
      priority: index + 1,
      status: 'active'
    })),
    status: 'active'
  }, access)
  assert(apiKey.key)
  gatewayCache.clearGatewayRuntimeCache()
  return apiKey.key
}

function activate(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `激活 Mock 账户失败：${accountId}`)
}

function createGatewayServer(): http.Server {
  const app = express()
  app.set('trust proxy', 1)
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
  return http.createServer(app)
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const requestId = url.searchParams.get('cross_request_id') ?? 'missing-cross-request-id'
    const key = bearerKey(req.headers.authorization)
    hits.push({ requestId, key })
    req.resume()
    req.once('end', () => respondToMockRequest(requestId, key, res))
  })
}

function respondToMockRequest(requestId: string, key: string, res: http.ServerResponse): void {
  if (key === mainKeys.capacity) {
    fakeNowMs += 40_000
    res.destroy(new Error('cross-wall-upstream-private-capacity-reset'))
    return
  }
  if (key === mainKeys.multi1 || key === mainKeys.multi2) {
    fakeNowMs += 40_000
    const status = key === mainKeys.multi1 ? 401 : 500
    res.writeHead(status, { 'content-type': 'application/json; charset=utf-8' })
    res.end(JSON.stringify({ error: { message: `cross-wall-upstream-private-${key}`, code: 'contradictory' } }))
    return
  }
  if (key === mainKeys.backup) {
    assert(mainRequestAcceptedAtMs !== undefined, '后备账户命中时必须仍持有同一请求 acceptedAt')
    fakeNowMs = Math.max(
      fakeNowMs,
      mainRequestAcceptedAtMs + defaultGatewayRequestWallBudgetMs - defaultGatewayFinalResponseReserveMs + 500
    )
    res.destroy(new Error('cross-wall-upstream-private-backup-reset'))
    return
  }
  if (key === mainKeys.tail) {
    sendSuccess(res, 'cross-wall-tail-success')
    return
  }
  if (key === confirmationKeys.first) {
    const probe = confirmationProbe
    if (!probe) {
      res.destroy(new Error('missing confirmation probe'))
      return
    }
    void probe.store.get(probe.scope, fakeNowMs).then((state) => {
      confirmationLeaseObserved = state.lease?.kind === 'confirmation'
      fakeNowMs += 1_000
      res.destroy(new Error('cross-confirmation-first-key-reset'))
    }, () => res.destroy(new Error('confirmation probe failed')))
    return
  }
  if (key === confirmationKeys.sibling) {
    sendSuccess(res, 'cross-confirmation-sibling-success')
    return
  }
  if (key === confirmationKeys.backup) {
    sendSuccess(res, 'cross-confirmation-backup-success')
    return
  }
  if (key === postcommitKeys.primary) {
    const event = `data: ${JSON.stringify({
      id: 'chatcmpl_cross_postcommit',
      object: 'chat.completion.chunk',
      choices: [{ index: 0, delta: { content: 'cross-postcommit-partial' }, finish_reason: null }]
    })}\n\n`
    res.writeHead(200, {
      'content-type': 'text/event-stream; charset=utf-8',
      'content-length': String(Buffer.byteLength(event) + 4096),
      connection: 'close'
    })
    res.flushHeaders()
    res.write(event)
    setTimeout(() => res.destroy(), 20).unref()
    return
  }
  if (key === postcommitKeys.backup) {
    sendSuccess(res, 'cross-postcommit-backup-must-not-run')
    return
  }
  res.destroy(new Error(`unexpected mock key for ${requestId}: ${key}`))
}

function sendSuccess(res: http.ServerResponse, content: string): void {
  if (res.destroyed || res.writableEnded) return
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl_${content}`,
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }))
}

async function postChat(
  baseUrl: string,
  apiKey: string,
  requestId: string,
  stream: boolean,
  signal?: AbortSignal
): Promise<HttpResult> {
  const response = await fetch(`${baseUrl}/v1/chat/completions?cross_request_id=${encodeURIComponent(requestId)}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': clientIp,
      'x-session-id': `${requestId}-session`,
      'x-trace-id': `trace-${requestId}`
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content: `request ${requestId}` }],
      stream
    }),
    signal
  })
  return { status: response.status, text: await response.text() }
}

function rawHttpPost(url: string, body: string, headers: Record<string, string>): Promise<PartialHttpResult> {
  return new Promise((resolvePromise, rejectPromise) => {
    let responseStarted = false
    let settled = false
    const timer = setTimeout(() => finishError(new Error(`等待已提交断流响应超时：${url}`)), 5_000)
    timer.unref()
    const request = http.request(url, {
      method: 'POST',
      headers: { ...headers, 'content-length': String(Buffer.byteLength(body)) }
    })
    const finishError = (error: Error) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      request.destroy()
      rejectPromise(error)
    }
    request.once('error', (error) => {
      if (!responseStarted) finishError(error)
    })
    request.once('response', (response) => {
      responseStarted = true
      const chunks: Buffer[] = []
      let ended = false
      const finish = (terminated: boolean) => {
        if (settled) return
        settled = true
        clearTimeout(timer)
        resolvePromise({
          statusCode: response.statusCode ?? 0,
          text: Buffer.concat(chunks).toString('utf8'),
          terminated
        })
      }
      response.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
      response.once('end', () => {
        ended = true
        finish(false)
      })
      response.once('aborted', () => finish(true))
      response.once('error', () => finish(true))
      response.once('close', () => {
        if (!ended) finish(true)
      })
    })
    request.end(body)
  })
}

async function auditMetadataForTrace(traceId: string): Promise<Array<{
  label?: string
  metadata: Record<string, unknown>
}>> {
  auditLogQueue.flushAllAuditLogQueue()
  const list = repositories.listAuditLogs({ traceId, pageSize: 10 })
  assert.equal(list.total, 1, `trace ${traceId} 必须只有一条根审计记录`)
  const detail = repositories.getAuditLogDetail(list.items[0]?.id ?? '')
  assert(detail, `trace ${traceId} 缺少审计详情`)
  const metadata: Array<{ label?: string; metadata: Record<string, unknown> }> = []
  for (const payload of detail.payloads.filter((item) => item.partType === 'gateway_metadata' && item.hasBody)) {
    const body = await repositories.getAuditLogPayload(detail.id, payload.id, { limit: 1024 * 1024 })
    const parsed = JSON.parse(body?.bodyText ?? '{}') as { label?: string; metadata?: Record<string, unknown> }
    metadata.push({ label: parsed.label, metadata: parsed.metadata ?? {} })
  }
  return metadata
}

function requireDispatchAccount(groupId: string, accountId: string) {
  const account = repositories.listOpenAIAccountsForGroup(groupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === accountId)
  assert(account, `找不到调度账户 ${accountId}`)
  return account
}

function findDispatchAccount(accountId: string) {
  const groupId = groupIdByAccountId.get(accountId)
  assert(groupId, `找不到账户 ${accountId} 的测试分组`)
  const account = repositories.findOpenAIAccountForGroup(
    groupId,
    accountId,
    access.systemAccountId,
    { ignoreAvailability: true }
  )
  assert(account, `找不到运行账户 ${accountId}`)
  return account
}

function bearerKey(value: string | undefined): string {
  return String(value ?? '').replace(/^Bearer\s+/i, '')
}

function hitKeys(requestId: string): string[] {
  return hits.filter((hit) => hit.requestId === requestId).map((hit) => hit.key)
}

function totalHighConcurrencyQueueSize(): number {
  return highConcurrencyGroupQueueSnapshot().reduce((total, item) => total + item.queueSize, 0)
}

async function recordCheck(label: string, check: () => void | Promise<void>): Promise<void> {
  try {
    await check()
  } catch (error) {
    failures.push(`${label}: ${error instanceof Error ? error.message : String(error)}`)
  }
}

async function waitUntil(predicate: () => boolean, timeoutMs: number): Promise<void> {
  const deadlineAtMs = originalDateNow() + timeoutMs
  while (!predicate() && originalDateNow() < deadlineAtMs) {
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 10))
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1')
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address !== 'string')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
