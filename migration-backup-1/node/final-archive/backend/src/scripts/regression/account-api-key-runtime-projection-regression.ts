import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-api-key-runtime-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-api-key-runtime-projection-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, runtimeRepository, apiKeyPoolRuntime] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-runtime.repository.js'),
  import('../../modules/accounts/account-api-key-pool-runtime.js')
])

try {
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const owner = repositories.createSystemAccount({
    username: 'api_key_runtime_owner',
    displayName: 'APIKey运行态所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'api_key_runtime_grantee',
    displayName: 'APIKey运行态被授权者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const outsider = repositories.createSystemAccount({
    username: 'api_key_runtime_outsider',
    displayName: 'APIKey运行态其他用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const outsiderAccess = { systemAccountId: outsider.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: 'API Key 运行态来源分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: 'API Key 运行态授权目标分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  const account = await repositories.createAccountAsync({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'API Key 运行态窄投影账户',
    type: 'api_key',
    credentials: {
      base_url: 'https://api.openai.com/v1',
      api_key: 'sk-runtime-primary',
      api_keys: ['sk-runtime-primary', 'sk-runtime-secondary']
    },
    supportedModels: ['gpt-5.4-mini'],
    healthCheckModel: 'gpt-5.4-mini',
    healthCheckEndpointMode: 'responses_sse',
    groupId: ownerGroup.id,
    status: 'active',
    skipInitialHealthCheck: true
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: 'API Key 运行态授权实例边界回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((item) => item.authorizationInstanceSourceAccountId === account.id)
  assert(authorizedInstance, '账户授权应创建被授权者作用域内的实例账户')

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedSql: string[] = []
  const capturedParams: SQLInputValue[][] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    statement.get = ((...params: SQLInputValue[]) => {
      capturedSql.push(sql)
      capturedParams.push(params)
      return originalGet(...params)
    }) as typeof statement.get
    return statement
  }) as typeof database.prepare

  let ownerProjection: Awaited<ReturnType<typeof runtimeRepository.findAccountApiKeyRuntimeAccountAsync>>
  try {
    ownerProjection = await runtimeRepository.findAccountApiKeyRuntimeAccountAsync(account.id, ownerAccess)
  } finally {
    database.prepare = originalPrepare
  }
  assert.deepEqual(ownerProjection, {
    id: account.id,
    configRevision: account.configRevision ?? 1,
    accessType: 'owner'
  })
  assert.equal(capturedSql.length, 1, 'API Key 运行态账户投影应只执行一条主表查询')
  assert.deepEqual(capturedParams, [[account.id, owner.id]], '普通用户查询必须把 owner scope 下推到 SQL')
  assert.doesNotMatch(capturedSql[0], /SELECT\s+\*/i)
  assert.match(capturedSql[0], /accounts\.id[\s\S]*accounts\.config_revision[\s\S]*accounts\.system_account_id/i)
  assert.match(capturedSql[0], /accounts\.system_account_id\s*=\s*\?/i)
  assert.doesNotMatch(capturedSql[0], /credentials|usage|quality|runtime_details|permissions|authorization_sources/i)

  assert.deepEqual(
    await runtimeRepository.findAccountApiKeyRuntimeAccountAsync(authorizedInstance.id, granteeAccess),
    {
      id: authorizedInstance.id,
      configRevision: authorizedInstance.configRevision ?? 1,
      accessType: 'authorized'
    },
    '当前 owner 作用域内的授权实例必须可被识别，以便 route 返回 403'
  )
  assert.equal(
    await runtimeRepository.findAccountApiKeyRuntimeAccountAsync(account.id, granteeAccess),
    undefined,
    '被授权者不得绕过实例 ID 读取来源账户，route 应映射为 404'
  )
  assert.equal(
    await runtimeRepository.findAccountApiKeyRuntimeAccountAsync(account.id, outsiderAccess),
    undefined,
    '跨 owner 账户必须返回 undefined，避免泄露账户存在性'
  )
  assert.equal(
    await runtimeRepository.findAccountApiKeyRuntimeAccountAsync(account.id, {
      ...adminAccess,
      systemAccountFilterId: grantee.id
    }),
    undefined,
    '管理员显式 owner filter 必须参与账户作用域过滤'
  )

  assert(ownerProjection)
  const runtimeResponse = await apiKeyPoolRuntime.loadOwnerAccountApiKeyRuntimeResponse(ownerProjection)
  assert.equal(runtimeResponse?.accountId, account.id)
  assert.equal(runtimeResponse?.configRevision, account.configRevision ?? 1)
  assert.equal(
    await apiKeyPoolRuntime.loadOwnerAccountApiKeyRuntimeResponse({
      id: authorizedInstance.id,
      configRevision: authorizedInstance.configRevision,
      accessType: 'authorized'
    }),
    undefined,
    '运行态加载器必须拒绝授权实例投影'
  )

  const runtimeSource = readFileSync(resolve('src/modules/accounts/account-api-key-pool-runtime.ts'), 'utf8')
  assert.doesNotMatch(runtimeSource, /\bAccountSummary\b/, 'API Key 运行态加载器不得重新依赖宽 AccountSummary')

  console.log('AI 账户 API Key 运行态窄投影回归通过：owner 200、授权实例 403、跨 owner 404 的数据契约可区分')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
