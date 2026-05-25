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
  assert.equal(runtimeConfig.statsDatabasePath.endsWith('env-override-stats.sqlite3'), true, '进程环境变量 JUHE_AI_STATS_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.usageShardRoot.endsWith('env-override-usage-shards'), true, '进程环境变量 JUHE_AI_USAGE_SHARD_ROOT 应覆盖 backend/.env')
  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('env-override-usage-shards'), true, '显式 usage shard 根目录不应被数据集目录库默认规则覆盖')
  assert.equal(runtimeConfig.usageShardCount, 32, '进程环境变量 JUHE_AI_USAGE_SHARD_COUNT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.log.consoleEnabled, false, '进程环境变量 JUHE_AI_LOG_CONSOLE_ENABLED 应覆盖 backend/.env')

  process.exit(0)
}

if (process.env.JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD === '1') {
  const { usageRecordShardRoot } = await import('../../storage/usage-record-shards.js')

  assert.equal(normalizePath(usageRecordShardRoot()).endsWith('runtime-config-dataset-dir/usage-shards'), true, '未配置 usage shard 根目录时应跟随数据集目录库生成同级 usage-shards')

  process.exit(0)
}

const result = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
  JUHE_AI_PORT: '39123',
  JUHE_AI_HOST: '127.0.0.2',
  JUHE_AI_DATABASE_PATH: 'env-override-business.sqlite3',
  JUHE_AI_DATASET_DATABASE_PATH: 'env-override-dataset.sqlite3',
  JUHE_AI_STATS_DATABASE_PATH: 'env-override-stats.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: 'env-override-usage-shards',
  JUHE_AI_USAGE_SHARD_COUNT: '32',
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false'
})

assertRegressionSuccess(result)

const defaultUsageRootResult = spawnRegression({
  JUHE_AI_RUNTIME_CONFIG_USAGE_SHARD_DEFAULT_CHILD: '1',
  JUHE_AI_DATASET_DATABASE_PATH: 'runtime-config-dataset-dir/dataset.sqlite3',
  JUHE_AI_USAGE_SHARD_ROOT: ''
})

assertRegressionSuccess(defaultUsageRootResult)

console.log('运行配置环境变量覆盖回归通过：进程环境变量优先于 backend/.env，usage shard 默认根目录跟随数据集目录库')

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
