import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-log-batch-existing-lookup-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-log-batch-existing-lookup-secret'
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
  repositories.createAuditLogsBatch([auditLog('audit_existing_lookup_0', 'trace-audit-existing-lookup-0')])

  const database = databaseModule.getDatasetDatabase()
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  let existingLookupSelects = 0
  const blobPrepareCounts = {
    selectExisting: 0,
    updateExisting: 0,
    insertBlob: 0
  }
  const errorGroupPrepareCounts = {
    selectExisting: 0,
    updateExisting: 0,
    insertGroup: 0
  }
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\s+id\s+FROM\s+audit_logs\s+WHERE\s+id\s+IN\s*\(/i.test(sql)) {
      existingLookupSelects += 1
    }
    if (/^\s*SELECT\s+id\s+FROM\s+audit_logs\s+WHERE\s+id\s+=\s+\?/i.test(sql)) {
      throw new Error('批量审计日志写入不应逐条查询 audit_logs 是否存在')
    }
    if (/^\s*SELECT\s+id,\s*storage_key\s+FROM\s+audit_payload_blobs\b/i.test(sql)) {
      blobPrepareCounts.selectExisting += 1
    } else if (/^\s*UPDATE\s+audit_payload_blobs\s+SET\s+ref_count\b/i.test(sql)) {
      blobPrepareCounts.updateExisting += 1
    } else if (/^\s*INSERT\s+INTO\s+audit_payload_blobs\b/i.test(sql)) {
      blobPrepareCounts.insertBlob += 1
    }
    if (/^\s*SELECT\s+id\s+FROM\s+audit_error_groups\s+WHERE\s+fingerprint\s+=\s+\?\s+AND\s+window_started_at\s+=\s+\?/i.test(sql)) {
      errorGroupPrepareCounts.selectExisting += 1
    } else if (/^\s*UPDATE\s+audit_error_groups\s+SET\s+count\s+=\s+count\s+\+\s+1\b/i.test(sql)) {
      errorGroupPrepareCounts.updateExisting += 1
    } else if (/^\s*INSERT\s+INTO\s+audit_error_groups\b/i.test(sql)) {
      errorGroupPrepareCounts.insertGroup += 1
    }
    return originalPrepare(sql)
  }) as typeof database.prepare

  try {
    repositories.createAuditLogsBatch([
      auditLog('audit_existing_lookup_0', 'trace-audit-existing-lookup-duplicate-existing'),
      auditLog('audit_existing_lookup_1', 'trace-audit-existing-lookup-1'),
      auditLog('audit_existing_lookup_1', 'trace-audit-existing-lookup-duplicate-in-batch'),
      auditLog('audit_existing_lookup_2', 'trace-audit-existing-lookup-2'),
      auditLog('audit_existing_lookup_3', 'trace-audit-existing-lookup-3')
    ])
  } finally {
    database.prepare = originalPrepare
  }

  assert.equal(existingLookupSelects, 1, '批量审计日志写入应一次性查询已存在 ID')
  assert.deepEqual(blobPrepareCounts, { selectExisting: 1, updateExisting: 1, insertBlob: 1 }, '批量审计日志写入应复用 payload blob statements')
  assert.deepEqual(errorGroupPrepareCounts, { selectExisting: 1, updateExisting: 1, insertGroup: 1 }, '批量审计日志写入应复用错误分组 statements')
  assert.equal(auditLogCount(), 4, '已存在 ID 和批内重复 ID 应被跳过，其余审计日志应写入')
  assert.equal(auditAttemptCount(), 4, '只应为实际写入的审计日志写入 attempt')
  assert.equal(auditPayloadRefCount(), 4, '只应为实际写入的审计日志写入 payload ref')
  assert.equal(auditErrorGroupCount(), 1, '相同错误指纹应聚合到同一个审计错误分组')
  assert.equal(auditErrorGroupEventCount(), 4, '审计错误分组计数应包含初始记录和批量新增记录')
  const detail = repositories.getAuditLogDetail('audit_existing_lookup_2')
  assert(detail, '批量写入的审计日志详情应可读取')
  assert.equal(detail.payloads.length, 1, '审计日志详情应保留 payload 引用')
  assert.equal(detail.attempts.length, 1, '审计日志详情应保留 attempt')

  console.log('审计日志批量已存在查询回归通过：批量预查 ID，避免逐条查询 audit_logs')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function auditLog(id: string, traceId: string): AuditLogInput {
  const timestamp = '2026-01-03T00:00:00.000Z'
  return {
    id,
    traceId,
    systemAccountId: 'sys_admin',
    providerCode: 'openai',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.1',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'batch_existing_lookup',
    errorMessage: 'audit log batch existing lookup regression',
    sampleBucket: 9999,
    sampleReason: 'full_capture',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    durationMs: 50,
    attempts: [{
      id: `${id}_attempt`,
      tempId: `${id}_attempt_tmp`,
      attemptIndex: 1,
      providerCode: 'openai',
      upstreamMethod: 'POST',
      upstreamUrl: 'https://api.openai.com/v1/responses',
      upstreamStatusCode: 502,
      success: false,
      errorPhase: 'upstream',
      errorCode: 'batch_existing_lookup',
      errorMessage: 'audit log batch existing lookup regression',
      startedAt: timestamp,
      endedAt: timestamp,
      durationMs: 40
    }],
    payloads: [{
      id: `${id}_payload`,
      attemptTempId: `${id}_attempt_tmp`,
      partType: 'gateway_error',
      sequenceIndex: 0,
      contentType: 'application/json',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ id, traceId }),
      createdAt: timestamp
    }],
    createdAt: timestamp
  }
}

function auditLogCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM audit_logs')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function auditAttemptCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM audit_log_attempts')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function auditPayloadRefCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM audit_payload_refs')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function auditErrorGroupCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT COUNT(*) AS total FROM audit_error_groups')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function auditErrorGroupEventCount(): number {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT SUM(count) AS total FROM audit_error_groups')
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
