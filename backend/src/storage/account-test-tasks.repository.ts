import type {
  AccountClientCompatibility,
  AccountSummary,
  AccountTestResult,
  AccountTestTask,
  AccountTestTaskStatus,
  SystemAccountRole
} from '../domain/types.js'
import { normalizeOpenAIAccountClientCompatibility } from '../domain/account-client-compatibility.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { AccessScope } from './access-scope.js'

export type AccountTestTaskDiagnostics = 'full' | 'limited'

export interface AccountTestTaskRecord extends AccountTestTask {
  requestSystemAccountId: string
  requestRole: SystemAccountRole
  requestSystemAccountFilterId?: string
  diagnostics: AccountTestTaskDiagnostics
  errorMessage?: string
}

interface AccountTestTaskRow {
  id: string
  account_id: string
  account_name: string
  provider_code: string
  account_type: string
  request_system_account_id: string
  request_role: string
  request_system_account_filter_id: string | null
  diagnostics: string
  model: string | null
  client_compatibility: string | null
  status: string
  status_message: string | null
  result_json: string | null
  error_message: string | null
  cancel_requested: number
  queued_at: string
  started_at: string | null
  finished_at: string | null
  created_at: string
  updated_at: string
}

export interface CreateAccountTestTaskInput {
  account: AccountSummary
  access: AccessScope
  diagnostics: AccountTestTaskDiagnostics
  model?: string
  clientCompatibility?: AccountClientCompatibility
}

const accountTestTaskRetentionHours = 24
const maxActiveAccountTestTasksPerRequester = 200

export function createAccountTestTask(input: CreateAccountTestTaskInput): AccountTestTask {
  cleanupExpiredAccountTestTasks()
  const activeCount = countActiveAccountTestTasksForRequester(input.access.systemAccountId)
  if (activeCount >= maxActiveAccountTestTasksPerRequester) {
    throw new Error('账户测试队列已达到当前用户上限，请等待已有任务完成后再试')
  }

  const now = nowIso()
  const id = newId('accttest')
  const clientCompatibility = normalizeAccountTestTaskClientCompatibility(input.account, input.clientCompatibility)
  getBusinessDatabase().prepare(`
    INSERT INTO account_test_tasks (
      id, account_id, account_name, provider_code, account_type,
      request_system_account_id, request_role, request_system_account_filter_id,
      diagnostics, model, client_compatibility, status, status_message,
      cancel_requested, queued_at, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', '等待后台测试', 0, ?, ?, ?)
  `).run(
    id,
    input.account.id,
    input.account.name,
    input.account.providerCode,
    input.account.type,
    input.access.systemAccountId,
    input.access.role,
    input.access.systemAccountFilterId ?? null,
    input.diagnostics,
    normalizedOptionalText(input.model) ?? null,
    clientCompatibility ?? null,
    now,
    now,
    now
  )

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
    SELECT *
    FROM account_test_tasks
    WHERE id IN (${placeholders})
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
  const rows = getBusinessDatabase().prepare(`
    SELECT id
    FROM account_test_tasks
    WHERE status = 'queued'
    ORDER BY queued_at ASC, id ASC
    LIMIT ?
  `).all(Math.max(1, Math.min(500, Math.trunc(limit)))) as unknown as Array<{ id: string }>
  return rows.map((row) => row.id)
}

export function requeueInterruptedAccountTestTasks(): string[] {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'queued',
        status_message = '后台 worker 重启后重新排队',
        started_at = NULL,
        cancel_requested = 0,
        updated_at = ?
    WHERE status = 'running'
  `).run(now)
  cleanupExpiredAccountTestTasks()
  return listRunnableAccountTestTaskIds()
}

export function markAccountTestTaskRunning(id: string): AccountTestTaskRecord | undefined {
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

export function completeAccountTestTask(id: string, result: AccountTestResult): AccountTestTaskRecord | undefined {
  const now = nowIso()
  const status: AccountTestTaskStatus = result.success ? 'success' : 'failed'
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = ?,
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status = 'running'
  `).run(
    status,
    result.message,
    JSON.stringify(result),
    result.success ? null : result.message,
    now,
    now,
    id
  )
  return getAccountTestTaskRecord(id)
}

export function failAccountTestTask(id: string, message: string, result?: AccountTestResult): AccountTestTaskRecord | undefined {
  const now = nowIso()
  getBusinessDatabase().prepare(`
    UPDATE account_test_tasks
    SET status = 'failed',
        status_message = ?,
        result_json = ?,
        error_message = ?,
        finished_at = ?,
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
  `).run(message, result ? JSON.stringify(result) : null, message, now, now, id)
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
        status_message = ?,
        cancel_requested = 1,
        finished_at = COALESCE(finished_at, ?),
        updated_at = ?
    WHERE id = ?
      AND status IN ('queued', 'running')
  `).run(message, now, now, id)
  return getAccountTestTaskRecord(id)
}

export function isAccountTestTaskCancelRequested(id: string): boolean {
  const row = getBusinessDatabase().prepare(`
    SELECT cancel_requested
    FROM account_test_tasks
    WHERE id = ?
    LIMIT 1
  `).get(id) as unknown as { cancel_requested?: number } | undefined
  return Number(row?.cancel_requested ?? 0) === 1
}

export function cleanupExpiredAccountTestTasks(): void {
  const cutoff = new Date(Date.now() - accountTestTaskRetentionHours * 60 * 60 * 1000).toISOString()
  getBusinessDatabase().prepare(`
    DELETE FROM account_test_tasks
    WHERE finished_at IS NOT NULL
      AND finished_at < ?
  `).run(cutoff)
}

function getAccountTestTaskRow(id: string): AccountTestTaskRow | undefined {
  const normalizedId = normalizedOptionalText(id)
  if (!normalizedId) return undefined
  return getBusinessDatabase().prepare(`
    SELECT *
    FROM account_test_tasks
    WHERE id = ?
    LIMIT 1
  `).get(normalizedId) as unknown as AccountTestTaskRow | undefined
}

function countActiveAccountTestTasksForRequester(systemAccountId: string): number {
  const row = getBusinessDatabase().prepare(`
    SELECT COUNT(*) AS count
    FROM account_test_tasks
    WHERE request_system_account_id = ?
      AND status IN ('queued', 'running')
  `).get(systemAccountId) as unknown as { count?: number } | undefined
  return Math.max(0, Number(row?.count ?? 0))
}

function canReadAccountTestTask(row: AccountTestTaskRow, access?: AccessScope): boolean {
  if (!access) return true
  if (row.request_system_account_id !== access.systemAccountId) return false
  const rowFilterId = normalizedOptionalText(row.request_system_account_filter_id)
  const accessFilterId = normalizedOptionalText(access.systemAccountFilterId)
  return rowFilterId === accessFilterId
}

function accountTestTaskFromRow(row: AccountTestTaskRow): AccountTestTask {
  return {
    id: row.id,
    accountId: row.account_id,
    accountName: row.account_name,
    providerCode: row.provider_code,
    type: row.account_type,
    status: accountTestTaskStatus(row.status),
    message: row.status_message ?? row.error_message ?? undefined,
    model: row.model ?? undefined,
    clientCompatibility: accountTestTaskClientCompatibility(row),
    result: accountTestResult(row.result_json),
    cancelRequested: Number(row.cancel_requested ?? 0) === 1,
    createdAt: row.created_at,
    queuedAt: row.queued_at,
    startedAt: row.started_at ?? undefined,
    finishedAt: row.finished_at ?? undefined,
    updatedAt: row.updated_at
  }
}

function accountTestTaskRecordFromRow(row: AccountTestTaskRow): AccountTestTaskRecord {
  return {
    ...accountTestTaskFromRow(row),
    requestSystemAccountId: row.request_system_account_id,
    requestRole: systemAccountRole(row.request_role),
    requestSystemAccountFilterId: row.request_system_account_filter_id ?? undefined,
    diagnostics: row.diagnostics === 'limited' ? 'limited' : 'full',
    errorMessage: row.error_message ?? undefined
  }
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

function normalizeAccountTestTaskClientCompatibility(account: AccountSummary, value: AccountClientCompatibility | undefined): AccountClientCompatibility | undefined {
  if (account.providerCode === 'openai' && account.type === 'oauth') {
    return 'codex_responses'
  }
  return value === undefined
    ? undefined
    : normalizeOpenAIAccountClientCompatibility(account.providerCode, account.type, value, account.clientCompatibility)
}

function accountTestTaskClientCompatibility(row: AccountTestTaskRow): AccountClientCompatibility | undefined {
  if (row.provider_code === 'openai' && row.account_type === 'oauth') {
    return 'codex_responses'
  }
  return accountClientCompatibility(row.client_compatibility)
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

function normalizedOptionalText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}
