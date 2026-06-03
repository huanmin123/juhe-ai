import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-deleted-account-related-cleanup-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'deleted-account-related-cleanup-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, usageStatsRepository, usageRecordShards, usageStatsHelpers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/usage-stats-helpers.js')
])

const createdAt = new Date(Date.now() - 10 * 60 * 1000).toISOString()
const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const owner = repositories.createSystemAccount({
    username: 'deleted_account_owner',
    displayName: '删除账户所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'deleted_account_grantee',
    displayName: '删除账户被授权人',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const granteeAccess = { systemAccountId: grantee.id, role: 'user' as const }
  const team = repositories.createSystemTeam({ name: '删除账户清理团队' }, adminAccess)
  assert(repositories.addSystemTeamMembers(team.id, { systemAccountIds: [grantee.id] }, adminAccess), '团队成员应添加成功')

  const ownerGroup = repositories.createGroup({ name: '删除账户归属分组', providerCode: 'openai' }, ownerAccess)
  const granteeGroup = repositories.createGroup({ name: '删除账户授权分组', providerCode: 'openai' }, granteeAccess)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '删除关联清理账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-deleted-account-related-cleanup',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: account.id,
    granteeType: 'team',
    granteeId: team.id,
    remark: '删除账户清理回归'
  }, ownerAccess)
  const authorizedInstance = authorizedInstanceForSource(account.id, granteeAccess)
  assert(repositories.setAccountGroup(authorizedInstance.id, granteeGroup.id, granteeAccess), '被授权实例账户应能绑定到被授权人分组')

  const runtimeAuthorizationId = accountRuntimeAuthorizationId(account.id, grantee.id)
  assert.ok(runtimeAuthorizationId, '团队授权应生成运行时账户授权')
  const ownerUsageId = 'usage_deleted_account_related_cleanup_owner'
  const instanceUsageId = 'usage_deleted_account_related_cleanup_instance'
  seedOwnerUsageRecord(ownerUsageId, account.id, owner.id)
  seedAuthorizedUsageRecord(instanceUsageId, authorizedInstance.id, owner.id, grantee.id, runtimeAuthorizationId, team.id)
  seedAuditData(account.id, 'owner')
  seedAuditData(authorizedInstance.id, 'instance')
  seedModelCheckRun(account.id, owner.id, 'owner')
  seedModelCheckRun(authorizedInstance.id, grantee.id, 'instance')
  seedDetachedAccountStats(account.id, owner.id)
  seedDetachedAccountStats(authorizedInstance.id, grantee.id)

  assert.equal(usageStatsRepository.aggregateUsageStatsBatch(10), 2, '账户删除前的使用记录应先完成统计聚合')
  usageStatsRepository.refreshUsageQuotaHourlyWindowsCache()
  usageStatsRepository.refreshUsageRankSnapshots()
  const statDate = usageStatsHelpers.dateKey(new Date(createdAt), usageStatsHelpers.usageStatsTimezone())
  const adminUsageOverview = repositories.getAccountUsageStatsOverview(adminAccess, {
    startDate: statDate,
    endDate: statDate,
    days: 1,
    maxDays: 31
  })
  const adminAuthorizedUsageRow = adminUsageOverview.rows.find((row) => row.id === authorizedInstance.id)
  assert.equal(adminAuthorizedUsageRow?.rangeUsage.requestCount, 1, '管理员全局账号用量应按被授权实例所属用户读取授权账户统计')
  assert.equal(usageStatsTotal(owner.id, 'account', account.id), 1, '删除前原账户自用统计应存在')
  assert.equal(usageStatsTotal(grantee.id, 'account', authorizedInstance.id), 1, '删除前授权实例账户统计应计入被授权使用方')
  assert.equal(usageStatsTotal(grantee.id, 'caller_account', authorizedInstance.id), 1, '删除前调用方账户统计应按授权实例账户记录')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization', runtimeAuthorizationId), 1, '删除前授权统计应计入被授权使用方')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization_team', `${authorizedInstance.id}:${team.id}`), 1, '删除前团队授权统计应按授权实例账户计入被授权使用方')
  assert.equal(usageScopeRangeWindowRequestCount(grantee.id, 'account', authorizedInstance.id), 1, '删除前范围窗口应存在被授权使用方实例账户统计')
  assert.equal(usageRankSnapshotMetric(grantee.id, 'account_authorization', runtimeAuthorizationId), 0.12, '删除前授权排行快照应存在')
  assert.equal(authorizationUserUsageRangeWindowRequestCount(owner.id, account.id), 1, '删除前授权报表窗口应存在账户过滤统计')
  assert.equal(authorizationUserUsageRangeWindowRequestCount(owner.id, authorizedInstance.id), 0, '授权方报表资源过滤不应写成被授权实例 ID')

  const directReturnAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '被授权人自删账户来源',
    type: 'api_key',
    credentials: {
      api_key: 'sk-deleted-account-direct-return',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  const directReturnAuthorization = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: directReturnAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '被授权人自删回归'
  }, ownerAccess)
  const directReturnInstance = authorizedInstanceForSource(directReturnAccount.id, granteeAccess)
  const directDeleteResult = repositories.deleteAccountWithRelatedCleanup(directReturnInstance.id, granteeAccess)
  assert.equal(directDeleteResult.deleted, true, '被授权人应能删除自己的授权实例账户')
  assert.equal(accountExists(directReturnAccount.id), true, '被授权人删除授权实例不应逻辑删除来源账户')
  assert.equal(accountExists(directReturnInstance.id), false, '被授权人删除后授权实例不应继续出现在业务读取中')
  assert.equal(rawAccountExists(directReturnInstance.id), true, '被授权人删除后授权实例业务行应暂时保留')
  assert.ok(accountDeletedAt(directReturnInstance.id), '被授权人删除后授权实例应写入 deleted_at')
  assert.equal(groupAccountCount(directReturnInstance.id), 1, '被授权人删除阶段不应同步删除授权实例分组绑定')
  assert.equal(resourceAuthorizationCount(directReturnAccount.id), 1, '被授权人删除阶段运行时授权应保留历史记录')
  assert.equal(resourceAuthorizationGrantCount(directReturnAccount.id), 1, '被授权人删除阶段授权 grant 应保留历史记录')
  assert.equal(resourceAuthorizationStatus(directReturnAccount.id), 'returned', '被授权人删除后运行时授权应标记为已归还')
  assert.equal(resourceAuthorizationGrantStatus(directReturnAccount.id), 'returned', '被授权人删除后个人授权 grant 应标记为已归还')
  assert.equal(repositories.listResourceAuthorizations({ resourceId: directReturnAccount.id, status: 'active' }, ownerAccess).length, 0, '被授权人删除后生效授权列表不应继续展示该授权')
  assert.equal(repositories.listResourceAuthorizations({ resourceId: directReturnAccount.id, status: 'all' }, ownerAccess).some((item) => item.id === directReturnAuthorization.id && item.status === 'returned'), true, '被授权人删除后全部状态仍可追溯已归还授权')
  assert.equal(cleanupTargetExists(directReturnInstance.id), false, '被授权人逻辑删除阶段不应登记即时关联清理目标')
  const directReauthorized = repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: directReturnAccount.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '被授权人自删后重新授权回归'
  }, ownerAccess)
  const directRestoredInstance = authorizedInstanceForSource(directReturnAccount.id, granteeAccess)
  assert.equal(directReauthorized.id, directReturnAuthorization.id, '重新授权应复用已归还的个人授权记录')
  assert.equal(directRestoredInstance.id, directReturnInstance.id, '重新授权应恢复同一个逻辑删除授权实例账户')
  assert.equal(accountDeletedAt(directRestoredInstance.id), undefined, '重新授权恢复后授权实例不应仍处于逻辑删除状态')
  assert.equal(resourceAuthorizationStatus(directReturnAccount.id), 'active', '重新授权后运行时授权应恢复生效')
  assert.equal(resourceAuthorizationGrantStatus(directReturnAccount.id), 'active', '重新授权后个人授权 grant 应恢复生效')

  const deleteResult = repositories.deleteAccountWithRelatedCleanup(account.id, ownerAccess)
  assert.equal(deleteResult.deleted, true, 'AI 账户删除应成功')
  assert.equal(accountExists(account.id), false, '逻辑删除后父账户不应继续出现在业务读取中')
  assert.equal(accountExists(authorizedInstance.id), false, '逻辑删除后被授权实例不应继续出现在业务读取中')
  assert.equal(rawAccountExists(account.id), true, '逻辑删除后父账户业务行应暂时保留')
  assert.equal(rawAccountExists(authorizedInstance.id), true, '逻辑删除后被授权实例业务行应暂时保留')
  assert.ok(accountDeletedAt(account.id), '逻辑删除后父账户应写入 deleted_at')
  assert.ok(accountDeletedAt(authorizedInstance.id), '逻辑删除后被授权实例应写入 deleted_at')
  assert.equal(groupAccountCount(account.id), 1, '逻辑删除阶段不应删除父账户分组绑定')
  assert.equal(groupAccountCount(authorizedInstance.id), 1, '逻辑删除阶段不应删除被授权实例分组绑定')
  assert.equal(resourceAuthorizationCount(account.id), 1, '逻辑删除阶段运行时授权应保留历史记录')
  assert.equal(resourceAuthorizationGrantCount(account.id), 1, '逻辑删除阶段授权规则应保留历史记录')
  assert.equal(resourceAuthorizationStatus(account.id), 'revoked', '父账户逻辑删除后运行时授权应标记为已回收')
  assert.equal(resourceAuthorizationGrantStatus(account.id), 'revoked', '父账户逻辑删除后授权操作记录应标记为已回收')
  assert.equal(repositories.listResourceAuthorizations({ resourceId: account.id, status: 'active' }, ownerAccess).length, 0, '父账户逻辑删除后默认生效授权列表不应继续展示该授权')
  assert.equal(repositories.listResourceAuthorizations({ resourceId: account.id, status: 'all' }, ownerAccess).some((item) => item.status === 'revoked'), true, '父账户逻辑删除后全部状态仍可追溯已回收授权')
  const visibleAfterDelete = repositories.listAccounts(granteeAccess).find((item) => item.id === authorizedInstance.id)
  assert.equal(visibleAfterDelete, undefined, '父账户逻辑删除后，被授权用户默认账户列表不应继续展示授权实例')
  assert.equal(authorizationInstanceSourceAccountId(authorizedInstance.id), account.id, '逻辑删除阶段不应断开授权实例来源账户引用')
  const dispatchableAfterDelete = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
  assert.equal(dispatchableAfterDelete.some((item) => item.id === authorizedInstance.id), false, '父账户逻辑删除后授权实例不应继续参与调度')
  assert.equal(cleanupTargetExists(account.id), false, '逻辑删除阶段不应登记即时关联清理目标')
  assert.equal(usageRecordExists(ownerUsageId), true, '逻辑删除阶段不应同步删除原账户关联使用记录')
  assert.equal(usageRecordExists(instanceUsageId), true, '逻辑删除阶段不应同步删除授权实例使用记录')
  assert.equal(auditDataCount(account.id), 2, '逻辑删除阶段不应同步删除账户关联原始审计数据')
  assert.equal(auditDataCount(authorizedInstance.id), 2, '逻辑删除阶段不应同步删除授权实例原始审计数据')
  assert.equal(modelCheckRunCount(account.id), 1, '逻辑删除阶段不应同步删除账户关联模型检测记录')
  assert.equal(modelCheckRunCount(authorizedInstance.id), 1, '逻辑删除阶段不应同步删除授权实例模型检测记录')
  assert.equal(accountQualityScoreCount(account.id), 1, '逻辑删除阶段不应同步删除账户质量快照')
  assert.equal(accountQualityScoreCount(authorizedInstance.id), 1, '逻辑删除阶段不应同步删除授权实例质量快照')
  assert.equal(accountUsageSnapshotCount(account.id), 1, '逻辑删除阶段不应同步删除账户外部用量快照')
  assert.equal(accountUsageSnapshotCount(authorizedInstance.id), 1, '逻辑删除阶段不应同步删除授权实例外部用量快照')
  assert.equal(usageStatsTotal(owner.id, 'account', account.id), 1, '逻辑删除阶段原账户自用统计应保留')
  assert.equal(usageStatsTotal(grantee.id, 'account', authorizedInstance.id), 1, '逻辑删除阶段授权实例账户统计应保留')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization', runtimeAuthorizationId), 1, '逻辑删除阶段授权统计应保留')

  ageDeletedAccountsForPhysicalCleanup([account.id, authorizedInstance.id])
  const cleanupResult = repositories.cleanupExpiredLogicallyDeletedAccounts({ limit: 10 })
  assert.equal(cleanupResult.attempted, 1, '过期物理清理应按父账户聚合处理一次')
  assert.equal(cleanupResult.completed, 1, '过期物理清理应完成父账户和授权实例清理')
  assert.equal(cleanupResult.deferred, 0, '统计游标已追平时不应延迟物理清理')
  assert.equal(cleanupResult.failed, 0, '过期物理清理不应失败')
  assert.equal(cleanupResult.physicallyDeletedAccounts, 2, '过期物理清理应删除父账户和被授权实例账户')
  assert.equal(cleanupResult.physicallyDeletedAuthorizations, 1, '过期物理清理应删除账户授权记录')
  assert.equal(cleanupResult.physicallyDeletedGrants, 1, '过期物理清理应删除账户授权 grant')

  assert.equal(rawAccountExists(account.id), false, '过期物理清理后父账户业务行应删除')
  assert.equal(rawAccountExists(authorizedInstance.id), false, '过期物理清理后被授权实例业务行应删除')
  assert.equal(groupAccountCount(account.id), 0, '过期物理清理后父账户分组绑定应删除')
  assert.equal(groupAccountCount(authorizedInstance.id), 0, '过期物理清理后被授权实例分组绑定应删除')
  assert.equal(resourceAuthorizationCount(account.id), 0, '过期物理清理后账户授权记录不应残留')
  assert.equal(resourceAuthorizationGrantCount(account.id), 0, '过期物理清理后账户授权 grant 不应残留')
  assert.equal(usageRecordExists(ownerUsageId), false, '过期物理清理应删除原账户关联使用记录')
  assert.equal(usageRecordExists(instanceUsageId), false, '过期物理清理应删除授权实例使用记录')
  assert.equal(auditDataCount(account.id), 0, '过期物理清理应删除账户关联原始审计数据')
  assert.equal(auditDataCount(authorizedInstance.id), 0, '过期物理清理应删除授权实例原始审计数据')
  assert.equal(modelCheckRunCount(account.id), 0, '过期物理清理应删除账户关联模型检测记录')
  assert.equal(modelCheckRunCount(authorizedInstance.id), 0, '过期物理清理应删除授权实例模型检测记录')
  assert.equal(accountQualityScoreCount(account.id), 0, '过期物理清理应删除账户质量快照')
  assert.equal(accountQualityScoreCount(authorizedInstance.id), 0, '过期物理清理应删除授权实例质量快照')
  assert.equal(accountUsageSnapshotCount(account.id), 0, '过期物理清理应删除账户外部用量快照')
  assert.equal(accountUsageSnapshotCount(authorizedInstance.id), 0, '过期物理清理应删除授权实例外部用量快照')
  assert.equal(usageStatsTotal(owner.id, 'account', account.id), 0, '过期物理清理后原账户自用统计不应残留')
  assert.equal(usageStatsTotal(grantee.id, 'account', authorizedInstance.id), 0, '过期物理清理后授权实例账户统计不应残留')
  assert.equal(usageStatsTotal(grantee.id, 'caller_account', authorizedInstance.id), 0, '过期物理清理后授权实例调用方账户统计不应残留')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization', runtimeAuthorizationId), 0, '过期物理清理后授权统计不应残留')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization_team', `${authorizedInstance.id}:${team.id}`), 0, '过期物理清理后团队授权统计不应残留')
  assert.equal(usageScopeRangeWindowRequestCount(grantee.id, 'account', authorizedInstance.id), 0, '过期物理清理后范围窗口不应残留授权实例账户统计')
  assert.equal(usageQuotaHourlyWindowCost(grantee.id, 'account_authorization', runtimeAuthorizationId), 0, '过期物理清理后额度窗口不应残留授权成本')
  assert.equal(usageRankSnapshotMetric(grantee.id, 'account_authorization', runtimeAuthorizationId), 0, '过期物理清理后授权排行快照不应残留')
  assert.equal(authorizationUserUsageRangeWindowRequestCount(owner.id, account.id), 0, '过期物理清理后授权报表窗口不应残留账户过滤统计')
  assert.equal(cleanupTargetExists(account.id), false, '过期物理清理完成后不应残留账户清理目标')

  const legacyDeletedSource = repositories.createAccount({
    providerCode: 'openai',
    name: '旧版本父账户已删授权实例',
    type: 'api_key',
    credentials: {
      api_key: 'sk-deleted-account-legacy-orphan',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: ownerGroup.id
  }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account',
    resourceId: legacyDeletedSource.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    targetGroupId: granteeGroup.id,
    remark: '旧版本父账户删除遗留回归'
  }, ownerAccess)
  const legacyOrphanInstance = authorizedInstanceForSource(legacyDeletedSource.id, granteeAccess)
  simulateLegacyDetachedAndDeletedSourceAccount(legacyDeletedSource.id, legacyOrphanInstance.id)
  assert.equal(rawAccountExists(legacyDeletedSource.id), false, '旧版本脏数据应模拟来源账户已被物理删除')
  assert.equal(accountExists(legacyOrphanInstance.id), true, '旧版本脏数据中授权实例仍处于未删除状态')
  assert.equal(repositories.listAccounts(granteeAccess).some((item) => item.id === legacyOrphanInstance.id), true, '扫尾前旧授权实例仍会误显示在被授权人账户列表')

  const orphanCleanupResult = repositories.cleanupExpiredLogicallyDeletedAccounts({ limit: 10 })
  assert.equal(orphanCleanupResult.orphanedAuthorizationInstances, 1, '每日清理应先逻辑删除来源已缺失的旧授权实例')
  assert.equal(orphanCleanupResult.attempted, 0, '刚逻辑删除的旧授权实例不应立刻进入一个月物理清理')
  assert.equal(accountExists(legacyOrphanInstance.id), false, '旧授权实例扫尾后不应继续出现在业务读取中')
  assert.equal(rawAccountExists(legacyOrphanInstance.id), true, '旧授权实例扫尾阶段仍应保留业务行等待一个月后物理清理')
  assert.ok(accountDeletedAt(legacyOrphanInstance.id), '旧授权实例扫尾后应写入 deleted_at')
  assert.equal(resourceAuthorizationStatus(legacyDeletedSource.id), 'revoked', '旧授权实例扫尾后运行时授权应标记为已回收')
  assert.equal(resourceAuthorizationGrantStatus(legacyDeletedSource.id), 'revoked', '旧授权实例扫尾后授权 grant 应标记为已回收')
  assert.equal(groupAccountCount(legacyOrphanInstance.id), 1, '旧授权实例扫尾阶段不应同步删除本地分组绑定')

  console.log('已删除 AI 账户关联清理回归通过：删除时逻辑隐藏并保留数据，一个月后物理清理账户、授权、历史和统计')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function accountRuntimeAuthorizationId(accountId: string, granteeSystemAccountId: string): string {
  const row = databaseModule.getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM resource_authorizations
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(accountId, granteeSystemAccountId) as { id?: string } | undefined
  return String(row?.id ?? '')
}

function authorizedInstanceForSource(sourceAccountId: string, access: { systemAccountId: string; role: 'user' }) {
  const account = repositories.listAccounts(access)
    .find((item) => item.authorizationInstanceSourceAccountId === sourceAccountId)
  assert(account, `被授权用户视角应能读取来源账户 ${sourceAccountId} 的授权实例`)
  return account
}

function seedOwnerUsageRecord(id: string, accountId: string, ownerSystemAccountId: string): void {
  const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
  usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare(`
      INSERT INTO usage_records (
        id, system_account_id, trace_id, traffic_source, account_id, endpoint, provider_code, model,
        stream, success, input_tokens, output_tokens, cost_usd,
        account_owner_system_account_id, account_access_type,
        created_at
      ) VALUES (?, ?, ?, 'gateway', ?, '/v1/chat/completions', 'openai', 'gpt-regression', 0, 1, 10, 20, 0.12, ?, 'owner', ?)
    `)
    .run(id, ownerSystemAccountId, `trace_${id}`, accountId, ownerSystemAccountId, createdAt)
  usageRecordShards.recordUsageRecordShardEntries([{
    id,
    shardKey: location.shardKey,
    systemAccountId: ownerSystemAccountId,
    apiKeyId: null,
    accountId,
    groupId: null,
    model: 'gpt-regression',
    trafficSource: 'gateway',
    success: true,
    statusCode: 200,
    clientIp: null,
    firstTokenMs: null,
    durationMs: null,
    costUsd: 0.12,
    createdAt
  }])
}

function seedAuthorizedUsageRecord(id: string, accountId: string, ownerSystemAccountId: string, callerSystemAccountId: string, authorizationId: string, teamId: string): void {
  const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
  usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare(`
      INSERT INTO usage_records (
        id, system_account_id, trace_id, traffic_source, account_id, endpoint, provider_code, model,
        stream, success, input_tokens, output_tokens, cost_usd,
        account_owner_system_account_id, account_access_type,
        account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
        created_at
      ) VALUES (?, ?, ?, 'gateway', ?, '/v1/chat/completions', 'openai', 'gpt-regression', 0, 1, 10, 20, 0.12, ?, 'account_authorized', ?, 'team', ?, ?)
    `)
    .run(id, callerSystemAccountId, `trace_${id}`, accountId, ownerSystemAccountId, authorizationId, teamId, createdAt)
  usageRecordShards.recordUsageRecordShardEntries([{
    id,
    shardKey: location.shardKey,
    systemAccountId: callerSystemAccountId,
    apiKeyId: null,
    accountId,
    groupId: null,
    model: 'gpt-regression',
    trafficSource: 'gateway',
    success: true,
    statusCode: 200,
    clientIp: null,
    firstTokenMs: null,
    durationMs: null,
    costUsd: 0.12,
    createdAt
  }])
}

function seedAuditData(accountId: string, suffix: string): void {
  const datasetDatabase = databaseModule.getDatasetDatabase()
  datasetDatabase
    .prepare(`
      INSERT INTO audit_logs (
        id, trace_id, traffic_source, system_account_id, account_id, method, path, audit_outcome,
        success, sample_bucket, sample_reason, started_at, ended_at, created_at
      ) VALUES (?, ?, 'gateway', 'sys_admin', ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'regression', ?, ?, ?)
    `)
    .run(`audit_deleted_account_related_cleanup_${suffix}`, `trace_audit_deleted_account_related_cleanup_${suffix}`, accountId, createdAt, createdAt, createdAt)
  datasetDatabase
    .prepare(`
      INSERT INTO audit_error_groups (
        id, fingerprint, window_started_at, window_ended_at, system_account_id, account_id,
        count, created_at, updated_at
      ) VALUES (?, ?, ?, ?, 'sys_admin', ?, 1, ?, ?)
    `)
    .run(`audit_group_deleted_account_related_cleanup_${suffix}`, `fp_deleted_account_related_cleanup_${suffix}`, createdAt, createdAt, accountId, createdAt, createdAt)
}

function seedModelCheckRun(accountId: string, ownerSystemAccountId: string, suffix: string): void {
  databaseModule.getDatasetDatabase()
    .prepare(`
      INSERT INTO model_check_runs (
        id, system_account_id, actor_system_account_id, provider_code, target_type, target_id,
        target_owner_system_account_id, account_id, model, started_at, created_at, updated_at
      ) VALUES (?, ?, ?, 'openai', 'account', ?, ?, ?, 'gpt-regression', ?, ?, ?)
    `)
    .run(`model_check_deleted_account_related_cleanup_${suffix}`, ownerSystemAccountId, ownerSystemAccountId, accountId, ownerSystemAccountId, accountId, createdAt, createdAt, createdAt)
}

function seedDetachedAccountStats(accountId: string, ownerSystemAccountId: string): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        window_started_at, window_ended_at, updated_at
      ) VALUES (?, ?, 'openai', 100, 'healthy', 1, 1, 0, 0, ?, ?, ?)
    `)
    .run(accountId, ownerSystemAccountId, createdAt, createdAt, createdAt)
  statsDatabase
    .prepare(`
      INSERT INTO account_usage_snapshots (
        system_account_id, account_id, kind, source, snapshot_json, updated_at, created_at
      ) VALUES (?, ?, 'openai_codex', 'regression', '{}', ?, ?)
    `)
    .run(ownerSystemAccountId, accountId, createdAt, createdAt)
}

function usageStatsTotal(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT request_count
      FROM usage_stats_totals
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageScopeRangeWindowRequestCount(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(request_count), 0) AS request_count
      FROM usage_scope_range_windows
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageQuotaHourlyWindowCost(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(total_cost_usd), 0) AS total_cost_usd
      FROM usage_quota_hourly_windows
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { total_cost_usd?: number } | undefined
  return Number(row?.total_cost_usd ?? 0)
}

function usageRankSnapshotMetric(systemAccountId: string, scopeType: string, scopeId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(metric_value), 0) AS metric_value
      FROM usage_rank_snapshots
      WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?
    `)
    .get(systemAccountId, scopeType, scopeId) as { metric_value?: number } | undefined
  return Number(row?.metric_value ?? 0)
}

function authorizationUserUsageRangeWindowRequestCount(systemAccountId: string, accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare(`
      SELECT COALESCE(MAX(request_count), 0) AS request_count
      FROM authorization_user_usage_range_windows
      WHERE system_account_id = ?
        AND resource_filter_type = 'account'
        AND resource_filter_id = ?
    `)
    .get(systemAccountId, accountId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}

function usageRecordExists(id: string): boolean {
  for (const location of usageRecordShards.listUsageRecordShardLocations()) {
    const row = usageRecordShards.getUsageRecordShardDatabase(location)
      .prepare('SELECT id FROM usage_records WHERE id = ?')
      .get(id) as { id?: string } | undefined
    if (row?.id) return true
  }
  return false
}

function accountExists(accountId: string): boolean {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT id FROM accounts WHERE id = ? AND deleted_at IS NULL')
    .get(accountId) as { id?: string } | undefined
  return Boolean(row?.id)
}

function rawAccountExists(accountId: string): boolean {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT id FROM accounts WHERE id = ?')
    .get(accountId) as { id?: string } | undefined
  return Boolean(row?.id)
}

function accountDeletedAt(accountId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT deleted_at FROM accounts WHERE id = ?')
    .get(accountId) as { deleted_at?: string | null } | undefined
  return row?.deleted_at ?? undefined
}

function ageDeletedAccountsForPhysicalCleanup(accountIds: string[]): void {
  const deletedAt = new Date(Date.now() - 40 * 24 * 60 * 60 * 1000).toISOString()
  const statement = databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL')
  for (const accountId of accountIds) {
    statement.run(deletedAt, deletedAt, accountId)
  }
}

function authorizationInstanceSourceAccountId(accountId: string): string | null | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT authorization_instance_source_account_id FROM accounts WHERE id = ?')
    .get(accountId) as { authorization_instance_source_account_id?: string | null } | undefined
  return row?.authorization_instance_source_account_id
}

function simulateLegacyDetachedAndDeletedSourceAccount(sourceAccountId: string, instanceAccountId: string): void {
  const now = new Date().toISOString()
  const database = databaseModule.getBusinessDatabase()
  database
    .prepare('UPDATE accounts SET authorization_instance_source_account_id = NULL, updated_at = ? WHERE id = ?')
    .run(now, instanceAccountId)
  database
    .prepare('DELETE FROM accounts WHERE id = ?')
    .run(sourceAccountId)
}

function groupAccountCount(accountId: string): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare('SELECT COUNT(*) AS total FROM group_accounts WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function resourceAuthorizationCount(accountId: string): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT COUNT(*) AS total FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ?")
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function resourceAuthorizationGrantCount(accountId: string): number {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT COUNT(*) AS total FROM resource_authorization_grants WHERE resource_type = 'account' AND resource_id = ?")
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function resourceAuthorizationStatus(accountId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT status FROM resource_authorizations WHERE resource_type = 'account' AND resource_id = ? LIMIT 1")
    .get(accountId) as { status?: string } | undefined
  return row?.status
}

function resourceAuthorizationGrantStatus(accountId: string): string | undefined {
  const row = databaseModule.getBusinessDatabase()
    .prepare("SELECT status FROM resource_authorization_grants WHERE resource_type = 'account' AND resource_id = ? LIMIT 1")
    .get(accountId) as { status?: string } | undefined
  return row?.status
}

function auditDataCount(accountId: string): number {
  const database = databaseModule.getDatasetDatabase()
  const logs = database.prepare('SELECT COUNT(*) AS total FROM audit_logs WHERE account_id = ?').get(accountId) as { total?: number } | undefined
  const groups = database.prepare('SELECT COUNT(*) AS total FROM audit_error_groups WHERE account_id = ?').get(accountId) as { total?: number } | undefined
  return Number(logs?.total ?? 0) + Number(groups?.total ?? 0)
}

function modelCheckRunCount(accountId: string): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare("SELECT COUNT(*) AS total FROM model_check_runs WHERE account_id = ? OR (target_type = 'account' AND target_id = ?)")
    .get(accountId, accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function accountQualityScoreCount(accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_quality_scores WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function accountUsageSnapshotCount(accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_usage_snapshots WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function cleanupTargetExists(accountId: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT account_id FROM account_record_cleanup_targets WHERE account_id = ?')
    .get(accountId) as { account_id?: string } | undefined
  return Boolean(row?.account_id)
}
