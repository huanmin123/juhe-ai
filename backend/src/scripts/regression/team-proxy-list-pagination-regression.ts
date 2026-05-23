import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-team-proxy-list-pagination-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'team-proxy-list-pagination-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, businessSchema] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/schema/business-schema.js')
])

const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const userA = repositories.createSystemAccount({
    username: 'team-page-user-a',
    displayName: '团队分页用户 A',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'team-page-user-b',
    displayName: '团队分页用户 B',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }

  const teamMatched = repositories.createSystemTeam({ name: '分页搜索团队' }, adminAccess)
  const teamPrefix = repositories.createSystemTeam({ name: '分页搜索团队扩展' }, adminAccess)
  const teamMiddle = repositories.createSystemTeam({ name: '普通分页搜索团队' }, adminAccess)
  const teamWildcard = repositories.createSystemTeam({ name: 'team%literal 团队' }, adminAccess)
  const teamWildcardNeighbor = repositories.createSystemTeam({ name: 'teamXliteral 团队' }, adminAccess)
  const teamDescriptionOnly = repositories.createSystemTeam({ name: '说明字段团队', description: '分页搜索团队说明前缀' }, adminAccess)
  repositories.addSystemTeamMembers(teamMatched.id, { systemAccountIds: [userA.id] }, adminAccess)
  repositories.addSystemTeamMembers(teamPrefix.id, { systemAccountIds: [userA.id] }, adminAccess)
  repositories.addSystemTeamMembers(teamMiddle.id, { systemAccountIds: [userB.id] }, adminAccess)
  repositories.addSystemTeamMembers(teamWildcard.id, { systemAccountIds: [userA.id] }, adminAccess)
  repositories.addSystemTeamMembers(teamWildcardNeighbor.id, { systemAccountIds: [userA.id] }, adminAccess)
  repositories.addSystemTeamMembers(teamDescriptionOnly.id, { systemAccountIds: [userA.id] }, adminAccess)

  const proxyMatched = repositories.createProxy({
    name: '分页搜索代理',
    type: 'http',
    host: 'proxy-page-host',
    port: 18_080,
    username: 'proxy-page-user',
    enabled: true
  })
  repositories.createProxy({
    name: '分页搜索代理扩展',
    type: 'http',
    host: 'proxy-page-host-extra',
    port: 18_081,
    username: 'proxy-page-user-extra',
    enabled: true
  })
  repositories.createProxy({
    name: '普通分页搜索代理',
    type: 'http',
    host: 'ordinary-proxy-page-host',
    port: 18_082,
    username: 'ordinary-proxy-page-user',
    enabled: true
  })
  repositories.createProxy({
    name: '分页搜索代理停用',
    type: 'http',
    host: 'proxy-page-disabled',
    port: 18_083,
    username: 'proxy-page-disabled-user',
    enabled: false
  })
  repositories.createProxy({
    name: 'proxy%literal 代理',
    type: 'socks5h',
    host: 'proxy-percent-literal',
    port: 18_084,
    enabled: true
  })
  repositories.createProxy({
    name: 'proxyXliteral 代理',
    type: 'socks5h',
    host: 'proxy-percent-neighbor',
    port: 18_085,
    enabled: true
  })
  repositories.createProxy({
    name: '说明字段代理',
    description: '分页搜索代理说明前缀',
    type: 'http',
    host: 'description-only-host',
    port: 18_086,
    enabled: true
  })
  repositories.createProxy({
    name: '用户名字段代理',
    type: 'http',
    host: 'username-only-host',
    port: 18_087,
    username: '分页搜索代理用户',
    enabled: true
  })
  for (let index = 0; index < 55; index += 1) {
    repositories.createProxy({
      name: `选项上限代理 ${String(index).padStart(2, '0')}`,
      type: 'http',
      host: `proxy-option-limit-${index}`,
      port: 19_000 + index,
      enabled: true
    })
  }

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /^\s*SELECT\b/i.test(sql)
      && (/\bFROM\s+system_teams\b/i.test(sql) || /\bFROM\s+proxy_profiles\b/i.test(sql))
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
    const teamPageOne = repositories.listSystemTeamsPage(adminAccess, { page: 1, pageSize: 1 })
    assert.equal(teamPageOne.items.length, 1, '系统团队分页第一页应只返回 pageSize 条')
    assert.equal(teamPageOne.hasMore, true, '系统团队第一页应通过 pageSize + 1 标记还有更多')
    assert(teamPageOne.total >= 2, '系统团队分页 total 应提供兼容上界')

    const teamSearchIds = repositories.listSystemTeamsPage(adminAccess, { keyword: '分页搜索团队', page: 1, pageSize: 20 }).items.map((team) => team.id)
    assert(teamSearchIds.includes(teamMatched.id), '系统团队搜索应命中名称精确值')
    assert(teamSearchIds.includes(teamPrefix.id), '系统团队搜索应命中名称前缀值')
    assert(!teamSearchIds.includes(teamMiddle.id), '系统团队搜索不应命中名称中间包含值')
    assert(!teamSearchIds.includes(teamDescriptionOnly.id), '系统团队搜索不应命中说明字段，避免通用关键词扫描长文本')

    const userTeamSearchIds = repositories.listSystemTeamsPage(userAAccess, { keyword: '分页搜索团队', page: 1, pageSize: 20 }).items.map((team) => team.id)
    assert(userTeamSearchIds.includes(teamMatched.id), '我的团队搜索应返回当前用户加入的匹配团队')
    assert(!userTeamSearchIds.includes(teamMiddle.id), '我的团队搜索不应返回当前用户未加入的团队')
    assert(!userTeamSearchIds.includes(teamDescriptionOnly.id), '我的团队搜索不应通过说明字段命中团队')

    const teamWildcardIds = repositories.listSystemTeamsPage(adminAccess, { keyword: 'team%', page: 1, pageSize: 20 }).items.map((team) => team.id)
    assert(teamWildcardIds.includes(teamWildcard.id), '系统团队搜索应把 % 当作字面量前缀处理')
    assert(!teamWildcardIds.includes(teamWildcardNeighbor.id), '系统团队搜索不应把用户输入的 % 当作 LIKE 通配符')

    const teamIdSearchIds = repositories.listSystemTeamsPage(adminAccess, { keyword: teamMatched.id, page: 1, pageSize: 20 }).items.map((team) => team.id)
    assert(!teamIdSearchIds.includes(teamMatched.id), '系统团队列表搜索不应通过团队 ID 命中')

    const proxyPageOne = repositories.listProxiesPage({ page: 1, pageSize: 1 })
    assert.equal(proxyPageOne.items.length, 1, '代理分页第一页应只返回 pageSize 条')
    assert.equal(proxyPageOne.hasMore, true, '代理第一页应通过 pageSize + 1 标记还有更多')
    assert(proxyPageOne.total >= 2, '代理分页 total 应提供兼容上界')

    const proxySearchNames = repositories.listProxiesPage({ keyword: '分页搜索代理', page: 1, pageSize: 20 }).items.map((proxy) => proxy.name)
    assert(proxySearchNames.includes('分页搜索代理'), '代理搜索应命中名称精确值')
    assert(proxySearchNames.includes('分页搜索代理扩展'), '代理搜索应命中名称前缀值')
    assert(proxySearchNames.includes('分页搜索代理停用'), '代理管理列表应返回匹配的停用代理')
    assert(!proxySearchNames.includes('普通分页搜索代理'), '代理搜索不应命中名称中间包含值')
    assert(!proxySearchNames.includes('说明字段代理'), '代理搜索不应通过说明字段命中，避免扫描长文本')
    assert(!proxySearchNames.includes('用户名字段代理'), '代理搜索不应通过用户名字段命中，避免弱索引字段进通用搜索')

    const proxyIdNames = repositories.listProxiesPage({ keyword: proxyMatched.id, page: 1, pageSize: 20 }).items.map((proxy) => proxy.name)
    assert(!proxyIdNames.includes('分页搜索代理'), '代理搜索不应通过 ID 命中')
    const proxyHostNames = repositories.listProxiesPage({ keyword: 'proxy-page-host', page: 1, pageSize: 20 }).items.map((proxy) => proxy.name)
    assert(!proxyHostNames.includes('分页搜索代理'), '代理搜索不应通过地址命中')
    const proxyTypeNames = repositories.listProxiesPage({ keyword: 'http', page: 1, pageSize: 20 }).items.map((proxy) => proxy.name)
    assert(!proxyTypeNames.includes('分页搜索代理'), '代理搜索不应通过类型命中')

    const proxyWildcardNames = repositories.listProxiesPage({ keyword: 'proxy%', page: 1, pageSize: 20 }).items.map((proxy) => proxy.name)
    assert(proxyWildcardNames.includes('proxy%literal 代理'), '代理搜索应把 % 当作字面量前缀处理')
    assert(!proxyWildcardNames.includes('proxyXliteral 代理'), '代理搜索不应把用户输入的 % 当作 LIKE 通配符')

    const proxyOptionNames = repositories.listProxyOptions({ keyword: '分页搜索代理', limit: 20 }).map((proxy) => proxy.name)
    assert(proxyOptionNames.includes('分页搜索代理'), '代理选项搜索应返回已启用匹配代理')
    assert(!proxyOptionNames.includes('分页搜索代理停用'), '代理选项不应返回停用代理')
    const proxyOptionLimitRows = repositories.listProxyOptions({ keyword: '选项上限代理', limit: 500 })
    assert.equal(proxyOptionLimitRows.length, 50, '代理选项即使调用方传入 500 也最多返回 50 条')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 8, '回归应捕获团队和代理列表 SQL')
  for (const call of capturedCalls) {
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '团队 / 代理搜索不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), '团队 / 代理前缀搜索应显式转义 LIKE 通配符')
    }
    assert(!/\bdescription\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '团队 / 代理关键词搜索不应把 description 放进 WHERE')
    assert(!/\bsystem_teams\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql), '系统团队列表搜索不应把团队 ID 放进通用关键词 WHERE')
    assert(!/\bproxy_profiles\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql) && !/\bid\s+(?:=|LIKE)\s+\?/i.test(call.sql), '代理列表搜索不应把 ID 放进通用关键词 WHERE')
    assert(!/\bhost\s+(?:=|LIKE)\s+\?/i.test(call.sql), '代理列表搜索不应把地址放进通用关键词 WHERE')
    assert(!/\btype\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '代理列表搜索不应把类型放进通用关键词 WHERE')
    assert(!/\busername\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '代理关键词搜索不应把 username 放进 WHERE')
  }
  assertBusinessIndexExists('idx_system_teams_name_lookup')
  assertBusinessIndexExists('idx_proxy_profiles_name_lookup')
  assertObsoleteProxySearchIndexesDroppedBySchema()
  assertBusinessIndexMissing('idx_proxy_profiles_host_lookup')
  assertBusinessIndexMissing('idx_proxy_profiles_type_lookup')

  console.log('系统团队和代理分页搜索回归通过：分页使用 pageSize+1，关键词仅按名称精确/前缀匹配并转义通配符')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
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

function assertObsoleteProxySearchIndexesDroppedBySchema(): void {
  const database = databaseModule.getDatabase()
  database.exec(`
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_host_lookup ON proxy_profiles(host, id);
    CREATE INDEX IF NOT EXISTS idx_proxy_profiles_type_lookup ON proxy_profiles(type COLLATE NOCASE, id);
  `)
  businessSchema.applyBusinessSchema(database)
}

function assertBusinessIndexMissing(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, undefined, `业务库不应保留已废弃的代理搜索索引 ${indexName}`)
}
