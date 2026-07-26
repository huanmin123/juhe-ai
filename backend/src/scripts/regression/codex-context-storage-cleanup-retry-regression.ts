import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-codex-context-cleanup-retry-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context-state')
runtimeConfig.codexContextStateShardCount = 2
runtimeConfig.codexContextStateWriterPoolEnabled = false
runtimeConfig.secret = 'codex-context-cleanup-retry-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repository, cleanupService, postgresSchema] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/codex-context-state.repository.js'),
  import('../../modules/background/codex-context-storage-cleanup.service.js'),
  import('../../storage/postgres-schema.js')
])

const boundary = {
  systemAccountId: 'sys_cleanup_retry',
  apiKeyId: 'apikey_cleanup_retry',
  groupId: 'group_cleanup_retry',
  providerCode: 'openai'
}
const expiredAt = '2000-01-01T00:00:00.000Z'
const failedStorageKey = 'sessions/a-failed/segments/2000010100.json.gz'
const successfulStorageKey = 'sessions/b-success/segments/2000010100.json.gz'

try {
  const postgresSchemaSql = postgresSchema.buildPostgresSchemaSql()
  assert.match(postgresSchemaSql, /CREATE TABLE IF NOT EXISTS codex_context_storage_cleanup_queue/u)
  assert.match(postgresSchemaSql, /CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due/u)

  seedExpiredResponse('resp_cleanup_failed', 'session_cleanup_failed', failedStorageKey)
  seedExpiredResponse('resp_cleanup_success', 'session_cleanup_success', successfulStorageKey)
  createNonEmptyDirectory(storagePath(failedStorageKey))
  createFile(storagePath(successfulStorageKey))

  const cleanup = repository.cleanupExpiredCodexContextStates({
    expiredBefore: '2026-07-26T00:00:00.000Z',
    limit: 10
  })
  assert.deepEqual(new Set(cleanup.storageKeys), new Set([failedStorageKey, successfulStorageKey]))
  assert.equal(repository.readCodexContextResponseStateRow('resp_cleanup_failed'), undefined, '过期索引应完成数据库清理')
  assert.equal(cleanupQueueCount(), 2, '数据库索引删除前必须把文件 key 持久入队')

  const firstDeletion = await cleanupService.deleteCodexContextStorageKeys(cleanup.storageKeys)
  assert.equal(firstDeletion.deleted, 1, '单个文件失败不能阻止同批其他文件删除')
  assert.deepEqual(firstDeletion.succeededStorageKeys, [successfulStorageKey])
  assert.equal(firstDeletion.failures.length, 1)
  assert.equal(firstDeletion.failures[0]?.storageKey, failedStorageKey)
  assert.equal(existsSync(storagePath(successfulStorageKey)), false)

  const settled = repository.settleCodexContextStorageCleanup({
    succeededStorageKeys: firstDeletion.succeededStorageKeys,
    failures: firstDeletion.failures
  })
  assert.equal(settled.acknowledged, 1, '成功文件应从持久队列确认移除')
  assert.equal(settled.deferred, 1, '失败文件应记录退避等待重试')
  assert.equal(cleanupQueueCount(), 1, '删除失败后必须保留唯一持久线索')
  const failedQueueRow = cleanupQueueRow(failedStorageKey)
  assert.equal(failedQueueRow?.attempt_count, 1)
  assert(String(failedQueueRow?.last_error ?? '').length > 0)

  const duringBackoff = repository.cleanupExpiredCodexContextStates({
    expiredBefore: '2026-07-26T00:00:00.000Z',
    limit: 10
  })
  assert.deepEqual(duringBackoff.storageKeys, [], '失败 key 在退避到期前不能形成热循环')

  rmSync(storagePath(failedStorageKey), { recursive: true, force: true })
  createFile(storagePath(failedStorageKey))
  forceCleanupRetryDue(failedStorageKey)
  const retryCleanup = repository.cleanupExpiredCodexContextStates({
    expiredBefore: '2026-07-26T00:00:00.000Z',
    limit: 10
  })
  assert.deepEqual(retryCleanup.storageKeys, [failedStorageKey], '即使原索引已删除，持久队列仍应恢复失败文件重试')
  const retryDeletion = await cleanupService.deleteCodexContextStorageKeys(retryCleanup.storageKeys)
  assert.equal(retryDeletion.deleted, 1)
  repository.settleCodexContextStorageCleanup({
    succeededStorageKeys: retryDeletion.succeededStorageKeys,
    failures: retryDeletion.failures
  })
  assert.equal(cleanupQueueCount(), 0)
  assert.equal(existsSync(storagePath(failedStorageKey)), false)

  const missingDeletion = await cleanupService.deleteCodexContextStorageKeys(['sessions/already-missing/segment.json.gz'])
  assert.equal(missingDeletion.deleted, 0)
  assert.deepEqual(missingDeletion.succeededStorageKeys, ['sessions/already-missing/segment.json.gz'], '文件已不存在应按幂等成功确认')

  const abortController = new AbortController()
  let settledAfterAbort = false
  await assert.rejects(
    cleanupService.processCodexContextStorageCleanupBatch({
      storageKeys: ['sessions/abort/segment.json.gz'],
      signal: abortController.signal,
      dependencies: {
        deleteStorageKeys: async () => {
          abortController.abort(new Error('stop after current batch'))
          return {
            deleted: 1,
            succeededStorageKeys: ['sessions/abort/segment.json.gz'],
            failures: []
          }
        },
        settle: async () => {
          settledAfterAbort = true
        }
      }
    }),
    /stop after current batch/,
    'abort 应在当前批文件删除和数据库确认完成后抛出'
  )
  assert.equal(settledAfterAbort, true, 'abort 发生在文件删除期间时仍必须完成当前批数据库确认')

  console.log('Codex Context 文件清理重试回归通过：删除失败持久退避、同批继续、缺失幂等、abort 当前批收尾')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedExpiredResponse(responseId: string, sessionId: string, storageKey: string): void {
  repository.saveCodexContextResponseStateIndex({
    responseId,
    sessionId,
    ...boundary,
    storageKey,
    storageOffsetBytes: 0,
    sha256: 'a'.repeat(64),
    rawSizeBytes: 1,
    compressedSizeBytes: 1,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: expiredAt
  })
}

function storagePath(storageKey: string): string {
  return resolve(runtimeConfig.codexContextRoot, storageKey)
}

function createFile(filePath: string): void {
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, 'fixture')
}

function createNonEmptyDirectory(directoryPath: string): void {
  mkdirSync(directoryPath, { recursive: true })
  writeFileSync(join(directoryPath, 'keep.txt'), 'force rm without recursive to fail')
}

function cleanupQueueCount(): number {
  return databaseModule.codexContextStateShardIndexes().reduce((total, shardIndex) => {
    const row = databaseModule.getCodexContextStateShardDatabase(shardIndex)
      .prepare('SELECT COUNT(*) AS total FROM codex_context_storage_cleanup_queue')
      .get() as { total?: number | bigint } | undefined
    return total + Number(row?.total ?? 0)
  }, 0)
}

function cleanupQueueRow(storageKey: string): { attempt_count: number; last_error?: string | null } | undefined {
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    const row = databaseModule.getCodexContextStateShardDatabase(shardIndex)
      .prepare('SELECT attempt_count, last_error FROM codex_context_storage_cleanup_queue WHERE storage_key = ?')
      .get(storageKey) as { attempt_count?: number | bigint; last_error?: string | null } | undefined
    if (row) return { attempt_count: Number(row.attempt_count ?? 0), last_error: row.last_error }
  }
  return undefined
}

function forceCleanupRetryDue(storageKey: string): void {
  for (const shardIndex of databaseModule.codexContextStateShardIndexes()) {
    databaseModule.getCodexContextStateShardDatabase(shardIndex)
      .prepare('UPDATE codex_context_storage_cleanup_queue SET next_attempt_at = ? WHERE storage_key = ?')
      .run(expiredAt, storageKey)
  }
}
