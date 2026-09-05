import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { disableExpiredAccountsAsync } from '../../storage/account-runtime-status.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createAccountAsync, createGroupAsync } from '../../storage/repositories.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账号过期 runtime PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')
assert.equal(
  process.env.JUHE_AI_ALLOW_ACCOUNT_EXPIRY_RUNTIME_POSTGRES_SMOKE,
  '1',
  '账号过期 runtime PG smoke 会写隔离 fixture，必须显式设置 JUHE_AI_ALLOW_ACCOUNT_EXPIRY_RUNTIME_POSTGRES_SMOKE=1'
)

const marker = `account_expiry_runtime_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const scopedAccess: AccessScope = { systemAccountId: `${marker}_scope`, role: 'user' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
let dirtyAllSnapshot: { reason: string | null; updated_at: string } | undefined
let dirtyAllWasCreated = false

try {
  await insertSmokeSystemAccount(scopedAccess.systemAccountId)
  const group = await createGroupAsync({
    name: `账号过期 runtime PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, adminAccess)
  createdGroupIds.push(group.id)

  const accountIds: string[] = []
  for (let index = 0; index < 22; index += 1) {
    accountIds.push(await createSmokeAccount(group.id, adminAccess, `batch-${index}`))
  }
  const sentinelId = accountIds.pop()!
  await prepareExpiredAccount(sentinelId, new Date(Date.now() - 4500).toISOString())
  await setAccountError(sentinelId, 'account_expired')
  const expiryAt = new Date(Date.now() - 5000).toISOString()
  await setAccountReadyForExpiry(accountIds, expiryAt, new Date(Date.now() - 4000).toISOString())

  assert.equal(await disableExpiredAccountsAsync(adminAccess, 20), 20, '第一轮应领取 20 个过期账号')
  assert.equal(await disableExpiredAccountsAsync(adminAccess, 1), 1, '第二轮应领取剩余 1 个过期账号')
  const beforeEmptyRound = await accountUpdatedAt(accountIds)
  assert.equal(await disableExpiredAccountsAsync(adminAccess, 1), 0, '第三轮应为空 claim')
  assert.deepEqual(await accountUpdatedAt(accountIds), beforeEmptyRound, '空 claim 不应更新 updated_at')
  assert.equal((await accountState(sentinelId)).last_error_code, 'account_expired', '已有 account_expired 不得重新入选')

  const concurrentIds = await createAccounts(group.id, adminAccess, 8, 'concurrent')
  await setAccountReadyForExpiry(concurrentIds, expiryAt, new Date(Date.now() - 3000).toISOString())
  const concurrentResults = await Promise.all([
    disableExpiredAccountsAsync(adminAccess, 4),
    disableExpiredAccountsAsync(adminAccess, 4)
  ])
  assert.equal(concurrentResults[0] + concurrentResults[1], concurrentIds.length, '并发 claim 应无重叠且无遗漏')
  assert.equal(await pendingAccountCount(concurrentIds), 0, '并发 claim 后不得残留候选')

  const lockedIds = await createAccounts(group.id, adminAccess, 2, 'locked')
  await setAccountReadyForExpiry(lockedIds, expiryAt, new Date(Date.now() - 2000).toISOString())
  const orderedLockedIds = [...lockedIds].sort()
  const lockClient = await (await getPostgresPool()).connect()
  await lockClient.query('BEGIN')
  await lockClient.query('SELECT id FROM juhe_business.accounts WHERE id = $1 FOR UPDATE', [orderedLockedIds[0]])
  const previousLockTimeoutMs = runtimeConfig.postgres.lockTimeoutMs
  runtimeConfig.postgres.lockTimeoutMs = 200
  try {
    const startedAt = Date.now()
    assert.equal(await disableExpiredAccountsAsync(adminAccess, 2), 1, '锁定首候选时应通过 SKIP LOCKED 处理其他候选')
    assert(Date.now() - startedAt < 1000, 'SKIP LOCKED 应快速返回')
  } finally {
    runtimeConfig.postgres.lockTimeoutMs = previousLockTimeoutMs
    await lockClient.query('ROLLBACK')
    lockClient.release()
  }
  assert.equal(await disableExpiredAccountsAsync(adminAccess, 2), 1, '释放账户锁后应处理被跳过的候选')

  const scopedGroup = await createGroupAsync({
    name: `账号过期 runtime scope PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, scopedAccess)
  createdGroupIds.push(scopedGroup.id)
  const scopedId = await createSmokeAccount(scopedGroup.id, scopedAccess, 'scoped')
  const unscopedId = await createSmokeAccount(group.id, adminAccess, 'unscoped')
  await setAccountReadyForExpiry([scopedId, unscopedId], expiryAt, new Date(Date.now() - 1000).toISOString())
  assert.equal(await disableExpiredAccountsAsync(scopedAccess, 10), 1, 'scope sweep 只能领取自身系统账户候选')
  assert.equal(await pendingAccountCount([unscopedId]), 1, 'scope sweep 不得越权处理其他系统账户')
  assert.equal(await disableExpiredAccountsAsync(adminAccess, 10), 1, '管理员 sweep 应处理剩余候选')

  const rollbackId = await createSmokeAccount(group.id, adminAccess, 'rollback')
  await setAccountReadyForExpiry([rollbackId], expiryAt, new Date().toISOString())
  const pool = await getPostgresPool()
  dirtyAllSnapshot = await readDirtyAllRow()
  if (!dirtyAllSnapshot) {
    dirtyAllWasCreated = true
  }
  await pool.query(`
    INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
    VALUES ('__all__', 'account_expiry_runtime_pg_smoke_lock', NOW())
    ON CONFLICT (group_id) DO NOTHING
  `)
  const dirtyLockClient = await pool.connect()
  await dirtyLockClient.query('BEGIN')
  await dirtyLockClient.query('SELECT group_id FROM juhe_business.group_account_stats_dirty WHERE group_id = $1 FOR UPDATE', ['__all__'])
  const previousDirtyLockTimeoutMs = runtimeConfig.postgres.lockTimeoutMs
  runtimeConfig.postgres.lockTimeoutMs = 200
  let invalidationCount = 0
  const unregisterInvalidator = registerGatewayRuntimeCacheInvalidator((reason) => {
    if (reason === 'account_expired') invalidationCount += 1
  })
  try {
    try {
      await assert.rejects(disableExpiredAccountsAsync(adminAccess, 1), /lock timeout|canceling statement/i, 'dirty 写失败必须透传原始锁错误')
      assert.equal((await accountState(rollbackId)).status, 'active', 'dirty 写失败必须回滚账户 claim')
      assert.equal(invalidationCount, 0, 'commit 前不得触发 gateway cache invalidation')
    } finally {
      runtimeConfig.postgres.lockTimeoutMs = previousDirtyLockTimeoutMs
      await dirtyLockClient.query('ROLLBACK')
      dirtyLockClient.release()
    }
    assert.equal(await disableExpiredAccountsAsync(adminAccess, 1), 1, '释放 dirty 锁后应成功提交账户过期')
    assert.equal(invalidationCount, 1, '真实提交后应触发一次 gateway cache invalidation')
  } finally {
    unregisterInvalidator()
  }

  console.log(JSON.stringify({ message: '账号过期 runtime PG smoke 通过', marker }))
} finally {
  try {
    await cleanupSmokeRows()
  } finally {
    try {
      await restoreDirtyAllRow()
    } finally {
      try {
        await closeRedisClients()
      } finally {
        await closePostgresPool()
      }
    }
  }
}

async function insertSmokeSystemAccount(id: string): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES ($1, $2, $3, 'user', 'active', $4, 0, 0, $5, $5)
  `, [id, `smoke_${marker}`, `account expiry runtime PG ${marker}`, 'pg-smoke-password-hash', now])
}

async function readDirtyAllRow(): Promise<{ reason: string | null; updated_at: string } | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT reason, updated_at FROM juhe_business.group_account_stats_dirty WHERE group_id = $1', ['__all__'])
  return result.rows[0] as { reason: string | null; updated_at: string } | undefined
}

async function restoreDirtyAllRow(): Promise<void> {
  if (!dirtyAllSnapshot && !dirtyAllWasCreated) return
  const pool = await getPostgresPool()
  if (dirtyAllSnapshot) {
    await pool.query(`
      UPDATE juhe_business.group_account_stats_dirty
      SET reason = $1, updated_at = $2
      WHERE group_id = $3
    `, [dirtyAllSnapshot.reason, dirtyAllSnapshot.updated_at, '__all__'])
    return
  }
  await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = $1', ['__all__'])
}

async function createSmokeAccount(groupId: string, access: AccessScope, suffix: string): Promise<string> {
  const account = await createAccountAsync({
    name: `账号过期 runtime PG smoke 账号 ${marker} ${suffix}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'disabled',
    groupId,
    credentials: { api_key: `sk-${marker}-${suffix}`, base_url: 'https://example.invalid/v1' },
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)
  return account.id
}

async function createAccounts(groupId: string, access: AccessScope, count: number, prefix: string): Promise<string[]> {
  const ids: string[] = []
  for (let index = 0; index < count; index += 1) ids.push(await createSmokeAccount(groupId, access, `${prefix}-${index}`))
  return ids
}

async function prepareExpiredAccount(accountId: string, updatedAt: string): Promise<void> {
  await setAccountReadyForExpiry([accountId], new Date(Date.now() - 5000).toISOString(), updatedAt)
}

async function setAccountReadyForExpiry(accountIds: string[], expiresAt: string, updatedAt: string): Promise<void> {
  if (!accountIds.length) return
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'active', schedulable = 1, cooldown_until = NULL,
        account_expires_at = $1, last_error_code = NULL, last_error_message = NULL,
        deleted_at = NULL, updated_at = $2
    WHERE id = ANY($3::text[])
  `, [expiresAt, updatedAt, accountIds])
}

async function setAccountError(accountId: string, errorCode: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`UPDATE juhe_business.accounts SET last_error_code = $1 WHERE id = $2`, [errorCode, accountId])
}

async function accountState(accountId: string): Promise<{ status: string; last_error_code: string | null }> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT status, last_error_code FROM juhe_business.accounts WHERE id = $1', [accountId])
  return result.rows[0] as { status: string; last_error_code: string | null }
}

async function accountUpdatedAt(accountIds: string[]): Promise<Array<{ id: string; updated_at: string }>> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT id, updated_at FROM juhe_business.accounts WHERE id = ANY($1::text[]) ORDER BY id', [accountIds])
  return result.rows as Array<{ id: string; updated_at: string }>
}

async function pendingAccountCount(accountIds: string[]): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT COUNT(*)::int AS count
    FROM juhe_business.accounts
    WHERE id = ANY($1::text[]) AND last_error_code IS DISTINCT FROM 'account_expired'
      AND status <> 'disabled'
  `, [accountIds])
  return Number(result.rows[0]?.count ?? 0)
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdAccountIds.length > 0) {
    const accountIds = [...new Set(createdAccountIds)]
    for (const table of [
      'group_accounts',
      'account_supported_models',
      'account_model_mappings',
      'account_tag_bindings',
      'account_name_search_terms',
      'account_name_search_documents',
      'account_api_key_runtime_states'
    ]) {
      await pool.query(`DELETE FROM juhe_business.${table} WHERE account_id = ANY($1::text[])`, [accountIds]).catch(() => undefined)
    }
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  if (createdGroupIds.length > 0) {
    const groupIds = [...new Set(createdGroupIds)]
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = $1', [scopedAccess.systemAccountId]).catch(() => undefined)
}
