import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_SETTINGS_PUBLIC_DRIVER_CHILD === 'postgres') {
  const repositories = await import('../../storage/repositories.js')
  await assertSettingsPublicAsync(repositories)
  process.exit(0)
}

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-settings-public-driver-'))
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
  await assertSettingsPublicAsync(repositories)

  if (process.env.JUHE_SETTINGS_PUBLIC_POSTGRES_URL) {
    const result = spawnSync(process.execPath, [
      '--import',
      'tsx',
      fileURLToPath(import.meta.url)
    ], {
      cwd: process.cwd(),
      encoding: 'utf8',
      env: {
        ...process.env,
        JUHE_SETTINGS_PUBLIC_DRIVER_CHILD: 'postgres',
        JUHE_AI_RUNTIME_MODE: 'performance',
        JUHE_AI_DATABASE_DRIVER: 'postgres',
        JUHE_AI_CACHE_DRIVER: 'redis',
        JUHE_AI_RUNTIME_STATE_DRIVER: 'redis',
        JUHE_AI_QUEUE_DRIVER: 'redis_stream',
        JUHE_AI_POSTGRES_URL: process.env.JUHE_SETTINGS_PUBLIC_POSTGRES_URL,
        JUHE_AI_REDIS_CACHE_URL: process.env.JUHE_SETTINGS_PUBLIC_REDIS_CACHE_URL ?? 'redis://:unused@127.0.0.1:6379/0',
        JUHE_AI_REDIS_STATE_URL: process.env.JUHE_SETTINGS_PUBLIC_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0',
        JUHE_AI_REDIS_QUEUE_URL: process.env.JUHE_SETTINGS_PUBLIC_REDIS_QUEUE_URL ?? process.env.JUHE_SETTINGS_PUBLIC_REDIS_STATE_URL ?? 'redis://:unused@127.0.0.1:6380/0'
      }
    })
    if (result.status !== 0) {
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      process.exit(result.status ?? 1)
    }
  }

  console.log('settings-public-driver-regression passed')
} finally {
  await closeSqliteStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function assertSettingsPublicAsync(repositories: typeof import('../../storage/repositories.js')): Promise<void> {
  const isPostgres = process.env.JUHE_AI_DATABASE_DRIVER === 'postgres'
  const publicSettings = await repositories.listPublicGlobalSettingsAsync()
  if (isPostgres) {
    assert.equal(typeof publicSettings.appName, 'string', 'PostgreSQL 公开设置应返回应用名字符串')
    assert.equal(typeof publicSettings.appIcon, 'string', 'PostgreSQL 公开设置应返回应用图标字符串')
  } else {
    assert.equal(publicSettings.appName, '聚合 AI', '公开设置应返回默认应用名')
    assert.equal(publicSettings.appIcon, '/__aisys__/brand-icon.svg', '公开设置应返回默认应用图标')
  }
  assert.deepEqual(Object.keys(publicSettings).sort(), ['appIcon', 'appName'], '公开设置只能暴露全局公开字段')

  const globalSettings = await repositories.listGlobalSettingsAsync()
  assert.equal(globalSettings.appName, publicSettings.appName, '全局设置 async 读取应包含公开字段')
  assert.equal(globalSettings.appIcon, publicSettings.appIcon, '全局设置 async 读取应包含公开图标')

  const systemSettings = await repositories.getSettingsAsync()
  if (isPostgres) {
    assert.equal(typeof systemSettings.systemApiRateLimitEnabled, 'boolean', 'PostgreSQL 系统 API 限流开关应可读取')
    assert.equal(typeof systemSettings.systemApiRateLimitIpReadPerMinute, 'number', 'PostgreSQL 系统 API IP 读限流应可读取')
    assert.equal(typeof systemSettings.systemApiRateLimitUserWritePerMinute, 'number', 'PostgreSQL 系统 API 用户写限流应可读取')
  } else {
    assert.equal(systemSettings.systemApiRateLimitEnabled, true, '系统 API 限流默认应启用')
    assert.equal(systemSettings.systemApiRateLimitIpReadPerMinute, 600, '系统 API IP 读限流默认值应可读取')
    assert.equal(systemSettings.systemApiRateLimitUserWritePerMinute, 120, '系统 API 用户写限流默认值应可读取')
  }
  assert.equal(Object.prototype.hasOwnProperty.call(systemSettings, 'rogue_settings_key_0'), false, '系统设置 async 读取不能暴露未知字段')
}

async function closeSqliteStorageDatabases(): Promise<void> {
  try {
    const databaseModule = await import('../../storage/database.js')
    databaseModule.closeStorageDatabases()
  } catch {
    // The regression may fail before SQLite storage is imported.
  }
}
