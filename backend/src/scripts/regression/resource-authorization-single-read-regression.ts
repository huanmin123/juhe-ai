import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'resource-authorization-single-read-secret'
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
  const owner = repositories.createSystemAccount({
    username: 'authorization_single_read_owner',
    displayName: '授权单读所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_single_read_grantee',
    displayName: '授权单读被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }

  let targetId = ''
  let targetGroupName = ''
  let dueAuthorizationId = ''
  for (let index = 0; index < 250; index += 1) {
    const group = repositories.createGroup({
      name: `授权单条读取回归-${String(index).padStart(3, '0')}`,
      providerCode: 'gpt',
      enabled: true
    }, ownerAccess)
    const authorization = repositories.createResourceAuthorization({
      resourceType: 'group',
      resourceId: group.id,
      granteeType: 'system_account',
      granteeId: grantee.id,
      remark: '授权单条读取回归'
    }, ownerAccess)
    if (index === 0) {
      targetId = authorization.id
      targetGroupName = group.name
    }
    if (index === 1) {
      dueAuthorizationId = authorization.id
    }
  }

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE resource_authorization_grants SET created_at = '2000-01-01T00:00:00.000Z', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(targetId)
  assertAuthorizationListQueryPlan(owner.id)

  const firstPageLikeList = repositories.listResourceAuthorizations({ status: 'all' }, ownerAccess).slice(0, 200)
  assert.equal(firstPageLikeList.some((authorization) => authorization.id === targetId), false, '最早创建的第 250 条外授权不应出现在前 200 条列表窗口里')

  const target = repositories.findResourceAuthorization(targetId, ownerAccess, { includeUsage: false })
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的授权')
  assert.equal(target?.resourceName, targetGroupName, '按 ID 单条读取应返回完整授权摘要')
  assert.equal(target?.usage.requestCount, 0, '轻量单条读取不应为操作日志 before 额外加载用量')

  const updated = repositories.updateResourceAuthorization(targetId, { expiresAt: '2099-01-01T00:00:00.000Z' }, ownerAccess)
  assert.equal(updated?.id, targetId, '更新授权应通过单条读取返回目标授权摘要')
  assert.equal(updated?.expiresAt, '2099-01-01T00:00:00.000Z', '更新授权应保留新的过期时间')

  const grantPatchSql: string[] = []
  const database = databaseModule.getBusinessDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    if (/^\s*UPDATE\s+resource_authorization_grants\s+SET/i.test(sql)) grantPatchSql.push(sql)
    return originalPrepare(sql)
  }) as typeof database.prepare
  try {
    const unchanged = await repositories.patchResourceAuthorizationAsync(targetId, {
      expectedUpdatedAt: updated?.updatedAt,
      expiresAt: updated?.expiresAt
    }, ownerAccess)
    assert.equal(unchanged.kind, 'unchanged', '授权同值 PATCH 必须成为 no-op')
    assert.equal(grantPatchSql.length, 0, '授权同值 PATCH 不得执行 grant DML')

    const unauthorized = await repositories.patchResourceAuthorizationAsync(targetId, {
      expectedUpdatedAt: updated?.updatedAt,
      expiresAt: '2099-02-01T00:00:00.000Z'
    }, { systemAccountId: grantee.id, role: 'user' })
    assert.equal(unauthorized.kind, 'not_found', '非资源 owner 不得读取或锁定目标授权后再做内存授权')
    assert.equal(grantPatchSql.length, 0, '跨用户授权 PATCH 不得执行 grant DML')

    const changed = await repositories.patchResourceAuthorizationAsync(targetId, {
      expectedUpdatedAt: updated?.updatedAt,
      expiresAt: '2099-02-01T00:00:00.000Z'
    }, ownerAccess)
    assert.equal(changed.kind, 'updated', '授权过期时间变化应执行字段级 PATCH')
    assert.equal(grantPatchSql.length, 1, '授权真实变化只能执行一条 grant DML')
    assert.match(grantPatchSql[0] ?? '', /SET\s+expires_at\s*=\s*\?,\s*updated_at\s*=\s*\?/i, '只修改过期时间时动态 SET 只能覆盖 expires_at / updated_at')
    assert.doesNotMatch(grantPatchSql[0] ?? '', /limits_json|remark|created_by/i, '过期时间 PATCH 不得覆盖额度、备注或创建字段')

    const stale = await repositories.patchResourceAuthorizationAsync(targetId, {
      expectedUpdatedAt: updated?.updatedAt,
      expiresAt: '2099-03-01T00:00:00.000Z'
    }, ownerAccess)
    assert.equal(stale.kind, 'conflict', '旧授权版本必须被 CAS 拒绝')
    assert.equal(grantPatchSql.length, 1, 'CAS 冲突不得继续写入 grant')
  } finally {
    database.prepare = originalPrepare
  }

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE resource_authorization_grants SET expires_at = '2000-01-01T00:00:00.000Z', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(dueAuthorizationId)
  const revoked = repositories.revokeResourceAuthorization(targetId, ownerAccess)
  assert.equal(revoked?.id, targetId, '回收授权应通过单条读取返回目标授权摘要')
  assert.equal(revoked?.status, 'revoked', '回收授权应返回已回收状态')
  assert.equal(grantStatus(dueAuthorizationId), 'active', '回收授权的返回摘要路径不应顺带扫描并过期其他授权')
  const defaultListAfterRevoke = repositories.listResourceAuthorizations({}, ownerAccess)
  assert.equal(defaultListAfterRevoke.some((authorization) => authorization.id === targetId), true, '默认授权列表应保留已回收授权')
  const allListAfterRevoke = repositories.listResourceAuthorizations({ status: 'all' }, ownerAccess)
  assert.equal(allListAfterRevoke.some((authorization) => authorization.id === targetId), true, '显式全部状态查询仍应保留已回收授权用于审计和重新授权')

  console.log('资源授权单条读取回归通过：操作日志 before 和写路径不再依赖全量授权列表装配')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertAuthorizationListQueryPlan(ownerSystemAccountId: string): void {
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

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}

function grantStatus(id: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM resource_authorization_grants WHERE id = ?')
    .get(id) as { status?: string } | undefined
  return row?.status
}
