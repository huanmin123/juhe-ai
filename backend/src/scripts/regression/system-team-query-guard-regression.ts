import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-team-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-team-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const routesSource = readFileSync(resolve('src/modules/system-teams/system-teams.routes.ts'), 'utf8')
assert(routesSource.includes('listSystemTeamsPageAsync'), '系统团队路由列表必须使用 async repository')
assert(routesSource.includes('createSystemTeamAsync'), '系统团队路由创建必须使用 async repository')
assert(routesSource.includes('updateSystemTeamAsync'), '系统团队路由更新必须使用 async repository')
assert(routesSource.includes('addSystemTeamMembersAsync'), '系统团队路由成员添加必须使用 async repository')
assert(routesSource.includes('removeSystemTeamMemberAsync'), '系统团队路由成员移除必须使用 async repository')
assert(routesSource.includes('runLoggedOperationAsync'), '系统团队写操作日志必须使用 async 包裹')
assert(!routesSource.includes('runLoggedOperation('), '系统团队路由不能重新引入同步操作日志包裹')

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const firstMember = repositories.createSystemAccount({
    username: 'system_team_query_guard_member_1',
    displayName: '系统团队查询防护成员1',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const secondMember = repositories.createSystemAccount({
    username: 'system_team_query_guard_member_2',
    displayName: '系统团队查询防护成员2',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const firstTeam = repositories.createSystemTeam({ name: '系统团队查询防护 1' }, access)
  const secondTeam = repositories.createSystemTeam({ name: '系统团队查询防护 2' }, access)
  repositories.addSystemTeamMembers(firstTeam.id, { systemAccountIds: [firstMember.id] }, access)
  repositories.addSystemTeamMembers(secondTeam.id, { systemAccountIds: [secondMember.id] }, access)
  const firstTeamMemberId = repositories.listSystemTeamMembers(firstTeam.id, access)?.items[0]?.id
  assert(firstTeamMemberId, '系统团队历史查询防护需要有效成员夹具')
  repositories.removeSystemTeamMember(firstTeam.id, firstTeamMemberId, access)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database)
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalAll = statement.all.bind(statement) as typeof statement.all
    statement.all = ((...params: SQLInputValue[]) => {
      capturedCalls.push({ sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    return statement
  }) as typeof database.prepare

  try {
    const page = repositories.listSystemTeamsPage(access, { page: 1, pageSize: 20 })
    assert(page.items.length >= 2, '系统团队列表回归应至少返回两个团队')
    const history = repositories.listSystemTeamMemberHistory(firstTeam.id, { page: 1, pageSize: 20 }, access)
    assert.deepEqual(history?.items.map((item) => item.status), ['removed'], '历史成员查询只能返回已移除成员')
  } finally {
    database.prepare = originalPrepare as typeof database.prepare
  }

  const teamListCall = capturedCalls.find((call) => /\bFROM\s+system_teams\b/i.test(call.sql) && /\bORDER\s+BY\s+status\s+ASC,\s+updated_at\s+DESC,\s+name\s+ASC,\s+id\s+ASC/i.test(call.sql))
  assert(teamListCall, '系统团队列表应按固定排序读取分页窗口')
  const teamListPlan = explainQueryPlan(database, teamListCall.sql, teamListCall.params)
  assertNoTempBtree(teamListPlan, '系统团队列表')
  assert(teamListPlan.includes('idx_system_teams_list_order'), `系统团队列表应使用完整排序索引，实际计划：${teamListPlan}`)

  const memberCountCall = capturedCalls.find((call) => /\bCOUNT\(\*\)\s+AS\s+member_count\b/i.test(call.sql) && /\bFROM\s+system_team_members\b/i.test(call.sql))
  assert(memberCountCall, '系统团队列表应只批量读取有效成员计数')
  assert.match(memberCountCall.sql, /\bstatus\s*=\s*'active'/i, '系统团队列表成员数只统计有效成员')
  const memberCountPlan = explainQueryPlan(database, memberCountCall.sql, memberCountCall.params)
  assertNoTempBtree(memberCountPlan, '系统团队成员计数')
  assert(/idx_system_team_members_team(?:_status_joined)?/.test(memberCountPlan), `系统团队成员计数应使用 team/status 索引，实际计划：${memberCountPlan}`)

  const historyCall = capturedCalls.find((call) => /system_team_members\.status\s*=\s*'removed'/i.test(call.sql)
    && /ORDER\s+BY\s+system_team_members\.joined_at\s+DESC,\s*system_team_members\.id\s+DESC/i.test(call.sql))
  assert(historyCall, '系统团队历史成员应使用仅 removed 的稳定倒序分页查询')
  const historyPlan = explainQueryPlan(database, historyCall.sql, historyCall.params)
  assertNoTempBtree(historyPlan, '系统团队历史成员')
  assert(historyPlan.includes('idx_system_team_members_team_status_joined'), `系统团队历史成员应使用 team/status/joined 索引，实际计划：${historyPlan}`)

  console.log('系统团队查询防护回归通过：列表、有效成员计数和历史成员均命中索引且不创建临时排序树')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function explainQueryPlan(database: DatabaseSync, sql: string, params: SQLInputValue[]): string {
  const rows = database
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params) as Array<{ detail?: string }>
  return rows.map((row) => row.detail ?? '').filter(Boolean).join('\n')
}

function assertNoTempBtree(details: string, label: string): void {
  assert(!/USE TEMP B-TREE/i.test(details), `${label}不应创建临时排序树，实际计划：${details}`)
}
