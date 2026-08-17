import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-sqlite-dispatch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'db-service-sqlite-dispatch-standalone-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const [
  { logger },
  databaseModule,
  repositories,
  dbServiceHandlers,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../shared/logger.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])
logger.level = 'silent'

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'DB service SQLite standalone 分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'DB service SQLite standalone 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-db-service-sqlite-dispatch-standalone',
      base_url: 'http://127.0.0.1:65535/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    healthCheckModel: 'gpt-5.5'
  }, access)
  assert(repositories.projectAccountHealthFixtureSuccess(account.id, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), 'SQLite standalone DB service dispatch 种子账号后台检查应成功')

  const settings = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_settings' })
  assert(settings, 'SQLite standalone DB service dispatch 应读取网关设置')

  const publicSettings = await dbServiceHandlers.handleDbServiceOperation({ type: 'list_public_global_settings' })
  assert(publicSettings && typeof publicSettings === 'object', 'SQLite standalone DB service dispatch 应读取公开设置')

  const groupAccess = await dbServiceHandlers.handleDbServiceOperation({
    type: 'resolve_group_usage_access',
    groupId: group.id,
    systemAccountId: 'sys_admin'
  })
  assert(groupAccess, 'SQLite standalone DB service dispatch 应读取分组授权元数据')

  const accountList = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions'
  })
  assert.equal(accountList.length, 1, 'SQLite standalone DB service dispatch 应读取分组账号列表')

  const accountResult = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group_result',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions'
  })
  assert.equal(accountResult.accounts.length, 1, 'SQLite standalone DB service dispatch 应读取带诊断的分组账号列表')
  assert(accountResult.diagnostics, '带诊断账号列表应保留 diagnostics')

  const recoverable = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_recoverable_unavailable_openai_accounts_for_group',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions',
    windowMs: 1000
  })
  assert(Array.isArray(recoverable), 'SQLite standalone DB service dispatch 应读取可恢复不可用账号列表')

  const modelCatalog = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_provider_model_catalog',
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeInactive: true,
    includeUnpriced: true
  })
  assert(Array.isArray(modelCatalog), 'SQLite standalone DB service dispatch 应读取供应商模型目录')

  console.log('DB service SQLite standalone dispatch 回归通过：SQLite 下 server/db-service 读操作不会返回 undefined')
} finally {
  try {
    await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
