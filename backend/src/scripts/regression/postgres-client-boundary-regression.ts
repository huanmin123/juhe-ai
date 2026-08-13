import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import { postgresApplicationName, postgresPoolTimeoutConfig, postgresTransactionLocalSettingsSql } from '../../storage/postgres-client.js'

const postgresClientSource = readFileSync(fileURLToPath(new URL('../../storage/postgres-client.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(postgresClientSource, /\boptions\s*:/, '连接池不得发送 PgBouncer transaction pooling 不兼容的 startup options')

const originalProcessRole = runtimeConfig.processRole
const originalWorkerRole = runtimeConfig.workerRole
const originalJitEnabled = runtimeConfig.postgres.jitEnabled

try {
  runtimeConfig.processRole = 'server'
  runtimeConfig.workerRole = 'worker'
  assert.equal(postgresApplicationName(), 'juhe-ai:server', 'server 连接应带清晰 application_name')

  runtimeConfig.processRole = 'db-service'
  assert.equal(postgresApplicationName(), 'juhe-ai:db-service', 'DB service 连接应带清晰 application_name')

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'stats-worker'
  assert.equal(postgresApplicationName(), 'juhe-ai:worker:stats-worker', 'worker 连接应带具体 worker role')

  const timeoutConfig = postgresPoolTimeoutConfig()
  assert.equal(timeoutConfig.statement_timeout, runtimeConfig.postgres.statementTimeoutMs, 'PG statement_timeout 应来自运行配置')
  assert.equal(timeoutConfig.lock_timeout, runtimeConfig.postgres.lockTimeoutMs, 'PG lock_timeout 应来自运行配置')
  assert.equal(
    timeoutConfig.idle_in_transaction_session_timeout,
    runtimeConfig.postgres.idleInTransactionSessionTimeoutMs,
    'PG idle_in_transaction_session_timeout 应来自运行配置'
  )

  runtimeConfig.postgres.jitEnabled = false
  assert.match(postgresTransactionLocalSettingsSql(), /SET LOCAL jit = off/, '应用连接不得发送 PgBouncer 不兼容的启动参数，应在事务内关闭 JIT')

  runtimeConfig.postgres.jitEnabled = true
  assert.doesNotMatch(postgresTransactionLocalSettingsSql(), /SET LOCAL jit = off/, '显式启用 JIT 时事务不得覆盖 JIT')

  console.log('PostgreSQL 连接边界回归通过：application_name、事务超时和 PgBouncer 兼容的事务级 JIT 设置均由运行配置收口')
} finally {
  runtimeConfig.processRole = originalProcessRole
  runtimeConfig.workerRole = originalWorkerRole
  runtimeConfig.postgres.jitEnabled = originalJitEnabled
}
