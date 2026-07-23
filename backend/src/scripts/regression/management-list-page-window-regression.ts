import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import type { AccountUsageStatsRange } from '../../domain/types.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-management-list-page-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'management-list-page-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, clientIpStats, accountUsageRepository, authorizationUsageRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/client-ip-stats.repository.js'),
  import('../../storage/account-usage.repository.js'),
  import('../../storage/authorization-usage.repository.js')
])

const range: AccountUsageStatsRange = {
  startDate: '2026-02-03',
  endDate: '2026-02-03',
  days: 1,
  maxDays: 31
}

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const statsDatabase = databaseModule.getStatsDatabase()

  seedClientIpWindow(statsDatabase, '203.0.113.10', 10)
  seedUsageScopeWindow(statsDatabase, 'sys_admin', 'account', 'account_page_window_0', 30)
  seedAuthorizationTeamWindow(statsDatabase, 'sys_admin', 'team_page_window_0', 20)
  seedAuthorizationUserWindow(statsDatabase, 'sys_admin', 'user_page_window_0', 40)
  seedModelCheckRun()

  const ipList = clientIpStats.listClientIpStats({
    startDate: range.startDate,
    endDate: range.endDate,
    page: 999999,
    pageSize: 100
  })
  assert.equal(ipList.page, 10, 'IP 统计 pageSize=100 时页码应收敛到 10 页以内')
  assert.equal((ipList.page - 1) * ipList.pageSize, 900, 'IP 统计深翻页 offset 应限制在 1000 行内')

  const accountUsage = accountUsageRepository.getAccountUsageStatsOverviewPageFromWindows({
    access,
    range,
    page: 999999,
    pageSize: 200
  })
  assert.equal(accountUsage.page, 5, '账号用量 pageSize=200 时页码应收敛到 5 页以内')
  assert.equal((accountUsage.page - 1) * accountUsage.pageSize, 800, '账号用量深翻页 offset 应限制在 1000 行内')

  const teamUsage = authorizationUsageRepository.getAuthorizationTeamUsageRows({}, access, range, {
    page: 999999,
    pageSize: 200
  })
  assert.equal(teamUsage.page, 5, '团队授权消耗 pageSize=200 时页码应收敛到 5 页以内')
  assert.equal((teamUsage.page - 1) * teamUsage.pageSize, 800, '团队授权消耗深翻页 offset 应限制在 1000 行内')

  const userUsage = authorizationUsageRepository.getAuthorizationUserUsageRows({}, access, range, {
    page: 999999,
    pageSize: 200
  })
  assert.equal(userUsage.page, 5, '用户授权消耗 pageSize=200 时页码应收敛到 5 页以内')
  assert.equal((userUsage.page - 1) * userUsage.pageSize, 800, '用户授权消耗深翻页 offset 应限制在 1000 行内')

  const modelChecks = repositories.listModelCheckRuns(access, { page: 999999, pageSize: 100 })
  assert.equal(modelChecks.page, 10, '模型检测列表 pageSize=100 时页码应收敛到 10 页以内')
  assert.equal((modelChecks.page - 1) * modelChecks.pageSize, 900, '模型检测列表深翻页 offset 应限制在 1000 行内')

  console.log('管理端列表页码窗口回归通过：IP 统计、账号用量、授权用量和模型检测列表 offset 被限制在 1000 行内')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedClientIpWindow(database: ReturnType<typeof databaseModule.getStatsDatabase>, ip: string, requestCount: number): void {
  const ipHash = createHash('sha256').update(ip).digest('hex')
  database.prepare(`
    INSERT INTO client_ip_registry (
      ip_hash, bucket_no, aggregate_ip_key, client_ip, ip_version, first_seen_at, last_seen_at, created_at, updated_at
    ) VALUES (?, 1, ?, ?, 4, ?, ?, ?, ?)
  `).run(ipHash, ip, ip, range.startDate, range.startDate, range.startDate, range.startDate)
  database.prepare(`
    INSERT INTO client_ip_usage_range_windows (
      ip_hash, start_date, end_date, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      active_days, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 1, ?, ?)
  `).run(ipHash, range.startDate, range.endDate, requestCount, requestCount, '2026-02-03T00:00:00.000Z', '2026-02-03T00:00:00.000Z')
}

function seedUsageScopeWindow(
  database: ReturnType<typeof databaseModule.getStatsDatabase>,
  systemAccountId: string,
  scopeType: string,
  scopeId: string,
  requestCount: number
): void {
  database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      total_cost_usd, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, ?, ?, ?)
  `).run(systemAccountId, scopeType, scopeId, range.startDate, range.endDate, requestCount, requestCount * 0.01, '2026-02-03T00:01:00.000Z', '2026-02-03T00:01:00.000Z')
}

function seedAuthorizationTeamWindow(
  database: ReturnType<typeof databaseModule.getStatsDatabase>,
  systemAccountId: string,
  teamId: string,
  requestCount: number
): void {
  database.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, 'account', 'account_page_window_0', ?, 0, 0, 0, 0, ?, ?, ?)
  `).run(systemAccountId, range.startDate, range.endDate, teamId, requestCount, requestCount * 0.01, '2026-02-03T00:02:00.000Z', '2026-02-03T00:02:00.000Z')
}

function seedAuthorizationUserWindow(
  database: ReturnType<typeof databaseModule.getStatsDatabase>,
  systemAccountId: string,
  granteeSystemAccountId: string,
  requestCount: number
): void {
  database.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, total_cost_usd,
      last_used_at, updated_at
    ) VALUES (?, ?, ?, '', ?, 'account', 'account_page_window_0', ?, 0, 0, 0, 0, ?, ?, ?)
  `).run(systemAccountId, range.startDate, range.endDate, granteeSystemAccountId, requestCount, requestCount * 0.01, '2026-02-03T00:03:00.000Z', '2026-02-03T00:03:00.000Z')
}

function seedModelCheckRun(): void {
  repositories.createModelCheckRun({
    id: 'model_check_page_window_0',
    systemAccountId: 'sys_admin',
    actorSystemAccountId: 'sys_admin',
    providerCode: 'gpt',
    targetType: 'account',
    targetId: 'account_page_window_0',
    targetName: '页码窗口账号',
    model: 'gpt-5.5',
    trustedComparison: false,
    probeSetVersion: 'page-window',
    startedAt: '2026-02-03T00:04:00.000Z'
  })
}
