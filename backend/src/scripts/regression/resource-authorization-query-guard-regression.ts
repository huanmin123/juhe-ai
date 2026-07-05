import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-query-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'resource-authorization-query-guard-secret'
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
  assertPostgresKeywordSourceGuard()

  const owner = repositories.createSystemAccount({
    username: 'authorization_query_guard_owner',
    displayName: '查询守卫所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_query_guard_grantee',
    displayName: '查询守卫被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }

  const exactGroup = createAuthorizedGroup('授权查询守卫', '精确备注', grantee.id, ownerAccess)
  const prefixGroup = createAuthorizedGroup('授权查询守卫扩展', '前缀备注', grantee.id, ownerAccess)
  const middleGroup = createAuthorizedGroup('普通授权查询守卫', '中间备注', grantee.id, ownerAccess)
  const percentGroup = createAuthorizedGroup('percent%literal 授权', '百分号备注', grantee.id, ownerAccess)
  const percentNeighborGroup = createAuthorizedGroup('percentXliteral 授权', '百分号邻居备注', grantee.id, ownerAccess)

  assertAuthorizationListPlan(owner.id)

  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const capturedCalls: Array<{ sql: string; params: SQLInputValue[] }> = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (/\bFROM\s+resource_authorization_grants\s+rag\b/i.test(sql)) {
      const originalAll = statement.all.bind(statement) as typeof statement.all
      statement.all = ((...params: SQLInputValue[]) => {
        capturedCalls.push({ sql, params })
        return originalAll(...params)
      }) as typeof statement.all
    }
    return statement
  }) as typeof database.prepare

  try {
    const keywordIds = repositories.listResourceAuthorizationsPage(
      { keyword: '授权查询守卫', status: 'all' },
      ownerAccess,
      { page: 1, pageSize: 20, includeUsage: false }
    ).items.map((item) => item.resourceId)
    assert(keywordIds.includes(exactGroup.id), '授权列表 keyword 应命中资源名精确值')
    assert(keywordIds.includes(prefixGroup.id), '授权列表 keyword 应命中资源名前缀值')
    assert(!keywordIds.includes(middleGroup.id), '授权列表 keyword 不应命中资源名中间包含值')

    const wildcardIds = repositories.listResourceAuthorizationsPage(
      { keyword: 'percent%', status: 'all' },
      ownerAccess,
      { page: 1, pageSize: 20, includeUsage: false }
    ).items.map((item) => item.resourceId)
    assert(wildcardIds.includes(percentGroup.id), '授权列表 keyword 应把 % 当作字面量前缀处理')
    assert(!wildcardIds.includes(percentNeighborGroup.id), '授权列表 keyword 不应把 % 当作 LIKE 通配符')
  } finally {
    database.prepare = originalPrepare
  }

  assert(capturedCalls.length >= 2, '回归应捕获授权列表 SQL')
  for (const call of capturedCalls) {
    assert(!/\bCOALESCE\s*\(/i.test(call.sql), '授权列表 keyword 不应通过 COALESCE 扫描字段')
    assert(!call.params.some((param) => typeof param === 'string' && param.startsWith('%')), '授权列表 keyword 不应传入前导通配符')
    if (/\bLIKE\s+\?/i.test(call.sql)) {
      assert(/\bESCAPE\s+'\\'/i.test(call.sql), 'SQLite 授权列表 keyword 前缀搜索应显式转义 LIKE 通配符')
    }
  }

  console.log('资源授权查询防护回归通过：授权列表窗口、keyword 前缀和通配符字面量语义已固定')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createAuthorizedGroup(
  name: string,
  remark: string,
  granteeId: string,
  access: { systemAccountId: string; role: 'user' }
): { id: string } {
  const group = repositories.createGroup({
    name,
    providerCode: 'gpt',
    enabled: true
  }, access)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId,
    remark
  }, access)
  return { id: group.id }
}

function assertAuthorizationListPlan(ownerSystemAccountId: string): void {
  const details = explainBusinessQuery(`
    SELECT id
    FROM resource_authorization_grants
    WHERE resource_owner_system_account_id = ?
      AND status = ?
    ORDER BY created_at DESC, id DESC
    LIMIT ? OFFSET ?
  `, [ownerSystemAccountId, 'active', 51, 0])
  assert(details.includes('idx_resource_authorization_grants_owner_created'), `授权列表应通过 owner + status + created_at 组合索引读取当前页，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `授权列表不应为默认排序创建临时 B-TREE，实际计划：${details}`)
}

function assertPostgresKeywordSourceGuard(): void {
  const source = readFileSync(resolve('src/storage/resource-authorization-read.repository.ts'), 'utf8')
  const start = source.indexOf('function resourceAuthorizationKeywordFilterForClient')
  const end = source.indexOf('function textPrefixUpperBound', start)
  assert.notEqual(start, -1, '应存在 PG 授权列表 keyword filter')
  assert.notEqual(end, -1, '应能截取 PG 授权列表 keyword filter')
  const snippet = source.slice(start, end)
  assert(snippet.includes('starts_with(${expression}, ?)'), 'PG 授权列表 keyword 应使用 starts_with 固定字面前缀语义')
  assert(snippet.includes('textPrefixUpperBound(keyword)'), 'PG 授权列表 keyword 应计算大小写敏感前缀范围边界')
  assert(!/starts_with\(lower\(/i.test(snippet), 'PG 授权列表 keyword 不应折叠名称大小写')
  assert(!/\bILIKE\b/i.test(snippet), 'PG 授权列表 keyword 不应使用 ILIKE 前缀参数')
  assert(!/ESCAPE\s+'\\\\'/.test(snippet), 'PG 授权列表 keyword 不应依赖 LIKE ESCAPE 语义')
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
