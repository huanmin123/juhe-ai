import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const backendSrcRoot = fileURLToPath(new URL('../..', import.meta.url))
const settingsRepositorySource = readFileSync(resolve(backendSrcRoot, 'storage/settings.repository.ts'), 'utf8')

assert(settingsRepositorySource.includes('createSharedJsonCache<Record<string, unknown>>'), '设置 async 缓存应声明 Redis JSON 共享缓存')
assertFunctionIncludes(settingsRepositorySource, 'listGlobalSettingsAsync', 'getGlobalSettingsSharedCache()', '全局设置 async 读取应先读 Redis 共享缓存')
assertFunctionIncludes(settingsRepositorySource, 'getSettingsAsync', 'getSystemSettingsSharedCache()', '系统设置 async 读取应先读 Redis 共享缓存')
assertFunctionIncludes(settingsRepositorySource, 'updateGlobalSettingsAsync', 'loadGlobalSettingsFromDatabaseAsync()', '全局设置 async 更新后应绕过旧共享缓存从数据库重读')
assertFunctionIncludes(settingsRepositorySource, 'updateSettingsAsync', 'loadSystemSettingsFromDatabaseAsync()', '系统设置 async 更新后应绕过旧共享缓存从数据库重读')
assertFunctionIncludes(settingsRepositorySource, 'clearSystemSettingsCache', 'clearSystemSettingsSharedCache()', '系统设置失效应清理 Redis 共享缓存命名空间')
assertFunctionIncludes(settingsRepositorySource, 'clearGlobalSettingsCache', 'clearGlobalSettingsSharedCache()', '全局设置失效应清理 Redis 共享缓存命名空间')

if (process.env.JUHE_SETTINGS_MANAGEMENT_DRIVER_CHILD === 'postgres') {
  const [repositories, settingsRepository] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../../storage/settings.repository.js')
  ])
  await assertSettingsManagementAsync(repositories, settingsRepository)
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-settings-management-driver-'))
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

  const [repositories, settingsRepository] = await Promise.all([
    import('../../storage/repositories.js'),
    import('../../storage/settings.repository.js')
  ])
  await assertSettingsManagementAsync(repositories, settingsRepository)

  if (process.env.JUHE_SETTINGS_MANAGEMENT_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_SETTINGS_MANAGEMENT_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_SETTINGS_MANAGEMENT_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_SETTINGS_MANAGEMENT_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_SETTINGS_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_SETTINGS_MANAGEMENT_REDIS_QUEUE_URL ?? process.env.JUHE_SETTINGS_MANAGEMENT_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('settings-management-driver-regression passed')
} finally {
  await closeSqliteStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSettingsManagementAsync(
  repositories: typeof import('../../storage/repositories.js'),
  settingsRepository: typeof import('../../storage/settings.repository.js')
): Promise<void> {
  const originalGlobal = await repositories.listGlobalSettingsAsync()
  const originalSystem = await repositories.getSettingsAsync()
  const label = process.env.JUHE_AI_DATABASE_DRIVER === 'postgres' ? 'postgres' : 'sqlite'
  const nextAppName = `聚合 AI ${label} 设置回归`
  const nextReadLimit = Number(originalSystem.systemApiRateLimitIpReadPerMinute) + 1

  try {
    const updatedGlobal = await repositories.updateGlobalSettingsAsync({ appName: nextAppName })
    assert.equal(updatedGlobal.appName, nextAppName, '全局设置 async 更新后应返回新应用名')
    assert.equal(updatedGlobal.appIcon, originalGlobal.appIcon, '全局设置 async 局部更新不应覆盖未提交字段')
    const publicSettings = await repositories.listPublicGlobalSettingsAsync()
    assert.equal(publicSettings.appName, nextAppName, '公开设置应读取到 async 更新后的全局应用名')

    const updatedSystem = await repositories.updateSettingsAsync({ systemApiRateLimitIpReadPerMinute: nextReadLimit })
    assert.equal(updatedSystem.systemApiRateLimitIpReadPerMinute, nextReadLimit, '系统设置 async 更新后应返回新限流值')
    assert.equal(Object.prototype.hasOwnProperty.call(updatedSystem, 'systemApiRateLimitEnabled'), false, '系统 API 限流开关不应暴露为系统设置')
    assert.equal(Object.prototype.hasOwnProperty.call(updatedSystem, 'streamCircuitBreakerEnabled'), false, '流式熔断开关不应暴露为系统设置')
    assert.equal(Object.prototype.hasOwnProperty.call(updatedSystem, 'operationLogEnabled'), false, '操作日志开关不应暴露为系统设置')
    const reloadedSystem = await repositories.getSettingsAsync()
    assert.equal(reloadedSystem.systemApiRateLimitIpReadPerMinute, nextReadLimit, '系统设置 async 更新后缓存应失效并可重新读取')

    await assert.rejects(
      () => repositories.updateSettingsAsync({ rogue_settings_key_0: true }),
      /未知系统设置字段/,
      '系统设置 async 更新不能接受未知字段'
    )
    await assert.rejects(
      () => repositories.updateSettingsAsync({ systemApiRateLimitEnabled: false }),
      /未知系统设置字段/,
      '系统设置 async 更新不能关闭系统 API 限流'
    )
    await assert.rejects(
      () => repositories.updateGlobalSettingsAsync({ legacyBrandName: '旧字段' }),
      /未知全局设置字段/,
      '全局设置 async 更新不能接受未知字段'
    )

    if (process.env.JUHE_AI_DATABASE_DRIVER === 'postgres') {
      await assert.rejects(
        () => repositories.updateSettingsAsync({ usageStatsTimezone: 'UTC' }),
        /PostgreSQL 模式下暂不支持在线修改统计时区/,
        'PostgreSQL 模式下不能在线修改统计时区'
      )
    }
  } finally {
    await repositories.updateGlobalSettingsAsync({
      appName: originalGlobal.appName,
      appIcon: originalGlobal.appIcon
    })
    await repositories.updateSettingsAsync({
      systemApiRateLimitIpReadPerMinute: originalSystem.systemApiRateLimitIpReadPerMinute
    })
  }
}

async function closeSqliteStorageDatabases(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}

function assertFunctionIncludes(source: string, functionName: string, pattern: string, message: string): void {
  assert(functionBody(source, functionName).includes(pattern), message)
}

function functionBody(source: string, functionName: string): string {
  const start = source.indexOf(`function ${functionName}`)
  assert(start >= 0, `缺少函数 ${functionName}`)
  let openBrace = -1
  let parenDepth = 0
  for (let index = start; index < source.length; index += 1) {
    const char = source[index]
    if (char === '(') parenDepth += 1
    if (char === ')') parenDepth = Math.max(0, parenDepth - 1)
    if (char === '{' && parenDepth === 0) {
      openBrace = index
      break
    }
  }
  assert(openBrace >= 0, `函数 ${functionName} 缺少函数体`)
  let depth = 0
  for (let index = openBrace; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') depth += 1
    if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(openBrace, index + 1)
      }
    }
  }
  throw new Error(`函数 ${functionName} 函数体未闭合`)
}
