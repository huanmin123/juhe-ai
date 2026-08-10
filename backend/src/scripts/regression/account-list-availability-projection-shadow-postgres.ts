import assert from 'node:assert/strict'
import http from 'node:http'

import { runAccountListAvailabilityProjectionMaintenance } from '../../modules/accounts/account-list-availability-projection.service.js'
import { shadowCompareAccountListAvailabilityProjectionPage } from '../../modules/accounts/account-list-availability-projection-shadow.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import {
  claimAccountListAvailabilityDirtyInClient,
  releaseAccountListAvailabilityDirtyForReplayInClient
} from '../../storage/account-list-availability-projection.repository.js'
import { accountNameSearchQueryTerms } from '../../storage/account-name-search.repository.js'
import { createPostgresDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createSessionAsync } from '../../storage/repositories.js'
import { runtimeConfig } from '../../config/runtime.js'

const viewerId = 'account-list-projection-shadow-viewer-20260810'
const accountPrefix = 'account-list-projection-shadow-account-20260810-'
const groupId = 'account-list-projection-shadow-group-20260810'
const tagId = 'account-list-projection-shadow-tag-20260810'
const accountCount = 32

async function main(): Promise<void> {
  assertScratchDatabase()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await cleanup(client)
  try {
    const profile = await seed(client)
    const initialDirty = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_dirty')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(Number(initialDirty?.count ?? 0), accountCount, '账户创建和绑定写入必须在事务内留下每账户一个脏标记')
    const preflightClaims = await claimAccountListAvailabilityDirtyInClient(client, {
      ownerId: 'account-list-projection-shadow-preflight',
      limit: 100,
      leaseMs: 30_000
    })
    assert.equal(preflightClaims.length, accountCount, 'PostgreSQL 脏队列必须可由 worker 认领')
    for (const claim of preflightClaims) {
      assert(await releaseAccountListAvailabilityDirtyForReplayInClient(client, {
        accountId: claim.accountId,
        generation: claim.generation,
        claimToken: claim.claimToken,
        reason: 'shadow_preflight_replay',
        retryDelayMs: 0
      }), '预检认领必须可释放给正式 materializer')
    }
    const maintenance = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-regression',
      batchSize: 100,
      maximumProjectionAgeMs: 60_000
    })
    assert.equal(maintenance.claimed, accountCount, '影子对账前必须完整认领初始投影脏标记')
    assert.equal(maintenance.projected, accountCount, '影子对账前必须完整物化账户投影')
    assert.equal(maintenance.released, 0, '初始影子投影不应需要重放')

    const access = { systemAccountId: viewerId, role: 'user' as const }
    const cases = [
      { name: 'default', options: { page: 1, pageSize: 20 } },
      { name: 'status-active', options: { page: 1, pageSize: 20, status: 'active' } },
      { name: 'schedulable-enabled', options: { page: 1, pageSize: 20, schedulable: 'enabled' as const } },
      { name: 'group-status', options: { page: 1, pageSize: 20, groupId, status: 'active' } },
      { name: 'tag-status', options: { page: 1, pageSize: 20, tagIds: [tagId], status: 'active' } },
      { name: 'keyword', options: { page: 1, pageSize: 20, keyword: 'shadow account' } },
      { name: 'provider', options: { page: 1, pageSize: 20, providerCode: profile.providerCode } }
    ]
    for (const testCase of cases) {
      const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
        access,
        options: testCase.options,
        maximumProjectionAgeMs: 60_000
      })
      assert.equal(comparison.outcome, 'equal', `影子对账 ${testCase.name} 必须与旧链路完全一致：${comparison.reason ?? ''}`)
    }

    await seedIndexedKeywordDocuments(client)
    const indexedDirty = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_dirty')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(Number(indexedDirty?.count ?? 0), accountCount, '名称检索文档完成后必须重建每个账户投影')
    const indexedMaintenance = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-indexed-keyword',
      batchSize: 100,
      maximumProjectionAgeMs: 60_000
    })
    assert.equal(indexedMaintenance.claimed, accountCount, '检索文档变更必须完整领取脏投影')
    assert.equal(indexedMaintenance.projected, accountCount, '检索文档变更必须完整重投影')
    const expectedKeywordTermCount = accountNameSearchQueryTerms('shadow account').length
    const indexedProjectionTerms = await client.one<{ count: string }>(`
      SELECT count(*)::text AS count
      FROM ${table(client, 'account_list_availability_projection_search_terms')}
      WHERE account_id LIKE ?
    `, [`${accountPrefix}%`])
    assert.equal(
      Number(indexedProjectionTerms?.count ?? 0),
      accountCount * expectedKeywordTermCount,
      '已完成检索文档的全部检索词必须写入账户投影'
    )
    const indexedTermValues = await client.query<{ term: string }>(`
      SELECT DISTINCT term
      FROM ${table(client, 'account_list_availability_projection_search_terms')}
      WHERE account_id LIKE ?
      ORDER BY term ASC
    `, [`${accountPrefix}%`])
    assert.deepEqual(
      indexedTermValues.map((row) => row.term),
      [...accountNameSearchQueryTerms('shadow account')].sort(),
      '投影检索词值必须与来源检索文档一致'
    )
    const indexedKeyword = await shadowCompareAccountListAvailabilityProjectionPage(client, {
      access,
      options: { page: 1, pageSize: 20, keyword: 'shadow account' },
      maximumProjectionAgeMs: 60_000
    })
    assert.equal(indexedKeyword.outcome, 'equal', `已完成关键词索引必须与旧链路一致：${indexedKeyword.reason ?? ''}`)
    assert.equal(indexedKeyword.legacyItemIds.length, 20, '已完成关键词索引应返回第一页完整结果')
    await verifyHttpProjectionReadPath(client)
    await verifyProjectionDeletionRecovery(client, access)
    console.log('account list availability projection PostgreSQL shadow regression passed')
  } finally {
    await cleanup(client)
    await closePostgresPool()
  }
}

async function verifyHttpProjectionReadPath(client: DatabaseClient): Promise<void> {
  const previousReadEnabled = runtimeConfig.background.accountListAvailabilityProjectionReadEnabled
  let server: http.Server | undefined
  try {
    runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = true
    const session = await createSessionAsync(viewerId, 1)
    const app = createSystemApiApp({
      systemApiPrefix: '/__aisys__/api',
      trustProxy: true,
      bypassSystemApiRateLimitForTest: true
    })
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const address = server.address()
    assert(address && typeof address !== 'string', 'HTTP 投影回归服务必须分配 TCP 端口')
    const baseUrl = `http://127.0.0.1:${address.port}`
    const cookie = `juhe_ai_session=${session.token}`

    const healthy = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const healthyBody = await healthy.text()
    assert.equal(healthy.status, 200, `投影 HTTP 列表必须返回 200，实际 ${healthy.status}: ${healthyBody}`)
    assert.match(healthy.headers.get('server-timing') ?? '', /account-list-projection;dur=[0-9.]+/, '投影 HTTP 列表必须暴露投影耗时指标')
    assert.match(healthy.headers.get('server-timing') ?? '', /account-status-filter;dur=0\.0/, '投影 HTTP 列表不得进入旧运行态候选筛选')

    await client.execute(`
      UPDATE ${table(client, 'accounts')}
      SET status = 'disabled'
      WHERE id = ?
    `, [`${accountPrefix}0001`])
    const blocked = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const blockedBody = await blocked.text()
    assert.equal(blocked.status, 503, `投影脏标记必须 fail-closed，实际 ${blocked.status}: ${blockedBody}`)
    assert.equal(blocked.headers.get('retry-after'), '1', '投影 unavailable 响应必须明确短重试时间')
    assert.match(blockedBody, /account_list_projection_unavailable/, '投影 unavailable 响应不得静默回退旧扫描')

    const rebuilt = await runAccountListAvailabilityProjectionMaintenance({
      ownerId: 'account-list-projection-shadow-http-rebuild',
      batchSize: 100,
      maximumProjectionAgeMs: 60_000
    })
    assert.equal(rebuilt.claimed, 1, 'HTTP fail-closed 后 worker 必须领取对应投影更新')
    const recovered = await fetch(`${baseUrl}/__aisys__/api/my-accounts?status=active&page=1&pageSize=20`, {
      headers: { cookie },
      signal: AbortSignal.timeout(10_000)
    })
    const recoveredBody = await recovered.text()
    assert.equal(recovered.status, 200, `投影重建后 HTTP 列表必须恢复 200，实际 ${recovered.status}: ${recoveredBody}`)
  } finally {
    runtimeConfig.background.accountListAvailabilityProjectionReadEnabled = previousReadEnabled
    if (server) await closeServer(server)
  }
}

async function verifyProjectionDeletionRecovery(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' }
): Promise<void> {
  const softDeletedAccountId = `${accountPrefix}0032`
  const hardDeletedAccountId = `${accountPrefix}0031`
  await client.execute(`
    UPDATE ${table(client, 'accounts')}
    SET deleted_at = ?
    WHERE id = ?
  `, [new Date().toISOString(), softDeletedAccountId])
  const softDeleteMaintenance = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-soft-delete',
    batchSize: 100,
    maximumProjectionAgeMs: 60_000
  })
  assert.equal(softDeleteMaintenance.claimed, 1, '软删除必须留下可领取的投影删除任务')
  assert.equal(softDeleteMaintenance.deleted, 1, '软删除必须移除可见投影')
  await assertShadowDefaultEqual(client, access, 'soft-delete')

  await client.execute(`DELETE FROM ${table(client, 'accounts')} WHERE id = ?`, [hardDeletedAccountId])
  const hardDeleteMaintenance = await runAccountListAvailabilityProjectionMaintenance({
    ownerId: 'account-list-projection-shadow-hard-delete',
    batchSize: 100,
    maximumProjectionAgeMs: 60_000
  })
  assert.equal(hardDeleteMaintenance.claimed, 0, '硬删除依赖级联清理，不应制造无法读取的幽灵 claim')
  await assertShadowDefaultEqual(client, access, 'hard-delete')
}

async function assertShadowDefaultEqual(
  client: DatabaseClient,
  access: { systemAccountId: string; role: 'user' },
  label: string
): Promise<void> {
  const comparison = await shadowCompareAccountListAvailabilityProjectionPage(client, {
    access,
    options: { page: 1, pageSize: 20 },
    maximumProjectionAgeMs: 60_000
  })
  assert.equal(comparison.outcome, 'equal', `${label} 后投影列表必须与旧链路一致：${comparison.reason ?? ''}`)
}

async function seedIndexedKeywordDocuments(client: DatabaseClient): Promise<void> {
  const terms = accountNameSearchQueryTerms('shadow account')
  assert(terms.length > 0, '关键词夹具必须生成检索词')
  const now = new Date().toISOString()
  const values = terms.map(() => '(?)').join(', ')
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_name_search_documents')} (account_id, system_account_id, normalized_name, updated_at)
      SELECT accounts.id, accounts.system_account_id, lower(accounts.name), ?
      FROM ${table(tx, 'accounts')} accounts
      WHERE accounts.id LIKE ?
    `, [now, `${accountPrefix}%`])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_name_search_terms')} (account_id, system_account_id, term, created_at)
      SELECT accounts.id, accounts.system_account_id, terms.term, ?
      FROM ${table(tx, 'accounts')} accounts
      CROSS JOIN (VALUES ${values}) AS terms(term)
      WHERE accounts.id LIKE ?
    `, [now, ...terms, `${accountPrefix}%`])
  })
}

async function seed(client: DatabaseClient): Promise<{ providerCode: string }> {
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
  assert(provider && profile, '隔离 PostgreSQL 必须已写入默认 provider/profile seed')
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO ${table(tx, 'system_accounts')} (
        id, username, display_name, role, status, password_hash, created_at, updated_at
      ) VALUES (?, ?, 'Projection shadow viewer', 'user', 'active', 'shadow-not-a-login-secret', ?, ?)
    `, [viewerId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'groups')} (
        id, system_account_id, name, provider_code, enabled, group_type, created_at, updated_at
      ) VALUES (?, ?, 'Projection shadow group', ?, 1, 'personal', ?, ?)
    `, [groupId, viewerId, profile.provider_code, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_tags')} (id, system_account_id, name, created_at, updated_at)
      VALUES (?, ?, 'Projection shadow tag', ?, ?)
    `, [tagId, viewerId, now, now])
    await tx.execute(`
      INSERT INTO ${table(tx, 'accounts')} (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        priority, schedulable, health_check_model, health_check_endpoint_mode, created_at, updated_at
      )
      SELECT ? || lpad(gs::text, 4, '0'), ?, ?, ?, ?, ?,
        'projection shadow account ' || lpad(gs::text, 4, '0'), 'api_key',
        CASE WHEN gs % 7 = 0 THEN 'disabled' WHEN gs % 5 = 0 THEN 'temporary_unavailable' ELSE 'active' END,
        '{}', gs % 10, CASE WHEN gs % 7 = 0 THEN 0 ELSE 1 END, ?, 'chat_json', ?, ?
      FROM generate_series(1, ?) AS generated(gs)
    `, [accountPrefix, viewerId, profile.provider_code, profile.id, profile.protocol_code, profile.protocol_version, profile.default_health_check_model, now, now, accountCount])
    await tx.execute(`
      INSERT INTO ${table(tx, 'group_accounts')} (
        system_account_id, group_id, account_id, enabled, created_at, updated_at
      )
      SELECT ?, ?, ? || lpad(gs::text, 4, '0'), 1, ?, ?
      FROM generate_series(1, ?) AS generated(gs)
      WHERE gs % 2 = 0
    `, [viewerId, groupId, accountPrefix, now, now, accountCount])
    await tx.execute(`
      INSERT INTO ${table(tx, 'account_tag_bindings')} (account_id, tag_id, system_account_id, created_at)
      SELECT ? || lpad(gs::text, 4, '0'), ?, ?, ?
      FROM generate_series(1, ?) AS generated(gs)
      WHERE gs % 3 = 0
    `, [accountPrefix, tagId, viewerId, now, accountCount])
  })
  return { providerCode: provider.code }
}

async function cleanup(client: DatabaseClient): Promise<void> {
  const accountIds = `${accountPrefix}%`
  await client.transaction(async (tx) => {
    await tx.execute(`DELETE FROM ${table(tx, 'accounts')} WHERE id LIKE ?`, [accountIds])
    await tx.execute(`DELETE FROM ${table(tx, 'account_tags')} WHERE id = ?`, [tagId])
    await tx.execute(`DELETE FROM ${table(tx, 'groups')} WHERE id = ?`, [groupId])
    await tx.execute(`DELETE FROM ${table(tx, 'system_accounts')} WHERE id = ?`, [viewerId])
  })
}

function assertScratchDatabase(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') throw new Error('影子对账必须在 PostgreSQL 模式运行')
  const postgresUrl = runtimeConfig.postgres.url
  if (!postgresUrl) throw new Error('影子对账缺少 JUHE_AI_POSTGRES_URL')
  const name = new URL(postgresUrl).pathname.replace(/^\//, '')
  if (!/^juhe_ai_sub2api_dev_[a-z0-9_]{3,80}$/.test(name)) {
    throw new Error(`影子对账只允许隔离开发库，当前 database=${name}`)
  }
}

function table(client: DatabaseClient, name: string): string {
  return client.dialect.qualifyTable('juhe_business', name)
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

function closeServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
  })
}

await main().catch(async (error) => {
  console.error(error instanceof Error ? (error.stack ?? error.message) : String(error))
  await closePostgresPool()
  process.exitCode = 1
})
// Runtime Redis clients intentionally stay open in the server process. This
// standalone regression has already finished its awaited cleanup, so force a
// deterministic process boundary instead of leaking a test worker.
process.exit(process.exitCode ?? 0)
