import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { automaticAccountProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'
import { logger } from '../../shared/logger.js'
import type {
  AccountApiKeyTransientMutationResult,
  AccountApiKeyTransientDispatchState,
  AccountApiKeyTransientState,
  AccountApiKeyTransientStateStore,
  AccountApiKeyTransientTarget
} from '../../modules/gateway/runtime/account-api-key-transient-redis-store.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-failure-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-failure-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  apiKeyRotation,
  apiKeyEffects,
  apiKeyFailureGuard,
  apiKeyRuntimeStates
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../modules/gateway/runtime/account-api-key-effects.service.js'),
  import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
  import('../../storage/account-api-key-runtime-state.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

class RegressionAccountApiKeyTransientStore implements AccountApiKeyTransientStateStore {
  private readonly values = new Map<string, AccountApiKeyTransientState>()
  private generationSequence = 0
  private nextSuccessGate?: { promise: Promise<void>; entered: () => void }

  holdNextSuccess(promise: Promise<void>, entered: () => void): void {
    this.nextSuccessGate = { promise, entered }
  }

  async recordFailure(input: {
    target: AccountApiKeyTransientTarget
    status: 'temporary_unavailable' | 'rate_limited' | 'error'
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    const key = `${input.target.accountId}\u0000${input.target.keyFingerprint}`
    const current = this.values.get(key)
    if (!current) return { applied: false, reason: 'missing_state' }
    const currentGeneration = current.generation
    if (input.expectedGeneration !== currentGeneration) return { applied: false, reason: 'stale_generation', state: current }
    const state: AccountApiKeyTransientState = {
      schemaVersion: 1,
      ...input.target,
      generation: currentGeneration,
      lastObservedAtMs: Date.now(),
      observationKind: 'failure',
      failureCount: current?.observationKind === 'failure' ? Math.min(3, current.failureCount + 1) : 1,
      status: input.status,
      suppressUntilMs: Date.now() + 3_000
    }
    this.values.set(key, state)
    return { applied: true, reason: 'applied', state }
  }

  async recordSuccess(input: { target: AccountApiKeyTransientTarget; expectedGeneration: string }): Promise<AccountApiKeyTransientMutationResult> {
    const gate = this.nextSuccessGate
    this.nextSuccessGate = undefined
    if (gate) {
      gate.entered()
      await gate.promise
    }
    const key = `${input.target.accountId}\u0000${input.target.keyFingerprint}`
    const current = this.values.get(key)
    if (!current) return { applied: false, reason: 'missing_state' }
    const currentGeneration = current.generation
    if (input.expectedGeneration !== currentGeneration) return { applied: false, reason: 'stale_generation', state: current }
    const state: AccountApiKeyTransientState = {
      schemaVersion: 1,
      ...input.target,
      generation: this.nextGeneration(),
      lastObservedAtMs: Date.now(),
      observationKind: 'success',
      failureCount: 0
    }
    this.values.set(key, state)
    return { applied: true, reason: 'applied', state }
  }

  async loadMany(accountId: string, keyFingerprints: string[]): Promise<AccountApiKeyTransientDispatchState[]> {
    return keyFingerprints
      .map((fingerprint) => {
        const key = `${accountId}\u0000${fingerprint}`
        const current = this.values.get(key)
        if (current) return current
        const state: AccountApiKeyTransientState = {
          schemaVersion: 1,
          accountId,
          keyFingerprint: fingerprint,
          generation: this.nextGeneration(),
          lastObservedAtMs: Date.now(),
          observationKind: 'success',
          failureCount: 0
        }
        this.values.set(key, state)
        return state
      })
      .map((state) => ({
        state,
        suppressed: state.observationKind === 'failure'
          && state.suppressUntilMs !== undefined
          && state.suppressUntilMs > Date.now()
      }))
  }

  private nextGeneration(): string {
    this.generationSequence += 1
    return `generation-${this.generationSequence}`
  }
}

try {
  const group = repositories.createGroup({
    name: 'Key 失败保护回归分组',
    providerCode: 'gpt',
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'Key 失败保护多 Key 账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    credentials: {
      api_key: 'sk-failure-guard-a',
      api_keys: ['sk-failure-guard-a', 'sk-failure-guard-b'],
      api_key_strategy: 'round_robin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  assert(repositories.recordAccountHealthCheckSuccess(account.id, {
    intervalHours: 24,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), 'Key 失败保护测试账户必须先通过协议健康检查并激活')
  const gatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, access.systemAccountId, { ignoreAvailability: true })
  assert(gatewayAccount, '应能读取测试网关账户')

  const selectedA = {
    ...gatewayAccount,
    apiKey: 'sk-failure-guard-a',
    selectedApiKeyFingerprint: apiKeyRotation.fingerprintAccountApiKey('sk-failure-guard-a'),
    selectedApiKeyIndex: 0
  }
  const selectedB = {
    ...gatewayAccount,
    apiKey: 'sk-failure-guard-b',
    selectedApiKeyFingerprint: apiKeyRotation.fingerprintAccountApiKey('sk-failure-guard-b'),
    selectedApiKeyIndex: 1
  }

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  for (let index = 0; index < 8; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '同一来源高并发波动',
      trafficSource: 'gateway',
      clientIp: '198.51.100.10',
      apiKeyId: 'gateway-key-a',
      source: 'same_ip_regression'
    })
  }
  await delay(50)
  assert.equal(runtimeRows(account.id).length, 0, '同一 IP 连续失败不应写入全局 Key 运行态')
  assert.equal(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).length,
    1,
    '同一 IP 失败仍应产生进程内短避让，避免当前进程持续打同一个 Key'
  )
  const gatewayFailureCount = apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()[0]?.localFailureCount
  for (const trafficSource of [undefined, 'manual_account_test', 'hybrid_scoring', 'hybrid_quality_scoring'] as const) {
    await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'error',
      trafficSource,
      source: 'unauthorized_source_failure_regression'
    })
    apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
      source: 'unauthorized_source_success_regression',
      trafficSource
    })
    assert.equal(
      apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()[0]?.localFailureCount,
      gatewayFailureCount,
      `${String(trafficSource)} 不得增加或清除 Key 进程内避让`
    )
    assert.equal(runtimeRows(account.id).length, 0, `${String(trafficSource)} 不得写 Key 持久状态`)
  }
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'error',
    trafficSource: 'manual_account_test',
    mutationContext: {
      authority: 'explicit_user_policy',
      trafficSource: 'gateway'
    },
    source: 'mismatched_authority_failure_regression'
  })
  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'mismatched_authority_success_regression',
    trafficSource: 'manual_account_test',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'complete_success'
    }
  })
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    trafficSource: 'gateway',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'upstream_failure'
    },
    source: 'reverse_mismatched_authority_failure_regression'
  })
  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'reverse_mismatched_authority_success_regression',
    trafficSource: 'gateway',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'complete_success'
    }
  })
  assert.equal(
    apiKeyFailureGuard.getGatewayAccountApiKeyFailureGuardSnapshotForTest()[0]?.localFailureCount,
    gatewayFailureCount,
    'trafficSource 与 mutationContext 不一致时不得产生 Key 状态副作用'
  )
  assert.equal(runtimeRows(account.id).length, 0, '伪造或错配 mutationContext 不得绕过持久状态边界')

  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'same_ip_recovered',
    trafficSource: 'gateway'
  })
  assert.equal(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).length,
    0,
    '真实成功应清理 Key 失败保护的本地短避让'
  )

  const staleTransportObservationEpoch = apiKeyEffects.captureGatewayAccountApiKeyFailureObservation(selectedA)
  assert(staleTransportObservationEpoch, 'transport 失败待兄弟 Key 确认前必须取得本地 observation epoch')
  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'newer_protocol_success_before_delayed_failure',
    trafficSource: 'gateway'
  })
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    errorMessage: '迟到的旧 transport failure',
    observationEpoch: staleTransportObservationEpoch,
    trafficSource: 'gateway',
    source: 'delayed_same_account_key_failure_confirmation'
  })
  assert.equal(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).length,
    0,
    '同 fingerprint 较新的协议成功必须 fence 掉迟到的旧 transport failure'
  )
  const currentTransportObservationEpoch = apiKeyEffects.captureGatewayAccountApiKeyFailureObservation(selectedA)
  assert(currentTransportObservationEpoch, '新的 transport failure 必须取得新的 observation epoch')
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    errorMessage: '当前代次 transport failure 已由兄弟 Key 成功确认',
    observationEpoch: currentTransportObservationEpoch,
    trafficSource: 'gateway',
    source: 'current_same_account_key_failure_confirmation'
  })
  assert.deepEqual(
    apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(account.id).map((state) => state.keyFingerprint),
    [selectedA.selectedApiKeyFingerprint],
    '当前 observation epoch 的确认失败只能短暂隔离对应 fingerprint'
  )
  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'current_transport_observation_recovered',
    trafficSource: 'gateway'
  })

  await apiKeyEffects.flushGatewayAccountApiKeySuccessWritesForTest()
  assert.equal(runtimeRows(account.id).length, 0, '普通网关成功只能清理短避让，不得创建或恢复 Key 持久状态')
  const successCountBeforeBurst = runtimeSuccessCount(account.id, selectedA.selectedApiKeyFingerprint)
  for (let index = 0; index < 100; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
      source: 'success_burst_coalescing',
      trafficSource: 'cooldown_retest',
      mutationContext: {
        authority: 'automatic_probe',
        trafficSource: 'cooldown_retest',
        probeOutcome: 'complete_success'
      }
    })
  }
  await apiKeyEffects.flushGatewayAccountApiKeySuccessWritesForTest()
  assert.equal(
    runtimeSuccessCount(account.id, selectedA.selectedApiKeyFingerprint),
    successCountBeforeBurst + 1,
    '同 Key 成功风暴必须按最新 observedAt 尾随合并，不能向 DB service 放大为逐请求写'
  )
  const staleActiveSnapshot = { ...selectedA, apiKeyRuntimeStates: [] }
  assert.equal(apiKeyRuntimeStates.recordAccountApiKeyRuntimeFailure({
    account: selectedA,
    status: 'temporary_unavailable',
    errorCode: 'external_failure_after_cached_success',
    observedAt: new Date().toISOString()
  }).changed, true)
  await delay(2)
  apiKeyEffects.recordGatewayAccountApiKeySuccess(staleActiveSnapshot, {
    source: 'stale_active_snapshot_recovered',
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'complete_success'
    }
  })
  await apiKeyEffects.flushGatewayAccountApiKeySuccessWritesForTest()
  assert.equal(
    runtimeStatus(account.id, selectedA.selectedApiKeyFingerprint),
    'active',
    '旧 active 快照的新成功不得被近期成功节流跳过，必须恢复其后到达的失败状态'
  )

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  for (let index = 0; index < 4; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '第一来源失败',
      trafficSource: 'gateway',
      clientIp: '198.51.100.20',
      apiKeyId: 'gateway-key-a',
      source: 'storm_pending_regression'
    })
  }
  await delay(400)
  assertNoPersistedFailure(account.id, selectedA.selectedApiKeyFingerprint, '网关失败不应把已恢复的 Key 写成全局不可用')

  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: '第二来源确认失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.21',
    apiKeyId: 'gateway-key-b',
    source: 'storm_confirmed_regression'
  })
  await delay(50)
  assertNoPersistedFailure(account.id, selectedA.selectedApiKeyFingerprint, '跨 IP 失败也不应写入全局 Key 不可用')

  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedA, {
    source: 'storm_recovered',
    trafficSource: 'gateway'
  })
  for (let index = 0; index < 4; index += 1) {
    apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
      status: 'temporary_unavailable',
      statusCode: 503,
      errorMessage: '成功后的第一来源失败',
      trafficSource: 'gateway',
      clientIp: '198.51.100.40',
      apiKeyId: 'gateway-key-a',
      source: 'recent_success_regression'
    })
  }
  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: '成功后的第二来源失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.41',
    apiKeyId: 'gateway-key-b',
    source: 'recent_success_regression'
  })
  await delay(50)
  assertNoPersistedFailure(account.id, selectedA.selectedApiKeyFingerprint, '近期真实成功后不应因为网关失败写成全局不可用')

  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedB, {
    status: 'error',
    statusCode: 401,
    errorMessage: '错误策略确认 Key 失效',
    trafficSource: 'gateway',
    clientIp: '198.51.100.30',
    apiKeyId: 'gateway-key-a',
    source: 'policy_error_regression'
  })
  await delay(50)
  assert.equal(runtimeStatus(account.id, selectedB.selectedApiKeyFingerprint), undefined, '网关 error 状态也不能直接写成全局 Key 错误')

  await apiKeyEffects.recordGatewayAccountApiKeyFailure(selectedB, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorCode: 'confirmed_probe_failure',
    errorMessage: '后台确认探针失败',
    traceId: 'trace-confirmed-probe',
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'upstream_failure'
    },
    source: 'confirmed_probe_regression'
  })
  assert.equal(runtimeTraceId(account.id, selectedB.selectedApiKeyFingerprint), 'trace-confirmed-probe', '确认探针失败的 traceId 应通过网关副作用写入 Key 运行态')

  forceApiKeyRuntimeProbeDue(account.id, selectedB.selectedApiKeyFingerprint)
  const neutralProbeCandidate = apiKeyRuntimeStates.listAccountApiKeyRuntimeStatesDueForProbe(10)
    .find((candidate) => candidate.accountId === account.id && candidate.keyFingerprint === selectedB.selectedApiKeyFingerprint)
  assert(neutralProbeCandidate, '未知探针保持测试必须取得当前 Key 的 claim')
  const beforeNeutralProbe = runtimeStateDetail(account.id, selectedB.selectedApiKeyFingerprint)
  assert.equal(automaticAccountProbeOutcome({ success: false }, {
    upstreamAttempt: {
      upstreamUrl: 'https://mock.invalid/v1/chat/completions',
      status: 599
    }
  }), 'framing_complete_neutral', '任意完整 HTTP frame 但未通过协议验证时只能视为中性探针')
  assert.equal(apiKeyRuntimeStates.deferAccountApiKeyRuntimeProbe({
    account: selectedB,
    expectedStatus: neutralProbeCandidate.status,
    expectedNextProbeAt: neutralProbeCandidate.nextProbeAt,
    expectedStateUpdatedAt: neutralProbeCandidate.stateUpdatedAt,
    expectedProbeClaimToken: neutralProbeCandidate.probeClaimToken,
    expectedAccountConfigRevision: neutralProbeCandidate.accountConfigRevision,
    delaySeconds: 60,
    observedAt: new Date().toISOString()
  }).changed, true, '中性/未知探针必须只顺延当前代次的下一次复测')
  const afterNeutralProbe = runtimeStateDetail(account.id, selectedB.selectedApiKeyFingerprint)
  assert.equal(afterNeutralProbe?.status, 'temporary_unavailable', '中性探针不得恢复坏 Key')
  assert.equal(afterNeutralProbe?.failure_count, beforeNeutralProbe?.failure_count, '中性探针不得增加失败次数')
  assert.equal(afterNeutralProbe?.last_trace_id, 'trace-confirmed-probe', '中性探针不得覆盖最近真实 transport 诊断')
  assert.equal(
    apiKeyRotation.selectAccountRuntimeApiKeyEntry({
      accountId: account.id,
      credentials: {
        api_key: 'sk-failure-guard-a',
        api_keys: ['sk-failure-guard-a', 'sk-failure-guard-b'],
        api_key_strategy: 'round_robin'
      },
      runtimeStates: runtimeSelectionStates(account.id),
      excludeFingerprints: [selectedA.selectedApiKeyFingerprint]
    }),
    undefined,
    '未知探针后坏 fingerprint 必须继续保持不可调度'
  )

  forceApiKeyRuntimeProbeDue(account.id, selectedB.selectedApiKeyFingerprint)
  const successProbeCandidate = apiKeyRuntimeStates.listAccountApiKeyRuntimeStatesDueForProbe(10)
    .find((candidate) => candidate.accountId === account.id && candidate.keyFingerprint === selectedB.selectedApiKeyFingerprint)
  assert(successProbeCandidate, '协议成功恢复测试必须取得新一代 Key claim')
  assert.equal(automaticAccountProbeOutcome({ success: true }, {
    upstreamAttempt: {
      upstreamUrl: 'https://mock.invalid/v1/chat/completions',
      status: 200
    }
  }), 'complete_success', '完整 framing 且协议验证成功才允许恢复 Key')
  const successProbeFence = {
    expectedStatus: successProbeCandidate.status,
    expectedNextProbeAt: successProbeCandidate.nextProbeAt,
    expectedStateUpdatedAt: successProbeCandidate.stateUpdatedAt,
    expectedProbeClaimToken: successProbeCandidate.probeClaimToken,
    expectedAccountConfigRevision: successProbeCandidate.accountConfigRevision
  }
  assert.equal(apiKeyRuntimeStates.recordAccountApiKeyRuntimeSuccess(selectedB, {
    ...successProbeFence,
    observedAt: new Date().toISOString()
  }).changed, true, '同代 complete_success 必须恢复 Key')
  assert.equal(runtimeStatus(account.id, selectedB.selectedApiKeyFingerprint), 'active', '协议成功探针后 Key 必须恢复 active')
  assert.equal(apiKeyRuntimeStates.recordAccountApiKeyRuntimeFailure({
    account: selectedB,
    status: 'temporary_unavailable',
    errorCode: 'late_failure_after_probe_success',
    observedAt: new Date().toISOString(),
    ...successProbeFence
  }).changed, false, '迟到旧 failure 不得覆盖同 generation 的新 success')
  assert.equal(runtimeStatus(account.id, selectedB.selectedApiKeyFingerprint), 'active', '迟到旧 failure 后 Key 必须保持 active')
  assert.equal(
    apiKeyRotation.selectAccountRuntimeApiKeyEntry({
      accountId: account.id,
      credentials: {
        api_key: 'sk-failure-guard-a',
        api_keys: ['sk-failure-guard-a', 'sk-failure-guard-b'],
        api_key_strategy: 'round_robin'
      },
      runtimeStates: runtimeSelectionStates(account.id),
      excludeFingerprints: [selectedA.selectedApiKeyFingerprint]
    })?.fingerprint,
    selectedB.selectedApiKeyFingerprint,
    '协议成功恢复后，新请求必须能把该 fingerprint 重新纳入轮换'
  )
  apiKeyEffects.recordGatewayAccountApiKeySuccess(selectedB, {
    source: 'confirmed_probe_recovered',
    trafficSource: 'cooldown_retest',
    mutationContext: {
      authority: 'automatic_probe',
      trafficSource: 'cooldown_retest',
      probeOutcome: 'complete_success'
    }
  })
  await apiKeyEffects.flushGatewayAccountApiKeySuccessWritesForTest()
  assert.equal(runtimeTraceId(account.id, selectedB.selectedApiKeyFingerprint), undefined, '真实成功应清理 Key 最近失败 traceId')

  runtimeConfig.runtimeStateDriver = 'redis'
  const transientStore = new RegressionAccountApiKeyTransientStore()
  apiKeyFailureGuard.setGatewayAccountApiKeyTransientStateStoreForTest(transientStore)
  const redisInitialState = (await apiKeyFailureGuard.loadGatewayAccountApiKeyTransientStatesForDispatch(
    account.id,
    [selectedA.selectedApiKeyFingerprint]
  ))[0]
  assert(redisInitialState?.transientGeneration)
  const redisSelectedA = {
    ...selectedA,
    selectedApiKeyTransientGeneration: redisInitialState.transientGeneration
  }
  const redisGatewayDecision = apiKeyFailureGuard.recordGatewayAccountApiKeyFailureGuard(selectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: 'Redis 运行态下的网关短暂失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.50',
    apiKeyId: 'gateway-key-redis',
    source: 'redis_transient_regression'
  })
  assert.equal(redisGatewayDecision.persist, false, 'Redis 运行态下真实网关失败不得持久化 DB Key runtime row')
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(redisSelectedA, {
    status: 'temporary_unavailable',
    statusCode: 503,
    errorMessage: 'Redis 运行态下的网关短暂失败',
    trafficSource: 'gateway',
    clientIp: '198.51.100.50',
    apiKeyId: 'gateway-key-redis',
    source: 'redis_transient_regression'
  })
  assertNoPersistedFailure(account.id, selectedA.selectedApiKeyFingerprint, 'Redis 短避让不得降级写入 DB Key runtime row')
  const redisTransientStates = await apiKeyFailureGuard.loadGatewayAccountApiKeyTransientStatesForDispatch(
    account.id,
    [selectedA.selectedApiKeyFingerprint, selectedB.selectedApiKeyFingerprint]
  )
  assert.deepEqual(
    redisTransientStates.filter((state) => state.status !== 'active').map((state) => state.keyFingerprint),
    [selectedA.selectedApiKeyFingerprint],
    'Redis 短避让必须按完整 fingerprint 隔离，不能误伤同账户其他 Key'
  )
  let releaseRedisSuccess!: () => void
  const redisSuccessGate = new Promise<void>((resolvePromise) => {
    releaseRedisSuccess = resolvePromise
  })
  let markRedisSuccessEntered!: () => void
  const redisSuccessEntered = new Promise<void>((resolvePromise) => {
    markRedisSuccessEntered = resolvePromise
  })
  transientStore.holdNextSuccess(redisSuccessGate, markRedisSuccessEntered)
  let redisSuccessSettled = false
  const redisSuccessSettlement = apiKeyEffects.recordGatewayAccountApiKeySuccess(redisSelectedA, {
    source: 'redis_success_release_order_regression',
    trafficSource: 'gateway'
  }).then(() => {
    redisSuccessSettled = true
  })
  await redisSuccessEntered
  await Promise.resolve()
  assert.equal(redisSuccessSettled, false, '账户槽释放前的 Key 成功结算必须等待 Redis 短避让清理完成')
  assert.equal(
    (await apiKeyFailureGuard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, [selectedA.selectedApiKeyFingerprint]))[0]?.status,
    'temporary_unavailable',
    'Redis 成功清理尚未完成时，新调度仍应看到旧的短避让状态'
  )
  releaseRedisSuccess()
  await redisSuccessSettlement
  assert.equal(
    (await apiKeyFailureGuard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, [selectedA.selectedApiKeyFingerprint]))[0]?.status,
    'active',
    '真实成功结算返回时必须已清理 Redis fingerprint 短避让并保留 generation tombstone'
  )
  await apiKeyEffects.recordGatewayAccountApiKeyFailure(redisSelectedA, {
    status: 'temporary_unavailable',
    errorMessage: 'Redis 旧 generation 的迟到 transport failure',
    trafficSource: 'gateway',
    source: 'redis_stale_generation_failure_regression'
  })
  assert.equal(
    (await apiKeyFailureGuard.loadGatewayAccountApiKeyTransientStatesForDispatch(account.id, [selectedA.selectedApiKeyFingerprint]))[0]?.status,
    'active',
    'Redis 较新成功生成的 tombstone 必须 fence 掉迟到旧 generation failure'
  )
  apiKeyFailureGuard.setGatewayAccountApiKeyTransientStateStoreForTest(undefined)
  runtimeConfig.runtimeStateDriver = 'memory'

  console.log('账户内 API Key 失败保护回归通过：未知来源零副作用，网关仅短避让，授权探针可恢复')
} finally {
  apiKeyFailureGuard.setGatewayAccountApiKeyTransientStateStoreForTest(undefined)
  runtimeConfig.runtimeStateDriver = 'memory'
  apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function runtimeRows(accountId: string): Array<{ key_fingerprint: string; status: string }> {
  return databaseModule.getBusinessDatabase()
    .prepare('SELECT key_fingerprint, status FROM account_api_key_runtime_states WHERE account_id = ? ORDER BY key_index ASC')
    .all(accountId) as Array<{ key_fingerprint: string; status: string }>
}

function runtimeStatus(accountId: string, keyFingerprint: string): string | undefined {
  return runtimeRows(accountId).find((row) => row.key_fingerprint === keyFingerprint)?.status
}

function runtimeTraceId(accountId: string, keyFingerprint: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT last_trace_id FROM account_api_key_runtime_states WHERE account_id = ? AND key_fingerprint = ? LIMIT 1')
    .get(accountId, keyFingerprint) as { last_trace_id: string | null } | undefined
  return row?.last_trace_id ?? undefined
}

function runtimeSuccessCount(accountId: string, keyFingerprint: string): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT success_count FROM account_api_key_runtime_states WHERE account_id = ? AND key_fingerprint = ? LIMIT 1')
    .get(accountId, keyFingerprint) as { success_count: number } | undefined
  return row?.success_count ?? 0
}

function runtimeStateDetail(accountId: string, keyFingerprint: string): {
  status: string
  failure_count: number
  last_trace_id: string | null
} | undefined {
  return databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT status, failure_count, last_trace_id
      FROM account_api_key_runtime_states
      WHERE account_id = ?
        AND key_fingerprint = ?
      LIMIT 1
    `)
    .get(accountId, keyFingerprint) as {
      status: string
      failure_count: number
      last_trace_id: string | null
    } | undefined
}

function runtimeSelectionStates(accountId: string) {
  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT key_fingerprint, key_index, status, cooldown_until, next_probe_at
      FROM account_api_key_runtime_states
      WHERE account_id = ?
      ORDER BY key_index ASC
    `)
    .all(accountId) as Array<{
      key_fingerprint: string
      key_index: number
      status: 'active' | 'temporary_unavailable' | 'rate_limited' | 'error' | 'disabled'
      cooldown_until: string | null
      next_probe_at: string | null
    }>
  return rows.map((row) => ({
    keyFingerprint: row.key_fingerprint,
    keyIndex: row.key_index,
    status: row.status,
    cooldownUntil: row.cooldown_until ?? undefined,
    nextProbeAt: row.next_probe_at ?? undefined
  }))
}

function forceApiKeyRuntimeProbeDue(accountId: string, keyFingerprint: string): void {
  const dueAt = new Date(Date.now() - 1_000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE account_api_key_runtime_states
      SET cooldown_until = ?,
          next_probe_at = ?,
          probe_claim_token = NULL,
          probe_claimed_until = NULL,
          updated_at = ?
      WHERE account_id = ?
        AND key_fingerprint = ?
    `)
    .run(dueAt, dueAt, new Date().toISOString(), accountId, keyFingerprint)
}

function assertNoPersistedFailure(accountId: string, keyFingerprint: string, message: string): void {
  const status = runtimeStatus(accountId, keyFingerprint)
  assert.notEqual(status, 'temporary_unavailable', message)
  assert.notEqual(status, 'rate_limited', message)
  assert.notEqual(status, 'error', message)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms))
}
