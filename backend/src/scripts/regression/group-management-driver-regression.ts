import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { AccessScope } from '../../storage/access-scope.js'

const createdGroupIds: string[] = []
const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }

if (process.env.JUHE_GROUP_MANAGEMENT_DRIVER_CHILD === 'postgres') {
  const repositories = await import('../../storage/repositories.js')
  try {
    await assertGroupManagementAsync(repositories)
  } finally {
    await cleanupCreatedGroups()
  }
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-group-management-driver-'))
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
  await assertGroupManagementAsync(repositories)

  if (process.env.JUHE_GROUP_MANAGEMENT_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_GROUP_MANAGEMENT_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_GROUP_MANAGEMENT_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_GROUP_MANAGEMENT_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_GROUP_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('group-management-driver-regression passed')
} finally {
  await cleanupCreatedGroups()
  await closeStorage()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertGroupManagementAsync(repositories: typeof import('../../storage/repositories.js')): Promise<void> {
  const suffix = `${Date.now()}${Math.random().toString(16).slice(2, 8)}`
  const name = `分组管理回归${suffix}`
  const created = await repositories.createGroupAsync({
    name,
    providerCode: 'gpt',
    description: '分组管理PG回归',
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 20,
      maxQueueWaitMs: 30_000,
      clientIpConcurrencyLimit: 2,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 1
    }
  }, adminAccess)
  createdGroupIds.push(created.id)
  assert.equal(created.name, name, '异步创建分组应返回名称')
  assert.equal(created.providerCode, 'gpt', '异步创建分组应保留供应商')
  assert.equal(created.groupType, 'high_concurrency', '异步创建分组应保存高并发类型')

  const page = await repositories.listGroupsPageAsync(adminAccess, { keyword: name, page: 1, pageSize: 20 })
  assert.ok(page.items.some((item) => item.id === created.id), '异步分组列表应能按名称查到新分组')

  const options = await repositories.listGroupOptionsAsync(adminAccess, { ids: [created.id], limit: 10 })
  assert.deepEqual(options.map((item) => item.id), [created.id], '异步分组选项应支持按 ID 精确读取')

  const accountOptions = await repositories.listAccountGroupOptionsAsync(adminAccess, { ids: [created.id], limit: 10 })
  assert.deepEqual(accountOptions.map((item) => item.id), [created.id], '异步账户组选项应支持按 ID 精确读取')
  assert.deepEqual(accountOptions[0]?.accountIds, [], '新建分组不应携带账户绑定')

  await assert.rejects(
    () => repositories.createGroupAsync({
      name,
      providerCode: 'gpt',
      description: '分组管理PG回归重复'
    }, adminAccess),
    /同一供应商下分组名称已存在/,
    '异步创建分组不能重复同供应商名称'
  )

  const renamed = await repositories.updateGroupAsync(created.id, {
    name: `${name}改`,
    description: '分组管理PG回归已更新',
    enabled: false
  }, adminAccess)
  assert.equal(renamed?.name, `${name}改`, '异步更新分组应返回新名称')
  assert.equal(renamed?.enabled, false, '异步更新分组应更新启用状态')

  const found = await repositories.findGroupSummaryAsync(created.id, adminAccess)
  assert.equal(found?.description, '分组管理PG回归已更新', '异步读取分组摘要应返回更新后的说明')

  const deleted = await repositories.deleteGroupAsync(created.id, adminAccess)
  createdGroupIds.splice(createdGroupIds.indexOf(created.id), 1)
  assert.equal(deleted.deleted, true, '异步删除分组应返回 deleted=true')
  assert.equal((await repositories.findGroupSummaryAsync(created.id, adminAccess)), undefined, '删除后异步摘要应不可见')
}

async function cleanupCreatedGroups(): Promise<void> {
  if (!createdGroupIds.length) {
    return
  }
  if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
    const [{ createPostgresDatabaseClient }, { closePostgresPool, getPostgresPool }] = await Promise.all([
      import('../../storage/database-client.js'),
      import('../../storage/postgres-client.js')
    ])
    const client = createPostgresDatabaseClient(await getPostgresPool())
    for (const id of createdGroupIds.splice(0)) {
      await client.execute('DELETE FROM "juhe_business"."route_strategy_groups" WHERE group_id = ?', [id])
      await client.execute('DELETE FROM "juhe_business"."groups" WHERE id = ?', [id])
    }
    await closePostgresPool()
    return
  }
  const { getBusinessDatabase } = await import('../../storage/database.js')
  const database = getBusinessDatabase()
  for (const id of createdGroupIds.splice(0)) {
    database.prepare('DELETE FROM route_strategy_groups WHERE group_id = ?').run(id)
    database.prepare('DELETE FROM groups WHERE id = ?').run(id)
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
