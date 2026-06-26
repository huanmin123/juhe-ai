import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccessScope } from '../../storage/access-scope.js'

const createdApiKeyIds: string[] = []
const createdGroupIds: string[] = []
const createdAccountIds: string[] = []
const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

if (process.env.JUHE_API_KEY_MANAGEMENT_DRIVER_CHILD === 'postgres') {
  const repositories = await import('../../storage/repositories.js')
  try {
    await assertApiKeyManagementAsync(repositories)
  } finally {
    await cleanupCreatedRows()
  }
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-api-key-management-driver-'))
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

  const repositories = await import('../../storage/repositories.js')
  await assertApiKeyManagementAsync(repositories)

  if (process.env.JUHE_API_KEY_MANAGEMENT_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_API_KEY_MANAGEMENT_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_API_KEY_MANAGEMENT_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_API_KEY_MANAGEMENT_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_API_KEY_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('api-key-management-driver-regression passed')
} finally {
  await cleanupCreatedRows()
  await closeStorage()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertApiKeyManagementAsync(repositories: typeof import('../../storage/repositories.js')): Promise<void> {
  const { logger } = await import('../../shared/logger.js')
  logger.level = 'silent'
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const group = await repositories.createGroupAsync({
    name: `APIKey回归分组${suffix}`,
    providerCode: 'gpt',
    enabled: true
  }, adminAccess)
  createdGroupIds.push(group.id)

  const name = `APIKey管理回归${suffix}`
  const created = await repositories.createApiKeyRecordAsync({
    name,
    description: 'API Key 管理PG回归',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 10, status: 'active' }],
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 3, limit: 1.25 }
    }
  }, adminAccess)
  createdApiKeyIds.push(created.id)
  const accountId = await seedActiveGatewayAccountForGroup(group.id, suffix)
  createdAccountIds.push(accountId)
  assert.equal(created.name, name, '异步创建 API Key 应返回名称')
  assert.ok(created.key.startsWith('sk-'), '异步创建 API Key 应返回一次性明文密钥')
  assert.equal(created.groupBindings[0]?.groupId, group.id, '异步创建 API Key 应保存分组绑定')
  const gatewayApiKey = await repositories.validateGatewayApiKeyAsync(created.key)
  assert.equal(gatewayApiKey?.id, created.id, '网关 API Key 校验应能读取异步创建的 Key')
  assert.equal(gatewayApiKey?.selected_group_id, group.id, '网关 API Key 校验应选中绑定分组')
  assert.equal(gatewayApiKey?.group_bindings?.[0]?.weight, 10, '网关 API Key 校验应读取绑定权重')

  const { handleDbServiceOperation } = await import('../../modules/db-service/db-service-handlers.js')
  const gatewayRuntime = await handleDbServiceOperation({ type: 'read_gateway_runtime', key: created.key })
  assert.equal(gatewayRuntime.apiKey?.id, created.id, 'DB service 网关运行态应能读取异步创建的 API Key')
  assert.equal(gatewayRuntime.groupAccess?.groupOwnerSystemAccountId, adminAccess.systemAccountId, 'DB service 网关运行态应返回分组访问元数据')
  assert.equal(gatewayRuntime.accounts.length, 1, 'DB service 网关运行态应返回分组内可调度账号候选')
  assert.equal(gatewayRuntime.accounts[0]?.id, accountId, 'DB service 网关运行态候选账号应来自当前分组绑定')
  assert.equal(gatewayRuntime.accounts[0]?.apiKey, `sk-api-key-management-driver-${suffix}`, 'DB service 网关运行态应解密候选账号 API Key')
  assert.deepEqual(gatewayRuntime.accounts[0]?.supportedModels, [`gpt-api-key-management-driver-${suffix}`], 'DB service 网关运行态应读取候选账号支持模型')
  assert.deepEqual(gatewayRuntime.accounts[0]?.modelMappings, [{
    sourceModel: `downstream-api-key-management-driver-${suffix}`,
    sourceEndpointFamily: 'responses',
    upstreamModel: `upstream-api-key-management-driver-${suffix}`,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }], 'DB service 网关运行态应读取候选账号模型映射')
  assert.ok((gatewayRuntime.responseInspectionPolicies?.length ?? 0) > 0, 'DB service 网关运行态应包含默认响应检查策略')

  const page = await repositories.listApiKeysPageAsync(adminAccess, { keyword: name, page: 1, pageSize: 20 })
  assert.ok(page.items.some((item) => item.id === created.id), '异步 API Key 列表应能按名称查到新 Key')

  const secret = await repositories.findApiKeySecretAsync(created.id, adminAccess)
  assert.equal(secret?.key, created.key, '异步 API Key secret 查询应返回当前完整密钥')

  const updated = await repositories.updateApiKeyAsync(created.id, {
    name: `${name}改`,
    description: 'API Key 管理PG回归已更新',
    status: 'disabled',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 20, status: 'active' }]
  }, adminAccess)
  assert.equal(updated?.name, `${name}改`, '异步更新 API Key 应返回新名称')
  assert.equal(updated?.status, 'disabled', '异步更新 API Key 应更新状态')
  assert.equal(updated?.groupBindings[0]?.weight, 20, '异步更新 API Key 应更新绑定权重')
  assert.equal(await repositories.validateGatewayApiKeyAsync(created.key), undefined, '异步停用 API Key 后网关校验应失效')

  await assert.rejects(
    () => repositories.createApiKeyRecordAsync({
      name: `${name}改`,
      groupBindings: [{ groupId: group.id }]
    }, adminAccess),
    /API Key 名称已存在/,
    '异步创建 API Key 不能重复同账户名称'
  )

  const refreshed = await repositories.refreshApiKeySecretAsync(created.id, adminAccess)
  assert.ok(refreshed?.key.startsWith('sk-'), '异步刷新 API Key 应返回新完整密钥')
  assert.notEqual(refreshed?.key, created.key, '异步刷新 API Key 应更换明文密钥')

  const deleted = await repositories.deleteApiKeyWithRelatedCleanupAsync(created.id, adminAccess)
  createdApiKeyIds.splice(createdApiKeyIds.indexOf(created.id), 1)
  assert.equal(deleted.deleted, true, '异步删除 API Key 应返回 deleted=true')
  assert.equal((await repositories.findApiKeySummaryAsync(created.id, adminAccess)), undefined, '删除后异步摘要应不可见')
}

async function cleanupCreatedRows(): Promise<void> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { closePostgresPool, getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of createdAccountIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."group_accounts" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_supported_models" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_model_mappings" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."account_api_key_runtime_states" WHERE account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."accounts" WHERE id = ?', [id])
    }
    for (const id of createdApiKeyIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."api_key_group_bindings" WHERE api_key_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."api_keys" WHERE id = ?', [id])
    }
    for (const id of createdGroupIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."api_key_group_bindings" WHERE group_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE id = ?', [id])
    }
    await closePostgresPool()
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of createdAccountIds.splice(0)) {
    database.prepare('DELETE FROM group_accounts WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_supported_models WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_model_mappings WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM account_api_key_runtime_states WHERE account_id = ?').run(id)
    database.prepare('DELETE FROM accounts WHERE id = ?').run(id)
  }
  for (const id of createdApiKeyIds.splice(0)) {
    database.prepare('DELETE FROM api_key_group_bindings WHERE api_key_id = ?').run(id)
    database.prepare('DELETE FROM api_keys WHERE id = ?').run(id)
  }
  for (const id of createdGroupIds.splice(0)) {
    database.prepare('DELETE FROM api_key_group_bindings WHERE group_id = ?').run(id)
    database.prepare('DELETE FROM groups WHERE id = ?').run(id)
  }
}

async function seedActiveGatewayAccountForGroup(groupId: string, suffix: string): Promise<string> {
  const [{ runtimeConfig }, { encryptJson }, { newId, nowIso }] = await Promise.all([
    import('../../config/runtime.js'),
    import('../../storage/crypto.js'),
    import('../../storage/database.js')
  ])
  const accountId = newId('acct')
  const now = nowIso()
  const apiKey = `sk-api-key-management-driver-${suffix}`
  const credentialsEncrypted = encryptJson({
    api_key: apiKey,
    base_url: 'https://example.invalid/v1'
  })
  if (runtimeConfig.databaseDriver === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const group = await client.one<{
      system_account_id: string
      provider_code: string
      provider_protocol_profile_id: string
      protocol_code: string
      protocol_version: string
    }>('SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version FROM "juhe_business"."groups" WHERE id = ?', [groupId])
    assert.ok(group, '回归分组应存在')
    await client.execute(`
      INSERT INTO "juhe_business"."accounts" (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
        super_priority_enabled, fallback_enabled, client_compatibility, schedulable, availability_schedule_active,
        stream_failure_count, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, 'api_key', 'active', ?, ?, 20, 0, 0, 0, 'openai_standard', 1, 1, 0, ?, ?)
    `, [
      accountId,
      group.system_account_id,
      group.provider_code,
      group.provider_protocol_profile_id,
      group.protocol_code,
      group.protocol_version,
      `APIKey管理回归账号${suffix}`,
      credentialsEncrypted,
      'sk-****',
      now,
      now
    ])
    await client.execute(`
      INSERT INTO "juhe_business"."group_accounts" (
        system_account_id, group_id, account_id, local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, 0, 0, 0, 1, ?, ?)
    `, [group.system_account_id, groupId, accountId, now, now])
    await seedGatewayAccountModelConfig(accountId, suffix)
    return accountId
  }

  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  const group = database
    .prepare('SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version FROM groups WHERE id = ?')
    .get(groupId) as {
      system_account_id: string
      provider_code: string
      provider_protocol_profile_id: string
      protocol_code: string
      protocol_version: string
    } | undefined
  assert.ok(group, '回归分组应存在')
  database.prepare(`
    INSERT INTO accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_mask, concurrency_limit, priority,
      super_priority_enabled, fallback_enabled, client_compatibility, schedulable, availability_schedule_active,
      stream_failure_count, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, 'api_key', 'active', ?, ?, 20, 0, 0, 0, 'openai_standard', 1, 1, 0, ?, ?)
  `).run(
    accountId,
    group.system_account_id,
    group.provider_code,
    group.provider_protocol_profile_id,
    group.protocol_code,
    group.protocol_version,
    `APIKey管理回归账号${suffix}`,
    credentialsEncrypted,
    'sk-****',
    now,
    now
  )
  database.prepare(`
    INSERT INTO group_accounts (
      system_account_id, group_id, account_id, local_priority, local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at
    )
    VALUES (?, ?, ?, 0, 0, 0, 1, ?, ?)
  `).run(group.system_account_id, groupId, accountId, now, now)
  await seedGatewayAccountModelConfig(accountId, suffix)
  return accountId
}

async function seedGatewayAccountModelConfig(accountId: string, suffix: string): Promise<void> {
  const [{ replaceAccountSupportedModelsAsync }, { replaceAccountModelMappingsAsync }] = await Promise.all([
    import('../../storage/account-supported-models.repository.js'),
    import('../../storage/account-model-mappings.repository.js')
  ])
  await replaceAccountSupportedModelsAsync(accountId, 'gpt', [`gpt-api-key-management-driver-${suffix}`])
  await replaceAccountModelMappingsAsync(accountId, 'gpt', [{
    sourceModel: `downstream-api-key-management-driver-${suffix}`,
    sourceEndpointFamily: 'responses',
    upstreamModel: `upstream-api-key-management-driver-${suffix}`,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }])
}

async function closeStorage(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}
