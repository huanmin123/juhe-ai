import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { AccountErrorHandlingRuleAction } from '../../modules/accounts/account-error-policy-validation.js'
import { automaticUpstreamReplayAllowedAfterDispatch } from '../../modules/gateway/protocols/openai-v1/request-lane.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  GatewayRequestAttemptTracker,
  gatewayAttemptProtocolModelKey
} from '../../modules/gateway/routing/route-coordination.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot
} from '../../modules/gateway/runtime/high-concurrency-queue.service.js'
import {
  clearAccountConcurrency,
  snapshotAccountConcurrency
} from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { requireUsageRecordDetails } from '../shared/usage-record-detail.js'

type UpstreamBehavior =
  | 'reset_once_then_success'
  | 'hang_once_then_success'
  | 'always_hang'
  | 'always_reset'
  | 'advance_wall_and_reset'
  | 'complete_471'
  | 'success'

interface UpstreamHit {
  key: string
  ordinalForKey: number
}

interface AccountFixture {
  accountId: string
  groupId: string
  keys: string[]
}

interface GatewayFixture {
  apiKey: string
  routeStrategyId: string
  accounts: AccountFixture[]
}

interface HttpResult {
  status: number
  text: string
}

const model = 'gpt-5.5'
const tempRoot = resolve(tmpdir(), `juhe-ai-same-account-retry-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'same-account-retry-http-regression-secret'
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
  readWorkerPool,
  repositories,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  accountCircuit,
  hotQuality,
  apiKeyFailureGuard,
  latencyDegradation,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/repositories.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-circuit.service.js'),
  import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const hits: UpstreamHit[] = []
const behaviorByKey = new Map<string, UpstreamBehavior>()
let advanceWallClockForTest: (() => void) | undefined
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

async function main(): Promise<void> {
  let upstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  const contractFailures: string[] = []
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
    clearAccountConcurrency()
    clearHighConcurrencyGroupQueues()
    accountCircuit.resetGatewayAccountCircuitStoreForTest()
    hotQuality.resetGatewayHotQualityRuntimeForTest()
    apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()

    assertReplaySafetyGate()
    assertRetryModeIsolation()

    upstreamServer = createMockUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const sameRetry = createGatewayFixture(upstreamBaseUrl, '同账户原地重试', [
      { keys: ['sk-same-retry'], priority: 0 },
      { keys: ['sk-same-retry-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-same-retry', 'reset_once_then_success')
    behaviorByKey.set('sk-same-retry-backup', 'success')

    const attemptsZero = createGatewayFixture(upstreamBaseUrl, '关闭原地重试', [
      { keys: ['sk-zero-primary'], priority: 0 },
      { keys: ['sk-zero-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-zero-primary', 'always_reset')
    behaviorByKey.set('sk-zero-backup', 'success')

    const completeHttp = createGatewayFixture(upstreamBaseUrl, '完整HTTP不原地重试', [
      {
        keys: ['sk-http-primary'],
        priority: 0,
        policyAction: 'retry_next',
        policyMarker: 'explicit-http-retry-next'
      },
      { keys: ['sk-http-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-http-primary', 'complete_471')
    behaviorByKey.set('sk-http-backup', 'success')

    const multiKey = createGatewayFixture(upstreamBaseUrl, '兄弟Key先轮换', [{
      keys: ['sk-multi-bad', 'sk-multi-good'],
      priority: 0
    }])
    behaviorByKey.set('sk-multi-bad', 'always_reset')
    behaviorByKey.set('sk-multi-good', 'success')

    const sharedBudget = createGatewayFixture(upstreamBaseUrl, '请求级共享重试预算', [
      { keys: ['sk-shared-a'], priority: 0 },
      { keys: ['sk-shared-b'], priority: 0, fallbackEnabled: true },
      { keys: ['sk-shared-c'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-shared-a', 'always_reset')
    behaviorByKey.set('sk-shared-b', 'always_reset')
    behaviorByKey.set('sk-shared-c', 'success')

    const abortWait = createGatewayFixture(upstreamBaseUrl, '等待期间客户端取消', [
      { keys: ['sk-abort-wait'], priority: 0 },
      { keys: ['sk-abort-wait-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-abort-wait', 'always_reset')
    behaviorByKey.set('sk-abort-wait-backup', 'success')

    const hardTimeout = createGatewayFixture(upstreamBaseUrl, 'hard request timeout 原地重试', [
      { keys: ['sk-hard-timeout'], priority: 0 },
      { keys: ['sk-hard-timeout-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-hard-timeout', 'hang_once_then_success')
    behaviorByKey.set('sk-hard-timeout-backup', 'success')

    const configuredFirstByte = createGatewayFixture(upstreamBaseUrl, '配置首字截止仅切号', [
      { keys: ['sk-configured-deadline'], priority: 0 },
      { keys: ['sk-configured-deadline-backup'], priority: 10, fallbackEnabled: true }
    ], {
      singleGroup: true,
      normalRoutingConfig: {
        schedulingPreference: 'speed_first',
        firstByteDeadlineMs: 10_000,
        speedFirstConfig: {
          slowTriggerCount: 2,
          slowWindowSeconds: 300,
          recoverySuccessCount: 3,
          probeIntervalSeconds: 10,
          degradedTtlSeconds: 60,
          maxFirstByteRetriesPerRequest: 1
        }
      }
    })
    behaviorByKey.set('sk-configured-deadline', 'always_hang')
    behaviorByKey.set('sk-configured-deadline-backup', 'success')

    const wallInsufficient = createGatewayFixture(upstreamBaseUrl, '墙钟不足不启动原地重试', [
      { keys: ['sk-wall-insufficient'], priority: 0 },
      { keys: ['sk-wall-insufficient-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-wall-insufficient', 'advance_wall_and_reset')
    behaviorByKey.set('sk-wall-insufficient-backup', 'success')

    const intermediateNeutral = createGatewayFixture(upstreamBaseUrl, '中间失败保持请求局部', [
      { keys: ['sk-intermediate-neutral'], priority: 0 },
      { keys: ['sk-intermediate-neutral-backup'], priority: 0, fallbackEnabled: true }
    ])
    behaviorByKey.set('sk-intermediate-neutral', 'reset_once_then_success')
    behaviorByKey.set('sk-intermediate-neutral-backup', 'success')

    const allFailingMultiKeys = Array.from({ length: 3 }, (_, index) => `sk-unique-${String(index + 1).padStart(2, '0')}`)
    const uniqueKeySafetyCap = createGatewayFixture(upstreamBaseUrl, '多 Key 与原地重试计数隔离', [
      { keys: allFailingMultiKeys, priority: 0 },
      { keys: ['sk-unique-backup'], priority: 0, fallbackEnabled: true }
    ])
    for (const key of allFailingMultiKeys) behaviorByKey.set(key, 'always_reset')
    behaviorByKey.set('sk-unique-backup', 'success')

    gatewayServer = http.createServer(app)
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    await verifyContract(contractFailures, '安全 chat 的 pre-header reset 必须切换后备账户', async () => {
      configureRetry(1, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, sameRetry.apiKey, 'same account retry once')
      assert.equal(response.status, 200, `账户切换应由后备账户成功：${response.status} ${response.text}`)
      assert.match(response.text, /mock success from sk-same-retry-backup/)
      assert.deepEqual(hitKeys(offset), ['sk-same-retry', 'sk-same-retry-backup'], 'pre-header transport 失败不得同账号重放，必须切换后备账户')
    })

    await verifyContract(contractFailures, 'attempts=0 应直接切后备账户', async () => {
      configureRetry(0, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, attemptsZero.apiKey, 'retry disabled must fail over')
      assert.equal(response.status, 200, `关闭原地重试后应切换后备账户成功：${response.status} ${response.text}`)
      assert.deepEqual(hitKeys(offset), ['sk-zero-primary', 'sk-zero-backup'], 'attempts=0 不得重复首账户，必须直接切后备账户')
    })

    await verifyContract(contractFailures, '完整 HTTP 非 2xx 绝不原地重试', async () => {
      configureRetry(3, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, completeHttp.apiKey, 'complete HTTP failure must not retry in place')
      assert.equal(response.status, 200, `显式 retry_next 应切换后备账户成功：${response.status} ${response.text}`)
      assert.deepEqual(hitKeys(offset), ['sk-http-primary', 'sk-http-backup'], '完整 HTTP frame 只能执行显式切号，绝不能重复首 Key')
    })

    await verifyContract(contractFailures, '无显式策略时同账户多 Key 不得隐式轮换', async () => {
      configureRetry(3, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, multiKey.apiKey, 'sibling key before same-key retry')
      assert.equal(response.status, 503, `单账户耗尽应返回统一网关失败：${response.status} ${response.text}`)
      assert.deepEqual(hitKeys(offset), ['sk-multi-bad'], '未配置 retry_next 时不得隐式轮换同账户兄弟 Key')
    })

    await verifyContract(contractFailures, 'pre-header transport 失败必须逐账户切换', async () => {
      configureRetry(1, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, sharedBudget.apiKey, 'retry budget shared by all accounts')
      assert.equal(response.status, 200, `两个失败账户后应切到第三账户成功：${response.status} ${response.text}`)
      assert.deepEqual(
        hitKeys(offset),
        ['sk-shared-a', 'sk-shared-b', 'sk-shared-c'],
        '每个失败账户只尝试一次，随后按候选顺序切换下一个账户'
      )
    })

    await verifyContract(contractFailures, 'hard pre-header request timeout 必须切换后备账户', async () => {
      configureRetry(1, 0, { textFirstResponseTimeoutSeconds: 10 })
      const offset = hits.length
      // Do not globally accelerate the hard timeout here. Under a busy event
      // loop an artificial 80ms timer can win against response-header I/O even
      // after the mock has synchronously sent success, creating a false retry.
      const response = await postChat(
        gatewayBaseUrl,
        hardTimeout.apiKey,
        'hard request timeout may retry in place',
        AbortSignal.timeout(15_000)
      )
      assert.equal(response.status, 200, `hard timeout 切号后应成功：${response.status} ${response.text}`)
      assert.match(
        response.text,
        /mock success from sk-hard-timeout-backup"/,
        `hard timeout 后必须由后备账户返回成功：${response.text}`
      )
      assert.deepEqual(
        hitKeys(offset),
        ['sk-hard-timeout', 'sk-hard-timeout-backup'],
        'hard request timeout 不得自动重放同账号，应切换后备账户'
      )
    })

    await verifyContract(contractFailures, 'speed-first 配置型首字 deadline 只能切号，绝不原地重试', async () => {
      configureRetry(3, 0, { textFirstResponseTimeoutSeconds: 30 })
      await primeConfiguredFirstByteCutover(configuredFirstByte)
      const offset = hits.length
      const response = await withAcceleratedTimeout(10_000, 80, () => postChat(
        gatewayBaseUrl,
        configuredFirstByte.apiKey,
        'configured first-byte deadline must cut over'
      ))
      assert.equal(response.status, 200, `配置型首字截止应切后备成功：${response.status} ${response.text}`)
      assert.deepEqual(
        hitKeys(offset),
        ['sk-configured-deadline', 'sk-configured-deadline-backup'],
        '配置型 speed-first 首字截止不得重复当前账户，必须直接切后备账户'
      )
    })

    await verifyContract(contractFailures, '墙钟不足原地重试预算不应阻断后备账户切换', async () => {
      const wallFailures: string[] = []
      configureRetry(1, 1)
      const offset = hits.length
      const realDateNow = Date.now
      let fakeNowMs = realDateNow()
      const trackedRetryTimers = new Set<ReturnType<typeof setTimeout>>()
      let response: HttpResult | undefined
      try {
        Date.now = () => fakeNowMs
        advanceWallClockForTest = () => { fakeNowMs += 267_500 }
        response = await withTrackedTimeout(1_000, trackedRetryTimers, () => postChat(
          gatewayBaseUrl,
          wallInsufficient.apiKey,
          'wall budget cannot fit retry interval and final reserve'
        ))
      } finally {
        advanceWallClockForTest = undefined
        Date.now = realDateNow
      }
      assert(response, '墙钟不足场景必须返回稳定网关结果')
      await verifyContract(wallFailures, '稳定响应', async () => {
        assert.equal(response.status, 200, `墙钟不足时仍应切换后备账户：${response.status} ${response.text}`)
      })
      await verifyContract(wallFailures, 'retry timer', async () => {
        assert.equal(trackedRetryTimers.size, 0, '墙钟判定返回时不得存在仍活动的 retry interval timer')
      })
      await delay(1_100)
      await verifyContract(wallFailures, 'dispatch 次数', async () => {
        assert.deepEqual(hitKeys(offset), ['sk-wall-insufficient', 'sk-wall-insufficient-backup'], '墙钟不足时不应原地重试，但必须允许后备账户 dispatch')
        assert.equal(trackedRetryTimers.size, 0, '墙钟不足不得留下 retry interval timer')
      })
      await verifyContract(wallFailures, '资源清理', async () => {
        await assertFixtureResourcesClean(wallInsufficient, '墙钟不足')
      })
      if (wallFailures.length > 0) assert.fail(wallFailures.join('；'))
    })

    await verifyContract(contractFailures, '中间 transport 失败必须保持请求局部且 audit attempt 唯一', async () => {
      await assertIntermediateFailureNeutral(gatewayBaseUrl, intermediateNeutral)
    })

    await verifyContract(contractFailures, '无显式策略时多 Key 账户失败后必须直接切后备账户', async () => {
      configureRetry(1, 0)
      const offset = hits.length
      const response = await postChat(gatewayBaseUrl, uniqueKeySafetyCap.apiKey, 'unique key safety cap excludes same retry')
      assert.equal(response.status, 200, `首账户失败后应切 backup 成功：${response.status} ${response.text}`)
      const actualKeys = hitKeys(offset)
      assert.deepEqual(
        actualKeys,
        [allFailingMultiKeys[0]!, 'sk-unique-backup'],
        '未配置 retry_next 时不得穷尽同账户 Key，必须直接切换后备账户'
      )
      const primaryHits = actualKeys.filter((key) => key !== 'sk-unique-backup')
      assert.equal(new Set(primaryHits).size, 1, '无显式策略时只允许尝试首个同账户 Key')
      assert.equal(primaryHits.length, 1, '同账户失败后不得自动切换兄弟 Key 或原地重放')
    })

    await verifyContract(contractFailures, 'interval 等待期间 abort 必须取消重试并清空资源', async () => {
      const abortFailures: string[] = []
      configureRetry(1, 1)
      const offset = hits.length
      const controller = new AbortController()
      const request = postChat(gatewayBaseUrl, abortWait.apiKey, 'abort during retry interval', controller.signal)
        .then((response) => ({ response }), (error: unknown) => ({ error }))
      await waitUntil(() => hitKeys(offset).length === 1, 1_000, 'abort 场景首个上游 hit 未发生')
      await delay(100)
      controller.abort()
      const result = await request
      await verifyContract(abortFailures, '客户端结果', async () => {
        assert('error' in result, `客户端 abort 后 fetch 不得得到完整网关响应：${JSON.stringify(result)}`)
        assert(result.error instanceof Error && result.error.name === 'AbortError', `客户端 abort 应得到 AbortError：${String(result.error)}`)
      })
      await delay(1_100)
      await verifyContract(abortFailures, '上游 hit', async () => {
        assert.deepEqual(hitKeys(offset), ['sk-abort-wait'], 'abort 后超过完整 interval 仍不得发生第二个上游 hit')
      })

      await accountSideEffects.flushGatewayAccountSideEffectsForTest()
      await verifyContract(abortFailures, '并发槽与分组等待', async () => {
        await waitUntil(() => Object.keys(snapshotAccountConcurrency()).length === 0, 2_000, 'abort 后账户并发槽未释放')
        assert.deepEqual(snapshotAccountConcurrency(), {}, 'abort 后账户并发槽必须归零')
        assert.deepEqual(highConcurrencyGroupQueueSnapshot(), [], 'abort 后不得残留分组等待队列或其 timer')
      })

      await verifyContract(abortFailures, '副作用与恢复等待', async () => {
        const sideEffectState = accountSideEffects.getGatewayAccountSideEffectState()
        assert.equal(sideEffectState.queueLength, 0, 'abort 后副作用队列必须归零')
        assert.equal(sideEffectState.processing, false, 'abort 后副作用 drain 必须停止')
        assert.equal(sideEffectState.precheckPendingAccountCount, 0, 'abort 后不得残留 precheck 任务')
        assert.equal(sideEffectState.recoveryProbePendingAccountCount, 0, 'abort 后不得残留 recovery probe')
        assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, 'abort 后 precheck lease 必须归零')

        const waitSnapshot = accountSideEffects.recoverableUnavailableWaitCoordinatorSnapshotForTest()
        assert.deepEqual(waitSnapshot, { scopeCount: 0, waiterCount: 0, timerCount: 0 }, 'abort 后恢复等待 scope、waiter、timer 必须归零')
      })

      const account = repositories.findOpenAIAccountForGroup(
        abortWait.accounts[0]!.groupId,
        abortWait.accounts[0]!.accountId,
        access.systemAccountId
      )
      assert(account, 'abort 场景必须能读取账户运行凭据')
      await verifyContract(abortFailures, '电路租约', async () => {
        const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
        const childCircuit = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model))
        const parentCircuit = await circuitStore.get({ kind: 'account', accountRuntimeKey: gatewayAccountRuntimeKey(account) })
        assert.equal(childCircuit.lease, undefined, 'abort 后 protocol/model confirmation lease 必须释放')
        assert.equal(parentCircuit.lease, undefined, 'abort 后账户级 circuit lease 必须释放')
      })
      if (abortFailures.length > 0) {
        assert.fail(abortFailures.join('；'))
      }
    })

    if (contractFailures.length > 0) {
      assert.fail(`请求级原地重试 HTTP 契约仍为红灯（${contractFailures.length} 项）：\n${contractFailures.map((failure, index) => `${index + 1}. ${failure}`).join('\n')}`)
    }
    console.log('请求级原地重试真实 HTTP 回归通过：原地重试、关闭重试、HTTP frame、多 Key、共享预算和 abort 清理均符合契约')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest().catch(() => undefined)
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    clearAccountConcurrency()
    clearHighConcurrencyGroupQueues()
    accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    accountCircuit.resetGatewayAccountCircuitStoreForTest()
    try {
      await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    await removeTempRoot()
  }
}

function assertReplaySafetyGate(): void {
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: '/v1/chat/completions',
    path: '/v1/chat/completions',
    body: { model, messages: [{ role: 'user', content: 'safe foreground chat' }] }
  }, 'text'), false, '普通前台文本 chat 失败后必须由账户候选切换接管，不得自动同账号重放')
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model, input: 'background work', background: true }
  }, 'text'), false, 'background Responses 失败后必须由账户候选切换接管，不得自动同账号重放')
  assert.equal(automaticUpstreamReplayAllowedAfterDispatch({
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model, input: 'hosted work', tools: [{ type: 'web_search' }] }
  }, 'text'), false, '供应商托管工具失败后必须由账户候选切换接管，不得自动同账号重放')
}

function assertRetryModeIsolation(): void {
  const identity = {
    accountRuntimeKey: 'retry-mode-runtime',
    physicalCredentialKey: 'retry-mode-physical',
    protocolModelKey: gatewayAttemptProtocolModelKey({
      accountRuntimeKey: 'retry-mode-runtime',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      model
    }),
    keyFingerprint: 'retry-mode-fingerprint'
  }
  for (const mode of ['confirmation', 'key_rotation', 'semantic_retry'] as const) {
    const tracker = new GatewayRequestAttemptTracker()
    assert.deepEqual(tracker.tryRecordDispatchAttempt(identity), { allowed: true })
    const reservation = tracker.tryReserveSameAccountRetry({ ...identity, maxRetries: 1 })
    assert(reservation.reserved, `${mode} 互斥测试必须先预留原地重试 token`)
    const decision = tracker.tryRecordDispatchAttempt({
      ...identity,
      sameAccountRetryId: reservation.retryId,
      ...(mode === 'confirmation' ? { matchingConfirmation: true } : {}),
      ...(mode === 'key_rotation' ? { allowKeyRotation: true } : {}),
      ...(mode === 'semantic_retry' ? { semanticRetryId: 'semantic-retry-conflict' } : {})
    })
    assert.deepEqual(
      decision,
      { allowed: false, reason: 'same_account_retry_mode_conflict' },
      `${mode} 模式不得与原地重试叠加`
    )
  }

  const safetyCapTracker = new GatewayRequestAttemptTracker()
  const identities = Array.from({ length: 64 }, (_, index) => ({
    accountRuntimeKey: 'retry-safety-cap-runtime',
    physicalCredentialKey: 'retry-safety-cap-physical',
    protocolModelKey: gatewayAttemptProtocolModelKey({
      accountRuntimeKey: 'retry-safety-cap-runtime',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      model
    }),
    keyFingerprint: `retry-safety-cap-key-${index + 1}`
  }))
  identities.forEach((candidate, index) => {
    assert.deepEqual(
      safetyCapTracker.tryRecordDispatchAttempt({
        ...candidate,
        ...(index === 0 ? {} : { allowKeyRotation: true })
      }),
      { allowed: true },
      `第 ${index + 1} 个唯一 Key 必须可登记到 64 Key safety cap`
    )
  })
  const safetyCapBeforeRetry = safetyCapTracker.snapshot()
  assert.equal(safetyCapBeforeRetry.attemptedKeyFingerprints.length, 64, '普通 Key 尝试快照必须精确记录 64 个唯一 fingerprint')
  const lastIdentity = identities.at(-1)!
  const retry = safetyCapTracker.tryReserveSameAccountRetry({ ...lastIdentity, maxRetries: 1 })
  assert(retry.reserved, '第 64 个 Key 必须可预留一次原地重试')
  assert.deepEqual(
    safetyCapTracker.tryRecordDispatchAttempt({ ...lastIdentity, sameAccountRetryId: retry.retryId }),
    { allowed: true },
    '第 64 个 Key 的原地重试 token 必须放行一次'
  )
  assert.deepEqual(
    safetyCapTracker.snapshot(),
    safetyCapBeforeRetry,
    'same retry 不得把第 64 个 Key 重复计为第 65 个 unique Key'
  )
}

async function verifyContract(failures: string[], label: string, run: () => Promise<void>): Promise<void> {
  try {
    await run()
  } catch (error) {
    failures.push(`${label}：${error instanceof Error ? error.message : String(error)}`)
  }
}

function configureRetry(
  attempts: number,
  intervalSeconds: number,
  overrides: { textFirstResponseTimeoutSeconds?: number } = {}
): void {
  settingsRepository.updateSettings({
    temporaryUnschedulableRetryAttempts: attempts,
    temporaryUnschedulableRetryIntervalSeconds: intervalSeconds,
    noAvailableAccountWaitTimeoutSeconds: 10,
    textFirstResponseTimeoutSeconds: overrides.textFirstResponseTimeoutSeconds ?? 10,
    textStreamIdleTimeoutSeconds: 5,
    textUncommittedAttemptMaxLifetimeSeconds: 60
  })
  gatewayCache.clearGatewayRuntimeCache()
}

function createGatewayFixture(
  upstreamBaseUrl: string,
  label: string,
  definitions: Array<{
    keys: string[]
    priority: number
    fallbackEnabled?: boolean
    policyAction?: AccountErrorHandlingRuleAction
    policyMarker?: string
  }>,
  options: { normalRoutingConfig?: unknown; singleGroup?: boolean } = {}
): GatewayFixture {
  const sharedGroup = options.singleGroup
    ? repositories.createGroup({
        name: `${label}-单一普通分组`,
        providerCode: GPT_VENDOR_CODE,
        enabled: true
      }, access)
    : undefined
  const accounts = definitions.map((definition, index) => {
    const group = sharedGroup ?? repositories.createGroup({
        name: `${label}-分组-${index + 1}`,
        providerCode: GPT_VENDOR_CODE,
        enabled: true
      }, access)
    const account = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `${label}-账户-${index + 1}`,
      type: 'api_key',
      credentials: {
        api_key: definition.keys[0],
        ...(definition.keys.length > 1
          ? { api_keys: definition.keys, api_key_strategy: 'round_robin' }
          : {}),
        base_url: upstreamBaseUrl,
        ...(definition.policyAction
          ? {
              error_handling_rules: [{
                enabled: true,
                name: `${label}-用户显式策略`,
                priority: 1,
                status_codes: [471],
                keywords: [requiredText(definition.policyMarker, 'policyMarker')],
                action: definition.policyAction
              }]
            }
          : {})
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 8,
      priority: definition.priority,
      fallbackEnabled: definition.fallbackEnabled,
      supportedModels: [model],
      healthCheckModel: model
    }, access)
    activateAccount(account.id)
    return { accountId: account.id, groupId: group.id, keys: definition.keys }
  })
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `${label}-网关Key`,
    groupBindings: [...new Map(accounts.map((account, index) => [account.groupId, {
      groupId: account.groupId,
      priority: index + 1,
      status: 'active' as const
    }])).values()],
    status: 'active',
    ...(options.normalRoutingConfig === undefined
      ? {}
      : { normalRoutingConfig: options.normalRoutingConfig })
  }, access)
  assert(apiKey.key && apiKey.routeStrategyId, `${label} 必须创建可用网关 API Key 与路由策略`)
  return { apiKey: apiKey.key, routeStrategyId: apiKey.routeStrategyId, accounts }
}

function activateAccount(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true)
}

function requireDispatchAccount(fixture: GatewayFixture, accountIndex = 0) {
  const selected = fixture.accounts[accountIndex]
  assert(selected, `fixture 缺少第 ${accountIndex + 1} 个账户`)
  const account = repositories.listOpenAIAccountsForGroup(selected.groupId, access.systemAccountId, {
    requestedModel: model
  }).find((candidate) => candidate.id === selected.accountId)
  assert(account, `找不到调度账户 ${selected.accountId}`)
  return account
}

function hotQualityScope(account: ReturnType<typeof requireDispatchAccount>) {
  return {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
    requestLane: 'text' as const,
    modelFamily: hotQuality.gatewayHotQualityModelFamily(model)
  }
}

async function primeConfiguredFirstByteCutover(fixture: GatewayFixture): Promise<void> {
  const account = requireDispatchAccount(fixture)
  const scope = latencyDegradation.normalRouteLatencyDegradationScope({
    systemAccountId: access.systemAccountId,
    routeStrategyId: fixture.routeStrategyId,
    groupId: fixture.accounts[0]!.groupId
  })
  assert(scope, 'speed-first 首字截止回归必须取得普通路由 scope')
  const result = await latencyDegradation.recordNormalRouteFirstByteSlowAsync(account, scope, {
    firstByteDeadlineMs: 10_000,
    slowTriggerCount: 2,
    slowWindowSeconds: 300,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 10,
    degradedTtlSeconds: 60,
    maxFirstByteRetriesPerRequest: 1
  }, '配置型首字 deadline 回归预置第一个慢样本')
  assert.equal(result?.degraded, false, '一个预置慢样本不得提前把账户标记为速度降级')
}

async function assertIntermediateFailureNeutral(gatewayBaseUrl: string, fixture: GatewayFixture): Promise<void> {
  const failures: string[] = []
  const account = requireDispatchAccount(fixture)
  const qualityScope = hotQualityScope(account)
  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  const childScope = accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model)
  const parentScope = { kind: 'account' as const, accountRuntimeKey: gatewayAccountRuntimeKey(account) }
  const childBefore = await circuitStore.get(childScope)
  const parentBefore = await circuitStore.get(parentScope)
  assert.equal(await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope), undefined, '中间失败测试必须从冷质量 scope 开始')

  configureRetry(1, 0)
  const traceId = `trace-same-account-intermediate-${Date.now()}`
  const offset = hits.length
  const response = await postChat(
    gatewayBaseUrl,
    fixture.apiKey,
    'intermediate failure must remain request local',
    undefined,
    traceId
  )
  await verifyContract(failures, 'HTTP 与真实 dispatch 顺序', async () => {
    assert.equal(response.status, 200, `中间失败后同账户重试应成功：${response.status} ${response.text}`)
    assert.deepEqual(
      hitKeys(offset),
      ['sk-intermediate-neutral', 'sk-intermediate-neutral'],
      '中间失败与成功必须是同账户、同 Key 的两个真实 dispatch'
    )
  })

  usageRecordQueue.flushAllUsageRecordQueue()
  await auditLogQueue.flushAllAuditLogQueueAsync()
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()

  await verifyContract(failures, 'circuit 状态', async () => {
    const childAfter = await circuitStore.get(childScope)
    const parentAfter = await circuitStore.get(parentScope)
    assert.equal(childAfter.phase, 'CLOSED', '成功的原地重试后 child circuit 必须保持 CLOSED')
    assert.equal(childAfter.generation, childBefore.generation, '请求内中间失败不得推进 child circuit generation')
    assert.equal(childAfter.transitionId, childBefore.transitionId, '请求内中间失败不得写 child circuit transition')
    assert.equal(childAfter.lease, undefined, '请求结束后 child circuit lease 必须为空')
    assert.equal(parentAfter.phase, 'CLOSED', '成功的原地重试后 parent circuit 必须保持 CLOSED')
    assert.equal(parentAfter.generation, parentBefore.generation, '请求内中间失败不得推进 parent circuit generation')
    assert.equal(parentAfter.transitionId, parentBefore.transitionId, '请求内中间失败不得写 parent circuit transition')
    assert.equal(parentAfter.lease, undefined, '请求结束后 parent circuit lease 必须为空')
  })

  await verifyContract(failures, 'shared hot-quality', async () => {
    const quality = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(qualityScope)
    assert(quality, '最终成功必须留下主账户 hot-quality 成功样本')
    assert.deepEqual({
      qualityAttempts: quality.window5m.qualityAttempts,
      completedResponses: quality.window5m.completedResponses,
      localTransportFailures: quality.window5m.localTransportFailures,
      timeouts: quality.window5m.timeouts,
      readInterruptions: quality.window5m.readInterruptions,
      incompleteResponses: quality.window5m.incompleteResponses,
      unknownOutcomes: quality.window5m.unknownOutcomes
    }, {
      qualityAttempts: 1,
      completedResponses: 1,
      localTransportFailures: 0,
      timeouts: 0,
      readInterruptions: 0,
      incompleteResponses: 0,
      unknownOutcomes: 0
    }, '两个真实 dispatch 只能留下一个最终成功质量样本，中间失败不得以任何形式进入共享 hot-quality')
  })

  await verifyContract(failures, 'Key 与账户状态', async () => {
    const summary = repositories.findAccountForTest(fixture.accounts[0]!.accountId, access)
    assert.equal(summary?.status, 'active', '中间失败不得修改账户业务状态')
    assert.equal(summary?.schedulable, true, '中间失败不得取消账户调度')
    assert.equal(summary?.apiKeyRuntime?.temporaryUnavailable ?? 0, 0, '中间失败不得把 Key 写成共享不可用')
    assert.equal(summary?.apiKeyRuntime?.allUnavailable ?? false, false, '中间失败不得把 Key 池写成全死')
    assert.equal(
      apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()
        .filter((entry) => entry.accountId === fixture.accounts[0]!.accountId).length,
      0,
      '中间失败不得留下进程级 Key failure guard'
    )
    assert.equal(
      accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[fixture.accounts[0]!.accountId],
      undefined,
      '中间失败不得留下账户共享屏障'
    )
  })

  await verifyContract(failures, 'audit attempt 唯一性与请求局部归因', async () => {
    const auditFailures: string[] = []
    const auditAttempts = databaseModule.getDatasetDatabase().prepare(`
      SELECT attempts.id, attempts.attempt_index, attempts.account_id, attempts.success,
             attempts.error_phase, attempts.error_code
      FROM audit_log_attempts attempts
      INNER JOIN audit_logs logs ON logs.id = attempts.audit_log_id
      WHERE logs.trace_id = ?
      ORDER BY attempts.attempt_index ASC
    `).all(traceId) as Array<{
      id: string
      attempt_index: number
      account_id: string | null
      success: number
      error_phase: string | null
      error_code: string | null
    }>
    await verifyContract(auditFailures, 'attempt 数量与 ID', async () => {
      assert.equal(auditAttempts.length, 2, `两个真实 dispatch 必须写两个 audit attempt：${JSON.stringify(auditAttempts)}`)
      assert.equal(new Set(auditAttempts.map((attempt) => attempt.id)).size, 2, '每次真实 dispatch 的 audit attempt ID 必须唯一')
    })
    await verifyContract(auditFailures, 'attempt 顺序与账户', async () => {
      assert.deepEqual(
        auditAttempts.map((attempt) => ({
          attemptIndex: attempt.attempt_index,
          accountId: attempt.account_id,
          success: attempt.success
        })),
        [
          { attemptIndex: 1, accountId: fixture.accounts[0]!.accountId, success: 0 },
          { attemptIndex: 2, accountId: fixture.accounts[0]!.accountId, success: 1 }
        ],
        'audit attempt_index 必须严格递增，且中间失败与最终成功必须归属同一账户'
      )
    })
    await verifyContract(auditFailures, 'audit 失败诊断', async () => {
      assert(!JSON.stringify(auditAttempts[0]).includes('account_upstream'), '中间 audit attempt 不得携带共享 account_upstream 归因')
    })

    const usageItems = requireUsageRecordDetails(
      repositories,
      repositories.listUsageRecords(access, { page: 1, pageSize: 100 }).items
        .filter((item) => item.traceId === traceId),
      access
    )
    const intermediateUsage = usageItems.filter((item) => item.success === false)
    await verifyContract(auditFailures, 'usage 请求局部归因', async () => {
      assert(
        intermediateUsage.every((item) => item.failureAttribution !== 'account_upstream'),
        `中间失败只能保持 request-local/unknown，不得写 account_upstream：${JSON.stringify(intermediateUsage)}`
      )
    })
    if (auditFailures.length > 0) assert.fail(auditFailures.join('；'))
  })

  await assertFixtureResourcesClean(fixture, '中间失败成功收口')
  if (failures.length > 0) assert.fail(failures.join('；'))
}

async function assertFixtureResourcesClean(fixture: GatewayFixture, label: string): Promise<void> {
  await accountSideEffects.flushGatewayAccountSideEffectsForTest()
  await waitUntil(() => Object.keys(snapshotAccountConcurrency()).length === 0, 2_000, `${label} 后账户并发槽未释放`)
  assert.deepEqual(snapshotAccountConcurrency(), {}, `${label} 后账户并发槽必须归零`)
  assert.deepEqual(highConcurrencyGroupQueueSnapshot(), [], `${label} 后不得残留分组等待队列或 timer`)
  const sideEffectState = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(sideEffectState.queueLength, 0, `${label} 后副作用队列必须归零`)
  assert.equal(sideEffectState.processing, false, `${label} 后副作用 drain 必须停止`)
  assert.equal(sideEffectState.precheckPendingAccountCount, 0, `${label} 后不得残留 precheck 任务`)
  assert.equal(sideEffectState.recoveryProbePendingAccountCount, 0, `${label} 后不得残留 recovery probe`)
  assert.equal(accountSideEffects.precheckHalfOpenGroupLeaseCountForTest(), 0, `${label} 后 precheck lease 必须归零`)
  assert.deepEqual(
    accountSideEffects.recoverableUnavailableWaitCoordinatorSnapshotForTest(),
    { scopeCount: 0, waiterCount: 0, timerCount: 0 },
    `${label} 后恢复等待 scope、waiter、timer 必须归零`
  )
  const circuitStore = accountCircuit.getGatewayAccountCircuitStore()
  for (let index = 0; index < fixture.accounts.length; index += 1) {
    const account = requireDispatchAccount(fixture, index)
    const child = await circuitStore.get(accountCircuit.gatewayAccountProtocolModelScope(account, 'text', model))
    const parent = await circuitStore.get({ kind: 'account', accountRuntimeKey: gatewayAccountRuntimeKey(account) })
    assert.equal(child.lease, undefined, `${label} 后 child circuit lease 必须释放`)
    assert.equal(parent.lease, undefined, `${label} 后 parent circuit lease 必须释放`)
  }
}

async function withAcceleratedTimeout<T>(targetMs: number, replacementMs: number, run: () => Promise<T>): Promise<T> {
  const originalSetTimeout = globalThis.setTimeout
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delayMs?: number, ...args: unknown[]) => originalSetTimeout(
    callback,
    delayMs === targetMs ? replacementMs : delayMs,
    ...args
  )) as typeof globalThis.setTimeout
  try {
    return await run()
  } finally {
    globalThis.setTimeout = originalSetTimeout
  }
}

async function withTrackedTimeout<T>(
  targetMs: number,
  tracked: Set<ReturnType<typeof setTimeout>>,
  run: () => Promise<T>
): Promise<T> {
  const originalSetTimeout = globalThis.setTimeout
  const originalClearTimeout = globalThis.clearTimeout
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delayMs?: number, ...args: unknown[]) => {
    let handle: ReturnType<typeof setTimeout>
    const wrapped = (...callbackArgs: unknown[]) => {
      tracked.delete(handle)
      callback(...callbackArgs)
    }
    handle = originalSetTimeout(wrapped, delayMs, ...args)
    if (delayMs === targetMs) tracked.add(handle)
    return handle
  }) as typeof globalThis.setTimeout
  globalThis.clearTimeout = ((handle: Parameters<typeof clearTimeout>[0]) => {
    tracked.delete(handle as ReturnType<typeof setTimeout>)
    originalClearTimeout(handle)
  }) as typeof globalThis.clearTimeout
  try {
    return await run()
  } finally {
    globalThis.setTimeout = originalSetTimeout
    globalThis.clearTimeout = originalClearTimeout
  }
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const key = bearerKey(req.headers.authorization)
    const ordinalForKey = hits.filter((hit) => hit.key === key).length + 1
    hits.push({ key, ordinalForKey })
    req.resume()
    req.once('end', () => {
      const behavior = behaviorByKey.get(key)
      if (!behavior) {
        res.writeHead(500, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: `missing behavior for ${key}` } }))
        return
      }
      if (behavior === 'always_hang' || (behavior === 'hang_once_then_success' && ordinalForKey === 1)) {
        return
      }
      if (behavior === 'advance_wall_and_reset') {
        advanceWallClockForTest?.()
        const timer = setTimeout(() => res.destroy(), 20)
        timer.unref()
        return
      }
      if (behavior === 'always_reset' || (behavior === 'reset_once_then_success' && ordinalForKey === 1)) {
        const timer = setTimeout(() => res.destroy(), 20)
        timer.unref()
        return
      }
      if (behavior === 'complete_471') {
        res.writeHead(471, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          error: {
            type: 'untrusted_vendor_type',
            code: 'untrusted_vendor_code',
            message: 'explicit-http-retry-next vendor-private-error'
          }
        }))
        return
      }
      sendSuccess(res, key)
    })
  })
}

function sendSuccess(res: http.ServerResponse, key: string): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify({
    id: `chatcmpl_${key}`,
    object: 'chat.completion',
    choices: [{
      index: 0,
      message: { role: 'assistant', content: `mock success from ${key}` },
      finish_reason: 'stop'
    }]
  }))
}

async function postChat(
  gatewayBaseUrl: string,
  apiKey: string,
  content: string,
  signal?: AbortSignal,
  traceId?: string
): Promise<HttpResult> {
  const response = await fetch(`${gatewayBaseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      ...(traceId ? { 'x-trace-id': traceId } : {})
    },
    body: JSON.stringify({
      model,
      messages: [{ role: 'user', content }],
      stream: false
    }),
    signal
  })
  return { status: response.status, text: await response.text() }
}

function hitKeys(offset: number): string[] {
  return hits.slice(offset).map((hit) => hit.key)
}

function bearerKey(authorization: string | undefined): string {
  return String(authorization ?? '').replace(/^Bearer\s+/i, '')
}

function requiredText(value: string | undefined, field: string): string {
  const normalized = value?.trim()
  assert(normalized, `${field} 不能为空`)
  return normalized
}

async function waitUntil(
  predicate: () => boolean,
  timeoutMs: number,
  failureMessage: string
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return
    await delay(10)
  }
  assert.fail(failureMessage)
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolveListen, rejectListen) => {
    server.once('error', rejectListen)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', rejectListen)
      resolveListen()
    })
  })
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolveClose) => server.close(() => resolveClose()))
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) throw error
      await delay(200)
    }
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

main().catch((error) => {
  console.error(error)
  process.exitCode = 1
})
