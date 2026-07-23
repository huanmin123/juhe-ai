import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountUsageStatsRange, ResourceAuthorizationResourceType } from '../../domain/types.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-usage-pagination-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-usage-pagination-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

assertAuthorizationUsageLookupSharedCacheBoundary()

const [databaseModule, repositories, authorizationUsageRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/authorization-usage.repository.js')
])

const range: AccountUsageStatsRange = {
  startDate: '2026-01-01',
  endDate: '2026-01-01',
  days: 1,
  maxDays: 31
}

try {
  const owner = repositories.createSystemAccount({
    username: 'authorization_usage_owner',
    displayName: '授权消耗所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeA = repositories.createSystemAccount({
    username: 'authorization_usage_grantee_a',
    displayName: '授权消耗用户A',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeB = repositories.createSystemAccount({
    username: 'authorization_usage_grantee_b',
    displayName: '授权消耗用户B',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const adminOwnerAccess = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: owner.id }
  const granteeAAccess = { systemAccountId: granteeA.id, role: 'user' as const }

  const teamA = repositories.createSystemTeam({ name: '授权分页团队A' }, adminAccess)
  const teamB = repositories.createSystemTeam({ name: '授权分页团队B' }, adminAccess)
  repositories.addSystemTeamMembers(teamB.id, { systemAccountIds: [granteeA.id] }, adminAccess)
  const group = repositories.createGroup({
    name: '授权分页分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权分页账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-authorization-usage-pagination',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, ownerAccess)
  const granteeAGroup = repositories.createGroup({
    name: '授权分页用户A目标分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAAccess)

  seedTeamWindow({
    systemAccountId: owner.id,
    teamId: '',
    resourceType: 'all',
    resourceId: '',
    requestCount: 50,
    inputTokens: 500,
    outputTokens: 100,
    totalCost: 0.5
  })
  seedTeamWindow({
    systemAccountId: owner.id,
    teamId: teamA.id,
    resourceType: 'account',
    resourceId: account.id,
    requestCount: 30,
    inputTokens: 300,
    outputTokens: 60,
    totalCost: 0.3,
    lastUsedAt: '2026-01-01T00:03:00.000Z'
  })
  seedTeamWindow({
    systemAccountId: owner.id,
    teamId: teamB.id,
    resourceType: 'group',
    resourceId: group.id,
    requestCount: 20,
    inputTokens: 200,
    outputTokens: 40,
    totalCost: 0.2,
    lastUsedAt: '2026-01-01T00:02:00.000Z'
  })

  seedUserWindow({
    systemAccountId: owner.id,
    teamId: '',
    granteeSystemAccountId: '',
    resourceType: 'all',
    resourceId: '',
    requestCount: 70,
    inputTokens: 700,
    outputTokens: 140,
    totalCost: 0.7
  })
  seedUserWindow({
    systemAccountId: owner.id,
    teamId: '',
    granteeSystemAccountId: granteeA.id,
    resourceType: 'account',
    resourceId: account.id,
    requestCount: 40,
    inputTokens: 400,
    outputTokens: 80,
    totalCost: 0.4,
    lastUsedAt: '2026-01-01T00:04:00.000Z'
  })
  seedUserWindow({
    systemAccountId: owner.id,
    teamId: '',
    granteeSystemAccountId: granteeB.id,
    resourceType: 'group',
    resourceId: group.id,
    requestCount: 30,
    inputTokens: 300,
    outputTokens: 60,
    totalCost: 0.3,
    lastUsedAt: '2026-01-01T00:03:00.000Z'
  })

  const teamPageOne = authorizationUsageRepository.getAuthorizationTeamUsageRows({}, ownerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(teamPageOne.rows.length, 1, '团队消耗第一页应只返回当前页窗口')
  assert.equal(teamPageOne.rows[0]?.teamId, teamA.id, '团队消耗第一页应按成本排序返回第一行')
  assert.equal(teamPageOne.rows[0]?.resourceName, account.name, '团队消耗页只应为当前页行补资源名称')
  assert.deepEqual(Object.keys(teamPageOne).sort(), ['hasMore', 'page', 'pageSize', 'range', 'rows', 'total'].sort(), '团队 rows result 必须保持严格字段白名单')
  assert.deepEqual(Object.keys(teamPageOne.rows[0] ?? {}).sort(), [
    'accountOwnerSystemAccountId', 'accountOwnerSystemAccountName', 'id', 'lastUsedAt', 'resourceId',
    'resourceName', 'resourceType', 'teamId', 'teamName', 'usage'
  ].sort(), '团队 row 只能返回展示与跳转需要的字段')
  assert.deepEqual(Object.keys(teamPageOne.rows[0]?.usage ?? {}).sort(), ['requestCount', 'totalCost', 'totalTokens'].sort(), '团队 row usage 必须保持三字段窄投影')
  assert.equal(teamPageOne.page, 1, '团队消耗第一页页码应稳定返回')
  assert.equal(teamPageOne.pageSize, 1, '团队消耗第一页 pageSize 应稳定返回')
  assert.equal(teamPageOne.total, 2, '团队消耗第一页分页上界 total 应覆盖当前页和下一页')
  assert.equal(teamPageOne.hasMore, true, '团队消耗第一页应标记还有下一页')
  const teamSummary = authorizationUsageRepository.getAuthorizationTeamUsageSummary({}, ownerAccess, range)
  assert.equal(teamSummary.summary.requestCount, 50, '独立团队摘要必须精确读取摘要窗口，不能累加明细窗口')
  assert.deepEqual(Object.keys(teamSummary.summary).sort(), ['cacheWriteTokens', 'inputTokens', 'lastUsedAt', 'requestCount', 'totalCost', 'totalTokens'].sort(), '团队 summary 必须保持页面消费的六字段窄投影')

  const teamPageTwo = authorizationUsageRepository.getAuthorizationTeamUsageRows({}, ownerAccess, range, { page: 2, pageSize: 1 })
  assert.equal(teamPageTwo.rows.length, 1, '团队消耗第二页应只返回当前页窗口')
  assert.equal(teamPageTwo.rows[0]?.teamId, teamB.id, '团队消耗第二页应返回下一行')
  assert.equal(teamPageTwo.total, 2, '团队消耗第二页分页上界 total 应保持已知总量')
  assert.equal(teamPageTwo.hasMore, false, '团队消耗第二页应标记没有更多')

  const adminTeamPage = authorizationUsageRepository.getAuthorizationTeamUsageRows({}, adminOwnerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(adminTeamPage.rows[0]?.teamId, teamA.id, '管理侧指定资源归属用户时应读取同一分页窗口')

  const userPageOne = authorizationUsageRepository.getAuthorizationUserUsageRows({}, ownerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(userPageOne.rows.length, 1, '用户消耗第一页应只返回当前页窗口')
  assert.equal(userPageOne.rows[0]?.userName, granteeA.displayName, '用户消耗第一页应按成本排序返回第一行')
  assert.equal(userPageOne.rows[0]?.resourceName, account.name, '用户消耗页只应为当前页行补资源名称')
  assert.deepEqual(userPageOne.rows[0]?.teamNames, [], '未筛团队时不得泄露被授权用户当前所属的其他团队名称')
  assert.deepEqual(Object.keys(userPageOne).sort(), ['hasMore', 'page', 'pageSize', 'range', 'rows', 'total'].sort(), '用户 rows result 必须保持严格字段白名单')
  assert.deepEqual(Object.keys(userPageOne.rows[0] ?? {}).sort(), [
    'accountOwnerSystemAccountName', 'id', 'lastUsedAt', 'resourceName', 'resourceType',
    'teamNames', 'usage', 'userName', 'username'
  ].sort(), '用户 row 只能返回页面实际需要的字段')
  assert.deepEqual(Object.keys(userPageOne.rows[0]?.usage ?? {}).sort(), ['requestCount', 'totalCost', 'totalTokens'].sort(), '用户 row usage 必须保持三字段窄投影')
  assert.equal(userPageOne.page, 1, '用户消耗第一页页码应稳定返回')
  assert.equal(userPageOne.pageSize, 1, '用户消耗第一页 pageSize 应稳定返回')
  assert.equal(userPageOne.total, 2, '用户消耗第一页分页上界 total 应覆盖当前页和下一页')
  assert.equal(userPageOne.hasMore, true, '用户消耗第一页应标记还有下一页')
  const userSummary = authorizationUsageRepository.getAuthorizationUserUsageSummary({}, ownerAccess, range)
  assert.equal(userSummary.summary.requestCount, 70, '独立用户摘要必须精确读取摘要窗口，不能累加明细窗口')
  assert.deepEqual(Object.keys(userSummary.summary).sort(), ['cacheWriteTokens', 'inputTokens', 'lastUsedAt', 'requestCount', 'totalCost', 'totalTokens'].sort(), '用户 summary 必须保持页面消费的六字段窄投影')

  const userPageTwo = authorizationUsageRepository.getAuthorizationUserUsageRows({}, ownerAccess, range, { page: 2, pageSize: 1 })
  assert.equal(userPageTwo.rows.length, 1, '用户消耗第二页应只返回当前页窗口')
  assert.equal(userPageTwo.rows[0]?.userName, granteeB.displayName, '用户消耗第二页应返回下一行')
  assert.equal(userPageTwo.total, 2, '用户消耗第二页分页上界 total 应保持已知总量')
  assert.equal(userPageTwo.hasMore, false, '用户消耗第二页应标记没有更多')

  const accountGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'system_account',
    granteeId: granteeA.id,
    targetGroupId: granteeAGroup.id
  }, ownerAccess)
  const runtimeAuthorizationId = runtimeAuthorizationIdFor(account.id, granteeA.id)
  seedUsageScopeRangeWindow(owner.id, 'account_authorization', runtimeAuthorizationId, 9)
  seedUsageScopeRangeWindow(granteeA.id, 'account_authorization', runtimeAuthorizationId, 44)
  const ownerAccountGrant = repositories.findResourceAuthorization(accountGrant.id, ownerAccess, { usageRange: range })
  assert.equal(ownerAccountGrant?.usage.requestCount, 44, '账号授权列表应读取被授权人侧 account_authorization 统计，而不是资源归属人统计')
  const inboundAccountGrant = repositories.listResourceAuthorizations({ direction: 'inbound', status: 'all' }, granteeAAccess, { usageRange: range })
    .find((authorization) => authorization.id === accountGrant.id)
  assert.equal(inboundAccountGrant?.usage.requestCount, 44, '授权给我的账号授权列表应展示我自己产生的授权用量')
  const accountGrantUsageDetail = repositories.getResourceAuthorizationUsage(accountGrant.id, granteeAAccess, { range })
  assert.equal(accountGrantUsageDetail?.usage.requestCount, 44, '授权给我的账号授权详情应展示我自己产生的授权用量')

  repositories.addSystemTeamMembers(teamA.id, { systemAccountIds: [granteeB.id] }, adminAccess)
  const accountTeamGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'team',
    granteeId: teamA.id
  }, ownerAccess)
  const ownerTeamGrant = repositories.findResourceAuthorization(accountTeamGrant.id, ownerAccess, { usageRange: range })
  assert.equal(ownerTeamGrant?.usage.requestCount, 30, '账号团队授权列表应读取授权团队报表窗口，而不是来源账号 scope')
  const accountTeamGrantUsageDetail = repositories.getResourceAuthorizationUsage(accountTeamGrant.id, ownerAccess, { range })
  assert.equal(accountTeamGrantUsageDetail?.usage.requestCount, 30, '账号团队授权详情应读取授权团队报表窗口')

  console.log('授权消耗分页回归通过：团队/用户明细按窗口分页返回，前端无需全量渲染；授权 usage 资源 lookup 复用 Redis shared cache')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertAuthorizationUsageLookupSharedCacheBoundary(): void {
  const source = readFileSync(new URL('../../storage/resource-authorization-usage.repository.ts', import.meta.url), 'utf8')
  assert(
    source.includes('loadSystemAccountPrincipalMapByIds, loadSystemAccountPrincipalMapByIdsAsync'),
    '授权 usage 仓储应从 repository-lookups 导入同步 / 异步系统账户 lookup'
  )
  assert(
    !/async function loadSystemAccountPrincipalMapByIdsAsync\b/.test(source),
    '授权 usage 仓储不应保留私有 async 系统账户 lookup，应复用 repository-lookups Redis shared cache'
  )
  assert(
    !source.includes("resourceAuthorizationUsageBusinessTable(client, 'system_accounts')"),
    '授权 usage async 名称装配不应直接查询 system_accounts，应走 shared cache helper'
  )
}

function seedTeamWindow(input: {
  systemAccountId: string
  teamId: string
  resourceType: 'all' | ResourceAuthorizationResourceType
  resourceId: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  totalCost: number
  lastUsedAt?: string
}): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO authorization_team_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)
    `)
    .run(
      input.systemAccountId,
      range.startDate,
      range.endDate,
      input.teamId,
      input.resourceType,
      input.resourceId,
      input.requestCount,
      input.inputTokens,
      input.outputTokens,
      input.totalCost,
      input.lastUsedAt ?? null,
      '2026-01-01T00:10:00.000Z'
    )
}

function seedUserWindow(input: {
  systemAccountId: string
  teamId: string
  granteeSystemAccountId: string
  resourceType: 'all' | ResourceAuthorizationResourceType
  resourceId: string
  requestCount: number
  inputTokens: number
  outputTokens: number
  totalCost: number
  lastUsedAt?: string
}): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO authorization_user_usage_range_windows (
        system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
        last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?)
    `)
    .run(
      input.systemAccountId,
      range.startDate,
      range.endDate,
      input.teamId,
      input.granteeSystemAccountId,
      input.resourceType,
      input.resourceId,
      input.requestCount,
      input.inputTokens,
      input.outputTokens,
      input.totalCost,
      input.lastUsedAt ?? null,
      '2026-01-01T00:10:00.000Z'
    )
}

function runtimeAuthorizationIdFor(accountId: string, granteeSystemAccountId: string): string {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM resource_authorizations
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(accountId, granteeSystemAccountId) as unknown as { id?: string } | undefined
  assert(row?.id, '账号授权应创建被授权人的运行时授权记录')
  return row.id
}

function seedUsageScopeRangeWindow(
  systemAccountId: string,
  scopeType: string,
  scopeId: string,
  requestCount: number
): void {
  const lastUsedAt = '2026-01-01T00:44:00.000Z'
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_scope_range_windows (
        system_account_id, scope_type, scope_id, start_date, end_date,
        request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
        total_cost_usd, last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, ?, ?, ?)
    `)
    .run(systemAccountId, scopeType, scopeId, range.startDate, range.endDate, requestCount, requestCount * 0.01, lastUsedAt, '2026-01-01T00:45:00.000Z')
}
