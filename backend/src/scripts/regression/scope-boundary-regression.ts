import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-scope-regression-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'scope-regression.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'scope-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })

const REGRESSION_FETCH_TIMEOUT_MS = 5000

const [
  { accountsRouter },
  { apiKeysRouter },
  { authorizationOptionsRouter },
  { authorizationsRouter },
  { groupsRouter },
  { providersRouter },
  { proxiesRouter },
  { statsRouter },
  { myTeamsRouter, systemTeamsRouter },
  { usageRecordsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  usageStatsRepository,
  usageStatsHelpers
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/api-keys/api-keys.routes.js'),
  import('../../modules/authorization-options/authorization-options.routes.js'),
  import('../../modules/authorizations/authorizations.routes.js'),
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/providers/providers.routes.js'),
  import('../../modules/proxies/proxies.routes.js'),
  import('../../modules/stats/stats.routes.js'),
  import('../../modules/system-teams/system-teams.routes.js'),
  import('../../modules/usage-records/usage-records.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-stats-helpers.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/__aisys__/api/my-api-keys', forceSelfAccessScope, apiKeysRouter)
app.use('/__aisys__/api/my-authorization-options', forceSelfAccessScope, authorizationOptionsRouter)
app.use('/__aisys__/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/__aisys__/api/my-usage-records', forceSelfAccessScope, usageRecordsRouter)
app.use('/__aisys__/api/my-stats', forceSelfAccessScope, statsRouter)
app.use('/__aisys__/api/my-teams', forceSelfAccessScope, myTeamsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)
app.use('/__aisys__/api/groups', requireAdmin, groupsRouter)
app.use('/__aisys__/api/api-keys', requireAdmin, apiKeysRouter)
app.use('/__aisys__/api/authorization-options', requireAdmin, authorizationOptionsRouter)
app.use('/__aisys__/api/authorizations', requireAdmin, authorizationsRouter)
app.use('/__aisys__/api/usage-records', requireAdmin, usageRecordsRouter)
app.use('/__aisys__/api/providers', providersRouter)
app.use('/__aisys__/api/proxies', proxiesRouter)
app.use('/__aisys__/api/stats', requireAdmin, statsRouter)
app.use('/__aisys__/api/system-teams', systemTeamsRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountSummary {
  id: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  name: string
  type?: string
  status?: string
  accessType?: string
  concurrencyLimit?: number
  supportedModels?: string[]
  proxyProfileId?: string
  accountAuthorizationId?: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceSourceAccountStatus?: string
  authorizationInstanceSourceAccountSchedulable?: boolean
  authorizationInstanceSourceAccountAvailabilitySchedule?: Record<string, unknown>
  authorizationInstanceSourceAccountScheduleActive?: boolean
  authorizationInstanceSourceAccountExpiresAt?: string
  boundGroupId?: string
  credentials?: Record<string, unknown>
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface AccountImportResult {
  canImport: boolean
  imported: boolean
  summary: {
    accounts: {
      create: number
      failed: number
      skip: number
      total: number
    }
  }
}

interface AccountExportResult {
  document: {
    accounts: Array<{ name: string }>
  }
  summary: {
    accounts: number
    skippedAccounts: number
  }
}

interface GroupSummary {
  id: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  name: string
  accessType?: string
}

interface GroupListResult {
  items: GroupSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface ApiKeySummary {
  id: string
  systemAccountId?: string
  name: string
  status?: string
  groupId?: string
}

interface ApiKeyListResult {
  items: ApiKeySummary[]
  total: number
  page: number
  pageSize: number
}

interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  accountId?: string
  accountName?: string
  groupId?: string
  model?: string
  statusCode?: number
  success: boolean
  createdAt: string
}

interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface AccountUsageStatsRow {
  id: string
  systemAccountId?: string
  ownerSystemAccountId: string
  name: string
  type: string
  accessType?: string
  rangeUsage: {
    requestCount: number
  }
}

interface AccountUsageStatsOverview {
  range: {
    days: number
    maxDays: number
  }
  rows: AccountUsageStatsRow[]
  defaultTrendAccountIds: string[]
  total: number
  page: number
  pageSize: number
}

interface AiPerformanceAccountOption {
  id: string
  name: string
  systemAccountId: string
  requestCountLast7d: number
}

interface AiPerformanceOverview {
  accounts: AiPerformanceAccountOption[]
  summary: {
    requestCount: number
  }
}

interface SystemTeamMemberSummary {
  id: string
  teamId: string
  systemAccountId: string
  status: string
}

interface SystemTeamSummary {
  id: string
  name: string
  status: string
  members?: SystemTeamMemberSummary[]
}

interface SystemTeamListResult {
  items: SystemTeamSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface SystemAccountPrincipalSummary {
  id: string
  username: string
  displayName: string
  status: string
}

interface SystemTeamPrincipalSummary {
  id: string
  name: string
  status: string
}

interface ResourceAuthorizationSummary {
  id: string
  resourceType: string
  resourceId: string
  resourceOwnerSystemAccountId: string
  granteeSystemAccountId?: string
  granteeTeamId?: string
  status: string
  permissions?: {
    canEdit: boolean
    canAuthorize: boolean
  }
}

interface ResourceAuthorizationListResult {
  items: ResourceAuthorizationSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface SeedState {
  adminId: string
  userAId: string
  userBId: string
  adminCookie: string
  userACookie: string
  userBCookie: string
  userAAccountId: string
  userBAccountId: string
  userAAuthorizedUserBAccountId: string
  userAAuthorizedUserBProxyId: string
  userBProxyId: string
  teamSharedId: string
  teamUserBOnlyId: string
  teamNoUserAId: string
  userCId: string
  userATargetGroupId: string
  inboundAuthorizationId: string
  inboundRuntimeAuthorizationId: string
  teamInboundAuthorizationId: string
  userBGroupId: string
  usageToday: string
  usageYesterday: string
}

async function main(): Promise<void> {
  let server: ReturnType<typeof app.listen> | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await onceListening(server)
    const address = server.address()
    if (!address || typeof address === 'string') {
      throw new Error('作用域回归服务地址不可用')
    }
    const baseUrl = `http://127.0.0.1:${address.port}`
    const seed = seedData()
    const summary: string[] = []

    await assertForbidden(`${baseUrl}/__aisys__/api/accounts`, seed.userACookie, '普通用户不能访问管理账户接口')
    summary.push('管理接口权限拦截通过')

    await assertForbidden(`${baseUrl}/__aisys__/api/system-teams`, seed.userACookie, '普通用户不能访问系统团队管理接口')
    await assertForbidden(`${baseUrl}/__aisys__/api/authorizations`, seed.userACookie, '普通用户不能访问统一授权管理接口')
    await assertForbidden(`${baseUrl}/__aisys__/api/providers`, seed.userACookie, '普通用户不能访问供应商管理接口')
    const userProviderOptions = await getEnvelope<Array<{ code: string; defaultTestModel: string }>>(baseUrl, '/__aisys__/api/providers/options', seed.userACookie)
    assert(userProviderOptions.some((item) => item.code === 'gpt' && typeof item.defaultTestModel === 'string'), '普通用户应能读取供应商安全选项')
    const userProviderModels = await getEnvelope<Array<{ model: string }>>(baseUrl, '/__aisys__/api/providers/gpt/models', seed.userACookie)
    assert(userProviderModels.some((item) => item.model === 'gpt-5.5'), '普通用户应能查询 GPT 模型列表用于账户模型限制下拉')
    const userProviderModelOptions = await getEnvelope<Array<{ providerCode: string; model: string }>>(baseUrl, '/__aisys__/api/providers/models/options', seed.userACookie)
    assert(userProviderModelOptions.some((item) => item.providerCode === 'gpt' && item.model === 'gpt-5.5'), '普通用户应能查询全部模型名称选项用于使用记录模型筛选')
    const userProxyOptions = await getEnvelope<Array<{ id: string; enabled: boolean }>>(baseUrl, '/__aisys__/api/proxies/options?limit=50', seed.userACookie)
    assert(userProxyOptions.some((proxy) => proxy.id === seed.userBProxyId && proxy.enabled === true), '普通用户应能查询已启用代理选项用于账户代理下拉')
    summary.push('仅管理员菜单接口拦截通过')

    const userAMyAccounts = await getAccountItems(baseUrl, '/__aisys__/api/my-accounts', seed.userACookie)
    assertSameIds(userAMyAccounts, [{ id: seed.userAAccountId }, { id: seed.userAAuthorizedUserBAccountId }], '用户 A 的 my-accounts 未返回自有账户和授权实例账户')
    assert(userAMyAccounts.some((account) => account.id === seed.userAAccountId && account.ownerSystemAccountId === seed.userAId), '用户 A 的 my-accounts 缺少自有账户')
    const userAAuthorizedAccount = userAMyAccounts.find((account) => account.id === seed.userAAuthorizedUserBAccountId)
    assert(userAAuthorizedAccount?.ownerSystemAccountId === seed.userBId && userAAuthorizedAccount.accessType === 'authorized', '用户 A 的 my-accounts 缺少授权给自己的实例账户')
    assert(userAAuthorizedAccount.accountAuthorizationId === seed.inboundRuntimeAuthorizationId, '用户 A 的授权实例应带最终用户运行时授权 ID')
    assert(userAAuthorizedAccount.authorizationInstanceSourceAccountId === seed.userBAccountId, '用户 A 的授权实例应保留来源账户快照')
    assert(userAAuthorizedAccount.proxyProfileId === seed.userBProxyId, '用户 A 的授权实例应从来源账户补齐代理配置')
    const userAMyAccountsWithQuery = await getAccountItems(baseUrl, `/__aisys__/api/my-accounts?systemAccountId=${seed.userBId}`, seed.userACookie)
    assertSameIds(userAMyAccounts, userAMyAccountsWithQuery, '用户 A 传 systemAccountId 后 my-accounts 结果发生变化')
    const userAOwnAccountDetail = await getEnvelope<AccountSummary>(baseUrl, `/__aisys__/api/my-accounts/${seed.userAAccountId}`, seed.userACookie)
    assert(userAOwnAccountDetail.credentials?.base_url === 'https://api.openai.com/v1', '用户 A 应能打开自有账户详情并读取非敏感 Base URL')
    assert(userAOwnAccountDetail.credentials?.api_key === 'sk-scope-user-a', '用户 A 自有账户详情应返回明文 API Key 供编辑弹窗查看')
    const userAExport = await postEnvelope<AccountExportResult>(
      baseUrl,
      `/__aisys__/api/my-accounts/export?systemAccountId=${seed.userBId}`,
      seed.userACookie,
      { accountIds: [seed.userAAccountId, seed.userBAccountId] }
    )
    assert(userAExport.summary.accounts === 1 && userAExport.summary.skippedAccounts === 1, '普通用户导出应只导出自己的自有账户并跳过不可见账户')
    assert(userAExport.document.accounts[0]?.name === '用户 A 账户', '普通用户导出不应混入其他用户账户')
    const userAImportDocument = {
      type: 'juhe-ai-account-import',
      version: 1,
      accounts: [{
        ref: 'scope-user-a-import',
        name: '用户 A 导入账户',
        providerCode: 'gpt',
        type: 'api_key',
        status: 'disabled',
        groupId: seed.userATargetGroupId,
        credentials: {
          api_key: 'sk-scope-user-a-import',
          base_url: 'https://api.openai.com/v1'
        }
      }]
    }
    const userAImportPreview = await postEnvelope<AccountImportResult>(
      baseUrl,
      `/__aisys__/api/my-accounts/import/preview?systemAccountId=${seed.userBId}`,
      seed.userACookie,
      { data: userAImportDocument, options: {} }
    )
    assert(userAImportPreview.canImport && userAImportPreview.summary.accounts.create === 1, '普通用户应能在我的 AI 账户预览导入自己的账户')
    const userAImportConfirm = await postEnvelope<AccountImportResult>(
      baseUrl,
      `/__aisys__/api/my-accounts/import/confirm?systemAccountId=${seed.userBId}`,
      seed.userACookie,
      { data: userAImportDocument, options: {} }
    )
    assert(userAImportConfirm.imported && userAImportConfirm.summary.accounts.create === 1, '普通用户应能在我的 AI 账户确认导入自己的账户')
    const userAImportedAccounts = await getAccountItems(baseUrl, `/__aisys__/api/my-accounts?keyword=${encodeURIComponent('用户 A 导入账户')}`, seed.userACookie)
    assert(userAImportedAccounts.length === 1 && userAImportedAccounts[0].ownerSystemAccountId === seed.userAId, '普通用户导入账户应固定写入当前用户作用域')
    await assertStatus(
      `${baseUrl}/__aisys__/api/my-accounts/${seed.userBAccountId}`,
      seed.userACookie,
      404,
      '用户 A 不应通过授权账户详情接口查看用户 B 原账户'
    )
    const userAAuthorizedAccountDetail = await getEnvelope<AccountSummary>(baseUrl, `/__aisys__/api/my-accounts/${seed.userAAuthorizedUserBAccountId}`, seed.userACookie)
    assert(userAAuthorizedAccountDetail.accessType === 'authorized', '用户 A 应能打开自己的授权实例详情')
    assert(userAAuthorizedAccountDetail.credentials?.base_url === 'https://api.openai.com/v1', '授权实例详情应返回来源账户公开 Base URL')
    assert(userAAuthorizedAccountDetail.concurrencyLimit === 3, '授权实例详情应只读展示来源账户并发上限')
    assert(userAAuthorizedAccountDetail.proxyProfileId === seed.userBProxyId, '授权实例详情应只读展示来源账户代理配置')
    assert(userAAuthorizedAccountDetail.supportedModels?.includes('gpt-5.5'), '授权实例详情应只读展示来源账户模型限制')
    assert(userAAuthorizedAccountDetail.authorizationInstanceSourceAccountStatus === 'active', '授权实例详情应返回来源账户状态')
    assert(userAAuthorizedAccountDetail.authorizationInstanceSourceAccountSchedulable === true, '授权实例详情应返回来源账户调度开关')
    assert(userAAuthorizedAccountDetail.authorizationInstanceSourceAccountExpiresAt === '2027-12-31T00:00:00.000Z', '授权实例详情应返回来源账户到期时间')
    assert(userAAuthorizedAccountDetail.authorizationInstanceSourceAccountAvailabilitySchedule?.enabled === true, '授权实例详情应返回来源账户可用时段计划')
    const authorizedDetailCredentials = userAAuthorizedAccountDetail.credentials ?? {}
    const authorizedDetailSecretJson = JSON.stringify(authorizedDetailCredentials)
    for (const secretKey of ['api_key', 'access_token', 'refresh_token', 'id_token']) {
      assert(!Object.prototype.hasOwnProperty.call(authorizedDetailCredentials, secretKey), `授权实例详情不应返回敏感凭据字段 ${secretKey}`)
    }
    assert(!authorizedDetailSecretJson.includes('sk-scope-user-b'), '授权实例详情不应泄露来源账户 API Key 明文')
    const errorHandlingRules = authorizedDetailCredentials.error_handling_rules as Array<Record<string, unknown>> | undefined
    assert(errorHandlingRules?.[0]?.name === '授权来源 429 限流', '授权实例详情应返回来源账户错误处理策略')
    const streamInterceptRules = authorizedDetailCredentials.stream_intercept_rules as Array<Record<string, unknown>> | undefined
    assert(streamInterceptRules?.[0]?.name === '授权来源流式异常', '授权实例详情应返回来源账户流式拦截规则')
    summary.push('我的账户自有作用域检查通过')

    const adminMyAccounts = await getAccountItems(baseUrl, `/__aisys__/api/my-accounts?systemAccountId=${seed.userBId}`, seed.adminCookie)
    assert(adminMyAccounts.every((account) => account.ownerSystemAccountId === seed.adminId), '管理员 my-accounts 没有固定为管理员自己的用户侧作用域')
    summary.push('管理员用户侧菜单作用域检查通过')

    const adminUserBAccounts = await getAccountItems(baseUrl, `/__aisys__/api/accounts?systemAccountId=${seed.userBId}`, seed.adminCookie)
    assert(adminUserBAccounts.every((account) => account.ownerSystemAccountId === seed.userBId), '管理账户接口按用户 B 筛选失败')
    summary.push('管理账户筛选检查通过')

    const userBAccountPage1 = await getEnvelope<AccountListResult>(baseUrl, `/__aisys__/api/accounts?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBAccountPage1.total === 1 && userBAccountPage1.items.length === 1 && userBAccountPage1.pageSize === 1, '管理账户分页或关键词筛选异常')
    const userBDisabledAccounts = await getEnvelope<AccountListResult>(baseUrl, `/__aisys__/api/accounts?systemAccountId=${seed.userBId}&status=disabled&page=1&pageSize=10`, seed.adminCookie)
    assert(userBDisabledAccounts.total === 0, '管理账户状态筛选异常')
    const userAApiKeyAccounts = await getEnvelope<AccountListResult>(baseUrl, '/__aisys__/api/my-accounts?type=api_key&page=1&pageSize=10', seed.userACookie)
    assert(
      userAApiKeyAccounts.items.every((account) => account.type === 'api_key')
        && userAApiKeyAccounts.items.some((account) => account.id === seed.userAAccountId)
        && userAApiKeyAccounts.items.some((account) => account.name === '用户 A 导入账户'),
      '用户侧账户类型筛选异常'
    )
    const userAAccountOptions = await getEnvelope<AccountSummary[]>(baseUrl, '/__aisys__/api/my-accounts/options?limit=50', seed.userACookie)
    assert(userAAccountOptions.some((account) => account.id === seed.userAAccountId), '普通用户应能查询自有账户选项用于用户侧下拉')
    assert(userAAccountOptions.some((account) => account.id === seed.userAAuthorizedUserBAccountId && account.accessType === 'authorized'), '普通用户应能查询授权实例账户选项用于用户侧下拉')
    summary.push('账户分页筛选检查通过')

    const createdGroup = await postEnvelope<GroupSummary>(baseUrl, `/__aisys__/api/groups?systemAccountId=${seed.userBId}`, seed.adminCookie, {
      name: '用户 B 管理代建分组',
      providerCode: 'gpt'
    })
    assert(createdGroup.systemAccountId === seed.userBId, '管理员按用户 B 创建分组没有归属到用户 B')
    const userBGroupPage1 = await getEnvelope<GroupListResult>(baseUrl, `/__aisys__/api/groups?systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBGroupPage1.items.length === 1 && userBGroupPage1.page === 1 && userBGroupPage1.pageSize === 1, '管理分组分页第一页异常')
    assert(userBGroupPage1.hasMore === true && userBGroupPage1.total >= 2, '管理分组分页应使用分页上界 total 并提示还有更多')
    const userAMyGroupPage1 = await getEnvelope<GroupListResult>(baseUrl, '/__aisys__/api/my-groups?page=1&pageSize=1', seed.userACookie)
    assert(userAMyGroupPage1.items.length === 1 && userAMyGroupPage1.pageSize === 1, '用户侧分组分页第一页异常')
    assert(userAMyGroupPage1.hasMore === true, '用户侧分组分页应提示还有更多')
    const userAGroupOptions = await getEnvelope<GroupSummary[]>(baseUrl, '/__aisys__/api/my-groups/options?limit=50', seed.userACookie)
    assert(userAGroupOptions.length >= 1 && userAGroupOptions.every((group) => group.ownerSystemAccountId === seed.userAId || group.accessType === 'authorized'), '普通用户应能查询用户侧分组选项且不能混入不可见分组')
    assert(userAGroupOptions.some((group) => group.id === seed.userBGroupId && group.ownerSystemAccountId === seed.userBId && group.accessType === 'authorized'), '团队分组授权应让用户 A 看到授权分组')
    summary.push('管理员代建分组归属检查通过')

    const createdApiKey = await postEnvelope<ApiKeySummary>(baseUrl, `/__aisys__/api/api-keys?systemAccountId=${seed.userBId}`, seed.adminCookie, {
      name: '用户 B 管理代建 Key',
      groupBindings: [{ groupId: seed.userBGroupId, priority: 1, status: 'active' }]
    })
    assert(createdApiKey.systemAccountId === seed.userBId, '管理员按用户 B 创建 API Key 没有归属到用户 B')
    summary.push('管理员代建 API Key 归属检查通过')

    const userAKeys = await getApiKeyItems(baseUrl, `/__aisys__/api/my-api-keys?systemAccountId=${seed.userBId}`, seed.userACookie)
    assert(userAKeys.every((item) => item.systemAccountId === undefined || item.systemAccountId === seed.userAId), '用户 A 的 my-api-keys 返回了其他用户密钥')
    summary.push('我的 API Key 自有作用域检查通过')

    const userBKeyPage = await getEnvelope<ApiKeyListResult>(baseUrl, `/__aisys__/api/api-keys?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBKeyPage.total >= 1 && userBKeyPage.items.length === 1 && userBKeyPage.items[0]?.systemAccountId === seed.userBId, '管理 API Key 分页或关键词筛选异常')
    const activeUserBKeys = await getEnvelope<ApiKeyListResult>(baseUrl, `/__aisys__/api/api-keys?systemAccountId=${seed.userBId}&status=active&groupId=${seed.userBGroupId}`, seed.adminCookie)
    assert(activeUserBKeys.items.every((item) => item.status === 'active' && ((item as unknown as { groupBindings: Array<{ groupId: string }> }).groupBindings.some((binding) => binding.groupId === seed.userBGroupId))), '管理 API Key 状态或分组筛选异常')
    summary.push('API Key 分页筛选检查通过')

    const userAUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/my-usage-records?systemAccountId=${seed.userBId}&page=1&pageSize=2`, seed.userACookie)
    assert(userAUsage.items.length === 2, `用户 A 使用记录分页数量异常：${userAUsage.items.length}`)
    assert(userAUsage.hasMore === true, '用户 A 使用记录第一页应提示还有更多')
    const userAUsagePage2 = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/my-usage-records?systemAccountId=${seed.userBId}&page=2&pageSize=2`, seed.userACookie)
    assert(userAUsagePage2.items.length === 1, `用户 A 使用记录第二页数量异常：${userAUsagePage2.items.length}`)
    assert(userAUsagePage2.hasMore === false, '用户 A 使用记录第二页不应还有更多')
    const userAUsageItems = [...userAUsage.items, ...userAUsagePage2.items]
    assert(userAUsageItems.every((record) => record.systemAccountId === undefined || record.systemAccountId === seed.userAId), '用户 A 的 my-usage-records 返回了其他用户记录')
    summary.push('我的使用记录自有作用域检查通过')

    const userBUsagePage1 = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&accountKeyword=${encodeURIComponent('用户 B')}&page=1&pageSize=2`, seed.adminCookie)
    assert(userBUsagePage1.items.length === 2, `管理使用记录分页第一页数量异常：${userBUsagePage1.items.length}`)
    assert(userBUsagePage1.hasMore === true, '管理使用记录第一页应提示还有更多')
    assert(userBUsagePage1.items.every((record) => record.systemAccountId === seed.userBId), '管理使用记录按用户 B 查询返回了其他用户记录')
    const userBUsagePage2 = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&accountKeyword=${encodeURIComponent('用户 B')}&page=2&pageSize=2`, seed.adminCookie)
    assert(userBUsagePage2.items.length === 1, `管理使用记录分页第二页数量异常：${userBUsagePage2.items.length}`)
    assert(userBUsagePage2.hasMore === false, '管理使用记录第二页不应还有更多')
    const failedUserBUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&result=failed&statusCode=429&page=1&pageSize=10`, seed.adminCookie)
    assert(failedUserBUsage.total === 1 && failedUserBUsage.items[0]?.statusCode === 429 && failedUserBUsage.items[0]?.success === false, '管理使用记录失败状态码筛选异常')
    const modelFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&model=${encodeURIComponent('scope-model-c')}&page=1&pageSize=10`, seed.adminCookie)
    assert(modelFilteredUsage.total === 1 && modelFilteredUsage.items[0]?.model === 'scope-model-c', '管理使用记录模型筛选异常')
    const groupFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&groupId=${seed.userBGroupId}&page=1&pageSize=10`, seed.adminCookie)
    assert(groupFilteredUsage.total === 3 && groupFilteredUsage.items.every((record) => record.groupId === seed.userBGroupId), '管理使用记录分组筛选异常')
    const dateFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&startDate=${seed.usageToday}&endDate=${seed.usageToday}&page=1&pageSize=10`, seed.adminCookie)
    assert(dateFilteredUsage.total === 3 && dateFilteredUsage.items.every((record) => record.systemAccountId === seed.userBId), '管理使用记录自然日范围筛选异常')
    const emptyDateFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&startDate=${seed.usageYesterday}&endDate=${seed.usageYesterday}&page=1&pageSize=10`, seed.adminCookie)
    assert(emptyDateFilteredUsage.total === 0 && emptyDateFilteredUsage.items.length === 0, '管理使用记录自然日范围空结果筛选异常')
    repositories.createUsageRecordsBatch([
      usageRecord('scope_usage_b_dst_before', seed.userBId, seed.userBAccountId, 'POST /v1/responses', 'scope-model-dst', 200, true, '2026-04-04T10:59:59.000Z'),
      usageRecord('scope_usage_b_dst_start', seed.userBId, seed.userBAccountId, 'POST /v1/responses', 'scope-model-dst', 200, true, '2026-04-04T11:00:01.000Z'),
      usageRecord('scope_usage_b_dst_end', seed.userBId, seed.userBAccountId, 'POST /v1/responses', 'scope-model-dst', 200, true, '2026-04-05T11:59:59.000Z')
    ])
    const originalUsageStatsTimezone = usageStatsTimezoneSetting()
    setUsageStatsTimezoneSetting('Pacific/Auckland')
    try {
      const dstDateFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/__aisys__/api/usage-records?systemAccountId=${seed.userBId}&model=${encodeURIComponent('scope-model-dst')}&startDate=2026-04-05&endDate=2026-04-05&page=1&pageSize=10`, seed.adminCookie)
      assert(dstDateFilteredUsage.total === 2, `管理使用记录夏令时自然日边界筛选异常：${dstDateFilteredUsage.total}`)
      assert(dstDateFilteredUsage.items.some((record) => record.id === 'scope_usage_b_dst_start'), '夏令时自然日筛选缺少当天首小时记录')
      assert(dstDateFilteredUsage.items.some((record) => record.id === 'scope_usage_b_dst_end'), '夏令时自然日筛选缺少当天结束前记录')
      assert(!dstDateFilteredUsage.items.some((record) => record.id === 'scope_usage_b_dst_before'), '夏令时自然日筛选不应包含前一日本地记录')
    } finally {
      setUsageStatsTimezoneSetting(originalUsageStatsTimezone)
    }
    summary.push('使用记录分页筛选检查通过')

    const userAAccountUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/my-stats/account-usage?systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.userACookie)
    assert(userAAccountUsage.total === 2, `用户 A 账号用量统计应只统计有用量账户，实际：${userAAccountUsage.total}`)
    assert(userAAccountUsage.rows.length === 1 && userAAccountUsage.pageSize === 1, '用户 A 账号用量统计分页异常')
    assert(userAAccountUsage.rows.every((row) => userAMyAccounts.some((account) => account.id === row.id)), '用户 A 的账号用量统计返回了不可见账户')
    const userATypeIgnoredUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, '/__aisys__/api/my-stats/account-usage?type=oauth&page=1&pageSize=10', seed.userACookie)
    assert(userATypeIgnoredUsage.total === 2 && userATypeIgnoredUsage.rows.some((row) => row.type === 'api_key'), '用户侧账号用量统计不应按账号类型过滤')
    const userAKeywordUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/my-stats/account-usage?keyword=${encodeURIComponent('用户 A')}&page=1&pageSize=10`, seed.userACookie)
    assert(userAKeywordUsage.total === 1 && userAKeywordUsage.rows[0]?.id === seed.userAAccountId, '用户侧账号用量统计关键词筛选异常')
    const userAAuthorizedAccountUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/my-stats/account-usage?keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=10`, seed.userACookie)
    assert(userAAuthorizedAccountUsage.total === 1 && userAAuthorizedAccountUsage.rows[0]?.id === seed.userAAuthorizedUserBAccountId, '用户 A 我的用量应能看到自己使用的授权实例账户')
    assert(userAAuthorizedAccountUsage.rows[0]?.accessType === 'authorized', '用户 A 我的用量应按授权实例口径标记授权来源')
    assert(userAAuthorizedAccountUsage.rows[0]?.rangeUsage.requestCount === 1, `用户 A 我的用量授权账户请求数异常：${userAAuthorizedAccountUsage.rows[0]?.rangeUsage.requestCount}`)

    const userBAccountUsagePage1 = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/stats/account-usage?systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBAccountUsagePage1.total === 1 && userBAccountUsagePage1.rows.length === 1 && userBAccountUsagePage1.pageSize === 1, '管理账号用量统计分页第一页异常')
    assert(userBAccountUsagePage1.rows.every((row) => row.ownerSystemAccountId === seed.userBId), '管理账号用量统计按用户 B 查询返回了其他用户账户')
    const userBAccountUsagePage2 = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/stats/account-usage?systemAccountId=${seed.userBId}&page=2&pageSize=1`, seed.adminCookie)
    assert(userBAccountUsagePage2.rows.length === 0, '管理账号用量统计分页第二页应无数据')
    const userBAccountUsageKeyword = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/stats/account-usage?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=10`, seed.adminCookie)
    assert(userBAccountUsageKeyword.total === 1 && userBAccountUsageKeyword.rows[0]?.name.includes('用户 B'), '管理账号用量统计关键词筛选异常')
    assert(userBAccountUsageKeyword.rows[0]?.rangeUsage.requestCount === 3, `用户 B 我的用量不应混入被授权人调用，实际 ${userBAccountUsageKeyword.rows[0]?.rangeUsage.requestCount}`)
    const userBTypeIgnoredUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/__aisys__/api/stats/account-usage?systemAccountId=${seed.userBId}&type=oauth&page=1&pageSize=10`, seed.adminCookie)
    assert(userBTypeIgnoredUsage.total === 1 && userBTypeIgnoredUsage.rows[0]?.id === seed.userBAccountId, '管理账号用量统计不应按账号类型过滤')
    const adminAllAccountUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, '/__aisys__/api/stats/account-usage', seed.adminCookie)
    assert(adminAllAccountUsage.range.days === 31 && adminAllAccountUsage.range.maxDays === 31, '管理账号用量统计默认范围应为最近 31 天')
    assert(adminAllAccountUsage.defaultTrendAccountIds.length === 3, `管理员全部用户默认趋势账户数量异常：${adminAllAccountUsage.defaultTrendAccountIds.length}`)
    assert(
      adminAllAccountUsage.defaultTrendAccountIds[0] === seed.userBAccountId
      && adminAllAccountUsage.defaultTrendAccountIds[1] === seed.userAAccountId
      && adminAllAccountUsage.defaultTrendAccountIds[2] === seed.userAAuthorizedUserBAccountId,
      '管理员全部用户默认趋势账户应按全局可见账户用量排序并包含授权实例'
    )
    summary.push('账号用量统计分页筛选检查通过')

    const userAAiPerformanceAccounts = await getEnvelope<AiPerformanceAccountOption[]>(baseUrl, `/__aisys__/api/my-stats/ai-performance/accounts?keyword=${encodeURIComponent('用户 B')}`, seed.userACookie)
    assert(userAAiPerformanceAccounts.some((account) => account.id === seed.userAAuthorizedUserBAccountId), 'AI性能监控应返回当前用户自己的授权实例账户')
    const aiPerformanceRangeQuery = `startDate=${seed.usageToday}&endDate=${seed.usageToday}`
    const userAAiPerformance = await getEnvelope<AiPerformanceOverview>(baseUrl, `/__aisys__/api/my-stats/ai-performance?${aiPerformanceRangeQuery}&accountIds=${seed.userAAuthorizedUserBAccountId}`, seed.userACookie)
    assert(userAAiPerformance.accounts.some((account) => account.id === seed.userAAuthorizedUserBAccountId), 'AI性能监控选中参数应能加入当前用户自己的授权实例')
    assert(userAAiPerformance.summary.requestCount === 3, `AI性能监控用户 A 摘要应包含自有和授权实例调用，实际 ${userAAiPerformance.summary.requestCount}`)
    const userBAiPerformance = await getEnvelope<AiPerformanceOverview>(baseUrl, `/__aisys__/api/my-stats/ai-performance?${aiPerformanceRangeQuery}`, seed.userBCookie)
    assert(userBAiPerformance.accounts.some((account) => account.id === seed.userBAccountId), 'AI性能监控拥有者应能看到自己的账户')
    assert(userBAiPerformance.summary.requestCount === 3, `AI性能监控归属人原账户不应混入授权实例调用，实际 ${userBAiPerformance.summary.requestCount}`)
    await assertForbidden(`${baseUrl}/__aisys__/api/my-stats/system-metrics`, seed.userACookie, '用户侧统计命名空间里的系统指标仍是管理员能力，普通用户不可访问')
    summary.push('AI性能监控授权实例独立统计检查通过')

    const userATeamsPage = await getEnvelope<SystemTeamListResult>(baseUrl, '/__aisys__/api/my-teams', seed.userACookie)
    const userATeams = userATeamsPage.items
    assert(userATeams.length === 1 && userATeams[0]?.id === seed.teamSharedId, '用户 A 我的团队没有只返回自己加入的团队')
    assert((userATeams[0]?.members ?? []).some((member) => member.systemAccountId === seed.userAId), '用户 A 我的团队缺少自己')
    assert((userATeams[0]?.members ?? []).some((member) => member.systemAccountId === seed.userBId), '用户 A 我的团队缺少同团队成员')
    assert(!userATeams.some((team) => team.id === seed.teamUserBOnlyId), '用户 A 我的团队返回了未加入团队')
    const userBTeamsPage = await getEnvelope<SystemTeamListResult>(baseUrl, '/__aisys__/api/my-teams', seed.userBCookie)
    const userBTeams = userBTeamsPage.items
    assert(userBTeams.some((team) => team.id === seed.teamSharedId) && userBTeams.some((team) => team.id === seed.teamUserBOnlyId), '用户 B 我的团队没有返回自己加入的多个团队')
    const adminTeamsPage = await getEnvelope<SystemTeamListResult>(baseUrl, '/__aisys__/api/system-teams', seed.adminCookie)
    const adminTeams = adminTeamsPage.items
    assert(adminTeams.some((team) => team.id === seed.teamSharedId) && adminTeams.some((team) => team.id === seed.teamUserBOnlyId), '管理员系统团队管理没有返回全量团队')
    const userBOnlyTeam = adminTeams.find((team) => team.id === seed.teamUserBOnlyId)
    const userBOnlyMemberId = userBOnlyTeam?.members?.find((member) => member.systemAccountId === seed.userBId)?.id
    assert(userBOnlyMemberId, '回归需要用户 B 专属团队成员 ID')
    await assertForbiddenOrNotFound(`${baseUrl}/__aisys__/api/system-teams/${seed.teamUserBOnlyId}?systemAccountId=${seed.userAId}`, seed.adminCookie, 'PATCH', { name: '不应跨作用域更新团队' }, '管理员按用户 A 作用域写入时不应能更新用户 A 不可见团队')
    await assertJsonStatus(`${baseUrl}/__aisys__/api/system-teams/${seed.teamUserBOnlyId}/members?systemAccountId=${seed.userAId}`, seed.adminCookie, 'POST', { systemAccountIds: [seed.userCId] }, 404, '管理员按用户 A 作用域写入时不应能向用户 A 不可见团队添加成员')
    await assertForbiddenOrNotFound(`${baseUrl}/__aisys__/api/system-teams/${seed.teamUserBOnlyId}/members/${userBOnlyMemberId}?systemAccountId=${seed.userAId}`, seed.adminCookie, 'DELETE', {}, '管理员按用户 A 作用域写入时不应能移除用户 A 不可见团队成员')
    const scopedWriteBlockedTeam = repositories.findSystemTeamSummary(seed.teamUserBOnlyId)
    assert(scopedWriteBlockedTeam?.name !== '不应跨作用域更新团队', '系统团队 scoped 写入拦截失败：团队名称被跨作用域更新')
    assert((scopedWriteBlockedTeam?.members ?? []).every((member) => member.systemAccountId !== seed.userCId), '系统团队 scoped 写入拦截失败：用户 A 不可见团队被添加成员')
    assert((scopedWriteBlockedTeam?.members ?? []).some((member) => member.id === userBOnlyMemberId && member.status === 'active'), '系统团队 scoped 写入拦截失败：用户 A 不可见团队成员被移除')
    summary.push('我的团队成员作用域检查通过')

    const userAGranteeAccounts = await getEnvelope<SystemAccountPrincipalSummary[]>(baseUrl, '/__aisys__/api/my-authorization-options/grantee-accounts', seed.userACookie)
    assert(userAGranteeAccounts.some((account) => account.id === seed.userAId), '授权候选用户应包含当前用户，前端负责阻止授权给自己')
    assert(userAGranteeAccounts.some((account) => account.id === seed.userBId), '授权候选用户应包含同团队用户')
    assert(userAGranteeAccounts.some((account) => account.id === seed.userCId), '授权候选用户应包含非同团队用户')
    const userAGranteeTeams = await getEnvelope<SystemTeamPrincipalSummary[]>(baseUrl, '/__aisys__/api/my-authorization-options/grantee-teams', seed.userACookie)
    assert(userAGranteeTeams.some((team) => team.id === seed.teamSharedId), '授权候选团队应包含当前用户加入的团队')
    assert(userAGranteeTeams.some((team) => team.id === seed.teamUserBOnlyId), '授权候选团队应包含当前用户未加入但同团队成员加入的团队')
    assert(userAGranteeTeams.some((team) => team.id === seed.teamNoUserAId), '授权候选团队应包含当前用户完全无关的系统团队')
    assert(userAGranteeTeams.every((team) => !Object.prototype.hasOwnProperty.call(team, 'members')), '授权候选团队不应返回成员明细')
    const userAGranteeGroups = await getEnvelope<GroupSummary[]>(baseUrl, `/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=${seed.userAId}&providerCode=gpt&preferDefault=true`, seed.userBCookie)
    assert(userAGranteeGroups.some((group) => group.id === seed.userATargetGroupId), '授权目标分组选项应返回被授权用户自己的同供应商分组')
    assert(userAGranteeGroups.every((group) => group.ownerSystemAccountId === seed.userAId), '授权目标分组选项不应混入其他用户分组')
    const selectedBinding = databaseModule.getBusinessDatabase()
      .prepare('SELECT group_id, account_authorization_id FROM group_accounts WHERE account_id = ? AND system_account_id = ? AND enabled = 1 LIMIT 1')
      .get(seed.userAAuthorizedUserBAccountId, seed.userAId) as unknown as { group_id?: string; account_authorization_id?: string | null } | undefined
    assert(selectedBinding?.group_id === seed.userATargetGroupId, '新增授权指定目标分组后应直接绑定到该分组')
    assert(selectedBinding.account_authorization_id === seed.inboundRuntimeAuthorizationId, '授权实例分组绑定应记录对应的用户级授权 ID')
    summary.push('授权候选用户、团队和目标分组选项检查通过')

    const userAAuthorizationsPage = await getEnvelope<ResourceAuthorizationListResult>(baseUrl, `/__aisys__/api/my-authorizations?status=all&systemAccountId=${seed.userBId}`, seed.userACookie)
    assert(userAAuthorizationsPage.page === 1 && userAAuthorizationsPage.pageSize > 0, '用户 A 我的授权默认入口应返回分页结果')
    const userAAuthorizations = userAAuthorizationsPage.items
    const inboundAuthorization = userAAuthorizations.find((authorization) => authorization.id === seed.inboundAuthorizationId)
    const teamInboundAuthorization = userAAuthorizations.find((authorization) => authorization.id === seed.teamInboundAuthorizationId)
    assert(inboundAuthorization?.resourceOwnerSystemAccountId === seed.userBId && inboundAuthorization.granteeSystemAccountId === seed.userAId, '用户 A 我的授权没有返回入站授权')
    assert(teamInboundAuthorization?.resourceOwnerSystemAccountId === seed.userBId && teamInboundAuthorization.granteeTeamId === seed.teamSharedId, '用户 A 我的授权没有返回团队入站授权')
    assert(inboundAuthorization.permissions?.canEdit === false && inboundAuthorization.permissions.canAuthorize === false, '入站授权不应允许普通用户管理')
    assert(teamInboundAuthorization.permissions?.canEdit === false && teamInboundAuthorization.permissions.canAuthorize === false, '团队入站授权不应允许普通用户管理')
    const userAAuthorizationPage1 = await getEnvelope<ResourceAuthorizationListResult>(baseUrl, '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=1', seed.userACookie)
    assert(userAAuthorizationPage1.items.length === 1 && userAAuthorizationPage1.page === 1 && userAAuthorizationPage1.pageSize === 1, '用户 A 我的授权分页第一页异常')
    assert(userAAuthorizationPage1.hasMore === true && userAAuthorizationPage1.total >= 2, '用户 A 我的授权分页应提示还有更多')
    const userAInboundAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, '/__aisys__/api/my-authorizations?status=all&direction=inbound', seed.userACookie)).items
    assert(userAInboundAuthorizations.some((authorization) => authorization.id === seed.inboundAuthorizationId), '用户 A 我的授权入站筛选没有返回授权给我的记录')
    assert(userAInboundAuthorizations.some((authorization) => authorization.id === seed.teamInboundAuthorizationId), '用户 A 我的授权入站筛选没有返回团队授权记录')
    assert(userAInboundAuthorizations.every((authorization) => authorization.granteeSystemAccountId === seed.userAId || authorization.granteeTeamId === seed.teamSharedId), '用户 A 我的授权入站筛选返回了非当前用户被授权记录')
    const userAOutboundAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, '/__aisys__/api/my-authorizations?status=all&direction=outbound', seed.userACookie)).items
    assert(!userAOutboundAuthorizations.some((authorization) => authorization.id === seed.inboundAuthorizationId), '用户 A 我的授权出站筛选不应返回授权给我的记录')
    assert(userAOutboundAuthorizations.every((authorization) => authorization.resourceOwnerSystemAccountId === seed.userAId), '用户 A 我的授权出站筛选返回了非当前用户资源授权')
    const userAManualAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, '/__aisys__/api/my-authorizations?status=all&sourceType=manual', seed.userACookie)).items
    assert(userAManualAuthorizations.some((authorization) => authorization.id === seed.inboundAuthorizationId), '用户 A 我的授权手动来源筛选没有返回个人授权记录')
    assert(!userAManualAuthorizations.some((authorization) => authorization.id === seed.teamInboundAuthorizationId), '用户 A 我的授权手动来源筛选不应返回团队授权记录')
    const userATeamAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, '/__aisys__/api/my-authorizations?status=all&sourceType=team', seed.userACookie)).items
    assert(userATeamAuthorizations.some((authorization) => authorization.id === seed.teamInboundAuthorizationId), '用户 A 我的授权团队来源筛选没有返回团队授权记录')
    assert(userATeamAuthorizations.every((authorization) => authorization.granteeTeamId === seed.teamSharedId), '用户 A 我的授权团队来源筛选返回了非目标团队授权')
    await getEnvelope<ResourceAuthorizationSummary>(baseUrl, `/__aisys__/api/my-authorizations/${seed.inboundAuthorizationId}/usage?systemAccountId=${seed.userBId}`, seed.userACookie)
    await assertForbiddenOrNotFound(`${baseUrl}/__aisys__/api/my-authorizations/${seed.inboundAuthorizationId}`, seed.userACookie, 'PATCH', { status: 'paused' }, '入站授权不应允许普通用户暂停')
    await assertForbiddenOrNotFound(`${baseUrl}/__aisys__/api/my-authorizations/${seed.inboundAuthorizationId}`, seed.userACookie, 'DELETE', { sourceType: 'manual' }, '入站授权不应允许普通用户回收')
    const adminAuthorization = await getEnvelope<ResourceAuthorizationSummary>(baseUrl, `/__aisys__/api/authorizations/${seed.inboundAuthorizationId}/usage`, seed.adminCookie)
    assert(adminAuthorization.permissions?.canEdit === true, '管理员统一授权管理应保留管理能力')
    const adminTeamAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, `/__aisys__/api/authorizations?status=all&systemAccountId=${seed.userBId}&sourceType=team`, seed.adminCookie)).items
    assert(adminTeamAuthorizations.some((authorization) => authorization.id === seed.teamInboundAuthorizationId), '管理员统一授权团队来源筛选没有返回团队授权记录')
    const adminUserBResourceOwnerAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, `/__aisys__/api/authorizations?status=all&resourceOwnerSystemAccountId=${seed.userBId}`, seed.adminCookie)).items
    assert(adminUserBResourceOwnerAuthorizations.length >= 2, '管理员统一授权资源归属用户筛选应返回用户 B 授权记录')
    assert(adminUserBResourceOwnerAuthorizations.every((authorization) => authorization.resourceOwnerSystemAccountId === seed.userBId), '管理员统一授权资源归属用户筛选不应混入其他所有者授权')
    const adminUserAVisibleAuthorizations = (await getEnvelope<ResourceAuthorizationListResult>(baseUrl, `/__aisys__/api/authorizations?status=all&systemAccountId=${seed.userAId}`, seed.adminCookie)).items
    assert(adminUserAVisibleAuthorizations.some((authorization) => authorization.resourceOwnerSystemAccountId === seed.userBId), '管理员统一授权 systemAccountId 作用域应保留被授权给用户 A 的入站记录')
    assert(adminUserAVisibleAuthorizations.some((authorization) => authorization.resourceOwnerSystemAccountId === seed.userAId), '管理员统一授权 systemAccountId 作用域应保留用户 A 自己授权出去的记录')
    const adminAuthorizationPage1 = await getEnvelope<ResourceAuthorizationListResult>(baseUrl, `/__aisys__/api/authorizations?status=all&systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.adminCookie)
    assert(adminAuthorizationPage1.items.length === 1 && adminAuthorizationPage1.hasMore === true && adminAuthorizationPage1.total >= 2, '管理员统一授权分页异常')
    summary.push('授权方向作用域检查通过')

    console.log(`作用域边界回归通过：${summary.join('，')}`)
  } finally {
    await closeServer(server)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function seedData(): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const userA = repositories.createSystemAccount({
    username: 'scope_user_a',
    displayName: '作用域用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'scope_user_b',
    displayName: '作用域用户B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userC = repositories.createSystemAccount({
    username: 'scope_user_c',
    displayName: '作用域用户C',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })

  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
  const userBAccess = { systemAccountId: userB.id, role: 'user' as const }
  const userBProxy = repositories.createProxy({
    name: '用户 B 授权账户代理',
    type: 'http',
    host: '127.0.0.1',
    port: 9,
    enabled: true
  }, userBAccess)
  const userBGroup = repositories.createGroup({
    name: '用户 B 自建分组',
    providerCode: 'gpt'
  }, userBAccess)
  const userATargetGroup = repositories.createGroup({
    name: '指定授权目标分组',
    providerCode: 'gpt'
  }, userAAccess)
  const userAAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '用户 A 账户',
    type: 'api_key',
    groupId: userATargetGroup.id,
    credentials: { api_key: 'sk-scope-user-a', base_url: 'https://api.openai.com/v1' }
  }, userAAccess)
  assert(
    repositories.clearAccountFailureStateResult(userAAccount.id, userAAccess, { allowPendingTestRestore: true }).account?.status === 'active',
    '作用域回归种子应模拟用户 A 账户测试成功'
  )
  const userBErrorHandlingRules = [{
    enabled: true,
    name: '授权来源 429 限流',
    priority: 10,
    status_codes: [429],
    action: 'rate_limited',
    reset_strategy: 'duration',
    duration_hours: 2
  }]
  const userBStreamInterceptRules = [{
    enabled: true,
    name: '授权来源流式异常',
    priority: 20,
    match: {
      errorCodes: ['upstream_scope_error']
    },
    action: 'retry_next_account',
    notes: '授权详情公开展示用'
  }]
  const userBAvailabilitySchedule = {
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [{
      daysOfWeek: [1, 2, 3, 4, 5, 6, 7],
      start: '00:00',
      end: '23:59'
    }],
    exceptions: [{
      date: '2027-01-01',
      action: 'deny'
    }]
  }
  const userBAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: '用户 B 账户',
    type: 'api_key',
    groupId: userBGroup.id,
    credentials: {
      api_key: 'sk-scope-user-b',
      base_url: 'https://api.openai.com/v1',
      error_handling_rules: userBErrorHandlingRules,
      stream_intercept_rules: userBStreamInterceptRules
    },
    concurrencyLimit: 3,
    supportedModels: ['gpt-5.5'],
    proxyProfileId: userBProxy.id,
    accountExpiresAt: '2027-12-31T00:00:00.000Z',
    availabilitySchedule: userBAvailabilitySchedule
  }, userBAccess)
  assert(
    repositories.clearAccountFailureStateResult(userBAccount.id, userBAccess, { allowPendingTestRestore: true }).account?.status === 'active',
    '作用域回归种子应模拟用户 B 账户测试成功后再创建授权'
  )
  repositories.createAccount({
    providerCode: 'gpt',
    name: 'Scope Extra OAuth',
    type: 'oauth',
    groupId: userBGroup.id,
    credentials: { refresh_token: 'refresh-scope-user-b-extra', base_url: 'https://api.openai.com/v1' }
  }, userBAccess)
  const teamShared = repositories.createSystemTeam({
    name: '作用域共享团队',
    description: '用户 A 和用户 B 都在此团队'
  }, { systemAccountId: admin.id, role: 'admin' as const })
  repositories.addSystemTeamMembers(teamShared.id, { systemAccountIds: [userA.id, userB.id] }, { systemAccountId: admin.id, role: 'admin' as const })
  const teamUserBOnly = repositories.createSystemTeam({
    name: '作用域用户 B 团队',
    description: '只有用户 B 加入'
  }, { systemAccountId: admin.id, role: 'admin' as const })
  repositories.addSystemTeamMembers(teamUserBOnly.id, { systemAccountIds: [userB.id] }, { systemAccountId: admin.id, role: 'admin' as const })
  const teamNoUserA = repositories.createSystemTeam({
    name: '作用域无用户 A 团队',
    description: '用户 A 不在此团队'
  }, { systemAccountId: admin.id, role: 'admin' as const })
  repositories.addSystemTeamMembers(teamNoUserA.id, { systemAccountIds: [userC.id] }, { systemAccountId: admin.id, role: 'admin' as const })
  const inboundAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: userBAccount.id,
    granteeType: 'system_account',
    granteeId: userA.id,
    targetGroupId: userATargetGroup.id,
    remark: '用户 B 授权给用户 A 的账户'
  }, userBAccess)
  const userAAuthorizedUserBAccount = repositories.listAccounts(userAAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === userBAccount.id)
  assert(userAAuthorizedUserBAccount?.id, '账户授权应为用户 A 创建独立授权实例账户')
  assert(userAAuthorizedUserBAccount.accountAuthorizationId, '授权实例账户应绑定用户 A 的运行时授权 ID')
  const inboundRuntimeAuthorizationId = userAAuthorizedUserBAccount.accountAuthorizationId
  assert(userAAuthorizedUserBAccount.boundGroupId === userATargetGroup.id, '账户授权指定目标分组后应绑定到用户 A 的目标分组')
  assert(userAAuthorizedUserBAccount.proxyProfileId === userBProxy.id, '授权实例账户列表应从来源账户补齐代理配置')
  const teamInboundAuthorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: userBGroup.id,
    granteeType: 'team',
    granteeId: teamShared.id,
    remark: '用户 B 授权给共享团队的分组'
  }, userBAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: userATargetGroup.id,
    granteeType: 'system_account',
    granteeId: userC.id,
    remark: '用户 A 授权给用户 C 的分组'
  }, userAAccess)
  const userAOwnedGroup = repositories.listGroups(userAAccess).find((group) => group.ownerSystemAccountId === userA.id)?.id
  assert(userAOwnedGroup, '用户 A 应存在可绑定的自有分组')
  const authorizedGroupApiKey = repositories.createApiKeyRecord({
    name: '用户 A 授权分组 Key',
    groupBindings: [{ groupId: userBGroup.id, priority: 1, status: 'active' }],
  }, userAAccess)
  assert(authorizedGroupApiKey.groupBindings[0]?.groupId === userBGroup.id, 'API Key 应允许绑定有效授权给当前用户的分组')
  const runtimeGroupAuthorization = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' LIMIT 1")
    .get(userBGroup.id, userA.id) as unknown as { id?: string } | undefined
  assert(runtimeGroupAuthorization?.id, '共享团队分组授权应生成用户 A 的运行时授权')
  repositories.createApiKeyRecord({
    name: '用户 A Key',
    groupBindings: [{ groupId: userAOwnedGroup, priority: 1, status: 'active' }],
  }, userAAccess)
  const usageToday = localDateKey(addDays(new Date(), -1))
  const usageYesterday = localDateKey(addDays(new Date(), -2))
  repositories.createUsageRecordsBatch([
    usageRecord('scope_usage_a_1', userA.id, userAAccount.id, 'GET /v1/models', 'scope-model-a', 200, true, usageAt(usageToday, 1)),
    usageRecord('scope_usage_a_2', userA.id, userAAccount.id, 'POST /v1/responses', 'scope-model-a', 500, false, usageAt(usageToday, 2)),
    usageRecord('scope_usage_a_authorized_b_1', userA.id, userAAuthorizedUserBAccount.id, 'POST /v1/responses', 'scope-model-authorized', 200, true, usageAt(usageToday, 3), userATargetGroup.id),
    usageRecord('scope_usage_b_1', userB.id, userBAccount.id, 'GET /v1/models', 'scope-model-b', 200, true, usageAt(usageToday, 4), userBGroup.id),
    usageRecord('scope_usage_b_2', userB.id, userBAccount.id, 'POST /v1/responses', 'scope-model-b', 429, false, usageAt(usageToday, 5), userBGroup.id),
    usageRecord('scope_usage_b_3', userB.id, userBAccount.id, 'POST /v1/responses', 'scope-model-c', 200, true, usageAt(usageToday, 6), userBGroup.id)
  ])
  while (usageStatsRepository.aggregateUsageStatsBatch(1000) > 0) {}
  usageStatsRepository.refreshUsageRankSnapshots()

  return {
    adminId: admin.id,
    userAId: userA.id,
    userBId: userB.id,
    userCId: userC.id,
    adminCookie: sessionCookie(admin.id),
    userACookie: sessionCookie(userA.id),
    userBCookie: sessionCookie(userB.id),
    userAAccountId: userAAccount.id,
    userBAccountId: userBAccount.id,
    userAAuthorizedUserBAccountId: userAAuthorizedUserBAccount.id,
    userAAuthorizedUserBProxyId: userAAuthorizedUserBAccount.proxyProfileId,
    userBProxyId: userBProxy.id,
    teamSharedId: teamShared.id,
    teamUserBOnlyId: teamUserBOnly.id,
    teamNoUserAId: teamNoUserA.id,
    userATargetGroupId: userATargetGroup.id,
    inboundAuthorizationId: inboundAuthorization.id,
    inboundRuntimeAuthorizationId,
    teamInboundAuthorizationId: teamInboundAuthorization.id,
    userBGroupId: userBGroup.id,
    usageToday,
    usageYesterday
  }
}

function usageAt(dateKey: string, offsetSeconds: number): string {
  const [year, month, day] = dateKey.split('-').map((part) => Number(part))
  return new Date(year, month - 1, day, 1, 0, offsetSeconds).toISOString()
}

function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function addDays(date: Date, days: number): Date {
  const next = new Date(date)
  next.setDate(next.getDate() + days)
  return next
}

function usageStatsTimezoneSetting(): string {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT value_json FROM system_settings WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone'")
    .get() as unknown as { value_json?: string } | undefined
  if (!row?.value_json) return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  try {
    const value = JSON.parse(row.value_json) as unknown
    return typeof value === 'string' && value.trim() ? value.trim() : Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  }
}

function setUsageStatsTimezoneSetting(timezone: string): void {
  databaseModule.getBusinessDatabase()
    .prepare(`
      INSERT INTO system_settings (system_account_id, key, value_json, updated_at)
      VALUES ('sys_admin', 'usageStatsTimezone', ?, ?)
      ON CONFLICT(system_account_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at
    `)
    .run(JSON.stringify(timezone), new Date().toISOString())
  usageStatsHelpers.clearUsageStatsTimezoneCache()
}

function usageRecord(
  id: string,
  systemAccountId: string,
  accountId: string,
  endpoint: string,
  model: string,
  statusCode: number,
  success: boolean,
  createdAt: string,
  groupId?: string
) {
  return {
    id,
    systemAccountId,
    traceId: `${id}_trace`,
    trafficSource: 'gateway' as const,
    clientIp: '127.0.0.1',
    groupId,
    accountId,
    endpoint,
    providerCode: 'gpt',
    model,
    stream: false,
    statusCode,
    success,
    firstTokenMs: success ? 120 : undefined,
    durationMs: success ? 360 : 90,
    inputTokens: success ? 10 : undefined,
    outputTokens: success ? 4 : undefined,
    costUsd: success ? 0.00001 : undefined,
    errorCode: success ? undefined : 'scope_regression_error',
    errorMessage: success ? undefined : '作用域回归模拟失败请求',
    createdAt
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetchRegression(`${baseUrl}${path}`, { headers: { cookie } }, path)
  return unwrapEnvelope<T>(response, path)
}

async function getAccountItems(baseUrl: string, path: string, cookie: string): Promise<AccountSummary[]> {
  return (await getEnvelope<AccountListResult>(baseUrl, path, cookie)).items
}

async function getApiKeyItems(baseUrl: string, path: string, cookie: string): Promise<ApiKeySummary[]> {
  return (await getEnvelope<ApiKeyListResult>(baseUrl, path, cookie)).items
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetchRegression(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  }, path)
  return unwrapEnvelope<T>(response, path)
}

async function unwrapEnvelope<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function assertForbidden(path: string, cookie: string, message: string): Promise<void> {
  await assertStatus(path, cookie, 403, message)
}

async function assertStatus(path: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetchRegression(path, { headers: { cookie } })
  assert(response.status === expectedStatus, `${message}，实际状态 ${response.status}: ${await response.text()}`)
}

async function assertJsonStatus(path: string, cookie: string, method: 'POST', body: unknown, expectedStatus: number, message: string): Promise<void> {
  const response = await fetchRegression(path, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  assert(response.status === expectedStatus, `${message}，实际状态 ${response.status}: ${await response.text()}`)
}

async function assertForbiddenOrNotFound(path: string, cookie: string, method: 'PATCH' | 'DELETE', body: unknown, message: string): Promise<void> {
  const response = await fetchRegression(path, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  assert(response.status === 403 || response.status === 404, `${message}，实际状态 ${response.status}: ${await response.text()}`)
}

async function fetchRegression(url: string, init: RequestInit = {}, label = url): Promise<Response> {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), REGRESSION_FETCH_TIMEOUT_MS)
  try {
    return await fetch(url, { ...init, signal: controller.signal })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') {
      throw new Error(`${label} 请求超过 ${REGRESSION_FETCH_TIMEOUT_MS}ms`)
    }
    throw error
  } finally {
    clearTimeout(timeout)
  }
}

function assertSameIds(left: Array<{ id: string }>, right: Array<{ id: string }>, message: string): void {
  const leftIds = left.map((item) => item.id).sort().join(',')
  const rightIds = right.map((item) => item.id).sort().join(',')
  assert(leftIds === rightIds, `${message}，实际 ${leftIds || '<empty>'}，期望 ${rightIds || '<empty>'}`)
}

async function onceListening(server: ReturnType<typeof app.listen>): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

async function closeServer(server?: ReturnType<typeof app.listen>): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      server.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    server.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    server.closeIdleConnections?.()
  })
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\n作用域边界回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
