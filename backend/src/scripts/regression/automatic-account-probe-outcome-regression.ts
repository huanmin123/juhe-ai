import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import {
  automaticAccountAvailabilityProbeFailed,
  automaticAccountProbeOutcome,
  transportProbeOutcomeFromAccountTestResult
} from '../../modules/accounts/automatic-account-probe-outcome.js'
import { isCompletedRealUpstreamAttempt, isRealUpstreamAttempt } from '../../modules/gateway/upstream/attempt.js'
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
assert.equal(isCompletedRealUpstreamAttempt({ upstreamUrl: 'https://api.openai.com/v1/responses' }), false, '仅发起 URL、未收到响应头不能作为账户失败证据')
assert.equal(isCompletedRealUpstreamAttempt({ upstreamUrl: 'https://api.openai.com/v1/responses', status: 503 }), true, '收到任意真实 HTTP 响应头只能作为 framing 完成证据')
assert.equal(isCompletedRealUpstreamAttempt({ upstreamUrl: 'account:capacity_limited', status: 503 }), false, '本地合成状态不得冒充上游响应')

const taskFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true })
assert.equal(taskFailure, 'probe_task_failure')

assert.equal(automaticAccountProbeOutcome({
  success: false,
  accountFailureEligible: true
}, {
  timeout: true
}), 'probe_task_failure', '没有真实上游 attempt 证据时，本地诊断超时必须归为服务端探针任务失败')

const upstreamFailure = automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    message: 'connection reset'
  }
})
assert.equal(upstreamFailure, 'upstream_failure')

assert.equal(automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses'
  },
  timeout: true,
  diagnosticTimeoutExhausted: true
}), 'upstream_failure', '完整诊断阶梯的每档真实上游超时必须成为上游不可访问证据')

assert.equal(automaticAccountProbeOutcome({ success: false, accountFailureEligible: true }, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses'
  },
  timeout: true,
  diagnosticTimeoutExhausted: false
}), 'probe_task_failure', '未耗尽完整诊断阶梯的本地 deadline 不得凭一次已开始请求升级为上游失败')

assert.equal(automaticAccountProbeOutcome({ success: true, accountFailureEligible: false }, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    status: 200
  }
}), 'complete_success')
assert.equal(automaticAccountProbeOutcome({ success: false, accountFailureEligible: false }), 'probe_task_failure')

for (const status of [400, 401, 429, 500, 503]) {
  assert.equal(automaticAccountProbeOutcome({
    success: false,
    statusCode: status,
    errorCode: status === 401 ? 'upstream_body_interrupted' : 'provider_defined_error',
    message: `HTTP ${status}`
  }, {
    upstreamAttempt: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      status
    }
  }), 'framing_complete_neutral', `完整 HTTP ${status} 只能形成中性 framing 结果`)
  assert.equal(
    automaticAccountAvailabilityProbeFailed(automaticAccountProbeOutcome({
      success: false,
      statusCode: status
    }, {
      upstreamAttempt: {
        upstreamUrl: 'https://api.openai.com/v1/responses',
        status
      }
    })),
    true,
    `受控可用性探针的完整 HTTP ${status} 失败必须形成可用性失败证据`
  )
  assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
    success: false,
    statusCode: status,
    message: `HTTP ${status}`
  }, {
    upstreamAttempt: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      status
    }
  }), {
    kind: 'framing_complete',
    statusCode: status
  }, `完整 HTTP ${status} 必须是 transport framing_complete，不能读取业务 success`)
}

for (const errorCode of ['invalid_protocol_success_response', 'invalid_image_generation_response', 'model_not_found', 'upstream_body_interrupted']) {
  assert.equal(automaticAccountProbeOutcome({
    success: false,
    statusCode: 200,
    errorCode,
    message: '上游返回 HTTP 2xx，但协议正文畸形'
  }, {
    upstreamAttempt: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      status: 200
    }
  }), 'framing_complete_neutral', `完整 2xx 的 ${errorCode} 不得冒充传输失败`)
}

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'upstream_said_timeout',
  message: 'socket hang up after quota rejection'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    status: 503
  }
}), {
  kind: 'framing_complete',
  statusCode: 503
}, '完整 framing 不得解析上游错误码或正文文案')

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户测试超时'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    message: 'first_byte_timeout',
    transportFailureKind: 'timeout'
  },
  timeout: true
}), {
  kind: 'transport_incomplete',
  failureKind: 'timeout'
})

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户测试超时'
}, {
  timeout: true
}), {
  kind: 'unknown',
  failureKind: 'task_failure'
}, '本地 deadline/lease timeout 不能在没有真实 upstream attempt 时伪造成上游超时')

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'ECONNREFUSED',
  message: 'connect ECONNREFUSED'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    message: 'connect ECONNREFUSED'
  }
}), {
  kind: 'transport_incomplete',
  failureKind: 'connection'
})

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  errorCode: 'upstream_body_interrupted',
  message: 'upstream body interrupted'
}, {
  upstreamAttempt: {
    upstreamUrl: 'https://api.openai.com/v1/responses',
    status: 200,
    transportFailureKind: 'read_incomplete'
  }
}), {
  kind: 'transport_incomplete',
  failureKind: 'read',
  statusCode: 200
})

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户测试已取消'
}, { canceled: true }), {
  kind: 'unknown',
  failureKind: 'canceled'
})

assert.deepEqual(transportProbeOutcomeFromAccountTestResult({
  success: false,
  message: '账户未绑定可用分组'
}), {
  kind: 'unknown',
  failureKind: 'task_failure'
})

const sideEffectsSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url)), 'utf8')
const accountTestEligibilitySource = readFileSync(fileURLToPath(new URL('../../modules/accounts/account-test-failure-eligibility.ts', import.meta.url)), 'utf8')
const healthCheckSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-health-check.service.ts', import.meta.url)), 'utf8')
const qualityPrecheckSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-quality-failure-precheck.service.ts', import.meta.url)), 'utf8')
const cooldownRetestSource = readFileSync(fileURLToPath(new URL('../../modules/background/cooldown-account-retest.service.ts', import.meta.url)), 'utf8')
const apiKeyRetestSource = readFileSync(fileURLToPath(new URL('../../modules/background/account-api-key-cooldown-retest.service.ts', import.meta.url)), 'utf8')
const backgroundIpcSource = readFileSync(fileURLToPath(new URL('../../modules/background/background-ipc.ts', import.meta.url)), 'utf8')
const inspectionRuntimeSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/response/inspection-runtime-effects.ts', import.meta.url)), 'utf8')
const gatewayRoutesSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/routes.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(
  accountTestEligibilitySource,
  /\b(?:400|401|403|404|405|409|415|422|429|500|503)\b|invalid_api_key|model_not_found|rate_limit|server_error/,
  '账户测试重试资格不得内置供应商状态码、错误码或错误类型语义'
)
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
  /operation\.input\.trafficSource === 'gateway'\s*&&\s*!operation\.input\.success\s*&&\s*!operation\.input\.policyDecision/,
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
const singleProbeSource = sourceBetween(sideEffectsSource, 'async function runSingleGatewayAccountPrecheck', 'function accountPrecheckFailureReason')
assert.match(singleProbeSource, /onUpstreamAttempt:/)
assert.match(singleProbeSource, /transportProbeOutcomeFromAccountTestResult\(/, '短运行态探针必须返回独立 transport outcome')
assert.match(singleProbeSource, /upstreamAttempt/, '短运行态探针必须携带真实 upstream attempt 证据')
assert.doesNotMatch(singleProbeSource, /automaticAccountProbeOutcome\(|result\.success/, '短运行态探针自身不得再读取业务 success')
assert.match(gatewayRoutesSource, /notifyUpstreamAttemptDiagnostic\(options, \{/)
assert.match(gatewayRoutesSource, /status: upstreamResponse\.status/, '收到上游响应头后必须立即向后台探针回传状态证据')

for (const [name, start, end] of [
  ['memory recovery', 'async function runGatewayAccountRecoveryProbe', 'async function runDistributedGatewayAccountRecoveryProbe'],
  ['redis recovery', 'async function runDistributedGatewayAccountRecoveryProbe', 'async function promoteDistributedRecoveryProbeToPrecheck'],
  ['redis precheck', 'async function runDistributedGatewayAccountPrecheck', 'function promoteRecoveryProbeToPrecheck'],
  ['memory precheck', 'async function runGatewayAccountPrecheck', 'function canUseProcessLocalGatewayAccountRuntimeState']
] as const) {
  const source = sourceBetween(sideEffectsSource, start, end)
  assert.doesNotMatch(source, /if \(result\.success\)/, `${name} 不能读取 AccountTestResult.success 决定短运行态恢复`)
  assert.match(source, /transportOutcome\.kind === 'framing_complete'/, `${name} 只能用 framing_complete 关闭短运行态`)
  assert.match(source, /transportOutcome\.kind === 'unknown'/, `${name} 必须单独处理 unknown`)
}

const memoryRecoveryUnknown = sourceBetween(
  sourceBetween(sideEffectsSource, 'async function runGatewayAccountRecoveryProbe', 'async function runDistributedGatewayAccountRecoveryProbe'),
  "if (result.transportOutcome.kind === 'unknown')",
  "if (result.transportOutcome.kind === 'framing_complete')"
)
assert.match(memoryRecoveryUnknown, /latest\.running = false/)
assert.match(memoryRecoveryUnknown, /scheduleRecoveryProbeTimer\(runtimeKey, recoveryProbeRetryDelayMs\)/)
assert.doesNotMatch(memoryRecoveryUnknown, /clearGatewayAccountRuntimeAvailabilityLocal|attemptCount \+=/)

const redisRecoveryUnknown = sourceBetween(
  sourceBetween(sideEffectsSource, 'async function runDistributedGatewayAccountRecoveryProbe', 'async function promoteDistributedRecoveryProbeToPrecheck'),
  "if (result.transportOutcome.kind === 'unknown')",
  "if (result.transportOutcome.kind === 'framing_complete')"
)
assert.match(redisRecoveryUnknown, /commitDistributedRecoveryProbeRun\(/)
assert.doesNotMatch(redisRecoveryUnknown, /clearDistributedRecoveryProbeRun|attemptCount:/)

const redisPrecheckUnknown = sourceBetween(
  sourceBetween(sideEffectsSource, 'async function runDistributedGatewayAccountPrecheck', 'function promoteRecoveryProbeToPrecheck'),
  "if (result.transportOutcome.kind === 'unknown')",
  "if (result.transportOutcome.kind === 'framing_complete')"
)
assert.match(redisPrecheckUnknown, /attemptCount: attempt/)
assert.match(redisPrecheckUnknown, /commitDistributedRecoveryProbeRun\(/)
assert.doesNotMatch(redisPrecheckUnknown, /clearDistributedRecoveryProbeRun/)

const memoryPrecheckUnknown = sourceBetween(
  sourceBetween(sideEffectsSource, 'async function runGatewayAccountPrecheck', 'function canUseProcessLocalGatewayAccountRuntimeState'),
  "if (result.transportOutcome.kind === 'unknown')",
  "if (result.transportOutcome.kind === 'framing_complete')"
)
assert.match(memoryPrecheckUnknown, /scheduleGatewayAccountPrecheckRun\(runtimeKey, recoveryProbeRetryDelayMs\)/)
assert.doesNotMatch(memoryPrecheckUnknown, /clearGatewayAccountRuntimeAvailabilityLocal|attemptCount = attempt \+ 1/)
assert.match(
  sideEffectsSource,
  /if \(observedOperation\.input\.success\)[\s\S]{0,700}await clearGatewayAccountRuntimeAvailabilityForRuntimeKey\(runtimeKey\)/,
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
  if (name === '主动健康检查' || name === '质量失败复核' || name === '账户冷却复测' || name === '账户 Key 冷却复测') {
    assert.match(source, /onDiagnosticAttemptResult: \(attempt\) => \{[\s\S]{0,240}upstreamAttempt = attempt\.upstreamAttempt[\s\S]{0,240}diagnosticTimeoutExhausted = attempt\.diagnosticTimeoutExhausted/, `${name}必须保留完整诊断阶梯超时与当前真实上游 attempt 的结构化事实`)
    assert.match(source, /automaticAccountProbeOutcome\(result, \{[\s\S]{0,160}upstreamAttempt,[\s\S]{0,160}diagnosticTimeoutExhausted[\s\S]{0,160}\}\)/, `${name}必须同时传递最后一次真实上游 attempt 和完整诊断阶梯超时事实`)
  }
  assert.match(source, /let upstreamAttempt: UpstreamAttempt \| undefined/, `${name}必须保存结构化传输证据，不能只保存是否见过响应头`)
  assert.doesNotMatch(source, /upstreamResponseObserved|isCompletedRealUpstreamAttempt/, `${name}不得把任意完整 HTTP 响应头直接解释成失败`)
  assert.match(source, /retryAllFailures: true/, `${name}必须完成通用后台诊断轮次，不能被错误类型提前截断`)
  assert.doesNotMatch(
    source,
    /probeOutcome === 'probe_task_failure'\s*\|\|\s*result\.accountFailureEligible === false/,
    `${name}不能再用错误类型分类决定账户状态`
  )
}
assert.match(healthCheckSource, /countTowardsThreshold: availabilityProbeFailed/, '健康检查必须累计受控探针确认的完整 HTTP 与 transport 失败')
assert.match(qualityPrecheckSource, /automaticAccountAvailabilityProbeFailed\(probeOutcome\)/, '质量失败复核必须接受受控探针确认的完整 HTTP 失败')
assert.match(qualityPrecheckSource, /type: 'mark_account_precheck_temporary_unavailable'/, '质量失败复核必须使用带调度代次和成功信号栅栏的状态写入')
assert.doesNotMatch(qualityPrecheckSource, /type: 'mark_account_test_temporary_unavailable'/, '质量失败复核不得复用缺少事前确认栅栏的人工测试写入')
assert.match(qualityPrecheckSource, /candidateAccount\.status !== 'active'/, '质量失败复核只能继续处理仍为 active 的候选账户')
assert.match(qualityPrecheckSource, /expectedStatus: 'active'/, '质量失败复核状态写入必须固定要求账户仍为 active')
for (const [name, source] of [
  ['账户冷却复测', cooldownRetestSource],
  ['账户 Key 冷却复测', apiKeyRetestSource]
] as const) {
  assert.match(source, /if \(probeOutcome !== 'upstream_failure'\)/, `${name}必须在共享状态写入前保留 framing 中性结果`)
}

console.log('AUTOMATIC_ACCOUNT_PROBE_OUTCOME_OK')

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert.notEqual(startIndex, -1, `缺少源码起点：${start}`)
  assert.notEqual(endIndex, -1, `缺少源码终点：${end}`)
  return source.slice(startIndex, endIndex)
}
