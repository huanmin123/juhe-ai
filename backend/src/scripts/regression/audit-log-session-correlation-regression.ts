import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { decodeAuditLogStreamPayload, encodeAuditLogStreamPayload } from '../../modules/audit-logs/audit-log-stream-codec.js'
import { buildAuditLogTransportCapacityFallback } from '../../modules/audit-logs/audit-log-capacity-fallback.js'
import { isAuditLogInput } from '../../modules/audit-logs/audit-log-queue.service.js'
import { prepareAuditLogsForBoundedTransport } from '../../modules/background/background-ipc-audit-trim.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { buildAuditLogFilters, normalizeAuditLogPage } from '../../storage/audit-log-list-query.js'
import { collectPostgresSchemaStatements } from '../../storage/postgres-schema.js'
import type { AuditLogInput } from '../../storage/repositories.js'
import { applyDatasetSchema } from '../../storage/schema/dataset-schema.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-session-correlation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-session-correlation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

assertLegacySqliteAuditLogUpgrade()

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const conversationKey = `hmac-sha256-v1:${'a'.repeat(64)}`
const sessionId = '0190fa10-2a8c-7c52-b6a4-4e15e31a58d0'

try {
  const first = auditLog('audit-session-1', 'trace-session-1', '2026-07-27T00:00:00.000Z', {
    conversationKey,
    sessionId,
    sessionClientType: 'codex'
  })
  const second = auditLog('audit-session-2', 'trace-session-2', '2026-07-27T00:00:01.000Z', {
    conversationKey,
    sessionId,
    sessionClientType: 'codex'
  })
  const otherClient = auditLog('audit-session-other-client', 'trace-session-other-client', '2026-07-27T00:00:02.000Z', {
    sessionId,
    sessionClientType: 'claude_code'
  })
  const historical = auditLog('audit-session-history', 'trace-session-history', '2026-07-27T00:00:03.000Z')

  repositories.createAuditLogsBatch([first, second, otherClient, historical])

  const result = repositories.listAuditLogs({
    sessionId,
    sessionClientType: 'codex',
    systemAccountId: 'sys-session',
    apiKeyId: 'key-session'
  })
  assert.equal(result.items.length, 2, '同一客户端 sessionId 的多次请求必须全部返回，不能受唯一约束限制')
  assert.deepEqual(result.items.map((item) => item.traceId).sort(), ['trace-session-1', 'trace-session-2'])
  assert(result.items.every((item) => item.sessionId === sessionId), '列表 DTO 应返回原始 sessionId')
  assert(result.items.every((item) => item.sessionClientType === 'codex'), '列表 DTO 应返回 sessionClientType')
  assert(result.items.every((item) => !('conversationKey' in item)), '列表 DTO 不应暴露内部 conversationKey')

  const sessionOnlyResult = repositories.listAuditLogs({ sessionId })
  assert.equal(sessionOnlyResult.items.length, 3, '仅按 sessionId 搜索时应返回所有匹配客户端记录')
  assert.equal(normalizeAuditLogPage(11, 100), 10, '普通审计宽查询必须保留 1001 行窗口保护')
  assert.equal(normalizeAuditLogPage(11, 100, sessionId), 11, '完整 sessionId 精确查询必须允许继续分页')

  const longSessionId = 'audit-session-with-more-than-one-thousand-requests'
  repositories.createAuditLogsBatch(Array.from({ length: 1005 }, (_, index) => {
    const sequence = String(index).padStart(4, '0')
    return auditLog(
      `audit-long-session-${sequence}`,
      `trace-long-session-${sequence}`,
      `2026-07-28T00:${String(Math.floor(index / 60) % 60).padStart(2, '0')}:${String(index % 60).padStart(2, '0')}.000Z`,
      { sessionId: longSessionId, sessionClientType: 'codex' }
    )
  }))
  const longSessionLastPage = repositories.listAuditLogs({
    sessionId: longSessionId,
    page: 11,
    pageSize: 100
  })
  assert.equal(longSessionLastPage.page, 11, 'sessionId 精确查询不能被普通列表的 1001 行窗口压回旧页码')
  assert.equal(longSessionLastPage.items.length, 5, '超过 1000 次请求的会话必须能继续翻页读取全部历史')
  assert.equal(longSessionLastPage.hasMore, false)
  assert(longSessionLastPage.items.every((item) => item.sessionId === longSessionId))

  const detail = repositories.getAuditLogDetail('audit-session-1')
  assert(detail, '会话审计详情应可读取')
  assert.equal(detail.sessionId, sessionId)
  assert.equal(detail.sessionClientType, 'codex')
  assert.equal(detail.conversationKey, conversationKey)

  const historicalDetail = repositories.getAuditLogDetail('audit-session-history')
  assert(historicalDetail, '历史空会话审计应可读取')
  assert.equal(historicalDetail.sessionId, undefined, '历史记录不应伪造 sessionId')
  assert.equal(historicalDetail.sessionClientType, undefined, '历史记录不应伪造 sessionClientType')

  const filters = buildAuditLogFilters({ sessionId, sessionClientType: 'codex' })
  assert.match(filters.clause, /al\.session_id = \?/, 'sessionId 必须使用精确等值查询')
  assert.match(filters.clause, /al\.session_client_type = \?/, 'sessionClientType 必须使用精确等值查询')
  assert.doesNotMatch(filters.clause, /LIKE|>=|</i, '会话筛选不得使用前缀或范围查询')
  assert.deepEqual(filters.params, [sessionId, 'codex'])

  const database = databaseModule.getDatasetDatabase()
  const storedRows = database.prepare(`
    SELECT conversation_key, session_id, session_client_type
    FROM audit_logs
    WHERE session_id = ? AND session_client_type = ?
    ORDER BY created_at ASC
  `).all(sessionId, 'codex') as Array<Record<string, unknown>>
  assert.equal(storedRows.length, 2, '数据库必须允许同一个 session_id 写入多条审计日志')
  assert.equal(storedRows[0]?.conversation_key, conversationKey)

  const indexes = database.prepare("PRAGMA index_list('audit_logs')")
    .all() as Array<{ name?: string; unique?: number }>
  const sessionIndex = indexes.find((index) => index.name === 'idx_audit_logs_session_created')
  assert(sessionIndex, 'SQLite 应创建全局 session 复合索引')
  assert.equal(sessionIndex.unique, 0, '全局 session 复合索引绝不能是 UNIQUE')
  const sessionIndexSql = database.prepare("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?")
    .get('idx_audit_logs_session_created') as { sql?: string } | undefined
  assert.match(
    sessionIndexSql?.sql ?? '',
    /ON audit_logs\s*\(session_id, created_at, id, session_client_type\)/i,
    'SQLite 全局 session 索引列顺序必须覆盖会话、客户端类型与分页排序'
  )
  assert.doesNotMatch(sessionIndexSql?.sql ?? '', /CREATE\s+UNIQUE\s+INDEX/i, 'SQLite session 索引不得唯一')

  const queryPlan = database.prepare(`
    EXPLAIN QUERY PLAN
    SELECT id FROM audit_logs
    WHERE session_id = ? AND session_client_type = ?
    ORDER BY created_at DESC, id DESC LIMIT 101
  `).all(sessionId, 'codex') as Array<{ detail?: string }>
  assert(queryPlan.some((row) => /idx_audit_logs_session_created/i.test(row.detail ?? '')), 'SQLite 全局 session 查询应命中全局复合索引')
  assert(!queryPlan.some((row) => /USE TEMP B-TREE/i.test(row.detail ?? '')), 'SQLite 全局 session 查询不应建立临时排序')
  const sessionOnlyQueryPlan = database.prepare(`
    EXPLAIN QUERY PLAN
    SELECT id FROM audit_logs
    WHERE session_id = ?
    ORDER BY created_at DESC, id DESC LIMIT 101
  `).all(sessionId) as Array<{ detail?: string }>
  assert(
    sessionOnlyQueryPlan.some((row) => /idx_audit_logs_session_created/i.test(row.detail ?? '')),
    '只传 sessionId 的管理端主搜索也应使用全局 session 复合索引定位记录'
  )
  assert(!sessionOnlyQueryPlan.some((row) => /USE TEMP B-TREE/i.test(row.detail ?? '')), '只传 sessionId 的管理端主搜索不应建立临时排序')
  const postgresStatements = collectPostgresSchemaStatements().filter((statement) => statement.schemaName === 'juhe_dataset')
  const postgresAuditTable = postgresStatements.find((statement) => /^CREATE TABLE IF NOT EXISTS audit_logs\b/i.test(statement.sql))?.sql ?? ''
  assert.match(postgresAuditTable, /conversation_key text/i, 'PostgreSQL audit_logs schema 应保留内部 conversation_key')
  assert.match(postgresAuditTable, /session_id text/i, 'PostgreSQL audit_logs schema 应包含 session_id')
  assert.match(postgresAuditTable, /session_client_type text/i, 'PostgreSQL audit_logs schema 应包含 session_client_type')
  const postgresSessionIndex = postgresStatements.find((statement) => /CREATE INDEX IF NOT EXISTS idx_audit_logs_session_created/i.test(statement.sql))
  assert(postgresSessionIndex, 'PostgreSQL schema 应包含全局 session 复合索引')
  assert.doesNotMatch(postgresSessionIndex.sql, /CREATE\s+UNIQUE\s+INDEX/i, 'PostgreSQL 全局 session 索引不得唯一')
  assert.match(postgresSessionIndex.sql, /\(session_id, created_at, id, session_client_type\)/i)
  assert(postgresStatements.some((statement) => /DROP INDEX IF EXISTS idx_audit_logs_system_api_key_session_created/i.test(statement.sql)), 'PostgreSQL 迁移必须删除旧的 API Key 前导 session 索引')
  const postgresIdentityAlterIndexes = postgresStatements
    .map((statement, index) => ({ statement, index }))
    .filter(({ statement }) => statement.source === 'audit-log-session-identity-pg-columns')
  assert.equal(postgresIdentityAlterIndexes.length, 3, 'PostgreSQL 既有 audit_logs 只补 conversationKey 与两个 session 字段')
  const postgresIdentityCreateIndexIndexes = postgresStatements
    .map((statement, index) => ({ statement, index }))
    .filter(({ statement }) => /CREATE INDEX IF NOT EXISTS idx_audit_logs_(?:system_)?session_created/i.test(statement.sql))
  assert(
    Math.max(...postgresIdentityAlterIndexes.map(({ index }) => index)) < Math.min(...postgresIdentityCreateIndexIndexes.map(({ index }) => index)),
    'PostgreSQL 会话字段 ALTER 必须先于依赖新列的索引'
  )

  const decoded = decodeAuditLogStreamPayload(encodeAuditLogStreamPayload(first))
  assertSessionIdentity(decoded, first, 'Redis Stream codec')
  assertSessionIdentity(buildAuditLogTransportCapacityFallback(first), first, '容量降级')
  assertSessionIdentity(prepareAuditLogsForBoundedTransport([first])[0]!, first, 'IPC 有界传输')
  assert.equal(isAuditLogInput(first), true, '带 session 字段的审计输入应通过队列校验')
  assert.equal(isAuditLogInput({ ...first, auditOutcome: 'removed_outcome' }), false, '队列不得接收已删除的审计终态')
  assert.equal(isAuditLogInput({ ...first, sessionId: 123 }), false, '队列应拒绝非字符串 sessionId')
  assert.equal(isAuditLogInput({ ...first, sessionClientType: false }), false, '队列应拒绝非字符串 sessionClientType')

  console.log('审计 session_id 串联回归通过：SQLite/PG schema、重复写入、精确筛选及队列传输均保留客户端会话字段')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function auditLog(
  id: string,
  traceId: string,
  createdAt: string,
  identity: Partial<Pick<AuditLogInput, 'conversationKey' | 'sessionId' | 'sessionClientType'>> = {}
): AuditLogInput {
  return {
    id,
    traceId,
    ...identity,
    trafficSource: 'gateway',
    systemAccountId: 'sys-session',
    apiKeyId: 'key-session',
    groupId: 'group-session',
    accountId: 'account-session',
    providerCode: 'openai',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.6-sol',
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: 1,
    sampleReason: 'success_hot_full_retention',
    captureStatus: 'complete',
    startedAt: createdAt,
    endedAt: createdAt,
    durationMs: 10,
    attempts: [],
    payloads: [],
    createdAt
  }
}

function assertSessionIdentity(actual: AuditLogInput, expected: AuditLogInput, label: string): void {
  for (const key of ['conversationKey', 'sessionId', 'sessionClientType'] as const) {
    assert.equal(actual[key], expected[key], `${label} 应保留 ${key}`)
  }
}

function assertLegacySqliteAuditLogUpgrade(): void {
  const database = new DatabaseSync(':memory:')
  try {
    database.exec(`
      CREATE TABLE audit_logs (
        id TEXT PRIMARY KEY,
        trace_id TEXT NOT NULL,
        traffic_source TEXT NOT NULL,
        system_account_id TEXT,
        api_key_id TEXT,
        group_id TEXT,
        account_id TEXT,
        client_ip TEXT,
        error_group_id TEXT,
        created_at TEXT NOT NULL
      );
    `)
    applyDatasetSchema(database)
    database.exec(`
      DROP INDEX IF EXISTS idx_audit_logs_session_created;
      CREATE INDEX idx_audit_logs_system_api_key_session_created
        ON audit_logs(system_account_id, api_key_id, session_id, session_client_type, created_at, id)
        WHERE session_id IS NOT NULL;
      CREATE INDEX idx_audit_logs_system_api_key_conversation_created
        ON audit_logs(system_account_id, api_key_id, conversation_key, created_at, id)
        WHERE conversation_key IS NOT NULL;
    `)
    applyDatasetSchema(database)

    const columns = new Set(
      (database.prepare('PRAGMA table_info(audit_logs)').all() as Array<{ name?: string }>)
        .map((column) => column.name)
        .filter((name): name is string => Boolean(name))
    )
    for (const column of ['conversation_key', 'session_id', 'session_client_type']) {
      assert(columns.has(column), `SQLite 旧表升级后应包含 ${column}`)
    }
    const indexes = database.prepare("PRAGMA index_list('audit_logs')")
      .all() as Array<{ name?: string; unique?: number }>
    assert(!indexes.some((index) => index.name === 'idx_audit_logs_system_api_key_conversation_created'), 'SQLite 旧表升级后不应保留内部 conversation 查询索引')
    assert(!indexes.some((index) => index.name === 'idx_audit_logs_system_api_key_session_created'), 'SQLite 旧表升级后不应保留 API Key 前导的旧 session 索引')
    const sessionIndex = indexes.find((index) => index.name === 'idx_audit_logs_session_created')
    assert(sessionIndex, 'SQLite 旧表升级后应创建全局 session 索引')
    assert.equal(sessionIndex.unique, 0, 'SQLite 旧表升级后的全局 session 索引不得唯一')
  } finally {
    database.close()
  }
}
