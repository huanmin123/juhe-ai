import { strict as assert } from 'node:assert'

import { clearAccountConcurrency, tryAcquireAccountConcurrency } from '../../shared/account-concurrency.js'
import {
  clearHighConcurrencyGroupQueues,
  highConcurrencyGroupQueueSnapshot,
  waitForHighConcurrencyGroupCapacity
} from '../../modules/gateway/openai-gateway-high-concurrency-queue.service.js'

try {
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const heldSlot = tryAcquireAccountConcurrency('acct_queue_release', 1)
  assert.equal(heldSlot.acquired, true, '测试前应先占用账号并发')
  const waitForRelease = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_queue',
    apiKeyId: 'key_queue_a',
    accountIds: ['acct_queue_release'],
    policy: {
      maxQueueWaitMs: 500,
      maxQueueSize: 10,
      perApiKeyQueueLimit: 2
    }
  })
  assert.equal(highConcurrencyGroupQueueSnapshot()[0]?.queueSize, 1, '分组短队列应记录等待项')
  setTimeout(() => heldSlot.release(), 20)
  const releaseResult = await waitForRelease
  assert.equal(releaseResult.ready, true, '账号并发释放后应唤醒队列等待项')
  assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '唤醒后队列应清空')

  const firstWaitAbort = new AbortController()
  const firstWait = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_limited',
    apiKeyId: 'key_limited',
    accountIds: ['acct_limited'],
    policy: {
      maxQueueWaitMs: 500,
      maxQueueSize: 5,
      perApiKeyQueueLimit: 1
    },
    signal: firstWaitAbort.signal
  })
  const rejectedByApiKeyLimit = await waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_limited',
    apiKeyId: 'key_limited',
    accountIds: ['acct_limited'],
    policy: {
      maxQueueWaitMs: 500,
      maxQueueSize: 5,
      perApiKeyQueueLimit: 1
    }
  })
  assert.equal(rejectedByApiKeyLimit.ready, false, '同一 API Key 超过队列上限应快速失败')
  assert.equal(rejectedByApiKeyLimit.ready === false ? rejectedByApiKeyLimit.reason : '', 'api_key_queue_full')
  firstWaitAbort.abort()
  await firstWait
  clearHighConcurrencyGroupQueues()

  const timeoutResult = await waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_timeout',
    apiKeyId: 'key_timeout',
    accountIds: ['acct_timeout'],
    policy: {
      maxQueueWaitMs: 5,
      maxQueueSize: 5,
      perApiKeyQueueLimit: 5
    }
  })
  assert.equal(timeoutResult.ready, false, '没有账号释放时应等待超时')
  assert.equal(timeoutResult.ready === false ? timeoutResult.reason : '', 'timeout')

  console.log('高并发分组短队列回归通过：账号释放唤醒、每 API Key 队列上限和等待超时均符合预期')
} finally {
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()
}
