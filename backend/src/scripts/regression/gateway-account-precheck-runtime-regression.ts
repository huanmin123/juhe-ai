import { strict as assert } from 'node:assert'

import {
  clearGatewayLocalAccountSuppressionsForTest,
  recordGatewayAccountFailureForPrecheckForTest,
  snapshotGatewayAccountRuntimeAvailability
} from '../../modules/gateway/gateway-account-side-effects.service.js'
import { logger } from '../../shared/logger.js'
import type { OpenAIAccountSecret } from '../../storage/repositories.js'

const account = createAccount('precheck-runtime-account')
logger.level = 'silent'

try {
  clearGatewayLocalAccountSuppressionsForTest()
  for (let index = 0; index < 4; index += 1) {
    recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
      systemAccountId: 'sys_admin',
      groupId: 'group-a',
      apiKeyId: 'key-a',
      clientIp: '10.0.0.1',
      endpoint: '/v1/responses',
      reason: '模拟网关短窗口失败'
    })
  }
  assert.equal(snapshotGatewayAccountRuntimeAvailability()[account.id], undefined, '未达到阈值前不应产生账号运行态避让')

  recordGatewayAccountFailureForPrecheckForTest(account, undefined, {
    systemAccountId: 'sys_admin',
    groupId: 'group-a',
    apiKeyId: 'key-b',
    clientIp: '10.0.0.2',
    endpoint: '/v1/responses',
    reason: '模拟网关短窗口失败'
  })

  const runtime = snapshotGatewayAccountRuntimeAvailability()[account.id]
  assert(runtime, '达到短窗口多来源阈值后应写入账号运行态缓存')
  assert.equal(runtime.status, 'precheck_pending')
  assert.equal(runtime.failureCount, 5)
  assert.equal(runtime.distinctClientIpCount, 2)
  assert.equal(runtime.distinctApiKeyCount, 2)
  assert.equal(runtime.precheckAttemptCount, 0)
  assert(runtime.reason?.includes('等待事前确认'), '运行态原因应说明这是事前确认前的短暂状态')

  console.log('网关账号事前确认运行态回归通过')
} finally {
  clearGatewayLocalAccountSuppressionsForTest()
}

function createAccount(id: string): OpenAIAccountSecret {
  return {
    id,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: '事前确认运行态账号',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 10,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    supportedModels: ['gpt-5.5'],
    currentConcurrency: 0,
    baseUrl: 'http://127.0.0.1:9/v1',
    apiKey: 'sk-precheck-runtime',
    passthroughEnabled: true,
    streamFailureCount: 0,
    credentials: {
      api_key: 'sk-precheck-runtime',
      base_url: 'http://127.0.0.1:9/v1'
    }
  }
}
