import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { automaticAccountProbeOutcome } from '../../modules/accounts/automatic-account-probe-outcome.js'
import { isRealUpstreamAttempt } from '../../modules/gateway/upstream/attempt.js'
import {
  accountPrecheckMinimumObservationMs,
  accountPrecheckProbeIntervalMs,
  nextAccountPrecheckProbeAtMs
} from '../../modules/gateway/runtime/account-probe-confirmation-policy.js'

assert.equal(accountPrecheckProbeIntervalMs, 2 * 60_000)
assert.equal(accountPrecheckMinimumObservationMs, 5 * 60_000)
assert.equal(nextAccountPrecheckProbeAtMs({
  attemptCount: 1,
  maxAttempts: 3,
  startedAtMs: 0,
  nowMs: 60_000
}), 180_000, '后台确认轮次必须跨时间分散，不能在同一短突发内连跑')
assert.equal(nextAccountPrecheckProbeAtMs({
  attemptCount: 3,
  maxAttempts: 3,
  startedAtMs: 0,
  nowMs: 240_000
}), 300_000, '探针次数达到门槛后仍必须满足最小观察时间')
assert.equal(nextAccountPrecheckProbeAtMs({
  attemptCount: 3,
  maxAttempts: 3,
  startedAtMs: 0,
  nowMs: 300_000
}), undefined, '次数和跨时间观察均满足后才允许最终确认')

assert.equal(isRealUpstreamAttempt({
  upstreamUrl: 'account:capacity_limited'
}), false, '网关本地合成失败不能冒充真实上游尝试')
assert.equal(isRealUpstreamAttempt({
  upstreamUrl: 'https://api.openai.com/v1/responses'
}), true, '真实 HTTP(S) 上游请求应计为有效探针尝试')

const taskFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, false)
assert.equal(taskFailure, 'probe_task_failure')

const upstreamFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, true)
assert.equal(upstreamFailure, 'upstream_failure')

assert.equal(automaticAccountProbeOutcome({ success: true, accountFailureEligible: false }, true), 'complete_success')
assert.equal(automaticAccountProbeOutcome({ success: false, accountFailureEligible: false }, false), 'probe_task_failure')

const sideEffectsSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url)), 'utf8')
const healthCheckSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-health-check.service.ts', import.meta.url)), 'utf8')
const qualityPrecheckSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-quality-failure-precheck.service.ts', import.meta.url)), 'utf8')
const cooldownRetestSource = readFileSync(fileURLToPath(new URL('../../modules/background/cooldown-account-retest.service.ts', import.meta.url)), 'utf8')
const apiKeyRetestSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-api-key-cooldown-retest.service.ts', import.meta.url)), 'utf8')
const backgroundIpcSource = readFileSync(fileURLToPath(new URL('../../modules/background/background-ipc.ts', import.meta.url)), 'utf8')
const inspectionRuntimeSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/response/inspection-runtime-effects.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(sideEffectsSource, /result\.success\s*\|\|\s*result\.accountFailureEligible\s*===\s*false/)
assert.match(
  sideEffectsSource,
  /const current = recoveryProbeStates\.get\(runtimeKey\)\s*if \(current\) return\s*const generation = nextRuntimeProbeGeneration/,
  '重复用户失败信号不得替换本地在途探针 generation'
)
assert.match(
  inspectionRuntimeSource,
  /configured_response_policy[\s\S]*suppressGatewayAccountLocallyForSeconds\(/,
  '用户显式响应拦截策略应允许直接执行配置的账户运行态避让'
)
assert.match(
  sideEffectsSource,
  /createRuntimeStateStore\('gateway-configured-account-policy-avoidance'\)/,
  '用户显式响应拦截账户避让必须使用独立的 memory/Redis TTL store'
)
assert.match(
  sideEffectsSource,
  /configuredPolicyAvoidanceStore\.getJsonMany/,
  '分布式运行态加载和候选过滤必须读取显式响应策略账户避让'
)
assert.match(
  sideEffectsSource,
  /configuredPolicyAvoidanceStore\.delete\(runtimeKey\)/,
  '手动恢复必须清理显式响应策略账户避让'
)
assert.doesNotMatch(
  sideEffectsSource.match(/async function clearDistributedRecoveryProbeState\([^]*?\n\}/)?.[0] ?? '',
  /configuredPolicyAvoidanceStore\.delete/,
  '后台探针成功清理自动复核状态时不得误清用户显式响应策略 TTL'
)
assert.match(
  backgroundIpcSource,
  /clearServerAccountRuntimeAvailability[\s\S]{0,300}clearGatewayAutomaticAccountRuntimeAvailability/,
  '后台健康检查成功必须使用自动探针专用清理入口，不能解除用户显式策略 TTL'
)
assert.match(sideEffectsSource, /distributedRecoveryProbeStore\.setIfAbsent\(/, 'Redis 用户信号只能首次创建后台事件')
assert.doesNotMatch(sideEffectsSource, /mergeDistributedRecoveryProbeFailureState\(/, 'Redis 用户信号不得 merge 或续期已有后台事件')
assert.match(
  sideEffectsSource,
  /function isPrecheckRuntimeBlocking\(runtimeKey: string\): boolean \{[\s\S]{0,120}return precheckStates\.has\(runtimeKey\)[\s\S]{0,20}\}/,
  'memory 运行态只能在后台探针进入 precheck_pending 后软阻断账户'
)
assert.match(
  sideEffectsSource,
  /operation\.input\.trafficSource === 'gateway'\s*&&\s*!operation\.input\.policyDecision/,
  '普通网关请求应被拦截，但显式账户错误策略必须保留状态写权限'
)
const distributedSuppressionFilterSource = sideEffectsSource.match(
  /async function filterConfiguredPolicyAvoidances<[^>]+>\([^]*?\n\}/
)?.[0] ?? ''
assert.doesNotMatch(
  distributedSuppressionFilterSource,
  /distributedRecoveryProbeStore|precheck_pending|recovery_wait/,
  '用户显式策略过滤器必须保持独立，自动探针软阻断由专用过滤器叠加'
)
assert.match(
  distributedSuppressionFilterSource,
  /loadConfiguredPolicyAvoidanceStates/,
  '用户显式策略过滤器必须继续读取账户所有者配置的避让状态'
)
assert.match(sideEffectsSource, /onUpstreamAttempt:/)
assert.match(sideEffectsSource, /isRealUpstreamAttempt\(attempt\)/, '网关后台复核只能接受真实 HTTP(S) 上游尝试')
assert.match(sideEffectsSource, /automaticAccountProbeOutcome\(result, upstreamAttemptObserved\)/)
assert.doesNotMatch(
  sideEffectsSource,
  /probeOutcome === 'probe_task_failure'[\s\S]{0,240}throw new Error/,
  '未形成有效上游尝试必须丢弃判断并释放观察，不能进入无限异常重试'
)
assert.match(
  sideEffectsSource,
  /probeOutcome === 'probe_task_failure'[\s\S]{0,500}clearDistributedRecoveryProbeStateGeneration/,
  'Redis 探针任务失败必须精确清理当前 generation'
)
assert.match(
  sideEffectsSource,
  /shouldSkipHealthySuccessfulAccountSideEffect[\s\S]{0,500}clearGatewayAccountRuntimeAvailabilityForRuntimeKey/,
  '真实健康成功的快速跳过分支必须使用 driver-aware clear'
)
assert.doesNotMatch(
  sideEffectsSource,
  /state\.phase === 'precheck_pending' \? 'precheck_pending' : 'local_suppressed'/,
  '尚未执行后台探针的 recovery_wait 不得显示成短暂避让'
)
for (const [name, source] of [
  ['主动健康检查', healthCheckSource],
  ['质量失败复核', qualityPrecheckSource],
  ['账户冷却复测', cooldownRetestSource],
  ['账户 Key 冷却复测', apiKeyRetestSource]
] as const) {
  assert.match(source, /automaticAccountProbeOutcome\(/, `${name}必须按是否形成真实上游尝试判断`)
  assert.match(source, /isRealUpstreamAttempt\(attempt\)/, `${name}不能把本地合成尝试当作真实上游证据`)
  if (name !== '账户 Key 冷却复测') {
    assert.match(source, /retryAllFailures: true/, `${name}必须完成通用后台诊断轮次，不能被错误类型提前截断`)
  }
  assert.doesNotMatch(
    source,
    /probeOutcome === 'probe_task_failure'\s*\|\|\s*result\.accountFailureEligible === false/,
    `${name}不能再用错误类型分类决定账户状态`
  )
}

console.log('AUTOMATIC_ACCOUNT_PROBE_OUTCOME_OK')
