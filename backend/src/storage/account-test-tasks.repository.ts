import type {
  AccountAvailabilitySchedule,
  AccountClientCompatibility,
  AccountSummary,
  AccountSupportedEndpointMode,
  AccountTestResult,
  AccountTestSession,
  AccountTestSessionDetail,
  AccountTestSessionStatus,
  AccountTestTask,
  AccountTestTaskStatus,
  SystemAccountRole
} from '../domain/types.js'
import { ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES } from '../domain/account-health-check-endpoint-mode.js'
import { runtimeConfig } from '../config/runtime.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { decryptJson, encryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import type { AccessScope } from './access-scope.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'

export type AccountTestTaskDiagnostics = 'full' | 'limited'

export interface AccountTestTaskRecord extends AccountTestTask {
  requestSystemAccountId: string
  requestRole: SystemAccountRole
  requestSystemAccountFilterId?: string
  diagnostics: AccountTestTaskDiagnostics
  draftAccount?: AccountTestDraftSnapshot
  errorMessage?: string
}

export interface AccountTestDraftSnapshot {
  id: string
  stateTargetAccountId?: string
  ownerSystemAccountId: string
  groupId: string
  groupName?: string
  providerCode: AccountSummary['providerCode']
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  name: string
  type: AccountSummary['type']
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels?: string[]
  healthCheckModel: string
  healthCheckEndpointMode: import('../domain/types.js').AccountHealthCheckEndpointMode
  modelMappings?: AccountSummary['modelMappings']
  proxyProfileId?: string
  accountExpiresAt?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  availabilityScheduleJson?: string
  notes?: string
}

interface AccountTestTaskRow {
  id: string
  session_id: string | null
  account_id: string
  account_name: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  account_type: string
  request_system_account_id: string
  request_role: string
  request_system_account_filter_id: string | null
  diagnostics: string
  model: string | null
  test_endpoint_mode: string | null
  draft_account_encrypted: string | null
  status: string
  status_message: string | null
  result_json: string | null
  error_message: string | null
  cancel_requested: number | boolean
  queued_at: string | Date
  queued_deadline_at: string | Date | null
  started_at: string | Date | null
  finished_at: string | Date | null
  created_at: string | Date
  updated_at: string | Date
}

interface AccountTestSessionRow {
  id: string
  request_system_account_id: string
  request_role: string
  request_system_account_filter_id: string | null
  status: string
  cancel_reason: string | null
  last_heartbeat_at: string | Date
  cancel_requested_at: string | Date | null
  finished_at: string | Date | null
  created_at: string | Date
  updated_at: string | Date
}

export interface CreateAccountTestTaskInput {
  account: AccountSummary
  access: AccessScope
  diagnostics: AccountTestTaskDiagnostics
  sessionId?: string
  model?: string
  testEndpointMode?: AccountSupportedEndpointMode
  draftAccount?: AccountTestDraftSnapshot
}

const accountTestTaskRetentionHours = 24
const accountTestSessionIdleCompleteMs = 15_000
const accountTestCleanupBatchSize = 200

export function createAccountTestSession(access: AccessScope): AccountTestSession {
  if (!access) {
    throw new Error('缺少系统账户上下文')
  }
  const now = nowIso()
  const id = newId('acctsess')
  getBusinessDatabase().prepare(`
    INSERT INTO account_test_sessions (
      id, request_system_account_id, request_role, request_system_account_filter_id,
      status, last_heartbeat_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, 'running', ?, ?, ?)
  `).run(
    id,
    access.systemAccountId,
    access.role,
    access.systemAccountFilterId ?? null,
    now,
    now,
    now
  )
  const session = getAccountTestSession(id, access)
  if (!session) {
    throw new Error('账户测试会话创建失败')
  }
  return session
}

export function getAccountTestSession(id: string, access?: AccessScope): AccountTestSession | undefined {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  return accountTestSessionFromRow(row)
}

export function getAccountTestSessionDetail(id: string, access?: AccessScope): AccountTestSessionDetail | undefined {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  return accountTestSessionDetailFromRow(row, access)
}

export function heartbeatAccountTestSession(id: string, access?: AccessScope): AccountTestSession | undefined {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  if (row.status !== 'running') {
    return accountTestSessionFromRow(row)
  }
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_sessions
    SET last_heartbeat_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `).run(now, now, row.id)
  return getAccountTestSession(row.id, access)
}

export function completeAccountTestSession(id: string, access?: AccessScope): AccountTestSession | undefined {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  completeAccountTestSessionIfSettled(row.id)
  return getAccountTestSession(row.id, access)
}

export function cancelAccountTestSession(id: string, access?: AccessScope, message = '已停止测试'): { session: AccountTestSession; taskIds: string[] } | undefined {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  const taskIds = cancelAccountTestSessionByRow(row, message, 'canceled')
  const session = getAccountTestSession(row.id, access)
  return session ? { session, taskIds } : undefined
}

export function completeIdleAccountTestSessions(limit = 200, access?: AccessScope): number {
  const cutoff = new Date(Date.now() - accountTestSessionIdleCompleteMs).toISOString()
  const scopeClause = access ? 'AND request_system_account_id = ?' : ''
  const rows = getBusinessDatabase().prepare(`
    SELECT *
    FROM account_test_sessions
    WHERE status = 'running'
      AND last_heartbeat_at < ?
      ${scopeClause}
      AND NOT EXISTS (
        SELECT 1
        FROM account_test_session_tasks st
        JOIN account_test_tasks t ON t.id = st.task_id
        WHERE st.session_id = account_test_sessions.id
          AND t.status IN ('queued', 'running')
      )
    ORDER BY last_heartbeat_at ASC, id ASC
    LIMIT ?
  `).all(...(access ? [cutoff, access.systemAccountId, Math.max(1, Math.trunc(limit))] : [cutoff, Math.max(1, Math.trunc(limit))])) as unknown as AccountTestSessionRow[]
  let completed = 0
  for (const row of rows) {
    completed += completeAccountTestSessionIfSettled(row.id) ? 1 : 0
  }
  return completed
}

export function createAccountTestTask(input: CreateAccountTestTaskInput): AccountTestTask {
  cleanupExpiredAccountTestTasks()

  const now = nowIso()
  const queuedDeadlineAt = accountTestTaskQueuedDeadlineAt(now)
  const id = newId('accttest')
  const sessionId = normalizedOptionalText(input.sessionId)
  if (sessionId) {
    assertUsableAccountTestSession(sessionId, input.access)
  }
  getBusinessDatabase().prepare(`
    INSERT INTO account_test_tasks (
      id, account_id, account_name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, account_type,
      request_system_account_id, request_role, request_system_account_filter_id,
      diagnostics, model, test_endpoint_mode, draft_account_encrypted, status, status_message,
      cancel_requested, queued_at, queued_deadline_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', '等待后台测试', 0, ?, ?, ?, ?)
  `).run(
    id,
    input.account.id,
    input.account.name,
    input.account.providerCode,
    input.account.providerProtocolProfileId ?? '',
    input.account.protocolCode ?? '',
    input.account.protocolVersion ?? '',
    input.account.type,
    input.access.systemAccountId,
    input.access.role,
    input.access.systemAccountFilterId ?? null,
    input.diagnostics,
    normalizedOptionalText(input.model) ?? null,
    accountTestEndpointMode(input.testEndpointMode) ?? null,
    encryptedDraftAccount(input.draftAccount),
    now,
    queuedDeadlineAt,
    now,
    now
  )
  if (sessionId) {
    getBusinessDatabase().prepare(`
      INSERT INTO account_test_session_tasks (session_id, task_id, created_at)
      VALUES (?, ?, ?)
    `).run(sessionId, id, now)
  }

  const task = getAccountTestTask(id)
  if (!task) {
    throw new Error('账户测试任务创建失败')
  }
  return task
}

export function getAccountTestTask(id: string, access?: AccessScope): AccountTestTask | undefined {
  const row = getAccountTestTaskRow(id)
  if (!row || !canReadAccountTestTask(row, access)) {
    return undefined
  }
  return accountTestTaskFromRow(row)
}

export function listAccountTestTasks(ids: string[], access?: AccessScope): AccountTestTask[] {
  const normalizedIds = [...new Set(ids.map(normalizedOptionalText).filter((id): id is string => Boolean(id)))].slice(0, 200)
  if (normalizedIds.length === 0) {
    return []
  }
  const placeholders = normalizedIds.map(() => '?').join(', ')
  const rows = getBusinessDatabase().prepare(`
    SELECT t.*, st.session_id
    FROM account_test_tasks t
    LEFT JOIN account_test_session_tasks st ON st.task_id = t.id
    WHERE t.id IN (${placeholders})
  `).all(...normalizedIds) as unknown as AccountTestTaskRow[]
  const tasksById = new Map(rows
    .filter((row) => canReadAccountTestTask(row, access))
    .map((row) => [row.id, accountTestTaskFromRow(row)]))
  return normalizedIds.map((id) => tasksById.get(id)).filter((task): task is AccountTestTask => Boolean(task))
}

export function getAccountTestTaskRecord(id: string): AccountTestTaskRecord | undefined {
  const row = getAccountTestTaskRow(id)
  return row ? accountTestTaskRecordFromRow(row) : undefined
}

export function listRunnableAccountTestTaskIds(limit = 100): string[] {
  completeIdleAccountTestSessions()
  const rows = getBusinessDatabase().prepare(`
    SELECT t.id
    FROM account_test_tasks t
    LEFT JOIN account_test_session_tasks st ON st.task_id = t.id
    LEFT JOIN account_test_sessions s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?
  `).all(Math.max(1, Math.trunc(limit))) as unknown as Array<{ id: string }>
  return rows.map((row) => row.id)
}

export function failExpiredQueuedAccountTestTasks(maxQueuedMs: number, limit = 200): string[] {
  completeIdleAccountTestSessions()
  const safeMaxQueuedMs = Math.max(1, Math.trunc(maxQueuedMs))
  const safeLimit = Math.max(1, Math.trunc(limit))
  const queuedCutoff = new Date(Date.now() - safeMaxQueuedMs).toISOString()
  const rows = getBusinessDatabase().prepare(`
    SELECT t.id
    FROM account_test_tasks t
    LEFT JOIN account_test_session_tasks st ON st.task_id = t.id
    LEFT JOIN account_test_sessions s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND t.cancel_requested = 0
      AND (
        (t.queued_deadline_at IS NOT NULL AND t.queued_deadline_at <= ?)
        OR (t.queued_deadline_at IS NULL AND t.queued_at < ?)
      )
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?
  `).all(nowIso(), queuedCutoff, safeLimit) as unknown as Array<{ id: string }>
  const taskIds = rows.map((row) => row.id)
  if (taskIds.length === 0) {
    return []
  }

  const placeholders = taskIds.map(() => '?').join(', ')
  const now = nowIso()
  const message = accountTestQueuedWaitExpiredMessage(safeMaxQueuedMs)
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'failed',
        status_message = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id IN (${placeholders})
      AND status = 'queued'
      AND cancel_requested = 0
  `).run(message, message, now, now, ...taskIds)
  return taskIds
}

export function requeueInterruptedAccountTestTasks(): string[] {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'canceled',
        status_message = '已停止测试',
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = 1
  `).run(now, now)
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'queued',
        status_message = '后台 worker 重启后重新排队',
        started_at = NULL,
        cancel_requested = 0,
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = 0
  `).run(now)
  cleanupExpiredAccountTestTasks()
  return listRunnableAccountTestTaskIds()
}

export function markAccountTestTaskRunning(id: string): AccountTestTaskRecord | undefined {
  const cancelReason = accountTestTaskSessionCancelReason(id)
  if (cancelReason) {
    markAccountTestTaskCanceled(id, cancelReason)
    return undefined
  }
  const now = nowIso()
  const result = getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'running',
        status_message = '后台测试中',
        started_at = COALESCE(started_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'queued'
      AND cancel_requested = 0
  `).run(now, now, id)
  if (Number(result.changes ?? 0) === 0) {
    const current = getAccountTestTaskRecord(id)
    if (current?.status === 'queued' && current.cancelRequested) {
      markAccountTestTaskCanceled(id, '已停止测试')
    }
    return undefined
  }
  return getAccountTestTaskRecord(id)
}

export function updateAccountTestTaskMessage(id: string, message: string): AccountTestTaskRecord | undefined {
  const normalizedMessage = normalizedOptionalText(message)
  if (!normalizedMessage) return getAccountTestTaskRecord(id)
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status_message = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = 0
  `).run(normalizedMessage, now, id)
  return getAccountTestTaskRecord(id)
}

export function completeAccountTestTask(id: string, result: AccountTestResult): AccountTestTaskRecord | undefined {
  const now = nowIso()
  const status: AccountTestTaskStatus = result.success ? 'success' : 'failed'
  const write = getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = ?,
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = 0
  `).run(
    status,
    result.message,
    JSON.stringify(result),
    result.success ? null : result.message,
    now,
    now,
    id
  )
  if (Number(write.changes ?? 0) === 0) {
    return finalizeAccountTestTaskIfCanceled(id)
  }
  return getAccountTestTaskRecord(id)
}

export function failAccountTestTask(id: string, message: string, result?: AccountTestResult): AccountTestTaskRecord | undefined {
  const now = nowIso()
  const write = getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'failed',
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
      AND cancel_requested = 0
  `).run(message, result ? JSON.stringify(result) : null, message, now, now, id)
  if (Number(write.changes ?? 0) === 0) {
    return finalizeAccountTestTaskIfCanceled(id)
  }
  return getAccountTestTaskRecord(id)
}

export function cancelAccountTestTask(id: string, access?: AccessScope): AccountTestTask | undefined {
  const row = getAccountTestTaskRow(id)
  if (!row || !canReadAccountTestTask(row, access)) {
    return undefined
  }
  if (row.status === 'queued') {
    markAccountTestTaskCanceled(id, '已停止测试')
  } else if (row.status === 'running') {
    const now = nowIso()
    getBusinessDatabase().prepare(`
      UPDATE account_test_tasks
      SET cancel_requested = 1,
          status_message = '正在停止测试',
          updated_at = ?
      WHERE id = ?
        AND status = 'running'
    `).run(now, id)
  }
  return getAccountTestTask(id, access)
}

export function markAccountTestTaskCanceled(id: string, message: string): AccountTestTaskRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'canceled',
        status_message = CASE
          WHEN cancel_requested = 1 AND status_message IS NOT NULL AND TRIM(status_message) != '' THEN status_message
          ELSE ?
        END,
        cancel_requested = 1,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
  `).run(message, now, now, id)
  return getAccountTestTaskRecord(id)
}

function finalizeAccountTestTaskIfCanceled(id: string): AccountTestTaskRecord | undefined {
  const current = getAccountTestTaskRecord(id)
  if (!current) return undefined
  if (current.cancelRequested && (current.status === 'queued' || current.status === 'running')) {
    return markAccountTestTaskCanceled(id, current.message ?? '已停止测试')
  }
  return current
}

export function isAccountTestTaskCancelRequested(id: string): boolean {
  const row = getBusinessDatabase().prepare(`
    SELECT t.cancel_requested
    FROM account_test_tasks t
    WHERE t.id = ?
    LIMIT 1
  `).get(id) as unknown as { cancel_requested?: number } | undefined
  return Number(row?.cancel_requested ?? 0) === 1 || Boolean(accountTestTaskSessionCancelReason(id))
}

export function accountTestTaskCancelMessage(id: string): string {
  const sessionReason = accountTestTaskSessionCancelReason(id)
  if (sessionReason) return sessionReason
  const row = getBusinessDatabase().prepare(`
    SELECT status_message, cancel_requested
    FROM account_test_tasks
    WHERE id = ?
    LIMIT 1
  `).get(id) as unknown as { status_message?: string | null; cancel_requested?: number } | undefined
  if (Number(row?.cancel_requested ?? 0) === 1) {
    return normalizedOptionalText(row?.status_message) ?? '已停止测试'
  }
  return '已停止测试'
}

export function cleanupExpiredAccountTestTasks(): void {
  const cutoff = new Date(Date.now() - accountTestTaskRetentionHours * 60 * 60 * 1000).toISOString()
  getBusinessDatabase().prepare(`
    DELETE FROM account_test_tasks
    WHERE id IN (
      SELECT id
      FROM account_test_tasks
      WHERE finished_at IS NOT NULL
        AND finished_at < ?
      ORDER BY finished_at ASC, id ASC
      LIMIT ?
    )
  `).run(cutoff, accountTestCleanupBatchSize)
  getBusinessDatabase().prepare(`
    DELETE FROM account_test_sessions
    WHERE id IN (
      SELECT s.id
      FROM account_test_sessions s
      WHERE s.updated_at < ?
        AND NOT EXISTS (
          SELECT 1
          FROM account_test_session_tasks st
          JOIN account_test_tasks t ON t.id = st.task_id
          WHERE st.session_id = s.id
            AND t.status IN ('queued', 'running')
        )
      ORDER BY s.updated_at ASC, s.id ASC
      LIMIT ?
    )
  `).run(cutoff, accountTestCleanupBatchSize)
}

export async function createAccountTestSessionAsync(access: AccessScope): Promise<AccountTestSession> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createAccountTestSession(access)
  }
  if (!access) {
    throw new Error('缺少系统账户上下文')
  }
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  const id = newId('acctsess')
  await client.execute(`
    INSERT INTO ${accountTestTable(client, 'account_test_sessions')} (
      id, request_system_account_id, request_role, request_system_account_filter_id,
      status, last_heartbeat_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, 'running', ?, ?, ?)
  `, [
    id,
    access.systemAccountId,
    access.role,
    access.systemAccountFilterId ?? null,
    now,
    now,
    now
  ])
  const session = await getAccountTestSessionAsync(id, access)
  if (!session) {
    throw new Error('账户测试会话创建失败')
  }
  return session
}

export async function getAccountTestSessionAsync(id: string, access?: AccessScope): Promise<AccountTestSession | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_account_test_session_read_only',
      id,
      access
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountTestSession(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  return accountTestSessionFromRow(row)
}

export async function getAccountTestSessionDetailAsync(id: string, access?: AccessScope): Promise<AccountTestSessionDetail | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountTestSessionDetail(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  return accountTestSessionDetailFromRowAsync(client, row, access)
}

export async function heartbeatAccountTestSessionAsync(id: string, access?: AccessScope): Promise<AccountTestSession | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return heartbeatAccountTestSession(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  if (row.status !== 'running') {
    return accountTestSessionFromRow(row)
  }
  const now = nowIso()
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_sessions')}
    SET last_heartbeat_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `, [now, now, row.id])
  return getAccountTestSessionAsync(row.id, access)
}

export async function completeAccountTestSessionAsync(id: string, access?: AccessScope): Promise<AccountTestSession | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return completeAccountTestSession(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  await completeAccountTestSessionIfSettledAsync(client, row.id)
  return getAccountTestSessionAsync(row.id, access)
}

export async function cancelAccountTestSessionAsync(id: string, access?: AccessScope, message = '已停止测试'): Promise<{ session: AccountTestSession; taskIds: string[] } | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cancelAccountTestSession(id, access, message)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    return undefined
  }
  const taskIds = await cancelAccountTestSessionByRowAsync(client, row, message, 'canceled')
  const session = await getAccountTestSessionAsync(row.id, access)
  return session ? { session, taskIds } : undefined
}

export async function completeIdleAccountTestSessionsAsync(limit = 200): Promise<number> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return completeIdleAccountTestSessions(limit)
  }
  const client = await accountTestTaskDatabaseClient()
  return completeIdleAccountTestSessionsWithClientAsync(client, limit)
}

export async function createAccountTestTaskAsync(input: CreateAccountTestTaskInput): Promise<AccountTestTask> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createAccountTestTask(input)
  }
  await cleanupExpiredAccountTestTasksAsync()

  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  const queuedDeadlineAt = accountTestTaskQueuedDeadlineAt(now)
  const id = newId('accttest')
  const sessionId = normalizedOptionalText(input.sessionId)
  await client.transaction(async (tx) => {
    if (sessionId) {
      await assertUsableAccountTestSessionAsync(tx, sessionId, input.access)
    }
    await tx.execute(`
      INSERT INTO ${accountTestTable(tx, 'account_test_tasks')} (
        id, account_id, account_name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, account_type,
        request_system_account_id, request_role, request_system_account_filter_id,
        diagnostics, model, test_endpoint_mode, draft_account_encrypted, status, status_message,
        cancel_requested, queued_at, queued_deadline_at, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', '等待后台测试', false, ?, ?, ?, ?)
    `, [
      id,
      input.account.id,
      input.account.name,
      input.account.providerCode,
      input.account.providerProtocolProfileId ?? '',
      input.account.protocolCode ?? '',
      input.account.protocolVersion ?? '',
      input.account.type,
      input.access.systemAccountId,
      input.access.role,
      input.access.systemAccountFilterId ?? null,
      input.diagnostics,
      normalizedOptionalText(input.model) ?? null,
      accountTestEndpointMode(input.testEndpointMode) ?? null,
      encryptedDraftAccount(input.draftAccount),
      now,
      queuedDeadlineAt,
      now,
      now
    ])
    if (sessionId) {
      await tx.execute(`
        INSERT INTO ${accountTestTable(tx, 'account_test_session_tasks')} (session_id, task_id, created_at)
        VALUES (?, ?, ?)
      `, [sessionId, id, now])
    }
  })

  const task = await getAccountTestTaskAsync(id)
  if (!task) {
    throw new Error('账户测试任务创建失败')
  }
  return task
}

export async function getAccountTestTaskAsync(id: string, access?: AccessScope): Promise<AccountTestTask | undefined> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_account_test_task_read_only',
      id,
      access
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountTestTask(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestTaskRowAsync(client, id)
  if (!row || !canReadAccountTestTask(row, access)) {
    return undefined
  }
  return accountTestTaskFromRow(row)
}

export async function listAccountTestTasksAsync(ids: string[], access?: AccessScope): Promise<AccountTestTask[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_account_test_tasks_read_only',
      ids,
      access
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountTestTasks(ids, access)
  }
  const normalizedIds = [...new Set(ids.map(normalizedOptionalText).filter((id): id is string => Boolean(id)))].slice(0, 200)
  if (normalizedIds.length === 0) {
    return []
  }
  const client = await accountTestTaskDatabaseClient()
  const placeholders = client.dialect.bindPlaceholders(normalizedIds.length)
  const rows = await client.query<AccountTestTaskRow>(`
    SELECT t.*, st.session_id
    FROM ${accountTestTable(client, 'account_test_tasks')} t
    LEFT JOIN ${accountTestTable(client, 'account_test_session_tasks')} st ON st.task_id = t.id
    WHERE t.id IN (${placeholders})
  `, normalizedIds)
  const tasksById = new Map(rows
    .filter((row) => canReadAccountTestTask(row, access))
    .map((row) => [row.id, accountTestTaskFromRow(row)]))
  return normalizedIds.map((taskId) => tasksById.get(taskId)).filter((task): task is AccountTestTask => Boolean(task))
}

export async function getAccountTestTaskRecordAsync(id: string): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAccountTestTaskRecord(id)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestTaskRowAsync(client, id)
  return row ? accountTestTaskRecordFromRow(row) : undefined
}

export async function listRunnableAccountTestTaskIdsAsync(limit = 100): Promise<string[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listRunnableAccountTestTaskIds(limit)
  }
  await completeIdleAccountTestSessionsAsync()
  const client = await accountTestTaskDatabaseClient()
  const rows = await client.query<{ id: string }>(`
    SELECT t.id
    FROM ${accountTestTable(client, 'account_test_tasks')} t
    LEFT JOIN ${accountTestTable(client, 'account_test_session_tasks')} st ON st.task_id = t.id
    LEFT JOIN ${accountTestTable(client, 'account_test_sessions')} s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  return rows.map((row) => row.id)
}

export async function failExpiredQueuedAccountTestTasksAsync(maxQueuedMs: number, limit = 200): Promise<string[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return failExpiredQueuedAccountTestTasks(maxQueuedMs, limit)
  }
  await completeIdleAccountTestSessionsAsync()
  const client = await accountTestTaskDatabaseClient()
  const safeMaxQueuedMs = Math.max(1, Math.trunc(maxQueuedMs))
  const safeLimit = Math.max(1, Math.trunc(limit))
  const queuedCutoff = new Date(Date.now() - safeMaxQueuedMs).toISOString()
  const rows = await client.query<{ id: string }>(`
    SELECT t.id
    FROM ${accountTestTable(client, 'account_test_tasks')} t
    LEFT JOIN ${accountTestTable(client, 'account_test_session_tasks')} st ON st.task_id = t.id
    LEFT JOIN ${accountTestTable(client, 'account_test_sessions')} s ON s.id = st.session_id
    WHERE t.status = 'queued'
      AND t.cancel_requested = false
      AND (
        (t.queued_deadline_at IS NOT NULL AND t.queued_deadline_at <= ?)
        OR (t.queued_deadline_at IS NULL AND t.queued_at < ?)
      )
      AND (
        st.session_id IS NULL
        OR s.status = 'running'
      )
    ORDER BY t.queued_at ASC, t.id ASC
    LIMIT ?
  `, [nowIso(), queuedCutoff, safeLimit])
  const taskIds = rows.map((row) => row.id)
  if (taskIds.length === 0) {
    return []
  }

  const placeholders = client.dialect.bindPlaceholders(taskIds.length)
  const now = nowIso()
  const message = accountTestQueuedWaitExpiredMessage(safeMaxQueuedMs)
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'failed',
        status_message = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id IN (${placeholders})
      AND status = 'queued'
      AND cancel_requested = false
  `, [message, message, now, now, ...taskIds])
  return taskIds
}

export async function requeueInterruptedAccountTestTasksAsync(): Promise<string[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return requeueInterruptedAccountTestTasks()
  }
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'canceled',
        status_message = '已停止测试',
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = true
  `, [now, now])
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'queued',
        status_message = '后台 worker 重启后重新排队',
        started_at = NULL,
        cancel_requested = false,
        updated_at = ?
    WHERE status = 'running'
      AND cancel_requested = false
  `, [now])
  await cleanupExpiredAccountTestTasksAsync()
  return listRunnableAccountTestTaskIdsAsync()
}

export async function markAccountTestTaskRunningAsync(id: string): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAccountTestTaskRunning(id)
  }
  const client = await accountTestTaskDatabaseClient()
  const cancelReason = await accountTestTaskSessionCancelReasonAsync(client, id)
  if (cancelReason) {
    await markAccountTestTaskCanceledAsync(id, cancelReason)
    return undefined
  }
  const now = nowIso()
  const result = await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'running',
        status_message = '后台测试中',
        started_at = COALESCE(started_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'queued'
      AND cancel_requested = false
  `, [now, now, id])
  if (Number(result.changes ?? 0) === 0) {
    const current = await getAccountTestTaskRecordAsync(id)
    if (current?.status === 'queued' && current.cancelRequested) {
      await markAccountTestTaskCanceledAsync(id, '已停止测试')
    }
    return undefined
  }
  return getAccountTestTaskRecordAsync(id)
}

export async function updateAccountTestTaskMessageAsync(id: string, message: string): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateAccountTestTaskMessage(id, message)
  }
  const normalizedMessage = normalizedOptionalText(message)
  if (!normalizedMessage) return getAccountTestTaskRecordAsync(id)
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status_message = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = false
  `, [normalizedMessage, now, id])
  return getAccountTestTaskRecordAsync(id)
}

export async function completeAccountTestTaskAsync(id: string, result: AccountTestResult): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return completeAccountTestTask(id, result)
  }
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  const status: AccountTestTaskStatus = result.success ? 'success' : 'failed'
  const write = await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = ?,
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND cancel_requested = false
  `, [
    status,
    result.message,
    JSON.stringify(result),
    result.success ? null : result.message,
    now,
    now,
    id
  ])
  if (Number(write.changes ?? 0) === 0) {
    return finalizeAccountTestTaskIfCanceledAsync(id)
  }
  return getAccountTestTaskRecordAsync(id)
}

export async function failAccountTestTaskAsync(id: string, message: string, result?: AccountTestResult): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return failAccountTestTask(id, message, result)
  }
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  const write = await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'failed',
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
      AND cancel_requested = false
  `, [message, result ? JSON.stringify(result) : null, message, now, now, id])
  if (Number(write.changes ?? 0) === 0) {
    return finalizeAccountTestTaskIfCanceledAsync(id)
  }
  return getAccountTestTaskRecordAsync(id)
}

export async function cancelAccountTestTaskAsync(id: string, access?: AccessScope): Promise<AccountTestTask | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return cancelAccountTestTask(id, access)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await getAccountTestTaskRowAsync(client, id)
  if (!row || !canReadAccountTestTask(row, access)) {
    return undefined
  }
  if (row.status === 'queued') {
    await markAccountTestTaskCanceledAsync(id, '已停止测试')
  } else if (row.status === 'running') {
    const now = nowIso()
    await client.execute(`
      UPDATE ${accountTestTable(client, 'account_test_tasks')}
      SET cancel_requested = true,
          status_message = '正在停止测试',
          updated_at = ?
      WHERE id = ?
        AND status = 'running'
    `, [now, id])
  }
  return getAccountTestTaskAsync(id, access)
}

export async function markAccountTestTaskCanceledAsync(id: string, message: string): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return markAccountTestTaskCanceled(id, message)
  }
  const client = await accountTestTaskDatabaseClient()
  const now = nowIso()
  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'canceled',
        status_message = CASE
          WHEN cancel_requested = true AND NULLIF(BTRIM(status_message), '') IS NOT NULL THEN status_message
          ELSE ?
        END,
        cancel_requested = true,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
  `, [message, now, now, id])
  return getAccountTestTaskRecordAsync(id)
}

async function finalizeAccountTestTaskIfCanceledAsync(id: string): Promise<AccountTestTaskRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return finalizeAccountTestTaskIfCanceled(id)
  }
  const current = await getAccountTestTaskRecordAsync(id)
  if (!current) return undefined
  if (current.cancelRequested && (current.status === 'queued' || current.status === 'running')) {
    return markAccountTestTaskCanceledAsync(id, current.message ?? '已停止测试')
  }
  return current
}

export async function isAccountTestTaskCancelRequestedAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return isAccountTestTaskCancelRequested(id)
  }
  const client = await accountTestTaskDatabaseClient()
  const row = await client.one<{ cancel_requested?: number | boolean }>(`
    SELECT t.cancel_requested
    FROM ${accountTestTable(client, 'account_test_tasks')} t
    WHERE t.id = ?
    LIMIT 1
  `, [id])
  return databaseBoolean(row?.cancel_requested) || Boolean(await accountTestTaskSessionCancelReasonAsync(client, id))
}

export async function accountTestTaskCancelMessageAsync(id: string): Promise<string> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return accountTestTaskCancelMessage(id)
  }
  const client = await accountTestTaskDatabaseClient()
  const sessionReason = await accountTestTaskSessionCancelReasonAsync(client, id)
  if (sessionReason) return sessionReason
  const row = await client.one<{ status_message?: string | null; cancel_requested?: number | boolean }>(`
    SELECT status_message, cancel_requested
    FROM ${accountTestTable(client, 'account_test_tasks')}
    WHERE id = ?
    LIMIT 1
  `, [id])
  if (databaseBoolean(row?.cancel_requested)) {
    return normalizedOptionalText(row?.status_message) ?? '已停止测试'
  }
  return '已停止测试'
}

export async function cleanupExpiredAccountTestTasksAsync(): Promise<void> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    cleanupExpiredAccountTestTasks()
    return
  }
  const client = await accountTestTaskDatabaseClient()
  const cutoff = new Date(Date.now() - accountTestTaskRetentionHours * 60 * 60 * 1000).toISOString()
  await client.execute(`
    DELETE FROM ${accountTestTable(client, 'account_test_tasks')}
    WHERE id IN (
      SELECT id
      FROM ${accountTestTable(client, 'account_test_tasks')}
      WHERE finished_at IS NOT NULL
        AND finished_at < ?
      ORDER BY finished_at ASC, id ASC
      LIMIT ?
    )
  `, [cutoff, accountTestCleanupBatchSize])
  await client.execute(`
    DELETE FROM ${accountTestTable(client, 'account_test_sessions')}
    WHERE id IN (
      SELECT s.id
      FROM ${accountTestTable(client, 'account_test_sessions')} s
      WHERE s.updated_at < ?
        AND NOT EXISTS (
          SELECT 1
          FROM ${accountTestTable(client, 'account_test_session_tasks')} st
          JOIN ${accountTestTable(client, 'account_test_tasks')} t ON t.id = st.task_id
          WHERE st.session_id = s.id
            AND t.status IN ('queued', 'running')
        )
      ORDER BY s.updated_at ASC, s.id ASC
      LIMIT ?
    )
  `, [cutoff, accountTestCleanupBatchSize])
}

async function accountTestTaskDatabaseClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function accountTestTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function getAccountTestTaskRowAsync(client: DatabaseClient, id: string): Promise<AccountTestTaskRow | undefined> {
  const normalizedId = normalizedOptionalText(id)
  if (!normalizedId) return undefined
  return await client.one<AccountTestTaskRow>(`
    SELECT t.*, st.session_id
    FROM ${accountTestTable(client, 'account_test_tasks')} t
    LEFT JOIN ${accountTestTable(client, 'account_test_session_tasks')} st ON st.task_id = t.id
    WHERE t.id = ?
    LIMIT 1
  `, [normalizedId])
}

async function getAccountTestSessionRowAsync(client: DatabaseClient, id: string): Promise<AccountTestSessionRow | undefined> {
  const normalizedId = normalizedOptionalText(id)
  if (!normalizedId) return undefined
  return await client.one<AccountTestSessionRow>(`
    SELECT *
    FROM ${accountTestTable(client, 'account_test_sessions')}
    WHERE id = ?
    LIMIT 1
  `, [normalizedId])
}

async function listAccountTestSessionTasksAsync(client: DatabaseClient, sessionId: string, access?: AccessScope, options: AccountTestReadOptions = {}): Promise<AccountTestTask[]> {
  const rows = await client.query<AccountTestTaskRow>(`
    SELECT t.*, st.session_id
    FROM ${accountTestTable(client, 'account_test_session_tasks')} st
    JOIN ${accountTestTable(client, 'account_test_tasks')} t ON t.id = st.task_id
    WHERE st.session_id = ?
    ORDER BY t.queued_at ASC, t.id ASC
  `, [sessionId])
  return rows
    .filter((row) => canReadAccountTestTask(row, access, options))
    .map(accountTestTaskFromRow)
}

async function accountTestSessionDetailFromRowAsync(client: DatabaseClient, row: AccountTestSessionRow, access?: AccessScope, options: AccountTestReadOptions = {}): Promise<AccountTestSessionDetail> {
  return {
    session: accountTestSessionFromRow(row),
    tasks: await listAccountTestSessionTasksAsync(client, row.id, access, options)
  }
}

async function completeAccountTestSessionIfSettledAsync(client: DatabaseClient, sessionId: string): Promise<boolean> {
  const now = nowIso()
  const result = await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_sessions')}
    SET status = 'completed',
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND NOT EXISTS (
        SELECT 1
        FROM ${accountTestTable(client, 'account_test_session_tasks')} st
        JOIN ${accountTestTable(client, 'account_test_tasks')} t ON t.id = st.task_id
        WHERE st.session_id = ?
          AND t.status IN ('queued', 'running')
      )
  `, [now, now, sessionId, sessionId])
  return Number(result.changes ?? 0) > 0
}

async function completeIdleAccountTestSessionsWithClientAsync(client: DatabaseClient, limit = 200, access?: AccessScope): Promise<number> {
  const cutoff = new Date(Date.now() - accountTestSessionIdleCompleteMs).toISOString()
  const scopeClause = access ? 'AND request_system_account_id = ?' : ''
  const params = access
    ? [cutoff, access.systemAccountId, Math.max(1, Math.trunc(limit))]
    : [cutoff, Math.max(1, Math.trunc(limit))]
  const rows = await client.query<AccountTestSessionRow>(`
    SELECT *
    FROM ${accountTestTable(client, 'account_test_sessions')}
    WHERE status = 'running'
      AND last_heartbeat_at < ?
      ${scopeClause}
      AND NOT EXISTS (
        SELECT 1
        FROM ${accountTestTable(client, 'account_test_session_tasks')} st
        JOIN ${accountTestTable(client, 'account_test_tasks')} t ON t.id = st.task_id
        WHERE st.session_id = ${accountTestTable(client, 'account_test_sessions')}.id
          AND t.status IN ('queued', 'running')
      )
    ORDER BY last_heartbeat_at ASC, id ASC
    LIMIT ?
  `, params)
  let completed = 0
  for (const row of rows) {
    completed += await completeAccountTestSessionIfSettledAsync(client, row.id) ? 1 : 0
  }
  return completed
}

async function assertUsableAccountTestSessionAsync(client: DatabaseClient, id: string, access: AccessScope): Promise<void> {
  const row = await getAccountTestSessionRowAsync(client, id)
  if (!row || !canReadAccountTestSession(row, access)) {
    throw new Error('账户测试会话不存在')
  }
  const cancelReason = accountTestSessionCancelReason(row)
  if (cancelReason) {
    await cancelAccountTestSessionByRowAsync(client, row, cancelReason, row.status === 'running' ? 'expired' : accountTestSessionStatus(row.status))
    throw new Error(cancelReason)
  }
  const existingTask = await client.one<{ task_id?: string }>(`
    SELECT task_id
    FROM ${accountTestTable(client, 'account_test_session_tasks')}
    WHERE session_id = ?
    LIMIT 1
  `, [row.id])
  if (existingTask?.task_id) {
    throw new Error('账户测试会话只能包含一个账户任务')
  }
}

async function cancelAccountTestSessionByRowAsync(client: DatabaseClient, row: AccountTestSessionRow, message: string, status: AccountTestSessionStatus): Promise<string[]> {
  const now = nowIso()
  const taskRows = await client.query<{ id: string; status: string }>(`
    SELECT t.id, t.status
    FROM ${accountTestTable(client, 'account_test_session_tasks')} st
    JOIN ${accountTestTable(client, 'account_test_tasks')} t ON t.id = st.task_id
    WHERE st.session_id = ?
      AND t.status IN ('queued', 'running')
    ORDER BY t.queued_at ASC, t.id ASC
  `, [row.id])

  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_sessions')}
    SET status = ?,
        cancel_reason = ?,
        cancel_requested_at = COALESCE(cancel_requested_at, ?),
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `, [status, message, now, now, now, row.id])

  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET status = 'canceled',
        status_message = ?,
        cancel_requested = true,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id IN (
      SELECT task_id
      FROM ${accountTestTable(client, 'account_test_session_tasks')}
      WHERE session_id = ?
    )
      AND status = 'queued'
  `, [message, now, now, row.id])

  await client.execute(`
    UPDATE ${accountTestTable(client, 'account_test_tasks')}
    SET cancel_requested = true,
        status_message = ?,
        updated_at = ?
    WHERE id IN (
      SELECT task_id
      FROM ${accountTestTable(client, 'account_test_session_tasks')}
      WHERE session_id = ?
    )
      AND status = 'running'
  `, [message, now, row.id])

  return taskRows.map((taskRow) => taskRow.id)
}

async function accountTestTaskSessionCancelReasonAsync(client: DatabaseClient, taskId: string): Promise<string | undefined> {
  const row = await client.one<AccountTestSessionRow>(`
    SELECT s.*
    FROM ${accountTestTable(client, 'account_test_session_tasks')} st
    JOIN ${accountTestTable(client, 'account_test_sessions')} s ON s.id = st.session_id
    WHERE st.task_id = ?
    LIMIT 1
  `, [taskId])
  return row ? accountTestSessionCancelReason(row) : undefined
}

function getAccountTestTaskRow(id: string): AccountTestTaskRow | undefined {
  const normalizedId = normalizedOptionalText(id)
  if (!normalizedId) return undefined
  return getBusinessDatabase().prepare(`
    SELECT t.*, st.session_id
    FROM account_test_tasks t
    LEFT JOIN account_test_session_tasks st ON st.task_id = t.id
    WHERE t.id = ?
    LIMIT 1
  `).get(normalizedId) as unknown as AccountTestTaskRow | undefined
}

function getAccountTestSessionRow(id: string): AccountTestSessionRow | undefined {
  const normalizedId = normalizedOptionalText(id)
  if (!normalizedId) return undefined
  return getBusinessDatabase().prepare(`
    SELECT *
    FROM account_test_sessions
    WHERE id = ?
    LIMIT 1
  `).get(normalizedId) as unknown as AccountTestSessionRow | undefined
}

interface AccountTestReadOptions {
  ownerOnly?: boolean
}

function canReadAccountTestTask(row: AccountTestTaskRow, access?: AccessScope, options: AccountTestReadOptions = {}): boolean {
  if (!access) return true
  if (row.request_system_account_id !== access.systemAccountId) return false
  if (options.ownerOnly) return true
  const rowFilterId = normalizedOptionalText(row.request_system_account_filter_id)
  const accessFilterId = normalizedOptionalText(access.systemAccountFilterId)
  return rowFilterId === accessFilterId
}

function canReadAccountTestSession(row: AccountTestSessionRow, access?: AccessScope): boolean {
  if (!access) return true
  if (row.request_system_account_id !== access.systemAccountId) return false
  const rowFilterId = normalizedOptionalText(row.request_system_account_filter_id)
  const accessFilterId = normalizedOptionalText(access.systemAccountFilterId)
  return !rowFilterId || rowFilterId === accessFilterId
}

function accountTestTaskFromRow(row: AccountTestTaskRow): AccountTestTask {
  const queuedAt = databaseDateTimeIso(row.queued_at, 'account_test_tasks.queued_at')
  const queuedDeadlineAt = row.queued_deadline_at === null || row.queued_deadline_at === undefined
    ? accountTestTaskQueuedDeadlineAt(queuedAt)
    : databaseDateTimeIso(row.queued_deadline_at, 'account_test_tasks.queued_deadline_at')
  return {
    id: row.id,
    sessionId: row.session_id ?? undefined,
    accountId: row.account_id,
    accountName: row.account_name,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    type: row.account_type,
    status: accountTestTaskStatus(row.status),
    message: row.status_message ?? row.error_message ?? undefined,
    model: row.model ?? undefined,
    testEndpointMode: accountTestEndpointMode(row.test_endpoint_mode),
    result: accountTestResult(row.result_json),
    cancelRequested: databaseBoolean(row.cancel_requested),
    createdAt: databaseDateTimeIso(row.created_at, 'account_test_tasks.created_at'),
    queuedAt,
    queuedDeadlineAt,
    startedAt: databaseOptionalDateTimeIso(row.started_at, 'account_test_tasks.started_at'),
    finishedAt: databaseOptionalDateTimeIso(row.finished_at, 'account_test_tasks.finished_at'),
    updatedAt: databaseDateTimeIso(row.updated_at, 'account_test_tasks.updated_at')
  }
}

function accountTestTaskQueuedDeadlineAt(queuedAt: string): string {
  const canonicalQueuedAt = requiredRfc3339Instant(queuedAt, 'account_test_tasks.queued_at')
  const queuedAtMs = rfc3339InstantMilliseconds(canonicalQueuedAt)
  if (queuedAtMs === undefined) throw new Error('account_test_tasks.queued_at 必须是有效 RFC3339 时间')
  return new Date(queuedAtMs + runtimeConfig.background.accountTestQueuedMaxWaitMs).toISOString()
}

function databaseBoolean(value: unknown): boolean {
  return value === true || Number(value ?? 0) === 1
}

function databaseDateTimeIso(value: string | Date, column: string): string {
  if (value instanceof Date) {
    if (!Number.isFinite(value.getTime())) {
      throw new Error(`${column} 必须是有效时间`)
    }
    return value.toISOString()
  }
  return requiredRfc3339Instant(value, column)
}

function databaseOptionalDateTimeIso(value: string | Date | null, column: string): string | undefined {
  return value === null ? undefined : databaseDateTimeIso(value, column)
}

function accountTestSessionFromRow(row: AccountTestSessionRow): AccountTestSession {
  return {
    id: row.id,
    status: accountTestSessionStatus(row.status),
    message: row.cancel_reason ?? undefined,
    lastHeartbeatAt: databaseDateTimeIso(row.last_heartbeat_at, 'account_test_sessions.last_heartbeat_at'),
    cancelRequestedAt: databaseOptionalDateTimeIso(row.cancel_requested_at, 'account_test_sessions.cancel_requested_at'),
    finishedAt: databaseOptionalDateTimeIso(row.finished_at, 'account_test_sessions.finished_at'),
    createdAt: databaseDateTimeIso(row.created_at, 'account_test_sessions.created_at'),
    updatedAt: databaseDateTimeIso(row.updated_at, 'account_test_sessions.updated_at')
  }
}

function listAccountTestSessionTasks(sessionId: string, access?: AccessScope, options: AccountTestReadOptions = {}): AccountTestTask[] {
  const rows = getBusinessDatabase().prepare(`
    SELECT t.*, st.session_id
    FROM account_test_session_tasks st
    JOIN account_test_tasks t ON t.id = st.task_id
    WHERE st.session_id = ?
    ORDER BY t.queued_at ASC, t.id ASC
  `).all(sessionId) as unknown as AccountTestTaskRow[]
  return rows
    .filter((row) => canReadAccountTestTask(row, access, options))
    .map(accountTestTaskFromRow)
}

function accountTestSessionDetailFromRow(row: AccountTestSessionRow, access?: AccessScope, options: AccountTestReadOptions = {}): AccountTestSessionDetail {
  return {
    session: accountTestSessionFromRow(row),
    tasks: listAccountTestSessionTasks(row.id, access, options)
  }
}

function completeAccountTestSessionIfSettled(sessionId: string): boolean {
  const now = nowIso()
  const result = getBusinessDatabase().prepare(`
    UPDATE account_test_sessions
    SET status = 'completed',
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
      AND NOT EXISTS (
        SELECT 1
        FROM account_test_session_tasks st
        JOIN account_test_tasks t ON t.id = st.task_id
        WHERE st.session_id = ?
          AND t.status IN ('queued', 'running')
      )
  `).run(now, now, sessionId, sessionId)
  return Number(result.changes ?? 0) > 0
}

function accountTestSessionStatus(value: string): AccountTestSessionStatus {
  if (value === 'running' || value === 'canceled' || value === 'expired' || value === 'completed') {
    return value
  }
  return 'expired'
}

function accountTestTaskRecordFromRow(row: AccountTestTaskRow): AccountTestTaskRecord {
  return {
    ...accountTestTaskFromRow(row),
    requestSystemAccountId: row.request_system_account_id,
    requestRole: systemAccountRole(row.request_role),
    requestSystemAccountFilterId: row.request_system_account_filter_id ?? undefined,
    diagnostics: row.diagnostics === 'limited' ? 'limited' : 'full',
    draftAccount: accountTestDraftSnapshot(row.draft_account_encrypted),
    errorMessage: row.error_message ?? undefined
  }
}

function assertUsableAccountTestSession(id: string, access: AccessScope): void {
  const row = getAccountTestSessionRow(id)
  if (!row || !canReadAccountTestSession(row, access)) {
    throw new Error('账户测试会话不存在')
  }
  const cancelReason = accountTestSessionCancelReason(row)
  if (cancelReason) {
    cancelAccountTestSessionByRow(row, cancelReason, row.status === 'running' ? 'expired' : accountTestSessionStatus(row.status))
    throw new Error(cancelReason)
  }
  const existingTask = getBusinessDatabase().prepare(`
    SELECT task_id
    FROM account_test_session_tasks
    WHERE session_id = ?
    LIMIT 1
  `).get(row.id) as unknown as { task_id?: string } | undefined
  if (existingTask?.task_id) {
    throw new Error('账户测试会话只能包含一个账户任务')
  }
}

function cancelAccountTestSessionByRow(row: AccountTestSessionRow, message: string, status: AccountTestSessionStatus): string[] {
  const now = nowIso()
  const taskRows = getBusinessDatabase().prepare(`
    SELECT t.id, t.status
    FROM account_test_session_tasks st
    JOIN account_test_tasks t ON t.id = st.task_id
    WHERE st.session_id = ?
      AND t.status IN ('queued', 'running')
    ORDER BY t.queued_at ASC, t.id ASC
  `).all(row.id) as unknown as Array<{ id: string; status: string }>

  getBusinessDatabase().prepare(`
    UPDATE account_test_sessions
    SET status = ?,
        cancel_reason = ?,
        cancel_requested_at = COALESCE(cancel_requested_at, ?),
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `).run(status, message, now, now, now, row.id)

  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'canceled',
        status_message = ?,
        cancel_requested = 1,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id IN (
      SELECT task_id
      FROM account_test_session_tasks
      WHERE session_id = ?
    )
      AND status = 'queued'
  `).run(message, now, now, row.id)

  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET cancel_requested = 1,
        status_message = ?,
        updated_at = ?
    WHERE id IN (
      SELECT task_id
      FROM account_test_session_tasks
      WHERE session_id = ?
    )
      AND status = 'running'
  `).run(message, now, row.id)

  return taskRows.map((taskRow) => taskRow.id)
}

function accountTestTaskSessionCancelReason(taskId: string): string | undefined {
  const row = getBusinessDatabase().prepare(`
    SELECT s.*
    FROM account_test_session_tasks st
    JOIN account_test_sessions s ON s.id = st.session_id
    WHERE st.task_id = ?
    LIMIT 1
  `).get(taskId) as unknown as AccountTestSessionRow | undefined
  return row ? accountTestSessionCancelReason(row) : undefined
}

function accountTestSessionCancelReason(row: AccountTestSessionRow): string | undefined {
  if (row.status === 'canceled') {
    return row.cancel_reason ?? '已停止测试'
  }
  if (row.status === 'expired') {
    return row.cancel_reason ?? '账户测试会话已过期'
  }
  if (row.status !== 'running') {
    return row.cancel_reason ?? '账户测试会话已结束'
  }
  return undefined
}

function accountTestQueuedWaitExpiredMessage(maxQueuedMs: number): string {
  return `后台测试队列等待超过 ${formatAccountTestQueuedWait(maxQueuedMs)}，任务已自动收口；请检查运维 worker 或降低批量并发`
}

function formatAccountTestQueuedWait(maxQueuedMs: number): string {
  const seconds = Math.max(1, Math.ceil(maxQueuedMs / 1000))
  if (seconds < 60) {
    return `${seconds} 秒`
  }
  const minutes = Math.ceil(seconds / 60)
  if (minutes < 60) {
    return `${minutes} 分钟`
  }
  const hours = Math.ceil(minutes / 60)
  return `${hours} 小时`
}

function accountTestTaskStatus(value: string): AccountTestTaskStatus {
  if (value === 'queued' || value === 'running' || value === 'success' || value === 'failed' || value === 'canceled') {
    return value
  }
  return 'failed'
}

function systemAccountRole(value: string): SystemAccountRole {
  return value === 'super_admin' || value === 'admin' || value === 'user' ? value : 'user'
}

function accountClientCompatibility(value: string | null): AccountClientCompatibility | undefined {
  return value === 'openai_standard' || value === 'codex_responses' ? value : undefined
}

function accountTestEndpointMode(value: unknown): AccountSupportedEndpointMode | undefined {
  return value === 'chat_json'
    || value === 'chat_sse'
    || value === 'responses_json'
    || value === 'responses_sse'
    || value === 'messages_json'
    || value === 'messages_sse'
    || value === 'message_token_counting'
    || value === 'generate_content_json'
    || value === 'generate_content_sse'
    || value === 'count_tokens'
    || value === 'embed_content'
    ? value
    : undefined
}

function accountTestResult(value: string | null): AccountTestResult | undefined {
  if (!value) return undefined
  try {
    const parsed = JSON.parse(value) as AccountTestResult
    if (typeof parsed === 'object' && parsed !== null && typeof parsed.accountId === 'string' && typeof parsed.message === 'string') {
      return parsed
    }
  } catch {
  }
  return undefined
}

function encryptedDraftAccount(value: AccountTestDraftSnapshot | undefined): string | null {
  if (!value) return null
  const bytes = Buffer.byteLength(JSON.stringify(value), 'utf8')
  if (bytes > 64 * 1024) {
    throw new Error('账户测试草稿过大')
  }
  return encryptJson(value)
}

function accountTestDraftSnapshot(value: string | null): AccountTestDraftSnapshot | undefined {
  if (!value) return undefined
  try {
    return normalizeAccountTestDraftSnapshot(decryptJson<unknown>(value))
  } catch {
    return undefined
  }
}

function normalizeAccountTestDraftSnapshot(value: unknown): AccountTestDraftSnapshot | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const record = value as Record<string, unknown>
  const id = normalizedOptionalText(record.id)
  const ownerSystemAccountId = normalizedOptionalText(record.ownerSystemAccountId)
  const groupId = normalizedOptionalText(record.groupId)
  const providerCode = normalizedOptionalText(record.providerCode)
  const providerProtocolProfileId = normalizedOptionalText(record.providerProtocolProfileId)
  const protocolCode = normalizedOptionalText(record.protocolCode)
  const protocolVersion = normalizedOptionalText(record.protocolVersion)
  const name = normalizedOptionalText(record.name)
  const type = normalizedOptionalText(record.type)
  const credentials = record.credentials
  const clientCompatibility = accountClientCompatibility(normalizedOptionalText(record.clientCompatibility) ?? null)
  const healthCheckModel = normalizedOptionalText(record.healthCheckModel)
  const healthCheckEndpointMode = accountHealthCheckEndpointModeValue(record.healthCheckEndpointMode)
  if (!id || !ownerSystemAccountId || !groupId || !providerCode || !name || !type || !clientCompatibility || !healthCheckModel || !healthCheckEndpointMode) {
    return undefined
  }
  if (typeof credentials !== 'object' || credentials === null || Array.isArray(credentials)) {
    return undefined
  }
  return {
    id,
    stateTargetAccountId: normalizedOptionalText(record.stateTargetAccountId),
    ownerSystemAccountId,
    groupId,
    groupName: normalizedOptionalText(record.groupName),
    providerCode,
    providerProtocolProfileId,
    protocolCode,
    protocolVersion,
    name,
    type,
    credentials: credentials as Record<string, unknown>,
    concurrencyLimit: positiveIntegerValue(record.concurrencyLimit, 1),
    priority: integerValue(record.priority, 0),
    superPriorityEnabled: booleanValue(record.superPriorityEnabled),
    fallbackEnabled: booleanValue(record.fallbackEnabled),
    clientCompatibility,
    supportedModels: stringListValue(record.supportedModels),
    healthCheckModel,
    healthCheckEndpointMode,
    modelMappings: accountModelMappingsValue(record.modelMappings),
    proxyProfileId: normalizedOptionalText(record.proxyProfileId),
    accountExpiresAt: normalizedOptionalText(record.accountExpiresAt),
    availabilitySchedule: availabilityScheduleValue(record.availabilitySchedule),
    availabilityScheduleJson: normalizedOptionalText(record.availabilityScheduleJson),
    notes: normalizedOptionalText(record.notes)
  }
}

function accountHealthCheckEndpointModeValue(value: unknown): AccountSummary['healthCheckEndpointMode'] | undefined {
  if (ACCOUNT_HEALTH_CHECK_ENDPOINT_MODES.includes(value as AccountSummary['healthCheckEndpointMode'])) {
    return value as AccountSummary['healthCheckEndpointMode']
  }
  return undefined
}

function stringListValue(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined
  const items = [...new Set(value.map(normalizedOptionalText).filter((item): item is string => Boolean(item)))].slice(0, 500)
  return items.length ? items : undefined
}

function accountModelMappingsValue(value: unknown): AccountSummary['modelMappings'] | undefined {
  if (!Array.isArray(value)) return undefined
  const output: NonNullable<AccountSummary['modelMappings']> = []
  const seenSources = new Set<string>()
  for (const item of value) {
    if (typeof item !== 'object' || item === null || Array.isArray(item)) {
      continue
    }
    const record = item as Record<string, unknown>
    const sourceModel = normalizedOptionalText(record.sourceModel)
    const sourceEndpointFamily = accountModelMappingSourceEndpointFamilyValue(record.sourceEndpointFamily)
    const upstreamModel = normalizedOptionalText(record.upstreamModel)
    const upstreamEndpointFamily = accountModelMappingUpstreamEndpointFamilyValue(record.upstreamEndpointFamily)
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel ?? ''}`
    if (!sourceModel || !sourceEndpointFamily || !upstreamModel || !upstreamEndpointFamily || (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) || seenSources.has(sourceKey)) {
      continue
    }
    seenSources.add(sourceKey)
    output.push({
      sourceModel,
      sourceEndpointFamily,
      upstreamModel,
      upstreamEndpointFamily,
      enabled: record.enabled !== false
    })
    if (output.length >= 500) {
      break
    }
  }
  return output.length ? output : undefined
}

function accountModelMappingSourceEndpointFamilyValue(value: unknown): 'chat_completions' | 'responses' | 'messages' | 'generate_content' | 'stream_generate_content' | undefined {
  return value === 'chat_completions'
    || value === 'responses'
    || value === 'messages'
    || value === 'generate_content'
    || value === 'stream_generate_content'
    ? value
    : undefined
}

function accountModelMappingUpstreamEndpointFamilyValue(value: unknown): 'chat_completions' | 'responses' | 'messages' | 'generate_content' | undefined {
  return value === 'chat_completions' || value === 'responses' || value === 'messages' || value === 'generate_content' ? value : undefined
}

function availabilityScheduleValue(value: unknown): AccountAvailabilitySchedule | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as AccountAvailabilitySchedule
    : undefined
}

function positiveIntegerValue(value: unknown, fallback: number): number {
  const number = integerValue(value, fallback)
  return number > 0 ? number : fallback
}

function integerValue(value: unknown, fallback: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return fallback
  return Math.trunc(value)
}

function booleanValue(value: unknown): boolean {
  return value === true
}

function normalizedOptionalText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}
