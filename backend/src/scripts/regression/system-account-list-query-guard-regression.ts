import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-account-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-account-list-query-guard-secret'
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
  const exactUser = repositories.createSystemAccount({
    username: 'system_account_list_user',
    displayName: '系统账户列表用户',
    description: '普通说明',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const prefixUser = repositories.createSystemAccount({
    username: 'system_account_list_user_prefix',
    displayName: '系统账户列表用户扩展',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const middleUser = repositories.createSystemAccount({
    username: 'system_account_list_middle',
    displayName: '普通系统账户列表用户中间',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const descriptionOnlyUser = repositories.createSystemAccount({
    username: 'system_account_list_description',
    displayName: '说明字段用户',
    description: '系统账户列表用户说明前缀',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const wildcardLiteralUser = repositories.createSystemAccount({
    username: 'sysacc_percent%literal',
    displayName: 'sysacc_percent%literal',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const wildcardNeighborUser = repositories.createSystemAccount({
    username: 'sysacc_percentXliteral',
    displayName: 'sysacc_percentXliteral',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/^\s*SELECT\b/i.test(sql) && /\bFROM\s+system_accounts\b/i.test(sql) && /\bORDER\s+BY\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const firstPage = repositories.listSystemAccountsPage({ page: 1, pageSize: 1 })
    assert.equal(firstPage.items.length, 1, '系统账户列表第一页应只返回 pageSize 条')
    assert.equal(firstPage.hasMore, true, '系统账户列表应通过 pageSize + 1 判断是否还有更多')
    assert(firstPage.total >= 2, '系统账户列表 total 应提供分页上界')

    const searchResult = repositories.listSystemAccountsPage({ keyword: '系统账户列表用户', page: 1, pageSize: 20 })
    const searchIds = searchResult.items.map((account) => account.id)
    assert(searchIds.includes(exactUser.id), '系统账户列表搜索应命中显示名称精确值')
    assert(searchIds.includes(prefixUser.id), '系统账户列表搜索应命中显示名称前缀值')
    assert(!searchIds.includes(middleUser.id), '系统账户列表搜索不应命中显示名称中间包含')
    assert(!searchIds.includes(descriptionOnlyUser.id), '系统账户列表搜索不应命中说明字段')

    const usernameResult = repositories.listSystemAccountsPage({ keyword: 'system_account_list_user', page: 1, pageSize: 20 })
    const usernameIds = usernameResult.items.map((account) => account.id)
    assert(usernameIds.includes(exactUser.id), '系统账户列表搜索应命中用户名精确值')
    assert(usernameIds.includes(prefixUser.id), '系统账户列表搜索应命中用户名前缀值')
    assert(!usernameIds.includes(middleUser.id), '系统账户列表搜索不应命中用户名中间包含')

    const wildcardResult = repositories.listSystemAccountsPage({ keyword: 'sysacc_percent%', page: 1, pageSize: 20 })
    const wildcardIds = wildcardResult.items.map((account) => account.id)
    assert(wildcardIds.includes(wildcardLiteralUser.id), '系统账户列表搜索应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(wildcardNeighborUser.id), '系统账户列表搜索不应把用户输入的 % 当作 LIKE 通配符')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 4, '回归应捕获系统账户列表 SQL')
  for (const call of capturedCalls) {
    assert(/\bLIMIT\s+\?\s+OFFSET\s+\?/i.test(call.sql), '系统账户列表必须分页查询')
    assert(!/\bpassword_hash\b/i.test(call.sql), '系统账户列表不应读取 password_hash')
    assert(!/\bid\s+(?:=|LIKE)\s+\?/i.test(call.sql), '系统账户管理列表搜索不应把 ID 放进通用关键词 WHERE')
    assert(!/\bdescription\s+(?:COLLATE|LIKE)\b/i.test(call.sql), '系统账户列表关键词搜索不应扫描 description')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '系统账户列表搜索不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), '系统账户列表前缀搜索应显式转义 LIKE 通配符')
    }
  }
  assertBusinessIndexExists('idx_system_accounts_updated_lookup')
  assertBusinessIndexExists('idx_system_accounts_username_lookup')
  assertBusinessIndexExists('idx_system_accounts_display_name_lookup')

  console.log('系统账户列表查询防护回归通过：列表分页读取，关键词按用户名和用户名称精确/前缀匹配且不读取密码哈希')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}
