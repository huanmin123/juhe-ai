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
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const raceSlot = tryAcquireAccountConcurrency('acct_queue_race_release', 1)
  assert.equal(raceSlot.acquired, true, '竞态测试前应先占用账号并发')
  raceSlot.release()
  const immediateReadyAfterRace = await waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_queue_race_release',
    apiKeyId: 'key_queue_race_release',
    accountIds: ['acct_queue_race_release'],
    accountConcurrencyLimits: { acct_queue_race_release: 1 },
    policy: queuePolicy(500)
  })
  assert.equal(immediateReadyAfterRace.ready, true, '入队前账号已释放时应立即返回可调度，避免无意义排队')
  assert.equal(immediateReadyAfterRace.ready ? immediateReadyAfterRace.waitedMs : -1, 0, '入队前容量复查命中时不应产生等待耗时')
  assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '入队前容量复查命中时不应留下队列项')
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const heldImageSlot = tryAcquireAccountConcurrency('acct_lane_cross_wake', 2, { lane: 'image', laneLimit: 1 })
  const heldTextSlot = tryAcquireAccountConcurrency('acct_lane_cross_wake', 2, { lane: 'text' })
  assert.equal(heldImageSlot.acquired, true, '跨通道唤醒测试前应占用图像槽')
  assert.equal(heldTextSlot.acquired, true, '跨通道唤醒测试前应占用文本槽')
  const queuedText = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_lane_cross_wake',
    apiKeyId: 'key_lane_text',
    accountIds: ['acct_lane_cross_wake'],
    accountConcurrencyLimits: { acct_lane_cross_wake: 2 },
    lane: 'text',
    policy: queuePolicy(500)
  })
  const queuedImage = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_lane_cross_wake',
    apiKeyId: 'key_lane_image',
    accountIds: ['acct_lane_cross_wake'],
    accountConcurrencyLimits: { acct_lane_cross_wake: 2 },
    lane: 'image',
    policy: queuePolicy(500)
  })
  let laneSnapshots = highConcurrencyGroupQueueSnapshot()
  assert.equal(laneSnapshots.length, 2, '文本和图像应进入不同通道队列')
  assert(laneSnapshots.some((snapshot) => snapshot.lane === 'text' && snapshot.groupKey.endsWith(':text')), '文本队列 key 应带 text 后缀')
  assert(laneSnapshots.some((snapshot) => snapshot.lane === 'image' && snapshot.groupKey.endsWith(':image')), '图像队列 key 应带 image 后缀')
  heldTextSlot.release()
  const textWakeResult = await queuedText
  assert.equal(textWakeResult.ready, true, '文本槽释放后应优先唤醒文本等待项')
  laneSnapshots = highConcurrencyGroupQueueSnapshot()
  assert.equal(laneSnapshots.length, 1, '只应唤醒一个等待项，图像队列仍应保留')
  assert.equal(laneSnapshots[0]?.lane, 'image', '同通道优先唤醒后剩余等待项应是图像队列')
  heldImageSlot.release()
  const imageWakeResult = await queuedImage
  assert.equal(imageWakeResult.ready, true, '图像槽释放后应优先唤醒图像等待项')
  assert.equal(highConcurrencyGroupQueueSnapshot().length, 0, '跨通道唤醒结束后队列应清空')
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const imageLaneFullSlot = tryAcquireAccountConcurrency('acct_image_lane_full_queue', 2, { lane: 'image', laneLimit: 1 })
  const textSlotForImageLaneFull = tryAcquireAccountConcurrency('acct_image_lane_full_queue', 2, { lane: 'text' })
  assert.equal(imageLaneFullSlot.acquired, true, '图像通道满测试前应占用图像槽')
  assert.equal(textSlotForImageLaneFull.acquired, true, '图像通道满测试前应占用文本槽')
  const imageLaneFullAbort = new AbortController()
  const queuedImageLaneFull = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_image_lane_full',
    apiKeyId: 'key_image_lane_full',
    accountIds: ['acct_image_lane_full_queue'],
    accountConcurrencyLimits: { acct_image_lane_full_queue: 2 },
    lane: 'image',
    policy: queuePolicy(500),
    signal: imageLaneFullAbort.signal
  })
  assert.equal(highConcurrencyGroupQueueSnapshot()[0]?.lane, 'image', '图像通道满时应记录图像等待队列')
  textSlotForImageLaneFull.release()
  await waitMs(30)
  assert.equal(highConcurrencyGroupQueueSnapshot()[0]?.queueSize, 1, '仅释放文本槽但图像通道仍满时不应误唤醒图像等待项')
  imageLaneFullAbort.abort()
  const imageLaneFullAbortResult = await queuedImageLaneFull
  assert.equal(imageLaneFullAbortResult.ready, false, '主动取消图像等待项应返回失败')
  assert.equal(imageLaneFullAbortResult.ready === false ? imageLaneFullAbortResult.reason : '', 'aborted')
  imageLaneFullSlot.release()
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const unlimitedQueueSlot = tryAcquireAccountConcurrency('acct_unlimited_queue', 1)
  assert.equal(unlimitedQueueSlot.acquired, true, '无限队列测试前应先占用账号并发')
  const firstWaitAbort = new AbortController()
  const firstWait = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_unlimited_queue',
    apiKeyId: 'key_unlimited_queue',
    accountIds: ['acct_unlimited_queue'],
    policy: {
      maxQueueWaitMs: 500,
      maxQueueSize: 1,
      perApiKeyQueueLimit: 1
    },
    signal: firstWaitAbort.signal
  })
  const secondWaitAbort = new AbortController()
  const secondWait = waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_unlimited_queue',
    apiKeyId: 'key_unlimited_queue',
    accountIds: ['acct_unlimited_queue'],
    policy: {
      maxQueueWaitMs: 500,
      maxQueueSize: 1,
      perApiKeyQueueLimit: 1
    },
    signal: secondWaitAbort.signal
  })
  const unlimitedQueueSnapshot = highConcurrencyGroupQueueSnapshot()[0]
  assert.equal(unlimitedQueueSnapshot?.queueSize, 2, '超过旧队列上限后仍应保留等待项')
  assert.equal(unlimitedQueueSnapshot?.perApiKeyQueueSize.key_unlimited_queue, 2, '超过旧 API Key 队列上限后仍应按 Key 记录排队数')
  firstWaitAbort.abort()
  secondWaitAbort.abort()
  const [firstAbortResult, secondAbortResult] = await Promise.all([firstWait, secondWait])
  assert.equal(firstAbortResult.ready === false ? firstAbortResult.reason : '', 'aborted')
  assert.equal(secondAbortResult.ready === false ? secondAbortResult.reason : '', 'aborted')
  unlimitedQueueSlot.release()
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()

  const timeoutSlot = tryAcquireAccountConcurrency('acct_timeout', 1)
  assert.equal(timeoutSlot.acquired, true, '超时测试前应先占用账号并发')
  const timeoutResult = await waitForHighConcurrencyGroupCapacity({
    systemAccountId: 'sys_queue',
    groupId: 'grp_timeout',
    apiKeyId: 'key_timeout',
    accountIds: ['acct_timeout'],
    accountConcurrencyLimits: { acct_timeout: 1 },
    policy: {
      maxQueueWaitMs: 5,
      maxQueueSize: 5,
      perApiKeyQueueLimit: 5
    }
  })
  assert.equal(timeoutResult.ready, false, '没有账号释放时应等待超时')
  assert.equal(timeoutResult.ready === false ? timeoutResult.reason : '', 'timeout')
  timeoutSlot.release()

  console.log('高并发分组短队列回归通过：账号释放唤醒、通道容量检查、旧队列上限不再拒绝和等待超时均符合预期')
} finally {
  clearAccountConcurrency()
  clearHighConcurrencyGroupQueues()
}

function queuePolicy(maxQueueWaitMs: number): { maxQueueWaitMs: number; maxQueueSize: number; perApiKeyQueueLimit: number } {
  return {
    maxQueueWaitMs,
    maxQueueSize: 10,
    perApiKeyQueueLimit: 10
  }
}

async function waitMs(delayMs: number): Promise<void> {
  await new Promise<void>((resolve) => {
    setTimeout(resolve, delayMs)
  })
}
