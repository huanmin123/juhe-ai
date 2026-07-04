import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { HYBRID_PROVIDER_CODE } from '../../domain/provider-protocol.js'
import { DEFAULT_BUILT_IN_GROUPS } from '../../storage/schema-defaults.js'

const createdSystemAccountIds: string[] = []
const defaultRouteResourceCount = DEFAULT_BUILT_IN_GROUPS.filter((group) => group.providerCode !== HYBRID_PROVIDER_CODE).length

if (process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_DRIVER_CHILD === 'postgres') {
  const repositories = await import('../../storage/repositories.js')
  try {
    await assertSystemAccountManagementAsync(repositories)
  } finally {
    await cleanupCreatedSystemAccounts()
  }
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-system-account-management-driver-'))
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
  await assertSystemAccountManagementAsync(repositories)

  if (process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_SYSTEM_ACCOUNT_MANAGEMENT_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_REDIS_QUEUE_URL ?? process.env.JUHE_SYSTEM_ACCOUNT_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('system-account-management-driver-regression passed')
} finally {
  await cleanupCreatedSystemAccounts()
  await closeStorage()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSystemAccountManagementAsync(repositories: typeof import('../../storage/repositories.js')): Promise<void> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const username = `sys_mgmt_${suffix}`
  const displayName = `系统账户管理回归${suffix}`
  const created = await repositories.createSystemAccountAsync({
    username,
    displayName,
    description: '系统账户管理PG回归',
    password: `Pwd${suffix}`,
    role: 'user',
    status: 'active',
    mustChangePassword: false,
    imageGenerationEnabled: false
  })
  createdSystemAccountIds.push(created.id)
  assert.equal(created.username, username, '异步创建系统账户应返回用户名')
  assert.equal(created.mustChangePassword, false, '普通用户显式关闭下次登录改密应生效')

  const defaultGroupCount = await defaultGroupCountForSystemAccount(created.id)
  assert.equal(defaultGroupCount, DEFAULT_BUILT_IN_GROUPS.length, '异步创建系统账户应同步创建全部默认内置分组')
  const createdDefaultRouteStrategyCount = await defaultRouteStrategyCountForSystemAccount(created.id)
  assert.equal(createdDefaultRouteStrategyCount, defaultRouteResourceCount, '异步创建系统账户应为非混合默认分组创建默认普通路由')
  const createdDefaultApiKeyCount = await defaultApiKeyCountForSystemAccount(created.id)
  assert.equal(createdDefaultApiKeyCount, defaultRouteResourceCount, '异步创建系统账户应为每条默认普通路由创建默认 API Key')

  const routeStrategyOptions = await repositories.listRouteStrategyOptionsAsync({ systemAccountId: created.id, role: 'user' }, { limit: DEFAULT_BUILT_IN_GROUPS.length + 5 })
  assert.equal(routeStrategyOptions.length, defaultRouteResourceCount, '策略路由选项应补齐非混合默认分组对应的默认普通路由')
  assert.equal(routeStrategyOptions.every((item) => item.isDefault && item.mode === 'normal'), true, '默认策略路由必须都是普通路由')

  const page = await repositories.listSystemAccountsPageAsync({ keyword: username, page: 1, pageSize: 20 })
  assert.ok(page.items.some((item) => item.id === created.id), '异步系统账户列表应能按用户名查到新账户')

  const options = await repositories.listSystemAccountOptionsAsync({ ids: [created.id], limit: 10 })
  assert.deepEqual(options.map((item) => item.id), [created.id], '异步系统账户选项应支持按 ID 精确读取')

  const renamed = await repositories.updateSystemAccountAsync(created.id, {
    displayName: `${displayName}改`,
    description: '系统账户管理PG回归已更新',
    imageGenerationEnabled: true
  })
  assert.equal(renamed?.displayName, `${displayName}改`, '异步更新系统账户应返回新显示名')
  assert.equal(renamed?.imageGenerationEnabled, true, '异步更新系统账户应更新图像生成开关')

  await assert.rejects(
    () => repositories.createSystemAccountAsync({
      username,
      displayName: `${displayName}重复`,
      password: `Pwd${suffix}x`,
      role: 'user'
    }),
    /用户账户已存在/,
    '异步创建系统账户不能重复用户名'
  )

  const disabled = await repositories.updateSystemAccountAsync(created.id, { status: 'disabled' })
  assert.equal(disabled?.status, 'disabled', '异步更新系统账户应能禁用账户')
}

async function defaultGroupCountForSystemAccount(systemAccountId: string): Promise<number> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<{ count?: string | number }>(
      'SELECT COUNT(*) AS count FROM "juhe_business"."groups" WHERE system_account_id = ? AND is_default = 1',
      [systemAccountId]
    )
    return Number(row?.count ?? 0)
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const row = getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM groups WHERE system_account_id = ? AND is_default = 1')
    .get(systemAccountId) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

async function defaultRouteStrategyCountForSystemAccount(systemAccountId: string): Promise<number> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<{ count?: string | number }>(
      'SELECT COUNT(*) AS count FROM "juhe_business"."route_strategies" WHERE system_account_id = ? AND is_default = 1',
      [systemAccountId]
    )
    return Number(row?.count ?? 0)
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const row = getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM route_strategies WHERE system_account_id = ? AND is_default = 1')
    .get(systemAccountId) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

async function defaultApiKeyCountForSystemAccount(systemAccountId: string): Promise<number> {
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    const row = await client.one<{ count?: string | number }>(
      'SELECT COUNT(*) AS count FROM "juhe_business"."api_keys" WHERE system_account_id = ? AND is_default = 1',
      [systemAccountId]
    )
    return Number(row?.count ?? 0)
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const row = getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM api_keys WHERE system_account_id = ? AND is_default = 1')
    .get(systemAccountId) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

async function cleanupCreatedSystemAccounts(): Promise<void> {
  if (!createdSystemAccountIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { closePostgresPool, getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of createdSystemAccountIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."api_keys" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."route_strategies" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_sessions" WHERE system_account_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."system_accounts" WHERE id = ?', [id])
    }
    await closePostgresPool()
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of createdSystemAccountIds.splice(0)) {
    database.prepare('DELETE FROM api_keys WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM route_strategy_groups WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM route_strategies WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM groups WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_sessions WHERE system_account_id = ?').run(id)
    database.prepare('DELETE FROM system_accounts WHERE id = ?').run(id)
  }
}

async function closeStorage(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}
