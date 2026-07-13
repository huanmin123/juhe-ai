import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  clearRuntimeLogIndexQueueForTest,
  getRuntimeLogIndexRuntime,
  recordRuntimeLogRedisStreamEnqueueFailureForTest
} from '../../modules/runtime-logs/runtime-log-index-queue.service.js'

clearRuntimeLogIndexQueueForTest()

try {
  recordRuntimeLogRedisStreamEnqueueFailureForTest(Object.assign(new Error(''), { code: 'ECONNRESET' }))
  const runtime = getRuntimeLogIndexRuntime()

  assert.equal(runtime.redisEnqueueFailureCount, 1, 'Redis Stream 入队失败应单独计数')
  assert.equal(runtime.droppedCount, 1, '入队失败的派生运行日志应计入丢弃总数')
  assert(runtime.redisEnqueueLastErrorAt, 'Redis Stream 入队失败应记录发生时间')
  assert.match(runtime.flushLastError ?? '', /Error code=ECONNRESET/, '空错误消息也必须保留错误类型和 code')

  const source = readFileSync(new URL('../../modules/runtime-logs/runtime-log-index-queue.service.ts', import.meta.url), 'utf8')
  assert.match(source, /catch\(recordRuntimeLogRedisStreamEnqueueFailure\)/, '运行日志生产者必须把入队失败降级为可观测丢弃')
  assert.doesNotMatch(source, /scheduleProcessFatalError/, '单次运行日志索引入队失败不得触发进程级 fatal')

  console.log('运行日志 Redis 入队失败边界回归通过：单次 XADD 失败记录丢弃事实但不终止业务进程')
} finally {
  clearRuntimeLogIndexQueueForTest()
}
