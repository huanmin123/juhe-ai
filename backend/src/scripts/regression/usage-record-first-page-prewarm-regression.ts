import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-first-page-prewarm-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.secret = 'usage-record-first-page-prewarm-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, candidateRepository, prewarmJob, firstPageCache, postgresSchema] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-record-first-page-prewarm.repository.js'),
  import('../../modules/background/usage-record-first-page-prewarm.job.js'),
  import('../../modules/usage-records/usage-record-first-page-cache.service.js'),
  import('../../storage/postgres-schema.js')
])

try {
  const businessDatabase = databaseModule.getBusinessDatabase()
  const statsDatabase = databaseModule.getStatsDatabase()
  const insertSystemAccount = businessDatabase.prepare(`
    INSERT INTO system_accounts (
      id, username, display_name, role, status, password_hash,
      must_change_password, image_generation_enabled, created_at, updated_at
    ) VALUES (?, ?, ?, 'user', ?, 'hash', 0, 0, ?, ?)
  `)
  const insertDaily = statsDatabase.prepare(`
    INSERT INTO usage_stats_daily (
      system_account_id, scope_type, scope_id, stat_date,
      request_count, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, ?, ?, ?, ?)
  `)
  const activeDate = '2026-07-26'
  const oldDate = '2026-07-10'
  for (let index = 0; index < 140; index += 1) {
    const id = `sys-${String(index).padStart(3, '0')}`
    const updatedAt = new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString()
    const lastUsedAt = new Date(Date.UTC(2026, 6, 26, 0, 0, index)).toISOString()
    insertSystemAccount.run(id, id, id, 'active', updatedAt, updatedAt)
    insertDaily.run(id, id, activeDate, index + 1, lastUsedAt, lastUsedAt)
  }
  insertSystemAccount.run('sys-disabled', 'sys-disabled', 'sys-disabled', 'disabled', activeDate, activeDate)
  insertDaily.run('sys-disabled', 'sys-disabled', activeDate, 1_000_000, `${activeDate}T00:00:00.000Z`, `${activeDate}T00:00:00.000Z`)
  insertSystemAccount.run('sys-old', 'sys-old', 'sys-old', 'active', activeDate, activeDate)
  insertDaily.run('sys-old', 'sys-old', oldDate, 100_000, `${oldDate}T00:00:00.000Z`, `${oldDate}T00:00:00.000Z`)
  insertDaily.run('global', 'global', activeDate, 200_000, `${activeDate}T00:00:00.000Z`, `${activeDate}T00:00:00.000Z`)

  const candidates = await candidateRepository.listUsageRecordFirstPagePrewarmCandidatesAsync({
    startDate: '2026-07-20',
    endDate: '2026-07-26',
    limit: 128
  })
  assert.equal(candidates.length, 128, '候选池应稳定返回最近 7 天活跃前 128 个启用账户')
  assert.equal(candidates[0]?.systemAccountId, 'sys-139', '候选应优先最近 7 天请求数最多的账户')
  assert.equal(candidates[127]?.systemAccountId, 'sys-012', '候选排名不应受 system_accounts.updated_at 顺序和 100 条上限影响')
  assert.equal(candidates.some((candidate) => candidate.systemAccountId === 'global'), false, '候选不得包含全局统计行')
  assert.equal(candidates.some((candidate) => candidate.systemAccountId === 'sys-old'), false, '候选不得包含 7 天窗口外账户')
  const activeOnlyCandidates = await candidateRepository.listUsageRecordFirstPagePrewarmCandidatesAsync({
    startDate: '2026-07-20',
    endDate: '2026-07-26',
    limit: 128
  })
  assert.equal(activeOnlyCandidates.length, 128, '高活跃已停用账户不应占用 128 个有效候选名额')
  assert.equal(activeOnlyCandidates.some((candidate) => candidate.systemAccountId === 'sys-disabled'), false, '候选必须批量过滤已停用系统账户')

  const queryPlan = statsDatabase.prepare(`
    EXPLAIN QUERY PLAN
    SELECT system_account_id, request_count, last_used_at
    FROM usage_stats_daily INDEXED BY idx_usage_stats_daily_system_account_top_activity
    WHERE stat_date = ?
      AND scope_type = 'system_account'
      AND scope_id = system_account_id
      AND system_account_id <> 'global'
      AND request_count > 0
    ORDER BY request_count DESC, last_used_at DESC, system_account_id ASC
    LIMIT ?
  `).all('2026-07-26', 512)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert.match(queryPlan, /idx_usage_stats_daily_system_account_top_activity/, `候选查询应命中系统账户日 Top-N 局部索引，实际计划：${queryPlan}`)
  const postgresActivityIndex = postgresSchema.collectPostgresSchemaStatements().find((statement) => (
    statement.schemaName === 'juhe_stats'
      && statement.sql.includes('idx_usage_stats_daily_system_account_top_activity')
  ))
  assert(postgresActivityIndex, 'PostgreSQL schema 必须同步生成系统账户活跃局部索引')
  assert.match(postgresActivityIndex.sql, /WHERE scope_type = 'system_account'/, 'PostgreSQL 活跃索引必须保留局部谓词')
  const postgresSummaryIndex = postgresSchema.collectPostgresSchemaStatements().find((statement) => (
    statement.schemaName === 'juhe_stats'
      && statement.sql.includes('idx_usage_overview_summary_windows_prewarm_order')
  ))
  assert(postgresSummaryIndex, 'PostgreSQL schema 必须同步生成窗口候选排序索引')
  assert.match(postgresSummaryIndex.sql, /request_count DESC/, '窗口候选索引必须直接支持请求数降序 LIMIT 查询')

  await firstPageCache.seedUsageRecordFirstPageForDate('sys-empty-cache', activeDate, [])
  assert.equal(await firstPageCache.hasUsageRecordFirstPageForDate('sys-empty-cache', activeDate), true, '空页缓存也应被视为已预热，避免反复回源')

  const jobCandidates = Array.from({ length: 40 }, (_, index) => ({
    systemAccountId: `job-${String(index).padStart(2, '0')}`,
    requestCount: 1000 - index,
    lastUsedAt: `${activeDate}T00:00:${String(index).padStart(2, '0')}.000Z`
  }))
  const listedAccounts: string[] = []
  const seededAccounts: string[] = []
  const firstRun = await prewarmJob.runUsageRecordFirstPagePrewarmJob({
    listCandidates: async () => jobCandidates,
    usageStatsTimezone: async () => 'UTC',
    hasCachedPage: async (systemAccountId) => {
      if (systemAccountId === 'job-00') return true
      if (systemAccountId === 'job-01') throw new Error('single account cache failure')
      return false
    },
    listUsageRecords: async (access, options) => {
      listedAccounts.push(access.systemAccountId)
      assert.equal(options.trafficSource, 'gateway')
      assert.equal(options.pageSize, 20)
      return { items: [], total: 0, page: 1, pageSize: 20, hasMore: false }
    },
    seedPage: async (systemAccountId) => { seededAccounts.push(systemAccountId) },
    nowMs: () => 0
  })
  assert.equal(firstRun.outcome, 'partial', '单账户失败应标记部分完成')
  assert.equal(firstRun.selectedCount, 32)
  assert.equal(firstRun.processedCount, 32, '单账户失败不得终止后续候选')
  assert.equal(firstRun.cacheHitCount, 1)
  assert.equal(firstRun.failedCount, 1)
  assert.equal(listedAccounts.includes('job-00'), false, '缓存命中账户不得查询使用记录')
  assert.equal(listedAccounts.includes('job-02'), true, '单账户失败后应继续查询后续账户')
  assert.equal(seededAccounts.length, 30)
  assert.equal(firstRun.nextRotatingCursorId, 'job-31', '结果应报告本轮已处理的最后尾部账户')

  const sameSlotAfterRestart = prewarmJob.selectUsageRecordFirstPagePrewarmCandidates(jobCandidates, 0)
  assert.deepEqual(sameSlotAfterRestart.map((item) => item.candidate.systemAccountId), [
    ...jobCandidates.slice(0, 8),
    ...jobCandidates.slice(8, 32)
  ].map((item) => item.systemAccountId), '同一时间槽在进程重启后仍应选择相同候选')
  const secondSelection = prewarmJob.selectUsageRecordFirstPagePrewarmCandidates(jobCandidates, 1)
  assert.deepEqual(secondSelection.slice(0, 8).map((item) => item.candidate.systemAccountId), jobCandidates.slice(0, 8).map((item) => item.systemAccountId), '热门前 8 每轮都应保留')
  assert.deepEqual(secondSelection.slice(8).map((item) => item.candidate.systemAccountId), [
    ...jobCandidates.slice(32, 40),
    ...jobCandidates.slice(8, 24)
  ].map((item) => item.systemAccountId), '尾部 24 应从上轮游标后环形继续')

  let fakeNowMs = 0
  const budgetRun = await prewarmJob.runUsageRecordFirstPagePrewarmJob({
    listCandidates: async () => jobCandidates,
    usageStatsTimezone: async () => 'UTC',
    hasCachedPage: async () => {
      fakeNowMs += 1000
      return true
    },
    listUsageRecords: async () => { throw new Error('缓存命中时不应查库') },
    seedPage: async () => { throw new Error('缓存命中时不应回填') },
    nowMs: () => fakeNowMs
  })
  assert.equal(budgetRun.outcome, 'partial')
  assert.equal(budgetRun.budgetExhausted, true, '到达 10 秒软预算后应停止启动新账户')
  assert.equal(budgetRun.processedCount, 10)
  assert.equal(budgetRun.cacheHitCount, 10)

  console.log('使用记录首屏预热回归通过：7 日日 Top-N 候选、128 池、确定性 8+24 轮转、缓存命中短路、单账户隔离与 10 秒启动预算均生效')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
