import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-context-state-sharding-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 8
runtimeConfig.secret = 'codex-context-state-sharding-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')
const repository = await import('../../storage/codex-context-state.repository.js')

const boundary = {
  systemAccountId: 'sys_admin',
  apiKeyId: 'apikey_codex_context_sharding',
  groupId: 'group_codex_context_sharding',
  providerCode: 'deepseek',
  providerProtocolProfileId: 'profile_deepseek_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

try {
  const futureExpiresAt = '2999-01-01T00:00:00.000Z'
  const expiredAt = '2000-01-01T00:00:00.000Z'
  const activeTail = writeResponseChains({
    sessionPrefix: 'active',
    sessionCount: 32,
    responsesPerSession: 24,
    expiresAt: futureExpiresAt
  })
  const expiredTail = writeResponseChains({
    sessionPrefix: 'expired',
    sessionCount: 10,
    responsesPerSession: 20,
    expiresAt: expiredAt
  })

  const shardCounts = countRowsByShard('codex_context_responses')
  assert(shardCounts.filter((count) => count > 0).length >= 4, `response 索引应分布到多个 shard，实际分布：${shardCounts.join(',')}`)

  const activeTailBeforeRead = readResponseRowFromShard(activeTail)
  const readResult = repository.readCodexContextResponseStateChain({
    responseId: activeTail,
    boundary,
    maxDepth: 64,
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(readResult.outcome, 'found', '跨 shard response chain 应可按 previous_response_id 恢复')
  if (readResult.outcome === 'found') {
    assert.equal(readResult.responses.length, 24, '应恢复完整 response 链')
  }
  const activeTailAfterRead = readResponseRowFromShard(activeTail)
  assert.equal(activeTailAfterRead.updated_at, activeTailBeforeRead.updated_at, '同步 response chain 读取不能刷新 updated_at')
  assert.equal(activeTailAfterRead.last_used_at, activeTailBeforeRead.last_used_at, '同步 response chain 读取不能刷新 last_used_at')
  assert.equal(activeTailAfterRead.expires_at, activeTailBeforeRead.expires_at, '同步 response chain 读取不能刷新 expires_at')

  const compact = repository.saveCodexContextCompactStateIndex({
    compactId: 'cmp_codex_context_sharding',
    sessionId: 'active_session_0',
    sourceResponseId: activeTail,
    summaryDigest: 'a'.repeat(64),
    ...boundary,
    storageKey: 'sessions/active_session_0/segments/2026062200.json.gz',
    storageOffsetBytes: 987654,
    sha256: 'b'.repeat(64),
    rawSizeBytes: 120,
    compressedSizeBytes: 80,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: futureExpiresAt
  })
  const compactBeforeRead = readCompactRowFromShard(compact.compactId)
  const compactRead = repository.readCodexContextCompactState({
    compactId: compact.compactId,
    boundary,
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(compactRead.outcome, 'found', 'compact snapshot 索引应可按 compact_id 从对应 shard 读取')
  const compactAfterRead = readCompactRowFromShard(compact.compactId)
  assert.equal(compactAfterRead.updated_at, compactBeforeRead.updated_at, '同步 compact 读取不能刷新 updated_at')
  assert.equal(compactAfterRead.last_used_at, compactBeforeRead.last_used_at, '同步 compact 读取不能刷新 last_used_at')
  assert.equal(compactAfterRead.expires_at, compactBeforeRead.expires_at, '同步 compact 读取不能刷新 expires_at')

  const compactBoundaryMismatch = repository.readCodexContextCompactState({
    compactId: compact.compactId,
    boundary: {
      ...boundary,
      groupId: 'group_other_boundary'
    },
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(compactBoundaryMismatch.outcome, 'boundary_mismatch', 'compact snapshot 跨分组边界应受控拒绝')

  const sharedStorageKey = 'sessions/shared_collision/segments/2026062200.json.gz'
  const staleActiveCompact = repository.saveCodexContextCompactStateIndex({
    compactId: 'cmp_codex_context_sharding_stale_active',
    sessionId: 'stale_active_compact_session',
    sourceResponseId: activeTail,
    summaryDigest: 'f'.repeat(64),
    ...boundary,
    storageKey: sharedStorageKey,
    storageOffsetBytes: 120,
    sha256: '1'.repeat(64),
    rawSizeBytes: 110,
    compressedSizeBytes: 75,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: futureExpiresAt
  })
  forceSessionExpiresAt(staleActiveCompact.sessionId, expiredAt)

  const expiredCompact = repository.saveCodexContextCompactStateIndex({
    compactId: 'cmp_codex_context_sharding_expired',
    sessionId: 'expired_compact_session',
    sourceResponseId: activeTail,
    summaryDigest: 'd'.repeat(64),
    ...boundary,
    storageKey: sharedStorageKey,
    storageOffsetBytes: 0,
    sha256: 'e'.repeat(64),
    rawSizeBytes: 100,
    compressedSizeBytes: 70,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: expiredAt
  })
  const expiredCompactRead = repository.readCodexContextCompactState({
    compactId: expiredCompact.compactId,
    boundary,
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(expiredCompactRead.outcome, 'expired', 'compact snapshot 过期后应受控拒绝')

  const cleanup = repository.cleanupExpiredCodexContextStates({
    expiredBefore: '2026-06-22T00:00:00.000Z',
    limit: 1000
  })
  assert.equal(cleanup.deletedSessions, 11, '应按过期 session 清理')
  assert.equal(cleanup.deletedResponses, 200, '过期 session 下的 response rows 应跨 shard 清理')
  assert.equal(cleanup.deletedCompacts, 1, '过期 session 下的 compact rows 应跨 shard 清理')
  assert(cleanup.storageKeys.length <= 10, `segment storage key 应按 session/hour 去重，不应每轮一个文件，实际 ${cleanup.storageKeys.length}`)
  assert(!cleanup.storageKeys.includes(sharedStorageKey), '仍被未过期 compact 引用的 shared segment 不能返回给文件删除器')

  const staleActiveCompactRead = repository.readCodexContextCompactState({
    compactId: staleActiveCompact.compactId,
    boundary,
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(staleActiveCompactRead.outcome, 'found', 'session 行过期但子 compact 未过期时，cleanup 应刷新 session 并保留 compact')

  const expiredRead = repository.readCodexContextResponseStateChain({
    responseId: expiredTail,
    boundary,
    maxDepth: 64,
    now: '2026-06-22T00:00:00.000Z',
    refreshExpiresAt: futureExpiresAt
  })
  assert.equal(expiredRead.outcome, 'not_found', '过期 session 清理后旧 response id 应不可恢复')

  console.log('Responses bridge state sharding 回归通过：response/compact 索引按 shard 分布、链路可恢复、TTL 清理按 session 删除且 segment key 去重')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function writeResponseChains(input: {
  sessionPrefix: string
  sessionCount: number
  responsesPerSession: number
  expiresAt: string
}): string {
  let lastResponseId = ''
  for (let sessionIndex = 0; sessionIndex < input.sessionCount; sessionIndex += 1) {
    const sessionId = `${input.sessionPrefix}_session_${sessionIndex}`
    let previousResponseId: string | undefined
    for (let responseIndex = 0; responseIndex < input.responsesPerSession; responseIndex += 1) {
      const responseId = `resp_${input.sessionPrefix}_${sessionIndex}_${responseIndex}`
      repository.saveCodexContextResponseStateIndex({
        responseId,
        sessionId,
        previousResponseId,
        ...boundary,
        upstreamAccountId: `acct_${sessionIndex % 5}`,
        model: 'deepseek-v4-flash',
        upstreamModel: 'deepseek-v4-flash',
        storageKey: `sessions/${sessionId}/segments/2026062200.json.gz`,
        storageOffsetBytes: responseIndex * 128,
        sha256: `${String(sessionIndex).padStart(2, '0')}${String(responseIndex).padStart(2, '0')}${'c'.repeat(60)}`.slice(0, 64),
        rawSizeBytes: 100 + responseIndex,
        compressedSizeBytes: 80 + responseIndex,
        compression: 'gzip',
        schemaVersion: 2,
        expiresAt: input.expiresAt
      })
      previousResponseId = responseId
      lastResponseId = responseId
    }
  }
  return lastResponseId
}

function countRowsByShard(table: string): number[] {
  return databaseModule.codexContextStateShardIndexes().map((shardIndex) => {
    const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
    const row = database.prepare(`SELECT COUNT(*) AS count FROM ${table}`).get() as { count?: number | bigint } | undefined
    return Number(row?.count ?? 0)
  })
}

function forceSessionExpiresAt(sessionId: string, expiresAt: string): void {
  const shardIndex = databaseModule.codexContextStateShardIndexForKey(sessionId)
  const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
  database.prepare('UPDATE codex_context_sessions SET expires_at = ? WHERE id = ?').run(expiresAt, sessionId)
}

function readResponseRowFromShard(responseId: string): { updated_at: string; last_used_at: string; expires_at: string } {
  const shardIndex = databaseModule.codexContextStateShardIndexForKey(responseId)
  const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
  const row = database
    .prepare('SELECT updated_at, last_used_at, expires_at FROM codex_context_responses WHERE response_id = ?')
    .get(responseId) as { updated_at?: string; last_used_at?: string; expires_at?: string } | undefined
  assert(row?.updated_at && row.last_used_at && row.expires_at, `response ${responseId} 应存在`)
  return { updated_at: row.updated_at, last_used_at: row.last_used_at, expires_at: row.expires_at }
}

function readCompactRowFromShard(compactId: string): { updated_at: string; last_used_at: string; expires_at: string } {
  const shardIndex = databaseModule.codexContextStateShardIndexForKey(compactId)
  const database = databaseModule.getCodexContextStateShardDatabase(shardIndex)
  const row = database
    .prepare('SELECT updated_at, last_used_at, expires_at FROM codex_context_compacts WHERE compact_id = ?')
    .get(compactId) as { updated_at?: string; last_used_at?: string; expires_at?: string } | undefined
  assert(row?.updated_at && row.last_used_at && row.expires_at, `compact ${compactId} 应存在`)
  return { updated_at: row.updated_at, last_used_at: row.last_used_at, expires_at: row.expires_at }
}
