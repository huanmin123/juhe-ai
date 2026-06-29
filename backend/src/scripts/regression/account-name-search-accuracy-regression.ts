import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-name-search-accuracy-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-name-search-accuracy-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountNameSearch] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-name-search.repository.js')
])

type TrackedAccount = {
  id: string
  ownerId: string
  name: string
}

const adminAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'admin' }
const trackedIds = new Set<string>()

try {
  const alpha = repositories.createSystemAccount({
    username: 'account_name_search_alpha',
    displayName: '账户名搜索准确性甲',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const beta = repositories.createSystemAccount({
    username: 'account_name_search_beta',
    displayName: '账户名搜索准确性乙',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const alphaAccess: AccessScope = { systemAccountId: alpha.id, role: 'user' }
  const betaAccess: AccessScope = { systemAccountId: beta.id, role: 'user' }
  const adminAlphaAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'admin', systemAccountFilterId: alpha.id }
  const adminBetaAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'admin', systemAccountFilterId: beta.id }

  const alphaGroup = repositories.createGroup({ name: '账户名搜索准确性甲分组', providerCode: 'gpt', providerProtocolProfileId: 'profile_gpt_openai_v1' }, alphaAccess)
  const betaGroup = repositories.createGroup({ name: '账户名搜索准确性乙分组', providerCode: 'gpt', providerProtocolProfileId: 'profile_gpt_openai_v1' }, betaAccess)

  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '账户检索目标')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '普通账户检索目标')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, 'AlphaMixedCase-Token')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '全角ＡＢＣ账号１２３')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, 'percent%literal_账户')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '反斜\\literal\\末尾')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '空 格-账户')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '重复重复重复账户')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, 'abc-bcd-cde-def-分散三元片段')
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, `${'长'.repeat(124)}末尾片段`)
  createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '共同名称维度检索')
  createTrackedAccount(betaAccess, betaGroup.id, beta.id, '共同名称维度检索')
  createTrackedAccount(betaAccess, betaGroup.id, beta.id, '用户B账户检索目标')
  createTrackedAccount(betaAccess, betaGroup.id, beta.id, 'BetaMixedCase-Token')

  for (let index = 0; index < 42; index += 1) {
    createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, deterministicAccountName('甲', index))
  }
  for (let index = 0; index < 31; index += 1) {
    createTrackedAccount(betaAccess, betaGroup.id, beta.id, deterministicAccountName('乙', index))
  }

  const renameAccount = createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '重命名前名称中段')
  assertSearchEquals(alphaAccess, '前名称中', '重命名前命中')
  repositories.updateAccount(renameAccount.id, { name: '重命名后名称片段' }, alphaAccess)
  assertSearchEquals(alphaAccess, '前名称中', '重命名后旧词不应命中')
  assertSearchEquals(alphaAccess, '后名称片', '重命名后新词应命中')

  const deleteAccount = createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '删除索引验证中段片段')
  assertSearchEquals(alphaAccess, '验证中段', '删除前应命中')
  assert.equal(repositories.deleteAccount(deleteAccount.id, alphaAccess), true, '删除测试账号应成功')
  assertSearchEquals(alphaAccess, '验证中段', '删除后不应命中')
  assertSearchIndexRows(deleteAccount.id, 0, 0, '删除后搜索索引应被清理')

  const authorizedSource = createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '授权源账户旧片段')
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: authorizedSource.id,
    granteeType: 'system_account',
    granteeId: beta.id,
    targetGroupId: betaGroup.id
  }, alphaAccess)
  trackAuthorizedInstance(authorizedSource.id, beta.id)
  assertSearchEquals(betaAccess, '旧片段', '授权实例创建后应按被授权用户维度可搜')
  repositories.updateAccount(authorizedSource.id, { name: '授权源账户新片段' }, alphaAccess)
  assertSearchEquals(betaAccess, '旧片段', '授权源重命名后旧授权实例名称不应命中')
  assertSearchEquals(betaAccess, '新片段', '授权源重命名后授权实例搜索词应同步')

  assertSearchEquals(adminAccess, '共同名称维度检索', '管理员无用户筛选时应跨用户命中')
  assertSearchEquals(adminAlphaAccess, '共同名称维度检索', '管理员指定用户甲时只命中用户甲')
  assertSearchEquals(adminBetaAccess, '共同名称维度检索', '管理员指定用户乙时只命中用户乙')
  assertSearchEquals(alphaAccess, '共同名称维度检索', '普通用户甲只能命中自己的账户')
  assertSearchEquals(betaAccess, '共同名称维度检索', '普通用户乙只能命中自己的账户')

  for (const query of [
    '索',
    '检索',
    '账户检索目标',
    'mixedcase-token',
    'ABC账号123',
    'percent%',
    'literal_账户',
    '反斜\\literal',
    '空 格',
    '重复重复',
    'abcdef',
    '末尾片段',
    '不存在片段',
    `${'超'.repeat(129)}`
  ]) {
    assertSearchEquals(adminAccess, query, `管理员搜索 ${query}`)
    assertSearchEquals(alphaAccess, query, `用户甲搜索 ${query}`)
    assertSearchEquals(betaAccess, query, `用户乙搜索 ${query}`)
  }

  assertSearchEquals(alphaAccess, 'abcdef', '三元词项全部存在但完整片段不存在时不能误命中')
  assertSearchEquals(alphaAccess, 'bcd-cde', '真实连续片段仍应命中')
  assertSearchEquals(alphaAccess, 'literalX账户', 'LIKE 下划线不能被当成通配符')

  const randomQueries = randomSubstringQueries(36)
  for (const query of randomQueries) {
    assertSearchEquals(adminAccess, query, `随机子串管理员搜索 ${query}`)
    assertSearchEquals(adminAlphaAccess, query, `随机子串管理员甲搜索 ${query}`)
    assertSearchEquals(betaAccess, query, `随机子串用户乙搜索 ${query}`)
  }

  assertAllTrackedTermSizesWithinLimit()
  assertContainsQueryPlanUsesTermIndex(alphaAccess, '检索目标')

  const rebuildTarget = createTrackedAccount(alphaAccess, alphaGroup.id, alpha.id, '重建索引专用中段片段')
  assertSearchEquals(alphaAccess, '专用中段', '重建前应命中')
  databaseModule.getBusinessDatabase().prepare('DELETE FROM account_name_search_terms').run()
  databaseModule.getBusinessDatabase().prepare('DELETE FROM account_name_search_documents').run()
  assert(!searchIds(alphaAccess, '专用中段').includes(rebuildTarget.id), '清空搜索索引后，中间片段不应靠 accounts 主表裸扫命中')
  const rebuildResult = accountNameSearch.rebuildAccountNameSearchTerms(databaseModule.getBusinessDatabase())
  assert(rebuildResult.accountCount >= currentTrackedAccounts().length, '重建脚本应扫描当前未删除账户')
  assert(rebuildResult.termCount > 0, '重建脚本应恢复搜索词项')
  assertSearchEquals(alphaAccess, '专用中段', '重建后应恢复中间片段命中')

  console.log(`AI 账户名称搜索准确性回归通过：覆盖 ${currentTrackedAccounts().length} 个模拟账户、${randomQueries.length + 44} 组固定/随机查询、跨用户维度、重命名、删除、授权实例同步、LIKE 转义、NFKC 归一化和重建索引`)
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createTrackedAccount(
  access: AccessScope,
  groupId: string,
  ownerId: string,
  name: string
): { id: string } {
  const index = trackedIds.size
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-name-search-accuracy-${index}`,
      base_url: 'https://api.openai.com/v1'
    },
    groupId,
    status: 'active'
  }, access)
  trackedIds.add(account.id)
  assertSearchIndexRows(account.id, 1, undefined, `创建账户 ${name} 后应写入搜索索引`)
  return account
}

function trackAuthorizedInstance(sourceAccountId: string, granteeSystemAccountId: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
      LIMIT 1
    `)
    .get(sourceAccountId, granteeSystemAccountId) as unknown as { id?: string } | undefined
  assert(row?.id, '账号授权应创建被授权账户实例')
  trackedIds.add(row.id)
  assertSearchIndexRows(row.id, 1, undefined, '授权实例应写入名称搜索索引')
}

function assertSearchEquals(access: AccessScope, keyword: string, label: string): void {
  const actual = searchIds(access, keyword).sort()
  const expected = expectedSearchIds(access, keyword).sort()
  assert.deepEqual(actual, expected, `${label}：搜索结果不符合独立归一化包含匹配期望\nkeyword=${keyword}\nexpected=${namesForIds(expected)}\nactual=${namesForIds(actual)}`)
}

function searchIds(access: AccessScope, keyword: string): string[] {
  const page = repositories.listAccountsPage(access, {
    keyword,
    page: 1,
    pageSize: 200,
    sorts: [{ field: 'name', order: 'asc' }]
  })
  return page.items
    .map((item) => item.id)
    .filter((id) => trackedIds.has(id))
}

function expectedSearchIds(access: AccessScope, keyword: string): string[] {
  const normalizedKeyword = normalizeForSearch(keyword)
  if (!normalizedKeyword) return []
  const ownerId = visibleOwnerId(access)
  return currentTrackedAccounts()
    .filter((account) => !ownerId || account.ownerId === ownerId)
    .filter((account) => normalizeForSearch(account.name).includes(normalizedKeyword))
    .map((account) => account.id)
}

function visibleOwnerId(access: AccessScope): string | undefined {
  if (access.role === 'admin') {
    return access.systemAccountFilterId?.trim() || undefined
  }
  return access.systemAccountId
}

function currentTrackedAccounts(): TrackedAccount[] {
  if (!trackedIds.size) return []
  const ids = [...trackedIds]
  const rows = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, system_account_id, name
      FROM accounts
      WHERE deleted_at IS NULL
        AND id IN (${placeholders(ids.length)})
      ORDER BY system_account_id ASC, name COLLATE NOCASE ASC, id ASC
    `)
    .all(...ids) as unknown as Array<{ id: string; system_account_id: string; name: string }>
  return rows.map((row) => ({
    id: row.id,
    ownerId: row.system_account_id,
    name: row.name
  }))
}

function namesForIds(ids: string[]): string {
  const names = new Map(currentTrackedAccounts().map((account) => [account.id, `${account.ownerId}:${account.name}`]))
  return ids.map((id) => names.get(id) ?? id).join(', ')
}

function normalizeForSearch(value: string): string {
  return value.normalize('NFKC').toLowerCase().trim()
}

function deterministicAccountName(prefix: '甲' | '乙', index: number): string {
  const chinese = ['北京', '上海', '测试', '检索', '账号', '模型', '中继', '备用', '高速', '中文', '节点', '末尾', '片段']
  const ascii = ['Alpha', 'Beta', 'Case', 'Token', 'Node', 'Key', 'Mix', 'Route', 'Edge']
  const left = chinese[(index * 5 + prefix.charCodeAt(0)) % chinese.length]
  const middle = ascii[(index * 7 + prefix.charCodeAt(0)) % ascii.length]
  const right = chinese[(index * 11 + 3) % chinese.length]
  return `${prefix}${left}-${middle}-${right}-${String(index).padStart(2, '0')}`
}

function randomSubstringQueries(limit: number): string[] {
  const queries = new Set<string>()
  const records = currentTrackedAccounts()
  let seed = 0x5f3759df
  for (let attempt = 0; attempt < limit * 8 && queries.size < limit; attempt += 1) {
    seed = lcg(seed)
    const record = records[seed % records.length]
    const normalized = normalizeForSearch(record.name)
    const chars = [...normalized]
    if (!chars.length) continue
    seed = lcg(seed)
    const start = seed % chars.length
    seed = lcg(seed)
    const length = Math.max(1, Math.min(chars.length - start, 1 + (seed % 6)))
    const query = chars.slice(start, start + length).join('').trim()
    if (query) queries.add(query)
  }
  return [...queries]
}

function lcg(seed: number): number {
  return (Math.imul(seed, 1664525) + 1013904223) >>> 0
}

function assertAllTrackedTermSizesWithinLimit(): void {
  for (const account of currentTrackedAccounts()) {
    const row = databaseModule.getBusinessDatabase()
      .prepare('SELECT COUNT(*) AS count FROM account_name_search_terms WHERE account_id = ?')
      .get(account.id) as unknown as { count?: number } | undefined
    const termCount = Number(row?.count ?? 0)
    assert(termCount > 0, `账户 ${account.name} 应存在搜索词项`)
    assert(termCount <= 381, `账户 ${account.name} 搜索词项应受 128 字符上限约束，实际 ${termCount}`)
  }
}

function assertSearchIndexRows(accountId: string, expectedDocumentCount: number, expectedTermCount: number | undefined, label: string): void {
  const documentRow = databaseModule.getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM account_name_search_documents WHERE account_id = ?')
    .get(accountId) as unknown as { count?: number } | undefined
  assert.equal(Number(documentRow?.count ?? 0), expectedDocumentCount, label)
  if (expectedTermCount === undefined) return
  const termRow = databaseModule.getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS count FROM account_name_search_terms WHERE account_id = ?')
    .get(accountId) as unknown as { count?: number } | undefined
  assert.equal(Number(termRow?.count ?? 0), expectedTermCount, label)
}

function assertContainsQueryPlanUsesTermIndex(access: AccessScope, keyword: string): void {
  const subquery = accountNameSearch.accountNameContainsAccountIdSubquery(keyword, access)
  assert(subquery, '包含匹配应生成账户名称搜索词项子查询')
  const details = explainBusinessQuery(subquery.sql, subquery.params as SQLInputValue[])
  assert(details.includes('idx_account_name_search_terms_term_owner'), `包含匹配必须走词项索引，实际计划：${details}`)
  assert(!details.includes('SCAN accounts'), `包含匹配不能扫描 accounts 主表，实际计划：${details}`)
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
