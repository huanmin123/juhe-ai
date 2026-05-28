import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
logger.level = 'silent'

const accountSideEffects = await import('../../modules/gateway/gateway-account-side-effects.service.js')

try {
  accountSideEffects.clearGatewayAccountSideEffectQueueForTest()
  const before = accountSideEffects.getGatewayAccountSideEffectState()

  for (let index = 0; index < 5000; index += 1) {
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

function buildStreamFailureOperation(index: number): Parameters<typeof accountSideEffects.enqueueGatewayStreamFailureSideEffect>[0] {
  return {
    type: 'record_account_stream_failure',
    input: {
      accountId: `acct_side_effect_limit_${index}`,
      thresholdCount: 3,
      thresholdWindowMinutes: 1,
      action: 'cooldown',
      cooldownMinutes: 1,
      reason: 'side effect queue limit regression'
    }
  }
}
