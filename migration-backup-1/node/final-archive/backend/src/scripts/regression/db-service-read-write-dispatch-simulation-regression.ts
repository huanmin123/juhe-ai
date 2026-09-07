import assert from 'node:assert/strict'

import {
  dbServiceOperationAccessMode,
  shouldQueueDbServiceOperationForDriver
} from '../../modules/db-service/db-service-operation-access-mode.js'
import { dbServiceOperationPriority } from '../../modules/db-service/db-service-request-priority.js'
import type { DbServiceOperation } from '../../modules/db-service/db-service-types.js'

const foregroundRead: DbServiceOperation = { type: 'validate_gateway_api_key', key: 'sk-simulated' }
const foregroundRuntime = { type: 'status' } as DbServiceOperation
const codexContextRead = {
  type: 'read_codex_context_response_chain',
  responseId: 'resp_simulated',
  boundary: {
    systemAccountId: 'sys_admin',
    apiKeyId: 'key_simulated',
    groupId: 'group_simulated',
    providerCode: 'gpt'
  },
  maxDepth: 4
} as DbServiceOperation
const backgroundWrite: DbServiceOperation = {
  type: 'project_account_health_jobs_outcome',
  outcome: {}
}
const backgroundMaintenance: DbServiceOperation = {
  type: 'cleanup_expired_system_sessions',
  expiredBefore: new Date(0).toISOString(),
  limit: 100
}

assert.equal(dbServiceOperationAccessMode(foregroundRead), 'read', '模拟前台读必须分类为 read')
assert.equal(dbServiceOperationAccessMode(foregroundRuntime), 'runtime', '模拟 runtime 请求必须分类为 runtime')
assert.equal(dbServiceOperationAccessMode(codexContextRead), 'read', '带异步续期的 Codex context 读取必须分类为 read')
assert.equal(dbServiceOperationAccessMode(backgroundWrite), 'write', '模拟后台状态写必须分类为 write')
assert.equal(dbServiceOperationAccessMode(backgroundMaintenance), 'maintenance', '模拟后台维护任务必须分类为 maintenance')

assert.equal(
  shouldQueueDbServiceOperationForDriver(foregroundRead, 'sqlite'),
  false,
  'SQLite 单机模式下前台纯读不能进入写队列'
)
assert.equal(
  shouldQueueDbServiceOperationForDriver(foregroundRuntime, 'sqlite'),
  false,
  'SQLite 单机模式下 runtime 请求不能进入写队列'
)
assert.equal(
  shouldQueueDbServiceOperationForDriver(codexContextRead, 'sqlite'),
  false,
  'SQLite 单机模式下带异步续期的 Codex context 读取不能进入写队列'
)
assert.equal(
  shouldQueueDbServiceOperationForDriver(backgroundWrite, 'sqlite'),
  true,
  'SQLite 单机模式下写请求必须进入受控写队列'
)
assert.equal(
  shouldQueueDbServiceOperationForDriver(backgroundMaintenance, 'sqlite'),
  true,
  'SQLite 单机模式下维护任务必须进入受控写队列'
)

for (const operation of [foregroundRead, foregroundRuntime, codexContextRead, backgroundWrite, backgroundMaintenance]) {
  assert.equal(
    shouldQueueDbServiceOperationForDriver(operation, 'postgres'),
    false,
    `PostgreSQL 模式不能对 ${operation.type} 套 SQLite 式全局写队列`
  )
}

assert.notEqual(dbServiceOperationPriority(foregroundRead), 'low', '前台纯读不能被降成 low priority')
assert.equal(dbServiceOperationPriority(backgroundMaintenance), 'low', '后台维护任务必须保持 low priority')

console.log('DB service 读写调度模拟回归通过：SQLite 只排写/维护，读/runtime 直派发；PostgreSQL 不套 SQLite 全局队列')
