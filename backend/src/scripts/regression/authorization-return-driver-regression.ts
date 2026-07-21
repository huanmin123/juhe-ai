import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccessScope } from '../../storage/access-scope.js'
import type { DatabaseClient } from '../../storage/database-client.js'

interface SeedIds {
  authorizationId: string
  authorizationSourceId: string
  grantId: string
  granteeSystemAccountId: string
  groupId?: string
  instanceAccountId?: string
  sourceAccountId?: string
}

interface ProviderProfileRow {
  id: string
  provider_code: string
  protocol_code: string
  protocol_version: string
}

const ownerSystemAccountId = 'sys_admin'
const createdSeedRows: SeedIds[] = []

if (process.env.JUHE_AUTHORIZATION_RETURN_DRIVER_CHILD === 'postgres') {
  try {
    await assertAuthorizationReturnAsync()
  } finally {
    await cleanupSeedRows()
  }
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-authorization-return-driver-'))
try {
  process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')
  process.env.JUHE_AI_SECRET = 'authorization-return-driver-secret'
  process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
  process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'

  await assertAuthorizationReturnAsync()

  if (process.env.JUHE_AUTHORIZATION_RETURN_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_AUTHORIZATION_RETURN_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_LOG_FILE_ENABLED: 'true',
        JUHE_AI_LOG_DIR: join(tempRoot, 'postgres-logs'),
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_AUTHORIZATION_RETURN_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_AUTHORIZATION_RETURN_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_AUTHORIZATION_RETURN_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_AUTHORIZATION_RETURN_REDIS_QUEUE_URL ?? process.env.JUHE_AUTHORIZATION_RETURN_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('authorization-return-driver-regression passed')
} finally {
  await cleanupSeedRows()
  await closeStorage()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertAuthorizationReturnAsync(): Promise<void> {
  const [{ logger }, repositories] = await Promise.all([
    import('../../shared/logger.js'),
    import('../../storage/repositories.js')
  ])
  logger.level = 'silent'
  const client = await createCurrentDatabaseClient()
  const instanceSeed = await seedReturnableAccountAuthorization(client)
  createdSeedRows.push(instanceSeed)
  assert(instanceSeed.instanceAccountId, '账户授权实例种子应包含实例账号 ID')
  const instanceAccess: AccessScope = { systemAccountId: instanceSeed.granteeSystemAccountId, role: 'user' }
  const instanceReturned = await repositories.returnAccountAuthorizationInstanceForGranteeAsync(instanceSeed.instanceAccountId, instanceAccess)
  await assertReturnedAuthorization(client, instanceSeed, instanceReturned, '账户授权实例归还')
  const secondInstanceReturn = await repositories.returnAccountAuthorizationInstanceForGranteeAsync(instanceSeed.instanceAccountId, instanceAccess)
  assert.equal(secondInstanceReturn, undefined, '账户授权实例已归还后不应重复归还')

  const resourceSeed = await seedReturnableAccountAuthorization(client)
  createdSeedRows.push(resourceSeed)
  const resourceAccess: AccessScope = { systemAccountId: resourceSeed.granteeSystemAccountId, role: 'user' }
  const resourceReturned = await repositories.returnResourceAuthorizationForGranteeAsync(resourceSeed.grantId, resourceAccess)
  await assertReturnedAuthorization(client, resourceSeed, resourceReturned, '授权列表个人归还')
  const secondResourceReturn = await repositories.returnResourceAuthorizationForGranteeAsync(resourceSeed.grantId, resourceAccess)
  assert.equal(secondResourceReturn, undefined, '授权列表已归还后不应重复归还')

  const groupSeed = await seedReturnableGroupAuthorization(client)
  createdSeedRows.push(groupSeed)
  assert(groupSeed.groupId, '分组授权种子应包含分组 ID')
  const groupAccess: AccessScope = { systemAccountId: groupSeed.granteeSystemAccountId, role: 'user' }
  const groupReturned = await repositories.returnGroupAuthorizationForGranteeAsync(groupSeed.groupId, groupAccess)
  await assertReturnedAuthorization(client, groupSeed, groupReturned, '分组个人归还')
  const secondGroupReturn = await repositories.returnGroupAuthorizationForGranteeAsync(groupSeed.groupId, groupAccess)
  assert.equal(secondGroupReturn, undefined, '分组授权已归还后不应重复归还')
}

async function assertReturnedAuthorization(
  client: DatabaseClient,
  seed: SeedIds,
  returned: { id?: string; status?: string; effective_source_type?: string | null; revoked_reason?: string | null } | undefined,
  label: string
): Promise<void> {
  assert.equal(returned?.id, seed.authorizationId, `${label} 应返回运行态授权记录`)
  assert.equal(returned?.status, 'returned', `${label} 应把运行态授权标记为 returned`)
  assert.equal(returned?.effective_source_type, null, `${label} 后运行态授权不应保留有效来源`)
  assert.equal(returned?.revoked_reason, 'grantee_returned', `${label} 应记录被授权人归还原因`)

  const grant = await client.one<{ status?: string; revoked_by?: string | null; revoked_at?: string | null }>(`
    SELECT status, revoked_by, revoked_at
    FROM ${table(client, 'resource_authorization_grants')}
    WHERE id = ?
  `, [seed.grantId])
  assert.equal(grant?.status, 'returned', `${label} 应把授权业务记录标记为 returned`)
  assert.equal(grant?.revoked_by, seed.granteeSystemAccountId, `${label} 应记录归还操作人`)
  assert.ok(grant?.revoked_at, `${label} 应记录归还时间`)

  const source = await client.one<{ status?: string; ended_reason?: string | null; revoked_by?: string | null }>(`
    SELECT status, ended_reason, revoked_by
    FROM ${table(client, 'resource_authorization_sources')}
    WHERE id = ?
  `, [seed.authorizationSourceId])
  assert.equal(source?.status, 'revoked', `${label} 应撤销手动授权来源`)
  assert.equal(source?.ended_reason, 'grantee_returned', `${label} 应记录授权来源结束原因`)
  assert.equal(source?.revoked_by, seed.granteeSystemAccountId, `${label} 应记录授权来源归还操作人`)
}

async function seedReturnableAccountAuthorization(client: DatabaseClient): Promise<SeedIds> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const now = new Date().toISOString()
  const profile = await client.one<ProviderProfileRow>(`
    SELECT id, provider_code, protocol_code, protocol_version
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE provider_code = 'gpt'
      AND enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(profile, '测试数据库应存在 GPT 协议档案')

  const granteeSystemAccountId = `sys_auth_return_driver_${suffix}`
  const sourceAccountId = `acc_auth_return_src_${suffix}`
  const authorizationId = `rauth_return_driver_${suffix}`
  const authorizationSourceId = `rauthsrc_return_driver_${suffix}`
  const grantId = `rag_return_driver_${suffix}`
  const instanceAccountId = `acc_auth_return_inst_${suffix}`

  await client.execute(`
    INSERT INTO ${table(client, 'system_accounts')} (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, 'user', 'active', ?, 0, 0, ?, ?)
  `, [
    granteeSystemAccountId,
    `auth_return_driver_${suffix}`,
    `授权归还驱动回归${suffix}`,
    'test-password-hash',
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'accounts')} (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, concurrency_limit, schedulable,
      authorization_instance_source_account_id, authorization_instance_authorization_id,
      authorization_instance_owner_system_account_id, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'api_key', 'active', ?, ?, 20, 1, NULL, NULL, NULL, ?, ?)
  `, [
    sourceAccountId,
    ownerSystemAccountId,
    profile.provider_code,
    profile.id,
    profile.protocol_code,
    profile.protocol_version,
    `授权归还源账户${suffix}`,
    '{}',
    'sk-***return',
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorizations')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at,
      last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
      revoked_by, revoked_at, revoked_reason, updated_at
    ) VALUES (?, 'account', ?, ?, ?, 'use', 'active', 'manual', NULL, ?, ?, ?, NULL, NULL, ?, ?, NULL, NULL, NULL, ?)
  `, [
    authorizationId,
    sourceAccountId,
    ownerSystemAccountId,
    granteeSystemAccountId,
    now,
    now,
    '授权归还驱动回归',
    ownerSystemAccountId,
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorization_sources')} (
      id, authorization_id, source_type, source_team_id, status, activated_at,
      ended_at, ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, 'manual', NULL, 'active', ?, NULL, NULL, ?, ?, NULL, NULL, ?)
  `, [authorizationSourceId, authorizationId, now, ownerSystemAccountId, now, now])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorization_grants')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
      grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
      limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, 'account', ?, ?, 'system_account', ?, NULL, 'use', 'active', ?, NULL, NULL, ?, ?, NULL, NULL, ?)
  `, [
    grantId,
    sourceAccountId,
    ownerSystemAccountId,
    granteeSystemAccountId,
    '授权归还驱动回归',
    ownerSystemAccountId,
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'accounts')} (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, concurrency_limit, schedulable,
      authorization_instance_source_account_id, authorization_instance_authorization_id,
      authorization_instance_owner_system_account_id, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 'api_key', 'active', ?, ?, 20, 1, ?, ?, ?, ?, ?)
  `, [
    instanceAccountId,
    granteeSystemAccountId,
    profile.provider_code,
    profile.id,
    profile.protocol_code,
    profile.protocol_version,
    `授权归还实例账户${suffix}`,
    '{}',
    'sk-***return',
    sourceAccountId,
    authorizationId,
    ownerSystemAccountId,
    now,
    now
  ])

  return {
    authorizationId,
    authorizationSourceId,
    grantId,
    granteeSystemAccountId,
    instanceAccountId,
    sourceAccountId
  }
}

async function seedReturnableGroupAuthorization(client: DatabaseClient): Promise<SeedIds> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const now = new Date().toISOString()
  const profile = await client.one<ProviderProfileRow>(`
    SELECT id, provider_code, protocol_code, protocol_version
    FROM ${table(client, 'provider_protocol_profiles')}
    WHERE provider_code = 'gpt'
      AND enabled = 1
    ORDER BY id ASC
    LIMIT 1
  `)
  assert(profile, '测试数据库应存在 GPT 协议档案')

  const granteeSystemAccountId = `sys_group_auth_return_${suffix}`
  const groupId = `grp_auth_return_${suffix}`
  const authorizationId = `rauth_group_return_${suffix}`
  const authorizationSourceId = `rauthsrc_group_return_${suffix}`
  const grantId = `rag_group_return_${suffix}`

  await client.execute(`
    INSERT INTO ${table(client, 'system_accounts')} (
      id, username, display_name, role, status, password_hash, must_change_password,
      image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, 'user', 'active', ?, 0, 0, ?, ?)
  `, [
    granteeSystemAccountId,
    `group_auth_return_${suffix}`,
    `分组授权归还驱动回归${suffix}`,
    'test-password-hash',
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'groups')} (
      id, system_account_id, name, provider_code, description, enabled, is_default, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, 1, 0, 'personal', NULL, ?, ?)
  `, [
    groupId,
    ownerSystemAccountId,
    `分组授权归还${suffix}`,
    profile.provider_code,
    '分组授权归还驱动回归',
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorizations')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
      scope, status, effective_source_type, effective_source_team_id, activated_at,
      last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
      revoked_by, revoked_at, revoked_reason, updated_at
    ) VALUES (?, 'group', ?, ?, ?, 'use', 'active', 'manual', NULL, ?, ?, ?, NULL, NULL, ?, ?, NULL, NULL, NULL, ?)
  `, [
    authorizationId,
    groupId,
    ownerSystemAccountId,
    granteeSystemAccountId,
    now,
    now,
    '分组授权归还驱动回归',
    ownerSystemAccountId,
    now,
    now
  ])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorization_sources')} (
      id, authorization_id, source_type, source_team_id, status, activated_at,
      ended_at, ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, 'manual', NULL, 'active', ?, NULL, NULL, ?, ?, NULL, NULL, ?)
  `, [authorizationSourceId, authorizationId, now, ownerSystemAccountId, now, now])

  await client.execute(`
    INSERT INTO ${table(client, 'resource_authorization_grants')} (
      id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
      grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
      limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, 'group', ?, ?, 'system_account', ?, NULL, 'use', 'active', ?, NULL, NULL, ?, ?, NULL, NULL, ?)
  `, [
    grantId,
    groupId,
    ownerSystemAccountId,
    granteeSystemAccountId,
    '分组授权归还驱动回归',
    ownerSystemAccountId,
    now,
    now
  ])

  return {
    authorizationId,
    authorizationSourceId,
    grantId,
    granteeSystemAccountId,
    groupId
  }
}

async function cleanupSeedRows(): Promise<void> {
  if (!createdSeedRows.length) return
  const client = await createCurrentDatabaseClient()
  for (const seed of createdSeedRows.splice(0)) {
    if (seed.instanceAccountId) {
      await client.execute(`DELETE FROM ${table(client, 'accounts')} WHERE id = ?`, [seed.instanceAccountId])
    }
    if (seed.sourceAccountId) {
      await client.execute(`DELETE FROM ${table(client, 'accounts')} WHERE id = ?`, [seed.sourceAccountId])
    }
    await client.execute(`DELETE FROM ${table(client, 'resource_authorization_sources')} WHERE authorization_id = ?`, [seed.authorizationId])
    await client.execute(`DELETE FROM ${table(client, 'resource_authorization_grants')} WHERE id = ?`, [seed.grantId])
    await client.execute(`DELETE FROM ${table(client, 'resource_authorizations')} WHERE id = ?`, [seed.authorizationId])
    if (seed.groupId) {
      await client.execute(`DELETE FROM ${table(client, 'groups')} WHERE id = ?`, [seed.groupId])
    }
    await client.execute(`DELETE FROM ${table(client, 'system_sessions')} WHERE system_account_id = ?`, [seed.granteeSystemAccountId])
    await client.execute(`DELETE FROM ${table(client, 'system_accounts')} WHERE id = ?`, [seed.granteeSystemAccountId])
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const { closePostgresPool } = await import('../../storage/postgres-client.js')
    await closePostgresPool()
  }
}

async function createCurrentDatabaseClient(): Promise<DatabaseClient> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  const [{ createSqliteDatabaseClient }, { getBusinessDatabase }] = await Promise.all([
    import('../../storage/database-client.js'),
    import('../../storage/database.js')
  ])
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function table(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function closeStorage(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may run against PostgreSQL only.
  }
}
