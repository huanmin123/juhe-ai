import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import { maxAccountExpirySweepBatchSize } from '../../storage/account-sweep-limits.js'

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
  assertAccountListRouteBoundary()

  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const matchedGroup = repositories.createGroup({
    name: '账户绑定前缀分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const middleGroup = repositories.createGroup({
    name: '普通账户绑定前缀分组',
    providerCode: 'gpt',
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
  const maxLengthTailName = `${'长'.repeat(124)}末尾片段`
  const maxLengthTailAccount = createGuardAccount(maxLengthTailName, 'sk-account-list-query-guard-max-tail', '最大长度末尾片段', matchedGroup.id)
  const disabledStatusAccount = createGuardAccount('多状态筛选停用账户', 'sk-account-list-query-guard-disabled-status', '停用状态筛选', matchedGroup.id, 'disabled')
  const errorStatusAccount = createGuardAccount('多状态筛选异常账户', 'sk-account-list-query-guard-error-status', '异常状态筛选', matchedGroup.id, 'error')
  databaseModule.getBusinessDatabase()
    .prepare(`UPDATE accounts SET status = 'error', schedulable = 0 WHERE id = ?`)
    .run(errorStatusAccount.id)
  assert.equal([...maxLengthTailName].length, 128, '回归账户名称应覆盖账户名称最大长度边界')
  assert.throws(
    () => createGuardAccount(`${'超'.repeat(129)}`, 'sk-account-list-query-guard-name-too-long', '超长名称', matchedGroup.id),
    /账户名称不能超过 128 个字符/,
    'AI 账户名称应限制最大长度，保证名称搜索词项规模可控'
  )
  const grantee = repositories.createSystemAccount({
    username: 'account_list_query_guard_grantee',
    displayName: '账户列表防护被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const granteeTargetGroup = repositories.createGroup({
    name: '账户列表防护被授权目标分组',
    providerCode: 'gpt',
  }, granteeAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: matchedByName.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id
  }, access)
  const authorizedInstance = databaseModule.getBusinessDatabase()
    .prepare('SELECT id FROM accounts WHERE authorization_instance_source_account_id = ? AND system_account_id = ? LIMIT 1')
    .get(matchedByName.id, grantee.id) as unknown as { id?: string } | undefined
  assert(authorizedInstance?.id, '账号授权应在创建授权时物化被授权实例')

  const database = databaseModule.getBusinessDatabase()
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
    assert(nameIds.includes(middleNameOnly.id), 'AI 账户搜索应命中名称中间包含值')

    const singleChineseResult = repositories.listAccountsPage(access, { keyword: '索', page: 1, pageSize: 20 })
    assert(singleChineseResult.items.some((item) => item.id === middleNameOnly.id), 'AI 账户搜索应支持中文单字包含值')

    const doubleChineseResult = repositories.listAccountsPage(access, { keyword: '检索', page: 1, pageSize: 20 })
    assert(doubleChineseResult.items.some((item) => item.id === middleNameOnly.id), 'AI 账户搜索应支持中文双字包含值')

    const maxLengthTailResult = repositories.listAccountsPage(access, { keyword: '末尾片段', page: 1, pageSize: 20 })
    assert(maxLengthTailResult.items.some((item) => item.id === maxLengthTailAccount.id), 'AI 账户搜索应命中最大长度账户名末尾片段')

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
    assert(invalidNotesSortResult.items.some((item) => item.id === matchedByName.id), 'AI 账户列表应忽略不支持的备注排序并继续返回结果')
    const invalidNotesSortCalls = capturedCalls.slice(invalidNotesSortCapturedStart)
    assert(invalidNotesSortCalls.length >= 1, '回归应捕获不支持备注排序的列表 SQL')
    for (const call of invalidNotesSortCalls) {
      assert(!/\bORDER\s+BY[\s\S]*\baccount_rows\.notes\s+COLLATE\s+NOCASE\b/i.test(call.sql), 'AI 账户列表不应允许按备注长文本排序')
    }
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 5, '回归应捕获 AI 账户列表 SQL')
  assert(
    capturedCalls.some((call) => /\binstr\s*\(\s*documents\.normalized_name\s*,\s*\?\s*\)\s*>\s*0/i.test(call.sql)),
    'AI 账户列表名称包含匹配应使用搜索文档字面包含确认'
  )
  for (const call of capturedCalls) {
    assert(!/\bCOALESCE\s*\(\s*account_rows\.notes\b/i.test(call.sql), 'AI 账户列表搜索不应通过 COALESCE 扫描备注字段')
    assert(!/\baccount_rows\.notes\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表搜索不应把备注字段放进通用关键词 WHERE')
    assert(!/\bCOALESCE\s*\(\s*bound_groups\.name\b/i.test(call.sql), 'AI 账户列表搜索不应通过 COALESCE 扫描分组名称')
    assert(!/\bbound_groups\.name\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表搜索不应把分组名称放进通用关键词 WHERE')
    assert(!/\baccount_rows\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql), 'AI 账户列表名称搜索不应把账户 ID 放进通用关键词 WHERE')
    assert(!/\baccount_rows\.provider_code\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表名称搜索不应把供应商编码放进通用关键词 WHERE')
    assert(!/\baccount_rows\.type\s+(?:COLLATE|LIKE)\b/i.test(call.sql), 'AI 账户列表名称搜索不应把账户类型放进通用关键词 WHERE')
    assert(!/\bLIKE\s+\?/i.test(call.sql), 'AI 账户列表名称搜索不应使用 LIKE，避免大小写折叠或通配符语义')
    if (/\binstr\s*\(\s*documents\.normalized_name\s*,\s*\?\s*\)\s*>\s*0/i.test(call.sql)) {
      assert(
        /\binstr\s*\(\s*documents\.normalized_name\s*,\s*\?\s*\)\s*>\s*0/i.test(call.sql),
        'AI 账户列表包含匹配只能落到账户名称规范化搜索文档'
      )
      assert(
        /\baccount_rows\.id\s+IN\s*\(/i.test(call.sql),
        'AI 账户列表包含匹配必须先由账户名称词项索引收敛候选 ID'
      )
    }
  }
  for (const indexName of [
    'idx_accounts_name_lookup',
    'idx_accounts_system_account_name_lookup',
    'idx_accounts_owner_list_order',
    'idx_account_name_search_terms_term_owner',
    'idx_account_name_search_terms_account',
    'idx_account_name_search_documents_owner',
    'idx_groups_name_lookup',
    'idx_groups_system_account_name_lookup'
  ]) {
    assertBusinessIndexExists(indexName)
  }
  assertAccountNameSearchTermRows(middleNameOnly.id)
  assertAccountNameSearchCandidateQueryPlan(middleNameOnly.id)
  for (const indexName of [
    'idx_accounts_notes_lookup',
    'idx_accounts_system_account_notes_lookup'
  ]) {
    assertBusinessIndexMissing(indexName)
  }

  assertNoAuthorizationInstanceBackfillScan(granteeAccess, authorizedInstance.id)
  assertExpiredAccountCleanupIsBoundedAndIndexed(access)

  console.log('AI 账户列表查询防护回归通过：搜索仅按账户名称精确/前缀/索引候选包含匹配，分组使用独立筛选，请求路径不再按被授权人全量回扫授权实例或无界清理过期账号')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
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
	    providerCode: 'gpt',
	    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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

function assertAccountListRouteBoundary(): void {
  const accountsRoutesSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
  const accountListRoutesSource = readFileSync(resolve('src/modules/accounts/account-list.routes.ts'), 'utf8')
  const accountListRuntimeStatusFilterSource = readFileSync(resolve('src/modules/accounts/account-list-runtime-status-filter.ts'), 'utf8')
  const accountSummaryRepositorySource = readFileSync(resolve('src/storage/account-summary.repository.ts'), 'utf8')
  const accountOptionsRepositorySource = readFileSync(resolve('src/storage/account-options.repository.ts'), 'utf8')
  const accountReadRepositorySource = readFileSync(resolve('src/storage/account-read.repository.ts'), 'utf8')
  assert(
    accountsRoutesSource.includes('registerAccountListRoutes(accountsRouter)'),
    '账户主路由应注册账户列表只读子路由'
  )
  assert(
    !accountsRoutesSource.includes('listAccountsPage(') && !accountsRoutesSource.includes('listAccountOptions('),
    '账户主路由不应直接承载列表 / options 查询'
  )
  assert(
    accountListRoutesSource.includes("router.get('/',")
      && accountListRoutesSource.includes("router.get('/options'")
      && accountListRoutesSource.includes('listAccountsPageAsync(')
      && accountListRoutesSource.includes('listAccountOptionsAsync('),
    '账户列表只读子路由应承接列表和 options 查询'
  )
  assert(
    accountListRoutesSource.includes('applyServerAccountConcurrencyToAccountList')
      && accountListRoutesSource.includes('applyAccountListRuntimeStatusFilter')
      && accountListRoutesSource.includes('Server-Timing')
      && accountListRoutesSource.includes('sanitizeAccountListResponse'),
    '账户列表只读子路由应保留并发水合、运行态状态后置归类、Server-Timing 和响应脱敏'
  )
  assert.match(
    accountListRoutesSource,
    /accountListNeedsRuntimeStatusFilter\(listOptions\)[\s\S]*listAccountsPageWithRuntimeStatusFilter\(requestAccess, listOptions\)[\s\S]*if \(!filteredResult\) \{[\s\S]*listAccountsPageAsync\(requestAccess, listOptions\)/,
    '账户列表运行态状态过滤应先走单次运行态分页，快照不可用时才回退普通列表，避免 PG 状态过滤重复昂贵查询'
  )
  assert(
    accountListRuntimeStatusFilterSource.includes('export function accountListNeedsRuntimeStatusFilter')
      && accountListRuntimeStatusFilterSource.includes('export async function listAccountsPageWithRuntimeStatusFilter')
      && accountListRuntimeStatusFilterSource.includes('peekServerAccountRuntimeAvailabilitySnapshot')
      && accountListRuntimeStatusFilterSource.includes('if (!runtimeAvailability) return undefined'),
    '运行态状态过滤模块应暴露显式接管入口，并保留只用已有快照、快照不可用时回退普通列表的契约'
  )
  assert(
    !accountListRoutesSource.includes('mutationGuard(')
      && !accountListRoutesSource.includes('recordOperationLog(')
      && !accountListRoutesSource.includes('createAccount('),
    '账户列表只读子路由不应引入写操作、操作日志或 mutation guard'
  )
  assert(
    !accountSummaryRepositorySource.includes('listAccountsPageWithDerivedStatusFilter')
      && !accountOptionsRepositorySource.includes('queryAccountOptionRowsForAccessWithDerivedStatusFilter'),
    '账户列表和 options 状态归类不应通过仓储层无界翻页后过滤实现'
  )
  assert.match(
    accountSummaryRepositorySource,
    /accounts\.name COLLATE "C" >= \?[\s\S]+accounts\.name COLLATE "C" < \?/,
    'PG 自有账户列表名称前缀搜索必须使用大小写敏感 C collation 范围条件，避免 LIKE 扫描'
  )
  assert.match(
    accountSummaryRepositorySource,
    /accountNamePrefixUpperBound\(keywordPrefix\)/,
    'PG 自有账户列表名称前缀搜索必须使用代码点上界，避免固定 \\uffff 在 PG 排序规则下失效'
  )
  assert.doesNotMatch(
    accountSummaryRepositorySource,
    /\$\{normalizedKeywordPrefix\}\\uffff/,
    'PG 自有账户列表名称前缀搜索不能使用固定 \\uffff 上界'
  )
  assert.doesNotMatch(
    accountSummaryRepositorySource,
    /lower\(accounts\.name\)\s+LIKE\s+lower\(\?\)/,
    'PG 自有账户列表名称前缀搜索不能回退 lower(name) LIKE lower(?)'
  )
  assert.doesNotMatch(
    accountSummaryRepositorySource,
    /lower\(accounts\.name\)\s+>=\s+\?/,
    'PG 自有账户列表名称前缀搜索不能折叠账户名称大小写'
  )
  assert.match(
    accountOptionsRepositorySource,
    /accounts\.name COLLATE "C" >= \?[\s\S]+accounts\.name COLLATE "C" < \?/,
    'PG 账户 options 名称前缀搜索必须使用大小写敏感 C collation 范围条件，避免 LIKE 扫描'
  )
  assert.match(
    accountOptionsRepositorySource,
    /accountOptionNamePrefixUpperBound\(keywordPrefix\)/,
    'PG 账户 options 名称前缀搜索必须使用代码点上界，避免固定 \\uffff 在 PG 排序规则下失效'
  )
  assert.doesNotMatch(
    accountOptionsRepositorySource,
    /\$\{normalizedKeywordPrefix\}\\uffff/,
    'PG 账户 options 名称前缀搜索不能使用固定 \\uffff 上界'
  )
  assert.doesNotMatch(
    accountOptionsRepositorySource,
    /lower\(accounts\.name\)\s+LIKE\s+lower\(\?\)/,
    'PG 账户 options 名称前缀搜索不能回退 lower(name) LIKE lower(?)'
  )
  assert.doesNotMatch(
    accountOptionsRepositorySource,
    /lower\(accounts\.name\)\s+>=\s+\?/,
    'PG 账户 options 名称前缀搜索不能折叠账户名称大小写'
  )
  assert(
    accountReadRepositorySource.includes('accountApiKeyPoolAllUnavailableSql')
      && accountOptionsRepositorySource.includes('accountApiKeyPoolAllUnavailableSql'),
    '账户列表和 options 派生状态应下推 Key 池 SQL 判断'
  )
  assert(
    !accountReadRepositorySource.includes('availability_schedule_active')
      && !accountOptionsRepositorySource.includes('availability_schedule_active'),
    '账户列表和 options 不应再读取旧账户时间计划派生列'
  )
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function assertBusinessIndexMissing(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, undefined, `业务库不应创建长文本搜索索引 ${indexName}`)
}

function assertAccountNameSearchTermRows(accountId: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM account_name_search_terms WHERE account_id = ?')
    .get(accountId) as unknown as { count?: number } | undefined
  assert(Number(row?.count ?? 0) > 0, 'AI 账户名称搜索词项应随账户创建写入')
}

function assertAccountNameSearchCandidateQueryPlan(accountId: string): void {
  const termRow = databaseModule.getBusinessDatabase()
    .prepare('SELECT term FROM account_name_search_terms WHERE account_id = ? AND length(term) = 3 ORDER BY term ASC LIMIT 1')
    .get(accountId) as unknown as { term?: string } | undefined
  assert(termRow?.term, 'AI 账户名称包含匹配回归需要三元搜索词项')
  const details = explainBusinessQuery(`
    SELECT search.account_id
    FROM account_name_search_terms search INDEXED BY idx_account_name_search_terms_term_owner
    INNER JOIN account_name_search_documents documents ON documents.account_id = search.account_id
    INNER JOIN accounts ON accounts.id = search.account_id
    WHERE search.term IN (?)
      AND instr(documents.normalized_name, ?) > 0
      AND accounts.deleted_at IS NULL
    GROUP BY search.account_id
    HAVING COUNT(DISTINCT search.term) = ?
  `, [termRow.term, termRow.term, 1])
  assert(details.includes('idx_account_name_search_terms_term_owner'), `AI 账户名称包含候选查询必须走词项索引，实际计划：${details}`)
  assert(!details.includes('SCAN accounts'), `AI 账户名称包含候选查询不能扫描 accounts 主表，实际计划：${details}`)
}

function assertNoAuthorizationInstanceBackfillScan(access: { systemAccountId: string; role: 'user' }, accountId: string): void {
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedSql: string[] = []
  database.prepare = ((sql: string) => {
    if (
      /\bFROM\s+resource_authorizations\b/i.test(sql)
      && /\bresource_type\s*=\s*'account'/i.test(sql)
      && /\bgrantee_system_account_id\s*=\s*\?/i.test(sql)
      && /\bORDER\s+BY\s+updated_at\s+ASC,\s+id\s+ASC\b/i.test(sql)
    ) {
      capturedSql.push(sql)
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    const page = repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
    assert(page.items.some((item) => item.id === accountId), '账户列表仍应返回已物化的授权实例')
    const options = repositories.listAccountOptions(access, { limit: 20 })
    assert(options.some((item) => item.id === accountId), '账户 options 仍应返回已物化的授权实例')
    const detail = repositories.findAccountSummary(accountId, access)
    assert.equal(detail?.id, accountId, '账户详情仍应读取已物化的授权实例')
  } finally {
    database.prepare = originalPrepare
  }
  assert.equal(capturedSql.length, 0, `账户读取请求路径不应按被授权人全量扫描账号授权并补实例，实际 SQL：${capturedSql.join('\n')}`)
}

function assertExpiredAccountCleanupIsBoundedAndIndexed(access: { systemAccountId: string; role: 'admin' }): void {
  assertAccountExpirySweepQueryPlan()
  assertAccountExpirySweepQueryPlan(access.systemAccountId)

  const group = repositories.createGroup({
    name: '过期账号读路径防写分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const accountIds: string[] = []
  for (let index = 0; index < maxAccountExpirySweepBatchSize + 1; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: `过期账号读路径防写 ${String(index).padStart(2, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-account-expiry-batch-guard-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id,
      status: 'active'
    }, access)
    accountIds.push(account.id)
  }
  const expiredAt = new Date(Date.now() - 60_000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET status = 'temporary_unavailable',
          schedulable = 1,
          cooldown_until = ?,
          account_expires_at = ?,
          updated_at = ?
      WHERE id IN (${placeholders(accountIds.length)})
    `)
    .run(expiredAt, expiredAt, expiredAt, ...accountIds)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedUpdates: string[] = []
  database.prepare = ((sql: string) => {
    if (/\bUPDATE\s+accounts\b/i.test(sql)) {
      capturedUpdates.push(sql)
    }
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    repositories.listAccountsPage(access, { page: 1, pageSize: 20 })
    repositories.listAccountOptions(access, { limit: 20 })
    repositories.findAccountSummary(accountIds[0] ?? '', access)
  } finally {
    database.prepare = originalPrepare
  }

  assert.equal(capturedUpdates.length, 0, `账户读取请求路径不应更新 accounts；过期账号停用必须交给后台 sweep，实际 SQL：${capturedUpdates.join('\n')}`)
  assert.equal(expiredDisabledCount(accountIds), 0, '账户读取请求不应停用过期账号')
  assert.equal(expiredPendingCount(accountIds), maxAccountExpirySweepBatchSize + 1, '过期账号应保留给后台 sweep 处理，列表只计算展示态')
}

function assertAccountExpirySweepQueryPlan(systemAccountId?: string): void {
  const scoped = Boolean(systemAccountId)
  const details = explainBusinessQuery(`
    SELECT id
    FROM accounts
    WHERE account_expires_at IS NOT NULL
      AND account_expires_at <= ?
      AND (
        status <> 'disabled'
        OR schedulable <> 0
        OR cooldown_until IS NOT NULL
        OR last_error_code IS NOT NULL
        OR last_error_message IS NULL
      )${scoped ? ' AND system_account_id = ?' : ''}
    ORDER BY account_expires_at ASC, updated_at ASC, id ASC
    LIMIT ?
  `, scoped
    ? ['2026-01-01T00:00:00.000Z', systemAccountId as string, maxAccountExpirySweepBatchSize]
    : ['2026-01-01T00:00:00.000Z', maxAccountExpirySweepBatchSize])
  const indexName = scoped ? 'idx_accounts_owner_expiry_sweep' : 'idx_accounts_expiry_sweep'
  assert(details.includes(indexName), `过期账号清理应走到期时间部分索引 ${indexName}，实际计划：${details}`)
  assert(!details.includes('SCAN accounts'), `过期账号清理不能全表扫描 accounts，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `过期账号清理不应为排序创建临时 B-TREE，实际计划：${details}`)
}

function expiredDisabledCount(accountIds: string[]): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT COUNT(*) AS count
      FROM accounts
      WHERE id IN (${placeholders(accountIds.length)})
        AND status = 'disabled'
        AND schedulable = 0
        AND last_error_code = 'account_expired'
    `)
    .get(...accountIds) as unknown as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function expiredPendingCount(accountIds: string[]): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT COUNT(*) AS count
      FROM accounts
      WHERE id IN (${placeholders(accountIds.length)})
        AND status = 'temporary_unavailable'
        AND account_expires_at IS NOT NULL
    `)
    .get(...accountIds) as unknown as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}

function placeholders(count: number): string {
  return Array.from({ length: count }, () => '?').join(', ')
}
