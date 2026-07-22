import { strict as assert } from 'node:assert'
import http from 'node:http'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-list-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-record-list-query-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [{ createSystemApiApp }, databaseModule, repositories, usageRecordShards] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface UsageRecordListResult {
  items: Array<{
    id: string
    model?: string
    createdAt: string
  }>
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  requiresSystemAccountSelection?: boolean
}

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: 'sys_admin' }
  const group = repositories.createGroup({
    name: '使用记录查询防护分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const otherGroup = repositories.createGroup({
    name: '使用记录查询防护其他分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '使用记录查询防护账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const middleNameAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '普通使用记录查询防护账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard-middle',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id
  }, access)
  const otherGroupAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '其他分组账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-list-query-guard-other-group',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: otherGroup.id
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '使用记录查询防护 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
  }, access)
  const otherApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '使用记录查询防护其他 Key',
    groupBindings: [{ groupId: otherGroup.id, priority: 1, status: 'active' }],
  }, access)
  const owner = repositories.createSystemAccount({
    username: 'usage_record_source_owner',
    displayName: '使用记录来源归属人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'usage_record_source_grantee',
    displayName: '使用记录被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const adminGranteeAccess = { systemAccountId: 'sys_admin', role: 'admin' as const, systemAccountFilterId: grantee.id }
  const granteeGroup = repositories.createGroup({
    name: '使用记录被授权人分组',
    providerCode: 'gpt',
    enabled: true
  }, granteeAccess)
  const renamedAuthorizedSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '授权使用记录来源初始名',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-authorized-source',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: repositories.createGroup({
      name: '使用记录来源账户分组',
      providerCode: 'gpt',
      enabled: true
    }, ownerAccess).id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: renamedAuthorizedSourceAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id
  }, ownerAccess)
  const businessSetupDatabase = databaseModule.getBusinessDatabase()
  const runtimeAccountAuthorization = businessSetupDatabase
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(renamedAuthorizedSourceAccount.id, grantee.id) as unknown as { id?: string } | undefined
  assert(runtimeAccountAuthorization?.id, '使用记录授权实例回归需要运行时账号授权 ID')
  const authorizedInstance = businessSetupDatabase
    .prepare('SELECT id, name FROM accounts WHERE authorization_instance_authorization_id = ? LIMIT 1')
    .get(runtimeAccountAuthorization.id) as unknown as { id?: string; name?: string } | undefined
  assert(authorizedInstance?.id, '使用记录授权实例回归需要被授权实例账户')
  businessSetupDatabase
    .prepare('UPDATE accounts SET name = ?, updated_at = ? WHERE id = ?')
    .run('授权使用记录账户A', '2026-01-02T00:00:04.000Z', renamedAuthorizedSourceAccount.id)
  const ownerGroup = repositories.createGroup({
    name: '使用记录来源分组授权分组',
    providerCode: 'gpt',
    enabled: true
  }, ownerAccess)
  const groupAuthorizedSourceAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '分组授权使用记录账户A',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-record-group-authorized-source',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: ownerGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id
  }, ownerAccess)
  const runtimeGroupAuthorization = businessSetupDatabase
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1")
    .get(ownerGroup.id, grantee.id) as unknown as { id?: string } | undefined
  assert(runtimeGroupAuthorization?.id, '使用记录分组授权回归需要运行时分组授权 ID')
  repositories.createUsageRecordsBatch([
    {
      id: 'usage_list_query_guard_exact',
      traceId: 'trace-usage-list-query-guard-exact',
      trafficSource: 'gateway',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5',
      clientIp: '127.0.0.1',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:00.000Z'
    },
    {
      id: 'usage_list_query_guard_prefix_only',
      traceId: 'trace-usage-list-query-guard-prefix-only',
      trafficSource: 'gateway',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: account.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-mini',
      clientIp: '127.0.0.2',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:01.000Z'
    },
    {
      id: 'usage_list_query_guard_middle_name',
      traceId: 'trace-usage-list-query-guard-middle-name',
      trafficSource: 'gateway',
      apiKeyId: apiKey.id,
      groupId: group.id,
      accountId: middleNameAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-4.1',
      clientIp: '10.0.0.3',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:02.000Z'
    },
    {
      id: 'usage_list_query_guard_other_group',
      traceId: 'trace-usage-list-query-guard-other-group',
      trafficSource: 'gateway',
      apiKeyId: otherApiKey.id,
      groupId: otherGroup.id,
      accountId: otherGroupAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-other-group',
      clientIp: '127.0.1.4',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:03.000Z'
    },
    {
      id: 'usage_list_query_guard_authorized_source_owner_record',
      traceId: 'trace-usage-list-query-guard-authorized-source-owner-record',
      trafficSource: 'gateway',
      groupId: ownerGroup.id,
      accountId: renamedAuthorizedSourceAccount.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-owner-source',
      clientIp: '127.0.2.4',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:04.000Z'
    },
    {
      id: 'usage_list_query_guard_authorized_instance_source_name',
      traceId: 'trace-usage-list-query-guard-authorized-instance-source-name',
      trafficSource: 'gateway',
      systemAccountId: grantee.id,
      groupId: granteeGroup.id,
      accountId: authorizedInstance.id,
      accountOwnerSystemAccountId: owner.id,
      groupOwnerSystemAccountId: grantee.id,
      accountAccessType: 'account_authorized',
      groupAccessType: 'owner',
      accountAuthorizationId: runtimeAccountAuthorization.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-authorized-source',
      clientIp: '127.0.2.5',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:05.000Z'
    },
    {
      id: 'usage_list_query_guard_authorized_instance_inferred_metadata',
      traceId: 'trace-usage-list-query-guard-authorized-instance-inferred-metadata',
      trafficSource: 'gateway',
      systemAccountId: grantee.id,
      groupId: granteeGroup.id,
      accountId: authorizedInstance.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-authorized-inferred',
      clientIp: '127.0.2.7',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:07.000Z'
    },
    {
      id: 'usage_list_query_guard_group_authorized_source_name',
      traceId: 'trace-usage-list-query-guard-group-authorized-source-name',
      trafficSource: 'gateway',
      systemAccountId: grantee.id,
      groupId: ownerGroup.id,
      accountId: groupAuthorizedSourceAccount.id,
      accountOwnerSystemAccountId: owner.id,
      groupOwnerSystemAccountId: owner.id,
      accountAccessType: 'group_authorized',
      groupAccessType: 'authorized',
      groupAuthorizationId: runtimeGroupAuthorization.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5-group-authorized-source',
      clientIp: '127.0.2.6',
      stream: false,
      statusCode: 200,
      success: true,
      createdAt: '2026-01-02T00:00:06.000Z'
    }
  ])
  const inferredAuthorizedRecord = usageRecordShards.queryUsageRecordShardById<{
    account_owner_system_account_id?: string | null
    account_access_type?: string | null
    account_authorization_id?: string | null
  }>(
    'usage_list_query_guard_authorized_instance_inferred_metadata',
    'SELECT account_owner_system_account_id, account_access_type, account_authorization_id FROM usage_records WHERE id = ?',
    ['usage_list_query_guard_authorized_instance_inferred_metadata'],
    '2026-01-02T00:00:07.000Z'
  )
  assert.equal(inferredAuthorizedRecord?.account_owner_system_account_id, owner.id, '只传授权实例 accountId 时应自动补齐来源账户归属人')
  assert.equal(inferredAuthorizedRecord?.account_access_type, 'account_authorized', '只传授权实例 accountId 时应自动识别账号授权口径')
  assert.equal(inferredAuthorizedRecord?.account_authorization_id, runtimeAccountAuthorization.id, '只传授权实例 accountId 时应自动补齐运行时授权 ID')

  const businessDatabase = databaseModule.getBusinessDatabase()
  const originalBusinessPrepare = businessDatabase.prepare.bind(businessDatabase) as typeof businessDatabase.prepare
  const accountLookupCalls: Array<{ sql: string; params: unknown[] }> = []
  businessDatabase.prepare = ((sql: string) => {
    const statement = originalBusinessPrepare(sql)
    if (/^\s*SELECT\s+(?:accounts|instance_accounts)\.id\s+FROM\s+accounts\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        accountLookupCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof businessDatabase.prepare

  const datasetDatabase = databaseModule.getUsageCatalogDatabase()
  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  const usageRecordListCalls: Array<{ sql: string; params: unknown[] }> = []
  const shardPrepareRestorers: Array<() => void> = []
  datasetDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+usage_records\s+ur\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        usageRecordListCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof datasetDatabase.prepare
  for (const location of usageRecordShards.listUsageRecordShardLocations()) {
    const shardDatabase = usageRecordShards.getUsageRecordShardDatabase(location)
    const originalShardPrepare = shardDatabase.prepare.bind(shardDatabase) as typeof shardDatabase.prepare
    shardPrepareRestorers.push(() => {
      shardDatabase.prepare = originalShardPrepare
    })
    shardDatabase.prepare = ((sql: string) => {
      const statement = originalShardPrepare(sql)
      if (/\bFROM\s+usage_records\s+ur\b/i.test(sql)) {
        const originalAll = statement.all.bind(statement) as typeof statement.all
        statement.all = ((...params: SQLInputValue[]) => {
          usageRecordListCalls.push({ sql, params })
          return originalAll(...params)
        }) as typeof statement.all
      }
      return statement
    }) as typeof shardDatabase.prepare
  }

  try {
    const exactModel = repositories.listUsageRecords(access, { model: 'gpt-5.5', page: 1, pageSize: 10 })
    assert.deepEqual(exactModel.items.map((item) => item.id), ['usage_list_query_guard_exact'], 'model 筛选应按精确值匹配，不应把前缀模型一并查出')

    const accountNamePrefix = repositories.listUsageRecords(access, { accountKeyword: '使用记录查询防护', page: 1, pageSize: 10 })
    assert.deepEqual(accountNamePrefix.items.map((item) => item.id), ['usage_list_query_guard_prefix_only', 'usage_list_query_guard_exact'], '账号名称关键字应按前缀匹配，不应命中中间包含名称')

    const accountPrefix = repositories.listUsageRecords(access, { accountKeyword: uniquePrefix(account.id, middleNameAccount.id), page: 1, pageSize: 10 })
    assert.equal(accountPrefix.items.length, 0, '账号名称关键字不应支持账号 ID 前缀定位使用记录')

    const authorizedInstanceBySourceName = repositories.listUsageRecords(granteeAccess, { accountKeyword: '授权使用记录账户A', page: 1, pageSize: 10 })
    assert.deepEqual(authorizedInstanceBySourceName.items.map((item) => item.id), ['usage_list_query_guard_authorized_instance_inferred_metadata', 'usage_list_query_guard_authorized_instance_source_name'], '被授权用户应能通过来源账户当前名称查询自己的授权实例使用记录，且不应混入授权方原账户记录')
    assert.equal(authorizedInstanceBySourceName.items[0]?.accountId, authorizedInstance.id, '来源账户名筛选应返回被授权实例自己的使用记录账户 ID')

    const adminAuthorizedInstanceBySourceName = repositories.listUsageRecords(adminGranteeAccess, { accountKeyword: '授权使用记录账户A', page: 1, pageSize: 10 })
    assert.deepEqual(adminAuthorizedInstanceBySourceName.items.map((item) => item.id), ['usage_list_query_guard_authorized_instance_inferred_metadata', 'usage_list_query_guard_authorized_instance_source_name'], '管理员按被授权人筛选时也应能通过来源账户当前名称查询授权实例使用记录')
    assert(adminAuthorizedInstanceBySourceName.items.every((item) => item.systemAccountId === grantee.id), '管理员按被授权人来源账户名筛选不应返回授权方原账户使用记录')

    const groupAuthorizedBySourceName = repositories.listUsageRecords(granteeAccess, { accountKeyword: '分组授权使用记录账户A', page: 1, pageSize: 10 })
    assert.deepEqual(groupAuthorizedBySourceName.items.map((item) => item.id), ['usage_list_query_guard_group_authorized_source_name'], '被授权用户应能通过来源账户名称查询分组授权产生的自己的使用记录')

    const groupFiltered = repositories.listUsageRecords(access, { groupId: group.id, page: 1, pageSize: 10 })
    assert.deepEqual(groupFiltered.items.map((item) => item.id), ['usage_list_query_guard_middle_name', 'usage_list_query_guard_prefix_only', 'usage_list_query_guard_exact'], '分组筛选应只返回目标分组的使用记录')

    const clientIpPrefix = repositories.listUsageRecords(access, { clientIp: '127.0.0.', page: 1, pageSize: 10 })
    assert.deepEqual(clientIpPrefix.items.map((item) => item.id), ['usage_list_query_guard_prefix_only', 'usage_list_query_guard_exact'], '客户端 IP 筛选应按右侧前缀匹配')

    const tracePrefix = repositories.listUsageRecords(access, { traceId: 'trace-usage-list-query-guard-prefix', page: 1, pageSize: 10 })
    assert.deepEqual(tracePrefix.items.map((item) => item.id), ['usage_list_query_guard_prefix_only'], 'traceId 筛选应按右侧前缀定位')

    const customIdDetail = repositories.getUsageRecordDetail('usage_list_query_guard_exact', access)
    assert.equal(customIdDetail?.id, 'usage_list_query_guard_exact', '非标准 usage id 应通过 shard 索引单条读取，不应依赖目录全量扫描')
  } finally {
    datasetDatabase.prepare = originalPrepare
    for (const restore of shardPrepareRestorers) restore()
    businessDatabase.prepare = originalBusinessPrepare
  }

  assert(accountLookupCalls.length >= 2, '回归应捕获账号关键词预解析 SQL')
  const usageRecordsRepositorySource = readFileSync(resolve('src/storage/usage-records.repository.ts'), 'utf8')
  assert(
    usageRecordsRepositorySource.includes('const accountNameExpression = \'(accounts.name COLLATE "C")\''),
    'PG 使用记录账号关键词预解析必须使用 accounts.name COLLATE "C" 表达式'
  )
  assert(
    usageRecordsRepositorySource.includes('return usageRecordBinaryPrefixUpperBound(value)'),
    'PG 使用记录账号关键词前缀上界必须使用二进制上界'
  )
  assert.doesNotMatch(
    usageRecordsRepositorySource,
    /lower\(accounts\.name\)/,
    'PG 使用记录账号关键词预解析不能折叠账号名称大小写'
  )
  const postgresListRowsFunction = usageRecordsRepositorySource.match(/async function listPostgresUsageRecordRows[\s\S]*?\n}\n\nfunction listUsageRecordRowsFromShards/)?.[0] ?? ''
  assert.doesNotMatch(postgresListRowsFunction, /SELECT\s+ur\.\*/i, 'PG 使用记录列表回表不应 SELECT ur.* 拉取详情快照大字段')
  assert.doesNotMatch(postgresListRowsFunction, /request_snapshot_json|response_snapshot_json/i, 'PG 使用记录列表不应读取请求或响应快照字段')
  const businessSchemaSource = readFileSync(resolve('src/storage/schema/business-schema.ts'), 'utf8')
  assert.match(
    businessSchemaSource,
    /idx_accounts_owner_all_name_lookup/,
    'SQLite 使用记录账号名前缀预解析必须保留未删除账户名称前缀索引'
  )
  assert.match(
    businessSchemaSource,
    /idx_accounts_authorization_instance_source_owner_lookup/,
    'SQLite 使用记录来源账户名前缀预解析必须保留授权实例来源索引'
  )
  const postgresSchemaSource = readFileSync(resolve('src/storage/postgres-schema.ts'), 'utf8')
  assert.match(
    postgresSchemaSource,
    /idx_accounts_owner_all_name_c_lookup/,
    'PG 使用记录账号名前缀预解析必须保留 C collation 账户名称索引'
  )
  for (const call of accountLookupCalls) {
    assert(/\b(?:accounts|source_accounts)\.name\s+>=\s+\?/i.test(call.sql), '账号关键词预解析应使用大小写敏感 name 范围下界')
    assert(/\b(?:accounts|source_accounts)\.name\s+<\s+\?/i.test(call.sql), '账号关键词预解析应使用大小写敏感 name 范围上界')
    assert(!/\blower\((?:accounts|source_accounts)\.name\)/i.test(call.sql), '账号关键词预解析不应折叠名称大小写')
    assert(/\b(?:accounts|source_accounts)\.deleted_at\s+IS\s+NULL/i.test(call.sql), '账号关键词预解析应只匹配未删除账户')
    if (/\binstance_accounts\b/i.test(call.sql)) {
      assert(/\binstance_accounts\.deleted_at\s+IS\s+NULL/i.test(call.sql), '授权实例来源名称预解析应只匹配未删除授权实例')
    }
    assert(!/\bLIKE\s+\?/i.test(call.sql), '账号关键词预解析不应使用 LIKE 扫描账号表')
    assert(!/\bWHERE[\s\S]*\bid\s+(?:=|LIKE)\s+\?/i.test(call.sql), '账号关键词预解析不应把账号 ID 放进名称搜索 WHERE')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '账号关键词预解析不应传入前导通配符参数')
  }
  assert(usageRecordListCalls.length >= 2, '回归应捕获使用记录列表 SQL')
  for (const call of usageRecordListCalls) {
    assert(!/\bur\.model\s+LIKE\s+\?/i.test(call.sql), 'model 筛选不应在 usage_records 上使用 LIKE')
    assert(!/\bur\.client_ip\s+LIKE\s+\?/i.test(call.sql), '客户端 IP 筛选不应在 usage_records 上使用 LIKE')
    assert(!/\b(?:ur|ue)\.trace_id\s+LIKE\s+\?/i.test(call.sql), 'traceId 筛选不应使用 LIKE 扫描')
    assert(!/\bur\.account_id\s+(?:=|LIKE)\s+\?/i.test(call.sql), '使用记录账号名称搜索不应直接按 account_id 精确或前缀匹配')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '使用记录列表不应向大表筛选传入前导通配符参数')
  }
  assertBusinessQueryPlanUsesAnyIndex(`
    SELECT accounts.id
    FROM accounts
    WHERE accounts.deleted_at IS NULL
      AND accounts.name >= ? AND accounts.name < ?
      AND accounts.system_account_id = ?
    ORDER BY accounts.name ASC, accounts.id ASC
    LIMIT ?
  `, ['使用记录查询防护', '使用记录查询防护\uffff', 'sys_admin', 10], ['idx_accounts_owner_all_name_lookup', 'idx_accounts_owner_name_unique'])
  assertBusinessQueryPlanUsesAnyIndex(`
    SELECT instance_accounts.id
    FROM accounts source_accounts
    CROSS JOIN accounts instance_accounts
    WHERE source_accounts.deleted_at IS NULL
      AND instance_accounts.authorization_instance_source_account_id = source_accounts.id
      AND instance_accounts.deleted_at IS NULL
      AND source_accounts.name >= ? AND source_accounts.name < ?
      AND instance_accounts.system_account_id = ?
    ORDER BY source_accounts.name ASC, instance_accounts.id ASC
    LIMIT ?
  `, ['授权使用记录账户A', '授权使用记录账户A\uffff', grantee.id, 10], ['idx_accounts_authorization_instance_source_owner_lookup', 'idx_accounts_authorization_instance_source'])
  const usageCatalogSchemaSource = readFileSync(resolve('src/storage/schema/usage-catalog-schema.ts'), 'utf8')
  assert.doesNotMatch(
    usageCatalogSchemaSource,
    /CREATE INDEX IF NOT EXISTS idx_usage_record_shard_entries_trace_created_sort\b/,
    '使用记录目录库不应继续创建全局 trace 索引'
  )
  assert.match(
    usageCatalogSchemaSource,
    /idx_usage_record_shard_entries_system_trace_created_sort/,
    '使用记录目录库只保留用户维度 trace 兜底索引'
  )
  const usageShardSource = readFileSync(resolve('src/storage/usage-record-shards.ts'), 'utf8')
  for (const obsoleteIndex of [
    'idx_usage_records_model_created_sort',
    'idx_usage_records_client_ip_created_sort',
    'idx_usage_records_first_token_sort',
    'idx_usage_records_duration_sort',
    'idx_usage_records_cost_sort'
  ]) {
    assert.doesNotMatch(
      usageShardSource,
      new RegExp(`CREATE INDEX IF NOT EXISTS ${obsoleteIndex}\\b`),
      `usage_records 不应继续创建 ${obsoleteIndex}`
    )
  }
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.trace_id >= ? AND ur.trace_id < ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', 'trace-usage-list-query-guard-', 'trace-usage-list-query-guard-\uffff', 10], 'idx_usage_records_system_account_trace_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.model = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', 'gpt-5.5', 10], 'idx_usage_records_system_account_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.group_id = ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', group.id, 10], 'idx_usage_records_system_account_group_created_sort')
  assertQueryPlanUsesIndex(`
    SELECT id
    FROM usage_records ur
    WHERE ur.system_account_id = ? AND ur.client_ip >= ? AND ur.client_ip < ?
    ORDER BY ur.created_at DESC, ur.id DESC
    LIMIT ?
  `, ['sys_admin', '127.0.0.', '127.0.0.\uffff', 10], 'idx_usage_records_system_account_created_sort')

  const admin = repositories.listSystemAccounts().find((systemAccount) => systemAccount.username === 'admin')
  assert(admin, '使用记录路由回归需要默认管理员')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const routeApp = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  const routeServer = routeApp.listen(0, '127.0.0.1')
  await listen(routeServer)
  try {
    const routeBaseUrl = `http://127.0.0.1:${serverAddress(routeServer).port}`
    const routeDefaultWindowInsideAt = new Date().toISOString()
    const routeDefaultWindowOutsideAt = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString()
    const routeDefaultWindowModel = `route-default-window-model-${Date.now()}`
    const routeDefaultWindowInsideId = usageRecordShards.generateUsageRecordId(routeDefaultWindowInsideAt, 'inside')
    const routeDefaultWindowOutsideId = usageRecordShards.generateUsageRecordId(routeDefaultWindowOutsideAt, 'outside')
    repositories.createUsageRecordsBatch([
      {
        id: routeDefaultWindowInsideId,
        traceId: 'trace-route-default-window-inside',
        trafficSource: 'gateway',
        apiKeyId: apiKey.id,
        groupId: group.id,
        accountId: account.id,
        endpoint: '/v1/responses',
        providerCode: 'gpt',
        model: routeDefaultWindowModel,
        stream: false,
        statusCode: 200,
        success: true,
        createdAt: routeDefaultWindowInsideAt
      },
      {
        id: routeDefaultWindowOutsideId,
        traceId: 'trace-route-default-window-outside',
        trafficSource: 'gateway',
        apiKeyId: apiKey.id,
        groupId: group.id,
        accountId: account.id,
        endpoint: '/v1/responses',
        providerCode: 'gpt',
        model: routeDefaultWindowModel,
        stream: false,
        statusCode: 200,
        success: true,
        createdAt: routeDefaultWindowOutsideAt
      }
    ])

    const routeDefaultWindow = await getEnvelope<UsageRecordListResult>(
      routeBaseUrl,
      `/__aisys__/api/usage-records?systemAccountId=sys_admin&model=${encodeURIComponent(routeDefaultWindowModel)}&page=1&pageSize=20`,
      sessionCookie(admin.id)
    )
    assert.deepEqual(routeDefaultWindow.items.map((item) => item.id), [routeDefaultWindowInsideId], '使用记录路由未传日期时应默认限制今天')

    const routeWithoutSystemAccount = await getEnvelope<UsageRecordListResult>(
      routeBaseUrl,
      '/__aisys__/api/usage-records?page=1&pageSize=20',
      sessionCookie(admin.id)
    )
    assert(routeWithoutSystemAccount.items.some((item) => item.id === routeDefaultWindowInsideId), '管理员使用记录路由未指定系统账户时应返回当天全用户列表')
    assert(!routeWithoutSystemAccount.items.some((item) => item.id === routeDefaultWindowOutsideId), '管理员使用记录当天全用户列表不应跨出默认日期窗口')

    const routeWithoutSystemAccountFiltered = await fetch(
      `${routeBaseUrl}/__aisys__/api/usage-records?model=${encodeURIComponent(routeDefaultWindowModel)}&page=1&pageSize=20`,
      { headers: { cookie: sessionCookie(admin.id) } }
    )
    assert.equal(routeWithoutSystemAccountFiltered.status, 400, '管理员未选系统账户时携带业务筛选应返回 400')
    assert.deepEqual(await routeWithoutSystemAccountFiltered.json(), { message: '请先选择系统账户后筛选' })

    for (const query of [
      'accountKeyword=guard',
      'result=all',
      'statusCode=200',
      'clientIp=127.0.0.1',
      `groupId=${encodeURIComponent(group.id)}`,
      `model=${encodeURIComponent(routeDefaultWindowModel)}`,
      'traceId=trace-route',
      'trafficSource=gateway',
      'startDate=2026-07-01',
      'endDate=2026-07-13',
      'sortBy=durationMs',
      'sortOrder=asc'
    ]) {
      const filterResponse: Awaited<ReturnType<typeof fetch>> = await fetch(`${routeBaseUrl}/__aisys__/api/usage-records?page=1&pageSize=20&${query}`, {
        headers: { cookie: sessionCookie(admin.id) }
      })
      assert.equal(filterResponse.status, 400, `管理员全用户使用记录不应接受筛选参数：${query}`)
      assert.deepEqual(await filterResponse.json(), { message: '请先选择系统账户后筛选' })
    }

    const routePageClamp = await getEnvelope<UsageRecordListResult>(
      routeBaseUrl,
      `/__aisys__/api/usage-records?systemAccountId=sys_admin&model=${encodeURIComponent(routeDefaultWindowModel)}&page=999999&pageSize=1`,
      sessionCookie(admin.id)
    )
    assert.equal(routePageClamp.page, 1000, '使用记录路由页码应在 1000 以内')
    assert.equal(routePageClamp.pageSize, 1, '使用记录路由分页大小应保持请求值')
  } finally {
    await closeServer(routeServer)
  }

  console.log('使用记录列表查询防护回归通过：model 精确匹配，clientIp 前缀范围匹配，accountKeyword 不对 usage_records 做前导通配符扫描')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertQueryPlanUsesIndex(sql: string, params: SQLInputValue[], indexName: string): void {
  const location = usageRecordShards.listUsageRecordShardLocations()[0]
  assert(location, '查询计划验证需要至少一个 usage shard')
  const details = usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes(indexName), `查询计划应使用 ${indexName}，实际计划：${details}`)
}

function assertBusinessQueryPlanUsesAnyIndex(sql: string, params: SQLInputValue[], indexNames: string[]): void {
  const details = databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(indexNames.some((indexName) => details.includes(indexName)), `业务库查询计划应使用 ${indexNames.join(' / ')}，实际计划：${details}`)
}

function uniquePrefix(value: string, otherValue: string): string {
  for (let length = 1; length <= value.length; length += 1) {
    const prefix = value.slice(0, length)
    if (!otherValue.startsWith(prefix)) return prefix
  }
  return value
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function listen(listeningServer: http.Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: http.Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}

function serverAddress(listeningServer: http.Server): { port: number } {
  const address = listeningServer.address()
  assert(address && typeof address !== 'string', '使用记录路由回归服务器应监听 TCP 地址')
  return { port: address.port }
}
