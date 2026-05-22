import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-resource-authorization-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
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
  for (let index = 0; index < 250; index += 1) {
    const group = repositories.createGroup({
      name: `授权单条读取回归-${String(index).padStart(3, '0')}`,
      providerCode: 'openai',
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
  }

  databaseModule.getDatabase()
    .prepare("UPDATE resource_authorization_grants SET created_at = '2000-01-01T00:00:00.000Z', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(targetId)

  const firstPageLikeList = repositories.listResourceAuthorizations({ status: 'all' }, ownerAccess).slice(0, 200)
  assert.equal(firstPageLikeList.some((authorization) => authorization.id === targetId), false, '最早创建的第 250 条外授权不应出现在前 200 条列表窗口里')

  const target = repositories.findResourceAuthorization(targetId, ownerAccess, { includeUsage: false })
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的授权')
  assert.equal(target?.resourceName, targetGroupName, '按 ID 单条读取应返回完整授权摘要')
  assert.equal(target?.usage.requestCount, 0, '轻量单条读取不应为操作日志 before 额外加载用量')

  const updated = repositories.updateResourceAuthorization(targetId, { expiresAt: '2099-01-01T00:00:00.000Z' }, ownerAccess)
  assert.equal(updated?.id, targetId, '更新授权应通过单条读取返回目标授权摘要')
  assert.equal(updated?.expiresAt, '2099-01-01T00:00:00.000Z', '更新授权应保留新的过期时间')

  const revoked = repositories.revokeResourceAuthorization(targetId, { revokeAll: true }, ownerAccess)
  assert.equal(revoked?.id, targetId, '撤销授权应通过单条读取返回目标授权摘要')
  assert.equal(revoked?.status, 'revoked', '撤销授权应返回已回收状态')
  const defaultListAfterRevoke = repositories.listResourceAuthorizations({}, ownerAccess)
  assert.equal(defaultListAfterRevoke.some((authorization) => authorization.id === targetId), false, '默认授权列表不应显示已回收授权')
  const allListAfterRevoke = repositories.listResourceAuthorizations({ status: 'all' }, ownerAccess)
  assert.equal(allListAfterRevoke.some((authorization) => authorization.id === targetId), true, '显式全部状态查询仍应保留已回收授权用于审计和重新授权')

  console.log('资源授权单条读取回归通过：操作日志 before 和写路径不再依赖全量授权列表装配')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
