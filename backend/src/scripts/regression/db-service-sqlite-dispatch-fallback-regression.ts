import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-sqlite-dispatch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'db-service-sqlite-dispatch-fallback-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })

const [
  { logger },
  databaseModule,
  repositories,
  dbServiceHandlers
] = await Promise.all([
  import('../../shared/logger.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/db-service/db-service-handlers.js')
])
logger.level = 'silent'

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: 'DB service SQLite 回落分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: 'gpt',
    name: 'DB service SQLite 回落账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-db-service-sqlite-dispatch-fallback',
      base_url: 'http://127.0.0.1:65535/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  }, access)

  const settings = await dbServiceHandlers.handleDbServiceOperation({ type: 'read_gateway_settings' })
  assert(settings, 'SQLite DB service dispatch 应回落读取网关设置')

  const publicSettings = await dbServiceHandlers.handleDbServiceOperation({ type: 'list_public_global_settings' })
  assert(publicSettings && typeof publicSettings === 'object', 'SQLite DB service dispatch 应回落读取公开设置')

  const groupAccess = await dbServiceHandlers.handleDbServiceOperation({
    type: 'resolve_group_usage_access',
    groupId: group.id,
    systemAccountId: 'sys_admin'
  })
  assert(groupAccess, 'SQLite DB service dispatch 应回落读取分组授权元数据')

  const accountList = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions'
  })
  assert.equal(accountList.length, 1, 'SQLite DB service dispatch 应回落读取分组账号列表')

  const accountResult = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_openai_accounts_for_group_result',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions'
  })
  assert.equal(accountResult.accounts.length, 1, 'SQLite DB service dispatch 应回落读取带诊断的分组账号列表')
  assert(accountResult.diagnostics, '带诊断账号列表应保留 diagnostics')

  const recoverable = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_recoverable_unavailable_openai_accounts_for_group',
    groupId: group.id,
    systemAccountId: 'sys_admin',
    requestedModel: 'gpt-5.5',
    requestedEndpointFamily: 'chat_completions',
    windowMs: 1000
  })
  assert(Array.isArray(recoverable), 'SQLite DB service dispatch 应回落读取可恢复不可用账号列表')

  const modelCatalog = await dbServiceHandlers.handleDbServiceOperation({
    type: 'list_provider_model_catalog',
    providerCode: 'gpt',
    systemAccountId: 'sys_admin',
    includeInactive: true,
    includeUnpriced: true
  })
  assert(Array.isArray(modelCatalog), 'SQLite DB service dispatch 应回落读取供应商模型目录')

  console.log('DB service SQLite dispatch 回落回归通过：SQLite 下 server/db-service 读操作不会返回 undefined')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
