import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-options-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'authorization-options-query-guard-secret'
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
  const matchedAccount = repositories.createSystemAccount({
    username: 'grant-target',
    displayName: '候选目标用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const prefixAccount = repositories.createSystemAccount({
    username: 'grant-target-extra',
    displayName: '候选目标用户扩展',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const middleAccount = repositories.createSystemAccount({
    username: 'ordinary-grant-target',
    displayName: '普通候选目标用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const wildcardAccount = repositories.createSystemAccount({
    username: 'grant-percent-literal',
    displayName: 'percent%literal 用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const wildcardNeighborAccount = repositories.createSystemAccount({
    username: 'grant-percent-neighbor',
    displayName: 'percentXliteral 用户',
    password: 'Password-123456',
    mustChangePassword: false
  })

  const matchedTeam = repositories.createSystemTeam({ name: '候选目标团队' })
  const prefixTeam = repositories.createSystemTeam({ name: '候选目标团队扩展' })
  const middleTeam = repositories.createSystemTeam({ name: '普通候选目标团队' })
  const wildcardTeam = repositories.createSystemTeam({ name: 'percent%literal 团队' })
  const wildcardNeighborTeam = repositories.createSystemTeam({ name: 'percentXliteral 团队' })

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /^\s*SELECT\b/i.test(sql)
      && (/\bFROM\s+system_accounts\b/i.test(sql) || /\bFROM\s+system_teams\b/i.test(sql))
      && /\bORDER\s+BY\b/i.test(sql)
    if (shouldCapture) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const accountOptions = repositories.listAuthorizationGranteeAccounts(undefined, { keyword: '候选目标用户', limit: 20 })
    const accountIds = accountOptions.map((account) => account.id)
    assert(accountIds.includes(matchedAccount.id), '授权候选用户搜索应命中展示名精确值')
    assert(accountIds.includes(prefixAccount.id), '授权候选用户搜索应命中展示名前缀值')
    assert(!accountIds.includes(middleAccount.id), '授权候选用户搜索不应命中展示名中间包含值')
    assert.equal(repositories.listAuthorizationGranteeAccounts(undefined, { keyword: '候选目标用户', limit: 1 }).length, 1, '授权候选用户搜索应遵守 limit')

    const accountWildcardIds = repositories.listAuthorizationGranteeAccounts(undefined, { keyword: 'percent%', limit: 20 }).map((account) => account.id)
    assert(accountWildcardIds.includes(wildcardAccount.id), '授权候选用户搜索应把 % 当作字面量前缀处理')
    assert(!accountWildcardIds.includes(wildcardNeighborAccount.id), '授权候选用户搜索不应把用户输入的 % 当作 LIKE 通配符')

    const teamOptions = repositories.listAuthorizationGranteeTeams(undefined, { keyword: '候选目标团队', limit: 20 })
    const teamIds = teamOptions.map((team) => team.id)
    assert(teamIds.includes(matchedTeam.id), '授权候选团队搜索应命中名称精确值')
    assert(teamIds.includes(prefixTeam.id), '授权候选团队搜索应命中名称前缀值')
    assert(!teamIds.includes(middleTeam.id), '授权候选团队搜索不应命中名称中间包含值')
    assert.equal(repositories.listAuthorizationGranteeTeams(undefined, { keyword: '候选目标团队', limit: 1 }).length, 1, '授权候选团队搜索应遵守 limit')

    const teamWildcardIds = repositories.listAuthorizationGranteeTeams(undefined, { keyword: 'percent%', limit: 20 }).map((team) => team.id)
    assert(teamWildcardIds.includes(wildcardTeam.id), '授权候选团队搜索应把 % 当作字面量前缀处理')
    assert(!teamWildcardIds.includes(wildcardNeighborTeam.id), '授权候选团队搜索不应把用户输入的 % 当作 LIKE 通配符')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 6, '回归应捕获授权候选项 SQL')
  for (const call of capturedCalls) {
    assert(!/\bCOALESCE\s*\(/i.test(call.sql), '授权候选项搜索不应通过 COALESCE 扫描字段')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '授权候选项搜索不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), '授权候选项前缀搜索应显式转义 LIKE 通配符')
    }
  }
  assertBusinessIndexExists('idx_system_accounts_username_lookup')
  assertBusinessIndexExists('idx_system_accounts_display_name_lookup')
  assertBusinessIndexExists('idx_system_teams_name_lookup')

  console.log('授权候选项查询防护回归通过：用户/团队 options 支持精确/前缀搜索、limit 和通配符转义')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
