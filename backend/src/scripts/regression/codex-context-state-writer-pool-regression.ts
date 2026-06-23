import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import * as databaseModule from '../../storage/database.js'
import * as writerPool from '../../storage/codex-context-state-writer-pool.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-context-writer-pool-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 8
runtimeConfig.codexContextStateWriterPoolEnabled = true
runtimeConfig.codexContextStateWriterPoolSize = 4
runtimeConfig.codexContextStateWriterQueueMaxItems = 2000
runtimeConfig.secret = 'codex-context-writer-pool-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const boundary = {
  systemAccountId: 'sys_writer_pool',
  apiKeyId: 'apikey_writer_pool',
  groupId: 'group_writer_pool',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

const futureExpiresAt = '2999-01-01T00:00:00.000Z'
const expiredAt = '2000-01-01T00:00:00.000Z'
const cleanupNow = '2026-06-22T00:00:00.000Z'
const sharedStorageKey = 'sessions/shared/segments/2026062200.json.gz'

try {
  await runRegression()
  console.log('Codex context state writer pool 回归通过：并发 shard 写入、read-after-write、compact 边界、cleanup 屏障和 shared storageKey 保护正常')
} finally {
  await writerPool.closeCodexContextStateWriterPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runRegression(): Promise<void> {
  const tails = await writeConcurrentResponseChains()
  const runtimeAfterWrites = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(runtimeAfterWrites.enabled, true, 'writer pool 应启用')
  assert(runtimeAfterWrites.workerCount > 0, 'writer pool 应按需创建 worker')
  assert(runtimeAfterWrites.handledJobs >= 64, 'writer pool 应执行并发写入任务')
  assert.equal(runtimeAfterWrites.failedJobs, 0, 'writer pool 不应产生失败任务')

  assertShardDistribution()
  await assertResponseChainRestores(tails)
  await assertCompactState()
  await assertCleanupBarrierAndStorageKeyProtection()

  const runtimeAfterCleanup = writerPool.getCodexContextStateWriterPoolRuntime()
  assert.equal(runtimeAfterCleanup.queueLength, 0, 'writer pool 队列应清空')
  assert.equal(runtimeAfterCleanup.activeJobs, 0, 'writer pool 不应遗留活动任务')
}

async function writeConcurrentResponseChains(): Promise<string[]> {
  const tails = await Promise.all(Array.from({ length: 16 }, async (_, sessionIndex) => {
    const sessionId = `pool_session_${sessionIndex}`
    let previousResponseId: string | undefined
    for (let responseIndex = 0; responseIndex < 4; responseIndex += 1) {
      const responseId = `pool_resp_${sessionIndex}_${responseIndex}`
      await saveResponse(responseId, sessionId, previousResponseId, futureExpiresAt, responseIndex)
      previousResponseId = responseId
    }
    return previousResponseId
  }))
  return tails.filter((value): value is string => Boolean(value))
}

async function assertResponseChainRestores(tails: string[]): Promise<void> {
  await Promise.all(tails.map(async (responseId) => {
    const result = await writerPool.readCodexContextResponseStateChainWithWriterPool({
      responseId,
      boundary,
      maxDepth: 8,
      now: cleanupNow,
      refreshExpiresAt: futureExpiresAt
    })
    assert.equal(result.outcome, 'found', `response chain ${responseId} 应可恢复`)
    if (result.outcome === 'found') {
      assert.equal(result.responses.length, 4, '每条 response chain 应保留 4 轮')
    }
  }))

  const mismatch = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: tails[0] ?? '',
    boundary: { ...boundary, groupId: 'wrong_group' },
    maxDepth: 8,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(mismatch.outcome, 'boundary_mismatch', '跨 group 的 previous_response_id 必须被拒绝')
}

async function assertCompactState(): Promise<void> {
  const compact = await writerPool.saveCodexContextCompactStateIndexWithWriterPool({
    compactId: 'cmp_writer_pool',
    sessionId: 'pool_session_0',
    sourceResponseId: 'pool_resp_0_3',
    summaryDigest: digestLike('c', 0),
    ...boundary,
    storageKey: 'sessions/compact/segments/2026062200.json.gz',
    storageOffsetBytes: 0,
    sha256: digestLike('d', 0),
    rawSizeBytes: 100,
    compressedSizeBytes: 80,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: futureExpiresAt
  })
  assert.equal(compact.compactId, 'cmp_writer_pool')

  const found = await writerPool.readCodexContextCompactStateWithWriterPool({
    compactId: 'cmp_writer_pool',
    boundary,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(found.outcome, 'found', 'compact state 应可读取')

  const mismatch = await writerPool.readCodexContextCompactStateWithWriterPool({
    compactId: 'cmp_writer_pool',
    boundary: { ...boundary, providerCode: 'glm' },
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(mismatch.outcome, 'boundary_mismatch', '跨供应商 compact state 必须被拒绝')
}

async function assertCleanupBarrierAndStorageKeyProtection(): Promise<void> {
  await saveResponse('active_shared_resp', 'active_shared_session', undefined, futureExpiresAt, 0, sharedStorageKey)

  for (let sessionIndex = 0; sessionIndex < 4; sessionIndex += 1) {
    let previousResponseId: string | undefined
    for (let responseIndex = 0; responseIndex < 3; responseIndex += 1) {
      const responseId = `expired_pool_resp_${sessionIndex}_${responseIndex}`
      await saveResponse(
        responseId,
        `expired_pool_session_${sessionIndex}`,
        previousResponseId,
        expiredAt,
        responseIndex,
        sessionIndex === 0 && responseIndex === 0 ? sharedStorageKey : undefined
      )
      previousResponseId = responseId
    }
  }

  const inFlightWrites = Promise.all(Array.from({ length: 12 }, async (_, index) => {
    await saveResponse(`barrier_future_resp_${index}`, `barrier_future_session_${index}`, undefined, futureExpiresAt, index)
  }))
  const cleanup = writerPool.cleanupExpiredCodexContextStatesWithWriterPool({
    expiredBefore: cleanupNow,
    limit: 1000
  })
  const [firstCleanupResult] = await Promise.all([cleanup, inFlightWrites])
  const cleanupResults = [firstCleanupResult]
  for (let index = 1; index < databaseModule.codexContextStateShardCount(); index += 1) {
    cleanupResults.push(await writerPool.cleanupExpiredCodexContextStatesWithWriterPool({
      expiredBefore: cleanupNow,
      limit: 1000
    }))
  }
  const cleanupResult = cleanupResults.reduce((total, result) => ({
    deletedSessions: total.deletedSessions + result.deletedSessions,
    deletedResponses: total.deletedResponses + result.deletedResponses,
    deletedCompacts: total.deletedCompacts + result.deletedCompacts,
    storageKeys: [...new Set([...total.storageKeys, ...result.storageKeys])],
    hasMore: total.hasMore || result.hasMore
  }), {
    deletedSessions: 0,
    deletedResponses: 0,
    deletedCompacts: 0,
    storageKeys: [] as string[],
    hasMore: false
  })

  assert.equal(cleanupResult.deletedResponses, 12, 'cleanup 应只删除过期 response')
  assert.equal(cleanupResult.deletedSessions, 4, 'cleanup 应只删除过期 session')
  assert.equal(cleanupResult.storageKeys.includes(sharedStorageKey), false, '仍被活跃 response 引用的 storageKey 不能返回删除')

  const activeShared = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: 'active_shared_resp',
    boundary,
    maxDepth: 2,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(activeShared.outcome, 'found', 'cleanup 不能误删活跃 shared storage response')

  const deletedExpired = await writerPool.readCodexContextResponseStateChainWithWriterPool({
    responseId: 'expired_pool_resp_0_2',
    boundary,
    maxDepth: 4,
    now: cleanupNow,
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(deletedExpired.outcome, 'not_found', '过期 response 清理后应不可恢复')
}

function assertShardDistribution(): void {
  const counts = databaseModule.codexContextStateShardIndexes().map((shardIndex) => {
    const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
    const row = database.prepare('SELECT COUNT(*) AS count FROM codex_context_responses').get() as { count?: number | bigint } | undefined
    return Number(row?.count ?? 0)
  })
  assert(counts.reduce((sum, count) => sum + count, 0) >= 64, 'response rows 应写入 shard')
  assert(counts.filter((count) => count > 0).length > 1, 'response rows 应分散到多个 shard')
}

async function saveResponse(
  responseId: string,
  sessionId: string,
  previousResponseId: string | undefined,
  expiresAt: string,
  index: number,
  storageKey = `sessions/${sessionId}/segments/2026062200.json.gz`
): Promise<void> {
  await writerPool.saveCodexContextResponseStateIndexWithWriterPool({
    responseId,
    sessionId,
    previousResponseId,
    ...boundary,
    upstreamAccountId: 'acct_writer_pool',
    model: 'deepseek-v4-flash',
    upstreamModel: 'deepseek-v4-flash',
    storageKey,
    storageOffsetBytes: index * 128,
    sha256: digestLike('a', index + responseId.length),
    rawSizeBytes: 120 + index,
    compressedSizeBytes: 90 + index,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt
  })
}

function digestLike(seed: string, index: number): string {
  return `${seed}${index.toString(16).padStart(8, '0')}${seed.repeat(64)}`.slice(0, 64)
}
