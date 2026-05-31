import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-base-list-page-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'base-list-page-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, externalIntegrationSources] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/external-integration-source.repository.js')
])

type PageLike = {
  page: number
  pageSize: number
}

try {
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const teamAuthorizationId = seedTeamAuthorization(adminAccess)
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedOffsets: Array<{ sql: string; offset: number }> = []

  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        const offsetParam = params[params.length - 1]
        if (typeof offsetParam === 'number') {
          capturedOffsets.push({ sql, offset: offsetParam })
        }
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    assertPageWindow('API Key 列表', repositories.listApiKeysPage(adminAccess, { page: 999999, pageSize: 100 }))
    assertPageWindow('AI 账户列表', repositories.listAccountsPage(adminAccess, { page: 999999, pageSize: 100 }))
    assertPageWindow('分组列表', repositories.listGroupsPage(adminAccess, { page: 999999, pageSize: 100 }))
    assertPageWindow('系统用户列表', repositories.listSystemAccountsPage({ page: 999999, pageSize: 100 }))
    assertPageWindow('代理列表', repositories.listProxiesPage({ page: 999999, pageSize: 100 }))
    assertPageWindow('公告列表', repositories.listAnnouncementsPage({ page: 999999, pageSize: 100 }))
    assertPageWindow('外部来源系统列表', externalIntegrationSources.listExternalIntegrationSources({ page: 999999, pageSize: 100 }))
    assertPageWindow('系统团队列表', repositories.listSystemTeamsPage(adminAccess, { page: 999999, pageSize: 100 }))
    assertPageWindow('统一授权列表', repositories.listResourceAuthorizationsPage({ status: 'all' }, adminAccess, { page: 999999, pageSize: 100 }))

    const usageDetail = repositories.getResourceAuthorizationUsage(teamAuthorizationId, adminAccess, { page: 999999, pageSize: 200 })
    assert(usageDetail, '团队授权用量详情应存在')
    assertPageWindow('统一授权用量详情', {
      page: usageDetail.usageBySystemAccountPage ?? 0,
      pageSize: usageDetail.usageBySystemAccountPageSize ?? 0
    })
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedOffsets.length >= 10, '回归应捕获基础管理列表和授权用量详情的分页 SQL')
  for (const captured of capturedOffsets) {
    assert(captured.offset <= 1000, `分页 SQL offset 不应超过 1000，实际为 ${captured.offset}：${compactSql(captured.sql)}`)
  }

  console.log('基础管理列表页码窗口回归通过：API Key、账户、分组、用户、代理、公告、外部来源、团队、统一授权和授权用量详情 offset 均限制在 1000 行内')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertPageWindow(label: string, result: PageLike): void {
  assert(result.page >= 1, `${label} 页码应至少为 1`)
  assert(result.pageSize >= 1, `${label} pageSize 应至少为 1`)
  assert((result.page - 1) * result.pageSize <= 1000, `${label} 深翻页 offset 应限制在 1000 行内`)
}

function seedTeamAuthorization(access: { systemAccountId: string; role: 'admin' }): string {
  const member = repositories.createSystemAccount({
    username: 'base_list_page_window_member',
    displayName: '基础列表页码窗口成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const team = repositories.createSystemTeam({
    name: '基础列表页码窗口团队'
  }, access)
  repositories.addSystemTeamMembers(team.id, { systemAccountIds: [member.id] }, access)
  const group = repositories.createGroup({
    name: '基础列表页码窗口分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const authorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'team',
    granteeId: team.id
  }, access)
  return authorization.id
}

function compactSql(sql: string): string {
  return sql.replace(/\s+/g, ' ').trim().slice(0, 240)
}
