import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'Codex Context 文件清理 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `codex_context_cleanup_pg_${Date.now()}_${Math.random().toString(16).slice(2)}`
const tempRoot = resolve(tmpdir(), marker)
const storageKey = `sessions/${marker}/segments/2000010100.json.gz`
const responseId = `resp_${marker}`
const sessionId = `session_${marker}`
const expiredAt = '2000-01-01T00:00:00.000Z'
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
mkdirSync(tempRoot, { recursive: true })

const [repository, cleanupService] = await Promise.all([
  import('../../storage/codex-context-state.repository.js'),
  import('../../modules/background/codex-context-storage-cleanup.service.js')
])
const client = createPostgresDatabaseClient(await getPostgresPool())

try {
  await cleanupRows()
  await repository.saveCodexContextResponseStateIndexAsync({
    responseId,
    sessionId,
    systemAccountId: `sys_${marker}`,
    apiKeyId: `apikey_${marker}`,
    groupId: `group_${marker}`,
    providerCode: 'openai',
    storageKey,
    storageOffsetBytes: 0,
    sha256: 'a'.repeat(64),
    rawSizeBytes: 1,
    compressedSizeBytes: 1,
    compression: 'gzip',
    schemaVersion: 2,
    expiresAt: expiredAt
  })
  createNonEmptyDirectory(storagePath())

  const cleanup = await repository.cleanupExpiredCodexContextStatesAsync({
    expiredBefore: '2026-07-26T00:00:00.000Z',
    limit: 10
  })
  assert.deepEqual(cleanup.storageKeys, [storageKey])
  assert.equal(await responseRowCount(), 0, 'PG 过期索引应在文件删除前完成清理')
  assert.equal(await queueRowCount(), 1, 'PG 删除索引的同一事务必须持久入队 storage key')

  const firstDeletion = await cleanupService.deleteCodexContextStorageKeys(cleanup.storageKeys)
  assert.equal(firstDeletion.failures.length, 1)
  const failedSettlement = await repository.settleCodexContextStorageCleanupAsync({
    succeededStorageKeys: firstDeletion.succeededStorageKeys,
    failures: firstDeletion.failures
  })
  assert.equal(failedSettlement.deferred, 1)
  assert.equal(await queueRowCount(), 1, 'PG 文件删除失败后必须保留待删线索')

  rmSync(storagePath(), { recursive: true, force: true })
  createFile(storagePath())
  await client.execute(`
    UPDATE juhe_codex_context.codex_context_storage_cleanup_queue
    SET next_attempt_at = ?
    WHERE storage_key = ?
  `, [expiredAt, storageKey])
  const retry = await repository.cleanupExpiredCodexContextStatesAsync({
    expiredBefore: '2026-07-26T00:00:00.000Z',
    limit: 10
  })
  assert.deepEqual(retry.storageKeys, [storageKey], '原索引已删除后 PG 队列仍应恢复重试')
  const retryDeletion = await cleanupService.deleteCodexContextStorageKeys(retry.storageKeys)
  assert.equal(retryDeletion.deleted, 1)
  const successSettlement = await repository.settleCodexContextStorageCleanupAsync({
    succeededStorageKeys: retryDeletion.succeededStorageKeys,
    failures: retryDeletion.failures
  })
  assert.equal(successSettlement.acknowledged, 1)
  assert.equal(await queueRowCount(), 0)
  assert.equal(existsSync(storagePath()), false)

  console.log(JSON.stringify({
    message: 'Codex Context 文件清理 PG smoke 通过',
    durableRetry: true,
    idempotentSettlement: true
  }))
} finally {
  await cleanupRows()
  await closePostgresPool()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function cleanupRows(): Promise<void> {
  await client.execute('DELETE FROM juhe_codex_context.codex_context_responses WHERE response_id = ?', [responseId])
  await client.execute('DELETE FROM juhe_codex_context.codex_context_compacts WHERE session_id = ?', [sessionId])
  await client.execute('DELETE FROM juhe_codex_context.codex_context_sessions WHERE id = ?', [sessionId])
  await client.execute('DELETE FROM juhe_codex_context.codex_context_storage_cleanup_queue WHERE storage_key = ?', [storageKey])
}

async function responseRowCount(): Promise<number> {
  const row = await client.one<{ total?: number | bigint }>(`
    SELECT COUNT(*) AS total
    FROM juhe_codex_context.codex_context_responses
    WHERE response_id = ?
  `, [responseId])
  return Number(row?.total ?? 0)
}

async function queueRowCount(): Promise<number> {
  const row = await client.one<{ total?: number | bigint }>(`
    SELECT COUNT(*) AS total
    FROM juhe_codex_context.codex_context_storage_cleanup_queue
    WHERE storage_key = ?
  `, [storageKey])
  return Number(row?.total ?? 0)
}

function storagePath(): string {
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
