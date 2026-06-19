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

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
logger.level = 'silent'

const [
  accountSideEffects,
  sideEffectPolicy
] = await Promise.all([
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/runtime/account-side-effect-policy.js')
])

try {
  assert.match(sideEffectServiceSource, /from '\.\/account-side-effect-policy\.js'/, '账号副作用服务应通过 policy 文件判断合并、取消和健康成功跳过')
  assert.doesNotMatch(sideEffectServiceSource, /function isHealthySuccessfulAccountSideEffect/, '健康成功跳过策略不应继续内联在账号副作用服务')
  assert.match(sideEffectPolicySource, /shouldSkipHealthySuccessfulAccountSideEffect/, '账号副作用 policy 应暴露健康成功跳过策略')

  const healthySuccess = buildAccountErrorHandlingOperation('acct_side_effect_healthy_success', true)
  const unhealthySuccess = buildAccountErrorHandlingOperation('acct_side_effect_unhealthy_success', true)
  unhealthySuccess.account.lastErrorMessage = 'previous failure'
  const failedOperation = buildAccountErrorHandlingOperation('acct_side_effect_failed', false, 503, 'failed')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(healthySuccess), true, '健康 active 账号成功副作用应允许跳过持久写')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(unhealthySuccess), false, '带历史错误的成功副作用不能跳过持久写')
  assert.equal(sideEffectPolicy.shouldSkipHealthySuccessfulAccountSideEffect(failedOperation), false, '失败副作用不能被健康成功策略跳过')

  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const before = accountSideEffects.getGatewayAccountSideEffectState()

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', false, 503, 'first failed write')
  )
  const firstPending = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(firstPending.queueLength, before.queueLength + 1, '首次账号失败副作用应进入队列')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', false, 429, 'latest failed write')
  )
  const coalesced = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(coalesced.queueLength, firstPending.queueLength, '同账号待执行失败副作用应只保留最新一条')
  assert.equal(coalesced.coalescedCount, before.coalescedCount + 1, '同账号失败副作用合并次数应递增')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_coalesce', true)
  )
  const canceled = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(canceled.queueLength, before.queueLength, '成功回写应取消同账号待执行失败状态覆盖写')
  assert.equal(canceled.canceledBySuccessCount, before.canceledBySuccessCount + 1, '成功回写取消计数应递增')

  accountSideEffects.enqueueGatewayStreamFailureSideEffect(buildStreamFailureOperation('stream_no_coalesce'))
  accountSideEffects.enqueueGatewayStreamFailureSideEffect(buildStreamFailureOperation('stream_no_coalesce'))
  const streamFailures = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(streamFailures.queueLength, before.queueLength + 2, '同账号流失败副作用有计数语义，不应被合并')
  assert.equal(streamFailures.coalescedCount, coalesced.coalescedCount, '流失败副作用不应计入 LWW 合并')

  accountSideEffects.enqueueGatewayAccountErrorHandlingSideEffect(
    buildAccountErrorHandlingOperation('acct_side_effect_limit_stream_no_coalesce', true)
  )
  const streamFailuresAfterSuccess = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(streamFailuresAfterSuccess.queueLength, streamFailures.queueLength, '成功回写不应取消同账号待执行流失败计数副作用')
  assert.equal(streamFailuresAfterSuccess.canceledBySuccessCount, canceled.canceledBySuccessCount, '成功回写只应取消失败状态覆盖写，不应取消流失败计数写')

  for (let index = 0; index < 4998; index += 1) {
    accountSideEffects.enqueueGatewayStreamFailureSideEffect(buildStreamFailureOperation(index))
  }
  const full = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(full.queueLength, 5000, '账号副作用队列应达到保护上限')
  assert.equal(full.droppedCount, before.droppedCount, '达到上限前不应丢弃账号副作用')

  accountSideEffects.enqueueGatewayStreamFailureSideEffect(buildStreamFailureOperation(5000))
  const overflow = accountSideEffects.getGatewayAccountSideEffectState()
  assert.equal(overflow.queueLength, 5000, '超过上限后账号副作用队列长度不应继续增长')
  assert.equal(overflow.droppedCount, before.droppedCount + 1, '超过上限后应记录账号副作用丢弃次数')

  console.log('网关账号副作用队列上限回归通过：副作用队列满后快速丢弃并记录指标，避免失败风暴无限堆积')
} finally {
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
}

function buildStreamFailureOperation(index: number | string): Parameters<typeof accountSideEffects.enqueueGatewayStreamFailureSideEffect>[0] {
  return {
    type: 'record_account_stream_failure',
    input: {
      accountId: `acct_side_effect_limit_${index}`,
      thresholdCount: 3,
      thresholdWindowMinutes: 1,
      action: 'cooldown',
      reason: 'side effect queue limit regression'
    }
  }
}

function buildAccountErrorHandlingOperation(
  accountId: string,
  success: boolean,
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
      concurrencyLimit: 1,
      priority: 0,
      superPriorityEnabled: false,
      fallbackEnabled: true,
      clientCompatibility: 'openai_standard',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: 'sk-regression',
      streamFailureCount: 0,
      credentials: {}
    },
    input: {
      success,
      statusCode,
      errorMessage
    }
  }
}
