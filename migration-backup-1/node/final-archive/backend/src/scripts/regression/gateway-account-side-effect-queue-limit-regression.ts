import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const currentDir = dirname(fileURLToPath(import.meta.url))
const backendSrc = resolve(currentDir, '../..')
const sideEffectServiceSource = readFileSync(resolve(backendSrc, 'modules/gateway/runtime/account-side-effects.service.ts'), 'utf8')
const sideEffectPolicySource = readFileSync(resolve(backendSrc, 'modules/gateway/runtime/account-side-effect-policy.ts'), 'utf8')
const queueObservedAtBaseMs = Date.parse('2026-07-24T12:00:00.000Z')

function queueObservedAt(offsetMs: number): string {
  return new Date(queueObservedAtBaseMs + offsetMs).toISOString()
}

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
runtimeConfig.runtimeStateDriver = 'memory'
logger.level = 'silent'

const [
  accountSideEffects,
  sideEffectPolicy
] = await Promise.all([
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-side-effect-policy.js')
])

try {
  assert.match(sideEffectServiceSource, /from '\.\/account-side-effect-policy\.js'/, '账号副作用服务应通过 policy 文件判断合并和取消')
  assert.doesNotMatch(sideEffectServiceSource, /function isHealthySuccessfulAccountSideEffect/, '健康成功跳过策略不应继续内联在账号副作用服务')
  assert.doesNotMatch(sideEffectServiceSource, /enqueueGatewayStreamFailureSideEffect/, '历史流失败计数不应继续进入账号副作用内存队列')
  assert.doesNotMatch(sideEffectServiceSource, /record_account_stream_failure/, '账号副作用队列不应再执行历史流失败计数写入')
  assert.match(sideEffectServiceSource, /sideEffectQueue\.hasRuntimeKey\(runtimeKey\)/, '满载准入应使用 runtime 索引')
  assert.match(sideEffectServiceSource, /sideEffectQueue\.hasFailures/, '成功 watermark 满载准入应使用失败计数索引')
  assert.match(sideEffectServiceSource, /sideEffectQueue\.removeOldestFailure\(\)/, '成功 watermark 腾位应使用失败年龄索引')
  assert.doesNotMatch(sideEffectServiceSource, /sideEffectQueue\.findIndex\(/, '生产副作用路径不得在每次满载准入时线性扫描队列')
  assert.doesNotMatch(sideEffectServiceSource, /dropExpiredSideEffects/, 'drain 不得每处理一项都全队列扫描过期项')
  assert.match(sideEffectPolicySource, /shouldSkipHealthySuccessfulAccountSideEffect/, '账号副作用 policy 应暴露健康成功跳过策略')

  const healthySuccess = buildAccountErrorHandlingOperation('acct_side_effect_healthy_success', true, queueObservedAt(0))
  const unhealthySuccess = buildAccountErrorHandlingOperation('acct_side_effect_unhealthy_success', true, queueObservedAt(1))
  unhealthySuccess.account.lastErrorMessage = 'previous failure'
  const failedOperation = buildAccountErrorHandlingOperation('acct_side_effect_failed', false, queueObservedAt(2), 503, 'failed')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(healthySuccess), true, '健康 active 账号成功副作用应允许跳过持久写')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(unhealthySuccess), false, '带历史错误的成功副作用不能跳过持久写')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(failedOperation), false, '失败副作用不能被健康成功策略跳过')

  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const beforeGatewaySuccess = accountSideEffects.getGatewayAccountSideEffectState()
  const gatewayHealthySuccess = buildAccountErrorHandlingOperation('acct_side_effect_gateway_healthy_success', true, queueObservedAt(3))
  gatewayHealthySuccess.input.trafficSource = 'gateway'
  await accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(gatewayHealthySuccess)
  assert.equal(
    accountSideEffects.getGatewayAccountSideEffectState().queueLength,
    beforeGatewaySuccess.queueLength + 1,
    '真实 gateway 健康成功即使没有 policyDecision 也必须入队持久化 watermark'
  )

  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const before = accountSideEffects.getGatewayAccountSideEffectState()

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', false, queueObservedAt(4), 503, 'first failed write')
  )
  const firstPending = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(firstPending.queueLength, before.queueLength + 1, '首次账号失败副作用应进入队列')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', false, queueObservedAt(5), 429, 'latest failed write')
  )
  const coalesced = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(coalesced.queueLength, firstPending.queueLength, '同账号待执行失败副作用应只保留最新一条')
  assert.equal(coalesced.coalescedCount, before.coalescedCount + 1, '同账号失败副作用合并次数应递增')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', true, queueObservedAt(6))
  )
  const canceled = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(canceled.queueLength, before.queueLength + 1, '成功回写取消旧失败后仍必须入队，不能因 active 快照丢失持久 watermark')
  assert.equal(canceled.canceledBySuccessCount, before.canceledBySuccessCount + 1, '成功回写取消计数应递增')

  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const beforeLimit = accountSideEffects.getGatewayAccountSideEffectState()

  for (let index = 0; index < 5000; index += 1) {
    accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
      buildAccountErrorHandlingOperation(`acct_side_effect_limit_${index}`, false, queueObservedAt(1_000 + index), 502, 'queue limit regression')
    )
  }
  const full = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(full.queueLength, 5000, '账号副作用队列应达到保护上限')
  assert.equal(full.droppedCount, beforeLimit.droppedCount, '达到上限前不应丢弃账号副作用')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_success_at_capacity', true, queueObservedAt(6_000))
  )
  const successAtCapacity = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(successAtCapacity.queueLength, 5000, '满队列成功 watermark 应淘汰一条失败并保持容量上限')
  assert.equal(successAtCapacity.droppedCount, beforeLimit.droppedCount + 1, '满队列成功入队应记录被淘汰的失败')
  assert.equal(
    successAtCapacity.evictedFailureForSuccessCount,
    beforeLimit.evictedFailureForSuccessCount + 1,
    '满队列必须为成功 watermark 淘汰最早失败'
  )

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_limit_overflow', false, queueObservedAt(6_001), 502, 'queue overflow regression')
  )
  const overflow = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(overflow.queueLength, 5000, '超过上限后账号副作用队列长度不应继续增长')
  assert.equal(overflow.droppedCount, beforeLimit.droppedCount + 2, '超过上限后应记录账号副作用丢弃次数')

  for (let index = 0; index < 10_000; index += 1) {
    accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
      buildAccountErrorHandlingOperation(`acct_side_effect_overflow_storm_${index}`, false, queueObservedAt(7_000 + index), 502, 'queue overflow storm')
    )
  }
  const overflowStorm = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(overflowStorm.queueLength, 5000, '一万次满载溢出风暴后队列仍不得突破上限')
  assert.equal(
    overflowStorm.droppedCount,
    beforeLimit.droppedCount + 10_002,
    '满载溢出风暴应逐次拒绝且不进入线性查找路径'
  )

  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const beforeAllSuccessCapacity = accountSideEffects.getGatewayAccountSideEffectState()
  const allSuccessCapacityEnqueues: Array<Promise<void>> = []
  for (let index = 0; index < 5000; index += 1) {
    allSuccessCapacityEnqueues.push(accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
      buildAccountErrorHandlingOperation(`acct_side_effect_success_limit_${index}`, true, queueObservedAt(8_000 + index))
    ))
  }
  assert.equal(
    accountSideEffects.getGatewayAccountSideEffectState().queueLength,
    5000,
    '全 success 队列应能稳定达到容量上限'
  )

  const sameRuntimeReplacement = buildAccountErrorHandlingOperation('acct_side_effect_success_limit_0', true, queueObservedAt(9_000))
  sameRuntimeReplacement.input.observedAt = '2098-07-24T12:00:00.000Z'
  allSuccessCapacityEnqueues.push(
    accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(sameRuntimeReplacement)
  )
  const afterSameRuntimeReplacement = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(afterSameRuntimeReplacement.queueLength, 5000, '全 success 满队列应允许同 runtime success 原位替换')
  assert.equal(
    afterSameRuntimeReplacement.droppedCount,
    beforeAllSuccessCapacity.droppedCount,
    '同 runtime success 原位替换不应误记为容量丢弃'
  )
  assert.equal(
    afterSameRuntimeReplacement.canceledBySuccessCount,
    beforeAllSuccessCapacity.canceledBySuccessCount + 1,
    '同 runtime 新 success 应取消旧 watermark 并保留最新观测'
  )

  const droppedRecovery = buildAccountErrorHandlingOperation('acct_side_effect_recovery_after_capacity', true, queueObservedAt(10_000))
  droppedRecovery.input.observedAt = '2026-07-24T12:00:02.000Z'
  allSuccessCapacityEnqueues.push(accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(droppedRecovery))
  const afterDroppedRecovery = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(afterDroppedRecovery.queueLength, 5000, '全 success 队列满载时不得突破容量上限')
  assert.equal(
    afterDroppedRecovery.droppedCount,
    beforeAllSuccessCapacity.droppedCount + 1,
    '无可淘汰 failure 时应记录本次 success 未入队'
  )

  const replaceableFailure = buildAccountErrorHandlingOperation(
    'acct_side_effect_success_limit_0',
    false,
    queueObservedAt(11_000),
    502,
    'replaceable failure'
  )
  replaceableFailure.input.observedAt = '2099-07-24T12:00:03.000Z'
  allSuccessCapacityEnqueues.push(accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(replaceableFailure))
  assert.equal(
    accountSideEffects.getGatewayAccountSideEffectState().queueLength,
    5000,
    '同 runtime 的 failure 应替换已排队 success 并释放可淘汰槽位'
  )

  const olderRecoverableSuccess = buildAccountErrorHandlingOperation('acct_side_effect_recovery_after_capacity', true, queueObservedAt(12_000))
  olderRecoverableSuccess.input.observedAt = '2026-07-24T12:00:01.000Z'
  allSuccessCapacityEnqueues.push(accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(olderRecoverableSuccess))
  const recoveredAfterCapacity = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(
    recoveredAfterCapacity.staleCount,
    afterDroppedRecovery.staleCount,
    '未入队的 success 不得推进 epoch，否则后续可写入的恢复 watermark 会被误判 stale'
  )
  assert.equal(
    recoveredAfterCapacity.evictedFailureForSuccessCount,
    beforeAllSuccessCapacity.evictedFailureForSuccessCount + 1,
    '容量恢复后较早 success 仍应淘汰 failure 并入队，不能被已丢弃观测卡死'
  )
  assert.equal(recoveredAfterCapacity.queueLength, 5000, '恢复 watermark 补写后队列仍应受容量上限约束')
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  await Promise.all(allSuccessCapacityEnqueues)

  console.log('网关账号副作用队列上限回归通过：副作用队列满后保持有界，且未入队 success 不会推进 epoch 卡死恢复 watermark')
} finally {
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
}

function buildAccountErrorHandlingOperation(
  accountId: string,
  success: boolean,
  observedAt: string,
  statusCode?: number,
  errorMessage?: string
): Parameters<typeof accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect>[0] {
  return {
    type: 'apply_account_error_handling',
    account: {
      id: accountId,
      providerCode: 'openai',
      providerProtocolProfileId: 'openai',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      systemAccountId: 'sys_side_effect_limit',
      accountOwnerSystemAccountId: 'sys_side_effect_limit',
      groupOwnerSystemAccountId: 'sys_side_effect_limit',
      accountAccessType: 'owner',
      groupAccessType: 'owner',
      name: accountId,
      type: 'api_key',
      status: 'active',
      dispatchRevision: 1,
      concurrencyLimit: 1,
      priority: 0,
      superPriorityEnabled: false,
      fallbackEnabled: true,
      clientCompatibility: 'openai_standard',
      healthCheckEndpointMode: 'chat_json',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: 'sk-regression',
      streamFailureCount: 0,
      credentials: {}
    },
    input: {
      success,
      observedAt,
      statusCode,
      errorMessage
    }
  }
}
