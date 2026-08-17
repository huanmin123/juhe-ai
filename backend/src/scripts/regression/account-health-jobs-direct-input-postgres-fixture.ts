import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { encryptJson } from '../../storage/crypto.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { currentAccountHealthJobsInputVersionForRuntimeAsync } from '../../storage/account-health-jobs-input-version.repository.js'
import { createAccountAsync, createGroupAsync } from '../../storage/repositories.js'
import { createResourceAuthorizationAsync } from '../../storage/resource-authorization-write.repository.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'J1 PG direct-input fixture 需要 PostgreSQL')

const marker = `j1_direct_input_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
let succeeded = false
try {
  const group = await createGroupAsync({
    name: `J1 PG direct input ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = await createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: `J1 PG direct input ${marker}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-j1-direct-input-${marker}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    supportedModels: ['gpt-4o-mini'],
    healthCheckModel: 'gpt-4o-mini',
    healthCheckEndpointMode: 'chat_json',
    status: 'active',
    schedulable: true
  }, access)
  const inputVersion = await currentAccountHealthJobsInputVersionForRuntimeAsync(account.id)
  assert(inputVersion && inputVersion >= 1, 'PG 账户创建必须在同一业务事务生成 J1 input epoch')
  process.stdout.write(`${account.id}\n`)

  // Account creation intentionally starts a source in pending_test. This
  // fixture needs a real dispatchable physical source before it verifies the
  // authorized-instance contract, so it establishes that independent source
  // precondition without weakening the Go candidate predicate.
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'active', schedulable = 1, next_health_check_at = NULL, updated_at = $2
    WHERE id = $1
  `, [account.id, new Date().toISOString()])
  const proxyId = `proxy_j1_${marker}`
  const proxyNow = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.proxy_profiles (
      id, system_account_id, name, type, host, port, username, password_encrypted, enabled, created_at, updated_at
    ) VALUES ($1, 'sys_admin', $2, 'socks5', '127.0.0.1', 1080, 'j1-user', $3, TRUE, $4, $4)
  `, [proxyId, `J1 proxy ${marker}`, encryptJson({ password: 'j1-password' }), proxyNow])
  await pool.query('UPDATE juhe_business.accounts SET proxy_profile_id = $2 WHERE id = $1', [account.id, proxyId])

  const granteeId = `sysacc_${marker}`
  await insertSmokeSystemAccount(granteeId)
  const granteeAccess: AccessScope = { systemAccountId: granteeId, role: 'user' }
  const granteeGroup = await createGroupAsync({
    name: `J1 PG direct input grantee ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  await createResourceAuthorizationAsync({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId,
    targetGroupId: granteeGroup.id,
    remark: `J1 PG direct input ${marker}`
  }, access)
  const authorizedAccountId = (await authorizedInstance(account.id, granteeId)).id
  const authorizedInputVersion = await currentAccountHealthJobsInputVersionForRuntimeAsync(authorizedAccountId)
  assert(authorizedInputVersion && authorizedInputVersion >= 1, 'PG 授权实例必须在同一业务事务生成 J1 input epoch')
  process.stdout.write(`authorized=${authorizedAccountId}\n`)

  const quotaGranteeId = `sysacc_quota_${marker}`
  await insertSmokeSystemAccount(quotaGranteeId)
  const quotaAccess: AccessScope = { systemAccountId: quotaGranteeId, role: 'user' }
  const quotaGroup = await createGroupAsync({
    name: `J1 PG quota grantee ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, quotaAccess)
  await createResourceAuthorizationAsync({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: quotaGranteeId,
    targetGroupId: quotaGroup.id,
    limits: { total: { enabled: true, limit: 1 } },
    remark: `J1 PG quota ${marker}`
  }, access)
  const quotaInstance = await authorizedInstance(account.id, quotaGranteeId)
  await pool.query(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, total_cost_usd, last_used_at, updated_at
    ) VALUES ($1, 'account_authorization', $2, 1, 1, $3, $3)
  `, [quotaGranteeId, quotaInstance.authorizationId, new Date().toISOString()])
  process.stdout.write(`quota_exceeded=${quotaInstance.id}\n`)

  const cooldownGranteeId = `sysacc_cooldown_${marker}`
  await insertSmokeSystemAccount(cooldownGranteeId)
  const cooldownAccess: AccessScope = { systemAccountId: cooldownGranteeId, role: 'user' }
  const cooldownGroup = await createGroupAsync({
    name: `J1 PG cooldown grantee ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, cooldownAccess)
  await createResourceAuthorizationAsync({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: cooldownGranteeId,
    targetGroupId: cooldownGroup.id,
    remark: `J1 PG cooldown ${marker}`
  }, access)
  const cooldownInstance = await authorizedInstance(account.id, cooldownGranteeId)
  const observedAt = new Date(Date.now() - 60_000).toISOString()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $2,
        cooldown_retest_observation_started_at = $3,
        cooldown_retest_generation = $4,
        updated_at = $3
    WHERE id = $1
  `, [cooldownInstance.id, new Date(Date.now() - 1000).toISOString(), observedAt, `j1-cooldown-${marker}`])
  process.stdout.write(`cooldown=${cooldownInstance.id}\n`)
  succeeded = true
} catch (error) {
  console.error(error)
} finally {
  await closePostgresPool()
}
// This fixture never uses Redis. Force-close any lazily initialized client
// handles so the one-shot process cannot remain alive after the seed.
process.exit(succeeded ? 0 : 1)

async function insertSmokeSystemAccount(id: string): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  await pool.query(`
    INSERT INTO juhe_business.system_accounts (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES ($1, $2, $3, 'user', 'active', $4, 0, 0, $5, $5)
  `, [id, `j1_${id}`, `J1 PG direct input ${id}`, 'j1-pg-fixture-password-hash', now])
}

async function authorizedInstance(sourceAccountId: string, granteeSystemAccountId: string): Promise<{ id: string; authorizationId: string }> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT id, authorization_instance_authorization_id
    FROM juhe_business.accounts
    WHERE authorization_instance_source_account_id = $1
      AND system_account_id = $2
      AND deleted_at IS NULL
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [sourceAccountId, granteeSystemAccountId])
  const id = result.rows[0]?.id
  const authorizationId = result.rows[0]?.authorization_instance_authorization_id
  if (typeof id !== 'string' || typeof authorizationId !== 'string') throw new Error('PG 授权必须创建隔离的被授权账户实例')
  return { id, authorizationId }
}
