import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-snapshot-sanitize-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.processRole = 'worker'
runtimeConfig.databasePath = join(tempRoot, 'usage-snapshot-sanitize.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 1
runtimeConfig.secret = 'usage-snapshot-sanitize-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  usageRecordQueue,
  repositories,
  usageRecordShards,
  databaseModule
] = await Promise.all([
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/database.js')
])

try {
  usageRecordQueue.clearUsageRecordQueueForTest()
  const snapshot = buildTrapSnapshot()
  usageRecordQueue.enqueueUsageRecordsLocal([{
    traceId: 'trace-usage-snapshot-sanitize-boundary',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: true,
    stream: false,
    statusCode: 200,
    requestSnapshot: snapshot,
    createdAt: '2000-01-01T00:00:00.000Z'
  }])

  const runtime = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(runtime.queueLength, 1, '带大 snapshot 的 usage 记录应可入队')
  usageRecordQueue.clearUsageRecordQueueForTest()

  const createdAt = '2000-01-01T00:00:00.000Z'
  const recordId = usageRecordShards.generateUsageRecordId(createdAt, 'sanitize-test')
  usageRecordQueue.enqueueUsageRecordsLocal([{
    id: recordId,
    traceId: 'trace-usage-snapshot-sensitive-boundary',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'grp_sensitive_snapshot',
    endpoint: 'POST /v1/responses',
    providerCode: 'gpt',
    success: false,
    stream: false,
    statusCode: 502,
    errorMessage: 'top client_secret=top-usage-client-secret Authorization: Bearer sk-top-usage-secret-token',
    requestSnapshot: {
      method: 'POST',
      originalUrl: '/v1/responses?api_key=request-url-secret&safe=ok',
      headers: {
        authorization: 'Bearer request-header-secret'
      },
      bodyText: '{"client_secret":"request-body-client-secret","safe":"ok"}'
    },
    responseSnapshot: {
      upstreamUrl: 'https://url-user:url-password@example.com/v1/chat/completions?client_secret=response-url-secret&safe=ok',
      headers: {
        'set-cookie': 'session=response-cookie-secret'
      },
      bodyText: '{"error":{"message":"client_secret=response-body-client-secret Authorization: Bearer sk-response-body-secret-token"}}',
      errorMessage: 'id_token=response-error-id-token sk-response-error-secret-token'
    },
    createdAt
  }])
  usageRecordQueue.flushAllUsageRecordQueue()
  const flushedRuntime = usageRecordQueue.getUsageRecordQueueRuntime()
  assert.equal(flushedRuntime.flushFailureCount, 0, '使用记录 flush 不应失败')
  assert.equal(flushedRuntime.queueLength, 0, '使用记录 flush 后队列应清空')

  const detail = repositories.getUsageRecordDetail(recordId)
  assert(detail, '应能读回写入的使用记录详情')
  const detailText = JSON.stringify(detail)
  assertAllPresent(detailText, [
    'top-usage-client-secret',
    'sk-top-usage-secret-token',
    'request-url-secret',
    'request-header-secret',
    'request-body-client-secret',
    'url-user',
    'url-password',
    'response-url-secret',
    'response-cookie-secret',
    'response-body-client-secret',
    'sk-response-body-secret-token',
    'response-error-id-token',
    'sk-response-error-secret-token'
  ], '使用记录落库内容应保留原文')
  assert(String(detail.responseSnapshot?.upstreamUrl ?? '').includes('safe=ok'), 'URL 安全查询参数应保留')
  assert(String(detail.requestSnapshot?.bodyText ?? '').includes('"safe":"ok"'), 'bodyText 中安全字段应保留')

  console.log('使用记录 snapshot 原文边界回归通过：对象字段上限仍生效，URL 凭据、敏感字符串和顶层错误按原文落库')
} finally {
  usageRecordQueue.clearUsageRecordQueueForTest()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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

function assertAllPresent(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(text.includes(marker), `${message}：${marker}`)
  }
}
