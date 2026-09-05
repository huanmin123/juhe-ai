import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { GatewayAccountCircuitService } from '../../modules/gateway/runtime/account-circuit.service.js'
import type { AccountCircuitScope } from '../../modules/gateway/runtime/account-circuit-store.js'

const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
const capacityEnvExampleSource = readFileSync(new URL('../../../.env.capacity.example', import.meta.url), 'utf8')

assert.match(
  runtimeSource,
  /ACCOUNT_CIRCUIT_ESCALATION_DISTINCT_SCOPE_THRESHOLD',[\s\S]{0,100}3,[\s\S]{0,100}3,[\s\S]{0,100}64/,
  '运行配置必须把父级升级阈值限制为 3..64，默认 3'
)
assert.match(
  runtimeSource,
  /ACCOUNT_CIRCUIT_ESCALATION_WINDOW_MS',[\s\S]{0,100}10 \* 60_000,[\s\S]{0,100}60_000,[\s\S]{0,100}24 \* 60 \* 60_000/,
  '运行配置必须把父级升级窗口限制为 1 分钟到 24 小时，默认 10 分钟'
)
assert.match(capacityEnvExampleSource, /JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_DISTINCT_SCOPE_THRESHOLD=3/)
assert.match(capacityEnvExampleSource, /JUHE_AI_GATEWAY_ACCOUNT_CIRCUIT_ESCALATION_WINDOW_MS=600000/)

let now = 10_000
let id = 0

await assertServiceEscalation({
  accountId: 'default-parent-threshold',
  models: ['default-a', 'default-b', 'default-c'],
  expectedThreshold: 3,
  expectedWindowMs: 600_000
})

await assertServiceEscalation({
  accountId: 'configured-parent-threshold',
  models: ['configured-a', 'configured-b', 'configured-c', 'configured-d'],
  expectedThreshold: 4,
  expectedWindowMs: 60_000,
  serviceOptions: {
    escalationDistinctScopeThreshold: 4,
    escalationWindowMs: 60_000
  }
})

const validationStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
assert.throws(
  () => new GatewayAccountCircuitService(validationStore, { escalationDistinctScopeThreshold: 2 }),
  /3\.\.64/,
  '坏会话保护边界不得允许配置低于三个独立 scope'
)
assert.throws(
  () => new GatewayAccountCircuitService(validationStore, { escalationWindowMs: 59_999 }),
  /60000\.\.86400000/,
  '父级升级窗口不得低于一分钟'
)

console.log('account-circuit-escalation-config-regression passed')

async function assertServiceEscalation(input: {
  accountId: string
  models: string[]
  expectedThreshold: number
  expectedWindowMs: number
  serviceOptions?: {
    escalationDistinctScopeThreshold: number
    escalationWindowMs: number
  }
}): Promise<void> {
  const account = accountFixture({ id: input.accountId, supportedModels: input.models })
  const store = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
  const observedInputs: Array<{
    confirmedFailureCount: number
    distinctScopeThreshold: number
    windowMs: number
  }> = []
  const recordEvidence = store.recordProtocolModelOpenEvidence.bind(store)
  store.recordProtocolModelOpenEvidence = async (evidence) => {
    observedInputs.push({
      confirmedFailureCount: evidence.confirmedFailureCount,
      distinctScopeThreshold: evidence.distinctScopeThreshold,
      windowMs: evidence.windowMs
    })
    return recordEvidence(evidence)
  }
  const service = new GatewayAccountCircuitService(store, {
    now: () => now,
    createId: () => `${input.accountId}-${++id}`,
    ...input.serviceOptions
  })
  const parentScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: input.accountId }

  for (const [index, model] of input.models.entries()) {
    const initial = await service.prepareAttempt({
      account,
      requestLane: 'text',
      model,
      confirmationLeaseDurationMs: 30_000,
      confirmationFailuresRequired: 1
    })
    assert.equal(initial.outcome, 'dispatchable')
    if (initial.outcome !== 'dispatchable') throw new Error(`${model} 初始 attempt 不可派发`)
    const decision = await initial.attempt.reportTransportFailure({ kind: 'transport', reason: `${model} 初次断流` })
    assert.equal(decision.outcome, 'suspected', '首次 transport 失败只能进入 SUSPECT，不能自取 confirmation')
    if (decision.outcome !== 'suspected') throw new Error(`${model} 未进入 SUSPECT`)
    now = Math.max(now, decision.state.retryAtMs ?? now)
    const confirmation = await service.prepareAttempt({
      account,
      requestLane: 'text',
      model,
      confirmationLeaseDurationMs: 30_000,
      confirmationFailuresRequired: 1,
      failureEvidenceKey: (index + 1).toString(16).repeat(64)
    })
    assert.equal(confirmation.outcome, 'dispatchable')
    if (confirmation.outcome !== 'dispatchable') throw new Error(`${model} confirmation 不可派发`)
    assert.equal(confirmation.attempt.isConfirmation, true, '独立证据到期后必须取得 confirmation lease')
    await confirmation.attempt.reportTransportFailure({ kind: 'timeout', reason: `${model} confirmation 失败` })

    const parent = await store.get(parentScope, now)
    assert.equal(
      parent.phase,
      index + 1 >= input.expectedThreshold ? 'OPEN' : 'CLOSED',
      `第 ${index + 1} 个独立 child OPEN 后父级 phase 不符合配置阈值`
    )
  }

  assert.deepEqual(
    observedInputs,
    input.models.map(() => ({
      confirmedFailureCount: 1,
      distinctScopeThreshold: input.expectedThreshold,
      windowMs: input.expectedWindowMs
    })),
    'service 必须向 Store 固定贡献一个 child，并传递同一阈值与窗口'
  )
}

function accountFixture(overrides: Partial<UpstreamAccount> = {}): UpstreamAccount {
  return {
    id: 'account-escalation',
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'Escalation account',
    type: 'openai_api_key',
    status: 'active',
    concurrencyLimit: 4,
    priority: 1,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['model-a'],
    modelMappings: [],
    healthCheckModel: 'model-a',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.example.com/v1',
    apiKey: 'secret-api-key',
    apiKeys: ['secret-api-key'],
    refreshToken: 'refresh-secret',
    streamFailureCount: 0,
    credentials: { api_key: 'secret-api-key', refresh_token: 'refresh-secret' },
    ...overrides
  }
}
