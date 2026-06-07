import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { maxAuthorizationExpirySweepBatchSize } from '../../storage/authorization-sweep-limits.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-expiry-batch-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'resource-authorization-expiry-batch-boundary-secret'
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
  const grantee = repositories.createSystemAccount({
    username: 'authorization_expiry_batch_grantee',
    displayName: '授权过期批量被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })

  for (let index = 0; index < maxAuthorizationExpirySweepBatchSize + 1; index += 1) {
    const group = repositories.createGroup({
      name: `授权过期批量分组 ${String(index).padStart(2, '0')}`,
      providerCode: 'gpt',
      enabled: true
    }, access)
    repositories.createResourceAuthorization({
      resourceType: 'group',
      resourceId: group.id,
      granteeType: 'system_account',
      granteeId: grantee.id
    }, access)
  }

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE resource_authorization_grants SET expires_at = '2026-01-01T00:00:00.000Z' WHERE status = 'active'")
    .run()
  assertExpirySweepQueryPlan()

  const firstSweep = repositories.expireDueResourceAuthorizations()
  assert.equal(firstSweep, maxAuthorizationExpirySweepBatchSize, '授权过期扫描单次只处理固定批量')
  assert.equal(expiredGrantCount(), maxAuthorizationExpirySweepBatchSize, '第一次扫描只应过期固定批量授权')

  const secondSweep = repositories.expireDueResourceAuthorizations()
  assert.equal(secondSweep, 1, '第二次扫描继续处理剩余到期授权')
  assert.equal(expiredGrantCount(), maxAuthorizationExpirySweepBatchSize + 1, '第二次扫描后剩余授权应过期')

  console.log('统一授权过期扫描批量边界回归通过：请求路径不会一次性处理全部到期授权')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function expiredGrantCount(): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT COUNT(*) AS count FROM resource_authorization_grants WHERE status = 'expired'")
    .get() as unknown as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function assertExpirySweepQueryPlan(): void {
  const details = explainBusinessQuery(`
    SELECT *
    FROM resource_authorization_grants
    WHERE status IN ('active', 'paused')
      AND expires_at IS NOT NULL
      AND expires_at <= ?
    ORDER BY expires_at ASC, updated_at ASC, id ASC
    LIMIT ?
  `, ['2026-01-01T00:00:00.000Z', maxAuthorizationExpirySweepBatchSize])
  assert(details.includes('idx_resource_authorization_grants_expiry_sweep'), `授权过期扫描应走 expires_at 部分索引，实际计划：${details}`)
  assert(!details.includes('SCAN resource_authorization_grants'), `授权过期扫描不能全表扫描 grant 表，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `授权过期扫描不应为排序创建临时 B-TREE，实际计划：${details}`)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
