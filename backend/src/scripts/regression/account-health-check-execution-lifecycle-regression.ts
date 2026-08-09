import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountSummary } from '../../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createRuntimeProbeStateStore } from '../../shared/runtime-probe-state-store.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-health-execution-lifecycle-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'health-execution-lifecycle-regression-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, healthCheckService, coordinator, backgroundIpc] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/background/account-health-check.service.js'),
  import('../../modules/gateway/runtime/availability-probe-coordinator.js'),
  import('../../modules/background/background-ipc.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const settings = { intervalHours: 1, jitterMinutes: 0, failureThreshold: 1, maxPauseMinutes: 5 }

try {
  const group = repositories.createGroup({ name: 'source fence 生命周期回归分组', providerCode: 'gpt', enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'source fence 生命周期回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-lifecycle', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  const summary = repositories.findAccountSummary(account.id, access)
  assert(summary, '回归前置必须创建可调度账户')

  let failFirstGet = true
  const baseStore = createRuntimeProbeStateStore<any>(`health-execution-lifecycle-${account.id}`)
  const failingCoordinatorStore = new Proxy(baseStore, {
    get(target, property) {
      const value = Reflect.get(target, property, target)
      if (property === 'get') {
        return async (...args: unknown[]) => {
          if (failFirstGet) {
            failFirstGet = false
            throw new Error('coordinator acquisition failed for regression')
          }
          return await (value as (...input: unknown[]) => Promise<unknown>).apply(target, args)
        }
      }
      return typeof value === 'function' ? value.bind(target) : value
    }
  })
  coordinator.setAvailabilityProbeStateStoreForTest(failingCoordinatorStore)

  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(summary, settings, 'request_failure', sourceFence(account.id, 1)),
    true,
    '初始 source fence 必须进入健康检查队列'
  )
  await waitForQueueIdle(healthCheckService)

  repositories.updateAccount(account.id, { status: 'disabled' }, access)
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(summary, settings, 'scheduled', sourceFence(account.id, 2)),
    true,
    'coordinator 获取失败且零重试后，后续 source fence 必须创建新执行而非被遗留 execution 合并'
  )
  const followUpSnapshot = healthCheckService.getAccountHealthCheckQueueSnapshot()
  assert.equal(
    followUpSnapshot.pendingCount + followUpSnapshot.runningCount,
    1,
    '后续 source fence 必须作为独立队列项等待结算'
  )
  await waitForQueueIdle(healthCheckService)

  const coordinatorRetryAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'coordinator 故障有界补跑回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-coordinator-retry', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  assert(
    repositories.recordAccountHealthCheckSuccess(coordinatorRetryAccount.id, {
      intervalHours: settings.intervalHours,
      jitterMinutes: settings.jitterMinutes,
      failureThreshold: settings.failureThreshold,
      statusCode: 200
    }),
    'coordinator 故障补跑回归前置必须激活账户'
  )
  const coordinatorRetrySummary = repositories.findAccountSummary(coordinatorRetryAccount.id, access)
  assert(coordinatorRetrySummary, 'coordinator 故障补跑回归前置必须创建可调度账户')
  let coordinatorAcquireFailures = 0
  const coordinatorRetryStore = new Proxy(createRuntimeProbeStateStore<any>(`health-execution-retry-${coordinatorRetryAccount.id}`), {
    get(target, property) {
      const value = Reflect.get(target, property, target)
      if (property === 'get') {
        return async (...args: unknown[]) => {
          if (coordinatorAcquireFailures === 0) {
            coordinatorAcquireFailures += 1
            throw new Error('coordinator acquire failed for bounded retry regression')
          }
          return await (value as (...input: unknown[]) => Promise<unknown>).apply(target, args)
        }
      }
      return typeof value === 'function' ? value.bind(target) : value
    }
  })
  coordinator.setAvailabilityProbeStateStoreForTest(coordinatorRetryStore)
  let coordinatorRetryProbeCalls = 0
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async (candidate) => {
    coordinatorRetryProbeCalls += 1
    return probeResult(candidate, false)
  })
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(coordinatorRetrySummary, settings, 'request_failure'),
    true,
    'coordinator 首次故障的 request_failure 必须进入健康检查队列'
  )
  await waitForQueueIdle(healthCheckService, 4_000)
  assert.equal(coordinatorAcquireFailures, 1, '回归前置必须只注入一次 coordinator 获取失败')
  assert.equal(coordinatorRetryProbeCalls, 1, 'coordinator 故障后必须保留一次有界 request_failure 补跑')
  assert.equal(
    repositories.findAccountSummary(coordinatorRetryAccount.id, access)?.status,
    'temporary_unavailable',
    '补跑后的完整上游失败必须仍能确认 temporary_unavailable'
  )
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(undefined)

  const settlementAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'source fence coordinator 结算失败回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-settlement', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  const settlementSummary = repositories.findAccountSummary(settlementAccount.id, access)
  assert(settlementSummary, 'coordinator 结算失败回归前置必须创建可调度账户')

  const settledSourceFences: Array<{ sourceFenceId: string; outcome: string }> = []
  backgroundIpc.setClientSourceFenceSettlementObserverForTest((fence, outcome) => {
    settledSourceFences.push({ sourceFenceId: fence.sourceFenceId, outcome })
  })
  let coordinatorSettlementCommitAttempts = 0
  const settlementFailingStore = new Proxy(createRuntimeProbeStateStore<any>(`health-execution-settlement-${settlementAccount.id}`), {
    get(target, property) {
      const value = Reflect.get(target, property, target)
      if (property === 'commitGenerationRun') {
        return async () => {
          coordinatorSettlementCommitAttempts += 1
          throw new Error('coordinator settlement failed for regression')
        }
      }
      return typeof value === 'function' ? value.bind(target) : value
    }
  })
  coordinator.setAvailabilityProbeStateStoreForTest(settlementFailingStore)
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async () => {
    throw new Error('probe failed after acquiring coordinator owner for regression')
  })
  const settlementFence = sourceFence(settlementAccount.id, 3)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(settlementSummary, settings, 'request_failure', settlementFence),
    true,
    'owner coordinator 结算失败场景必须进入健康检查队列'
  )
  await waitForQueueIdle(healthCheckService)
  assert.equal(coordinatorSettlementCommitAttempts >= 1, true, '已获取 owner 的 probe 失败后必须尝试结算 coordinator')
  assert.deepEqual(
    settledSourceFences,
    [{ sourceFenceId: settlementFence.sourceFenceId, outcome: 'probe_task_failure' }],
    'coordinator 二次结算失败不得阻止本地 source fence 以 probe_task_failure 结算'
  )
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)

  const tailAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'request failure 尾随与来源 fence 回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-tail', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  assert(
    repositories.recordAccountHealthCheckSuccess(tailAccount.id, {
      intervalHours: settings.intervalHours,
      jitterMinutes: settings.jitterMinutes,
      failureThreshold: settings.failureThreshold,
      statusCode: 200
    }),
    '尾随回归前置必须激活账户'
  )
  const tailSummary = repositories.findAccountSummary(tailAccount.id, access)
  assert(tailSummary, '尾随回归前置必须创建可调度账户')
  assert.equal(tailSummary.status, 'active', '尾随回归必须从 active 账户开始，不能混入激活状态机')

  const tailSettlements: Array<{ sourceFenceId: string; outcome: string }> = []
  backgroundIpc.setClientSourceFenceSettlementObserverForTest((fence, outcome) => {
    tailSettlements.push({ sourceFenceId: fence.sourceFenceId, outcome })
  })
  let probeCalls = 0
  let releaseFirstProbe!: () => void
  let releaseSecondProbe!: () => void
  let firstProbeStarted!: () => void
  let secondProbeStarted!: () => void
  const firstProbeGate = new Promise<void>((resolvePromise) => { releaseFirstProbe = resolvePromise })
  const secondProbeGate = new Promise<void>((resolvePromise) => { releaseSecondProbe = resolvePromise })
  const firstProbeStartedGate = new Promise<void>((resolvePromise) => { firstProbeStarted = resolvePromise })
  const secondProbeStartedGate = new Promise<void>((resolvePromise) => { secondProbeStarted = resolvePromise })
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async (candidate) => {
    probeCalls += 1
    if (probeCalls === 1) {
      firstProbeStarted()
      await firstProbeGate
      return probeResult(candidate, true)
    }
    if (probeCalls === 2) {
      secondProbeStarted()
      await secondProbeGate
      return probeResult(candidate, false)
    }
    assert.fail(`request_failure 尾随不得额外创建探针：${probeCalls}`)
  })
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(tailSummary, settings, 'request_failure'),
    true,
    '首轮普通 request_failure 必须进入健康检查队列'
  )
  await firstProbeStartedGate
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(tailSummary, settings, 'request_failure'),
    true,
    '运行中的普通 request_failure 必须追加一个尾随探针'
  )
  releaseFirstProbe()
  assert(
    await waitForCondition(() => probeCalls === 2, 1_000),
    `首轮完成后必须启动 request_failure 尾随探针：${JSON.stringify({
      queue: healthCheckService.getAccountHealthCheckQueueSnapshot(),
      account: repositories.findAccountSummary(tailAccount.id, access)
    })}`
  )
  await secondProbeStartedGate
  const tailFence = sourceFence(tailAccount.id, 4)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(tailSummary, settings, 'request_failure', tailFence),
    true,
    '尾随运行中到达的 source fence 必须复用尾随而不是替换为 source-only 任务'
  )
  releaseSecondProbe()
  await waitForQueueIdle(healthCheckService)
  assert.equal(probeCalls, 2, '首轮加尾随只能执行两次物理探针')
  assert.equal(
    repositories.findAccountSummary(tailAccount.id, access)?.status,
    'temporary_unavailable',
    '尾随完整 HTTP 失败必须保留普通账户健康写权限并进入 temporary_unavailable'
  )
  assert.deepEqual(
    tailSettlements,
    [{ sourceFenceId: tailFence.sourceFenceId, outcome: 'unknown' }],
    '完整 HTTP 尾随失败必须只以 unknown 结算来源 fence，不能阻止账户级确认'
  )

  const lateFenceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '探针结算后晚到来源 fence 回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-late-fence', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  assert(
    repositories.recordAccountHealthCheckSuccess(lateFenceAccount.id, {
      intervalHours: settings.intervalHours,
      jitterMinutes: settings.jitterMinutes,
      failureThreshold: settings.failureThreshold,
      statusCode: 200
    }),
    '晚到来源 fence 回归前置必须激活账户'
  )
  const lateFenceSummary = repositories.findAccountSummary(lateFenceAccount.id, access)
  assert(lateFenceSummary, '晚到来源 fence 回归前置必须创建可调度账户')
  let releaseLateFenceCoordinatorRead!: () => void
  let lateFenceCoordinatorReadStarted!: () => void
  const lateFenceCoordinatorReadGate = new Promise<void>((resolvePromise) => { releaseLateFenceCoordinatorRead = resolvePromise })
  const lateFenceCoordinatorReadStartedGate = new Promise<void>((resolvePromise) => { lateFenceCoordinatorReadStarted = resolvePromise })
  let lateFenceCoordinatorGetCount = 0
  const lateFenceStore = new Proxy(createRuntimeProbeStateStore<any>(`health-execution-late-fence-${lateFenceAccount.id}`), {
    get(target, property) {
      const value = Reflect.get(target, property, target)
      if (property === 'get') {
        return async (...args: unknown[]) => {
          lateFenceCoordinatorGetCount += 1
          if (lateFenceCoordinatorGetCount === 3) {
            lateFenceCoordinatorReadStarted()
            await lateFenceCoordinatorReadGate
          }
          return await (value as (...input: unknown[]) => Promise<unknown>).apply(target, args)
        }
      }
      return typeof value === 'function' ? value.bind(target) : value
    }
  })
  coordinator.setAvailabilityProbeStateStoreForTest(lateFenceStore)
  const lateFenceSettlements: Array<{ sourceFenceId: string; outcome: string }> = []
  backgroundIpc.setClientSourceFenceSettlementObserverForTest((fence, outcome) => {
    lateFenceSettlements.push({ sourceFenceId: fence.sourceFenceId, outcome })
  })
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async (candidate) => probeResult(candidate, true))
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(lateFenceSummary, settings, 'request_failure'),
    true,
    '晚到来源 fence 场景的普通 request_failure 必须进入队列'
  )
  await lateFenceCoordinatorReadStartedGate
  const lateFence = sourceFence(lateFenceAccount.id, 1)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(lateFenceSummary, settings, 'request_failure', lateFence),
    true,
    '探针已结算但尚在读取共享 fence 时，晚到来源 fence 必须即时关联本代成功结果'
  )
  assert.deepEqual(
    lateFenceSettlements,
    [{ sourceFenceId: lateFence.sourceFenceId, outcome: 'success' }],
    '晚到来源 fence 不得在 finally 中被错误降级为 unknown'
  )
  releaseLateFenceCoordinatorRead()
  await waitForQueueIdle(healthCheckService)
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(undefined)

  const joinedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '来源 owner 占用时的 request failure 回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-joined', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  assert(
    repositories.recordAccountHealthCheckSuccess(joinedAccount.id, {
      intervalHours: settings.intervalHours,
      jitterMinutes: settings.jitterMinutes,
      failureThreshold: settings.failureThreshold,
      statusCode: 200
    }),
    '来源 owner 占用回归前置必须激活账户'
  )
  const joinedSummary = repositories.findAccountSummary(joinedAccount.id, access)
  assert(joinedSummary, '来源 owner 占用回归前置必须创建可调度账户')
  const joinedSettlements: Array<{ sourceFenceId: string; outcome: string }> = []
  backgroundIpc.setClientSourceFenceSettlementObserverForTest((fence, outcome) => {
    joinedSettlements.push({ sourceFenceId: fence.sourceFenceId, outcome })
  })
  const joinedCoordinatorStore = createRuntimeProbeStateStore<any>(`health-execution-joined-${joinedAccount.id}`)
  coordinator.setAvailabilityProbeStateStoreForTest(joinedCoordinatorStore)
  const joinedScope = gatewayAccountRuntimeKey(joinedSummary)
  const joinedRuntimeKey = coordinator.availabilityProbeRuntimeKey(
    joinedScope,
    'account_health_check',
    joinedSummary.configRevision ?? 1
  )
  const foreignLeaseUntilMs = Date.now() + 100
  assert.equal(await joinedCoordinatorStore.setIfAbsent({
    runtimeKey: joinedRuntimeKey,
    generation: 1,
    nextProbeAtMs: foreignLeaseUntilMs,
    accountRuntimeScope: joinedScope,
    probeKind: 'account_health_check',
    configRevision: joinedSummary.configRevision ?? 1,
    probeRunId: 'foreign-source-owner',
    probeRunUntilMs: foreignLeaseUntilMs
  }, 1_000), true, '回归前置必须建立一个未完成的来源探针 owner')
  let joinedProbeCalls = 0
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async (candidate) => {
    joinedProbeCalls += 1
    return probeResult(candidate, false)
  })
  const joinedFence = sourceFence(joinedAccount.id, 5)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(joinedSummary, settings, 'request_failure', joinedFence),
    true,
    '来源派发先到时必须进入健康检查队列'
  )
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(joinedSummary, settings, 'request_failure'),
    false,
    '同优先级普通失败必须复用来源任务并提升为账户级确认语义'
  )
  assert(await waitForCondition(() => joinedSettlements.length === 1, 1_000), '来源 fence 必须在 join 时以 unknown 结算')
  assert.equal(joinedProbeCalls, 0, '远端来源 owner 未到期时不得并发执行第二次物理探针')
  await waitForQueueIdle(healthCheckService)
  assert.equal(joinedProbeCalls, 1, '远端来源 owner 到期后必须补跑一次普通 request_failure 探针')
  assert.equal(
    repositories.findAccountSummary(joinedAccount.id, access)?.status,
    'temporary_unavailable',
    '来源 owner 不能吞掉普通 request_failure 的 temporary_unavailable 确认'
  )
  assert.deepEqual(
    joinedSettlements,
    [{ sourceFenceId: joinedFence.sourceFenceId, outcome: 'unknown' }],
    '来源 owner 的完成结果不能被本地普通失败任务错误消费'
  )

  const exceptionAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '异常后尾随 request failure 回归账户',
    type: 'api_key',
    credentials: { api_key: 'sk-health-execution-exception', base_url: 'https://example.test/v1' },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5']
  }, access)
  assert(
    repositories.recordAccountHealthCheckSuccess(exceptionAccount.id, {
      intervalHours: settings.intervalHours,
      jitterMinutes: settings.jitterMinutes,
      failureThreshold: settings.failureThreshold,
      statusCode: 200
    }),
    '异常尾随回归前置必须激活账户'
  )
  const exceptionSummary = repositories.findAccountSummary(exceptionAccount.id, access)
  assert(exceptionSummary, '异常尾随回归前置必须创建可调度账户')
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)
  const exceptionSettlements: Array<{ sourceFenceId: string; outcome: string }> = []
  backgroundIpc.setClientSourceFenceSettlementObserverForTest((fence, outcome) => {
    exceptionSettlements.push({ sourceFenceId: fence.sourceFenceId, outcome })
  })
  let exceptionProbeCalls = 0
  let releaseExceptionFirstProbe!: () => void
  let releaseExceptionSecondProbe!: () => void
  let exceptionFirstProbeStarted!: () => void
  let exceptionSecondProbeStarted!: () => void
  const exceptionFirstProbeGate = new Promise<void>((resolvePromise) => { releaseExceptionFirstProbe = resolvePromise })
  const exceptionSecondProbeGate = new Promise<void>((resolvePromise) => { releaseExceptionSecondProbe = resolvePromise })
  const exceptionFirstProbeStartedGate = new Promise<void>((resolvePromise) => { exceptionFirstProbeStarted = resolvePromise })
  const exceptionSecondProbeStartedGate = new Promise<void>((resolvePromise) => { exceptionSecondProbeStarted = resolvePromise })
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(async (candidate) => {
    exceptionProbeCalls += 1
    if (exceptionProbeCalls === 1) {
      exceptionFirstProbeStarted()
      await exceptionFirstProbeGate
      throw new Error('exception tail regression first probe failed')
    }
    if (exceptionProbeCalls === 2) {
      exceptionSecondProbeStarted()
      await exceptionSecondProbeGate
      return probeResult(candidate, false)
    }
    assert.fail(`异常尾随不得额外创建探针：${exceptionProbeCalls}`)
  })
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(exceptionSummary, settings, 'request_failure'),
    true,
    '异常尾随首轮普通 request_failure 必须进入队列'
  )
  await exceptionFirstProbeStartedGate
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(exceptionSummary, settings, 'request_failure'),
    true,
    '异常中的普通 request_failure 必须保留一次尾随探针'
  )
  releaseExceptionFirstProbe()
  assert(
    await waitForCondition(() => exceptionProbeCalls === 2, 1_000),
    `异常首轮结束后必须启动尾随探针：${JSON.stringify({
      queue: healthCheckService.getAccountHealthCheckQueueSnapshot(),
      account: repositories.findAccountSummary(exceptionAccount.id, access)
    })}`
  )
  const exceptionFence = sourceFence(exceptionAccount.id, 6)
  assert.equal(
    healthCheckService.enqueueAccountHealthCheck(exceptionSummary, settings, 'request_failure', exceptionFence),
    true,
    '异常后的尾随探针必须接纳来源 fence，不能降格为 source-only'
  )
  releaseExceptionSecondProbe()
  await waitForQueueIdle(healthCheckService)
  assert.equal(exceptionProbeCalls, 2, '异常首轮后只能运行一个普通尾随探针')
  assert.equal(
    repositories.findAccountSummary(exceptionAccount.id, access)?.status,
    'temporary_unavailable',
    '异常后的普通尾随完整 HTTP 失败必须仍能确认 temporary_unavailable'
  )
  assert.deepEqual(
    exceptionSettlements,
    [{ sourceFenceId: exceptionFence.sourceFenceId, outcome: 'unknown' }],
    '异常后的尾随来源 fence 必须以独立 unknown 结算'
  )

  console.log('账户健康检查执行生命周期回归通过：coordinator owner、异常和 request_failure 尾随来源 fence 都不会遗留或降格')
} finally {
  healthCheckService.setAccountHealthCheckProbeRunnerForTest(undefined)
  backgroundIpc.setClientSourceFenceSettlementObserverForTest(undefined)
  coordinator.setAvailabilityProbeStateStoreForTest(undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true, maxRetries: 5, retryDelay: 50 })
}

function sourceFence(accountId: string, sourceGeneration: number) {
  return {
    stateKey: `source-health-execution-${accountId}`,
    accountId,
    sourceGeneration,
    sourceFenceId: `00000000-0000-4000-8000-000000000${String(sourceGeneration).padStart(3, '0')}`,
    runtimeKey: `runtime-health-execution-${accountId}`,
    probeGeneration: sourceGeneration,
    configRevision: 1
  }
}

function probeResult(
  account: Pick<AccountSummary, 'id' | 'name' | 'providerCode' | 'providerProtocolProfileId' | 'type'>,
  success: boolean
): import('../../modules/background/account-health-check.service.js').AccountHealthCheckProbeResult {
  const statusCode = success ? 200 : 503
  return {
    result: {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      type: account.type,
      success,
      statusCode,
      errorCode: success ? undefined : 'upstream_retryable_error',
      message: success ? 'ok' : 'upstream unavailable'
    },
    upstreamAttempt: {
      accountId: account.id,
      accountName: account.name,
      providerCode: account.providerCode,
      providerProtocolProfileId: account.providerProtocolProfileId,
      upstreamUrl: 'https://example.test/v1/responses',
      status: statusCode
    },
    diagnosticCompleted: true
  }
}

async function waitForQueueIdle(
  healthCheckService: typeof import('../../modules/background/account-health-check.service.js'),
  timeoutMs = 2_000
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const snapshot = healthCheckService.getAccountHealthCheckQueueSnapshot()
    if (snapshot.pendingCount === 0 && snapshot.runningCount === 0) return
    assert(Date.now() < deadline, '健康检查队列未在零重试边界内排空')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}

async function waitForCondition(predicate: () => boolean, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return true
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  return predicate()
}
