import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountUsageStatsRange, ResourceAuthorizationResourceType } from '../../domain/types.js'
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

  const teamA = repositories.createSystemTeam({ name: '授权分页团队A' }, adminAccess)
  const teamB = repositories.createSystemTeam({ name: '授权分页团队B' }, adminAccess)
  const group = repositories.createGroup({
    name: '授权分页分组',
    providerCode: 'openai',
    enabled: true
  }, ownerAccess)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '授权分页账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-authorization-usage-pagination',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, ownerAccess)

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

  const teamPageOne = authorizationUsageRepository.getAuthorizationTeamUsageOverview({}, ownerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(teamPageOne.rows.length, 1, '团队消耗第一页应只返回当前页窗口')
  assert.equal(teamPageOne.rows[0]?.teamId, teamA.id, '团队消耗第一页应按成本排序返回第一行')
  assert.equal(teamPageOne.rows[0]?.resourceName, account.name, '团队消耗页只应为当前页行补资源名称')
  assert.equal(teamPageOne.summary.requestCount, 50, '团队消耗摘要应来自摘要窗口行')
  assert.equal(teamPageOne.teamCount, 2, '团队消耗团队数应来自窗口表 distinct 统计')
  assert.equal(teamPageOne.page, 1, '团队消耗第一页页码应稳定返回')
  assert.equal(teamPageOne.pageSize, 1, '团队消耗第一页 pageSize 应稳定返回')
  assert.equal(teamPageOne.total, 2, '团队消耗第一页分页上界 total 应覆盖当前页和下一页')
  assert.equal(teamPageOne.hasMore, true, '团队消耗第一页应标记还有下一页')

  const teamPageTwo = authorizationUsageRepository.getAuthorizationTeamUsageOverview({}, ownerAccess, range, { page: 2, pageSize: 1 })
  assert.equal(teamPageTwo.rows.length, 1, '团队消耗第二页应只返回当前页窗口')
  assert.equal(teamPageTwo.rows[0]?.teamId, teamB.id, '团队消耗第二页应返回下一行')
  assert.equal(teamPageTwo.total, 2, '团队消耗第二页分页上界 total 应保持已知总量')
  assert.equal(teamPageTwo.hasMore, false, '团队消耗第二页应标记没有更多')

  const adminTeamPage = authorizationUsageRepository.getAuthorizationTeamUsageOverview({}, adminOwnerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(adminTeamPage.rows[0]?.teamId, teamA.id, '管理侧指定资源归属用户时应读取同一分页窗口')

  const userPageOne = authorizationUsageRepository.getAuthorizationUserUsageOverview({}, ownerAccess, range, { page: 1, pageSize: 1 })
  assert.equal(userPageOne.rows.length, 1, '用户消耗第一页应只返回当前页窗口')
  assert.equal(userPageOne.rows[0]?.systemAccountId, granteeA.id, '用户消耗第一页应按成本排序返回第一行')
  assert.equal(userPageOne.rows[0]?.resourceName, account.name, '用户消耗页只应为当前页行补资源名称')
  assert.equal(userPageOne.summary.requestCount, 70, '用户消耗摘要应来自摘要窗口行')
  assert.equal(userPageOne.userCount, 2, '用户消耗用户数应来自窗口表 distinct 统计')
  assert.equal(userPageOne.page, 1, '用户消耗第一页页码应稳定返回')
  assert.equal(userPageOne.pageSize, 1, '用户消耗第一页 pageSize 应稳定返回')
  assert.equal(userPageOne.total, 2, '用户消耗第一页分页上界 total 应覆盖当前页和下一页')
  assert.equal(userPageOne.hasMore, true, '用户消耗第一页应标记还有下一页')

  const userPageTwo = authorizationUsageRepository.getAuthorizationUserUsageOverview({}, ownerAccess, range, { page: 2, pageSize: 1 })
  assert.equal(userPageTwo.rows.length, 1, '用户消耗第二页应只返回当前页窗口')
  assert.equal(userPageTwo.rows[0]?.systemAccountId, granteeB.id, '用户消耗第二页应返回下一行')
  assert.equal(userPageTwo.total, 2, '用户消耗第二页分页上界 total 应保持已知总量')
  assert.equal(userPageTwo.hasMore, false, '用户消耗第二页应标记没有更多')

  console.log('授权消耗分页回归通过：团队/用户明细按窗口分页返回，前端无需全量渲染')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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
