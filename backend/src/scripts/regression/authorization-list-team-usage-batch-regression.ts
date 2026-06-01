import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountUsageStatsRange, ResourceAuthorizationResourceType } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-team-usage-batch-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-team-usage-batch-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const range: AccountUsageStatsRange = {
  startDate: '2026-01-01',
  endDate: '2026-01-01',
  days: 1,
  maxDays: 31
}

try {
  const owner = repositories.createSystemAccount({
    username: 'authorization_team_batch_owner',
    displayName: '团队授权批量查询所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_team_batch_grantee',
    displayName: '团队授权批量查询成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const teams = Array.from({ length: 6 }, (_value, index) => {
    const team = repositories.createSystemTeam({ name: `团队授权批量查询团队 ${index}` }, adminAccess)
    repositories.addSystemTeamMembers(team.id, { systemAccountIds: [grantee.id] }, adminAccess)
    return team
  })
  const group = repositories.createGroup({
    name: '团队授权批量查询分组',
    providerCode: 'openai'
  }, ownerAccess)

  const grantCount = 120
  const grants: Array<{ id: string; requestCount: number }> = []
  for (let index = 0; index < grantCount; index += 1) {
    const team = teams[Math.floor(index / 20)]
    assert(team, '团队种子数据不足')
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `团队授权批量查询账户 ${index}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-authorization-team-batch-${index.toString().padStart(3, '0')}`,
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id
    }, ownerAccess)
    const grant = repositories.createResourceAuthorization({
      resourceType: 'account',
      resourceId: account.id,
      granteeType: 'team',
      granteeId: team.id
    }, ownerAccess)
    const requestCount = index + 1
    grants.push({ id: grant.id, requestCount })
    seedTeamWindow({
      systemAccountId: owner.id,
      teamId: team.id,
      resourceType: 'account',
      resourceId: account.id,
      requestCount
    })
  }

  const statsDatabase = databaseModule.getStatsDatabase()
  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  let rangeWindowGetCalls = 0
  let rangeWindowAllCalls = 0
  statsDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bauthorization_team_usage_range_windows\b/i.test(sql) && /^\s*(?:WITH|SELECT)\b/i.test(sql)) {
      const originalGet = statement.get.bind(statement) as typeof statement.get
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.get = ((...params: SQLInputValue[]) => {
        rangeWindowGetCalls += 1
        return originalGet(...params)
      }) as typeof statement.get
      statement.all = ((...params: SQLInputValue[]) => {
        rangeWindowAllCalls += 1
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof statsDatabase.prepare

  try {
    const page = repositories.listResourceAuthorizationsPage({ status: 'all' }, ownerAccess, {
      usageRange: range,
      page: 1,
      pageSize: grantCount
    })
    assert.equal(page.items.length, grantCount, '授权列表应返回当前页全部团队授权')
    const byId = new Map(page.items.map((item) => [item.id, item]))
    assert.equal(byId.get(grants[0]?.id ?? '')?.usage.requestCount, grants[0]?.requestCount, '团队授权列表应读取第一页首条报表窗口用量')
    assert.equal(byId.get(grants.at(-1)?.id ?? '')?.usage.requestCount, grants.at(-1)?.requestCount, '团队授权列表应读取第一页末条报表窗口用量')
    assert.equal(rangeWindowGetCalls, 0, `团队授权列表不应逐条 get 查询报表窗口，实际 ${rangeWindowGetCalls}`)
    assert(
      rangeWindowAllCalls <= 2,
      `团队授权列表应按批读取团队报表窗口，120 条授权最多 2 次批量查询，实际 ${rangeWindowAllCalls}`
    )
  } finally {
    statsDatabase.prepare = originalPrepare
  }

  console.log('授权列表团队用量批量查询回归通过：团队授权页按批读取报表窗口，避免按行 N+1 查询')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedTeamWindow(input: {
  systemAccountId: string
  teamId: string
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  requestCount: number
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
      input.requestCount * 10,
      input.requestCount * 2,
      input.requestCount * 0.01,
      '2026-01-01T00:10:00.000Z',
      '2026-01-01T00:11:00.000Z'
    )
}
