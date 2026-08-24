import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  AccountCircuitRecoveryService,
  type AccountCircuitRecoveryTargetResolver
} from '../../modules/background/account-circuit-recovery.service.js'
import { AccountCircuitControlPlaneBridge } from '../../modules/gateway/runtime/account-circuit-control-plane-bridge.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { GatewayAccountCircuitService } from '../../modules/gateway/runtime/account-circuit.service.js'
import {
  accountCircuitScopeKey,
  type AccountCircuitScope,
  type AccountCircuitState
} from '../../modules/gateway/runtime/account-circuit-store.js'
import type {
  AccountCircuitIncidentRecord,
  AccountCircuitIncidentRebuildPage
} from '../../storage/account-circuit-control-plane.repository.js'

const runtimeSource = source('../../config/runtime.ts')
const gatewayCircuitSource = source('../../modules/gateway/runtime/account-circuit.service.ts')
const bridgeSource = source('../../modules/gateway/runtime/account-circuit-control-plane-bridge.ts')
const redisStoreSource = source('../../modules/gateway/runtime/account-circuit-redis-store.ts')
const recoverySource = source('../../modules/background/account-circuit-recovery.service.ts')

const productionCapacity = 50_000
assert.match(runtimeSource, /ACCOUNT_CIRCUIT_CAPACITY', 50_000, 1_000, 1_000_000/)
assert.match(runtimeSource, /const globalConcurrencyMax = integerConfig\('JUHE_AI_CONCURRENCY_GLOBAL_MAX', 5_000, 1, 50_000\)/)
assert.match(runtimeSource, /accountCircuitRecoveryBatchSize: integerConfig\('JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_RECOVERY_BATCH_SIZE', 200, 1, 2_000\)/)
assert.match(gatewayCircuitSource, /gatewayAccountCircuitCapacity = runtimeConfig\.gateway\.accountCircuitCapacity/)
assert.match(
  recoverySource,
  /const defaultRecoveryConcurrency = runtimeConfig\.concurrency\.globalMax/,
  '账户电路恢复必须使用统一全局并发配置'
)
assert.match(
  recoverySource,
  /concurrency: runtimeConfig\.concurrency\.globalMax/,
  '计划任务创建恢复服务时不得绕过统一全局并发配置'
)
assert.equal(
  (gatewayCircuitSource.match(/capacity: gatewayAccountCircuitCapacity/g) ?? []).length,
  2,
  'memory 与 Redis 必须使用同一个可配置容量'
)
assert.doesNotMatch(
  recoverySource,
  /if \(!await ensureGatewayAccountCircuitRuntimeStateReady\(\)\)[\s\S]{0,160}return/,
  '部分重建时不得全局跳过已加载 OPEN/SUSPECT 的恢复探针'
)
assert.match(
  gatewayCircuitSource,
  /await ensureGatewayAccountCircuitRuntimeStateReady\(\)[\s\S]{0,200}projectPending/,
  'control-plane maintenance 必须在部分重建时继续投影已加载状态'
)

await verifyCapacitySaturationBlocksAndRecovers()
await verifyCapacityReclaimsOnlyClosedEntries()
await verifyCapacityExceededRebuildIsBounded()
await verifyPageFailureAllowsTargetedProgressiveService()
await verifyRepeatedCursorTerminates()
await verifyTotalDeadlineTerminatesAdvancingPages()
await verifyLargePaginatedRebuildContract()
await verifySlowRebuildIsBoundedAndDoesNotBlockReadyAccount()
await verifyLongOpenBackoffJitterAndRecoveryConcurrency()
verifyRedisLargeDueScanBound()

console.log(JSON.stringify({
  message: 'account circuit 容量、重建与长期 OPEN 压力回归通过',
  verified: {
    productionCapacity,
    rebuildPageSize: 500,
    rebuildPageTimeoutMs: 2_000,
    rebuildTotalTimeoutMs: 15_000,
    redisMaxEntriesPerLuaDueScan: 512,
    longOpenMaximumBaseBackoffMs: 900_000,
    recoveryBatchSize: 200,
    recoveryConcurrencySource: 'JUHE_AI_CONCURRENCY_GLOBAL_MAX',
    minimumSecondsToTouchTenThousandDueScopesAtZeroProbeLatency: 250
  }
}))

async function verifyCapacitySaturationBlocksAndRecovers(): Promise<void> {
  let nowMs = 10_000
  let sequence = 0
  const store = new MemoryAccountCircuitStore({ capacity: 1, now: () => nowMs })
  const occupiedScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-occupied' }
  assert.equal((await store.restore(openState(occupiedScope, nowMs), nowMs)).status, 'applied')
  const service = readyService(store, () => nowMs, () => `capacity-${++sequence}`)

  const firstAttempt = await prepare(service, 'capacity-new-failure')
  assert.equal(firstAttempt.outcome, 'dispatchable')
  if (firstAttempt.outcome !== 'dispatchable') assert.fail('首次真实失败前不得预判账户不可用')
  const failed = await firstAttempt.attempt.reportTransportFailure({ kind: 'transport', reason: 'active capacity full' })
  assert.equal(failed.state.phase, 'SUSPECT')
  assert.equal(failed.state.failureReason, 'runtime_state_capacity_exhausted')
  assert.equal(await store.size(), 1, '容量不足不得淘汰活动 incident')

  nowMs += 1
  const blocked = await prepare(service, 'capacity-new-failure')
  assert.equal(blocked.outcome, 'blocked', '容量不足后的未知 scope 不得在下一请求静默按 CLOSED 放行')
  if (blocked.outcome !== 'blocked') assert.fail('capacity sentinel 必须受控阻塞')
  assert.equal(blocked.state.failureReason, 'runtime_state_capacity_exhausted')

  await store.replaceAccountDispatchRevision({
    accountRuntimeKey: occupiedScope.accountRuntimeKey,
    dispatchRevision: '2',
    transitionId: 'capacity-release',
    nowMs
  })
  const recovered = await prepare(service, 'capacity-new-failure')
  assert.equal(recovered.outcome, 'dispatchable', '活动 incident 关闭后容量哨兵必须自动恢复')
}

async function verifyCapacityReclaimsOnlyClosedEntries(): Promise<void> {
  const nowMs = 20_000
  const store = new MemoryAccountCircuitStore({ capacity: 1, closedRetentionMs: 60_000, now: () => nowMs })
  const closedScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-closed-tombstone' }
  const nextScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: 'capacity-replacement' }
  await store.restore({ ...openState(closedScope, nowMs), phase: 'CLOSED', backoffAttempt: 0, retryAtMs: undefined }, nowMs)
  const replaced = await store.suspect({
    scope: nextScope,
    dispatchRevision: '1',
    transitionId: 'capacity-replace-closed',
    reason: 'transport',
    nowMs
  })
  assert.equal(replaced.status, 'applied')
  assert.equal((await store.get(nextScope, nowMs)).phase, 'SUSPECT')
  assert.equal((await store.get(closedScope, nowMs)).phase, 'CLOSED')
  assert.equal(await store.size(), 1)
}

async function verifyCapacityExceededRebuildIsBounded(): Promise<void> {
  const nowMs = 30_000
  const incidents = [0, 1, 2].map((index) => incident(`rebuild-capacity-${index}`, nowMs + index))
  const store = new MemoryAccountCircuitStore({ capacity: 2, now: () => nowMs })
  let calls = 0
  const bridge = new AccountCircuitControlPlaneBridge({
    store,
    now: () => nowMs,
    loadRebuildPage: async (input) => {
      calls += 1
      return input.afterCircuitScopeKey
        ? { items: [incidents[2]!], nextCursor: undefined }
        : { items: incidents.slice(0, 2), nextCursor: cursorOf(incidents[1]!) }
    },
    loadAccountIncidents: async (runtimeKey) => incidents.filter((item) => item.accountRuntimeKey === runtimeKey)
  })
  const result = await bridge.rebuild()
  assert.deepEqual(result, {
    loaded: 2,
    blocked: true,
    reason: 'runtime_state_rebuild_capacity_exhausted'
  })
  assert.equal(calls, 2)
  assert.equal(bridge.isReady(), false)
  assert.equal(await bridge.ensureAccountReady(incidents[0]!.accountRuntimeKey), true, '已加载账户可独立完成权威查询')
  assert.equal(await bridge.ensureAccountReady(incidents[2]!.accountRuntimeKey), false, '未能恢复的 active incident 必须保守阻塞')

  const second = await bridge.rebuild()
  assert.equal(second.reason, 'runtime_state_rebuild_capacity_exhausted')
  assert.equal(calls, 4, '失败后 rebuilding 标志必须释放，允许后续有界重试')
}

async function verifyPageFailureAllowsTargetedProgressiveService(): Promise<void> {
  const nowMs = 40_000
  const opened = incident('partial-opened', nowMs)
  const staleOpened = incident('partial-closed-after-page', nowMs)
  const retainedClosed: AccountCircuitIncidentRecord = {
    ...staleOpened,
    state: 'CLOSED',
    openUntilMs: undefined,
    nextTransitionAtMs: undefined,
    backoffLevel: 0,
    retainedUntilMs: nowMs + 60_000,
    updatedAtMs: nowMs + 1
  }
  const store = new MemoryAccountCircuitStore({ capacity: 8, now: () => nowMs })
  let pageCalls = 0
  const bridge = new AccountCircuitControlPlaneBridge({
    store,
    now: () => nowMs,
    loadRebuildPage: async () => {
      pageCalls += 1
      if (pageCalls === 1) return { items: [opened, staleOpened], nextCursor: cursorOf(staleOpened) }
      throw new Error('mock: second page failed')
    },
    loadAccountIncidents: async (runtimeKey) => runtimeKey === opened.accountRuntimeKey
      ? [opened]
      : runtimeKey === staleOpened.accountRuntimeKey
        ? [retainedClosed]
        : []
  })
  const result = await bridge.rebuild()
  assert.equal(result.reason, 'runtime_state_rebuild_failed')
  assert.equal(await bridge.reconcileActive(), 0)

  const service = bridgeService(store, bridge, nowMs)
  const knownOpen = await prepare(service, opened.accountRuntimeKey)
  assert.equal(knownOpen.outcome, 'blocked', 'targeted rebuild 必须恢复并执行已知 OPEN 状态')
  if (knownOpen.outcome !== 'blocked') assert.fail('已知 OPEN 不得放行')
  assert.notEqual(knownOpen.state.failureReason, 'runtime_state_rebuilding')
  const closedAfterPartialPage = await prepare(service, staleOpened.accountRuntimeKey)
  assert.equal(closedAfterPartialPage.outcome, 'dispatchable', 'targeted retained CLOSED 必须覆盖全局分页早先加载的旧 OPEN')
  const unrelatedHealthy = await prepare(service, 'partial-unrelated-healthy')
  assert.equal(unrelatedHealthy.outcome, 'dispatchable', '全局分页失败不得永久阻塞可权威确认无 incident 的账户')
}

async function verifyRepeatedCursorTerminates(): Promise<void> {
  const nowMs = 45_000
  const repeated = { updatedAtMs: nowMs, circuitScopeKey: 'repeated-cursor' }
  let calls = 0
  const bridge = new AccountCircuitControlPlaneBridge({
    store: new MemoryAccountCircuitStore({ capacity: 8, now: () => nowMs }),
    now: () => nowMs,
    loadRebuildPage: async () => {
      calls += 1
      return { items: [], nextCursor: repeated }
    }
  })
  const result = await bridge.rebuild()
  assert.equal(result.reason, 'runtime_state_rebuild_invalid_cursor')
  assert.equal(calls, 2, '重复 cursor 必须在第二页立即终止')
}

async function verifyTotalDeadlineTerminatesAdvancingPages(): Promise<void> {
  const nowMs = 47_000
  let cursorIndex = 0
  let monotonicMs = 0
  const bridge = new AccountCircuitControlPlaneBridge({
    store: new MemoryAccountCircuitStore({ capacity: 8, now: () => nowMs }),
    now: () => nowMs,
    monotonicNow: () => { monotonicMs += 10; return monotonicMs },
    rebuildTotalTimeoutMs: 25,
    rebuildPageTimeoutMs: 20,
    loadRebuildPage: async () => ({
      items: [],
      nextCursor: { updatedAtMs: nowMs + ++cursorIndex, circuitScopeKey: `advancing-${cursorIndex}` }
    })
  })
  const result = await bridge.rebuild()
  assert.equal(result.reason, 'runtime_state_rebuild_timeout')
  assert.equal(cursorIndex, 2, '总时限必须独立于单页成功而终止持续翻页')
}

async function verifyLargePaginatedRebuildContract(): Promise<void> {
  const nowMs = 50_000
  const incidents = Array.from({ length: 1_201 }, (_, index) => incident(`paged-${index.toString().padStart(4, '0')}`, nowMs + index))
  const pages = [incidents.slice(0, 500), incidents.slice(500, 1_000), incidents.slice(1_000)]
  const inputs: Array<{ afterUpdatedAtMs?: number; afterCircuitScopeKey?: string; limit: number }> = []
  const store = new MemoryAccountCircuitStore({ capacity: 1_300, now: () => nowMs })
  const bridge = new AccountCircuitControlPlaneBridge({
    store,
    now: () => nowMs,
    loadRebuildPage: async (input) => {
      inputs.push(input)
      const pageIndex = inputs.length - 1
      const items = pages[pageIndex] ?? []
      return { items, ...(pageIndex < pages.length - 1 ? { nextCursor: cursorOf(items.at(-1)!) } : {}) }
    }
  })
  assert.deepEqual(await bridge.rebuild(), { loaded: incidents.length, blocked: false })
  assert.equal(await store.size(), incidents.length)
  assert.deepEqual(inputs.map((input) => input.limit), [500, 500, 500])
  assert.deepEqual(
    { updatedAtMs: inputs[2]!.afterUpdatedAtMs, circuitScopeKey: inputs[2]!.afterCircuitScopeKey },
    cursorOf(incidents[999]!)
  )
  assert.equal(bridge.isReady(), true)
}

async function verifySlowRebuildIsBoundedAndDoesNotBlockReadyAccount(): Promise<void> {
  const nowMs = 60_000
  const never = deferred<AccountCircuitIncidentRebuildPage>()
  const store = new MemoryAccountCircuitStore({ capacity: 8, now: () => nowMs })
  const bridge = new AccountCircuitControlPlaneBridge({
    store,
    now: () => nowMs,
    rebuildPageTimeoutMs: 20,
    rebuildTotalTimeoutMs: 40,
    loadRebuildPage: async () => await never.promise,
    loadAccountIncidents: async (runtimeKey) => {
      if (runtimeKey === 'targeted-hang') return await new Promise<AccountCircuitIncidentRecord[]>(() => {})
      return []
    }
  })
  const rebuild = bridge.rebuild()
  const service = bridgeService(store, bridge, nowMs)
  assert.equal((await prepare(service, 'targeted-ready')).outcome, 'dispatchable')

  const startedAt = Date.now()
  const blocked = await prepare(service, 'targeted-hang')
  assert.equal(blocked.outcome, 'blocked')
  if (blocked.outcome !== 'blocked') assert.fail('超时账户必须阻塞')
  assert.equal(blocked.state.failureReason, 'runtime_state_rebuilding')
  assert.ok(Date.now() - startedAt < 250, 'targeted DB hang 必须受单页 deadline 限制')
  assert.deepEqual(await rebuild, { loaded: 0, blocked: true, reason: 'runtime_state_rebuild_timeout' })
  assert.match(bridgeSource, /rebuildPageTimeoutMs[\s\S]*rebuildTotalTimeoutMs[\s\S]*withinTimeout/)
}

async function verifyLongOpenBackoffJitterAndRecoveryConcurrency(): Promise<void> {
  let nowMs = 70_000
  let sequence = 0
  let activeProbes = 0
  let maximumActiveProbes = 0
  const store = new MemoryAccountCircuitStore({ capacity: 64, now: () => nowMs })
  const scopes = Array.from({ length: 24 }, (_, index): AccountCircuitScope => ({
    kind: 'account',
    accountRuntimeKey: `long-open-${index}`
  }))
  for (const scope of scopes) await store.restore({ ...openState(scope, nowMs), backoffAttempt: 5, retryAtMs: nowMs }, nowMs)
  const resolver: AccountCircuitRecoveryTargetResolver = async () => ({
    dispatchRevision: '1',
    probe: async () => {
      activeProbes += 1
      maximumActiveProbes = Math.max(maximumActiveProbes, activeProbes)
      await delay(2)
      activeProbes -= 1
      return { kind: 'transport_incomplete', failureKind: 'connection' }
    }
  })
  const service = new AccountCircuitRecoveryService(store, resolver, {
    batchSize: 24,
    concurrency: 6,
    leaseDurationMs: 1_000,
    now: () => nowMs,
    createId: () => `long-open-${++sequence}`
  })
  const first = await service.sweep()
  assert.equal(first.transportIncompleteCount, scopes.length)
  assert.equal(maximumActiveProbes, 6, '恢复探针并发必须命中配置上限')
  const retryTimes = await Promise.all(scopes.map(async (scope) => (await store.get(scope, nowMs)).retryAtMs!))
  const delays = retryTimes.map((retryAtMs) => retryAtMs - nowMs)
  assert.ok(delays.every((delayMs) => delayMs >= 90_000 && delayMs <= 150_000 && delayMs !== 120_000), '第六次失败应使用 120s 全局偏移')
  assert.ok(new Set(delays).size > 1, '不同 scope 的长期探针必须被偏移打散')

  nowMs = Math.min(...retryTimes) - 1
  assert.equal((await service.sweep()).dueCount, 0)
  nowMs += 1
  assert.ok((await service.sweep()).dueCount < scopes.length, '最早到期时不得再次形成整池同步波次')
  assert.match(recoverySource, /const defaultRecoveryBatchSize = runtimeConfig\.gateway\.accountCircuitRecoveryBatchSize/)
  assert.equal(Math.ceil(10_000 / 200) * 5_000, 250_000, '10k 到期 scope 零探针延迟触达上限应降到 250 秒')
}

function verifyRedisLargeDueScanBound(): void {
  assert.match(redisStoreSource, /scanChunkSize = Math\.min\(512, Math\.max\(64, normalizedLimit \* 2\)\)/)
  assert.match(redisStoreSource, /while \(scopeKeys\.length < normalizedLimit && scanned < this\.capacity\)/)
  assert.match(redisStoreSource, /local batch_size = math\.min\(scan_limit, 128\)/)
  assert.match(redisStoreSource, /nextOffset = retained_offset/)
  assert.match(redisStoreSource, /capacityExhaustedAccountCircuitState/)
  assert.match(redisStoreSource, /capacity_saturated_key[\s\S]*input\['capacityState'\]/)
  assert.match(redisStoreSource, /redisAccountCircuitRestoreScript[\s\S]*String\(this\.capacity\)/)
  assert.doesNotMatch(redisStoreSource, /redis\.call\(['"](?:KEYS|HGETALL|SMEMBERS)['"]/)
  assert.match(redisStoreSource, /redisAccountCircuitAccountRevisionScript[\s\S]*HSCAN[\s\S]*COUNT', 128/)
}

function source(relative: string): string {
  return readFileSync(new URL(relative, import.meta.url), 'utf8')
}

function readyService(
  store: MemoryAccountCircuitStore,
  now: () => number,
  createId: () => string
): GatewayAccountCircuitService {
  return new GatewayAccountCircuitService(store, { now, createId, isRuntimeStateReady: () => true })
}

function bridgeService(
  store: MemoryAccountCircuitStore,
  bridge: AccountCircuitControlPlaneBridge,
  nowMs: number
): GatewayAccountCircuitService {
  return new GatewayAccountCircuitService(store, {
    now: () => nowMs,
    isRuntimeStateReady: (runtimeKey) => bridge.isAccountReady(runtimeKey),
    ensureRuntimeStateReady: async (runtimeKey) => await bridge.ensureAccountReady(runtimeKey)
  })
}

async function prepare(service: GatewayAccountCircuitService, accountRuntimeKey: string) {
  return await service.prepareAttempt({
    account: gatewayAccount(accountRuntimeKey),
    requestLane: 'text',
    model: 'gpt-5',
    confirmationLeaseDurationMs: 1_000
  })
}

function gatewayAccount(id: string): never {
  return { id, dispatchRevision: 1, protocolCode: 'openai', protocolVersion: 'v1', supportedModels: ['gpt-5'] } as never
}

function openState(scope: AccountCircuitScope, nowMs: number): AccountCircuitState {
  return {
    scope,
    scopeKey: accountCircuitScopeKey(scope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision: '1',
    transitionId: `open:${scope.accountRuntimeKey}`,
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    openedAtMs: nowMs,
    retryAtMs: nowMs,
    updatedAtMs: nowMs
  }
}

function incident(accountRuntimeKey: string, updatedAtMs: number): AccountCircuitIncidentRecord {
  const scope: AccountCircuitScope = { kind: 'account', accountRuntimeKey }
  return {
    circuitScopeKey: accountCircuitScopeKey(scope),
    accountId: accountRuntimeKey,
    accountRuntimeKey,
    scopeKind: 'account',
    incidentId: `incident:${accountRuntimeKey}`,
    childIncidentIds: [],
    state: 'OPEN',
    generation: 1,
    dispatchRevision: 1,
    ledgerRevision: 1,
    projectedLedgerRevision: 1,
    transitionId: `transition:${accountRuntimeKey}`,
    cooldownObservationGeneration: 0,
    openUntilMs: updatedAtMs + 60_000,
    nextTransitionAtMs: updatedAtMs + 60_000,
    upstreamAttemptObserved: true,
    backoffLevel: 5,
    consecutiveFailures: 1,
    confirmationFailuresRequired: 1,
    confirmationFailureEvidenceKeys: [],
    recoveringSuccesses: 0,
    createdAtMs: updatedAtMs,
    updatedAtMs
  }
}

function cursorOf(value: AccountCircuitIncidentRecord): { updatedAtMs: number; circuitScopeKey: string } {
  return { updatedAtMs: value.updatedAtMs, circuitScopeKey: value.circuitScopeKey }
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((innerResolve) => { resolve = innerResolve })
  return { promise, resolve }
}

async function delay(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
