import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { runAccountListAvailabilityProjectionMaintenance } from '../../modules/accounts/account-list-availability-projection.service.js'
import {
  listAccountListAvailabilityProjectionPageInClient
} from '../../storage/account-list-availability-projection.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'

const viewerSystemAccountId = 'account-list-projection-bootstrap-viewer-20260810'
const accountPrefix = 'account-list-projection-bootstrap-account-20260810-'
const rowCount = 20_000

interface MaintenancePass {
  pass: number
  claimed: number
  projected: number
  deleted: number
  staleClaims: number
  released: number
  durationMs: number
}

async function main(): Promise<void> {
  assertScratchDatabase()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await cleanup(client)
  try {
    await seed(client)
    const initialDirty = await countDirty(client)
    assert.equal(initialDirty, rowCount, '20k bootstrap fixture 必须全部进入 durable dirty queue')

    const passes: MaintenancePass[] = []
    const startedAt = performance.now()
    let projected = 0
    for (let pass = 1; pass <= 20 && projected < rowCount; pass += 1) {
      const passStartedAt = performance.now()
      const result = await runAccountListAvailabilityProjectionMaintenance({
        ownerId: `projection-bootstrap-${process.pid}-${pass}`
      })
      const durationMs = performance.now() - passStartedAt
      passes.push({
        pass,
        claimed: result.claimed,
        projected: result.projected,
        deleted: result.deleted,
        staleClaims: result.staleClaims,
        released: result.released,
        durationMs
      })
      assert.equal(result.released, 0, `bootstrap pass ${pass} 不得因读取或写入失败释放任务`)
      projected += result.projected
      if (result.claimed === 0) break
    }
    const materializeDurationMs = performance.now() - startedAt
    assert.equal(projected, rowCount, '默认 maintenance drain 必须在有限 pass 内物化全部 20k 账户')
    assert.equal(await countDirty(client), 0, '完整 bootstrap 后不能残留可处理 dirty 行')

    const page = await listAccountListAvailabilityProjectionPageInClient(client, {
      viewerSystemAccountId,
      options: { status: 'active', page: 1, pageSize: 20 }
    })
    assert.equal(page.items.length, 20, '默认 freshness 内投影筛选页必须可用')
    console.log(JSON.stringify({
      database: scratchDatabaseName(),
      rowCount,
      defaultBatchSize: runtimeConfig.background.accountListAvailabilityProjectionBatchSize,
      defaultMaxBatchesPerRun: runtimeConfig.background.accountListAvailabilityProjectionMaxBatchesPerRun,
      materializeDurationMs,
      passes
    }, null, 2))
  } finally {
    await cleanup(client)
    await closePostgresPool()
  }
}

async function seed(client: DatabaseClient): Promise<void> {
  const now = new Date().toISOString()
  const provider = await client.one<{ code: string }>(`
    SELECT code FROM ${table(client, 'providers')} ORDER BY code ASC LIMIT 1
  `)
  const profile = await client.one<{
    id: string
    provider_code: string
    protocol_code: string
    protocol_version: string
    default_health_check_model: string
  }>(`
    SELECT id, provider_code, protocol_code, protocol_version, default_health_check_model
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(provider && profile, '隔离 PostgreSQL 必须具备 provider/profile seed')
  await client.transaction(async (tx) => {
    await tx.execute("SET LOCAL statement_timeout = '120s'")
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_accounts')} (
        id, username, display_name, role, status, password_hash, created_at, updated_at
      ) VALUES (?, ?, 'Projection bootstrap viewer', 'user', 'active', 'bootstrap-not-a-login-secret', ?, ?)
    `, [viewerSystemAccountId, viewerSystemAccountId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 6, '0'), ?, ?, ?, ?, ?,
        'projection bootstrap account ' || lpad(gs::text, 6, '0'), 'api_key',
        CASE WHEN gs % 97 = 0 THEN 'quality_isolated'
             WHEN gs % 11 = 0 THEN 'temporary_unavailable'
             WHEN gs % 5 = 0 THEN 'rate_limited'
             ELSE 'active' END,
        '{}', gs % 100, CASE WHEN gs % 7 = 0 THEN 0 ELSE 1 END, ?, 'chat_json', ?, ?
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
      rowCount
    ])
  })
}

async function countDirty(client: DatabaseClient): Promise<number> {
  const row = await client.one<{ count: string }>(`
    SELECT count(*)::text AS count
    FROM ${table(client, 'account_list_availability_dirty')}
    WHERE account_id LIKE ?
  `, [`${accountPrefix}%`])
  return Number(row?.count ?? 0)
}

async function cleanup(client: DatabaseClient): Promise<void> {
  await client.transaction(async (tx) => {
    await tx.execute("SET LOCAL statement_timeout = '120s'")
    await tx.execute(`DELETE FROM ${table(tx, 'accounts')} WHERE id LIKE ?`, [`${accountPrefix}%`])
    await tx.execute(`DELETE FROM ${table(tx, 'system_accounts')} WHERE id = ?`, [viewerSystemAccountId])
  })
}

function assertScratchDatabase(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') throw new Error('bootstrap 压测必须在 PostgreSQL 模式运行')
  const name = new URL(runtimeConfig.postgres.url ?? '').pathname.replace(/^\//, '')
  if (!/^juhe_ai_sub2api_dev_[a-z0-9_]{3,80}$/.test(name)) {
    throw new Error(`bootstrap 压测只允许隔离开发库，当前 database=${name}`)
  }
}

function scratchDatabaseName(): string {
  return new URL(runtimeConfig.postgres.url ?? '').pathname.replace(/^\//, '')
}

function table(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_business', name)
}

await main().catch(async (error) => {
  console.error(error instanceof Error ? (error.stack ?? error.message) : String(error))
  await closePostgresPool()
  process.exitCode = 1
})

process.exit(process.exitCode ?? 0)
