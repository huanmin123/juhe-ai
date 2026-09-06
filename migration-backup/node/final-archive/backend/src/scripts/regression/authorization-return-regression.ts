import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-return-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-return-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { groupsRouter },
  { authorizationsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/groups/groups.routes.js'),
  import('../../modules/authorizations/authorizations.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

assertAccountAuthorizationReturnRouteBoundary()

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/my-groups', forceSelfAccessScope, groupsRouter)
app.use('/__aisys__/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)
app.use('/__aisys__/api/groups', requireAdmin, groupsRouter)
app.use('/__aisys__/api/authorizations', requireAdmin, authorizationsRouter)

let server: ReturnType<typeof app.listen> | undefined

try {
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('授权归还回归服务地址不可用')
  }
  const baseUrl = `http://127.0.0.1:${address.port}`
  const seed = seedData()
  const ownerAccess = { systemAccountId: seed.ownerId, role: 'user' as const }
  const granteeAccess = { systemAccountId: seed.granteeId, role: 'user' as const }

  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1
    WHERE id = ?
  `).run(seed.ownerAccountId)
  const authorizedAccount = authorizedAccountForSource(seed.ownerAccountId, granteeAccess)
  const accountAuthorizationId = authorizedAccount?.accountAuthorizationId
  assert(accountAuthorizationId, '被授权账户应带运行态授权 ID')
  const j1SourceGroup = repositories.createGroup({
    name: 'J1 授权归还来源分组',
    providerCode: 'openai'
  }, ownerAccess)
  const j1TargetGroup = repositories.createGroup({
    name: 'J1 授权归还目标分组',
    providerCode: 'openai'
  }, granteeAccess)
  const j1SourceAccount = repositories.createAccount({
    providerCode: 'openai',
    providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
    name: 'J1 授权归还来源账户',
    type: 'api_key',
    status: 'active',
    schedulable: true,
    credentials: { api_key: 'sk-j1-authorization-return', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.5'],
    groupId: j1SourceGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: j1SourceAccount.id,
    granteeType: 'system_account',
    granteeId: seed.granteeId,
    targetGroupId: j1TargetGroup.id,
    remark: 'J1 授权归还输入 epoch 回归'
  }, ownerAccess)
  const j1AuthorizedAccount = authorizedAccountForSource(j1SourceAccount.id, granteeAccess)
  assert(j1AuthorizedAccount?.id, 'J1 授权账户必须创建授权实例')
  databaseModule.getBusinessDatabase().prepare('DELETE FROM account_health_jobs_input_outbox').run()
  await postOk(baseUrl, `/__aisys__/api/my-accounts/${j1AuthorizedAccount.id}/return-authorization`, seed.granteeCookie)
  const returnedInputIntent = databaseModule.getBusinessDatabase().prepare(`
    SELECT account_id, event_kind, reason
    FROM account_health_jobs_input_outbox
    ORDER BY created_at ASC, event_id ASC
  `).get() as { account_id?: string, event_kind?: string, reason?: string } | undefined
  assert.equal(returnedInputIntent?.account_id, j1AuthorizedAccount.id, '归还授权前必须为授权实例保留新的 J1 input epoch')
  assert.equal(returnedInputIntent?.event_kind, 'snapshot', '归还授权由发布器按最新资格转换为 tombstone，业务事务本身只保存可重放意图')
  assert.equal(returnedInputIntent?.reason, 'authorization_grant_changed', '归还授权必须留下可审计的授权输入发布原因')
  assert.equal(repositories.findAccountSummary(seed.ownerAccountId, ownerAccess)?.status, 'active', '持续探活同步回归要求来源账户保持正常')
  const oldObservationStartedAt = '2020-01-01T00:00:00.000Z'
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'temporary_unavailable',
        schedulable = 1,
        cooldown_retest_failure_count = 4,
        cooldown_retest_observation_started_at = ?,
        cooldown_retest_last_at = ?,
        cooldown_retest_last_status_code = 503,
        cooldown_until = ?
    WHERE id = ?
  `).run(oldObservationStartedAt, oldObservationStartedAt, oldObservationStartedAt, authorizedAccount.id)
  const policyChangedAt = Date.now()
  repositories.updateAccount(seed.ownerAccountId, {
    temporaryUnavailableContinuousProbeEnabled: false
  }, ownerAccess)
  const boundedInstance = databaseModule.getBusinessDatabase().prepare(`
    SELECT cooldown_retest_failure_count, cooldown_retest_observation_started_at,
      cooldown_retest_generation, cooldown_retest_last_at, cooldown_retest_last_status_code, cooldown_until,
      temporary_unavailable_continuous_probe_enabled
    FROM accounts
    WHERE id = ?
  `).get(authorizedAccount.id) as {
    cooldown_retest_failure_count: number
    cooldown_retest_observation_started_at: string | null
    cooldown_retest_generation: string | null
    cooldown_retest_last_at: string | null
    cooldown_retest_last_status_code: number | null
    cooldown_until: string | null
    temporary_unavailable_continuous_probe_enabled: number
  }
  assert.equal(boundedInstance.temporary_unavailable_continuous_probe_enabled, 0, '授权实例必须同步来源的关闭策略')
  assert.equal(boundedInstance.cooldown_retest_failure_count, 0, '来源正常时也必须按授权实例自身临时不可用状态清零失败计数')
  assert.notEqual(boundedInstance.cooldown_retest_observation_started_at, oldObservationStartedAt, '授权实例必须从保存时重启十分钟观察代次')
  assert.ok(Date.parse(boundedInstance.cooldown_retest_observation_started_at ?? '') >= policyChangedAt, '授权实例新观察代次不得早于本次保存')
  assert.match(boundedInstance.cooldown_retest_generation ?? '', /^cooldown:/, '授权实例重启观察窗口时必须创建完整的 cooldown fence')
  assert.equal(boundedInstance.cooldown_retest_last_at, null, '授权实例重启观察窗口时必须清理旧复测时间')
  assert.equal(boundedInstance.cooldown_retest_last_status_code, null, '授权实例重启观察窗口时必须清理旧状态码')
  assert.ok(Date.parse(boundedInstance.cooldown_until ?? '') > policyChangedAt, '授权实例重启观察窗口后必须尽快安排下一次复测')
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE accounts
    SET status = 'active', schedulable = 1, cooldown_until = NULL,
        cooldown_retest_failure_count = 0, cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL
    WHERE id = ?
  `).run(authorizedAccount.id)
  assert.equal(authorizedAccount?.permissions?.canDelete, false, '个人直授权账户不应暴露删除权限')
  assert.equal(authorizedAccount?.permissions?.canReturnAuthorization, true, '个人直授权账户应暴露归还授权权限')
  const initialAccountGrant = repositories
    .listResourceAuthorizations({ direction: 'inbound', status: 'all' }, granteeAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert(initialAccountGrant, '被授权账户应能在授权操作列表读取授权业务记录')
  await postOk(baseUrl, `/__aisys__/api/my-accounts/${authorizedAccount.id}/return-authorization`, seed.granteeCookie)
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === seed.ownerAccountId),
    false,
    '被授权用户从账户列表归还账户授权后不应继续看到该授权账户'
  )
  assert.equal(
    repositories.listAccounts(ownerAccess).some((account) => account.id === seed.ownerAccountId),
    true,
    '被授权用户归还账户授权不应删除授权方原账户'
  )
  const returnedAccountGrant = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(returnedAccountGrant?.status, 'returned', '被授权用户归还后，授权方授权列表仍应保留已归还记录')
  const restoredAccountGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: seed.ownerAccountId,
    granteeType: 'system_account',
    granteeId: seed.granteeId,
    targetGroupId: seed.granteeTargetGroupId,
    remark: '授权账户归还后重新授权'
  }, ownerAccess)
  assert.equal(restoredAccountGrant.id, returnedAccountGrant?.id, '归还后重新授权应复用原授权业务记录')
  const restoredRuntimeAuthorizationId = authorizedAccountForSource(seed.ownerAccountId, granteeAccess)?.accountAuthorizationId
  assert.equal(restoredRuntimeAuthorizationId, accountAuthorizationId, '归还后重新授权应复用原用户级授权 ID')
  const pausedAccountGrant = repositories.updateResourceAuthorization(restoredAccountGrant.id, { status: 'paused' }, ownerAccess)
  assert.equal(pausedAccountGrant?.status, 'paused', '授权方暂停后授权状态应为 paused')
  const pausedAccount = authorizedAccountForSource(seed.ownerAccountId, granteeAccess)
  assert.equal(pausedAccount?.authorizationStatus, 'paused', '暂停授权后被授权账户仍应可见但标记为暂停')
  repositories.updateResourceAuthorization(restoredAccountGrant.id, { status: 'active' }, ownerAccess)
  const reactivatedAccount = authorizedAccountForSource(seed.ownerAccountId, granteeAccess)
  assert(reactivatedAccount?.id, '恢复授权后应能重新看到被授权账户')
  assert.equal(reactivatedAccount?.authorizationStatus, 'active', '暂停后恢复授权应同步恢复运行态授权状态')
  assert.equal(reactivatedAccount?.status, 'active', '暂停后恢复授权应让被授权账户恢复可调用状态')
  await deleteRejected(baseUrl, `/__aisys__/api/my-accounts/${reactivatedAccount.id}`, seed.granteeCookie, /授权账户请使用归还操作/)
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === seed.ownerAccountId),
    true,
    '删除授权实例被拒绝后，被授权账户仍应可见'
  )
  const inboundAccountGrant = repositories
    .listResourceAuthorizations({ direction: 'inbound', status: 'all' }, granteeAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(inboundAccountGrant?.id, restoredAccountGrant.id, '个人授权列表应返回授权业务记录 ID')
  await returnOk(
    baseUrl,
    `/__aisys__/api/my-authorizations/${inboundAccountGrant.id}/return`,
    seed.granteeCookie,
    inboundAccountGrant.updatedAt
  )
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === seed.ownerAccountId),
    false,
    '被授权用户通过个人授权列表归还后不应继续看到该授权账户'
  )
  const returnedAccountGrantByListId = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.resourceType === 'account' && authorization.resourceId === seed.ownerAccountId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(returnedAccountGrantByListId?.status, 'returned', '个人授权列表归还后，授权方授权列表仍应保留已归还记录')

  const teamAuthorizedAccount = authorizedAccountForSource(seed.teamAccountId, granteeAccess)
  assert(teamAuthorizedAccount?.id, '团队授权账户应能在被授权用户账户列表中看到')
  assert.equal(teamAuthorizedAccount?.permissions?.canDelete, false, '团队来源授权账户不应暴露删除权限')
  assert.equal(teamAuthorizedAccount?.permissions?.canReturnAuthorization, false, '团队来源授权账户不应暴露个人归还权限')
  await postRejected(baseUrl, `/__aisys__/api/my-accounts/${teamAuthorizedAccount.id}/return-authorization`, seed.granteeCookie, /授权账户不存在或不可归还/)
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === seed.teamAccountId),
    true,
    '团队来源授权账户不能通过个人归还入口移除'
  )
  const mixedAuthorizedAccount = authorizedAccountForSource(seed.mixedAccountId, granteeAccess)
  assert(mixedAuthorizedAccount?.id, '被团队覆盖的个人授权账户应仍能通过团队来源看到')
  assert.equal(mixedAuthorizedAccount?.permissions?.canDelete, false, '被团队覆盖的个人授权账户不应暴露删除权限')
  assert.equal(mixedAuthorizedAccount?.permissions?.canReturnAuthorization, false, '被团队覆盖的个人授权账户不应暴露账户归还权限')
  await postRejected(baseUrl, `/__aisys__/api/my-accounts/${mixedAuthorizedAccount.id}/return-authorization`, seed.granteeCookie, /授权账户不存在或不可归还/)
  const mixedDirectGrant = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.id === seed.mixedDirectGrantId)
  assert(mixedDirectGrant, '被团队覆盖的个人授权负向归还前应存在授权记录')
  assert(mixedDirectGrant.updatedAt, '被团队覆盖的个人授权负向归还必须携带列表 CAS 版本')
  await deleteRejected(
    baseUrl,
    `/__aisys__/api/my-authorizations/${seed.mixedDirectGrantId}/return`,
    seed.granteeCookie,
    /授权记录不存在/,
    mixedDirectGrant.updatedAt
  )
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === seed.mixedAccountId),
    true,
    '被团队覆盖的个人授权不能通过归还入口移除团队来源账户'
  )

  const groupAuthorizationId = repositories
    .listGroups({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((group) => group.id === seed.ownerGroupId)?.groupAuthorizationId
  assert(groupAuthorizationId, '被授权分组应带运行态授权 ID')
  const authorizedGroup = repositories
    .listGroups({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((group) => group.id === seed.ownerGroupId)
  assert.equal(authorizedGroup?.accessType, 'authorized', '被授权分组应在分组列表标记为授权资源')
  assert.equal(authorizedGroup?.permissions?.canEdit, true, '被授权分组应允许调整使用方本地配置')
  assert.equal(authorizedGroup?.permissions?.canReturnAuthorization, true, '个人直授权分组应允许在分组页归还')
  const authorizedGroupEditDetail = await repositories.findGroupEditDetailAsync(seed.ownerGroupId, granteeAccess)
  assert(authorizedGroupEditDetail?.updatedAt, '被授权分组编辑详情应返回版本')
  await patchOk(baseUrl, `/__aisys__/api/my-groups/${seed.ownerGroupId}`, seed.granteeCookie, {
    expectedUpdatedAt: authorizedGroupEditDetail.updatedAt,
    enabled: true,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 2,
      maxQueueWaitMs: 30000,
      clientIpConcurrencyLimit: 3,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 0
    }
  })
  const updatedAuthorizedGroup = repositories
    .listGroups({ systemAccountId: seed.granteeId, role: 'user' as const })
    .find((group) => group.id === seed.ownerGroupId)
  assert.equal(updatedAuthorizedGroup?.groupType, 'high_concurrency', '被授权人应能把授权分组切为自己的高并发配置')
  assert.equal(updatedAuthorizedGroup?.schedulingPolicy?.defaultSoftConcurrency, 2, '授权分组本地调度配置应回显给被授权人')
  const ownerGroupAfterAuthorizedUpdate = repositories.findGroupSummary(seed.ownerGroupId, ownerAccess)
  assert.equal(ownerGroupAfterAuthorizedUpdate?.groupType, 'personal', '被授权人调整分组类型不应影响授权方原分组')
  assert.equal(ownerGroupAfterAuthorizedUpdate?.schedulingPolicy, undefined, '被授权人调整调度配置不应写回授权方原分组')
  const granteeRuntimeGroupAccess = repositories.resolveGroupUsageAccessMetadata(seed.ownerGroupId, seed.granteeId)
  assert.equal(granteeRuntimeGroupAccess?.groupType, 'high_concurrency', '授权分组运行态应读取被授权人的本地分组类型')
  assert.equal(granteeRuntimeGroupAccess?.schedulingPolicy?.clientIpConcurrencyLimit, 3, '授权分组运行态应读取被授权人的本地调度配置')
  const ownerRuntimeGroupAccess = repositories.resolveGroupUsageAccessMetadata(seed.ownerGroupId, seed.ownerId)
  assert.equal(ownerRuntimeGroupAccess?.groupType, 'personal', '授权方运行态仍应读取原分组配置')
  const updatedAuthorizedGroupEditDetail = await repositories.findGroupEditDetailAsync(seed.ownerGroupId, granteeAccess)
  assert(updatedAuthorizedGroupEditDetail?.updatedAt, '更新后的被授权分组编辑详情应返回版本')
  await patchOk(baseUrl, `/__aisys__/api/my-groups/${seed.ownerGroupId}`, seed.granteeCookie, {
    expectedUpdatedAt: updatedAuthorizedGroupEditDetail.updatedAt,
    enabled: false,
    groupType: 'high_concurrency',
    schedulingPolicy: {
      defaultSoftConcurrency: 2,
      maxQueueWaitMs: 30000,
      clientIpConcurrencyLimit: 3,
      clientIpConcurrencyOverflowMode: 'queue',
      imageLaneMaxConcurrency: 0
    }
  })
  assert.equal(repositories.resolveGroupUsageAccessMetadata(seed.ownerGroupId, seed.granteeId), undefined, '被授权人本地停用授权分组后运行态不应继续可用')
  const inboundGroupGrant = repositories
    .listResourceAuthorizations({ direction: 'inbound', status: 'all' }, granteeAccess)
    .find((authorization) => authorization.resourceType === 'group' && authorization.resourceId === seed.ownerGroupId && authorization.granteeSystemAccountId === seed.granteeId)
  assert(inboundGroupGrant, '被授权分组应能在授权操作列表读取授权业务记录')
  await postOk(baseUrl, `/__aisys__/api/my-groups/${seed.ownerGroupId}/return-authorization`, seed.granteeCookie)
  assert.equal(
    repositories.listGroups({ systemAccountId: seed.granteeId, role: 'user' as const }).some((group) => group.id === seed.ownerGroupId),
    false,
    '被授权用户归还分组授权后不应继续看到该授权分组'
  )
  assert.equal(
    repositories.listGroups({ systemAccountId: seed.ownerId, role: 'user' as const }).some((group) => group.id === seed.ownerGroupId),
    true,
    '被授权用户归还分组授权不应删除授权方原分组'
  )
  const returnedGroupGrant = repositories
    .listResourceAuthorizations({}, ownerAccess)
    .find((authorization) => authorization.resourceType === 'group' && authorization.resourceId === seed.ownerGroupId && authorization.granteeSystemAccountId === seed.granteeId)
  assert.equal(returnedGroupGrant?.status, 'returned', '分组页归还后，授权方授权列表仍应保留已归还记录')

  const adminManagedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: seed.gptProtocolProfileId,
    name: '管理员代归还授权账户',
    type: 'api_key',
    credentials: { api_key: 'sk-admin-authorization-return', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1'],
    groupId: seed.ownerGroupId
  }, { systemAccountId: seed.ownerId, role: 'user' as const })
  const adminManagedGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: adminManagedAccount.id,
    granteeType: 'system_account',
    granteeId: seed.granteeId,
    targetGroupId: seed.granteeTargetGroupId,
    remark: '管理员代归还授权使用权'
  }, { systemAccountId: seed.ownerId, role: 'user' as const })
  const adminManagedAuthorizationId = authorizedAccountForSource(adminManagedAccount.id, granteeAccess)?.accountAuthorizationId
  assert(adminManagedAuthorizationId, '管理员代归还前应能看到被授权账户')
  await returnOk(
    baseUrl,
    `/__aisys__/api/authorizations/${adminManagedGrant.id}/return?systemAccountId=${seed.granteeId}`,
    seed.adminCookie,
    adminManagedGrant.updatedAt
  )
  assert.equal(
    repositories.listAccounts(granteeAccess).some((account) => account.authorizationInstanceSourceAccountId === adminManagedAccount.id),
    false,
    '管理员按用户作用域归还授权使用权后，该用户不应继续看到授权账户'
  )

  console.log('授权归还回归通过：被授权人可归还账户/分组授权使用权，且不删除授权方原资源')
} finally {
  await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData() {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const owner = repositories.createSystemAccount({
    username: 'authorization_return_owner',
    displayName: '授权归还所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_return_grantee',
    displayName: '授权归还被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const adminAccess = { systemAccountId: admin.id, role: 'admin' as const }
  const ownerGroup = repositories.createGroup({
    name: '授权归还分组',
    providerCode: 'gpt'
  }, ownerAccess)
  const granteeTargetGroup = repositories.createGroup({
    name: '授权归还被授权人目标分组',
    providerCode: 'gpt'
  }, { systemAccountId: grantee.id, role: 'user' as const })
  const gptProtocolProfileId = repositories.defaultProviderProtocolProfile('gpt')?.id
  assert(gptProtocolProfileId, 'GPT 默认协议档案不存在')
  const ownerAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: gptProtocolProfileId,
    groupId: ownerGroup.id,
    name: '授权归还账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorization-return', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1']
  }, ownerAccess)
  const teamAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: gptProtocolProfileId,
    groupId: ownerGroup.id,
    name: '授权归还团队来源账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorization-return-team', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1']
  }, ownerAccess)
  const mixedAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: gptProtocolProfileId,
    groupId: ownerGroup.id,
    name: '授权归还团队覆盖个人来源账户',
    type: 'api_key',
    credentials: { api_key: 'sk-authorization-return-mixed', base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.1']
  }, ownerAccess)
  const team = repositories.createSystemTeam({ name: '授权归还团队' }, adminAccess)
  repositories.addSystemTeamMembers(team.id, { systemAccountIds: [grantee.id] }, adminAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: ownerAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id,
    remark: '授权账户归还回归'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: ownerGroup.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '授权分组归还回归'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: teamAccount.id,
    granteeType: 'team',
    granteeId: team.id,
    remark: '团队来源授权账户不可个人归还回归'
  }, ownerAccess)
  const mixedDirectGrant = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: mixedAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeTargetGroup.id,
    remark: '团队覆盖个人来源前的直授权回归'
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: mixedAccount.id,
    granteeType: 'team',
    granteeId: team.id,
    remark: '团队覆盖个人来源后不可个人归还回归'
  }, ownerAccess)
  return {
    adminCookie: sessionCookie(admin.id),
    granteeCookie: sessionCookie(grantee.id),
    granteeId: grantee.id,
    granteeTargetGroupId: granteeTargetGroup.id,
    gptProtocolProfileId,
    mixedAccountId: mixedAccount.id,
    mixedDirectGrantId: mixedDirectGrant.id,
    ownerAccountId: ownerAccount.id,
    ownerGroupId: ownerGroup.id,
    ownerId: owner.id,
    teamAccountId: teamAccount.id
  }
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

function authorizedAccountForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  return repositories.listAccounts(access)
    .find((account) => account.authorizationInstanceSourceAccountId === sourceAccountId)
}

async function returnOk(baseUrl: string, path: string, cookie: string, expectedUpdatedAt: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'DELETE',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ expectedUpdatedAt })
  })
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${await response.text()}`)
  }
}

async function postOk(baseUrl: string, path: string, cookie: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, { method: 'POST', headers: { cookie } })
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${await response.text()}`)
  }
}

async function patchOk(baseUrl: string, path: string, cookie: string, body: unknown): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${await response.text()}`)
  }
}

async function postRejected(baseUrl: string, path: string, cookie: string, pattern: RegExp): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, { method: 'POST', headers: { cookie } })
  const text = await response.text()
  assert.equal(response.ok, false, `${path} 应拒绝请求`)
  assert.match(text, pattern, `${path} 应返回预期中文错误`)
}

async function deleteRejected(
  baseUrl: string,
  path: string,
  cookie: string,
  pattern: RegExp,
  expectedUpdatedAt?: string
): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'DELETE',
    headers: expectedUpdatedAt ? { cookie, 'content-type': 'application/json' } : { cookie },
    body: expectedUpdatedAt ? JSON.stringify({ expectedUpdatedAt }) : undefined
  })
  const text = await response.text()
  assert.equal(response.ok, false, `${path} 应拒绝请求`)
  assert.match(text, pattern, `${path} 应返回预期中文错误`)
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => {
      listeningServer.closeAllConnections?.()
      resolvePromise()
    }, 1000)
    listeningServer.close((error) => {
      clearTimeout(timeout)
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
    listeningServer.closeIdleConnections?.()
  })
}

function assertAccountAuthorizationReturnRouteBoundary(): void {
  const accountsRoutesSource = readFileSync(resolve('src/modules/accounts/accounts.routes.ts'), 'utf8')
  const authorizationReturnRoutesSource = readFileSync(resolve('src/modules/accounts/account-authorization-return.routes.ts'), 'utf8')
  assert.match(
    accountsRoutesSource,
    /registerAccountAuthorizationReturnRoutes\(accountsRouter\)/,
    '账户主路由必须注册授权归还子路由'
  )
  assert.equal(
    accountsRoutesSource.includes("accountsRouter.post('/:id/return-authorization'"),
    false,
    '账户授权归还路由不应继续内联在账户主路由'
  )
  assert.match(
    authorizationReturnRoutesSource,
    /operationKey:\s*'accounts\.return_authorization'/,
    '账户授权归还子路由必须保留 operationKey'
  )
  assert.match(
    authorizationReturnRoutesSource,
    /returnAccountAuthorizationInstanceForGrantee/,
    '账户授权归还子路由必须调用归还授权实例仓储方法'
  )
}
