import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-authorization-shard-context-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.usageShardCount = 4
runtimeConfig.secret = 'usage-stats-authorization-shard-context-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js')
])

type UsageIdWithShard = {
  id: string
  shardKey: string
}

try {
  const owner = repositories.createSystemAccount({
    username: 'usage_stats_auth_context_owner',
    displayName: '统计授权上下文所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'usage_stats_auth_context_grantee',
    displayName: '统计授权上下文被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const ownerGroup = repositories.createGroup({
    name: '统计授权上下文来源分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const granteeGroup = repositories.createGroup({
    name: '统计授权上下文目标分组',
    providerCode: 'gpt'
  }, granteeAccess)
  const sourceAccountA = repositories.createAccount({
    providerCode: 'gpt',
    name: '统计授权上下文来源账号 A',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-stats-authorization-context-a',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  const sourceAccountB = repositories.createAccount({
    providerCode: 'gpt',
    name: '统计授权上下文来源账号 B',
    type: 'api_key',
    credentials: {
      api_key: 'sk-usage-stats-authorization-context-b',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)

  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccountA.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '统计授权上下文分片 A'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: sourceAccountB.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '统计授权上下文分片 B'
  }, ownerAccess)

  const authorizedAccountA = authorizedInstanceForSource(sourceAccountA.id, grantee.id)
  const authorizedAccountB = authorizedInstanceForSource(sourceAccountB.id, grantee.id)
  const createdAt = new Date(Date.now() - 60_000).toISOString()
  const usageA = usageIdOnNewShard(createdAt, 'usage-auth-context-a')
  const usageB = usageIdOnNewShard(createdAt, 'usage-auth-context-b', new Set([usageA.shardKey]))
  assert.notEqual(usageA.shardKey, usageB.shardKey, '回归准备：两条授权使用记录必须落到不同 usage shard')

  repositories.createUsageRecordsBatch([
    {
      id: usageA.id,
      traceId: 'trace-usage-auth-context-a',
      trafficSource: 'gateway',
      systemAccountId: grantee.id,
      groupId: granteeGroup.id,
      accountId: authorizedAccountA.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5',
      statusCode: 200,
      success: true,
      inputTokens: 10,
      outputTokens: 20,
      createdAt
    },
    {
      id: usageB.id,
      traceId: 'trace-usage-auth-context-b',
      trafficSource: 'gateway',
      systemAccountId: grantee.id,
      groupId: granteeGroup.id,
      accountId: authorizedAccountB.id,
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      model: 'gpt-5.5',
      statusCode: 200,
      success: true,
      inputTokens: 30,
      outputTokens: 40,
      createdAt
    }
  ])

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(2), 2, '统计聚合应在同一批次处理两个不同 shard 的授权记录')
  const resourceRows = authorizationUserAccountResourceRows(owner.id, grantee.id)
  assert.equal(resourceRows.get(sourceAccountA.id), 1, '授权报表应按来源账号 A 记录资源过滤维度')
  assert.equal(resourceRows.get(sourceAccountB.id), 1, '授权报表应按来源账号 B 记录资源过滤维度，不能复用首个 shard 的授权 lookup')
  assert.equal(resourceRows.has(authorizedAccountA.id), false, '授权报表不应把授权实例账号 A 写成资源 ID')
  assert.equal(resourceRows.has(authorizedAccountB.id), false, '授权报表不应把授权实例账号 B 写成资源 ID')

  console.log('用量统计授权分片上下文回归通过：跨 shard 聚合会递增加载授权映射，授权报表资源 ID 保持来源账号')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function usageIdOnNewShard(createdAt: string, prefix: string, excludedShardKeys = new Set<string>()): UsageIdWithShard {
  for (let index = 0; index < 1000; index += 1) {
    const id = usageRecordShards.generateUsageRecordId(createdAt, `${prefix}-${index}`)
    const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
    if (!excludedShardKeys.has(location.shardKey)) {
      return { id, shardKey: location.shardKey }
    }
  }
  throw new Error('无法为统计授权上下文回归找到可用 usage shard')
}

function authorizedInstanceForSource(sourceAccountId: string, systemAccountId: string): { id: string; accountAuthorizationId: string } {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id, authorization_instance_authorization_id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND system_account_id = ?
      LIMIT 1
    `)
    .get(sourceAccountId, systemAccountId) as { id?: string; authorization_instance_authorization_id?: string | null } | undefined
  assert(row?.id, `被授权用户应存在来源账号 ${sourceAccountId} 的授权实例`)
  assert(row.authorization_instance_authorization_id, `授权实例 ${row.id} 应绑定运行时授权`)
  return {
    id: row.id,
    accountAuthorizationId: row.authorization_instance_authorization_id
  }
}

function authorizationUserAccountResourceRows(ownerSystemAccountId: string, granteeSystemAccountId: string): Map<string, number> {
  const rows = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT resource_filter_id, request_count
      FROM authorization_user_usage_summary_daily
      WHERE system_account_id = ?
        AND grantee_filter_system_account_id = ?
        AND resource_filter_type = 'account'
        AND resource_filter_id <> ''
    `)
    .all(ownerSystemAccountId, granteeSystemAccountId) as Array<{ resource_filter_id?: string | null; request_count?: number | null }>
  return new Map(rows.map((row) => [String(row.resource_filter_id ?? ''), Number(row.request_count ?? 0)]))
}
