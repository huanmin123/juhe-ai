import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(runtimeConfig.port, 39123, '进程环境变量 JUHE_AI_PORT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.host, '127.0.0.2', '进程环境变量 JUHE_AI_HOST 应覆盖 backend/.env')
  assert.equal(runtimeConfig.databasePath.endsWith('env-override-business.sqlite3'), true, '进程环境变量 JUHE_AI_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.datasetDatabasePath.endsWith('env-override-dataset.sqlite3'), true, '进程环境变量 JUHE_AI_DATASET_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.usageCatalogDatabasePath.endsWith('env-override-usage-catalog.sqlite3'), true, '进程环境变量 JUHE_AI_USAGE_CATALOG_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.statsDatabasePath.endsWith('env-override-stats.sqlite3'), true, '进程环境变量 JUHE_AI_STATS_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.usageShardRoot.endsWith('env-override-usage-shards'), true, '进程环境变量 JUHE_AI_USAGE_SHARD_ROOT 应覆盖 backend/.env')
  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('env-override-usage-shards'), true, '显式 usage shard 根目录不应被数据集目录库默认规则覆盖')
  assert.equal(runtimeConfig.usageShardCount, 32, '进程环境变量 JUHE_AI_USAGE_SHARD_COUNT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.log.consoleEnabled, false, '进程环境变量 JUHE_AI_LOG_CONSOLE_ENABLED 应覆盖 backend/.env')
  assert.equal(runtimeConfig.runtimeMode, 'standalone', '默认运行模式应为 standalone')
  assert.equal(runtimeConfig.databaseDriver, 'sqlite', 'standalone 默认数据库 driver 应为 sqlite')
  assert.equal(runtimeConfig.cacheDriver, 'memory', 'standalone 默认缓存 driver 应为 memory')
  assert.equal(runtimeConfig.runtimeStateDriver, 'memory', 'standalone 默认运行态 driver 应为 memory')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD === '1') {
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-usage-catalog-dir/usage-shards'), true, '未配置 usage shard 根目录时应跟随使用记录目录库生成同级 usage-shards')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.runtimeMode, 'performance', '高性能模式应读取为 performance')
  assert.equal(runtimeConfig.databaseDriver, 'postgres', '高性能模式数据库 driver 应为 postgres')
  assert.equal(runtimeConfig.cacheDriver, 'redis', '高性能模式缓存 driver 应为 redis')
  assert.equal(runtimeConfig.runtimeStateDriver, 'redis', '高性能模式运行态 driver 应为 redis')
  assert.equal(runtimeConfig.postgres.url, 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai', 'PostgreSQL URL 应正确读取')
  assert.equal(runtimeConfig.redis.cacheUrl, 'redis://:cache-secret@127.0.0.1:6379/0', 'Redis cache URL 应正确读取')
  assert.equal(runtimeConfig.redis.stateUrl, 'redis://:state-secret@127.0.0.1:6380/0', 'Redis state URL 应正确读取')
  assert.equal(runtimeConfig.postgres.poolMax, 25, 'PostgreSQL pool max 应正确读取')
  assert.equal(runtimeConfig.postgres.writeMaxConcurrency, 100, 'PostgreSQL 写队列并发应正确读取')
  assert.equal(runtimeConfig.postgres.writeQueueMaxItems, 60000, 'PostgreSQL 写队列容量应正确读取')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_CATALOG_DERIVED_CHILD === '1') {
  const { usageCatalogDatabasePath } = await import('../../storage/database.js')
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageCatalogDatabasePath()).endsWith('runtime-config-derived-dataset/usage-catalog.sqlite3'), true, '未显式配置使用记录目录库时应跟随数据集目录库所在目录，避免回归脚本误写默认 data 目录')
  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-derived-dataset/usage-shards'), true, '未显式配置 usage shard 根目录时应跟随推导后的使用记录目录库')

  process.exit(0)
}

const result = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_PORT: '39123',
  JUHE_AI_HOST: '127.0.0.2',
  JUHE_AI_DATABASE_PATH: 'env-override-business.sqlite3',
  JUHE_AI_DATASET_DATABASE_PATH: 'env-override-dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: 'env-override-usage-catalog.sqlite3',
  JUHE_AI_STATS_DATABASE_PATH: 'env-override-stats.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: 'env-override-usage-shards',
  JUHE_AI_USAGE_SHARD_COUNT: '32',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false'
})

assertRegressionSuccess(result)

const defaultUsageRootResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD: '1',
  JUHE_AI_DATASET_DATABASE_PATH: 'runtime-config-dataset-dir/dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: 'runtime-config-usage-catalog-dir/usage-catalog.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: ''
})

assertRegressionSuccess(defaultUsageRootResult)

const derivedUsageCatalogResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_USAGE_CATALOG_DERIVED_CHILD: '1',
  JUHE_AI_DATASET_DATABASE_PATH: 'runtime-config-derived-dataset/dataset.sqlite3',
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: '',
  JUHE_AI_USAGE_SHARD_ROOT: ''
})

assertRegressionSuccess(derivedUsageCatalogResult)

const performanceResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_PERFORMANCE_CHILD: '1',
  JUHE_AI_RUNTIME_MODE: 'performance',
  JUHE_AI_POSTGRES_URL: 'postgres://juhe_ai:secret@127.0.0.1:5432/juhe_ai',
  JUHE_AI_REDIS_CACHE_URL: 'redis://:cache-secret@127.0.0.1:6379/0',
  JUHE_AI_REDIS_STATE_URL: 'redis://:state-secret@127.0.0.1:6380/0',
  JUHE_AI_DB_POOL_MAX: '25',
  JUHE_AI_DB_WRITE_MAX_CONCURRENCY: '100',
  JUHE_AI_DB_WRITE_QUEUE_MAX_ITEMS: '60000'
})

assertRegressionSuccess(performanceResult)

console.log('运行配置环境变量覆盖回归通过：进程环境变量优先于 backend/.env，usage shard 默认根目录跟随使用记录目录库，高性能模式配置可读取')

function spawnRegression(env: Record<string, string>): ReturnType<typeof spawnSync> {
  return spawnSync(process.execPath, [
    '--import',
    'tsx',
    fileURLToPath(import.meta.url)
  ], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      ...env
    },
    encoding: 'utf8'
  })
}

function assertRegressionSuccess(result: ReturnType<typeof spawnSync>): void {
  if (result.status === 0) {
    return
  }
  process.stdout.write(result.stdout)
  process.stderr.write(result.stderr)
  process.exit(result.status ?? 1)
}

function normalizePath(path: string): string {
  return path.replace(/\\/g, '/')
}
