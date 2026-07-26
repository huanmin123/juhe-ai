import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-model-check-capacity-health-sync-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'model-check-capacity-health-sync-retention-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const repositorySource = readFileSync(new URL('../../storage/model-checks.repository.ts', import.meta.url), 'utf8')
const sqliteTrimSource = repositorySource.slice(
  repositorySource.indexOf('function trimOldestModelCheckRunBatch('),
  repositorySource.indexOf('async function modelCheckRunCountAsync(')
)
const postgresTrimSource = repositorySource.slice(
  repositorySource.indexOf('async function trimOldestModelCheckRunBatchAsync('),
  repositorySource.indexOf('function cursorHasConsumed(')
)

assert.match(
  sqliteTrimSource,
  /SELECT id FROM model_check_runs[\s\S]*?quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied'[\s\S]*?ORDER BY created_at ASC, id ASC[\s\S]*?LIMIT \?/,
  'SQLite 容量裁剪候选只能选择 health-sync 未开始或已应用的 run'
)
assert.match(
  sqliteTrimSource,
  /DELETE FROM model_check_runs[\s\S]*?id IN[\s\S]*?quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied'/,
  'SQLite 容量裁剪最终删除必须重新校验 health-sync 状态'
)
assert.match(
  postgresTrimSource,
  /SELECT id FROM \$\{runs\}[\s\S]*?quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied'[\s\S]*?FOR UPDATE/,
  'PostgreSQL 容量裁剪候选只能选择 health-sync 未开始或已应用的 run，并锁定候选行'
)
assert.match(
  postgresTrimSource,
  /DELETE FROM \$\{runs\}[\s\S]*?id IN[\s\S]*?quality_health_sync_status IS NULL OR quality_health_sync_status = 'applied'/,
  'PostgreSQL 容量裁剪最终删除必须重新校验 health-sync 状态'
)

const [modelChecks, databaseModule] = await Promise.all([
  import('../../storage/model-checks.repository.js'),
  import('../../storage/database.js')
])

try {
  const datasetDatabase = databaseModule.getDatasetDatabase()
  datasetDatabase.exec('PRAGMA ignore_check_constraints = ON')

  const candidateAccountId = 'acct_capacity_health_sync_candidates'
  seedModelCheckRuns(candidateAccountId, [
    { id: '000-protected-failed', healthSyncStatus: 'failed' },
    { id: '001-protected-pending', healthSyncStatus: 'pending_retry' },
    { id: '002-protected-claimed', healthSyncStatus: 'claimed' },
    ...Array.from({ length: 997 }, (_, index) => ({
      id: `100-deletable-${index.toString().padStart(4, '0')}`,
      healthSyncStatus: index % 2 === 0 ? null : 'applied'
    }))
  ])

  createRun(candidateAccountId, 'mcr_capacity_candidate_insert')
  assert.equal(accountRunCount(candidateAccountId), 901, '达到容量后应只裁剪 100 条安全候选，再原子写入新 run')
  assertHealthSyncStatus('000-protected-failed', 'failed')
  assertHealthSyncStatus('001-protected-pending', 'pending_retry')
  assertHealthSyncStatus('002-protected-claimed', 'claimed')
  assert.equal(runExists('100-deletable-0000'), false, '安全候选中的最老 run 应被裁剪')
  assert.equal(runExists('mcr_capacity_candidate_insert'), true, '容量裁剪后应写入本次新 run')

  const finalGuardAccountId = 'acct_capacity_health_sync_final_guard'
  seedModelCheckRuns(finalGuardAccountId, Array.from({ length: 1000 }, (_, index) => ({
    id: `200-race-${index.toString().padStart(4, '0')}`,
    healthSyncStatus: null
  })))

  const originalPrepare = datasetDatabase.prepare.bind(datasetDatabase) as typeof datasetDatabase.prepare
  let reclassifiedRunId: string | undefined
  datasetDatabase.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    if (!reclassifiedRunId && /^\s*DELETE FROM model_check_runs\b/i.test(sql)) {
      const originalRun = statement.run.bind(statement) as typeof statement.run
      statement.run = ((...params: SQLInputValue[]) => {
        reclassifiedRunId = String(params[0])
        originalPrepare(`
          UPDATE model_check_runs
          SET quality_health_sync_status = 'failed'
          WHERE id = ?
        `).run(reclassifiedRunId)
        return originalRun(...params)
      }) as typeof statement.run
    }
    return statement
  }) as typeof datasetDatabase.prepare

  try {
    createRun(finalGuardAccountId, 'mcr_capacity_final_guard_insert')
  } finally {
    datasetDatabase.prepare = originalPrepare
  }

  assert(reclassifiedRunId, '回归应在候选选择后、最终删除前重分类一个 run')
  assertHealthSyncStatus(reclassifiedRunId, 'failed')
  assert.equal(accountRunCount(finalGuardAccountId), 902, '最终 DELETE 二次校验应保留刚进入 failed 的 run')
  assert.equal(runExists('mcr_capacity_final_guard_insert'), true, '二次校验保留竞态 run 后仍应完成新 run 写入')

  console.log('模型检测容量裁剪 health-sync 回归通过：failed、pending_retry、claimed 以及删除前刚变为 failed 的 run 均不会被误删')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedModelCheckRuns(accountId: string, rows: Array<{ id: string; healthSyncStatus: string | null }>): void {
  const database = databaseModule.getDatasetDatabase()
  const insert = database.prepare(`
    INSERT INTO model_check_runs (
      id, system_account_id, actor_system_account_id, provider_code, target_type, target_id,
      account_id, model, status, quality_health_sync_status, started_at, finished_at, created_at, updated_at
    ) VALUES (?, 'sys_admin', 'sys_admin', 'gpt', 'account', ?, ?, 'gpt-5.5', 'completed', ?, ?, ?, ?, ?)
  `)
  database.exec('BEGIN IMMEDIATE')
  try {
    for (const row of rows) {
      const createdAt = '2000-01-01T00:00:00.000Z'
      insert.run(row.id, accountId, accountId, row.healthSyncStatus, createdAt, createdAt, createdAt, createdAt)
    }
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function createRun(accountId: string, id: string): void {
  modelChecks.createModelCheckRun({
    id,
    systemAccountId: 'sys_admin',
    actorSystemAccountId: 'sys_admin',
    providerCode: 'gpt',
    targetType: 'account',
    targetId: accountId,
    targetOwnerSystemAccountId: 'sys_admin',
    accountId,
    model: 'gpt-5.5',
    profile: 'quick',
    trustedComparison: false,
    trustedComparisonAvailable: false,
    probeSetVersion: 'capacity-health-sync-retention-regression'
  })
}

function accountRunCount(accountId: string): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM model_check_runs WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function runExists(runId: string): boolean {
  return Boolean(databaseModule.getDatasetDatabase().prepare('SELECT 1 FROM model_check_runs WHERE id = ?').get(runId))
}

function assertHealthSyncStatus(runId: string, expected: string): void {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT quality_health_sync_status FROM model_check_runs WHERE id = ?')
    .get(runId) as { quality_health_sync_status?: string | null } | undefined
  assert.equal(row?.quality_health_sync_status, expected, `${runId} 的 health-sync 状态应保持为 ${expected}`)
}
