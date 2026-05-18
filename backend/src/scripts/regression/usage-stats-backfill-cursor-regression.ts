import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { cleanupProcessedUsageRecordsBeforeWithResult } from '../../storage/data-retention.repository.js'
import { ensureUsageStatsBackfill } from '../../storage/usage-stats-backfill-runner.js'
import type { UsageStatsRecordRow } from '../../storage/usage-stats-types.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-backfill-cursor-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'usage-stats-backfill-cursor-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')

const statTime = '2000-01-01T00:00:00.000Z'
const jobName = 'caller_account_usage_stats_backfill'

try {
  seedUsageRecord('a')
  seedUsageRecord('b')
  seedUsageRecord('c')
  seedJobState('usage_stats_aggregation', statTime, 'c', statTime)
  seedJobState(jobName, statTime, 'a')
  seedJobState('account_quality_minute_stats_backfill', '', 'processed:0', statTime)

  assert.throws(() => ensureUsageStatsBackfill({
    database: databaseModule.getRecordDatabase(),
    jobName,
    limit: 10,
    sourceCursor: { cursorCreatedAt: statTime, cursorId: 'c' },
    recordFilterSql: 'account_id IS NOT NULL',
    failureMessage: '调用账号统计回填失败',
    aggregateRecord: (_database, row) => {
      if (row.id === 'b') {
        throw new Error('模拟 backfill 批内失败')
      }
    }
  }), /模拟 backfill 批内失败/, 'backfill 批内失败应向上抛出')

  const failedState = readJobState(jobName)
  assert.equal(failedState?.cursor_created_at, statTime, 'backfill 失败不应改写已成功游标时间')
  assert.equal(failedState?.cursor_id, 'a', 'backfill 失败不应把游标污染成 failed 或后续 id')
  assert.equal(failedState?.last_success_at, null, 'backfill 失败应保持未完成')
  assert.match(String(failedState?.last_error_message ?? ''), /模拟 backfill 批内失败/, 'backfill 失败应记录错误信息')

  const cleanup = cleanupProcessedUsageRecordsBeforeWithResult('2000-01-02T00:00:00.000Z', 10)
  assert.equal(cleanup.deletedRows, 1, '失败后的清理安全游标只能删到已成功回填的 a')
  assert.equal(usageRecordExists('a'), false, '已成功回填游标内的记录可清理')
  assert.equal(usageRecordExists('b'), true, '失败批次中的记录不能被伪游标误删')
  assert.equal(usageRecordExists('c'), true, '失败批次后的记录不能被伪游标误删')

  const processedIds: string[] = []
  const rerun = ensureUsageStatsBackfill({
    database: databaseModule.getRecordDatabase(),
    jobName,
    limit: 10,
    sourceCursor: { cursorCreatedAt: statTime, cursorId: 'c' },
    recordFilterSql: 'account_id IS NOT NULL',
    failureMessage: '调用账号统计回填失败',
    aggregateRecord: (_database, row) => {
      processedIds.push(row.id)
    }
  })
  assert.deepEqual(processedIds, ['b', 'c'], '失败后重跑应从上次成功游标之后重新处理未完成记录')
  assert.equal(rerun.complete, true, '重跑处理到 source cursor 后应标记完成')
  assert.equal(rerun.processed, 2, '重跑应处理失败批次遗留记录')

  const completedState = readJobState(jobName)
  assert.equal(completedState?.cursor_created_at, '', '完成态不应残留旧 progress cursor_created_at')
  assert.equal(completedState?.cursor_id, 'processed:2', '完成态应记录本轮处理数量')
  assert.equal(typeof completedState?.last_success_at, 'string', '完成态应写入 last_success_at')
  assert.equal(completedState?.last_error_message, null, '完成态应清空失败信息')

  console.log('用量统计 backfill 游标回归通过：失败不污染游标，清理不会越过已成功回填位置')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageRecord(id: string): void {
  databaseModule.getRecordDatabase()
    .prepare(`
      INSERT INTO usage_records (id, system_account_id, trace_id, account_id, stream, success, created_at)
      VALUES (?, 'sys_admin', ?, 'acct_backfill_cursor', 0, 1, ?)
    `)
    .run(id, `trace_${id}`, statTime)
}

function seedJobState(jobNameInput: string, cursorCreatedAt: string, cursorId: string, lastSuccessAt?: string): void {
  databaseModule.getRecordDatabase()
    .prepare(`
      INSERT INTO stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, updated_at
      ) VALUES ('global', '', ?, ?, ?, ?, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = excluded.cursor_created_at,
        cursor_id = excluded.cursor_id,
        last_success_at = excluded.last_success_at,
        updated_at = excluded.updated_at
    `)
    .run(jobNameInput, cursorCreatedAt, cursorId, lastSuccessAt ?? null, statTime)
}

function readJobState(jobNameInput: string): {
  cursor_created_at: string | null
  cursor_id: string | null
  last_success_at: string | null
  last_error_message: string | null
} | undefined {
  return databaseModule.getRecordDatabase()
    .prepare(`
      SELECT cursor_created_at, cursor_id, last_success_at, last_error_message
      FROM stats_job_state
      WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?
    `)
    .get(jobNameInput) as {
      cursor_created_at: string | null
      cursor_id: string | null
      last_success_at: string | null
      last_error_message: string | null
    } | undefined
}

function usageRecordExists(id: string): boolean {
  const row = databaseModule.getRecordDatabase()
    .prepare('SELECT id FROM usage_records WHERE id = ?')
    .get(id) as Pick<UsageStatsRecordRow, 'id'> | undefined
  return Boolean(row?.id)
}
