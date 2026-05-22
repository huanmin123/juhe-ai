import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

if (process.env.JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD === '1') {
  const { runtimeConfig } = await import('../../config/runtime.js')

  assert.equal(runtimeConfig.port, 39123, '进程环境变量 JUHE_AI_PORT 应覆盖 backend/.env')
  assert.equal(runtimeConfig.host, '127.0.0.2', '进程环境变量 JUHE_AI_HOST 应覆盖 backend/.env')
  assert.equal(runtimeConfig.databasePath.endsWith('env-override-business.sqlite3'), true, '进程环境变量 JUHE_AI_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.datasetDatabasePath.endsWith('env-override-dataset.sqlite3'), true, '进程环境变量 JUHE_AI_DATASET_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.statsDatabasePath.endsWith('env-override-stats.sqlite3'), true, '进程环境变量 JUHE_AI_STATS_DATABASE_PATH 应覆盖 backend/.env')
  assert.equal(runtimeConfig.log.consoleEnabled, false, '进程环境变量 JUHE_AI_LOG_CONSOLE_ENABLED 应覆盖 backend/.env')

  process.exit(0)
}

const result = spawnSync(process.execPath, [
  '--import',
  'tsx',
  fileURLToPath(import.meta.url)
], {
  cwd: process.cwd(),
  env: {
    ...process.env,
    JUHE_AI_RUNTIME_CONFIG_ENV_OVERRIDE_CHILD: '1',
    JUHE_AI_PORT: '39123',
    JUHE_AI_HOST: '127.0.0.2',
    JUHE_AI_DATABASE_PATH: 'env-override-business.sqlite3',
    JUHE_AI_DATASET_DATABASE_PATH: 'env-override-dataset.sqlite3',
    JUHE_AI_STATS_DATABASE_PATH: 'env-override-stats.sqlite3',
    JUHE_AI_LOG_CONSOLE_ENABLED: 'false'
  },
  encoding: 'utf8'
})

if (result.status !== 0) {
  process.stdout.write(result.stdout)
  process.stderr.write(result.stderr)
  process.exit(result.status ?? 1)
}

console.log('运行配置环境变量覆盖回归通过：进程环境变量优先于 backend/.env')
