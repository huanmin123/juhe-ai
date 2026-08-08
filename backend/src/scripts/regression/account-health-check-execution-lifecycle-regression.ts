import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { createRuntimeProbeStateStore } from '../../shared/runtime-probe-state-store.js'

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

  console.log('账户健康检查执行生命周期回归通过：coordinator 早期失败不会遗留 source execution')
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

async function waitForQueueIdle(healthCheckService: typeof import('../../modules/background/account-health-check.service.js')): Promise<void> {
  const deadline = Date.now() + 2_000
  while (true) {
    const snapshot = healthCheckService.getAccountHealthCheckQueueSnapshot()
    if (snapshot.pendingCount === 0 && snapshot.runningCount === 0) return
    assert(Date.now() < deadline, '健康检查队列未在零重试边界内排空')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}
