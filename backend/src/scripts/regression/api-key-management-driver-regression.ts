import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccessScope } from '../../storage/access-scope.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { DEFAULT_BUILT_IN_GROUPS, DEFAULT_GPT_GROUP } from '../../storage/schema-defaults.js'

const createdApiKeyIds: string[] = []
const createdGroupIds: string[] = []
const createdAccountIds: string[] = []
const createdRouteStrategyIds: string[] = []
const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const driverRegressionModel = 'gpt-5-mini'
const driverRegressionUpstreamModel = 'gpt-5'
const defaultRouteResourceCount = DEFAULT_BUILT_IN_GROUPS.filter((group) => group.providerCode !== HYBRID_PROVIDER_CODE).length

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
  process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
  process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
  process.env.JUHE_AI_CACHE_DRIVER = 'memory'
  process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
  process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '2'
  process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
  process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
  process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
  process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
  process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
  process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

  const repositories = await import('../../storage/repositories.js')
  const { getBusinessDatabase } = await import('../../storage/database.js')
  getBusinessDatabase()
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
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_API_KEY_MANAGEMENT_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_API_KEY_MANAGEMENT_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_API_KEY_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_API_KEY_MANAGEMENT_REDIS_QUEUE_URL ?? process.env.JUHE_API_KEY_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
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
  const defaultApiKeyPage = await repositories.listApiKeysPageAsync({ ...adminAccess, systemAccountFilterId: adminAccess.systemAccountId }, { page: 1, pageSize: 50 })
  const defaultApiKeys = defaultApiKeyPage.items.filter((item) => item.isDefault)
  assert.equal(defaultApiKeys.length, defaultRouteResourceCount, 'API Key 列表应包含非混合默认路由对应的默认 API Key')
  assert.equal(defaultApiKeys.every((item) => item.routeStrategyMode === 'normal'), true, '默认 API Key 必须绑定默认普通路由')
  const defaultApiKey = defaultApiKeys[0]
  assert(defaultApiKey, '默认 API Key 名称保护回归需要至少一个默认 Key')
  assert.equal((await repositories.updateApiKeyAsync(defaultApiKey.id, { name: defaultApiKey.name }, adminAccess))?.name, defaultApiKey.name, '默认 API Key 携带原名称更新其他字段时不应被误拦截')
  await assert.rejects(
    repositories.updateApiKeyAsync(defaultApiKey.id, { name: `${defaultApiKey.name}改` }, adminAccess),
    /默认 API Key 不允许修改名称/,
    '默认 API Key 必须拒绝修改名称'
  )
  const defaultRouteStrategy = await repositories.findRouteStrategySummaryAsync(defaultApiKey.routeStrategyId, adminAccess)
  assert(defaultRouteStrategy, '默认策略路由名称保护回归需要默认 Key 绑定的策略路由')
  assert.equal((await repositories.updateRouteStrategyAsync(defaultRouteStrategy.id, { name: defaultRouteStrategy.name }, adminAccess))?.name, defaultRouteStrategy.name, '默认策略路由携带原名称更新其他字段时不应被误拦截')
  await assert.rejects(
    repositories.updateRouteStrategyAsync(defaultRouteStrategy.id, { name: `${defaultRouteStrategy.name}改` }, adminAccess),
    /默认策略路由不允许修改名称/,
    '默认策略路由必须拒绝修改名称'
  )
  const chatApiKeyId = await repositories.ensureChatApiKeyForSystemAccountAsync(adminAccess.systemAccountId)
  assert.equal(await repositories.ensureChatApiKeyForSystemAccountAsync(adminAccess.systemAccountId), chatApiKeyId, 'AI 对话专用 Key 补齐必须幂等')
  const chatApiKey = await repositories.findChatApiKeySecretByPurposeAsync(adminAccess.systemAccountId)
  assert.equal(chatApiKey?.id, chatApiKeyId, 'AI 对话必须按专用用途读取 Key')
  assert.equal(chatApiKey?.status, 'active', 'AI 对话专用 Key 必须默认处于可用状态')
  assert.ok(chatApiKey?.key?.startsWith('sk-'), 'AI 对话专用 Key 查询必须返回服务端调用所需完整密钥')
  assert.equal((await repositories.findChatApiKeySecretAsync(chatApiKeyId, adminAccess.systemAccountId))?.key, chatApiKey?.key, '聊天专用 Key ID 查询必须返回同一完整密钥')
  assert.equal(await repositories.findChatApiKeySecretAsync(chatApiKeyId, 'sys_other'), undefined, '聊天专用 Key 查询不得跨系统账户读取密钥')
  assert.equal(await repositories.findChatApiKeySecretByPurposeAsync('sys_other'), undefined, '聊天用途查询不得跨系统账户读取密钥')
  const chatSummary = (await repositories.listApiKeysPageAsync({ ...adminAccess, systemAccountFilterId: adminAccess.systemAccountId }, { page: 1, pageSize: 50 })).items.find((item) => item.id === chatApiKeyId)
  assert.equal(chatSummary?.purpose, 'chat', 'API Key 列表必须暴露 AI 对话用途')
  assert.equal(chatSummary?.isDefault, false, 'AI 对话专用 Key 不能占用不可换路由的默认 Key 身份')
  assert(chatSummary, 'AI 对话 API Key 名称保护回归需要列表摘要')
  assert.equal((await repositories.updateApiKeyAsync(chatApiKeyId, { name: chatSummary.name }, adminAccess))?.name, chatSummary.name, 'AI 对话 API Key 携带原名称更新其他字段时不应被误拦截')
  await assert.rejects(
    repositories.updateApiKeyAsync(chatApiKeyId, { name: `${chatSummary.name}改` }, adminAccess),
    /AI 对话 API Key 不允许修改名称/,
    'AI 对话 API Key 必须拒绝修改名称'
  )
  await assert.rejects(
    repositories.deleteApiKeyWithRelatedCleanupAsync(chatApiKeyId, adminAccess),
    /AI 对话 API Key 不允许删除/,
    'AI 对话专用 Key 必须拒绝删除'
  )

  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const group = await repositories.createGroupAsync({
    name: `APIKey回归分组${suffix}`,
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    enabled: true
  }, adminAccess)
  createdGroupIds.push(group.id)

  const name = `APIKey管理回归${suffix}`
  const routeStrategy = await repositories.createRouteStrategyAsync({
    name: `${name}策略路由`,
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, weight: 10, status: 'active' }]
  }, adminAccess)
  createdRouteStrategyIds.push(routeStrategy.id)
  const created = await repositories.createApiKeyRecordAsync({
    name,
    description: 'API Key 管理PG回归',
    routeStrategyId: routeStrategy.id,
    status: 'active',
    quotaLimits: {
      hourly: { enabled: true, hours: 3, limit: 1.25 }
    }
  }, adminAccess)
  createdApiKeyIds.push(created.id)
  const accountId = await seedActiveGatewayAccountForGroup(repositories, group.id, GPT_OPENAI_V1_PROFILE_ID, suffix)
  createdAccountIds.push(accountId)
  assert.equal(created.name, name, '异步创建 API Key 应返回名称')
  assert.ok(created.key.startsWith('sk-'), '异步创建 API Key 应返回一次性明文密钥')
  assert.equal(created.routeStrategyId, routeStrategy.id, '异步创建 API Key 应保存策略路由绑定')
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
  assert.deepEqual([...(gatewayRuntime.accounts[0]?.supportedModels ?? [])].sort(), [driverRegressionModel, driverRegressionUpstreamModel].sort(), 'DB service 网关运行态应读取候选账号支持模型')
  assert.deepEqual(gatewayRuntime.accounts[0]?.modelMappings, [{
    sourceModel: driverRegressionModel,
    sourceEndpointFamily: 'chat_completions',
    upstreamModel: driverRegressionUpstreamModel,
    upstreamEndpointFamily: 'chat_completions',
    enabled: true
  }], 'DB service 网关运行态应读取候选账号模型映射')
  assert.ok((gatewayRuntime.responseInspectionPolicies?.length ?? 0) > 0, 'DB service 网关运行态应包含默认响应检查策略')

  const page = await repositories.listApiKeysPageAsync(adminAccess, { keyword: name, page: 1, pageSize: 20 })
  assert.ok(page.items.some((item) => item.id === created.id), '异步 API Key 列表应能按名称查到新 Key')

  const secret = await repositories.findApiKeySecretAsync(created.id, adminAccess)
  assert.equal(secret?.key, created.key, '异步 API Key secret 查询应返回当前完整密钥')
  assert.equal((await repositories.findChatApiKeySecretAsync(created.id, adminAccess.systemAccountId))?.key, created.key, '启用且未过期的 Key 必须可供聊天链路读取')

  await repositories.updateApiKeyAsync(created.id, {
    expiresAt: '2000-01-01T00:00:00.000Z'
  }, adminAccess)
  assert.equal(await repositories.findChatApiKeySecretAsync(created.id, adminAccess.systemAccountId), undefined, '已过期 Key 不得供聊天链路读取')

  await repositories.updateRouteStrategyAsync(routeStrategy.id, {
    groupBindings: [{ groupId: group.id, priority: 1, weight: 20, status: 'active' }]
  }, adminAccess)
  const updated = await repositories.updateApiKeyAsync(created.id, {
    name: `${name}改`,
    description: 'API Key 管理PG回归已更新',
    status: 'disabled'
  }, adminAccess)
  assert.equal(updated?.name, `${name}改`, '异步更新 API Key 应返回新名称')
  assert.equal(updated?.status, 'disabled', '异步更新 API Key 应更新状态')
  assert.equal(await repositories.findChatApiKeySecretAsync(created.id, adminAccess.systemAccountId), undefined, '已停用 Key 不得供聊天链路读取')
  assert.equal((await repositories.findRouteStrategySummaryAsync(routeStrategy.id, adminAccess))?.groupBindings[0]?.weight, 20, '异步更新策略路由应更新绑定权重')
  assert.equal(await repositories.validateGatewayApiKeyAsync(created.key), undefined, '异步停用 API Key 后网关校验应失效')

  await assert.rejects(
    () => repositories.createApiKeyRecordAsync({
      name: `${name}改`,
      routeStrategyId: routeStrategy.id
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
  const routeDeleted = await repositories.deleteRouteStrategyAsync(routeStrategy.id, adminAccess)
  createdRouteStrategyIds.splice(createdRouteStrategyIds.indexOf(routeStrategy.id), 1)
  assert.equal(routeDeleted, true, '异步删除 API Key 后应清理测试策略路由')
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
      const routeRows = await client.query<{ route_strategy_id?: string }>('SELECT route_strategy_id FROM "juhe_business"."api_keys" WHERE id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."api_keys" WHERE id = ?', [id])
      for (const routeRow of routeRows) {
        if (!routeRow.route_strategy_id) continue
        await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [routeRow.route_strategy_id])
        await client.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [routeRow.route_strategy_id])
      }
    }
    for (const id of createdRouteStrategyIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE route_strategy_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."route_strategies" WHERE id = ?', [id])
    }
    for (const id of createdGroupIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE group_id = ?', [id])
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
    const routeRows = database.prepare('SELECT route_strategy_id FROM api_keys WHERE id = ?').all(id) as Array<{ route_strategy_id?: string }>
    database.prepare('DELETE FROM api_keys WHERE id = ?').run(id)
    for (const routeRow of routeRows) {
      if (!routeRow.route_strategy_id) continue
      database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(routeRow.route_strategy_id)
      database.prepare('DELETE FROM route_strategies WHERE id = ?').run(routeRow.route_strategy_id)
    }
  }
  for (const id of createdRouteStrategyIds.splice(0)) {
    database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(id)
    database.prepare('DELETE FROM route_strategies WHERE id = ?').run(id)
  }
  for (const id of createdGroupIds.splice(0)) {
    database.prepare('DELETE FROM route_strategy_groups WHERE group_id = ?').run(id)
    database.prepare('DELETE FROM groups WHERE id = ?').run(id)
  }
}

async function seedActiveGatewayAccountForGroup(
  repositories: typeof import('../../storage/repositories.js'),
  groupId: string,
  providerProtocolProfileId: string | undefined,
  suffix: string
): Promise<string> {
  const apiKey = `sk-api-key-management-driver-${suffix}`
  const account = await repositories.createAccountAsync({
    name: `APIKey管理回归账号${suffix}`,
    providerCode: DEFAULT_GPT_GROUP.providerCode,
    providerProtocolProfileId,
    type: 'api_key',
    status: 'active',
    groupId,
    credentials: {
      api_key: apiKey,
      base_url: 'https://example.invalid/v1'
    },
    concurrencyLimit: 20,
    supportedModels: [driverRegressionModel, driverRegressionUpstreamModel],
    healthCheckModel: driverRegressionModel,
    modelMappings: [{
      sourceModel: driverRegressionModel,
      sourceEndpointFamily: 'chat_completions',
      upstreamModel: driverRegressionUpstreamModel,
      upstreamEndpointFamily: 'chat_completions',
      enabled: true
    }]
  }, adminAccess)
  assert.equal(await repositories.recordAccountHealthCheckSuccessAsync(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, 'API Key 管理回归账号应显式通过后台健康成功激活')
  return account.id
}

async function closeStorage(): Promise<void> {
  try {
    const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
    await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  } catch {
    // The regression may fail before the SQLite read worker pool is imported.
  }
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}
