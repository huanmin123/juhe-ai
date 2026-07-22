import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { postgresApplicationName, postgresPoolTimeoutConfig } from '../../storage/postgres-client.js'

const originalProcessRole = runtimeConfig.processRole
const originalWorkerRole = runtimeConfig.workerRole

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

  console.log('PostgreSQL 连接边界回归通过：application_name 区分进程角色，超时参数由运行配置收口')
} finally {
  runtimeConfig.processRole = originalProcessRole
  runtimeConfig.workerRole = originalWorkerRole
}
