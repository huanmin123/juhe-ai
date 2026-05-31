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

const [databaseModule, repositories, usageStatsRepository, usageRecordShards, usageStatsHelpers, accountCleanupService, recordMaintenanceQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-stats.repository.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../storage/usage-stats-helpers.js'),
  import('../../modules/accounts/account-cleanup.service.js'),
  import('../../modules/record-maintenance/record-maintenance-queue.service.js')
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

  const deleteResult = repositories.deleteAccountWithRelatedCleanup(account.id, ownerAccess)
  assert.equal(deleteResult.deleted, true, 'AI 账户删除应成功')
  assert.equal(deleteResult.cleanupTarget?.authorizationIds?.includes(runtimeAuthorizationId), false, '删除父账户不应把授权实例统计作为父账户清理目标')
  assert.equal(deleteResult.cleanupTarget?.teamScopeIds?.includes(`${authorizedInstance.id}:${team.id}`), false, '删除父账户不应按授权实例 scope 清理团队统计')
  assert.equal(accountExists(account.id), false, '账户主记录应被删除')
  assert.equal(accountExists(authorizedInstance.id), true, '父账户删除后授权实例账户应继续存在')
  assert.equal(groupAccountCount(account.id), 0, '账户绑定关系应随账户删除清理')
  assert.equal(groupAccountCount(authorizedInstance.id), 1, '父账户删除后授权实例分组绑定不应被父账户清理影响')
  assert.equal(resourceAuthorizationCount(account.id), 1, '父账户删除不应回收授权列表中的运行时授权')
  assert.equal(resourceAuthorizationGrantCount(account.id), 1, '父账户删除不应回收授权列表中的授权规则')
  const visibleAfterDelete = repositories.listAccounts(granteeAccess).find((item) => item.id === authorizedInstance.id)
  assert(visibleAfterDelete, '父账户删除后被授权用户打开账户列表不应触发来源回填外键失败')
  assert.equal(visibleAfterDelete.authorizationInstanceSourceAccountId, undefined, '父账户删除后授权实例来源账户应保持为空')
  const dispatchableAfterDelete = repositories.listOpenAIAccountsForGroup(granteeGroup.id, grantee.id)
  assert(dispatchableAfterDelete.some((item) => item.id === authorizedInstance.id), '父账户删除后授权实例仍应可按自身状态参与调度')

  assert.ok(deleteResult.cleanupTarget, '删除成功后应返回后台清理目标')
  accountCleanupService.submitAccountRelatedCleanup(deleteResult.cleanupTarget)
  assert.equal(cleanupTargetExists(account.id), true, '提交清理后应先登记后台清理目标')
  assert.equal(usageRecordExists(ownerUsageId), true, '提交清理时不应同步删除原账户关联使用记录')
  assert.equal(usageRecordExists(instanceUsageId), true, '提交清理时不应同步删除授权实例使用记录')
  assert.equal(auditDataCount(account.id), 2, '提交清理时不应同步删除账户关联原始审计数据')
  assert.equal(auditDataCount(authorizedInstance.id), 2, '提交清理时不应同步删除授权实例原始审计数据')
  assert.equal(modelCheckRunCount(account.id), 1, '提交清理时不应同步删除账户关联模型检测记录')
  assert.equal(modelCheckRunCount(authorizedInstance.id), 1, '提交清理时不应同步删除授权实例模型检测记录')
  assert.equal(accountQualityScoreCount(account.id), 1, '提交清理时不应同步删除账户质量快照')
  assert.equal(accountQualityScoreCount(authorizedInstance.id), 1, '提交清理时不应同步删除授权实例质量快照')
  assert.equal(accountUsageSnapshotCount(account.id), 1, '提交清理时不应同步删除账户外部用量快照')
  assert.equal(accountUsageSnapshotCount(authorizedInstance.id), 1, '提交清理时不应同步删除授权实例外部用量快照')
  recordMaintenanceQueue.flushAllRecordMaintenanceQueue()

  assert.equal(usageRecordExists(ownerUsageId), false, '后台清理应删除原账户关联使用记录')
  assert.equal(usageRecordExists(instanceUsageId), true, '后台清理不应删除授权实例使用记录')
  assert.equal(auditDataCount(account.id), 0, '后台清理应删除账户关联原始审计数据')
  assert.equal(auditDataCount(authorizedInstance.id), 2, '后台清理不应删除授权实例原始审计数据')
  assert.equal(modelCheckRunCount(account.id), 0, '后台清理应删除账户关联模型检测记录')
  assert.equal(modelCheckRunCount(authorizedInstance.id), 1, '后台清理不应删除授权实例模型检测记录')
  assert.equal(accountQualityScoreCount(account.id), 0, '后台清理应删除账户质量快照')
  assert.equal(accountQualityScoreCount(authorizedInstance.id), 1, '后台清理不应删除授权实例质量快照')
  assert.equal(accountUsageSnapshotCount(account.id), 0, '后台清理应删除账户外部用量快照')
  assert.equal(accountUsageSnapshotCount(authorizedInstance.id), 1, '后台清理不应删除授权实例外部用量快照')
  assert.equal(usageStatsTotal(owner.id, 'account', account.id), 0, '后台清理后原账户自用统计不应残留')
  assert.equal(usageStatsTotal(grantee.id, 'account', authorizedInstance.id), 1, '后台清理后授权实例账户统计应保留')
  assert.equal(usageStatsTotal(grantee.id, 'caller_account', authorizedInstance.id), 1, '后台清理后授权实例调用方账户统计应保留')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization', runtimeAuthorizationId), 1, '后台清理后授权统计应保留')
  assert.equal(usageStatsTotal(grantee.id, 'account_authorization_team', `${authorizedInstance.id}:${team.id}`), 1, '后台清理后团队授权统计应保留')
  assert.equal(usageScopeRangeWindowRequestCount(grantee.id, 'account', authorizedInstance.id), 1, '后台清理后范围窗口应保留授权实例账户统计')
  assert.equal(usageQuotaHourlyWindowCost(grantee.id, 'account_authorization', runtimeAuthorizationId), 0.12, '后台清理后额度窗口应保留授权成本')
  assert.equal(usageRankSnapshotMetric(grantee.id, 'account_authorization', runtimeAuthorizationId), 0.12, '后台清理后授权排行快照应保留')
  assert.equal(authorizationUserUsageRangeWindowRequestCount(owner.id, account.id), 0, '后台清理后授权报表窗口不应残留账户过滤统计')
  assert.equal(cleanupTargetExists(account.id), false, '后台清理完成后应移除账户清理目标')

  console.log('已删除 AI 账户关联清理回归通过：父账户记录清理不影响授权实例和授权列表')
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
        id, system_account_id, trace_id, account_id, endpoint, provider_code, model,
        stream, success, input_tokens, output_tokens, cost_usd,
        created_at
      ) VALUES (?, ?, ?, ?, '/v1/chat/completions', 'openai', 'gpt-regression', 0, 1, 10, 20, 0.12, ?)
    `)
    .run(id, ownerSystemAccountId, `trace_${id}`, accountId, createdAt)
}

function seedAuthorizedUsageRecord(id: string, accountId: string, ownerSystemAccountId: string, callerSystemAccountId: string, authorizationId: string, teamId: string): void {
  const location = usageRecordShards.usageRecordShardLocationForRecord(id, createdAt)
  usageRecordShards.getUsageRecordShardDatabase(location)
    .prepare(`
      INSERT INTO usage_records (
        id, system_account_id, trace_id, account_id, endpoint, provider_code, model,
        stream, success, input_tokens, output_tokens, cost_usd,
        account_owner_system_account_id, account_access_type,
        account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
        created_at
      ) VALUES (?, ?, ?, ?, '/v1/chat/completions', 'openai', 'gpt-regression', 0, 1, 10, 20, 0.12, ?, 'account_authorized', ?, 'team', ?, ?)
    `)
    .run(id, callerSystemAccountId, `trace_${id}`, accountId, ownerSystemAccountId, authorizationId, teamId, createdAt)
}

function seedAuditData(accountId: string, suffix: string): void {
  const datasetDatabase = databaseModule.getDatasetDatabase()
  datasetDatabase
    .prepare(`
      INSERT INTO audit_logs (
        id, trace_id, system_account_id, account_id, method, path, audit_outcome,
        success, sample_bucket, sample_reason, started_at, ended_at, created_at
      ) VALUES (?, ?, 'sys_admin', ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'regression', ?, ?, ?)
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
    .prepare('SELECT id FROM accounts WHERE id = ?')
    .get(accountId) as { id?: string } | undefined
  return Boolean(row?.id)
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
