import { strict as assert } from 'node:assert'

import {
  acquireHighConcurrencyClientIpSlot,
  clearClientIpConcurrency,
  clientIpConcurrencySnapshot
} from '../../modules/gateway/openai-gateway-client-ip-concurrency.service.js'

try {
  clearClientIpConcurrency()

  const disabled = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_ip',
    apiKeyId: 'key_ip',
    clientIp: '127.0.0.1',
    policy: {}
  })
  assert.equal(disabled.enabled, false, '默认不配置单 IP 并发限制时应完全关闭')
  assert.equal(clientIpConcurrencySnapshot().length, 0, '默认关闭时不应创建 IP 并发运行态')

  const alreadyAborted = new AbortController()
  alreadyAborted.abort()
  const abortedBeforeAcquire = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_aborted',
    apiKeyId: 'key_aborted',
    clientIp: '127.0.0.2',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue'
    },
    signal: alreadyAborted.signal
  })
  assert.equal(abortedBeforeAcquire.acquired, false, '已取消请求不应获得 IP 槽位')
  assert.equal(abortedBeforeAcquire.acquired === false ? abortedBeforeAcquire.reason : '', 'aborted')
  assert.equal(clientIpConcurrencySnapshot().length, 0, '已取消请求不应留下空 IP 并发运行态')

  const firstRejectSlot = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_ip',
    apiKeyId: 'key_ip',
    clientIp: '127.0.0.1',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'reject'
    }
  })
  assert.equal(firstRejectSlot.acquired, true, '第一个同 IP 请求应成功占用槽位')
  const rejected = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_ip',
    apiKeyId: 'key_ip',
    clientIp: '127.0.0.1',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'reject'
    }
  })
  assert.equal(rejected.acquired, false, '默认立即拒绝模式下超过限制应快速失败')
  assert.equal(rejected.acquired === false ? rejected.reason : '', 'limit_reached')

  const otherApiKeySlot = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_ip',
    apiKeyId: 'key_ip_other',
    clientIp: '127.0.0.1',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'reject'
    }
  })
  assert.equal(otherApiKeySlot.acquired, true, '不同 API Key 不应被同一个出口 IP 互相占用限制')
  firstRejectSlot.release()
  otherApiKeySlot.release()
  assert.equal(clientIpConcurrencySnapshot().length, 0, '释放后 IP 并发运行态应清空')

  const queueFirstSlot = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_queue',
    apiKeyId: 'key_queue',
    clientIp: '10.0.0.8',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 500
    }
  })
  assert.equal(queueFirstSlot.acquired, true, '排队模式第一个请求应占用槽位')
  const queuedWait = acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_queue',
    apiKeyId: 'key_queue',
    clientIp: '10.0.0.8',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 500
    }
  })
  assert.equal(clientIpConcurrencySnapshot()[0]?.queueSize, 1, '排队模式超过限制时应进入 IP 等待队列')
  setTimeout(() => queueFirstSlot.release(), 20)
  const queuedSlot = await queuedWait
  assert.equal(queuedSlot.acquired, true, '排队等待应在同 IP 槽位释放后继续')
  assert(queuedSlot.enabled && queuedSlot.acquired, '排队等待结果应是启用状态下获得槽位')
  assert.equal(queuedSlot.queued, true, '唤醒后的请求应标记为排队获得槽位')
  queuedSlot.release()

  const unlimitedQueueFirstSlot = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_unlimited_queue',
    apiKeyId: 'key_unlimited_queue',
    clientIp: '10.0.0.10',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 500
    }
  })
  const unlimitedQueueAbort = new AbortController()
  const queuedItems = Array.from({ length: 21 }, () => acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_unlimited_queue',
    apiKeyId: 'key_unlimited_queue',
    clientIp: '10.0.0.10',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 500
    },
    signal: unlimitedQueueAbort.signal
  }))
  assert.equal(clientIpConcurrencySnapshot()[0]?.queueSize, 21, '超过旧单 IP 等待队列上限后仍应进入等待队列')
  unlimitedQueueAbort.abort()
  const abortedQueuedItems = await Promise.all(queuedItems)
  assert(abortedQueuedItems.every((item) => item.acquired === false && item.reason === 'aborted'), '主动取消后旧上限外的等待项都应释放')
  assert(unlimitedQueueFirstSlot.acquired, '无限队列回归的第一个请求应已占用槽位')
  unlimitedQueueFirstSlot.release()

  const timeoutFirstSlot = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_timeout',
    apiKeyId: 'key_timeout',
    clientIp: '10.0.0.9',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 5
    }
  })
  const timeoutResult = await acquireHighConcurrencyClientIpSlot({
    systemAccountId: 'sys_ip',
    groupId: 'grp_timeout',
    apiKeyId: 'key_timeout',
    clientIp: '10.0.0.9',
    policy: {
      clientIpConcurrencyLimit: 1,
      clientIpConcurrencyOverflowMode: 'queue',
      maxQueueWaitMs: 5
    }
  })
  assert.equal(timeoutResult.acquired, false, '排队模式超过等待时间应快速失败')
  assert.equal(timeoutResult.acquired === false ? timeoutResult.reason : '', 'timeout')
  assert(timeoutFirstSlot.acquired, '超时回归的第一个请求应已占用槽位')
  timeoutFirstSlot.release()

  console.log('单 IP 并发保护回归通过：默认关闭、立即拒绝、按 Key 隔离、无限排队等待和超时均符合预期')
} finally {
  clearClientIpConcurrency()
}
