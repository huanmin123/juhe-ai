import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-stable-sort-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'resource-authorization-stable-sort-secret'
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
    username: 'authorization_stable_sort_owner',
    displayName: '授权稳定排序所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_stable_sort_grantee',
    displayName: '授权稳定排序被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }

  const oldest = createStableAuthorization('授权稳定排序-A', grantee.id, '2026-01-01T00:00:00.000Z', ownerAccess)
  const middle = createStableAuthorization('授权稳定排序-B', grantee.id, '2026-01-01T00:00:01.000Z', ownerAccess)
  const newest = createStableAuthorization('授权稳定排序-C', grantee.id, '2026-01-01T00:00:02.000Z', ownerAccess)
  const expectedIds = [newest.id, middle.id, oldest.id]

  assert.deepEqual(listStableAuthorizationIds(expectedIds, ownerAccess), expectedIds, '授权列表初始顺序应按创建时间倒序稳定')

  repositories.updateResourceAuthorization(middle.id, { status: 'paused' }, ownerAccess)
  assert.deepEqual(listStableAuthorizationIds(expectedIds, ownerAccess), expectedIds, '暂停授权不应因状态变化改变列表顺序')

  repositories.updateResourceAuthorization(oldest.id, { expiresAt: '2099-01-01T00:00:00.000Z' }, ownerAccess)
  assert.deepEqual(listStableAuthorizationIds(expectedIds, ownerAccess), expectedIds, '调整授权配置不应因更新时间改变列表顺序')

  const firstPage = repositories.listResourceAuthorizationsPage({ status: 'all' }, ownerAccess, { page: 1, pageSize: 2 })
  assert.deepEqual(firstPage.items.map((authorization) => authorization.id), expectedIds.slice(0, 2), '分页第一页应沿用创建时间稳定排序')
  const secondPage = repositories.listResourceAuthorizationsPage({ status: 'all' }, ownerAccess, { page: 2, pageSize: 2 })
  assert.deepEqual(secondPage.items.map((authorization) => authorization.id), expectedIds.slice(2), '分页第二页应沿用创建时间稳定排序')

  console.log('统一授权列表稳定排序回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createStableAuthorization(name: string, granteeId: string, createdAt: string, access: { systemAccountId: string; role: 'user' }): { id: string } {
  const group = repositories.createGroup({
    name,
    providerCode: 'openai',
    enabled: true
  }, access)
  const authorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId,
    remark: '授权稳定排序回归'
  }, access)
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE resource_authorization_grants SET created_at = ?, updated_at = ? WHERE id = ?')
    .run(createdAt, createdAt, authorization.id)
  return { id: authorization.id }
}

function listStableAuthorizationIds(expectedIds: string[], access: { systemAccountId: string; role: 'user' }): string[] {
  const expected = new Set(expectedIds)
  return repositories.listResourceAuthorizations({ status: 'all' }, access)
    .filter((authorization) => expected.has(authorization.id))
    .map((authorization) => authorization.id)
}
