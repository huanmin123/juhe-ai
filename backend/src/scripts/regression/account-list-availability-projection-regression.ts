import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { DatabaseSync } from 'node:sqlite'
import { fileURLToPath } from 'node:url'

import type { AccountListItem } from '../../domain/types.js'
import { buildAccountNameSearchTerms, normalizeAccountNameSearchText } from '../../storage/account-name-search.repository.js'
import {
  AccountListAvailabilityProjectionUnavailableError,
  acknowledgeAccountListAvailabilityDirtyInClient,
  applyAccountListAvailabilityProjectionDirtyClaimInClient,
  claimAccountListAvailabilityDirtyInClient,
  completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient,
  enqueueDueAccountListAvailabilityProjectionsInClient,
  enqueueMissingAccountListAvailabilityProjectionsInClient,
  ensureAccountListAvailabilityProjectionViewerHealthInClient,
  ensureAccountListAvailabilityProjectionRuntimeDependencyInClient,
  listAccountListAvailabilityProjectionPageInClient,
  listAccountListAvailabilityProjectionScopesInClient,
  markAccountListAvailabilityDirtyFamilyInTransaction,
  markAccountListAvailabilityDirtyInClient,
  refreshAccountListAvailabilityProjectionViewerHealthInClient,
  releaseAccountListAvailabilityDirtyForReplayInClient,
  upsertAccountListAvailabilityProjectionInClient,
  type AccountListAvailabilityProjectionWrite
} from '../../storage/account-list-availability-projection.repository.js'
import { createSqliteDatabaseClient, type DatabaseClient } from '../../storage/database-client.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import { applyBusinessSchema } from '../../storage/schema/business-schema.js'

const database = new DatabaseSync(':memory:')
applyBusinessSchema(database)
const client = createSqliteDatabaseClient(database)
const viewerSystemAccountId = 'projection_viewer'
const accountIds = ['projection_account_alpha', 'projection_account_beta', 'projection_account_gamma']

try {
  insertSchemaFixture()
  await verifyExistingViewerHealthBackfill()
  await verifyDirtyLeaseAndGenerationFence()
  await verifySingleQueryPaginationAndFilters()
  await verifyFamilyExpansionAndBoundedWorkerQueues()
  verifyRuntimeDirtyBridgeContract()
  verifyPostgresSchemaProjectionParity()
  console.log('account list availability projection regression passed')
} finally {
  database.close()
}

async function verifyExistingViewerHealthBackfill(): Promise<void> {
  const legacyViewerSystemAccountId = 'projection_legacy_empty_viewer'
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `).run(legacyViewerSystemAccountId, 'projection_legacy_empty_viewer', 'Legacy empty viewer', 'user', 'active', 'test-only', now, now)

  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
      viewerSystemAccountId: legacyViewerSystemAccountId
    })),
    AccountListAvailabilityProjectionUnavailableError,
    '历史 viewer 缺 health 行时必须 fail-closed，不能把未知状态当作空列表'
  )
  assert.equal(await ensureAccountListAvailabilityProjectionViewerHealthInClient(client, {
    limit: 10,
    updatedAt: now
  }), 2, '维护任务必须补齐历史 viewer 和当前 fixture 的 health 行')
  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
      viewerSystemAccountId: legacyViewerSystemAccountId
    })),
    AccountListAvailabilityProjectionUnavailableError,
    'health 回填但尚未完成投影时仍不得返回旧或未知结果'
  )
  await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, {
    viewerSystemAccountId: legacyViewerSystemAccountId,
    updatedAt: now
  })
  await ensureAccountListAvailabilityProjectionRuntimeDependencyInClient(client, { updatedAt: now })
  assert.equal(await completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(client, {
    updatedAt: now
  }), true, '无待重放行时，运行态依赖初始化后必须显式转为可读')
  const emptyPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    viewerSystemAccountId: legacyViewerSystemAccountId
  }))
  assert.deepEqual(emptyPage.items, [])
  assert.equal(emptyPage.page, 1)
  assert.equal(emptyPage.pageSize, 20)
  assert.equal(emptyPage.total, 0)
  assert.equal(emptyPage.hasMore, false)
  assert.equal(emptyPage.generatedAt, new Date(1_000).toISOString())
  assert.equal(emptyPage.projectedAt, '')
}

async function verifyDirtyLeaseAndGenerationFence(): Promise<void> {
  const firstDirty = await markAccountListAvailabilityDirtyInClient(client, {
    accountId: accountIds[0]!,
    reason: 'account_changed',
    nowMs: 1_000
  })
  assert.equal(firstDirty.generation, 1)

  const [firstClaim] = await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId: 'projection-worker-a',
    limit: 1,
    leaseMs: 100,
    nowMs: 1_000
  })
  assert.ok(firstClaim)
  assert.equal(firstClaim.generation, 1)
  assert.equal(firstClaim.attemptCount, 1)

  assert.equal(await releaseAccountListAvailabilityDirtyForReplayInClient(client, {
    accountId: firstClaim.accountId,
    generation: firstClaim.generation,
    claimToken: firstClaim.claimToken,
    reason: 'transient_failure',
    retryDelayMs: 50,
    nowMs: 1_010
  }), true, '同一 lease 必须能显式释放为可重放状态')
  assert.equal((await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId: 'projection-worker-a',
    limit: 1,
    leaseMs: 100,
    nowMs: 1_050
  })).length, 0, '重放延迟前不得重复领取')

  const [retryClaim] = await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId: 'projection-worker-b',
    limit: 1,
    leaseMs: 100,
    nowMs: 1_060
  })
  assert.ok(retryClaim)
  assert.equal(retryClaim.generation, 1)
  assert.equal(retryClaim.attemptCount, 2)
  assert.equal(await applyAccountListAvailabilityProjectionDirtyClaimInClient(client, {
    claim: retryClaim,
    projection: projectionWrite(accountIds[0]!, {
      sourceGeneration: retryClaim.generation,
      projectedAt: new Date(2_000).toISOString(),
      tagIds: ['tag_green']
    })
  }), true)
  assert.equal(await acknowledgeAccountListAvailabilityDirtyInClient(client, {
    accountId: retryClaim.accountId,
    generation: retryClaim.generation,
    claimToken: retryClaim.claimToken
  }), false, '原子 apply 已确认 dirty claim，不能二次确认')

  const secondDirty = await markAccountListAvailabilityDirtyInClient(client, {
    accountId: accountIds[0]!,
    reason: 'authorization_changed',
    nowMs: 2_100
  })
  const [secondClaim] = await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId: 'projection-worker-a',
    limit: 1,
    leaseMs: 100,
    nowMs: 2_100
  })
  assert.ok(secondClaim)
  assert.equal(secondClaim.generation, secondDirty.generation)

  const thirdDirty = await markAccountListAvailabilityDirtyInClient(client, {
    accountId: accountIds[0]!,
    reason: 'runtime_blocked',
    nowMs: 2_110
  })
  assert.equal(thirdDirty.generation, secondClaim.generation + 1)
  assert.equal(await applyAccountListAvailabilityProjectionDirtyClaimInClient(client, {
    claim: secondClaim,
    projection: projectionWrite(accountIds[0]!, {
      sourceGeneration: secondClaim.generation,
      effectiveStatus: 'temporary_unavailable',
      projectedAt: new Date(2_120).toISOString(),
      tagIds: ['tag_blue']
    })
  }), false, '迟到 worker 不得修改新版 projection payload 或标签')
  const remainingTags = database.prepare(`
    SELECT tag_id FROM account_list_availability_projection_tags
    WHERE viewer_system_account_id = ? AND account_id = ?
    ORDER BY tag_id ASC
  `).all(viewerSystemAccountId, accountIds[0]) as Array<{ tag_id: string }>
  assert.deepEqual(remainingTags.map((row) => row.tag_id), ['tag_green'])
  assert.equal(await acknowledgeAccountListAvailabilityDirtyInClient(client, {
    accountId: secondClaim.accountId,
    generation: secondClaim.generation,
    claimToken: secondClaim.claimToken
  }), false, '迟到 worker 不得确认更新后的 dirty generation')

  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, projectionQuery()),
    AccountListAvailabilityProjectionUnavailableError,
    'dirty 行存在时列表必须 fail-closed，不能读旧 payload'
  )

  const [thirdClaim] = await claimAccountListAvailabilityDirtyInClient(client, {
    ownerId: 'projection-worker-c',
    limit: 1,
    leaseMs: 100,
    nowMs: 2_110
  })
  assert.ok(thirdClaim)
  assert.equal(await applyAccountListAvailabilityProjectionDirtyClaimInClient(client, {
    claim: thirdClaim,
    projection: projectionWrite(accountIds[0]!, {
      sourceGeneration: thirdClaim.generation,
      projectedAt: new Date(2_200).toISOString(),
      tagIds: ['tag_green']
    })
  }), true)
}

async function verifySingleQueryPaginationAndFilters(): Promise<void> {
  await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(accountIds[1]!, {
    prioritySortKey: 2,
    nameSortKey: 'beta',
    projectedAt: new Date(2_200).toISOString(),
    tagIds: ['tag_blue']
  }))
  await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(accountIds[2]!, {
    prioritySortKey: 3,
    nameSortKey: 'gamma',
    projectedAt: new Date(2_200).toISOString(),
    tagIds: ['tag_green', 'tag_blue']
  }))
  await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, { viewerSystemAccountId })

  const observed = { query: 0, one: 0 }
  const countedClient: DatabaseClient = {
    ...client,
    async query<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T[]> {
      observed.query += 1
      return client.query<T>(sql, params)
    },
    async one<T extends object = Record<string, unknown>>(sql: string, params?: readonly unknown[]): Promise<T | undefined> {
      observed.one += 1
      return client.one<T>(sql, params)
    }
  }
  const page = await listAccountListAvailabilityProjectionPageInClient(countedClient, projectionQuery({
    options: { status: 'active', page: 1, pageSize: 2, sorts: [{ field: 'priority', order: 'asc' }] }
  }))
  assert.equal(observed.query, 1, '投影列表必须用单条 SQL 同时检查 freshness 并取得分页结果')
  assert.equal(observed.one, 0)
  assert.deepEqual(page.items.map((item) => item.id), accountIds.slice(0, 2))
  assert.equal(page.hasMore, true, 'LIMIT pageSize + 1 必须直接得出 hasMore')
  assert.equal(page.total, 3, 'total 只保留当前页上界，不允许额外 COUNT(*)')

  const agedPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    nowMs: 60 * 60_000,
    options: { status: 'active', page: 1, pageSize: 2, sorts: [{ field: 'priority', order: 'asc' }] }
  }))
  assert.deepEqual(
    agedPage.items.map((item) => item.id),
    accountIds.slice(0, 2),
    '无 dirty 且无已到期 transition 时，投影年龄不得触发全量重建或把大账户列表变为 unavailable'
  )

  const greenPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { tagIds: ['tag_green'], page: 1, pageSize: 20 }
  }))
  assert.deepEqual(greenPage.items.map((item) => item.id), [accountIds[0], accountIds[2]])

  const eitherTagPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { tagIds: ['tag_green', 'tag_blue'], page: 1, pageSize: 20 }
  }))
  assert.deepEqual(eitherTagPage.items.map((item) => item.id), accountIds, '多个 tagId 必须保持现有任一标签命中语义')

  const containedName = 'alpha account suffix'
  await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(accountIds[0]!, {
    sourceGeneration: 100,
    nameSortKey: containedName
  }))
  const projectedName = database.prepare(`
    SELECT name_sort_key FROM account_list_availability_projections
    WHERE viewer_system_account_id = ? AND account_id = ?
  `).get(viewerSystemAccountId, accountIds[0]) as { name_sort_key?: string } | undefined
  assert.equal(projectedName?.name_sort_key, containedName, 'contains 回归必须先确认投影名称已随 generation 更新')
  const normalizedContainedName = normalizeAccountNameSearchText(containedName)
  database.prepare(`
    INSERT INTO account_name_search_documents (account_id, system_account_id, normalized_name, updated_at)
    VALUES (?, ?, ?, ?)
  `).run(accountIds[0], viewerSystemAccountId, normalizedContainedName, new Date(2_200).toISOString())
  for (const term of buildAccountNameSearchTerms(containedName)) {
    database.prepare(`
      INSERT INTO account_name_search_terms (account_id, system_account_id, term, created_at)
      VALUES (?, ?, ?, ?)
    `).run(accountIds[0], viewerSystemAccountId, term, new Date(2_200).toISOString())
  }
  await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(accountIds[0]!, {
    sourceGeneration: 101,
    nameSortKey: containedName,
    searchTerms: buildAccountNameSearchTerms(containedName)
  }))
  await refreshAccountListAvailabilityProjectionViewerHealthInClient(client, { viewerSystemAccountId })
  const containsKeywordPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { keyword: 'suffix', page: 1, pageSize: 20 }
  }))
  assert.deepEqual(containsKeywordPage.items.map((item) => item.id), [accountIds[0]], '读模型必须保持账户名称的包含检索语义，而不只支持前缀')

  const allValuePage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { providerCode: 'all', type: 'all', page: 1, pageSize: 20 }
  }))
  assert.equal(allValuePage.items.length, 3, 'provider/type=all 不得变成真实筛选值')

  const groupPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { groupId: 'group_projection', status: 'active', page: 1, pageSize: 20 }
  }))
  assert.equal(groupPage.items.length, 3)

  const invalidStatusPage = await listAccountListAvailabilityProjectionPageInClient(client, projectionQuery({
    options: { status: 'not_a_status', page: 1, pageSize: 20 }
  }))
  assert.deepEqual(invalidStatusPage.items, [], '非法状态必须保持现有列表的空集语义')

  await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(accountIds[2]!, {
    sourceGeneration: 2,
    nextTransitionAt: new Date(900).toISOString(),
    projectedAt: new Date(2_200).toISOString()
  }))
  assert.equal(
    await enqueueDueAccountListAvailabilityProjectionsInClient(client, { limit: 1, nowMs: 2_200 }),
    1,
    '自然到期必须由有界维护任务转为脏标记'
  )
  await assert.rejects(
    () => listAccountListAvailabilityProjectionPageInClient(client, projectionQuery()),
    AccountListAvailabilityProjectionUnavailableError,
    '进入待处理队列的自然到期投影必须 unavailable，不能返回旧页面'
  )
  database.prepare('DELETE FROM account_list_availability_dirty WHERE account_id = ?').run(accountIds[2])
}

async function verifyFamilyExpansionAndBoundedWorkerQueues(): Promise<void> {
  const authorizationId = 'projection_authorization'
  const sourceAccountId = 'projection_source'
  const authorizedAccountId = 'projection_authorized_instance'
  const now = new Date(3_000).toISOString()
  database.prepare(`
    INSERT INTO resource_authorizations (
      id, resource_type, resource_id, resource_owner_system_account_id,
      grantee_system_account_id, scope, status, created_by, created_at, updated_at
    ) VALUES (?, 'account', ?, ?, ?, 'use', 'active', ?, ?, ?)
  `).run(authorizationId, sourceAccountId, viewerSystemAccountId, viewerSystemAccountId, viewerSystemAccountId, now, now)
  for (const [accountId, sourceId, authorization] of [
    [sourceAccountId, null, null],
    [authorizedAccountId, sourceAccountId, authorizationId]
  ] as const) {
    database.prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        health_check_model, health_check_endpoint_mode, authorization_instance_source_account_id,
        authorization_instance_authorization_id, created_at, updated_at
      ) VALUES (?, ?, 'gpt', 'profile_projection', 'openai', 'v1', ?, 'api_key', 'active', '{}',
        'gpt-5-mini', 'chat_json', ?, ?, ?, ?)
    `).run(accountId, viewerSystemAccountId, accountId, sourceId, authorization, now, now)
  }
  const dirtyRecords = await client.transaction((tx) => markAccountListAvailabilityDirtyFamilyInTransaction(tx, {
    sourceAccountIds: [sourceAccountId],
    reason: 'source_runtime_changed',
    nowMs: 3_000
  }))
  assert.deepEqual(dirtyRecords.map((record) => record.accountId), [authorizedAccountId, sourceAccountId])
  const scopes = await listAccountListAvailabilityProjectionScopesInClient(client, [sourceAccountId, authorizedAccountId])
  assert.deepEqual(scopes, [
    { accountId: authorizedAccountId, viewerSystemAccountId, createdAt: now },
    { accountId: sourceAccountId, viewerSystemAccountId, createdAt: now }
  ])

  database.prepare(`DELETE FROM account_list_availability_dirty WHERE account_id IN (?, ?)`)
    .run(sourceAccountId, authorizedAccountId)
  assert.equal(await enqueueMissingAccountListAvailabilityProjectionsInClient(client, { limit: 1, nowMs: 3_000 }), 1)
  const missingCount = database.prepare(`SELECT COUNT(*) AS count FROM account_list_availability_dirty WHERE reason = 'projection_missing'`)
    .get() as { count: number }
  assert.equal(missingCount.count, 1, '缺失投影 bootstrap 必须有界')
  database.prepare(`DELETE FROM account_list_availability_dirty`).run()

  assert.equal(await upsertAccountListAvailabilityProjectionInClient(client, projectionWrite(sourceAccountId, {
    sourceGeneration: 1,
    nextTransitionAt: new Date(2_999).toISOString(),
    projectedAt: now
  })), true)
  assert.equal(await enqueueDueAccountListAvailabilityProjectionsInClient(client, { limit: 1, nowMs: 3_000 }), 1)
  const dueCount = database.prepare(`SELECT COUNT(*) AS count FROM account_list_availability_dirty WHERE reason = 'projection_due_transition'`)
    .get() as { count: number }
  assert.equal(dueCount.count, 1, '自然到期扫描必须转为单个脏标记')
}

function projectionQuery(overrides: Partial<Parameters<typeof listAccountListAvailabilityProjectionPageInClient>[1]> = {}) {
  return {
    viewerSystemAccountId,
    nowMs: 1_000,
    options: { page: 1, pageSize: 20 },
    ...overrides
  }
}

function projectionWrite(
  accountId: string,
  overrides: Partial<AccountListAvailabilityProjectionWrite> = {}
): AccountListAvailabilityProjectionWrite {
  const name = accountId.endsWith('alpha') ? 'Alpha' : accountId.endsWith('beta') ? 'Beta' : 'Gamma'
  return {
    viewerSystemAccountId,
    accountId,
    concurrencyAccountId: accountId,
    currentConcurrency: 0,
    effectiveStatus: 'active',
    schedulableBucket: 'enabled',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile_projection',
    accountType: 'api_key',
    boundGroupId: 'group_projection',
    nameSortKey: name,
    prioritySortKey: accountId.endsWith('alpha') ? 1 : accountId.endsWith('beta') ? 2 : 3,
    superPrioritySortKey: 0,
    fallbackSortKey: 0,
    concurrencySortKey: 100,
    createdAtSortKey: new Date(100).toISOString(),
    payload: { id: accountId, name } as AccountListItem,
    tagIds: [],
    sourceGeneration: 1,
    projectedAt: new Date(2_000).toISOString(),
    ...overrides
  }
}

function insertSchemaFixture(): void {
  const now = new Date(0).toISOString()
  database.prepare(`
    INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `).run(viewerSystemAccountId, 'projection_viewer', 'Projection viewer', 'user', 'active', 'test-only', now, now)
  database.prepare(`INSERT INTO providers (id, code, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`)
    .run('gpt', 'gpt', 'GPT', now, now)
  database.prepare(`INSERT INTO protocols (id, code, version, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`)
    .run('openai_v1', 'openai', 'v1', 'OpenAI v1', now, now)
  database.prepare(`
    INSERT INTO provider_protocol_profiles (
      id, provider_code, name, protocol_code, protocol_version, base_url,
      default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run('profile_projection', 'gpt', 'Projection', 'openai', 'v1', 'https://example.invalid/v1', 'gpt-5-mini', '["api_key"]', '[]', now, now)
  for (const accountId of accountIds) {
    database.prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, credentials_encrypted,
        health_check_model, health_check_endpoint_mode, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(
      accountId,
      viewerSystemAccountId,
      'gpt',
      'profile_projection',
      'openai',
      'v1',
      accountId,
      'api_key',
      'active',
      '{}',
      'gpt-5-mini',
      'chat_json',
      now,
      now
    )
  }
  for (const tagId of ['tag_green', 'tag_blue']) {
    database.prepare(`INSERT INTO account_tags (id, system_account_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`)
      .run(tagId, viewerSystemAccountId, tagId, now, now)
  }
}

function verifyPostgresSchemaProjectionParity(): void {
  const statements = collectPostgresSchemaStatements()
  const projection = statements.find((statement) => /^CREATE TABLE IF NOT EXISTS account_list_availability_projections\b/.test(statement.sql))?.sql
  const dirty = statements.find((statement) => /^CREATE TABLE IF NOT EXISTS account_list_availability_dirty\b/.test(statement.sql))?.sql
  assert.ok(projection, 'PostgreSQL schema 必须创建账户列表投影表')
  assert.ok(dirty, 'PostgreSQL schema 必须创建账户列表脏队列')
  assert.match(projection, /payload_json text NOT NULL/)
  assert.match(projection, /source_generation integer NOT NULL/)
  assert.match(dirty, /available_at_ms bigint NOT NULL/)
}

function verifyRuntimeDirtyBridgeContract(): void {
  const source = readFileSync(fileURLToPath(new URL('../../modules/gateway/runtime/account-side-effects.service.ts', import.meta.url)), 'utf8')
  assert.match(
    source,
    /async function markAccountListRuntimeProjectionDirty\(runtimeKey: string\)[\s\S]*sourceAccountIds: \[accountId\][\s\S]*reason: 'runtime_availability_changed'/,
    'Redis 运行态变更必须将 source 账户及授权实例标为 durable dirty'
  )
  assert.match(
    source,
    /async function suppressGatewayAccountLocallyForSeconds[\s\S]*await markAccountListRuntimeProjectionDirty\(runtimeKey\)[\s\S]*configuredPolicyAvoidanceStore\.setJson/,
    '策略避让必须先标 dirty，后写 Redis'
  )
  assert.match(
    source,
    /async function persistDistributedRecoveryProbeState[\s\S]*await markAccountListRuntimeProjectionDirty\(state\.runtimeKey\)[\s\S]*distributedRecoveryProbeStore\.set/,
    '恢复探针更新必须先标 dirty，后写 Redis'
  )
  assert.match(
    source,
    /async function clearDistributedRecoveryProbeState[\s\S]*markAccountListRuntimeProjectionDirty\(runtimeKey\)[\s\S]*distributedRecoveryProbeStore\.delete/,
    '恢复探针删除必须先标 dirty，后删除 Redis 状态'
  )
}
