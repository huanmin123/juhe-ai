import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import type { CodexSourceProbeFence } from '../../modules/accounts/account-health-check-trigger.js'
import {
  dispatchAccountHealthCheck,
  dispatchAccountHealthCheckWithOutcome
} from '../../modules/internal-api/account-health-check-dispatch.service.js'
import { dispatchRequestFailureAccountHealthCheck } from '../../modules/gateway/response/request-failure-health-check.js'
import {
  acquireAvailabilityProbe,
  getAvailabilityProbeState,
  releaseAvailabilityProbeForExecution
} from '../../modules/gateway/runtime/availability-probe-coordinator.js'

const originalRuntimeMode = runtimeConfig.runtimeMode
const originalPerformanceNodeRole = runtimeConfig.performanceNodeRole
const originalProcessRole = runtimeConfig.processRole
const originalStateDriver = runtimeConfig.runtimeStateDriver
const originalDispatchUrl = runtimeConfig.accountHealthCheckDispatchUrl
const originalFetch = globalThis.fetch

try {
  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.performanceNodeRole = 'gateway'
  runtimeConfig.processRole = 'server'
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.accountHealthCheckDispatchUrl = 'http://127.0.0.1:47001'

  await assertControlInFlightTrailing('control-rejected-trailing', () => new Response(null, { status: 503 }))
  await assertControlInFlightTrailing('control-throw-trailing', () => {
    throw new Error('control timeout')
  })

  globalThis.fetch = (async () => new Response(null, { status: 503 })) as typeof fetch
  await assertControlFailureSettlesSourceFence('control-rejected')

  globalThis.fetch = (async () => {
    throw new Error('control unreachable')
  }) as typeof fetch
  await assertControlFailureSettlesSourceFence('control-network-error')

  console.log('账户健康检查 control 来源 fence 回归通过：503 与网络失败都会结算 unknown，不遗留 dispatchPending')
} finally {
  globalThis.fetch = originalFetch
  runtimeConfig.runtimeMode = originalRuntimeMode
  runtimeConfig.performanceNodeRole = originalPerformanceNodeRole
  runtimeConfig.processRole = originalProcessRole
  runtimeConfig.runtimeStateDriver = originalStateDriver
  runtimeConfig.accountHealthCheckDispatchUrl = originalDispatchUrl
}

async function assertControlInFlightTrailing(
  label: string,
  result: () => Response
): Promise<void> {
  const accountId = `acct_${label}`
  let postCount = 0
  let releaseFirstPost!: () => void
  let firstPostStarted!: () => void
  const firstPostGate = new Promise<void>((resolve) => {
    releaseFirstPost = resolve
  })
  const firstPostStartedGate = new Promise<void>((resolve) => {
    firstPostStarted = resolve
  })
  globalThis.fetch = (async () => {
    postCount += 1
    if (postCount === 1) {
      firstPostStarted()
      await firstPostGate
    }
    return result()
  }) as typeof fetch

  assert.deepEqual(
    dispatchAccountHealthCheckWithOutcome(accountId, 'request_failure', 'first-trace'),
    { outcome: 'queued', decisionCode: 'queued', targetRole: 'ops-worker' },
    `${label} 首轮 control POST 必须立即受理`
  )
  await firstPostStartedGate
  const gatewayRequest = {} as Parameters<typeof dispatchRequestFailureAccountHealthCheck>[0]
  assert.equal(
    dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'gateway', accountId),
    true,
    `${label} control 在途合并也必须被请求级去重视为已受理`
  )
  assert.equal(
    dispatchRequestFailureAccountHealthCheck(gatewayRequest, 'gateway', accountId),
    false,
    `${label} control 在途合并后同一请求不得重复触发第二个账户检查`
  )
  assert.equal(
    dispatchAccountHealthCheck(accountId, 'request_failure', 'boolean-coalesced-trace'),
    true,
    `${label} 结构化 coalesced outcome 必须映射为已受理的布尔结果`
  )
  assert.deepEqual(
    dispatchAccountHealthCheckWithOutcome(accountId, 'request_failure', 'second-trace'),
    { outcome: 'coalesced', decisionCode: 'request_failure_in_flight', targetRole: 'ops-worker' },
    `${label} 在途请求失败必须只追加一个尾随 POST`
  )
  assert.deepEqual(
    dispatchAccountHealthCheckWithOutcome(accountId, 'request_failure', 'third-trace'),
    { outcome: 'coalesced', decisionCode: 'request_failure_in_flight', targetRole: 'ops-worker' },
    `${label} 重复在途请求失败不得扩展为无界 POST 队列`
  )
  releaseFirstPost()
  await waitFor(() => postCount === 2, `${label} 必须在首轮结束后恰好发出一次尾随 POST`)
  await Promise.resolve()
  await Promise.resolve()
  assert.deepEqual(
    dispatchAccountHealthCheckWithOutcome(accountId, 'request_failure', 'after-cleanup-trace'),
    { outcome: 'queued', decisionCode: 'queued', targetRole: 'ops-worker' },
    `${label} 完成尾随 POST 后必须清理 in-flight 状态，允许下一次投递`
  )
  await waitFor(() => postCount === 3, `${label} 清理后新的请求失败必须重新发起 POST`)
}

async function waitFor(condition: () => boolean, message: string): Promise<void> {
  const deadline = Date.now() + 1_000
  while (Date.now() < deadline) {
    if (condition()) return
    await new Promise<void>((resolve) => setTimeout(resolve, 5))
  }
  assert.fail(message)
}

async function assertControlFailureSettlesSourceFence(label: string): Promise<void> {
  const accountId = `acct_${label}`
  const acquired = await acquireAvailabilityProbe({
    accountRuntimeScope: `runtime_${label}`,
    probeKind: 'account_health_check',
    configRevision: 1,
    executionRole: 'source_dispatch',
    sourceFence: {
      stateKey: `source_${label}`,
      accountId,
      sourceGeneration: 1,
      sourceFenceId: label === 'control-rejected'
        ? '00000000-0000-4000-8000-000000000101'
        : '00000000-0000-4000-8000-000000000102'
    }
  })
  assert.equal(acquired.disposition, 'owner', `${label} 必须先创建 source owner`)
  if (acquired.disposition !== 'owner') return

  const sourceFence: CodexSourceProbeFence = {
    stateKey: `source_${label}`,
    accountId,
    sourceGeneration: 1,
    sourceFenceId: label === 'control-rejected'
      ? '00000000-0000-4000-8000-000000000101'
      : '00000000-0000-4000-8000-000000000102',
    runtimeKey: acquired.runtimeKey,
    probeGeneration: acquired.generation,
    configRevision: 1
  }
  assert.equal(await releaseAvailabilityProbeForExecution({
    runtimeKey: acquired.runtimeKey,
    generation: acquired.generation,
    ownerToken: acquired.ownerToken
  }), true, `${label} 必须先进入 dispatchPending，模拟来源 owner 已释放执行权`)

  assert.equal(
    dispatchAccountHealthCheckWithOutcome(accountId, 'request_failure', undefined, sourceFence).outcome,
    'queued',
    `${label} 的网关热路径应保持非阻塞 queued 语义`
  )

  for (let attempt = 0; attempt < 40; attempt += 1) {
    const state = await getAvailabilityProbeState(acquired.runtimeKey)
    if (state?.outcome === 'unknown') return
    await new Promise<void>((resolve) => setTimeout(resolve, 5))
  }
  assert.fail(`${label} 的 control 投递失败必须将对应 source fence 结算为 unknown`)
}
