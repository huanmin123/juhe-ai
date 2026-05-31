import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.processRole = 'worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const usageRecordQueue = await import('../../modules/gateway/usage-record-queue.service.js')

try {
  usageRecordQueue.clearUsageRecordQueueForTest()
  const snapshot = buildTrapSnapshot()
  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-snapshot-sanitize-boundary',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    endpoint: 'POST /v1/responses',
    providerCode: 'openai',
    success: true,
    stream: false,
    statusCode: 200,
    requestSnapshot: snapshot,
    createdAt: '2000-01-01T00:00:00.000Z'
  }])

  const runtime = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(runtime.queueLength, 1, '带大 snapshot 的 usage 记录应可入队')
  console.log('使用记录 snapshot 清洗边界回归通过：对象清洗达到字段上限后停止，不会为了截断先遍历完整对象')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
}

function buildTrapSnapshot(): Record<string, string> {
  const snapshot: Record<string, string> = {}
  for (let index = 0; index < 80; index += 1) {
    snapshot[`field_${index}`] = 'x'.repeat(1024)
  }
  Object.defineProperty(snapshot, 'field_80_trap', {
    enumerable: true,
    get() {
      throw new Error('usage snapshot 清洗不应读取超过字段上限后的属性')
    }
  })
  return snapshot
}
