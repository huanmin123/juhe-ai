import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import type { CodexSourceProbeFence } from '../../modules/accounts/account-health-check-trigger.js'
import { dispatchAccountHealthCheckWithOutcome } from '../../modules/internal-api/account-health-check-dispatch.service.js'
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
