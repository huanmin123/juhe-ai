import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  deleteAccountAsync,
  deleteGroupAsync,
  findAccountSummaryAsync,
  migrateAccountTrafficAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户流量迁移 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `traffic_migration_pg_smoke_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `账户流量迁移 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)
  const providerProtocolProfileId = GPT_OPENAI_V1_PROFILE_ID

  const source = await createMigrationAccount('源账号', group.id, providerProtocolProfileId)
  const target = await createMigrationAccount('目标账号', group.id, providerProtocolProfileId)

  const migration = await migrateAccountTrafficAsync({
    sourceAccountId: source.id,
    targetAccountId: target.id,
    sourceStatus: 'temporary_unavailable'
  }, access)
  assert.equal(migration?.sourceAccount.id, source.id, 'PG 流量迁移应返回源账号')
  assert.equal(migration?.targetAccount.id, target.id, 'PG 流量迁移应返回目标账号')
  assert.equal(migration?.groupId, group.id, 'PG 流量迁移应保留同组上下文')
  assert.equal(migration?.sourceAccount.status, 'temporary_unavailable', 'PG 流量迁移应把源账号置为临时不可用')
  assert.ok(migration?.sourceCooldownUntil, 'PG 流量迁移应写入源账号冷却时间')

  const reloadedSource = await findAccountSummaryAsync(source.id, access)
  const reloadedTarget = await findAccountSummaryAsync(target.id, access)
  assert.equal(reloadedSource?.status, 'temporary_unavailable', 'PG 流量迁移后源账号状态应可通过 async 读取')
  assert.equal(reloadedTarget?.status, 'active', 'PG 流量迁移不应改变目标账号状态')
  assert.equal(reloadedTarget?.schedulable, true, 'PG 流量迁移目标账号应仍可调度')

  await assertTrafficMigrationIndexedPlans(source.id, group.systemAccountId ?? access.systemAccountId)

  console.log(JSON.stringify({
    message: '账户流量迁移 PG smoke 通过',
    sourceAccountId: source.id,
    targetAccountId: target.id,
    groupId: group.id,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function createMigrationAccount(label: string, groupId: string, providerProtocolProfileId: string) {
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId,
    name: `账户流量迁移 PG smoke ${label} ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-traffic-migration-pg-smoke-${label}-${marker}`,
      base_url: 'https://api.openai.com/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId,
    supportedModels: ['gpt-4o-mini'],
    status: 'active',
    schedulable: true
  }, access)
  createdAccountIds.push(account.id)
  return account
}

async function assertTrafficMigrationIndexedPlans(accountId: string, systemAccountId: string): Promise<void> {
  await assertIndexedPlan(
    '账户流量迁移账号管理读取 PG 查询',
    `
      SELECT *
      FROM juhe_business.accounts
      WHERE id = $1
        AND deleted_at IS NULL
      LIMIT 1
    `,
    [accountId],
    ['accounts_pkey', 'idx_accounts_health_check_due', 'idx_accounts_deleted_cleanup']
  )
  await assertIndexedPlan(
    '账户流量迁移同组绑定读取 PG 查询',
    `
      SELECT group_id
      FROM juhe_business.group_accounts
      WHERE account_id = $1
        AND system_account_id = $2
        AND enabled = 1
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
      LIMIT 1
    `,
    [accountId, systemAccountId],
    ['idx_group_accounts_account_scope_enabled', 'idx_group_accounts_scope_enabled_updated']
  )
}

async function assertIndexedPlan(label: string, sql: string, params: unknown[], expectedIndexes: string[]): Promise<void> {
  const pool = await getPostgresPool()
  const connection = await pool.connect()
  try {
    await connection.query('BEGIN')
    await connection.query('SET LOCAL enable_seqscan = off')
    const planResult = await connection.query(`EXPLAIN (COSTS OFF) ${sql}`, params)
    await connection.query('ROLLBACK')
    const plan = planResult.rows
      .map((row: Record<string, unknown>) => String(row['QUERY PLAN'] ?? ''))
      .filter(Boolean)
      .join('\n')
    assert(!/\bSeq Scan\b/i.test(plan), `${label} 不应退化为 Seq Scan，实际计划：${plan}`)
    assert(
      expectedIndexes.some((indexName) => plan.includes(indexName)),
      `${label} 应命中索引 ${expectedIndexes.join(' / ')}，实际计划：${plan}`
    )
  } catch (error) {
    await connection.query('ROLLBACK').catch(() => undefined)
    throw error
  } finally {
    connection.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  for (const accountId of createdAccountIds) {
    await deleteAccountAsync(accountId, access).catch(() => false)
  }
  for (const groupId of createdGroupIds) {
    await deleteGroupAsync(groupId, access).catch(() => undefined)
  }

  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[]) OR group_id = ANY($2::text[])', [createdAccountIds, createdGroupIds])
  await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.account_tags WHERE system_account_id = $1 AND name LIKE $2', [access.systemAccountId, `%${marker}%`])
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
}
