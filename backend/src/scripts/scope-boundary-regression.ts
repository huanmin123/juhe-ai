import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../config/runtime.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-scope-regression-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'scope-regression.sqlite3')
runtimeConfig.secret = 'scope-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })

const [
  { accountsRouter },
  { apiKeysRouter },
  { authorizationsRouter },
  { groupsRouter },
  { statsRouter },
  { myTeamsRouter, systemTeamsRouter },
  { usageRecordsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../modules/accounts/accounts.routes.js'),
  import('../modules/api-keys/api-keys.routes.js'),
  import('../modules/authorizations/authorizations.routes.js'),
  import('../modules/groups/groups.routes.js'),
  import('../modules/stats/stats.routes.js'),
  import('../modules/system-teams/system-teams.routes.js'),
  import('../modules/usage-records/usage-records.routes.js'),
  import('../modules/auth/auth.middleware.js'),
  import('../shared/request-context.js'),
  import('../storage/database.js'),
  import('../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/api', requireAuth)
app.use('/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/api/my-api-keys', forceSelfAccessScope, apiKeysRouter)
app.use('/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/api/my-usage-records', forceSelfAccessScope, usageRecordsRouter)
app.use('/api/my-stats', forceSelfAccessScope, statsRouter)
app.use('/api/my-teams', forceSelfAccessScope, myTeamsRouter)
app.use('/api/accounts', requireAdmin, accountsRouter)
app.use('/api/groups', requireAdmin, groupsRouter)
app.use('/api/api-keys', requireAdmin, apiKeysRouter)
app.use('/api/authorizations', requireAdmin, authorizationsRouter)
app.use('/api/usage-records', requireAdmin, usageRecordsRouter)
app.use('/api/stats', requireAdmin, statsRouter)
app.use('/api/system-teams', systemTeamsRouter)

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
}

interface AccountListResult {
  items: AccountSummary[]
  total: number
  page: number
  pageSize: number
}

interface GroupSummary {
  id: string
  ownerSystemAccountId?: string
  systemAccountId?: string
  name: string
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
  model?: string
  statusCode?: number
  success: boolean
  createdAt: string
}

interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  page: number
  pageSize: number
}

interface AccountUsageStatsRow {
  id: string
  systemAccountId?: string
  ownerSystemAccountId: string
  name: string
  type: string
}

interface AccountUsageStatsOverview {
  rows: AccountUsageStatsRow[]
  total: number
  page: number
  pageSize: number
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

interface ResourceAuthorizationSummary {
  id: string
  resourceType: string
  resourceId: string
  resourceOwnerSystemAccountId: string
  granteeSystemAccountId: string
  status: string
  permissions?: {
    canEdit: boolean
    canAuthorize: boolean
  }
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
  teamSharedId: string
  teamUserBOnlyId: string
  inboundAuthorizationId: string
  userBGroupId: string
}

async function main(): Promise<void> {
  try {
    const server = app.listen(0, '127.0.0.1')
    await onceListening(server)
    const address = server.address()
    if (!address || typeof address === 'string') {
      throw new Error('scope regression server address unavailable')
    }
    const baseUrl = `http://127.0.0.1:${address.port}`
    const seed = seedData()
    const summary: string[] = []

    await assertForbidden(`${baseUrl}/api/accounts`, seed.userACookie, '普通用户不能访问管理账户接口')
    summary.push('admin gate ok')

    await assertForbidden(`${baseUrl}/api/system-teams`, seed.userACookie, '普通用户不能访问系统团队管理接口')
    await assertForbidden(`${baseUrl}/api/authorizations`, seed.userACookie, '普通用户不能访问统一授权管理接口')
    summary.push('admin-only menu api gate ok')

    const userAMyAccounts = await getAccountItems(baseUrl, '/api/my-accounts', seed.userACookie)
    assertSameIds(userAMyAccounts, [{ id: seed.userAAccountId }, { id: seed.userBAccountId }], '用户 A 的 my-accounts 未返回自有账户和授权给自己的账户')
    assert(userAMyAccounts.some((account) => account.id === seed.userAAccountId && account.ownerSystemAccountId === seed.userAId), '用户 A 的 my-accounts 缺少自有账户')
    assert(userAMyAccounts.some((account) => account.id === seed.userBAccountId && account.ownerSystemAccountId === seed.userBId && account.accessType === 'authorized'), '用户 A 的 my-accounts 缺少授权给自己的账户')
    const userAMyAccountsWithQuery = await getAccountItems(baseUrl, `/api/my-accounts?systemAccountId=${seed.userBId}`, seed.userACookie)
    assertSameIds(userAMyAccounts, userAMyAccountsWithQuery, '用户 A 传 systemAccountId 后 my-accounts 结果发生变化')
    summary.push('my accounts self scope ok')

    const adminMyAccounts = await getAccountItems(baseUrl, `/api/my-accounts?systemAccountId=${seed.userBId}`, seed.adminCookie)
    assert(adminMyAccounts.every((account) => account.ownerSystemAccountId === seed.adminId), '管理员 my-accounts 没有固定为管理员自己的用户侧作用域')
    summary.push('admin self menu scope ok')

    const adminUserBAccounts = await getAccountItems(baseUrl, `/api/accounts?systemAccountId=${seed.userBId}`, seed.adminCookie)
    assert(adminUserBAccounts.every((account) => account.ownerSystemAccountId === seed.userBId), '管理账户接口按用户 B 筛选失败')
    summary.push('admin account filter ok')

    const userBAccountPage1 = await getEnvelope<AccountListResult>(baseUrl, `/api/accounts?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBAccountPage1.total === 1 && userBAccountPage1.items.length === 1 && userBAccountPage1.pageSize === 1, '管理账户分页或关键词筛选异常')
    const userBDisabledAccounts = await getEnvelope<AccountListResult>(baseUrl, `/api/accounts?systemAccountId=${seed.userBId}&status=disabled&page=1&pageSize=10`, seed.adminCookie)
    assert(userBDisabledAccounts.total === 0, '管理账户状态筛选异常')
    const userAApiKeyAccounts = await getEnvelope<AccountListResult>(baseUrl, '/api/my-accounts?type=api_key&page=1&pageSize=10', seed.userACookie)
    assert(userAApiKeyAccounts.total === userAMyAccounts.length && userAApiKeyAccounts.items.every((account) => account.type === 'api_key'), '用户侧账户类型筛选异常')
    summary.push('account pagination filter ok')

    const createdGroup = await postEnvelope<GroupSummary>(baseUrl, `/api/groups?systemAccountId=${seed.userBId}`, seed.adminCookie, {
      name: '用户 B 管理代建分组',
      providerCode: 'openai'
    })
    assert(createdGroup.systemAccountId === seed.userBId, '管理员按用户 B 创建分组没有归属到用户 B')
    summary.push('admin create group target ok')

    const createdApiKey = await postEnvelope<ApiKeySummary>(baseUrl, `/api/api-keys?systemAccountId=${seed.userBId}`, seed.adminCookie, {
      name: '用户 B 管理代建 Key',
      groupId: seed.userBGroupId
    })
    assert(createdApiKey.systemAccountId === seed.userBId, '管理员按用户 B 创建 API Key 没有归属到用户 B')
    summary.push('admin create api key target ok')

    const userAKeys = await getApiKeyItems(baseUrl, `/api/my-api-keys?systemAccountId=${seed.userBId}`, seed.userACookie)
    assert(userAKeys.every((item) => item.systemAccountId === undefined || item.systemAccountId === seed.userAId), '用户 A 的 my-api-keys 返回了其他用户密钥')
    summary.push('my api keys self scope ok')

    const userBKeyPage = await getEnvelope<ApiKeyListResult>(baseUrl, `/api/api-keys?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBKeyPage.total >= 1 && userBKeyPage.items.length === 1 && userBKeyPage.items[0]?.systemAccountId === seed.userBId, '管理 API Key 分页或关键词筛选异常')
    const activeUserBKeys = await getEnvelope<ApiKeyListResult>(baseUrl, `/api/api-keys?systemAccountId=${seed.userBId}&status=active&groupId=${seed.userBGroupId}`, seed.adminCookie)
    assert(activeUserBKeys.items.every((item) => item.status === 'active' && item.groupId === seed.userBGroupId), '管理 API Key 状态或分组筛选异常')
    summary.push('api key pagination filter ok')

    const userAUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/api/my-usage-records?systemAccountId=${seed.userBId}&page=1&pageSize=2`, seed.userACookie)
    assert(userAUsage.total === 2, `用户 A 使用记录总数异常：${userAUsage.total}`)
    assert(userAUsage.items.length === 2, `用户 A 使用记录分页数量异常：${userAUsage.items.length}`)
    assert(userAUsage.items.every((record) => record.systemAccountId === undefined || record.systemAccountId === seed.userAId), '用户 A 的 my-usage-records 返回了其他用户记录')
    summary.push('my usage records self scope ok')

    const userBUsagePage1 = await getEnvelope<UsageRecordListResult>(baseUrl, `/api/usage-records?systemAccountId=${seed.userBId}&accountKeyword=${encodeURIComponent('用户 B')}&page=1&pageSize=2`, seed.adminCookie)
    assert(userBUsagePage1.total === 3, `管理使用记录按用户 B 查询总数异常：${userBUsagePage1.total}`)
    assert(userBUsagePage1.items.length === 2, `管理使用记录分页第一页数量异常：${userBUsagePage1.items.length}`)
    assert(userBUsagePage1.items.every((record) => record.systemAccountId === seed.userBId), '管理使用记录按用户 B 查询返回了其他用户记录')
    const userBUsagePage2 = await getEnvelope<UsageRecordListResult>(baseUrl, `/api/usage-records?systemAccountId=${seed.userBId}&accountKeyword=${encodeURIComponent('用户 B')}&page=2&pageSize=2`, seed.adminCookie)
    assert(userBUsagePage2.items.length === 1, `管理使用记录分页第二页数量异常：${userBUsagePage2.items.length}`)
    const failedUserBUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/api/usage-records?systemAccountId=${seed.userBId}&result=failed&statusCode=429&page=1&pageSize=10`, seed.adminCookie)
    assert(failedUserBUsage.total === 1 && failedUserBUsage.items[0]?.statusCode === 429 && failedUserBUsage.items[0]?.success === false, '管理使用记录失败状态码筛选异常')
    const modelFilteredUsage = await getEnvelope<UsageRecordListResult>(baseUrl, `/api/usage-records?systemAccountId=${seed.userBId}&model=${encodeURIComponent('scope-model-c')}&page=1&pageSize=10`, seed.adminCookie)
    assert(modelFilteredUsage.total === 1 && modelFilteredUsage.items[0]?.model === 'scope-model-c', '管理使用记录模型筛选异常')
    summary.push('usage records pagination filter ok')

    const userAAccountUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/my-stats/account-usage?systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.userACookie)
    assert(userAAccountUsage.total === userAMyAccounts.length, `用户 A 账号用量统计总数异常：${userAAccountUsage.total}`)
    assert(userAAccountUsage.rows.length === 1 && userAAccountUsage.pageSize === 1, '用户 A 账号用量统计分页异常')
    assert(userAAccountUsage.rows.every((row) => userAMyAccounts.some((account) => account.id === row.id)), '用户 A 的账号用量统计返回了不可见账户')
    const userAApiKeyUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, '/api/my-stats/account-usage?type=api_key&page=1&pageSize=10', seed.userACookie)
    assert(userAApiKeyUsage.total === userAMyAccounts.length && userAApiKeyUsage.rows.every((row) => row.type === 'api_key'), '用户侧账号用量统计类型筛选异常')
    const userAKeywordUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/my-stats/account-usage?keyword=${encodeURIComponent('用户 A')}&page=1&pageSize=10`, seed.userACookie)
    assert(userAKeywordUsage.total === 1 && userAKeywordUsage.rows[0]?.id === seed.userAAccountId, '用户侧账号用量统计关键词筛选异常')

    const userBAccountUsagePage1 = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/stats/account-usage?systemAccountId=${seed.userBId}&page=1&pageSize=1`, seed.adminCookie)
    assert(userBAccountUsagePage1.total === 2 && userBAccountUsagePage1.rows.length === 1 && userBAccountUsagePage1.pageSize === 1, '管理账号用量统计分页第一页异常')
    assert(userBAccountUsagePage1.rows.every((row) => row.ownerSystemAccountId === seed.userBId), '管理账号用量统计按用户 B 查询返回了其他用户账户')
    const userBAccountUsagePage2 = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/stats/account-usage?systemAccountId=${seed.userBId}&page=2&pageSize=1`, seed.adminCookie)
    assert(userBAccountUsagePage2.rows.length === 1 && userBAccountUsagePage2.rows[0]?.ownerSystemAccountId === seed.userBId, '管理账号用量统计分页第二页异常')
    const userBAccountUsageKeyword = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/stats/account-usage?systemAccountId=${seed.userBId}&keyword=${encodeURIComponent('用户 B')}&page=1&pageSize=10`, seed.adminCookie)
    assert(userBAccountUsageKeyword.total === 1 && userBAccountUsageKeyword.rows[0]?.name.includes('用户 B'), '管理账号用量统计关键词筛选异常')
    const userBOAuthUsage = await getEnvelope<AccountUsageStatsOverview>(baseUrl, `/api/stats/account-usage?systemAccountId=${seed.userBId}&type=oauth&page=1&pageSize=10`, seed.adminCookie)
    assert(userBOAuthUsage.total === 1 && userBOAuthUsage.rows[0]?.type === 'oauth', '管理账号用量统计类型筛选异常')
    summary.push('account usage stats pagination filter ok')

    const userATeams = await getEnvelope<SystemTeamSummary[]>(baseUrl, '/api/my-teams', seed.userACookie)
    assert(userATeams.length === 1 && userATeams[0]?.id === seed.teamSharedId, '用户 A 我的团队没有只返回自己加入的团队')
    assert((userATeams[0]?.members ?? []).some((member) => member.systemAccountId === seed.userAId), '用户 A 我的团队缺少自己')
    assert((userATeams[0]?.members ?? []).some((member) => member.systemAccountId === seed.userBId), '用户 A 我的团队缺少同团队成员')
    assert(!userATeams.some((team) => team.id === seed.teamUserBOnlyId), '用户 A 我的团队返回了未加入团队')
    const userBTeams = await getEnvelope<SystemTeamSummary[]>(baseUrl, '/api/my-teams', seed.userBCookie)
    assert(userBTeams.some((team) => team.id === seed.teamSharedId) && userBTeams.some((team) => team.id === seed.teamUserBOnlyId), '用户 B 我的团队没有返回自己加入的多个团队')
    const adminTeams = await getEnvelope<SystemTeamSummary[]>(baseUrl, '/api/system-teams', seed.adminCookie)
    assert(adminTeams.some((team) => team.id === seed.teamSharedId) && adminTeams.some((team) => team.id === seed.teamUserBOnlyId), '管理员系统团队管理没有返回全量团队')
    summary.push('my teams membership scope ok')

    const userAAuthorizations = await getEnvelope<ResourceAuthorizationSummary[]>(baseUrl, `/api/my-authorizations?status=all&systemAccountId=${seed.userBId}`, seed.userACookie)
    const inboundAuthorization = userAAuthorizations.find((authorization) => authorization.id === seed.inboundAuthorizationId)
    assert(inboundAuthorization?.resourceOwnerSystemAccountId === seed.userBId && inboundAuthorization.granteeSystemAccountId === seed.userAId, '用户 A 我的授权没有返回入站授权')
    assert(inboundAuthorization.permissions?.canEdit === false && inboundAuthorization.permissions.canAuthorize === false, '入站授权不应允许普通用户管理')
    const userAInboundAuthorizations = await getEnvelope<ResourceAuthorizationSummary[]>(baseUrl, '/api/my-authorizations?status=all&direction=inbound', seed.userACookie)
    assert(userAInboundAuthorizations.some((authorization) => authorization.id === seed.inboundAuthorizationId), '用户 A 我的授权入站筛选没有返回授权给我的记录')
    assert(userAInboundAuthorizations.every((authorization) => authorization.granteeSystemAccountId === seed.userAId), '用户 A 我的授权入站筛选返回了非当前用户被授权记录')
    const userAOutboundAuthorizations = await getEnvelope<ResourceAuthorizationSummary[]>(baseUrl, '/api/my-authorizations?status=all&direction=outbound', seed.userACookie)
    assert(!userAOutboundAuthorizations.some((authorization) => authorization.id === seed.inboundAuthorizationId), '用户 A 我的授权出站筛选不应返回授权给我的记录')
    assert(userAOutboundAuthorizations.every((authorization) => authorization.resourceOwnerSystemAccountId === seed.userAId), '用户 A 我的授权出站筛选返回了非当前用户资源授权')
    await getEnvelope<ResourceAuthorizationSummary>(baseUrl, `/api/my-authorizations/${seed.inboundAuthorizationId}/usage?systemAccountId=${seed.userBId}`, seed.userACookie)
    await assertForbiddenOrNotFound(`${baseUrl}/api/my-authorizations/${seed.inboundAuthorizationId}`, seed.userACookie, 'PATCH', { status: 'paused' }, '入站授权不应允许普通用户暂停')
    await assertForbiddenOrNotFound(`${baseUrl}/api/my-authorizations/${seed.inboundAuthorizationId}`, seed.userACookie, 'DELETE', { sourceType: 'manual' }, '入站授权不应允许普通用户回收')
    const adminAuthorization = await getEnvelope<ResourceAuthorizationSummary>(baseUrl, `/api/authorizations/${seed.inboundAuthorizationId}/usage`, seed.adminCookie)
    assert(adminAuthorization.permissions?.canEdit === true, '管理员统一授权管理应保留管理能力')
    summary.push('authorization direction scope ok')

    await new Promise<void>((resolvePromise, rejectPromise) => {
      server.close((error) => error ? rejectPromise(error) : resolvePromise())
    })

    console.log(`scope boundary regression ok: ${summary.join(', ')}`)
  } finally {
    try {
      databaseModule.getDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function seedData(): SeedState {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  const userA = repositories.createSystemAccount({
    username: 'scope_user_a',
    displayName: '作用域用户 A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'scope_user_b',
    displayName: '作用域用户 B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })

  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
  const userBAccess = { systemAccountId: userB.id, role: 'user' as const }
  const userAAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '用户 A 账户',
    type: 'api_key',
    credentials: { api_key: 'sk-scope-user-a', base_url: 'https://api.openai.com/v1' }
  }, userAAccess)
  const userBAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '用户 B 账户',
    type: 'api_key',
    credentials: { api_key: 'sk-scope-user-b', base_url: 'https://api.openai.com/v1' }
  }, userBAccess)
  repositories.createAccount({
    providerCode: 'openai',
    name: 'Scope Extra OAuth',
    type: 'oauth',
    credentials: { refresh_token: 'refresh-scope-user-b-extra', base_url: 'https://api.openai.com/v1' }
  }, userBAccess)
  const userBGroup = repositories.createGroup({
    name: '用户 B 自建分组',
    providerCode: 'openai'
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
  const inboundAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: userBAccount.id,
    granteeType: 'system_account',
    granteeId: userA.id,
    remark: '用户 B 授权给用户 A 的账户'
  }, userBAccess)
  repositories.createApiKeyRecord({
    name: '用户 A Key',
    groupId: repositories.listGroups(userAAccess).find((group) => group.ownerSystemAccountId === userA.id)?.id
  }, userAAccess)
  repositories.createUsageRecordsBatch([
    usageRecord('scope_usage_a_1', userA.id, userAAccount.id, 'GET /v1/models', 'scope-model-a', 200, true, '2026-05-07T00:00:01.000Z'),
    usageRecord('scope_usage_a_2', userA.id, userAAccount.id, 'POST /v1/responses', 'scope-model-a', 500, false, '2026-05-07T00:00:02.000Z'),
    usageRecord('scope_usage_b_1', userB.id, userBAccount.id, 'GET /v1/models', 'scope-model-b', 200, true, '2026-05-07T00:00:03.000Z'),
    usageRecord('scope_usage_b_2', userB.id, userBAccount.id, 'POST /v1/responses', 'scope-model-b', 429, false, '2026-05-07T00:00:04.000Z'),
    usageRecord('scope_usage_b_3', userB.id, userBAccount.id, 'POST /v1/responses', 'scope-model-c', 200, true, '2026-05-07T00:00:05.000Z')
  ])

  return {
    adminId: admin.id,
    userAId: userA.id,
    userBId: userB.id,
    adminCookie: sessionCookie(admin.id),
    userACookie: sessionCookie(userA.id),
    userBCookie: sessionCookie(userB.id),
    userAAccountId: userAAccount.id,
    userBAccountId: userBAccount.id,
    teamSharedId: teamShared.id,
    teamUserBOnlyId: teamUserBOnly.id,
    inboundAuthorizationId: inboundAuthorization.id,
    userBGroupId: userBGroup.id
  }
}

function usageRecord(
  id: string,
  systemAccountId: string,
  accountId: string,
  endpoint: string,
  model: string,
  statusCode: number,
  success: boolean,
  createdAt: string
) {
  return {
    id,
    systemAccountId,
    traceId: `${id}_trace`,
    clientIp: '127.0.0.1',
    accountId,
    endpoint,
    providerCode: 'openai',
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
    errorMessage: success ? undefined : 'scope regression failed request',
    createdAt
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  return unwrapEnvelope<T>(response, path)
}

async function getAccountItems(baseUrl: string, path: string, cookie: string): Promise<AccountSummary[]> {
  return (await getEnvelope<AccountListResult>(baseUrl, path, cookie)).items
}

async function getApiKeyItems(baseUrl: string, path: string, cookie: string): Promise<ApiKeySummary[]> {
  return (await getEnvelope<ApiKeyListResult>(baseUrl, path, cookie)).items
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
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
  const response = await fetch(path, { headers: { cookie } })
  assert(response.status === 403, `${message}，实际状态 ${response.status}`)
}

async function assertForbiddenOrNotFound(path: string, cookie: string, method: 'PATCH' | 'DELETE', body: unknown, message: string): Promise<void> {
  const response = await fetch(path, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  assert(response.status === 403 || response.status === 404, `${message}，实际状态 ${response.status}: ${await response.text()}`)
}

function assertSameIds(left: Array<{ id: string }>, right: Array<{ id: string }>, message: string): void {
  const leftIds = left.map((item) => item.id).sort().join(',')
  const rightIds = right.map((item) => item.id).sort().join(',')
  assert(leftIds === rightIds, message)
}

async function onceListening(server: ReturnType<typeof app.listen>): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\nscope boundary regression failed')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
