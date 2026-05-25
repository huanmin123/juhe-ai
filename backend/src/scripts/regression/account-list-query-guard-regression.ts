import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-list-query-guard-secret'
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
  const matchedGroup = repositories.createGroup({
    name: '账户绑定前缀分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const middleGroup = repositories.createGroup({
    name: '普通账户绑定前缀分组',
    providerCode: 'openai',
    enabled: true
  }, access)
  const matchedByName = createGuardAccount('账户检索目标', 'sk-account-list-query-guard-name', '普通备注', matchedGroup.id)
  const matchedByNamePrefix = createGuardAccount('账户检索目标扩展', 'sk-account-list-query-guard-name-prefix', '普通备注扩展', matchedGroup.id)
  const middleNameOnly = createGuardAccount('普通账户检索目标', 'sk-account-list-query-guard-name-middle', '普通备注中间', matchedGroup.id)
  const matchedByNotes = createGuardAccount('备注字段账户', 'sk-account-list-query-guard-notes', '备注前缀命中', matchedGroup.id)
  const middleNotesOnly = createGuardAccount('普通备注账户', 'sk-account-list-query-guard-notes-middle', '普通备注前缀命中', matchedGroup.id)
  const matchedByGroup = createGuardAccount('绑定分组命中账户', 'sk-account-list-query-guard-group', '普通备注绑定分组', matchedGroup.id)
  const middleGroupOnly = createGuardAccount('绑定分组中间账户', 'sk-account-list-query-guard-group-middle', '普通备注绑定分组中间', middleGroup.id)
  const wildcardLiteral = createGuardAccount('percent%literal 账户', 'sk-account-list-query-guard-percent-literal', '通配符字面量', matchedGroup.id)
  const wildcardNeighbor = createGuardAccount('percentXliteral 账户', 'sk-account-list-query-guard-percent-neighbor', '通配符邻近值', matchedGroup.id)
  const disabledStatusAccount = createGuardAccount('多状态筛选停用账户', 'sk-account-list-query-guard-disabled-status', '停用状态筛选', matchedGroup.id, 'disabled')
  const errorStatusAccount = createGuardAccount('多状态筛选异常账户', 'sk-account-list-query-guard-error-status', '异常状态筛选', matchedGroup.id, 'error')

  const database = databaseModule.getDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\baccount_rows\.name\b/i.test(sql) && /\bFROM\s+\(/i.test(sql) && /\bORDER\s+BY\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const nameResult = repositories.listAccountsPage(access, { keyword: '账户检索目标', page: 1, pageSize: 20 })
    const nameIds = nameResult.items.map((item) => item.id)
    assert(nameIds.includes(matchedByName.id), 'AI 账户搜索应命中名称精确值')
    assert(nameIds.includes(matchedByNamePrefix.id), 'AI 账户搜索应命中名称前缀值')
    assert(!nameIds.includes(middleNameOnly.id), 'AI 账户搜索不应命中名称中间包含值')

    const notesResult = repositories.listAccountsPage(access, { keyword: '备注前缀', page: 1, pageSize: 20 })
    const notesIds = notesResult.items.map((item) => item.id)
    assert(!notesIds.includes(matchedByNotes.id), 'AI 账户搜索不应通过备注字段命中，避免通用关键词扫描长文本')
    assert(!notesIds.includes(middleNotesOnly.id), 'AI 账户搜索不应命中备注中间包含值')

    const groupResult = repositories.listAccountsPage(access, { keyword: '账户绑定前缀', page: 1, pageSize: 20 })
    const groupIds = groupResult.items.map((item) => item.id)
    assert(!groupIds.includes(matchedByGroup.id), 'AI 账户名称搜索不应通过绑定分组名命中')
    assert(!groupIds.includes(middleGroupOnly.id), 'AI 账户搜索不应命中绑定分组名中间包含值')

    const groupFilterResult = repositories.listAccountsPage(access, { groupId: matchedGroup.id, page: 1, pageSize: 20 })
    const groupFilterIds = groupFilterResult.items.map((item) => item.id)
    assert(groupFilterIds.includes(matchedByGroup.id), 'AI 账户分组筛选应命中所选分组绑定账户')
    assert(!groupFilterIds.includes(middleGroupOnly.id), 'AI 账户分组筛选不应混入其他分组绑定账户')

    const idResult = repositories.listAccountsPage(access, { keyword: matchedByName.id, page: 1, pageSize: 20 })
    assert(!idResult.items.some((item) => item.id === matchedByName.id), 'AI 账户名称搜索不应支持账户 ID 定位')

    const providerResult = repositories.listAccountsPage(access, { keyword: 'openai', page: 1, pageSize: 20 })
    assert(!providerResult.items.some((item) => item.id === matchedByName.id), 'AI 账户名称搜索不应通过供应商编码命中')

    const typeResult = repositories.listAccountsPage(access, { keyword: 'api_key', page: 1, pageSize: 20 })
    assert(!typeResult.items.some((item) => item.id === matchedByName.id), 'AI 账户名称搜索不应通过账户类型命中')

    const wildcardResult = repositories.listAccountsPage(access, { keyword: 'percent%', page: 1, pageSize: 20 })
    const wildcardIds = wildcardResult.items.map((item) => item.id)
    assert(wildcardIds.includes(wildcardLiteral.id), 'AI 账户搜索应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(wildcardNeighbor.id), 'AI 账户搜索不应把用户输入的 % 当作 LIKE 通配符')

    const multiStatusCapturedStart = capturedCalls.length
    const multiStatusResult = repositories.listAccountsPage(access, { status: 'disabled,error', page: 1, pageSize: 20 })
    const multiStatusIds = multiStatusResult.items.map((item) => item.id)
    assert(multiStatusIds.includes(disabledStatusAccount.id), 'AI 账户列表多状态筛选应命中停用账户')
    assert(multiStatusIds.includes(errorStatusAccount.id), 'AI 账户列表多状态筛选应命中异常账户')
    assert(!multiStatusIds.includes(matchedByName.id), 'AI 账户列表多状态筛选不应混入未勾选状态')
    capturedCalls.splice(multiStatusCapturedStart)

    const multiStatusOptions = repositories.listAccountOptions(access, { status: 'disabled,error', limit: 20 })
    const multiStatusOptionIds = multiStatusOptions.map((item) => item.id)
    assert(multiStatusOptionIds.includes(disabledStatusAccount.id), 'AI 账户 options 多状态筛选应命中停用账户')
    assert(multiStatusOptionIds.includes(errorStatusAccount.id), 'AI 账户 options 多状态筛选应命中异常账户')
    assert(!multiStatusOptionIds.includes(matchedByName.id), 'AI 账户 options 多状态筛选不应混入未勾选状态')

    const invalidNotesSortCapturedStart = capturedCalls.length
    const invalidNotesSortResult = repositories.listAccountsPage(access, {
      keyword: '账户检索目标',
      sorts: [{ field: 'notes', order: 'asc' } as never],
      page: 1,
      pageSize: 20
    })
    assert(invalidNotesSortResult.items.some((item) => item.id === matchedByName.id), 'AI 账户列表应忽略已废弃的备注排序并继续返回结果')
    const invalidNotesSortCalls = capturedCalls.slice(invalidNotesSortCapturedStart)
    assert(invalidNotesSortCalls.length >= 1, '回归应捕获已废弃备注排序的列表 SQL')
    for (const call of invalidNotesSortCalls) {
      assert(!/\bORDER\s+BY[\s\S]*\baccount_rows\.notes\s+COLLATE\s+NOCASE\b/i.test(call.sql), 'AI 账户列表不应允许按备注长文本排序')
    }
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 5, '回归应捕获 AI 账户列表 SQL')
  for (const call of capturedCalls) {
    assert(!/\bCOALESCE\s*\(\s*account_rows\.notes\b/i.test(call.sql), 'AI 账户列表搜索不应通过 COALESCE 扫描备注字段')
    assert(!/\baccount_rows\.notes\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表搜索不应把备注字段放进通用关键词 WHERE')
    assert(!/\bCOALESCE\s*\(\s*bound_groups\.name\b/i.test(call.sql), 'AI 账户列表搜索不应通过 COALESCE 扫描分组名称')
    assert(!/\bbound_groups\.name\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表搜索不应把分组名称放进通用关键词 WHERE')
    assert(!/\baccount_rows\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql), 'AI 账户列表名称搜索不应把账户 ID 放进通用关键词 WHERE')
    assert(!/\baccount_rows\.provider_code\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表名称搜索不应把供应商编码放进通用关键词 WHERE')
    assert(!/\baccount_rows\.type\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表名称搜索不应把账户类型放进通用关键词 WHERE')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), 'AI 账户列表前缀搜索应显式转义 LIKE 通配符')
    }
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), 'AI 账户列表搜索不应传入前导通配符参数')
  }
  for (const indexName of [
    'idx_accounts_name_lookup',
    'idx_accounts_system_account_name_lookup',
    'idx_groups_name_lookup',
    'idx_groups_system_account_name_lookup'
  ]) {
    assertBusinessIndexExists(indexName)
  }
  for (const indexName of [
    'idx_accounts_notes_lookup',
    'idx_accounts_system_account_notes_lookup'
  ]) {
    assertBusinessIndexMissing(indexName)
  }

  console.log('AI 账户列表查询防护回归通过：搜索仅按账户名称精确/前缀匹配，分组使用独立筛选')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createGuardAccount(
  name: string,
  apiKey: string,
  notes: string,
  groupId: string,
  status: 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable' = 'active'
): { id: string } {
  return repositories.createAccount({
    providerCode: 'openai',
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'https://api.openai.com/v1'
    },
    notes,
    groupId,
    status
  }, { systemAccountId: 'sys_admin', role: 'admin' as const })
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function assertBusinessIndexMissing(indexName: string): void {
  const row = databaseModule.getDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, undefined, `业务库不应保留已废弃的长文本搜索索引 ${indexName}`)
}
