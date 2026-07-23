import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-ai-performance-account-options-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'ai-performance-account-options-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js')
])

try {
  const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const owner = repositories.createSystemAccount({
    username: 'perf-owner',
    displayName: '性能账号用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const grantee = repositories.createSystemAccount({
    username: 'perf-grantee',
    displayName: '性能授权用户',
    password: 'Password-123456',
    mustChangePassword: false
  })
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: 'AI 性能账号选项拥有者分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const adminGroup = repositories.createGroup({
    name: 'AI 性能账号选项管理员分组',
    providerCode: 'gpt',
    enabled: true
  }, adminAccess)
  const matchedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'perfneedle 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-matched',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  const granteeTargetGroup = repositories.createGroup({
    name: 'AI 性能授权目标分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  const authorizedSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '性能授权来源账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-authorized-source',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: authorizedSourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id,
    remark: 'AI 性能来源名查询回归'
  }, ownerAccess)
  const authorizedInstance = repositories.listAccounts(granteeAccess)
    .find((account) => account.authorizationInstanceSourceAccountId === authorizedSourceAccount.id)
  assert(authorizedInstance?.id, 'AI 性能回归需要被授权实例账户')
  repositories.updateAccount(authorizedSourceAccount.id, {
    name: 'perfauthcurrent 来源账号'
  }, ownerAccess)
  const otherOwnerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'otherneedle 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-other',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  const adminAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '管理员普通账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-admin',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: adminGroup.id
  }, adminAccess)
  const wildcardAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'perf%literal 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-wildcard-literal',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  const wildcardNeighborAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'perfXliteral 主账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-options-query-guard-wildcard-neighbor',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: unknown[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const shouldCapture = /^\s*SELECT\s+accounts\.id\s+FROM\s+accounts\b/i.test(sql)
      || /^\s*SELECT\s+id\s+FROM\s+system_accounts\b/i.test(sql)
    if (shouldCapture) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      const originalGet = statement.get.bind(statement) as typeof statement.get
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
      statement.get = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalGet(...params)
      }) as typeof statement.get
    }
    return statement
  }) as typeof database.prepare

  try {
    const nameKeyword = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: 'perfneedle',
      limit: 10
    })
    assert.deepEqual(nameKeyword.map((account) => account.id), [matchedAccount.id], 'AI 性能账号选项应支持账号名前缀搜索')

    const idPrefix = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: uniquePrefix(matchedAccount.id, otherOwnerAccount.id),
      limit: 10
    })
    assert.deepEqual(idPrefix.map((account) => account.id), [], 'AI 性能账号选项名称搜索不应支持账号 ID 前缀搜索')

    const ownerKeyword = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: owner.displayName,
      limit: 10
    })
    assert(!ownerKeyword.some((account) => account.id === matchedAccount.id), 'AI 性能账号选项名称搜索不应通过系统用户名称命中账号')

    const wildcardKeyword = usageStatsRepository.listAiPerformanceAccountOptions(adminAccess, {
      keyword: 'perf%',
      limit: 10
    })
    assert.deepEqual(wildcardKeyword.map((account) => account.id), [wildcardAccount.id], 'AI 性能账号选项应把 % 当作字面量前缀处理')
    assert(!wildcardKeyword.some((account) => account.id === wildcardNeighborAccount.id), 'AI 性能账号选项不应把用户输入的 % 当作 LIKE 通配符')

    const userScopedKeyword = usageStatsRepository.listAiPerformanceAccountOptions(ownerAccess, {
      keyword: '管理员',
      limit: 10
    })
    assert(!userScopedKeyword.some((account) => account.id === adminAccount.id), '用户侧 AI 性能账号选项不能因关键词命中其他 owner 账号而越权')

  const authorizedSourceKeyword = usageStatsRepository.listAiPerformanceAccountOptions(granteeAccess, {
    keyword: 'perfauthcurrent',
    limit: 10
  })
  assert.deepEqual(authorizedSourceKeyword.map((account) => account.id), [authorizedInstance.id], '用户侧 AI 性能账号选项应能通过来源账户当前名称命中自己的授权实例')
  assert.equal(authorizedSourceKeyword[0]?.accessType, 'authorized', '账号授权实例在用户侧 AI 性能选项中应标记为授权来源')

  const groupAuthorizedGroup = repositories.createGroup({
    name: 'AI 性能分组授权来源分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const groupAuthorizedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'perfgroupauth 分组来源账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-ai-performance-group-authorized-source',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: groupAuthorizedGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: groupAuthorizedGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: 'AI 性能分组授权查询回归'
  }, ownerAccess)
  seedUsageStatsHourly(grantee.id, 'caller_account', groupAuthorizedAccount.id, '2026-01-01T00', 7)
  const groupAuthorizedKeyword = usageStatsRepository.listAiPerformanceAccountOptions(granteeAccess, {
    keyword: 'perfgroupauth',
    limit: 10
  })
  assert.deepEqual(groupAuthorizedKeyword.map((account) => account.id), [groupAuthorizedAccount.id], '用户侧 AI 性能账号选项应能命中授权分组里的来源账户')
  assert.equal(groupAuthorizedKeyword[0]?.accessType, 'authorized', '分组授权来源账户在被授权人视角应标记为授权来源')
  assert.equal(groupAuthorizedKeyword[0]?.ownerSystemAccountId, owner.id, '分组授权来源账户应保留资源归属人用于前端展示')

  const groupAuthorizedOverview = usageStatsRepository.getAiPerformanceOverview(granteeAccess, {
    startDate: '2026-01-01',
    endDate: '2026-01-01',
    days: 1,
    maxDays: 31
  }, [groupAuthorizedAccount.id])
  const groupAuthorizedOverviewAccount = groupAuthorizedOverview.accounts.find((account) => account.id === groupAuthorizedAccount.id)
  assert.equal(groupAuthorizedOverviewAccount?.accessType, 'authorized', '被授权人的 AI 性能概览应能追加分组授权来源账户')
  assert.equal(groupAuthorizedOverviewAccount?.ownerSystemAccountId, owner.id, '被授权人的 AI 性能概览应保留分组授权来源账户归属人')
  const groupAuthorizedPoint = groupAuthorizedOverview.hourlySeries
    .find((series) => series.accountId === groupAuthorizedAccount.id)
    ?.points.find((point) => point.statHour === '2026-01-01T00')
  assert.equal(groupAuthorizedPoint?.requestCount, 7, '分组授权来源账户的 AI 性能小时趋势应读取被授权人自己的 caller_account 数据')
  } finally {
    database.prepare = originalPrepare
  }

  const accountOptionCalls = capturedCalls.filter((call) => /^\s*SELECT\s+accounts\.id\s+FROM\s+accounts\b/i.test(call.sql))
  assert(accountOptionCalls.length >= 4, '回归应捕获 AI 性能账号选项账号查询')
  for (const call of capturedCalls) {
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), 'AI 性能账号选项查询不应传入前导通配符参数')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), 'AI 性能账号选项前缀搜索应显式转义 LIKE 通配符')
    }
  }
  for (const call of accountOptionCalls) {
    assert(!/\bWHERE[\s\S]*\baccounts\.id\s+(?:=|LIKE)\s+\?/i.test(call.sql), 'AI 性能账号选项名称搜索不应把账号 ID 放进 WHERE')
    assert(!/\baccounts\.provider_code\s+(?:=|LIKE)\s+\?/i.test(call.sql), 'AI 性能账号选项名称搜索不应把供应商编码放进 WHERE')
    assert(!/\bLEFT\s+JOIN\s+system_accounts\b/i.test(call.sql), 'AI 性能账号选项关键词查询不应为 owner 名称挂系统账号表')
    assert(!/\bCOALESCE\s*\(\s*system_accounts\./i.test(call.sql), 'AI 性能账号选项关键词查询不应在账号查询中扫描系统账号展示字段')
    assert(!/\blower\(accounts\.name\)\s+LIKE\s+\?/i.test(call.sql), 'AI 性能账号选项名称前缀搜索不应回退 lower(name) LIKE 扫描')
    assert(/\baccounts\.deleted_at\s+IS\s+NULL\b/i.test(call.sql), 'AI 性能账号选项名称搜索不应返回逻辑删除账号')
  }
  assertBusinessIndexExists('idx_accounts_name_lookup')
  assertBusinessIndexExists('idx_accounts_system_account_name_lookup')
  assertBusinessIndexExists('idx_accounts_owner_all_name_lookup')
  const aiPerformanceRepositorySource = readFileSync(new URL('../../storage/usage-stats-ai-performance.repository.ts', import.meta.url), 'utf8')
  const asyncAccountOptionSnippet = aiPerformanceRepositorySource.slice(
    aiPerformanceRepositorySource.indexOf('async function loadAiPerformanceAccountOptionRowsAsync'),
    aiPerformanceRepositorySource.indexOf('function normalizeAccountNamePrefix')
  )
  assert.match(
    asyncAccountOptionSnippet,
    /accounts\.name COLLATE "C" >= \? AND accounts\.name COLLATE "C" < \? AND starts_with\(accounts\.name, \?\)/,
    'PG AI 性能账号选项名称搜索必须使用大小写敏感 C collation 范围 + starts_with 条件'
  )
  assert.doesNotMatch(
    asyncAccountOptionSnippet,
    /LOWER\(accounts\.name\)/,
    'PG AI 性能账号选项名称搜索不能折叠大小写'
  )
  assert.match(
    asyncAccountOptionSnippet,
    /accounts\.deleted_at IS NULL/,
    'PG AI 性能账号选项名称搜索必须过滤逻辑删除账号'
  )
  assert.match(
    asyncAccountOptionSnippet,
    /accounts\.authorization_instance_authorization_id IS NULL/,
    'PG AI 性能账号选项自有账号路径必须匹配 owner partial index 谓词'
  )
  assert.match(
    asyncAccountOptionSnippet,
    /source_accounts\.deleted_at IS NULL[\s\S]+instance_accounts\.deleted_at IS NULL/,
    'PG AI 性能账号选项授权实例来源搜索必须过滤逻辑删除的来源账号和实例账号'
  )
  assert.match(
    asyncAccountOptionSnippet,
    /ORDER BY accounts\.name COLLATE "C" ASC, accounts\.id ASC/,
    'PG AI 性能账号选项名称搜索排序必须使用 C collation，避免受默认排序规则影响'
  )
  const postgresSchemaSource = readFileSync(new URL('../../storage/postgres-schema.ts', import.meta.url), 'utf8')
  assert.match(postgresSchemaSource, /idx_accounts_name_c_lookup/, 'PG AI 性能账号选项全局名称前缀查询必须有 C collation 索引')
  assert.match(postgresSchemaSource, /idx_accounts_owner_name_c_lookup/, 'PG AI 性能账号选项租户名称前缀查询必须有 owner + C collation 索引')
  const statsRoutesSource = readFileSync(new URL('../../modules/stats/stats.routes.ts', import.meta.url), 'utf8')
  assert.match(statsRoutesSource, /getAiPerformanceBaseAsync/, 'AI 性能 base 路由应使用 async repository')
  assert.match(statsRoutesSource, /getAiPerformanceSeriesAsync/, 'AI 性能 series 路由应使用 async repository')
  assert.match(statsRoutesSource, /listAiPerformanceAccountOptionsAsync/, 'AI 性能账号选项路由应使用 async repository')
  assert.doesNotMatch(statsRoutesSource, /\bgetAiPerformanceBase\(/, 'AI 性能 base 路由不应直接调用同步 repository')
  assert.doesNotMatch(statsRoutesSource, /\bgetAiPerformanceSeries\(/, 'AI 性能 series 路由不应直接调用同步 repository')
  assert.doesNotMatch(statsRoutesSource, /\blistAiPerformanceAccountOptions\(/, 'AI 性能账号选项路由不应直接调用同步 repository')

  console.log('AI 性能账号选项查询防护回归通过：关键词仅按账号名称精确/前缀匹配，显式账号 ID 仅用于已选项回填')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}

function assertBusinessIndexExists(indexName: string): void {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get(indexName) as unknown as { name?: string } | undefined
  assert.equal(row?.name, indexName, `业务库应创建索引 ${indexName}`)
}

function seedUsageStatsHourly(
  systemAccountId: string,
  scopeType: string,
  scopeId: string,
  statHour: string,
  requestCount: number
): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO usage_stats_hourly (
        system_account_id, scope_type, scope_id, stat_hour,
        request_count, success_count, input_tokens, output_tokens,
        duration_ms_sum, duration_ms_count, duration_ms_max,
        first_token_ms_sum, first_token_ms_count, first_token_ms_max,
        last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 1200, ?, 300, 600, ?, 180, ?, ?)
    `)
    .run(
      systemAccountId,
      scopeType,
      scopeId,
      statHour,
      requestCount,
      requestCount,
      requestCount,
      requestCount,
      `${statHour}:30:00.000Z`,
      '2026-01-01T00:30:00.000Z'
    )
}
