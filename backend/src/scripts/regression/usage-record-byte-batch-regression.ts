import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-byte-batch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-record-byte-batch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageRecordQueue, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/usage-record-shards.js')
])

try {
  usageRecordQueue.clearUsageRecordQueueForTest()
  const totalRecords = 200
  for (let index = 0; index < totalRecords; index += 1) {
    usageRecordQueue.enqueueUsageRecordsLocal([buildLargeUsageRecord(index)])
  }

  const beforeFlush = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(beforeFlush.queueLength, totalRecords, '大 usage 记录应先进入 worker 本地队列')
  assert(beforeFlush.queueBytes > 8 * 1024 * 1024, '回归样本必须超过单次 flush 字节窗口')

  usageRecordQueue.flushUsageRecordQueue({
    drain: true,
    retryOnFailure: false,
    maxBatches: 1
  })

  const afterOneBatch = usageRecordQueue.getUsageRecordQueueRuntime()
  assert(afterOneBatch.queueLength > 0, 'maxBatches=1 时超过字节窗口的 usage 队列不应被一次性写完')
  assert(afterOneBatch.queueLength < totalRecords, '按字节切批后第一批仍应写入一部分 usage 记录')
  assert(usageRecordCount() > 0, '第一批 usage 记录应已落库')

  usageRecordQueue.clearUsageRecordQueueForTest()
  console.log('使用记录字节切批回归通过：worker flush 按条数和 8MB 字节窗口切批，避免单次大事务长时间阻塞')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildLargeUsageRecord(index: number) {
  const createdAt = new Date(Date.UTC(2026, 0, 3, 0, 0, index)).toISOString()
  return {
    traceId: `trace-usage-byte-batch-${index}`,
    trafficSource: 'gateway' as const,
    systemAccountId: 'sys_admin',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    model: 'gpt-5.5',
    stream: false,
    statusCode: 200,
    success: true,
    durationMs: 10,
    requestSnapshot: largeSnapshot(index),
    createdAt
  }
}

function largeSnapshot(index: number): Record<string, string> {
  const output: Record<string, string> = {}
  for (let keyIndex = 0; keyIndex < 80; keyIndex += 1) {
    output[`field_${index}_${keyIndex}`] = 'x'.repeat(1024)
  }
  return output
}

function usageRecordCount(): number {
  return usageRecordShards.listUsageRecordShardLocations()
    .reduce((total, location) => {
      const row = usageRecordShards.getUsageRecordShardDatabase(location)
        .prepare('SELECT COUNT(*) AS total FROM usage_records')
        .get() as { total?: number } | undefined
      return total + Number(row?.total ?? 0)
    }, 0)
}
