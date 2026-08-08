import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { runtimeConfig } from '../../config/runtime.js'
import {
  availabilityProbeRuntimeKey,
  acquireAvailabilityProbe,
  settleAvailabilityProbe
} from '../../modules/gateway/runtime/availability-probe-coordinator.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import {
  clearCodexTurnRetryStateForTest,
  orderOpenAIAccountsByCodexTurnAvoidance,
  rememberCodexTurnStreamFailure
} from '../../modules/gateway/client-profiles/codex-turn-retry.service.js'
import { runCodexTurnAvoidanceAvailabilityProbe } from '../../modules/gateway/client-profiles/codex-turn-availability-probe.service.js'
import { resolveOpenAIGatewayClientStrategy } from '../../modules/gateway/client-profiles/strategy.js'
import { sourceBoundProbePermitsAccountHealthMutation } from '../../modules/background/account-health-check.service.js'
import { settleCodexSourceFenceFromWorker } from '../../modules/background/background-ipc.js'

runtimeConfig.runtimeStateDriver = 'memory'
clearCodexTurnRetryStateForTest()

const identity = {
  systemAccountId: 'sys_probe',
  apiKeyId: 'key_probe',
  groupId: 'group_probe',
  endpoint: 'POST /v1/responses',
  clientIp: '198.51.100.22'
}
const account = { id: 'acct_probe', name: 'acct_probe', priority: 0, configRevision: 7 } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret
const request = createRequest('turn_probe_owner')
const strategy = resolveOpenAIGatewayClientStrategy(request, identity)
const joinedStrategy = resolveOpenAIGatewayClientStrategy(createRequest('turn_probe_owner'), { ...identity, apiKeyId: 'key_joined' })
const unrelatedStrategy = resolveOpenAIGatewayClientStrategy(createRequest('turn_probe_owner'), { ...identity, apiKeyId: 'key_unrelated' })
const activation = rememberCodexTurnStreamFailure(strategy, 'acct_probe', {
  evidence: 'committed_retry_signal',
  observationId: 'activation'
})?.activation
const joinedActivation = rememberCodexTurnStreamFailure(joinedStrategy, 'acct_probe', {
  evidence: 'committed_retry_signal',
  observationId: 'joined-activation'
})?.activation
rememberCodexTurnStreamFailure(unrelatedStrategy, 'acct_probe', {
  evidence: 'committed_retry_signal',
  observationId: 'unrelated-activation'
})
assert(activation && joinedActivation, '首次来源避让激活必须返回可绑定的 source generation')

let dispatchCount = 0
const handoffs: import('../../modules/accounts/account-health-check-trigger.js').CodexSourceProbeFence[] = []
const dispatch = (_accountId: string, _reason: 'request_failure', _traceId?: string, sourceFence?: import('../../modules/accounts/account-health-check-trigger.js').CodexSourceProbeFence) => {
  dispatchCount += 1
  assert(sourceFence, 'Codex activation dispatch 必须携带 opaque source fence')
  handoffs.push(sourceFence)
  return { outcome: 'queued' as const, decisionCode: 'queued' as const, targetRole: 'ops-worker' as const }
}
const [first, second] = await Promise.all([
  runCodexTurnAvoidanceAvailabilityProbe({ account, strategy, activation, dispatch }),
  runCodexTurnAvoidanceAvailabilityProbe({ account, strategy: joinedStrategy, activation: joinedActivation, dispatch })
])
assert.equal(dispatchCount, 2, '每个 source 必须向独立 worker 显式 handoff 自己的 fence')
assert.deepEqual([first.disposition, second.disposition].sort(), ['joined', 'owner'], 'joined 来源不得取得第二个 coordinator owner')
assert.equal(new Set(handoffs.map((fence) => `${fence.runtimeKey}:${fence.probeGeneration}`)).size, 1, 'joined handoff 必须绑定同一实际 probe generation')
assert(handoffs.every((fence) => fence.stateKey && fence.accountId === 'acct_probe' && fence.sourceGeneration >= 1 && fence.configRevision === 7), 'IPC handoff 必须携带 state/account/source/config/generation fence')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_probe', name: 'acct_probe' } as never], strategy).thresholdReached, true, '投递成功不是上游成功，来源避让仍须保留')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_probe', name: 'acct_probe' } as never], joinedStrategy).thresholdReached, true, 'joined 来源在后台结算前也须保留避让')

const runtimeKey = availabilityProbeRuntimeKey(gatewayAccountRuntimeKey(account), 'account_health_check', 7)
const worker = await acquireAvailabilityProbe({
  accountRuntimeScope: gatewayAccountRuntimeKey(account),
  probeKind: 'account_health_check',
  configRevision: 7,
  executionRole: 'health_probe'
})
assert.equal(worker.disposition, 'owner', '来源 owner 释放后，后台健康 worker 必须接管同一 generation 执行唯一真实探活')
if (worker.disposition !== 'owner') throw new Error('expected health worker owner')
assert.equal(worker.runtimeKey, runtimeKey, '来源与健康 worker 必须共享同一 runtime/config generation key')
assert.equal(await settleAvailabilityProbe({
  runtimeKey: worker.runtimeKey,
  generation: worker.generation,
  ownerToken: worker.ownerToken,
  outcome: 'success'
}), true, '真实健康 probe 的 owner 必须能按 fence 结算')
for (const sourceFence of handoffs) {
  // Simulate a separate worker memory store returning the IPC payload to the
  // gateway process. The handler may not broad-clear account runtime state.
  await settleCodexSourceFenceFromWorker(sourceFence, 'success')
}
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_probe', name: 'acct_probe' } as never], strategy).thresholdReached, false, '成功只能清除已登记的精确 source fence')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_probe', name: 'acct_probe' } as never], joinedStrategy).thresholdReached, false, '共享成功必须清除同 generation 内 joined 来源的精确 fence')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_probe', name: 'acct_probe' } as never], unrelatedStrategy).thresholdReached, true, '成功不得清除未加入本次 generation 的其他来源避让')

const nextActivation = rememberCodexTurnStreamFailure(strategy, 'acct_probe', {
  evidence: 'committed_retry_signal', observationId: 'activation-next-generation'
})?.activation
assert(nextActivation && nextActivation.sourceGeneration > activation.sourceGeneration, '清理后再次 activation 必须具有更高来源 generation')
const nextGeneration = await runCodexTurnAvoidanceAvailabilityProbe({ account, strategy, activation: nextActivation, dispatch })
assert.equal(nextGeneration.disposition, 'owner', '旧 settled success 后的新 activation 必须创建新的 coordinator owner')
assert.equal(dispatchCount, 3, '新 generation 必须发送新的 source handoff，不能 join/clear 旧成功')

const unavailableAccount = { id: 'acct_unknown', name: 'acct_unknown', priority: 0, configRevision: 7, status: 'temporary_unavailable' } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret
const unknownStrategy = resolveOpenAIGatewayClientStrategy(createRequest('turn_probe_unknown'), identity)
const unknownActivation = rememberCodexTurnStreamFailure(unknownStrategy, 'acct_unknown', {
  evidence: 'committed_retry_signal',
  observationId: 'unknown'
})?.activation
assert(unknownActivation)
const rejected = await runCodexTurnAvoidanceAvailabilityProbe({
  account: unavailableAccount,
  strategy: unknownStrategy,
  activation: unknownActivation,
  dispatch: () => ({ outcome: 'rejected', decisionCode: 'ops_ipc_unavailable' })
})
assert.equal(rejected.outcome, 'probe_task_failure', '未接受的调度必须显式结算为可重试任务失败')
assert.equal(unavailableAccount.status, 'temporary_unavailable', 'unknown/task failure 不得把 temporary_unavailable 旁路恢复')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_unknown', name: 'acct_unknown' } as never], unknownStrategy).thresholdReached, true, 'unknown/task failure 不得清除来源避让')

const coalescedStrategy = resolveOpenAIGatewayClientStrategy(createRequest('turn_probe_coalesced'), identity)
const coalescedActivation = rememberCodexTurnStreamFailure(coalescedStrategy, 'acct_coalesced', {
  evidence: 'committed_retry_signal',
  observationId: 'coalesced'
})?.activation
assert(coalescedActivation)
const coalesced = await runCodexTurnAvoidanceAvailabilityProbe({
  account: { id: 'acct_coalesced', name: 'acct_coalesced', priority: 0, configRevision: 7 } as unknown as import('../../storage/openai-account-selector.types.js').OpenAIAccountSecret,
  strategy: coalescedStrategy,
  activation: coalescedActivation,
  dispatch: () => ({ outcome: 'coalesced', decisionCode: 'request_failure_cooldown', targetRole: 'ops-worker', cooldownRemainingMs: 1_000 })
})
assert.equal(coalesced.outcome, 'unknown', '冷却合并不能假定既有 worker 会接管本 generation，必须结算为 unknown')
assert.equal(orderOpenAIAccountsByCodexTurnAvoidance([{ id: 'acct_coalesced', name: 'acct_coalesced' } as never], coalescedStrategy).thresholdReached, true, 'coalesced unknown 不得清除来源避让')
assert.equal(sourceBoundProbePermitsAccountHealthMutation('health_failure'), true, 'source-bound 确认上游 transport failure 才可交既有账户阈值处理')
for (const outcome of ['success', 'unknown', 'probe_task_failure', 'canceled', 'stale'] as const) {
  assert.equal(sourceBoundProbePermitsAccountHealthMutation(outcome), false, `source-bound ${outcome} 不得写账户健康、运行态或 circuit`)
}

const sourceService = readFileSync(new URL('../../modules/gateway/client-profiles/codex-turn-availability-probe.service.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
assert(!sourceService.includes('probeCodexSwitchCandidateAccount'), '来源 owner 不得自行发起第二次上游探活')
assert(!sourceService.includes('clearGatewayAccountRuntimeAvailabilityLocal'), '来源 success 不得宽泛清除账户运行态')
assert(!sourceService.includes('account-circuit'), '来源避让不得改写账户 circuit')
assert(routesSource.includes('scheduleCodexTurnAvoidanceProbeFromRetryableRoute'), 'HTTP candidate exhaustion/unified retryable 产生新 activation 后必须接入来源探活调度')
const healthSource = readFileSync(new URL('../../modules/background/account-health-check.service.ts', import.meta.url), 'utf8')
assert(healthSource.includes('ordinaryAccountHealthSemantics'), '普通健康任务与 source-only 任务必须分离结算语义')
assert(healthSource.includes('sendCodexSourceFenceSettledToServer'), 'worker 必须把 source fence outcome 回传 gateway 进程')
assert(!healthSource.includes('if (sourceFences.length > 0)'), 'fence 数量不得把普通 health generation 降格为 source-only')

console.log('Codex turn 可用性探活回归通过：activation fence、owner/join、跨进程 handoff、后台唯一探活、精确来源清理、unknown 保留与账户/circuit 边界符合预期')

function createRequest(turnId: string) {
  const body = { model: 'gpt-5.3-codex', input: turnId, stream: true }
  const headers = { 'x-codex-turn-metadata': JSON.stringify({ turn_id: turnId }) }
  return {
    method: 'POST', originalUrl: '/v1/responses', path: '/v1/responses', body,
    headers,
    rawBody: Buffer.from(JSON.stringify(body)),
    header(name: string) { return headers[name.toLowerCase() as keyof typeof headers] }
  } as never
}
