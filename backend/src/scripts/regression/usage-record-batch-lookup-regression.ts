import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { UsageRecordInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-batch-lookup-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'usage-record-batch-lookup-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageRecordQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/usage-record-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: '使用记录批量查询回归分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '使用记录批量查询回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-batch-lookup',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const apiKey = repositories.createApiKeyRecord({
    name: '使用记录批量查询回归 Key',
    groupId: group.id
  }, access)

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const selectCounts = {
    apiKeys: 0,
    groups: 0,
    accounts: 0
  }
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+api_keys\b/i.test(sql)) {
      selectCounts.apiKeys += 1
    } else if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+groups\b/i.test(sql)) {
      selectCounts.groups += 1
    } else if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+accounts\b/i.test(sql)) {
      selectCounts.accounts += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    repositories.createUsageRecordsBatch([
      ...Array.from({ length: 5 }, (_, index) => buildUsageRecord(index, apiKey.id, group.id, account.id)),
      buildUsageRecord(99, 'missing-api-key', group.id, account.id)
    ])
  } finally {
    database.prepare = originalPrepare
  }

  assert.deepEqual(selectCounts, { apiKeys: 1, groups: 1, accounts: 1 }, '批量使用记录写入应一次性预加载 API Key、分组和账户归属')
  assert.equal(usageRecordCount(), 5, '不存在的 API Key 仍应被跳过，其余使用记录应正常写入')
  const detail = repositories.getUsageRecordDetail('usage_batch_lookup_3', access)
  assert(detail, '批量写入的使用记录详情应可读取')
  assert.equal(detail.systemAccountId, 'sys_admin', '使用记录应保留归属系统账户')
  assert.equal(detail.apiKeyId, apiKey.id, '使用记录应保留 API Key')
  assert.equal(detail.groupId, group.id, '使用记录应保留分组')
  assert.equal(detail.accountId, account.id, '使用记录应保留账户')

  const recordDatabase = databaseModule.getRecordDatabase()
  const originalRecordPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  let failedInsertPrepares = 0
  const failuresBefore = usageRecordQueue.getUsageRecordQueueRuntime().flushFailureCount
  recordDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+usage_records\b/i.test(sql)) {
      failedInsertPrepares += 1
      if (failedInsertPrepares === 1) {
        throw new Error('模拟使用记录批量写入失败')
      }
    }
    return originalRecordPrepare(sql)
  }) as typeof recordDatabase.prepare
  try {
    usageRecordQueue.enqueueUsageRecordsLocal([buildUsageRecord(101, apiKey.id, group.id, account.id)])
    usageRecordQueue.flushUsageRecordQueue({ retryOnFailure: false })
    assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().flushFailureCount, failuresBefore + 1, '使用记录写入失败应记录 flush 失败')
    assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().queueLength, 1, 'retryOnFailure=false 时失败使用记录应保留在队列')
    await waitForImmediate()
    assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().queueLength, 1, 'retryOnFailure=false 不应在返回后立刻异步重试使用记录')
    await waitForRetryDelay()
    assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().queueLength, 1, 'retryOnFailure=false 不应在默认重试延迟后异步重试使用记录')
    assert.equal(usageRecordExists('usage_batch_lookup_101'), 0, '失败后使用记录不应被后台定时器偷偷写入')
  } finally {
    recordDatabase.prepare = originalRecordPrepare
  }
  usageRecordQueue.flushAllUsageRecordQueue()
  assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().queueLength, 0, '恢复后保留的使用记录应可继续 flush 完成')
  assert.equal(usageRecordExists('usage_batch_lookup_101'), 1, '恢复后应写入保留的使用记录')

  console.log('使用记录批量查询回归通过：批量写入预加载归属，避免逐条查询 API Key/分组/账户')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function buildUsageRecord(index: number, apiKeyId: string, groupId: string, accountId: string): UsageRecordInput {
  return {
    id: `usage_batch_lookup_${index}`,
    traceId: `trace-usage-batch-lookup-${index}`,
    apiKeyId,
    groupId,
    accountId,
    endpoint: '/v1/responses',
    providerCode: 'openai',
    model: 'gpt-5.1',
    stream: false,
    statusCode: 200,
    success: true,
    durationMs: 100 + index,
    inputTokens: 10 + index,
    outputTokens: 20 + index,
    costUsd: 0.001,
    createdAt: new Date(Date.UTC(2026, 0, 2, 0, 0, index)).toISOString()
  }
}

function usageRecordCount(): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_records')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function usageRecordExists(id: string): number {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT COUNT(*) AS total FROM usage_records WHERE id = ?')
    .get(id) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

async function waitForImmediate(): Promise<void> {
  await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
}

async function waitForRetryDelay(): Promise<void> {
  await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 1100))
}
