import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-multi-group-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-multi-group-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, dbServiceHandlers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const primaryEmptyGroup = repositories.createGroup({
    name: '多分组回归 A 空池',
    providerCode: 'openai',
    enabled: true
  }, access)
  const fallbackGroup = repositories.createGroup({
    name: '多分组回归 B 后备池',
    providerCode: 'openai',
    enabled: true
  }, access)
  const fallbackAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '多分组回归后备账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-api-key-multi-group-fallback',
      base_url: 'http://127.0.0.1:9/v1'
    },
    groupId: fallbackGroup.id,
    status: 'active',
    schedulable: true
  }, access)

  const apiKey = repositories.createApiKeyRecord({
    name: '多分组路由回归 Key',
    groupBindings: [
      { groupId: primaryEmptyGroup.id, priority: 1, status: 'active' },
      { groupId: fallbackGroup.id, priority: 2, status: 'active' }
    ]
  }, access)

  const created = repositories.findApiKeySummary(apiKey.id, access)
  assert.equal(created?.groupId, primaryEmptyGroup.id, '兼容主分组应等于最高优先级启用分组')
  assert.deepEqual(
    created?.groupBindings.map((binding) => [binding.groupId, binding.priority, binding.status]),
    [
      [primaryEmptyGroup.id, 1, 'active'],
      [fallbackGroup.id, 2, 'active']
    ],
    '详情应返回完整分组路由'
  )

  const filteredByFallback = repositories.listApiKeysPage(access, {
    groupId: fallbackGroup.id,
    page: 1,
    pageSize: 20
  })
  assert(filteredByFallback.items.some((item) => item.id === apiKey.id), '按后备分组筛选也应命中 API Key')

  const runtime = await dbServiceHandlers.handleDbServiceOperation({
    type: 'read_gateway_runtime',
    key: apiKey.key
  })
  assert.equal(runtime.apiKey?.id, apiKey.id, '运行时应识别多分组 API Key')
  assert.equal(runtime.apiKey?.group_id, fallbackGroup.id, '优先分组无账号时运行时应切到后备分组')
  assert.equal(runtime.accounts.length, 1, '运行时应返回后备分组账号')
  assert.equal(runtime.accounts[0]?.id, fallbackAccount.id, '运行时账号应来自后备分组')

  const updated = repositories.updateApiKey(apiKey.id, {
    groupBindings: [
      { groupId: fallbackGroup.id, priority: 1, status: 'active' },
      { groupId: primaryEmptyGroup.id, priority: 2, status: 'disabled' }
    ]
  }, access)
  assert.equal(updated?.groupId, fallbackGroup.id, '更新优先级后兼容主分组应同步为新的最高优先级启用分组')
  assert.deepEqual(
    updated?.groupBindings.map((binding) => [binding.groupId, binding.priority, binding.status]),
    [
      [fallbackGroup.id, 1, 'active'],
      [primaryEmptyGroup.id, 2, 'disabled']
    ],
    '更新后应保留启停状态和优先级顺序'
  )

  assert.throws(() => {
    repositories.updateApiKey(apiKey.id, {
      groupBindings: [
        { groupId: fallbackGroup.id, priority: 1, status: 'active' },
        { groupId: fallbackGroup.id, priority: 2, status: 'active' }
      ]
    }, access)
  }, /不能重复/, '重复绑定同一分组应被拒绝')

  console.log('API Key 多分组绑定回归通过：创建、筛选、优先级更新和空池后备切换正常')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
