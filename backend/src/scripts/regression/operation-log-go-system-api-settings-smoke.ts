import { strict as assert } from 'node:assert'
import { mkdtempSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

const inputURL = requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_URL')
requiredEnv('JUHE_AI_OPERATION_LOG_INPUT_SECRET')
const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-f4-system-api-producer-'))

process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')
process.env.JUHE_AI_SECRET = 'f4-system-api-settings-smoke-secret'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '4'
process.env.NODE_ENV = 'test'

let server: http.Server | undefined
let closeStorageDatabases: (() => void) | undefined
let closeSqliteReadWorkerPool: (() => Promise<void>) | undefined

try {
  const [
    { createSystemApiApp },
    { captchaAnswerForTest },
    { logger },
    databaseModule,
    readWorkerPool
  ] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../modules/auth/captcha.service.js'),
    import('../../shared/logger.js'),
    import('../../storage/database.js'),
    import('../../storage/sqlite-read-worker-pool.js')
  ])
  logger.level = 'silent'
  closeStorageDatabases = databaseModule.closeStorageDatabases
  closeSqliteReadWorkerPool = readWorkerPool.closeSqliteReadWorkerPool
  databaseModule.getBusinessDatabase()
  databaseModule.getDatasetDatabase()
  databaseModule.getUsageCatalogDatabase()
  databaseModule.getStatsDatabase()

  const app = createSystemApiApp({
    systemApiPrefix: '/__aisys__/api',
    trustProxy: true,
    bypassSystemApiRateLimitForTest: true
  })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseURL = `http://127.0.0.1:${addressPort(server)}`
  const cookie = await login(baseURL, captchaAnswerForTest)
  const adminProfile = await request(baseURL, '/__aisys__/api/auth/me', cookie)
  assert.equal(adminProfile.status, 200, `管理员身份读取应成功：${adminProfile.text}`)
  const adminSystemAccountID = envelope<{ id: string }>(adminProfile.text).id

  const patch = await request(baseURL, '/__aisys__/api/settings/global', cookie, {
    method: 'PATCH',
    body: { appName: 'F4 System API settings smoke' }
  })
  assert.equal(patch.status, 200, `真实 settings/global 业务写入应成功：${patch.text}`)

  const announcement = await request(baseURL, '/__aisys__/api/announcements', cookie, {
    method: 'POST',
    body: {
      title: 'F4 System API announcement smoke',
      content: 'F4 System API announcement smoke content',
      level: 'info',
      status: 'draft'
    }
  })
  assert.equal(announcement.status, 201, `真实 announcements 业务写入应成功：${announcement.text}`)

  const responsePolicy = await request(baseURL, '/__aisys__/api/response-inspection-policies', cookie, {
    method: 'POST',
    body: {
      name: 'F4 System API response policy smoke',
      enabled: true,
      priority: 10,
      scopeType: 'protocol',
      protocolCode: 'openai',
      match: { outputTextIncludes: ['F4-smoke'] },
      action: 'observe',
      notes: 'F4 System API response policy smoke'
    }
  })
  assert.equal(responsePolicy.status, 201, `真实 response-inspection-policies 业务写入应成功：${responsePolicy.text}`)

  const externalIntegrationSource = await request(baseURL, '/__aisys__/api/external-integration-sources', cookie, {
    method: 'POST',
    body: {
      name: 'F4 System API external source smoke',
      status: 'active',
      scopes: [
        'juhe_ai_public:account_list:read',
        'juhe_ai_public:account_add:write'
      ]
    }
  })
  assert.equal(externalIntegrationSource.status, 201, `真实 external-integration-sources 业务写入应成功：${externalIntegrationSource.text}`)
  const externalSourceToken = envelope<{ token: { token: string } }>(externalIntegrationSource.text).token.token

  const externalAccount = await request(baseURL, '/__aipublic__/account/add', undefined, {
    method: 'POST',
    headers: { authorization: `Bearer ${externalSourceToken}` },
    body: {
      targetUsername: 'f4_external_target',
      targetDisplayName: 'F4ExternalTarget',
      targetGroupName: 'F4 external target group smoke',
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 external integration account smoke',
      type: 'api_key',
      baseUrl: 'https://api.openai.com/v1',
      apiKey: 'sk-f4-external-integration-smoke',
      supportedModels: ['gpt-5.5'],
      status: 'active'
    }
  })
  assert.equal(externalAccount.status, 201, `真实 external-integrations 业务写入应成功：${externalAccount.text}`)

  const providerModels = await request(baseURL, '/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true', cookie)
  assert.equal(providerModels.status, 200, `真实 providers 模型列表读取应成功：${providerModels.text}`)
  const builtInProviderModel = envelope<Array<{ id?: string; model: string; scope: string; updatedAt?: string; catalogVisible?: boolean }>>(providerModels.text)
    .find((candidate) => candidate.scope === 'built_in' && candidate.id && candidate.updatedAt)
  assert(builtInProviderModel?.id && builtInProviderModel.updatedAt, '测试初始化必须提供可修改的内置 gpt 模型')
  const patchProviderModel = await request(baseURL, `/__aisys__/api/providers/gpt/models/${encodeURIComponent(builtInProviderModel.id)}`, cookie, {
    method: 'PATCH',
    body: {
      catalogVisible: !Boolean(builtInProviderModel.catalogVisible),
      expectedUpdatedAt: builtInProviderModel.updatedAt
    }
  })
  assert.equal(patchProviderModel.status, 200, `真实 providers 内置模型更新应成功：${patchProviderModel.text}`)

  const memberUsername = 'f4_smoke_member'
  const memberPassword = 'f4-smoke-member-password'
  const systemAccount = await request(baseURL, '/__aisys__/api/system-accounts', cookie, {
    method: 'POST',
    body: {
      username: memberUsername,
      displayName: 'F4SystemAPIMemberSmoke',
      password: memberPassword,
      role: 'user',
      status: 'active',
      mustChangePassword: false
    }
  })
  assert.equal(systemAccount.status, 201, `真实 system-accounts 业务写入应成功：${systemAccount.text}`)
  const systemAccountID = envelope<{ id: string }>(systemAccount.text).id
  const memberCookie = await login(baseURL, captchaAnswerForTest, memberUsername, memberPassword)
  const memberGroup = await request(baseURL, '/__aisys__/api/my-groups', memberCookie, {
    method: 'POST',
    body: { name: 'F4 System API member target group smoke', providerCode: 'gpt', enabled: true }
  })
  assert.equal(memberGroup.status, 201, `真实普通用户分组写入应成功：${memberGroup.text}`)
  const memberGroupID = envelope<{ id: string }>(memberGroup.text).id

  const systemTeam = await request(baseURL, '/__aisys__/api/system-teams', cookie, {
    method: 'POST',
    body: { name: 'F4 System API team smoke', description: 'F4 team smoke', status: 'active' }
  })
  assert.equal(systemTeam.status, 201, `真实 system-teams 业务写入应成功：${systemTeam.text}`)
  const team = envelope<{ id: string; updatedAt: string }>(systemTeam.text)
  const addTeamMember = await request(baseURL, `/__aisys__/api/system-teams/${encodeURIComponent(team.id)}/members`, cookie, {
    method: 'POST',
    body: { systemAccountIds: [systemAccountID], expectedUpdatedAt: team.updatedAt }
  })
  assert.equal(addTeamMember.status, 200, `真实 system-teams 成员写入应成功：${addTeamMember.text}`)

  const group = await request(baseURL, '/__aisys__/api/groups', cookie, {
    method: 'POST',
    body: { name: 'F4 System API group smoke', providerCode: 'gpt', enabled: true }
  })
  assert.equal(group.status, 201, `真实 groups 业务写入应成功：${group.text}`)
  const groupID = envelope<{ id: string }>(group.text).id

  const account = await request(baseURL, '/__aisys__/api/accounts', cookie, {
    method: 'POST',
    body: {
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 System API account smoke',
      type: 'api_key',
      credentials: {
        api_key: 'sk-f4-system-api-smoke',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      healthCheckModel: 'gpt-5.4-mini',
      status: 'disabled',
      groupId: groupID
    }
  })
  assert.equal(account.status, 201, `真实 accounts 业务写入应成功：${account.text}`)
  const accountID = envelope<{ id: string }>(account.text).id

  const batchTargetAccount = await request(baseURL, '/__aisys__/api/accounts', cookie, {
    method: 'POST',
    body: {
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 System API batch target smoke',
      type: 'api_key',
      credentials: {
        api_key: 'sk-f4-system-api-batch-target',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      healthCheckModel: 'gpt-5.4-mini',
      status: 'disabled',
      groupId: groupID
    }
  })
  assert.equal(batchTargetAccount.status, 201, `真实 accounts 批量目标创建应成功：${batchTargetAccount.text}`)
  const batchTargetAccountID = envelope<{ id: string }>(batchTargetAccount.text).id

  const pendingAccount = await request(baseURL, '/__aisys__/api/accounts', cookie, {
    method: 'POST',
    body: {
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 System API pending account smoke',
      type: 'api_key',
      credentials: {
        api_key: 'sk-f4-system-api-pending',
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      healthCheckModel: 'gpt-5.4-mini',
      groupId: groupID
    }
  })
  assert.equal(pendingAccount.status, 201, `真实 accounts 待检查账户创建应成功：${pendingAccount.text}`)
  const pendingAccountID = envelope<{ id: string }>(pendingAccount.text).id
  const forceActivatePendingAccount = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(pendingAccountID)}/force-activate`, cookie, {
    method: 'POST',
    body: { acknowledgedAccountAvailable: true }
  })
  assert.equal(forceActivatePendingAccount.status, 200, `真实 accounts 人工恢复应成功：${forceActivatePendingAccount.text}`)

  const trafficMigrationTarget = await request(baseURL, '/__aisys__/api/accounts', cookie, {
    method: 'POST',
    body: {
      providerCode: 'gpt',
      providerProtocolProfileId: 'profile_gpt_openai_v1',
      name: 'F4 System API traffic target smoke',
      type: 'api_key',
      credentials: {
        api_keys: ['sk-f4-system-api-traffic-a', 'sk-f4-system-api-traffic-b'],
        base_url: 'https://api.openai.com/v1'
      },
      supportedModels: ['gpt-5.4-mini'],
      healthCheckModel: 'gpt-5.4-mini',
      status: 'active',
      groupId: groupID
    }
  })
  assert.equal(trafficMigrationTarget.status, 201, `真实 accounts 流量目标创建应成功：${trafficMigrationTarget.text}`)
  const trafficMigrationTargetID = envelope<{ id: string }>(trafficMigrationTarget.text).id
  const trafficMigration = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(accountID)}/traffic-migration`, cookie, {
    method: 'POST',
    body: { targetAccountId: trafficMigrationTargetID, sourceStatus: 'unchanged' }
  })
  assert.equal(trafficMigration.status, 200, `真实 accounts 流量迁移应成功：${trafficMigration.text}`)

  const secondaryGroup = await request(baseURL, '/__aisys__/api/groups', cookie, {
    method: 'POST',
    body: { name: 'F4 System API account target group smoke', providerCode: 'gpt', enabled: true }
  })
  assert.equal(secondaryGroup.status, 201, `真实 accounts 分组目标写入应成功：${secondaryGroup.text}`)
  const secondaryGroupID = envelope<{ id: string }>(secondaryGroup.text).id
  const accountDetail = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(accountID)}/edit-basic`, cookie)
  assert.equal(accountDetail.status, 200, `真实 accounts 基础详情应成功：${accountDetail.text}`)
  const initialAccountConfigRevision = envelope<{ configRevision: number }>(accountDetail.text).configRevision

  const bindAccountGroup = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(accountID)}/group`, cookie, {
    method: 'POST',
    body: { groupId: secondaryGroupID, expectedConfigRevision: initialAccountConfigRevision }
  })
  assert.equal(bindAccountGroup.status, 200, `真实 accounts 分组绑定应成功：${bindAccountGroup.text}`)
  const boundAccountConfigRevision = envelope<{ configRevision: number }>(bindAccountGroup.text).configRevision

  const updateAccountTags = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(accountID)}/tags`, cookie, {
    method: 'PATCH',
    body: { tags: ['f4-system-api-smoke'], expectedConfigRevision: boundAccountConfigRevision }
  })
  assert.equal(updateAccountTags.status, 200, `真实 accounts 标签更新应成功：${updateAccountTags.text}`)
  const taggedAccountConfigRevision = envelope<{ configRevision: number }>(updateAccountTags.text).configRevision

  const batchTargetAccountDetail = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(batchTargetAccountID)}/edit-basic`, cookie)
  assert.equal(batchTargetAccountDetail.status, 200, `真实 accounts 批量目标详情应成功：${batchTargetAccountDetail.text}`)
  const batchTargetAccountConfigRevision = envelope<{ configRevision: number }>(batchTargetAccountDetail.text).configRevision
  const batchUpdateAccounts = await request(baseURL, '/__aisys__/api/accounts/batch-update', cookie, {
    method: 'POST',
    body: {
      targets: [
        { accountId: accountID, configRevision: taggedAccountConfigRevision },
        { accountId: batchTargetAccountID, configRevision: batchTargetAccountConfigRevision }
      ],
      updates: {
        notes: { enabled: true, value: 'F4 System API batch edit smoke' }
      }
    }
  })
  assert.equal(batchUpdateAccounts.status, 200, `真实 accounts 批量编辑应成功：${batchUpdateAccounts.text}`)

  const exportAccounts = await request(baseURL, '/__aisys__/api/accounts/export', cookie, {
    method: 'POST',
    body: { accountIds: [accountID] }
  })
  assert.equal(exportAccounts.status, 200, `真实 accounts 导出应成功：${exportAccounts.text}`)

  const createAuthorization = await request(baseURL, `/__aisys__/api/authorizations?systemAccountId=${encodeURIComponent(adminSystemAccountID)}`, cookie, {
    method: 'POST',
    body: {
      resourceType: 'account',
      resourceId: accountID,
      granteeType: 'system_account',
      granteeId: systemAccountID,
      targetGroupId: memberGroupID,
      remark: 'F4 System API authorization smoke'
    }
  })
  assert.equal(createAuthorization.status, 201, `真实 authorizations 业务写入应成功：${createAuthorization.text}`)

  const memberAccounts = await request(baseURL, '/__aisys__/api/my-accounts?page=1&pageSize=50', memberCookie)
  assert.equal(memberAccounts.status, 200, `真实授权账户列表读取应成功：${memberAccounts.text}`)
  const authorizedAccount = envelope<{ items: Array<{ id: string; configRevision: number; authorizationInstanceSourceAccountId?: string }> }>(memberAccounts.text)
    .items.find((candidate) => candidate.authorizationInstanceSourceAccountId === accountID)
  assert(authorizedAccount, '普通用户必须能在账户列表中看到刚创建的授权账户实例')

  const updateAuthorizedDispatch = await request(baseURL, `/__aisys__/api/my-accounts/${encodeURIComponent(authorizedAccount.id)}/authorized-dispatch`, memberCookie, {
    method: 'PATCH',
    body: { priority: 7, expectedConfigRevision: authorizedAccount.configRevision }
  })
  assert.equal(updateAuthorizedDispatch.status, 200, `真实授权账户调度修改应成功：${updateAuthorizedDispatch.text}`)

  const returnAuthorizedAccount = await request(baseURL, `/__aisys__/api/my-accounts/${encodeURIComponent(authorizedAccount.id)}/return-authorization`, memberCookie, {
    method: 'POST'
  })
  assert.equal(returnAuthorizedAccount.status, 204, `真实授权账户归还应成功：${returnAuthorizedAccount.text}`)

  const deleteBatchTargetAccount = await request(baseURL, `/__aisys__/api/accounts/${encodeURIComponent(batchTargetAccountID)}`, cookie, {
    method: 'DELETE'
  })
  assert.equal(deleteBatchTargetAccount.status, 204, `真实 accounts 删除应成功：${deleteBatchTargetAccount.text}`)

  const updateOwnProfile = await request(baseURL, '/__aisys__/api/auth/me', memberCookie, {
    method: 'PATCH',
    body: { displayName: 'F4SystemAPIMemberUpdated' }
  })
  assert.equal(updateOwnProfile.status, 200, `真实 auth 个人资料更新应成功：${updateOwnProfile.text}`)

  const routeStrategy = await request(baseURL, '/__aisys__/api/route-strategies', cookie, {
    method: 'POST',
    body: {
      name: 'F4 System API route strategy smoke',
      mode: 'normal',
      groupBindings: [{ groupId: groupID, priority: 1, weight: 100, status: 'active' }]
    }
  })
  assert.equal(routeStrategy.status, 201, `真实 route-strategies 业务写入应成功：${routeStrategy.text}`)
  const routeStrategyID = envelope<{ id: string }>(routeStrategy.text).id

  const apiKey = await request(baseURL, '/__aisys__/api/api-keys', cookie, {
    method: 'POST',
    body: { name: 'F4 System API key smoke', routeStrategyId: routeStrategyID, status: 'active' }
  })
  assert.equal(apiKey.status, 201, `真实 api-keys 业务写入应成功：${apiKey.text}`)

  const proxy = await request(baseURL, '/__aisys__/api/proxies', cookie, {
    method: 'POST',
    body: { name: 'F4 System API proxy smoke', type: 'http', host: '127.0.0.1', port: 7890, enabled: true }
  })
  assert.equal(proxy.status, 201, `真实 proxies 业务写入应成功：${proxy.text}`)

  const settingsItem = await assertOperation(baseURL, cookie, 'settings', 'update_global', '更新全局品牌设置')
  const settingsDetail = await readAdminDetail(baseURL, cookie, settingsItem.id, 'settings.update_global', 'global_settings')
  assert.equal(settingsDetail.method, 'PATCH')
  assert.equal(settingsDetail.path, '/__aisys__/api/settings/global')
  assert(settingsDetail.changes?.some((change) => change.field === 'appName' && change.after === 'F4 System API settings smoke'), '管理详情必须保留真实业务字段变更')
  await assertPersonalSummary(baseURL, cookie, settingsItem.id, true, false)

  const announcementItem = await assertOperation(baseURL, cookie, 'announcements', 'create', '创建公告：F4 System API announcement smoke')
  const announcementDetail = await readAdminDetail(baseURL, cookie, announcementItem.id, 'announcements.create', 'announcement')
  assert.equal(announcementDetail.path, '/__aisys__/api/announcements/')
  await assertPersonalSummary(baseURL, cookie, announcementItem.id, false, false)

  const policyItem = await assertOperation(baseURL, cookie, 'response_inspection_policies', 'create', '创建响应检查策略：F4 System API response policy smoke')
  const policyDetail = await readAdminDetail(baseURL, cookie, policyItem.id, 'response_inspection_policies.create', 'response_inspection_policy')
  assert.equal(policyDetail.path, '/__aisys__/api/response-inspection-policies/')
  await assertPersonalSummary(baseURL, cookie, policyItem.id, false, false)

  const externalSourceItem = await assertOperation(baseURL, cookie, 'external_integration_sources', 'create', '创建外部来源系统：F4 System API external source smoke')
  const externalSourceDetail = await readAdminDetail(baseURL, cookie, externalSourceItem.id, 'external_integration_sources.create', 'external_integration_source')
  assert.equal(externalSourceDetail.path, '/__aisys__/api/external-integration-sources/')
  await assertPersonalSummary(baseURL, cookie, externalSourceItem.id, false, false)

  const providerItem = await assertOperation(baseURL, cookie, 'providers', 'update_model_configuration', `更新模型配置：${builtInProviderModel.model}`)
  const providerDetail = await readAdminDetail(baseURL, cookie, providerItem.id, 'providers.update_model_configuration', 'provider_model')
  assert.equal(providerDetail.path, `/__aisys__/api/providers/gpt/models/${builtInProviderModel.id}`)
  await assertPersonalSummary(baseURL, cookie, providerItem.id, false, false)

  const externalAccountItem = await assertOperation(baseURL, cookie, 'external_integrations', 'account_add', 'F4 System API external source smoke 新增账号：F4 external integration account smoke')
  const externalAccountDetail = await readAdminDetail(baseURL, cookie, externalAccountItem.id, 'external_integrations.public_account_add', 'account')
  assert.equal(externalAccountDetail.path, '/__aipublic__/account/add')
  await assertPersonalSummary(baseURL, cookie, externalAccountItem.id, false, false)

  const groupItem = await assertOperation(baseURL, cookie, 'groups', 'create', '创建分组：F4 System API group smoke')
  const groupDetail = await readAdminDetail(baseURL, cookie, groupItem.id, 'groups.create', 'group')
  assert.equal(groupDetail.path, '/__aisys__/api/groups/')
  await assertPersonalSummary(baseURL, cookie, groupItem.id, true, true)

  const accountItem = await assertOperation(baseURL, cookie, 'accounts', 'create', '创建 AI 账户：F4 System API account smoke')
  const accountLogDetail = await readAdminDetail(baseURL, cookie, accountItem.id, 'accounts.create', 'account')
  assert.equal(accountLogDetail.path, '/__aisys__/api/accounts/')
  assert(accountLogDetail.changes?.some((change) => change.field === 'credentials'), '账户创建详情必须保留脱敏后的凭据字段变更')
  await assertPersonalSummary(baseURL, cookie, accountItem.id, true, true)

  const forceActivateItem = await assertOperation(baseURL, cookie, 'accounts', 'force_activate', '人工恢复待检查 AI 账户：F4 System API pending account smoke')
  const forceActivateDetail = await readAdminDetail(baseURL, cookie, forceActivateItem.id, 'accounts.force_activate_pending', 'account')
  assert.equal(forceActivateDetail.path, `/__aisys__/api/accounts/${pendingAccountID}/force-activate`)
  await assertPersonalSummary(baseURL, cookie, forceActivateItem.id, true, true)

  const trafficMigrationItem = await assertOperation(baseURL, cookie, 'accounts', 'traffic_migration', '迁移账户流量：F4 System API account smoke -> F4 System API traffic target smoke')
  const trafficMigrationDetail = await readAdminDetail(baseURL, cookie, trafficMigrationItem.id, 'accounts.traffic_migration', 'account')
  assert.equal(trafficMigrationDetail.path, `/__aisys__/api/accounts/${accountID}/traffic-migration`)
  await assertPersonalSummary(baseURL, cookie, trafficMigrationItem.id, true, true)

  const bindGroupItem = await assertOperation(baseURL, cookie, 'accounts', 'bind_group', '绑定账户分组：F4 System API account smoke')
  const bindGroupDetail = await readAdminDetail(baseURL, cookie, bindGroupItem.id, 'accounts.bind_group', 'account')
  assert.equal(bindGroupDetail.path, `/__aisys__/api/accounts/${accountID}/group`)
  await assertPersonalSummary(baseURL, cookie, bindGroupItem.id, true, true)

  const tagsItem = await assertOperation(baseURL, cookie, 'accounts', 'update_tags', '更新账户标签：F4 System API account smoke')
  const tagsDetail = await readAdminDetail(baseURL, cookie, tagsItem.id, 'accounts.update_tags', 'account')
  assert.equal(tagsDetail.path, `/__aisys__/api/accounts/${accountID}/tags`)
  await assertPersonalSummary(baseURL, cookie, tagsItem.id, true, true)

  const batchUpdateItem = await assertOperation(baseURL, cookie, 'accounts', 'batch_update', '批量更新 2 个 AI 账户')
  const batchUpdateDetail = await readAdminDetail(baseURL, cookie, batchUpdateItem.id, 'accounts.batch_update', 'account_batch')
  assert.equal(batchUpdateDetail.path, '/__aisys__/api/accounts/batch-update')
  await assertPersonalSummary(baseURL, cookie, batchUpdateItem.id, true, true)

  const exportItem = await assertOperation(baseURL, cookie, 'accounts', 'export', '导出 AI 账户：1 个账户，0 个代理')
  const exportDetail = await readAdminDetail(baseURL, cookie, exportItem.id, 'accounts.export', 'account')
  assert.equal(exportDetail.path, '/__aisys__/api/accounts/export')
  await assertPersonalSummary(baseURL, cookie, exportItem.id, false, false)

  const authorizationItem = await assertOperation(baseURL, cookie, 'authorizations', 'create', '创建资源授权：F4 System API account smoke -> F4SystemAPIMemberSmoke')
  const authorizationDetail = await readAdminDetail(baseURL, cookie, authorizationItem.id, 'authorizations.create', 'authorization')
  assert.equal(authorizationDetail.path, '/__aisys__/api/authorizations/')
  await assertPersonalSummary(baseURL, cookie, authorizationItem.id, true, true)
  await assertPersonalSummary(baseURL, memberCookie, authorizationItem.id, true, true)

  const authorizedDispatchItem = await assertOperation(baseURL, cookie, 'accounts', 'authorized_dispatch', '调整授权账户使用设置：F4 System API account smoke')
  const authorizedDispatchDetail = await readAdminDetail(baseURL, cookie, authorizedDispatchItem.id, 'accounts.authorized_dispatch', 'account')
  assert.equal(authorizedDispatchDetail.path, `/__aisys__/api/my-accounts/${authorizedAccount.id}/authorized-dispatch`)
  await assertPersonalSummary(baseURL, cookie, authorizedDispatchItem.id, false, false)
  await assertPersonalSummary(baseURL, memberCookie, authorizedDispatchItem.id, true, true)

  const returnAuthorizationItem = await assertOperation(baseURL, cookie, 'authorizations', 'return', '归还授权账户：F4 System API account smoke')
  const returnAuthorizationDetail = await readAdminDetail(baseURL, cookie, returnAuthorizationItem.id, 'accounts.return_authorization', 'authorization')
  assert.equal(returnAuthorizationDetail.path, `/__aisys__/api/my-accounts/${authorizedAccount.id}/return-authorization`)
  await assertPersonalSummary(baseURL, cookie, returnAuthorizationItem.id, true, true)
  await assertPersonalSummary(baseURL, memberCookie, returnAuthorizationItem.id, true, true)

  const deleteItem = await assertOperation(baseURL, cookie, 'accounts', 'delete', '删除 AI 账户：F4 System API batch target smoke')
  const deleteDetail = await readAdminDetail(baseURL, cookie, deleteItem.id, 'accounts.delete', 'account')
  assert.equal(deleteDetail.path, `/__aisys__/api/accounts/${batchTargetAccountID}`)
  await assertPersonalSummary(baseURL, cookie, deleteItem.id, true, true)

  const profileItem = await assertOperation(baseURL, cookie, 'system_accounts', 'update', '修改显示名称：F4SystemAPIMemberUpdated')
  const profileDetail = await readAdminDetail(baseURL, cookie, profileItem.id, 'auth.update_profile', 'system_account')
  assert.equal(profileDetail.path, '/__aisys__/api/auth/me')
  await assertPersonalSummary(baseURL, memberCookie, profileItem.id, true, true)

  const routeStrategyItem = await assertOperation(baseURL, cookie, 'route_strategies', 'create', '创建策略路由：F4 System API route strategy smoke')
  const routeStrategyDetail = await readAdminDetail(baseURL, cookie, routeStrategyItem.id, 'route_strategies.create', 'route_strategy')
  assert.equal(routeStrategyDetail.path, '/__aisys__/api/route-strategies/')
  await assertPersonalSummary(baseURL, cookie, routeStrategyItem.id, true, true)

  const apiKeyItem = await assertOperation(baseURL, cookie, 'api_keys', 'create', '创建 API Key：F4 System API key smoke')
  const apiKeyDetail = await readAdminDetail(baseURL, cookie, apiKeyItem.id, 'api_keys.create', 'api_key')
  assert.equal(apiKeyDetail.path, '/__aisys__/api/api-keys/')
  await assertPersonalSummary(baseURL, cookie, apiKeyItem.id, true, true)

  const proxyItem = await assertOperation(baseURL, cookie, 'proxies', 'create', '创建代理：F4 System API proxy smoke')
  const proxyDetail = await readAdminDetail(baseURL, cookie, proxyItem.id, 'proxies.create', 'proxy')
  assert.equal(proxyDetail.path, '/__aisys__/api/proxies/')
  await assertPersonalSummary(baseURL, cookie, proxyItem.id, false, false)

  const systemAccountItem = await assertOperation(baseURL, cookie, 'system_accounts', 'create', '创建系统账户：F4SystemAPIMemberSmoke')
  const systemAccountDetail = await readAdminDetail(baseURL, cookie, systemAccountItem.id, 'system_accounts.create', 'system_account')
  assert.equal(systemAccountDetail.path, '/__aisys__/api/system-accounts/')
  await assertPersonalSummary(baseURL, cookie, systemAccountItem.id, true, true)

  const teamItem = await assertOperation(baseURL, cookie, 'system_teams', 'create', '创建系统团队：F4 System API team smoke')
  const teamDetail = await readAdminDetail(baseURL, cookie, teamItem.id, 'system_teams.create', 'system_team')
  assert.equal(teamDetail.path, '/__aisys__/api/system-teams/')
  await assertPersonalSummary(baseURL, cookie, teamItem.id, true, true)

  const addMemberItem = await assertOperation(baseURL, cookie, 'system_teams', 'add_members', '添加团队成员：F4 System API team smoke')
  const addMemberDetail = await readAdminDetail(baseURL, cookie, addMemberItem.id, 'system_teams.add_members', 'system_team')
  assert.equal(addMemberDetail.path, `/__aisys__/api/system-teams/${team.id}/members`)
  await assertPersonalSummary(baseURL, cookie, addMemberItem.id, true, true)
  await assertPersonalSummary(baseURL, memberCookie, addMemberItem.id, true, true)

  console.log(`F4 System API producer smoke passed: settings, announcements, response_inspection_policies, external-integration-sources, external-integrations, providers, groups, accounts, account-batch-edit, account-delete, account-force-activate, account-traffic-migration, account-group-binding, account-tags, account-export, account-authorized-dispatch, account-authorization-return, auth, authorizations, route_strategies, api_keys, proxies, system_accounts, system-teams (${inputURL})`)
} finally {
  if (server) await close(server)
  await closeSqliteReadWorkerPool?.().catch(() => undefined)
  closeStorageDatabases?.()
  await removeTempRoot()
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) throw new Error(`${name} is required for the F4 System API producer smoke`)
  return value
}

async function login(baseURL: string, captchaAnswerForTest: (captchaID: string) => string | undefined, username = 'admin', password = 'admin'): Promise<string> {
  const captcha = await request(baseURL, '/__aisys__/api/auth/captcha')
  assert.equal(captcha.status, 200, `captcha 应成功：${captcha.text}`)
  const captchaID = envelope<{ captchaId: string }>(captcha.text).captchaId
  const captchaCode = captchaAnswerForTest(captchaID)
  assert.ok(captchaCode, '测试必须能取得 captcha 答案')
  const response = await request(baseURL, '/__aisys__/api/auth/login', undefined, {
    method: 'POST',
    body: { username, password, captchaId: captchaID, captchaCode }
  })
  assert.equal(response.status, 200, `登录应成功：${response.text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert.ok(cookie, '登录应返回 session cookie')
  return cookie
}

async function request(baseURL: string, path: string, cookie?: string, options: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<{ status: number; text: string; headers: Headers }> {
  const response = await fetch(`${baseURL}${path}`, {
    method: options.method,
    headers: {
      ...(cookie ? { cookie } : {}),
      ...options.headers,
      ...(options.body === undefined ? {} : { 'content-type': 'application/json' })
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: AbortSignal.timeout(10_000)
  })
  return { status: response.status, text: await response.text(), headers: response.headers }
}

type OperationListItem = { id: string; module: string; action: string; summary: string }
type OperationDetail = { operationKey?: string; resourceType?: string; changes?: Array<{ field?: string; after?: unknown }>; method?: string; path?: string }

async function assertOperation(baseURL: string, cookie: string, module: string, action: string, summary: string): Promise<OperationListItem> {
  return eventually(async () => {
    const response = await request(baseURL, `/__aisys__/api/operation-logs?module=${encodeURIComponent(module)}&action=${encodeURIComponent(action)}&page=1&pageSize=20`, cookie)
    assert.equal(response.status, 200, `Go owner 管理读取 ${module}.${action} 应成功：${response.text}`)
    const data = envelope<{ items?: OperationListItem[] }>(response.text)
    return data.items?.find((candidate) => candidate.module === module && candidate.action === action && candidate.summary === summary)
  }, 10_000, `真实 ${module}.${action} 操作未通过 Node -> Go F4 -> Node 管理列表读回`)
}

async function readAdminDetail(baseURL: string, cookie: string, id: string, operationKey: string, resourceType: string): Promise<OperationDetail> {
  const response = await request(baseURL, `/__aisys__/api/operation-logs/${encodeURIComponent(id)}`, cookie)
  assert.equal(response.status, 200, `Go owner 管理详情应成功：${response.text}`)
  const data = envelope<OperationDetail>(response.text)
  assert.equal(data.operationKey, operationKey)
  assert.equal(data.resourceType, resourceType)
  return data
}

async function assertPersonalSummary(baseURL: string, cookie: string, id: string, visible: boolean, expectChanges: boolean): Promise<void> {
  const response = await request(baseURL, `/__aisys__/api/my-operation-logs/${encodeURIComponent(id)}`, cookie)
  assert.equal(response.status, visible ? 200 : 404, `个人详情可见性不符合 operation log contract：${response.text}`)
  if (!visible) return
  const data = envelope<{ changes?: unknown[]; targets?: unknown[]; viewers?: unknown[]; clientIp?: unknown }>(response.text)
  if (expectChanges) {
    assert((data.changes?.length ?? 0) > 0, 'full 个人详情必须保留脱敏后的业务变更')
  } else {
    assert.deepEqual(data.changes, [], 'summary 个人详情不得展开完整变更')
  }
  if (expectChanges) {
    assert(Array.isArray(data.targets), 'full 个人详情 targets 必须保持数组 JSON 形状')
    assert(Array.isArray(data.viewers), 'full 个人详情 viewers 必须保持数组 JSON 形状')
  } else {
    assert.deepEqual(data.targets, [], 'summary 个人详情 targets 必须为空数组')
    assert.deepEqual(data.viewers, [], 'summary 个人详情 viewers 必须为空数组')
  }
  assert.equal(data.clientIp, undefined, '个人详情不得返回 clientIp')
}

function envelope<T>(text: string): T {
  return (JSON.parse(text) as { data: T }).data
}

async function eventually<T>(operation: () => Promise<T | undefined>, timeoutMs: number, message: string): Promise<T> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      const value = await operation()
      if (value !== undefined) return value
    } catch (error) {
      lastError = error
    }
    await delay(50)
  }
  throw new Error(lastError ? `${message}: ${lastError instanceof Error ? lastError.message : String(lastError)}` : message)
}

async function listen(serverInstance: http.Server): Promise<void> {
  if (serverInstance.listening) return
  await new Promise<void>((resolve, reject) => {
    serverInstance.once('listening', resolve)
    serverInstance.once('error', reject)
  })
}

function addressPort(serverInstance: http.Server): number {
  const address = serverInstance.address()
  assert(address && typeof address === 'object', '测试服务监听地址无效')
  return address.port
}

async function close(serverInstance: http.Server): Promise<void> {
  if (!serverInstance.listening) return
  await new Promise<void>((resolve, reject) => {
    serverInstance.close((error) => error ? reject(error) : resolve())
  })
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}
