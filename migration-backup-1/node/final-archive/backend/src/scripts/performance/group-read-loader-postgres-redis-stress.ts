import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { loadGroupAccountIdsByGroupIdsAsync } from '../../storage/group-read-loaders.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients, createDedicatedRedisClient } from '../../shared/redis-client.js'
import { redisNamespacedKey } from '../../shared/redis-namespace.js'

const groupCount = 500
const suffix = `${Date.now()}_${Math.random().toString(16).slice(2)}`
const viewerSystemAccountId = `group-loader-stress-viewer-${suffix}`
const groupPrefix = `group-loader-stress-group-${suffix}-`
const accountPrefix = `group-loader-stress-account-${suffix}-`

async function main(): Promise<void> {
  assertScratchDatabase()
  const cacheUrl = runtimeConfig.redis.cacheUrl
  if (!cacheUrl) throw new Error('分组 Redis 压测需要 JUHE_AI_REDIS_CACHE_URL')

  // The namespace is process-private so cleanup can scan and unlink the
  // entire test prefix without sharing a keyspace with local development.
  runtimeConfig.cacheDriver = 'redis'
  runtimeConfig.redis.namespace = `group-loader-stress-${suffix}`

  const redisClient = await createDedicatedRedisClient(cacheUrl)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const redisPattern = redisNamespacedKey('juhe-ai:*')
  try {
    await clearRedisKeys(redisClient, redisPattern)
    await cleanup(client)
    await seed(client)

    const groupIds = Array.from({ length: groupCount }, (_, index) => `${groupPrefix}${String(index + 1).padStart(4, '0')}`)
    const cold = await measure(() => loadGroupAccountIdsByGroupIdsAsync(groupIds))
    assertGroupAccountIds(cold.value, groupIds)

    // Redis mode intentionally bypasses process-local cache. This second read
    // therefore proves the bounded shared-cache path, rather than merely a
    // Map hit in this Node process.
    const warm = await measure(() => loadGroupAccountIdsByGroupIdsAsync(groupIds))
    assertGroupAccountIds(warm.value, groupIds)
    assert(warm.durationMs < 5_000, `500 个分组 Redis 热读取必须在 5 秒内完成，实际 ${warm.durationMs.toFixed(1)}ms`)

    console.log(JSON.stringify({
      database: scratchDatabaseName(),
      redisNamespace: runtimeConfig.redis.namespace,
      groupCount,
      coldMs: cold.durationMs,
      warmMs: warm.durationMs
    }, null, 2))
  } finally {
    await cleanup(client).catch(() => undefined)
    await clearRedisKeys(redisClient, redisPattern).catch(() => undefined)
    await closeDedicatedRedisClient(redisClient)
    await closeRedisClients()
    await closePostgresPool()
  }
}

async function seed(client: DatabaseClient): Promise<void> {
  const now = new Date().toISOString()
  const provider = await client.one<{ code: string }>(`
    SELECT code
    FROM ${table(client, 'providers')}
    ORDER BY code ASC
    LIMIT 1
  `)
  const profile = await client.one<{
    id: string
    protocol_code: string
    protocol_version: string
    default_health_check_model: string
  }>(`
    SELECT id, protocol_code, protocol_version, default_health_check_model
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(provider && profile, '隔离 PostgreSQL 必须已完成默认 provider/profile 初始化')
  await client.transaction(async (tx) => {
    await tx.execute("SET LOCAL statement_timeout = '30s'")
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_accounts')} (
        id, username, display_name, role, status, password_hash, created_at, updated_at
      ) VALUES (?, ?, 'Group loader stress viewer', 'user', 'active', 'group-loader-not-a-login-secret', ?, ?)
    `, [viewerSystemAccountId, viewerSystemAccountId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'groups')} (
        id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 4, '0'), ?,
        'Group loader stress ' || lpad(gs::text, 4, '0'), ?, 1, 'personal', ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [groupPrefix, viewerSystemAccountId, provider.code, now, now, groupCount])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 4, '0'), ?, ?, ?, ?, ?,
        'Group loader account ' || lpad(gs::text, 4, '0'), 'api_key', 'active', '{}',
        gs, 1, ?, 'chat_json', ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [
      accountPrefix,
      viewerSystemAccountId,
      provider.code,
      profile.id,
      profile.protocol_code,
      profile.protocol_version,
      profile.default_health_check_model,
      now,
      now,
      groupCount
    ])
    await tx.execute(`
      INSERT INTO ${table(tx, 'group_accounts')} (
        system_account_id, group_id, account_id, enabled, created_at, updated_at
      )
      SELECT ?, ? || lpad(gs::text, 4, '0'), ? || lpad(gs::text, 4, '0'), 1, ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [viewerSystemAccountId, groupPrefix, accountPrefix, now, now, groupCount])
  })
}

async function cleanup(client: DatabaseClient): Promise<void> {
  await client.transaction(async (tx) => {
    await tx.execute(`DELETE FROM ${table(tx, 'group_accounts')} WHERE group_id LIKE ?`, [`${groupPrefix}%`])
    await tx.execute(`DELETE FROM ${table(tx, 'groups')} WHERE id LIKE ?`, [`${groupPrefix}%`])
    await tx.execute(`DELETE FROM ${table(tx, 'accounts')} WHERE id LIKE ?`, [`${accountPrefix}%`])
    await tx.execute(`DELETE FROM ${table(tx, 'system_accounts')} WHERE id = ?`, [viewerSystemAccountId])
  })
}

function assertGroupAccountIds(values: Map<string, string[]>, groupIds: string[]): void {
  assert.equal(values.size, groupCount, '500 个分组必须都返回成员映射')
  for (const [index, groupId] of groupIds.entries()) {
    assert.deepEqual(
      values.get(groupId),
      [`${accountPrefix}${String(index + 1).padStart(4, '0')}`],
      `分组 ${groupId} 的成员映射不能丢失或错序`
    )
  }
}

async function measure<T>(operation: () => Promise<T>): Promise<{ value: T; durationMs: number }> {
  const startedAt = performance.now()
  const value = await operation()
  return { value, durationMs: performance.now() - startedAt }
}

async function clearRedisKeys(client: { sendCommand(command: string[]): Promise<unknown> }, pattern: string): Promise<void> {
  let cursor = '0'
  do {
    const response = await client.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '200'])
    const [nextCursor, keys] = parseScanResponse(response)
    cursor = nextCursor
    for (let index = 0; index < keys.length; index += 100) {
      const chunk = keys.slice(index, index + 100)
      if (chunk.length > 0) await client.sendCommand(['UNLINK', ...chunk])
    }
  } while (cursor !== '0')
}

function parseScanResponse(value: unknown): [string, string[]] {
  if (!Array.isArray(value)) return ['0', []]
  return [String(value[0] ?? '0'), Array.isArray(value[1]) ? value[1].map(String) : []]
}

async function closeDedicatedRedisClient(client: { quit?: () => Promise<unknown>; destroy?: () => void }): Promise<void> {
  if (client.quit) {
    await client.quit().catch(() => undefined)
    return
  }
  client.destroy?.()
}

function assertScratchDatabase(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') throw new Error('分组 PG/Redis 压测必须在 PostgreSQL 模式执行')
  const database = scratchDatabaseName()
  if (!/^juhe_ai_sub2api_dev_[a-z0-9_]{3,80}$/.test(database)) {
    throw new Error(`分组 PG/Redis 压测只允许隔离开发库，当前 database=${database}`)
  }
}

function scratchDatabaseName(): string {
  return new URL(runtimeConfig.postgres.url ?? '').pathname.replace(/^\//, '')
}

function table(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_business', name)
}

await main().catch((error) => {
  console.error(error instanceof Error ? (error.stack ?? error.message) : String(error))
  process.exitCode = 1
})
