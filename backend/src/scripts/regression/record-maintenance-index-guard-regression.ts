import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { SQLInputValue } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-record-maintenance-index-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'record-maintenance-index-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')

try {
  const businessDatabase = databaseModule.getDatabase()
  const recordDatabase = databaseModule.getRecordDatabase()

  assertPlanUsesIndex(
    businessDatabase,
    'system_sessions expires_at 清理',
    'EXPLAIN QUERY PLAN DELETE FROM system_sessions WHERE expires_at < ?',
    ['2026-01-01T00:00:00.000Z'],
    'idx_system_sessions_expires_at'
  )

  assertPlanUsesIndex(
    recordDatabase,
    'operation_log_targets 详情读取',
    'EXPLAIN QUERY PLAN SELECT * FROM operation_log_targets WHERE operation_log_id = ? ORDER BY created_at ASC, id ASC',
    ['op_guard'],
    'idx_operation_log_targets_log_created',
    { rejectTempSort: true }
  )

  assertPlanUsesIndex(
    recordDatabase,
    'audit_error_groups API Key 删除清理',
    'EXPLAIN QUERY PLAN SELECT id FROM audit_error_groups WHERE api_key_id = ? AND system_account_id = ?',
    ['key_guard', 'sys_admin'],
    'idx_audit_error_groups_api_key_account'
  )

  assertPlanUsesIndex(
    recordDatabase,
    'audit_logs API Key 删除批次选择',
    `
      EXPLAIN QUERY PLAN
      SELECT id
      FROM audit_logs
      WHERE api_key_id = ?
        AND system_account_id = ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `,
    ['key_guard', 'sys_admin', 1000],
    'idx_audit_logs_api_key_created',
    { rejectTempSort: true }
  )

  assertPlanUsesIndex(
    recordDatabase,
    'audit_payload_blobs 未引用清理',
    `
      EXPLAIN QUERY PLAN
      SELECT b.id, b.storage_key
      FROM audit_payload_blobs b
      WHERE NOT EXISTS (
        SELECT 1
        FROM audit_payload_refs r
        WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
      )
      ORDER BY b.created_at ASC, b.id ASC
      LIMIT ?
    `,
    [1000],
    'idx_audit_payload_blobs_created',
    { rejectTempSort: true }
  )

  console.log('记录库维护索引回归通过：删除清理、详情读取和过期清理查询均使用目标索引且避免临时排序')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertPlanUsesIndex(
  database: ReturnType<typeof databaseModule.getDatabase>,
  label: string,
  sql: string,
  params: SQLInputValue[],
  indexName: string,
  options: { rejectTempSort?: boolean } = {}
): void {
  const details = database
    .prepare(sql)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes(indexName), `${label} 应使用 ${indexName}，实际计划：${details}`)
  if (options.rejectTempSort) {
    assert(!/USE TEMP B-TREE/i.test(details), `${label} 不应使用临时排序，实际计划：${details}`)
  }
}
