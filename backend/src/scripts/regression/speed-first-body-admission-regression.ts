import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import {
  acquireSpeedFirstBodyAdmission,
  clearSpeedFirstBodyAdmissionsForTest,
  speedFirstBodyAdmissionSnapshot
} from '../../modules/gateway/runtime/speed-first-body-admission.service.js'

try {
  const first = await acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_high_concurrency',
    apiKeyId: 'key_one',
    capacity: 1,
    maxQueueWaitMs: 500,
    maxQueueSize: 2,
    perApiKeyQueueLimit: 1
  })
  assert.equal(first.acquired, true, '第一个请求应立即取得正文 admission lease')

  let secondSettled = false
  const secondPromise = acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_high_concurrency',
    apiKeyId: 'key_two',
    capacity: 1,
    maxQueueWaitMs: 500,
    maxQueueSize: 2,
    perApiKeyQueueLimit: 1
  }).then((decision) => {
    secondSettled = true
    return decision
  })
  await waitMs(20)
  assert.equal(secondSettled, false, '容量占满时第二个请求必须在读取正文前等待')
  assert.deepEqual(speedFirstBodyAdmissionSnapshot(), [{
    key: 'sys_body_admission:route_speed_first:group_high_concurrency',
    capacity: 1,
    active: 1,
    queued: 1
  }])

  const sameKeyOverflow = await acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_high_concurrency',
    apiKeyId: 'key_two',
    capacity: 1,
    maxQueueWaitMs: 500,
    maxQueueSize: 2,
    perApiKeyQueueLimit: 1
  })
  assert.equal(sameKeyOverflow.acquired, false, '同一 Key 超过等待上限应拒绝')
  assert.equal(sameKeyOverflow.acquired === false ? sameKeyOverflow.reason : '', 'api_key_queue_full')

  const abortController = new AbortController()
  const abortPromise = acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_high_concurrency',
    apiKeyId: 'key_three',
    capacity: 1,
    maxQueueWaitMs: 500,
    maxQueueSize: 2,
    perApiKeyQueueLimit: 1,
    signal: abortController.signal
  })
  const queueFull = await acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_high_concurrency',
    apiKeyId: 'key_four',
    capacity: 1,
    maxQueueWaitMs: 500,
    maxQueueSize: 2,
    perApiKeyQueueLimit: 1
  })
  assert.equal(queueFull.acquired, false, '超过分组总等待上限应拒绝')
  assert.equal(queueFull.acquired === false ? queueFull.reason : '', 'queue_full')
  abortController.abort()
  const aborted = await abortPromise
  assert.equal(aborted.acquired, false, '客户端断开应取消正文 admission 等待')
  assert.equal(aborted.acquired === false ? aborted.reason : '', 'aborted')

  if (first.acquired) first.release()
  const second = await secondPromise
  assert.equal(second.acquired, true, '前一个 lease 释放后应按队列顺序唤醒')
  if (second.acquired) second.release()

  const timeoutHolder = await acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_timeout',
    apiKeyId: 'key_holder',
    capacity: 1,
    maxQueueWaitMs: 10,
    maxQueueSize: 1,
    perApiKeyQueueLimit: 1
  })
  const timedOut = await acquireSpeedFirstBodyAdmission({
    systemAccountId: 'sys_body_admission',
    routeStrategyId: 'route_speed_first',
    groupId: 'group_timeout',
    apiKeyId: 'key_waiter',
    capacity: 1,
    maxQueueWaitMs: 10,
    maxQueueSize: 1,
    perApiKeyQueueLimit: 1
  })
  assert.equal(timedOut.acquired, false, '等待超过配置窗口应超时')
  assert.equal(timedOut.acquired === false ? timedOut.reason : '', 'timeout')
  if (timeoutHolder.acquired) timeoutHolder.release()

  const serverSource = readFileSync(resolve(process.cwd(), 'src/server.ts'), 'utf8')
  assert(
    serverSource.indexOf('admitSpeedFirstRequestBody,') < serverSource.indexOf('parseGatewayRawBody,'),
    '正文 admission middleware 必须位于 express.raw 完整读取之前'
  )
  assert.match(
    serverSource,
    /const parseGatewayRawBody = wrapGatewayRawBodyParser\([\s\S]*?express\.raw\(\{ type: \(\) => true/,
    '正文解析器应继续由 express.raw 和统一错误分类包装构造'
  )
  assert(
    serverSource.indexOf('rejectGatewayRawBodyByContentLength,') < serverSource.indexOf('admitSpeedFirstRequestBody,'),
    '无需读取正文即可确认的 413 必须先于 admission 返回'
  )

  console.log('速度优先正文 admission 回归通过：正文前容量 lease、队列公平、单 Key 上限和超时符合预期')
} finally {
  clearSpeedFirstBodyAdmissionsForTest()
}

async function waitMs(ms: number): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, ms))
}
