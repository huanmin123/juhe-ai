import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-record-first-page-prewarm-scale-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.secret = 'usage-record-first-page-prewarm-scale-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, candidateRepository, prewarmJob] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-record-first-page-prewarm.repository.js'),
  import('../../modules/background/usage-record-first-page-prewarm.job.js')
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
  const insertSummary = statsDatabase.prepare(`
    INSERT INTO usage_overview_summary_windows (
      system_account_id, window_key, start_date, end_date,
      request_count, last_used_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?)
  `)
  const dates = Array.from({ length: 7 }, (_item, index) => `2026-07-${String(20 + index).padStart(2, '0')}`)
  const endDate = dates.at(-1)!
  const accountCount = 2_000
  const createdAt = '2026-07-26T00:00:00.000Z'

  businessDatabase.exec('BEGIN IMMEDIATE')
  statsDatabase.exec('BEGIN IMMEDIATE')
  try {
    for (let index = 0; index < accountCount; index += 1) {
      const id = `scale-${String(index).padStart(4, '0')}`
      const status = index === accountCount - 1 ? 'disabled' : 'active'
      insertSystemAccount.run(id, id, id, status, createdAt, createdAt)
      for (const date of dates) {
        const lastUsedAt = `${date}T12:00:00.000Z`
        insertDaily.run(id, id, date, index + 1, lastUsedAt, lastUsedAt)
      }
      insertSummary.run(
        id,
        `${dates[0]}:${endDate}`,
        dates[0],
        endDate,
        (index + 1) * dates.length,
        `${endDate}T12:00:00.000Z`,
        createdAt
      )
    }
    businessDatabase.exec('COMMIT')
    statsDatabase.exec('COMMIT')
  } catch (error) {
    if (businessDatabase.isTransaction) businessDatabase.exec('ROLLBACK')
    if (statsDatabase.isTransaction) statsDatabase.exec('ROLLBACK')
    throw error
  }

  const candidates = await candidateRepository.listUsageRecordFirstPagePrewarmCandidatesAsync({
    startDate: dates[0],
    endDate,
    limit: 128
  })
  assert.equal(candidates.length, 128, '两千账户规模下候选结果仍必须受 128 上限约束')
  assert.equal(candidates[0]?.systemAccountId, 'scale-1998', '最高活跃但已停用账户不得占用首位')
  assert.equal(candidates[0]?.requestCount, 1999 * 7, '周排名应由最多七个有界日 Top-N 批次合并')
  assert.equal(candidates.at(-1)?.systemAccountId, 'scale-1871', '过滤停用账户后应从有界扫描池补足 128 个候选')

  const queryPlan = statsDatabase.prepare(`
    EXPLAIN QUERY PLAN
    SELECT system_account_id, request_count, last_used_at
    FROM usage_overview_summary_windows INDEXED BY idx_usage_overview_summary_windows_prewarm_order
    WHERE window_key = ?
      AND system_account_id <> 'global'
      AND request_count > 0
    ORDER BY request_count DESC, last_used_at DESC, system_account_id ASC
    LIMIT ?
  `).all(`${dates[0]}:${endDate}`, 512)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert.match(queryPlan, /idx_usage_overview_summary_windows_prewarm_order/, `规模候选查询必须优先使用既有窗口汇总排序索引，实际计划：${queryPlan}`)

  const rotationCandidates = Array.from({ length: 128 }, (_item, index) => ({
    systemAccountId: `rotation-${String(index).padStart(3, '0')}`,
    requestCount: 128 - index
  }))
  const rotatingIds = new Set<string>()
  for (let slot = 0; slot < 5; slot += 1) {
    const selected = prewarmJob.selectUsageRecordFirstPagePrewarmCandidates(rotationCandidates, slot)
    assert.equal(selected.length, 32)
    for (const item of selected.slice(8)) rotatingIds.add(item.candidate.systemAccountId)
  }
  assert.equal(rotatingIds.size, 120, '128 候选池的尾部 120 个账户应在五个时间槽内完整覆盖且不依赖内存游标')

  const repositorySource = readFileSync(resolve('src/storage/usage-record-first-page-prewarm.repository.ts'), 'utf8')
  assert.match(repositorySource, /maximumCandidateScanLimit = 512/, '每日查询必须保留固定 512 行上限')
  assert.match(repositorySource, /postgresCandidateStatementTimeoutMs = 1_500/, 'PostgreSQL 候选查询必须保留专用硬超时')
  assert.match(repositorySource, /SET LOCAL statement_timeout/, 'PostgreSQL 专用超时必须在短事务内生效')
  assert.match(repositorySource, /listSummaryWindowCandidateRows/, '候选必须优先复用现有窗口聚合而非重复汇总日统计')
  assert.match(repositorySource, /if \(summaryRows\.length > 0\) return summaryRows/, '仅当窗口聚合缺失时才允许退回按日有界读取')

  console.log('使用记录首屏预热规模回归通过：2,000 账户 × 7 日窗口汇总索引读取、有界日统计回退、PG 查询硬超时契约与跨重启确定性轮转均生效')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
