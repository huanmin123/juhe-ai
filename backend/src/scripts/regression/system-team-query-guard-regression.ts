import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
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

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const firstMember = repositories.createSystemAccount({
    username: 'system_team_query_guard_member_1',
    displayName: '系统团队查询防护成员 1',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const secondMember = repositories.createSystemAccount({
    username: 'system_team_query_guard_member_2',
    displayName: '系统团队查询防护成员 2',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const firstTeam = repositories.createSystemTeam({ name: '系统团队查询防护 1' }, access)
  const secondTeam = repositories.createSystemTeam({ name: '系统团队查询防护 2' }, access)
  repositories.addSystemTeamMembers(firstTeam.id, { systemAccountIds: [firstMember.id] }, access)
  repositories.addSystemTeamMembers(secondTeam.id, { systemAccountIds: [secondMember.id] }, access)

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
  } finally {
    database.prepare = originalPrepare as typeof database.prepare
  }

  const teamListCall = capturedCalls.find((call) => /\bFROM\s+system_teams\b/i.test(call.sql) && /\bORDER\s+BY\s+status\s+ASC,\s+updated_at\s+DESC,\s+name\s+ASC,\s+id\s+ASC/i.test(call.sql))
  assert(teamListCall, '系统团队列表应按固定排序读取分页窗口')
  const teamListPlan = explainQueryPlan(database, teamListCall.sql, teamListCall.params)
  assertNoTempBtree(teamListPlan, '系统团队列表')
  assert(teamListPlan.includes('idx_system_teams_list_order'), `系统团队列表应使用完整排序索引，实际计划：${teamListPlan}`)

  const memberWindowCall = capturedCalls.find((call) => /\bROW_NUMBER\(\)\s+OVER\b/i.test(call.sql) && /\bFROM\s+system_team_members\b/i.test(call.sql))
  assert(memberWindowCall, '系统团队列表应按团队窗口读取成员摘要')
  assert(!/\bORDER\s+BY\s+team_id\s+ASC,\s+status\s+ASC,\s+joined_at\s+ASC,\s+id\s+ASC\b/i.test(memberWindowCall.sql), '团队成员窗口不应在 SQL 末端再次排序')
  const memberWindowPlan = explainQueryPlan(database, memberWindowCall.sql, memberWindowCall.params)
  assertNoTempBtree(memberWindowPlan, '系统团队成员窗口')
  assert(memberWindowPlan.includes('idx_system_team_members_team_status_joined'), `系统团队成员窗口应使用 team/status/joined_at/id 索引，实际计划：${memberWindowPlan}`)

  console.log('系统团队查询防护回归通过：列表和成员窗口均命中排序索引且不创建临时排序树')
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
