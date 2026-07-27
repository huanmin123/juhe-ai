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
import { buildAuditLogFilters } from '../../storage/audit-log-list-query.js'
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

try {
  const first = auditLog('audit-session-1', 'trace-session-1', '2026-07-27T00:00:00.000Z', {
    conversationKey,
    sessionNamespace: 'openai.codex',
    sessionSource: 'header:session-id',
    sessionResolution: 'official',
    sessionConfidence: 'authoritative',
    threadKey: `hmac-sha256-v1:${'b'.repeat(64)}`,
    turnKey: `hmac-sha256-v1:${'c'.repeat(64)}`,
    agentKey: `hmac-sha256-v1:${'d'.repeat(64)}`,
    parentResponseKey: `hmac-sha256-v1:${'e'.repeat(64)}`,
    identityConflict: false
  })
  const second = auditLog('audit-session-2', 'trace-session-2', '2026-07-27T00:00:01.000Z', {
    conversationKey,
    sessionNamespace: 'openai.codex',
    sessionSource: 'body:client_metadata.x-codex-turn-metadata.session_id',
    sessionResolution: 'official',
    sessionConfidence: 'authoritative',
    identityConflict: true
  })
  const historical = auditLog('audit-session-history', 'trace-session-history', '2026-07-27T00:00:02.000Z')

  repositories.createAuditLogsBatch([first, second, historical])

  const result = repositories.listAuditLogs({
    conversationKey,
    systemAccountId: 'sys-session',
    apiKeyId: 'key-session'
  })
  assert.equal(result.items.length, 2, 'conversationKey 精确筛选应返回同会话的两次请求')
  assert.deepEqual(result.items.map((item) => item.traceId).sort(), ['trace-session-1', 'trace-session-2'])
  assert(result.items.every((item) => item.conversationKey === conversationKey), '列表 DTO 应返回统一 conversationKey')
  assert.equal(result.items.find((item) => item.traceId === 'trace-session-1')?.identityConflict, false, '可空冲突字段必须保留显式 false')
  assert.equal(result.items.find((item) => item.traceId === 'trace-session-2')?.identityConflict, true, '冲突请求必须映射为 true')

  const detail = repositories.getAuditLogDetail('audit-session-1')
  assert(detail, '会话审计详情应可读取')
  assert.equal(detail.sessionNamespace, 'openai.codex')
  assert.equal(detail.threadKey, first.threadKey)
  assert.equal(detail.turnKey, first.turnKey)
  assert.equal(detail.agentKey, first.agentKey)
  assert.equal(detail.parentResponseKey, first.parentResponseKey)

  const historicalDetail = repositories.getAuditLogDetail('audit-session-history')
  assert(historicalDetail, '历史空会话审计应可读取')
  assert.equal(historicalDetail.conversationKey, undefined, '历史记录不应伪造 conversationKey')
  assert.equal(historicalDetail.identityConflict, undefined, '历史记录的可空冲突字段应保持空值')

  const filters = buildAuditLogFilters({ conversationKey })
  assert.match(filters.clause, /al\.conversation_key = \?/, 'conversationKey 必须使用精确等值查询')
  assert.doesNotMatch(filters.clause, /LIKE|>=|</i, 'conversationKey 不得使用前缀或范围查询')
  assert.deepEqual(filters.params, [conversationKey])

  const database = databaseModule.getDatasetDatabase()
  const stored = database.prepare(`
    SELECT conversation_key, session_namespace, session_source, session_resolution, session_confidence,
      thread_key, turn_key, agent_key, parent_response_key, identity_conflict
    FROM audit_logs WHERE id = ?
  `).get(first.id!) as Record<string, unknown>
  assert.equal(stored.conversation_key, conversationKey)
  assert.equal(stored.identity_conflict, 0)

  const indexes = database.prepare("SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'audit_logs'")
    .all() as Array<{ name?: string; sql?: string }>
  assert(indexes.some((index) => index.name === 'idx_audit_logs_system_api_key_conversation_created' && /WHERE conversation_key IS NOT NULL/i.test(index.sql ?? '')), 'SQLite 应创建 scoped conversation partial index')
  assert(indexes.some((index) => index.name === 'idx_audit_logs_system_api_key_thread_created' && /WHERE thread_key IS NOT NULL/i.test(index.sql ?? '')), 'SQLite 应创建 scoped thread partial index')
  const queryPlan = database.prepare(`
    EXPLAIN QUERY PLAN
    SELECT id FROM audit_logs
    WHERE system_account_id = ? AND api_key_id = ? AND conversation_key = ?
    ORDER BY created_at DESC, id DESC LIMIT 101
  `).all('sys-session', 'key-session', conversationKey) as Array<{ detail?: string }>
  assert(queryPlan.some((row) => /idx_audit_logs_system_api_key_conversation_created/i.test(row.detail ?? '')), 'SQLite conversation 查询应命中 scoped 复合索引')
  assert(!queryPlan.some((row) => /USE TEMP B-TREE/i.test(row.detail ?? '')), 'SQLite conversation 查询不应建立临时排序')

  const postgresStatements = collectPostgresSchemaStatements().filter((statement) => statement.schemaName === 'juhe_dataset')
  const postgresAuditTable = postgresStatements.find((statement) => /^CREATE TABLE IF NOT EXISTS audit_logs\b/i.test(statement.sql))?.sql ?? ''
  assert.match(postgresAuditTable, /conversation_key text/i, 'PostgreSQL audit_logs schema 应包含 conversation_key')
  assert.match(postgresAuditTable, /identity_conflict integer/i, 'PostgreSQL audit_logs schema 应包含可空 identity_conflict')
  assert(postgresStatements.some((statement) => /idx_audit_logs_system_api_key_conversation_created/i.test(statement.sql)), 'PostgreSQL schema 应包含 conversation 复合索引')
  assert(postgresStatements.some((statement) => /idx_audit_logs_system_api_key_thread_created/i.test(statement.sql)), 'PostgreSQL schema 应包含 thread 复合索引')
  const postgresIdentityAlterIndexes = postgresStatements
    .map((statement, index) => ({ statement, index }))
    .filter(({ statement }) => statement.source === 'audit-log-session-identity-pg-columns')
  assert.equal(postgresIdentityAlterIndexes.length, 10, 'PostgreSQL 既有 audit_logs 应补齐 10 个会话身份字段')
  const postgresIdentityIndexIndexes = postgresStatements
    .map((statement, index) => ({ statement, index }))
    .filter(({ statement }) => statement.source === 'audit-log-session-identity-pg-indexes')
  assert.equal(postgresIdentityIndexIndexes.length, 2, 'PostgreSQL 应在补列后创建两个会话身份索引')
  assert(!postgresStatements.some((statement) => statement.source === 'dataset' && /idx_audit_logs_system_api_key_(?:conversation|thread)_created/i.test(statement.sql)), 'PostgreSQL 基础 schema 不应在补列前创建会话身份索引')
  assert(
    Math.max(...postgresIdentityAlterIndexes.map(({ index }) => index)) < Math.min(...postgresIdentityIndexIndexes.map(({ index }) => index)),
    'PostgreSQL 会话身份 ALTER 必须先于依赖新列的索引'
  )

  const decoded = decodeAuditLogStreamPayload(encodeAuditLogStreamPayload(first))
  assertSessionIdentity(decoded, first, 'Redis Stream codec')
  assertSessionIdentity(buildAuditLogTransportCapacityFallback(first), first, '容量降级')
  assertSessionIdentity(prepareAuditLogsForBoundedTransport([first])[0]!, first, 'IPC 有界传输')
  assert.equal(isAuditLogInput(first), true, '带会话元数据的审计输入应通过队列校验')
  assert.equal(isAuditLogInput({ ...first, identityConflict: 'false' }), false, '队列应拒绝非法 identityConflict 类型')

  console.log('审计会话串联回归通过：SQLite/PG schema、写读、精确筛选及队列传输均保留统一会话字段')
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
  identity: Partial<Pick<AuditLogInput,
    'conversationKey' | 'sessionNamespace' | 'sessionSource' | 'sessionResolution' | 'sessionConfidence'
    | 'threadKey' | 'turnKey' | 'agentKey' | 'parentResponseKey' | 'identityConflict'
  >> = {}
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
  for (const key of [
    'conversationKey', 'sessionNamespace', 'sessionSource', 'sessionResolution', 'sessionConfidence',
    'threadKey', 'turnKey', 'agentKey', 'parentResponseKey', 'identityConflict'
  ] as const) {
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
    applyDatasetSchema(database)

    const columns = new Set(
      (database.prepare('PRAGMA table_info(audit_logs)').all() as Array<{ name?: string }>)
        .map((column) => column.name)
        .filter((name): name is string => Boolean(name))
    )
    for (const column of [
      'conversation_key', 'session_namespace', 'session_source', 'session_resolution', 'session_confidence',
      'thread_key', 'turn_key', 'agent_key', 'parent_response_key', 'identity_conflict'
    ]) {
      assert(columns.has(column), `SQLite 旧表升级后应包含 ${column}`)
    }
    const indexes = new Set(
      (database.prepare("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'audit_logs'").all() as Array<{ name?: string }>)
        .map((index) => index.name)
        .filter((name): name is string => Boolean(name))
    )
    assert(indexes.has('idx_audit_logs_system_api_key_conversation_created'), 'SQLite 旧表升级后应创建 conversation 索引')
    assert(indexes.has('idx_audit_logs_system_api_key_thread_created'), 'SQLite 旧表升级后应创建 thread 索引')
  } finally {
    database.close()
  }
}
