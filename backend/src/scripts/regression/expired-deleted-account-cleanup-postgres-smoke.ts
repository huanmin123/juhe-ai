import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { createAccountAsync, createGroupAsync, deleteAccountAsync } from '../../storage/repositories.js'
import { handleDbServiceOperation } from '../../modules/db-service/db-service-handlers.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '过期逻辑删除账户清理 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `expired_deleted_account_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `过期删除清理PG烟测分组${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const account = await createAccountAsync({
    name: `过期删除清理PG烟测账号${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'disabled',
    groupId: group.id,
    credentials: {
      api_key: `sk-${marker}`,
      base_url: 'https://example.invalid/v1'
    },
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)

  assert.equal(await deleteAccountAsync(account.id, access), true, 'PG smoke 应能先逻辑删除临时账户')
  await ageDeletedAccount(account.id)
  assert.equal(await rawAccountExists(account.id), true, '过期物理清理前账户业务行应仍存在')
  assert.equal(await groupBindingCount(account.id), 1, '过期物理清理前分组绑定应仍存在')

  const cleanup = await handleDbServiceOperation({ type: 'cleanup_expired_deleted_accounts' })
  assert.equal(cleanup.attempted, 1, `PG 过期删除清理应扫描到 1 个候选：${JSON.stringify(cleanup)}`)
  assert.equal(cleanup.completed, 1, `PG 过期删除清理应完成物理删除：${JSON.stringify(cleanup)}`)
  assert.equal(cleanup.deferred, 0, '无历史记录阻塞时不应延迟清理')
  assert.equal(cleanup.failed, 0, 'PG 过期删除清理不应失败')
  assert.equal(cleanup.physicallyDeletedAccounts, 1, 'PG 过期删除清理应物理删除账户')
  assert.equal(cleanup.physicallyDeletedGroupBindings, 1, 'PG 过期删除清理应删除分组绑定')
  assert.equal(cleanup.recordCleanupTargets.length, 0, '无历史记录阻塞时不应返回记录清理目标')

  assert.equal(await rawAccountExists(account.id), false, 'PG 过期物理清理后账户业务行应删除')
  assert.equal(await groupBindingCount(account.id), 0, 'PG 过期物理清理后分组绑定应删除')
  assert.equal(await childRowCount(account.id), 0, 'PG 过期物理清理后账户子表不应残留')

  console.log(JSON.stringify({
    message: '过期逻辑删除账户 DB service PG smoke 通过',
    accountId: account.id,
    attempted: cleanup.attempted,
    completed: cleanup.completed,
    physicallyDeletedAccounts: cleanup.physicallyDeletedAccounts,
    physicallyDeletedGroupBindings: cleanup.physicallyDeletedGroupBindings
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function ageDeletedAccount(accountId: string): Promise<void> {
  const pool = await getPostgresPool()
  const oldDeletedAt = new Date(Date.now() - 65 * 24 * 60 * 60 * 1000).toISOString()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET deleted_at = $1,
        updated_at = $1
    WHERE id = $2
      AND deleted_at IS NOT NULL
  `, [oldDeletedAt, accountId])
}

async function rawAccountExists(accountId: string): Promise<boolean> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT 1 FROM juhe_business.accounts WHERE id = $1 LIMIT 1', [accountId])
  return Number(result.rowCount ?? 0) > 0
}

async function groupBindingCount(accountId: string): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT COUNT(*)::int AS count FROM juhe_business.group_accounts WHERE account_id = $1', [accountId])
  return Number(result.rows[0]?.count ?? 0)
}

async function childRowCount(accountId: string): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT
      (SELECT COUNT(*) FROM juhe_business.account_supported_models WHERE account_id = $1)
      + (SELECT COUNT(*) FROM juhe_business.account_model_mappings WHERE account_id = $1)
      + (SELECT COUNT(*) FROM juhe_business.account_tag_bindings WHERE account_id = $1)
      + (SELECT COUNT(*) FROM juhe_business.account_name_search_terms WHERE account_id = $1)
      + (SELECT COUNT(*) FROM juhe_business.account_name_search_documents WHERE account_id = $1)
      + (SELECT COUNT(*) FROM juhe_business.account_api_key_runtime_states WHERE account_id = $1)
      AS count
  `, [accountId])
  return Number(result.rows[0]?.count ?? 0)
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const accountIds = [...new Set(createdAccountIds)]
  if (accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [groupIds])
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.groups WHERE name = $1', [`过期删除清理PG烟测分组${marker}`])
}
