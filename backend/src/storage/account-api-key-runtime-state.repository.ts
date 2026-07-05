import type { AccountApiKeyRuntimeSelectionState, AccountApiKeyRuntimeStatus } from './account-api-key-rotation.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled } from './account-api-key-rotation.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'
import type { OpenAIAccountSecret } from './openai-account-selector.types.js'
import type { AccountApiKeyRuntimeDetail } from '../domain/types.js'

export interface AccountApiKeyRuntimeFailureInput {
  account: OpenAIAccountSecret
  status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  cooldownUntil?: string
  observedAt?: string
}

export interface AccountApiKeyRuntimeWriteResult {
  changed: boolean
  skippedReason?: string
}

export interface AccountApiKeyRuntimeProbeCandidate {
  accountId: string
  accountName: string
  keyFingerprint: string
  keyIndex: number
  apiKey: string
  status: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  nextProbeAt?: string
}

export interface AccountApiKeyRuntimeSummary {
  total: number
  active: number
  temporaryUnavailable: number
  rateLimited: number
  error: number
  disabled: number
  unavailable: number
  allUnavailable: boolean
  nextProbeAt?: string
}

interface AccountApiKeyRuntimeRow {
  account_id: string
  key_fingerprint: string
  key_index: number
  status: AccountApiKeyRuntimeStatus
  cooldown_until: string | null
  next_probe_at: string | null
  probe_backoff_seconds: number | null
}

interface AccountApiKeyRuntimeDetailRow {
  account_id: string
  key_fingerprint: string
  key_index: number
  status: AccountApiKeyRuntimeStatus
  failure_count: number | null
  consecutive_failures: number | null
  success_count: number | null
  cooldown_until: string | null
  next_probe_at: string | null
  last_attempt_at: string | null
  last_success_at: string | null
  last_failure_at: string | null
  last_error_code: string | null
  last_error_message: string | null
}

interface AccountApiKeyRuntimeTarget {
  systemAccountId: string
  accountId: string
  keyFingerprint: string
  keyIndex: number
}

interface AccountApiKeyRuntimeProbeRow {
  account_id: string
  key_fingerprint: string
  key_index: number
  status: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  next_probe_at: string | null
  account_name: string
  provider_code: string
  protocol_code: string
  protocol_version: string
  type: string
  credentials_encrypted: string
}

interface AccountApiKeyRuntimeSummarySourceRow {
  viewAccountId: string
  sourceAccountId: string
  providerCode: string
  protocolCode: string
  protocolVersion: string
  type: string
  credentialsEncrypted: string
}

const initialProbeBackoffSeconds = 3
const maxProbeBackoffSeconds = 60 * 60
const businessSchemaName = 'juhe_business'

export function loadAccountApiKeyRuntimeStatesByAccountIds(
  accountIds: string[]
): Map<string, AccountApiKeyRuntimeSelectionState[]> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const result = new Map<string, AccountApiKeyRuntimeSelectionState[]>()
  if (!ids.length) return result
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = database
      .prepare(`
        SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at
        FROM account_api_key_runtime_states
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{
        account_id: string
        key_fingerprint: string
        key_index: number
        status: AccountApiKeyRuntimeStatus
        cooldown_until: string | null
        next_probe_at: string | null
      }>
    for (const row of rows) {
      const states = result.get(row.account_id) ?? []
      states.push({
        keyFingerprint: row.key_fingerprint,
        status: row.status,
        keyIndex: Number.isInteger(row.key_index) ? row.key_index : undefined,
        cooldownUntil: row.cooldown_until ?? undefined,
        nextProbeAt: row.next_probe_at ?? undefined
      })
      result.set(row.account_id, states)
    }
  }
  return result
}

export async function loadAccountApiKeyRuntimeStatesByAccountIdsAsync(
  accountIds: string[]
): Promise<Map<string, AccountApiKeyRuntimeSelectionState[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadAccountApiKeyRuntimeStatesByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const result = new Map<string, AccountApiKeyRuntimeSelectionState[]>()
  if (!ids.length) return result
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = await client.query<{
      account_id: string
      key_fingerprint: string
      key_index: number
      status: AccountApiKeyRuntimeStatus
      cooldown_until: string | null
      next_probe_at: string | null
    }>(`
      SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at
      FROM ${accountApiKeyRuntimeStatesTable(client)}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
    for (const row of rows) {
      const states = result.get(row.account_id) ?? []
      states.push({
        keyFingerprint: row.key_fingerprint,
        status: row.status,
        keyIndex: Number.isInteger(row.key_index) ? row.key_index : undefined,
        cooldownUntil: row.cooldown_until ?? undefined,
        nextProbeAt: row.next_probe_at ?? undefined
      })
      result.set(row.account_id, states)
    }
  }
  return result
}

export function listAccountApiKeyRuntimeStatesDueForProbe(limit = 20): AccountApiKeyRuntimeProbeCandidate[] {
  const normalizedLimit = Math.max(1, Math.min(100, Math.trunc(limit)))
  const now = nowIso()
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT states.account_id, states.key_fingerprint, states.key_index, states.status, states.next_probe_at,
        accounts.name AS account_name, accounts.provider_code, accounts.protocol_code, accounts.protocol_version,
        accounts.type, accounts.credentials_encrypted
      FROM account_api_key_runtime_states states
      JOIN accounts ON accounts.id = states.account_id
      WHERE states.status IN ('temporary_unavailable', 'rate_limited', 'error')
        AND states.next_probe_at IS NOT NULL
        AND states.next_probe_at <= ?
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
      LIMIT ?
    `)
    .all(now, now, normalizedLimit * 3) as unknown as AccountApiKeyRuntimeProbeRow[]
  return accountApiKeyRuntimeProbeCandidatesFromRows(rows, normalizedLimit)
}

export async function listAccountApiKeyRuntimeStatesDueForProbeAsync(limit = 20): Promise<AccountApiKeyRuntimeProbeCandidate[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAccountApiKeyRuntimeStatesDueForProbe(limit)
  }
  const normalizedLimit = Math.max(1, Math.min(100, Math.trunc(limit)))
  const now = nowIso()
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const rows = await client.query<AccountApiKeyRuntimeProbeRow>(`
    SELECT states.account_id, states.key_fingerprint, states.key_index, states.status, states.next_probe_at,
      accounts.name AS account_name, accounts.provider_code, accounts.protocol_code, accounts.protocol_version,
      accounts.type, accounts.credentials_encrypted
    FROM ${accountApiKeyRuntimeStatesTable(client)} states
    JOIN ${accountApiKeyRuntimeBusinessTable(client, 'accounts')} accounts ON accounts.id = states.account_id
    WHERE states.status IN ('temporary_unavailable', 'rate_limited', 'error')
      AND states.next_probe_at IS NOT NULL
      AND states.next_probe_at <= ?
      AND accounts.deleted_at IS NULL
      AND accounts.status = 'active'
      AND accounts.schedulable = 1
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
    LIMIT ?
  `, [now, now, normalizedLimit * 3])
  return accountApiKeyRuntimeProbeCandidatesFromRows(rows, normalizedLimit)
}

function accountApiKeyRuntimeProbeCandidatesFromRows(rows: AccountApiKeyRuntimeProbeRow[], limit: number): AccountApiKeyRuntimeProbeCandidate[] {
  const normalizedLimit = Math.max(1, Math.min(100, Math.trunc(limit)))
  const output: AccountApiKeyRuntimeProbeCandidate[] = []
  for (const row of rows) {
    let credentials: Record<string, unknown>
    try {
      credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
    } catch {
      continue
    }
    if (!isAccountApiKeyPoolIsolationEnabled({
      providerCode: row.provider_code,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type,
      credentials
    })) {
      continue
    }
    const entry = accountApiKeyEntries(credentials).find((item) => item.fingerprint === row.key_fingerprint)
    if (!entry) {
      continue
    }
    output.push({
      accountId: row.account_id,
      accountName: row.account_name,
      keyFingerprint: row.key_fingerprint,
      keyIndex: Number.isInteger(row.key_index) ? row.key_index : entry.index,
      apiKey: entry.key,
      status: row.status,
      nextProbeAt: row.next_probe_at ?? undefined
    })
    if (output.length >= normalizedLimit) {
      break
    }
  }
  return output
}

export function loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds: string[]): Map<string, AccountApiKeyRuntimeSummary> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeSummary>()
  const rows = accountApiKeyRuntimeSummaryRows(ids)
  const statesByAccountId = loadAccountApiKeyRuntimeStatesByAccountIds(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeSummariesFromRows(rows, statesByAccountId)
}

export async function loadAccountApiKeyRuntimeSummariesByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountApiKeyRuntimeSummary>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeSummary>()
  const rows = await accountApiKeyRuntimeSummaryRowsAsync(ids)
  const statesByAccountId = await loadAccountApiKeyRuntimeStatesByAccountIdsAsync(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeSummariesFromRows(rows, statesByAccountId)
}

function accountApiKeyRuntimeSummariesFromRows(
  rows: AccountApiKeyRuntimeSummarySourceRow[],
  statesByAccountId: Map<string, AccountApiKeyRuntimeSelectionState[]>
): Map<string, AccountApiKeyRuntimeSummary> {
  const output = new Map<string, AccountApiKeyRuntimeSummary>()
  for (const row of rows) {
    let credentials: Record<string, unknown>
    try {
      credentials = decryptJson<Record<string, unknown>>(row.credentialsEncrypted)
    } catch {
      continue
    }
    if (!isAccountApiKeyPoolIsolationEnabled({
      providerCode: row.providerCode,
      protocolCode: row.protocolCode,
      protocolVersion: row.protocolVersion,
      type: row.type,
      credentials
    })) {
      continue
    }
    const entries = accountApiKeyEntries(credentials)
    if (entries.length < 2) {
      continue
    }
    const statesByFingerprint = new Map(
      (statesByAccountId.get(row.sourceAccountId) ?? []).map((state) => [state.keyFingerprint, state])
    )
    const summary: AccountApiKeyRuntimeSummary = {
      total: entries.length,
      active: 0,
      temporaryUnavailable: 0,
      rateLimited: 0,
      error: 0,
      disabled: 0,
      unavailable: 0,
      allUnavailable: false
    }
    for (const entry of entries) {
      const state = statesByFingerprint.get(entry.fingerprint)
      if (!state || state.status === 'active') {
        summary.active += 1
        continue
      }
      summary.unavailable += 1
      if (state.status === 'temporary_unavailable') summary.temporaryUnavailable += 1
      if (state.status === 'rate_limited') summary.rateLimited += 1
      if (state.status === 'error') summary.error += 1
      if (state.status === 'disabled') summary.disabled += 1
      if (state.nextProbeAt && (!summary.nextProbeAt || state.nextProbeAt < summary.nextProbeAt)) {
        summary.nextProbeAt = state.nextProbeAt
      }
    }
    summary.allUnavailable = summary.total > 0 && summary.active === 0
    output.set(row.viewAccountId, summary)
  }
  return output
}

export function loadAccountApiKeyRuntimeDetailsByAccountIds(accountIds: string[]): Map<string, AccountApiKeyRuntimeDetail[]> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeDetail[]>()
  const rows = accountApiKeyRuntimeSummaryRows(ids)
  const statesByAccountId = loadAccountApiKeyRuntimeDetailRowsByAccountIds(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeDetailsFromRows(rows, statesByAccountId)
}

export async function loadAccountApiKeyRuntimeDetailsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountApiKeyRuntimeDetail[]>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadAccountApiKeyRuntimeDetailsByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeDetail[]>()
  const rows = await accountApiKeyRuntimeSummaryRowsAsync(ids)
  const statesByAccountId = await loadAccountApiKeyRuntimeDetailRowsByAccountIdsAsync(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeDetailsFromRows(rows, statesByAccountId)
}

function accountApiKeyRuntimeDetailsFromRows(
  rows: AccountApiKeyRuntimeSummarySourceRow[],
  statesByAccountId: Map<string, AccountApiKeyRuntimeDetailRow[]>
): Map<string, AccountApiKeyRuntimeDetail[]> {
  const output = new Map<string, AccountApiKeyRuntimeDetail[]>()
  for (const row of rows) {
    let credentials: Record<string, unknown>
    try {
      credentials = decryptJson<Record<string, unknown>>(row.credentialsEncrypted)
    } catch {
      continue
    }
    if (!isAccountApiKeyPoolIsolationEnabled({
      providerCode: row.providerCode,
      protocolCode: row.protocolCode,
      protocolVersion: row.protocolVersion,
      type: row.type,
      credentials
    })) {
      continue
    }
    const entries = accountApiKeyEntries(credentials)
    if (entries.length < 2) {
      continue
    }
    const statesByFingerprint = new Map(
      (statesByAccountId.get(row.sourceAccountId) ?? []).map((state) => [state.key_fingerprint, state])
    )
    output.set(row.viewAccountId, entries.map((entry) => {
      const state = statesByFingerprint.get(entry.fingerprint)
      return {
        keyIndex: entry.index,
        keyFingerprintPrefix: entry.fingerprint.slice(0, 12),
        keySuffix: keySuffixForRuntimeDisplay(entry.key),
        weight: entry.weight,
        status: state?.status ?? 'active',
        failureCount: positiveInteger(state?.failure_count),
        consecutiveFailures: positiveInteger(state?.consecutive_failures),
        successCount: positiveInteger(state?.success_count),
        cooldownUntil: state?.cooldown_until ?? undefined,
        nextProbeAt: state?.next_probe_at ?? undefined,
        lastAttemptAt: state?.last_attempt_at ?? undefined,
        lastSuccessAt: state?.last_success_at ?? undefined,
        lastFailureAt: state?.last_failure_at ?? undefined,
        lastErrorCode: state?.last_error_code ?? undefined,
        lastErrorMessage: runtimeErrorMessageForResponse(state?.last_error_message)
      }
    }))
  }
  return output
}

export function recordAccountApiKeyRuntimeFailure(input: AccountApiKeyRuntimeFailureInput): AccountApiKeyRuntimeWriteResult {
  const target = accountApiKeyRuntimeTarget(input.account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const database = getBusinessDatabase()
  const existing = database
    .prepare(`
      SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, probe_backoff_seconds
      FROM account_api_key_runtime_states
      WHERE account_id = ?
        AND key_fingerprint = ?
      LIMIT 1
    `)
    .get(target.accountId, target.keyFingerprint) as unknown as AccountApiKeyRuntimeRow | undefined
  if (existing?.status === 'disabled') {
    return { changed: false, skippedReason: 'key_disabled' }
  }

  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextBackoffSeconds = nextProbeBackoffSeconds(existing?.probe_backoff_seconds)
  const status = normalizeFailureStatus(input.status)
  const nextProbeAt = input.cooldownUntil && status === 'rate_limited'
    ? input.cooldownUntil
    : new Date(Date.now() + nextBackoffSeconds * 1000).toISOString()
  const errorCode = input.errorCode ?? (typeof input.statusCode === 'number' ? `http_${input.statusCode}` : null)
  const errorMessage = sanitizeRuntimeErrorMessage(input.errorMessage ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败'))

  const result = existing
    ? database
        .prepare(`
          UPDATE account_api_key_runtime_states
          SET system_account_id = ?,
              key_index = ?,
              status = ?,
              failure_count = failure_count + 1,
              consecutive_failures = consecutive_failures + 1,
              cooldown_until = ?,
              next_probe_at = ?,
              probe_backoff_seconds = ?,
              recovery_started_at = COALESCE(recovery_started_at, ?),
              last_attempt_at = ?,
              last_failure_at = ?,
              last_error_code = ?,
              last_error_message = ?,
              updated_at = ?
          WHERE account_id = ?
            AND key_fingerprint = ?
            AND status <> 'disabled'
            AND (last_attempt_at IS NULL OR last_attempt_at <= ?)
        `)
        .run(
          target.systemAccountId,
          target.keyIndex,
          status,
          nextProbeAt,
          nextProbeAt,
          nextBackoffSeconds,
          now,
          observedAt,
          observedAt,
          errorCode,
          errorMessage,
          now,
          target.accountId,
          target.keyFingerprint,
          observedAt
        )
    : database
        .prepare(`
          INSERT INTO account_api_key_runtime_states (
            id, system_account_id, account_id, key_fingerprint, key_index,
            status, failure_count, consecutive_failures, success_count,
            cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
            last_attempt_at, last_failure_at, last_error_code, last_error_message,
            created_at, updated_at
          )
          VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        `)
        .run(
          newId('account_api_key_runtime_state'),
          target.systemAccountId,
          target.accountId,
          target.keyFingerprint,
          target.keyIndex,
          status,
          nextProbeAt,
          nextProbeAt,
          nextBackoffSeconds,
          now,
          observedAt,
          observedAt,
          errorCode,
          errorMessage,
          now,
          now
        )

  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    markRuntimeStateChanged(target.accountId)
  }
  return { changed }
}

export async function recordAccountApiKeyRuntimeFailureAsync(input: AccountApiKeyRuntimeFailureInput): Promise<AccountApiKeyRuntimeWriteResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountApiKeyRuntimeFailure(input)
  }
  const target = accountApiKeyRuntimeTarget(input.account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const existing = await client.one<AccountApiKeyRuntimeRow>(`
    SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, probe_backoff_seconds
    FROM ${accountApiKeyRuntimeStatesTable(client)}
    WHERE account_id = ?
      AND key_fingerprint = ?
    LIMIT 1
  `, [target.accountId, target.keyFingerprint])
  if (existing?.status === 'disabled') {
    return { changed: false, skippedReason: 'key_disabled' }
  }

  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextBackoffSeconds = nextProbeBackoffSeconds(existing?.probe_backoff_seconds)
  const status = normalizeFailureStatus(input.status)
  const nextProbeAt = input.cooldownUntil && status === 'rate_limited'
    ? input.cooldownUntil
    : new Date(Date.now() + nextBackoffSeconds * 1000).toISOString()
  const errorCode = input.errorCode ?? (typeof input.statusCode === 'number' ? `http_${input.statusCode}` : null)
  const errorMessage = sanitizeRuntimeErrorMessage(input.errorMessage ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败'))
  const table = accountApiKeyRuntimeStatesTable(client)

  const result = await client.execute(`
    INSERT INTO ${table} AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_failure_at, last_error_code, last_error_message,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = excluded.status,
      failure_count = current_state.failure_count + 1,
      consecutive_failures = current_state.consecutive_failures + 1,
      cooldown_until = excluded.cooldown_until,
      next_probe_at = excluded.next_probe_at,
      probe_backoff_seconds = excluded.probe_backoff_seconds,
      recovery_started_at = COALESCE(current_state.recovery_started_at, excluded.recovery_started_at),
      last_attempt_at = excluded.last_attempt_at,
      last_failure_at = excluded.last_failure_at,
      last_error_code = excluded.last_error_code,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
    WHERE current_state.status <> 'disabled'
      AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at <= excluded.last_attempt_at)
  `, [
    newId('account_api_key_runtime_state'),
    target.systemAccountId,
    target.accountId,
    target.keyFingerprint,
    target.keyIndex,
    status,
    nextProbeAt,
    nextProbeAt,
    nextBackoffSeconds,
    now,
    observedAt,
    observedAt,
    errorCode,
    errorMessage,
    now,
    now
  ])

  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await markRuntimeStateChangedAsync(client, target.accountId)
  }
  return { changed }
}

export function recordAccountApiKeyRuntimeSuccess(account: OpenAIAccountSecret, input: { observedAt?: string } = {}): AccountApiKeyRuntimeWriteResult {
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const result = getBusinessDatabase()
    .prepare(`
      INSERT INTO account_api_key_runtime_states (
        id, system_account_id, account_id, key_fingerprint, key_index,
        status, failure_count, consecutive_failures, success_count,
        cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
        last_attempt_at, last_success_at, last_error_code, last_error_message,
        created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, ?, ?)
      ON CONFLICT(account_id, key_fingerprint) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        key_index = excluded.key_index,
        status = 'active',
        consecutive_failures = 0,
        success_count = account_api_key_runtime_states.success_count + 1,
        cooldown_until = NULL,
        next_probe_at = NULL,
        probe_backoff_seconds = 0,
        recovery_started_at = NULL,
        last_attempt_at = excluded.last_attempt_at,
        last_success_at = excluded.last_success_at,
        last_error_code = NULL,
        last_error_message = NULL,
        updated_at = excluded.updated_at
      WHERE account_api_key_runtime_states.status <> 'disabled'
        AND (account_api_key_runtime_states.last_attempt_at IS NULL OR account_api_key_runtime_states.last_attempt_at <= excluded.last_attempt_at)
    `)
    .run(
      newId('account_api_key_runtime_state'),
      target.systemAccountId,
      target.accountId,
      target.keyFingerprint,
      target.keyIndex,
      observedAt,
      observedAt,
      now,
      now
    )
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    markRuntimeStateChanged(target.accountId)
  }
  return { changed }
}

export async function recordAccountApiKeyRuntimeSuccessAsync(account: OpenAIAccountSecret, input: { observedAt?: string } = {}): Promise<AccountApiKeyRuntimeWriteResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountApiKeyRuntimeSuccess(account, input)
  }
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const result = await client.execute(`
    INSERT INTO ${accountApiKeyRuntimeStatesTable(client)} AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_error_code, last_error_message,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = 'active',
      consecutive_failures = 0,
      success_count = current_state.success_count + 1,
      cooldown_until = NULL,
      next_probe_at = NULL,
      probe_backoff_seconds = 0,
      recovery_started_at = NULL,
      last_attempt_at = excluded.last_attempt_at,
      last_success_at = excluded.last_success_at,
      last_error_code = NULL,
      last_error_message = NULL,
      updated_at = excluded.updated_at
    WHERE current_state.status <> 'disabled'
      AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at <= excluded.last_attempt_at)
  `, [
    newId('account_api_key_runtime_state'),
    target.systemAccountId,
    target.accountId,
    target.keyFingerprint,
    target.keyIndex,
    observedAt,
    observedAt,
    now,
    now
  ])
  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await markRuntimeStateChangedAsync(client, target.accountId)
  }
  return { changed }
}

function normalizeObservedAt(value: string | undefined, fallback: string): string {
  if (!value) return fallback
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time).toISOString() : fallback
}

function accountApiKeyRuntimeTarget(account: OpenAIAccountSecret): AccountApiKeyRuntimeTarget | undefined {
  if (account.apiKeyRuntimeStateDisabled) return undefined
  const keyFingerprint = account.selectedApiKeyFingerprint?.trim()
  if (!keyFingerprint) return undefined
  const apiKeys = account.apiKeys ?? []
  if (!isAccountApiKeyPoolIsolationEnabled({
    providerCode: account.providerCode,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    apiKeys,
    credentials: {
      ...account.credentials,
      api_key: account.apiKey,
      ...(apiKeys.length ? { api_keys: apiKeys } : {})
    }
  })) {
    return undefined
  }
  const accountId = account.credentialSourceAccountId ?? account.id
  const systemAccountId = account.accountOwnerSystemAccountId || account.systemAccountId
  if (!accountId || !systemAccountId) return undefined
  return {
    systemAccountId,
    accountId,
    keyFingerprint,
    keyIndex: Number.isInteger(account.selectedApiKeyIndex) ? account.selectedApiKeyIndex! : 0
  }
}

function accountApiKeyRuntimeSummaryRows(accountIds: string[]): Array<{
  viewAccountId: string
  sourceAccountId: string
  providerCode: string
  protocolCode: string
  protocolVersion: string
  type: string
  credentialsEncrypted: string
}> {
  const rows: Array<{
    view_account_id: string
    source_account_id: string
    provider_code: string
    protocol_code: string
    protocol_version: string
    type: string
    credentials_encrypted: string
  }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...database
      .prepare(`
        SELECT accounts.id AS view_account_id,
          COALESCE(source_accounts.id, accounts.id) AS source_account_id,
          COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
          COALESCE(source_accounts.protocol_code, accounts.protocol_code) AS protocol_code,
          COALESCE(source_accounts.protocol_version, accounts.protocol_version) AS protocol_version,
          COALESCE(source_accounts.type, accounts.type) AS type,
          COALESCE(source_accounts.credentials_encrypted, accounts.credentials_encrypted) AS credentials_encrypted
        FROM accounts
        LEFT JOIN accounts source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
        WHERE accounts.id IN (${sqlPlaceholders(chunk.length)})
          AND accounts.deleted_at IS NULL
          AND (source_accounts.id IS NULL OR source_accounts.deleted_at IS NULL)
      `)
      .all(...chunk) as unknown as Array<{
        view_account_id: string
        source_account_id: string
        provider_code: string
        protocol_code: string
        protocol_version: string
        type: string
        credentials_encrypted: string
      }>)
  }
  return rows
    .filter((row) => row.view_account_id && row.source_account_id && row.credentials_encrypted)
    .map((row) => ({
      viewAccountId: row.view_account_id,
      sourceAccountId: row.source_account_id,
      providerCode: row.provider_code,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type,
      credentialsEncrypted: row.credentials_encrypted
    }))
}

async function accountApiKeyRuntimeSummaryRowsAsync(accountIds: string[]): Promise<AccountApiKeyRuntimeSummarySourceRow[]> {
  const rows: Array<{
    view_account_id: string
    source_account_id: string
    provider_code: string
    protocol_code: string
    protocol_version: string
    type: string
    credentials_encrypted: string
  }> = []
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...await client.query<{
      view_account_id: string
      source_account_id: string
      provider_code: string
      protocol_code: string
      protocol_version: string
      type: string
      credentials_encrypted: string
    }>(`
      SELECT accounts.id AS view_account_id,
        COALESCE(source_accounts.id, accounts.id) AS source_account_id,
        COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
        COALESCE(source_accounts.protocol_code, accounts.protocol_code) AS protocol_code,
        COALESCE(source_accounts.protocol_version, accounts.protocol_version) AS protocol_version,
        COALESCE(source_accounts.type, accounts.type) AS type,
        COALESCE(source_accounts.credentials_encrypted, accounts.credentials_encrypted) AS credentials_encrypted
      FROM ${accountApiKeyRuntimeBusinessTable(client, 'accounts')} accounts
      LEFT JOIN ${accountApiKeyRuntimeBusinessTable(client, 'accounts')} source_accounts ON source_accounts.id = accounts.authorization_instance_source_account_id
      WHERE accounts.id IN (${chunk.map(() => '?').join(', ')})
        AND accounts.deleted_at IS NULL
        AND (source_accounts.id IS NULL OR source_accounts.deleted_at IS NULL)
    `, chunk))
  }
  return rows
    .filter((row) => row.view_account_id && row.source_account_id && row.credentials_encrypted)
    .map((row) => ({
      viewAccountId: row.view_account_id,
      sourceAccountId: row.source_account_id,
      providerCode: row.provider_code,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type,
      credentialsEncrypted: row.credentials_encrypted
    }))
}

function loadAccountApiKeyRuntimeDetailRowsByAccountIds(accountIds: string[]): Map<string, AccountApiKeyRuntimeDetailRow[]> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, AccountApiKeyRuntimeDetailRow[]>()
  if (!ids.length) return output
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = database
      .prepare(`
        SELECT account_id, key_fingerprint, key_index, status, failure_count, consecutive_failures,
          success_count, cooldown_until, next_probe_at, last_attempt_at, last_success_at, last_failure_at,
          last_error_code, last_error_message
        FROM account_api_key_runtime_states
        WHERE account_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as AccountApiKeyRuntimeDetailRow[]
    for (const row of rows) {
      const items = output.get(row.account_id) ?? []
      items.push(row)
      output.set(row.account_id, items)
    }
  }
  return output
}

async function loadAccountApiKeyRuntimeDetailRowsByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountApiKeyRuntimeDetailRow[]>> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const output = new Map<string, AccountApiKeyRuntimeDetailRow[]>()
  if (!ids.length) return output
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = await client.query<AccountApiKeyRuntimeDetailRow>(`
      SELECT account_id, key_fingerprint, key_index, status, failure_count, consecutive_failures,
        success_count, cooldown_until, next_probe_at, last_attempt_at, last_success_at, last_failure_at,
        last_error_code, last_error_message
      FROM ${accountApiKeyRuntimeStatesTable(client)}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
    for (const row of rows) {
      const items = output.get(row.account_id) ?? []
      items.push(row)
      output.set(row.account_id, items)
    }
  }
  return output
}

async function getAccountApiKeyRuntimeStateDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountApiKeyRuntimeStatesTable(client: DatabaseClient): string {
  return accountApiKeyRuntimeBusinessTable(client, 'account_api_key_runtime_states')
}

function accountApiKeyRuntimeBusinessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function markRuntimeStateChanged(sourceAccountId: string): void {
  const affectedAccountIds = accountIdsAffectedBySourceAccount(sourceAccountId)
  markGroupAccountStatsDirtyByAccountIds(affectedAccountIds.length ? affectedAccountIds : [sourceAccountId], 'account_api_key_runtime')
  notifyGatewayRuntimeCacheInvalidation('account_api_key_runtime')
}

async function markRuntimeStateChangedAsync(client: DatabaseClient, sourceAccountId: string): Promise<void> {
  const affectedAccountIds = await accountIdsAffectedBySourceAccountAsync(client, sourceAccountId)
  await markGroupAccountStatsDirtyByAccountIdsAsync(client, affectedAccountIds.length ? affectedAccountIds : [sourceAccountId], 'account_api_key_runtime')
  notifyGatewayRuntimeCacheInvalidation('account_api_key_runtime')
}

function accountIdsAffectedBySourceAccount(sourceAccountId: string): string[] {
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT id
      FROM accounts
      WHERE id = ?
         OR authorization_instance_source_account_id = ?
    `)
    .all(sourceAccountId, sourceAccountId) as unknown as Array<{ id: string }>
  return rows.map((row) => row.id).filter(Boolean)
}

async function accountIdsAffectedBySourceAccountAsync(client: DatabaseClient, sourceAccountId: string): Promise<string[]> {
  const rows = await client.query<{ id: string }>(`
    SELECT id
    FROM ${accountApiKeyRuntimeBusinessTable(client, 'accounts')}
    WHERE id = ?
       OR authorization_instance_source_account_id = ?
  `, [sourceAccountId, sourceAccountId])
  return rows.map((row) => row.id).filter(Boolean)
}

async function markGroupAccountStatsDirtyByAccountIdsAsync(
  client: DatabaseClient,
  accountIds: Array<string | null | undefined>,
  reason = 'account_write'
): Promise<void> {
  const ids = [...new Set(accountIds.map((id) => id?.trim()).filter((id): id is string => Boolean(id)))]
  if (!ids.length) return
  const groupIds: string[] = []
  for (const chunk of chunkValues(ids, 900)) {
    const rows = await client.query<{ group_id: string }>(`
      SELECT DISTINCT group_id
      FROM ${accountApiKeyRuntimeBusinessTable(client, 'group_accounts')}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
    groupIds.push(...rows.map((row) => row.group_id))
  }
  const uniqueGroupIds = [...new Set(groupIds.map((id) => id?.trim()).filter((id): id is string => Boolean(id)))]
  if (!uniqueGroupIds.length) return
  const updatedAt = nowIso()
  for (const groupId of uniqueGroupIds) {
    await client.execute(`
      INSERT INTO ${accountApiKeyRuntimeBusinessTable(client, 'group_account_stats_dirty')} (group_id, reason, updated_at)
      VALUES (?, ?, ?)
      ON CONFLICT (group_id) DO UPDATE SET
        reason = excluded.reason,
        updated_at = excluded.updated_at
    `, [groupId, reason, updatedAt])
  }
}

function normalizeFailureStatus(status: AccountApiKeyRuntimeFailureInput['status']): Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'> {
  if (status === 'rate_limited' || status === 'error') return status
  return 'temporary_unavailable'
}

function nextProbeBackoffSeconds(previous: number | null | undefined): number {
  const value = typeof previous === 'number' && Number.isFinite(previous) && previous > 0
    ? Math.trunc(previous)
    : 0
  return value > 0
    ? Math.min(maxProbeBackoffSeconds, value * 2)
    : initialProbeBackoffSeconds
}

function sanitizeRuntimeErrorMessage(value: string): string {
  return value.replace(/\s+/g, ' ').trim().slice(0, 1000) || '上游请求失败'
}

function runtimeErrorMessageForResponse(value: string | null | undefined): string | undefined {
  const text = value?.replace(/\s+/g, ' ').trim()
  return text ? text.slice(0, 240) : undefined
}

function keySuffixForRuntimeDisplay(key: string): string | undefined {
  const normalized = key.trim()
  return normalized ? normalized.slice(-4) : undefined
}

function positiveInteger(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0
}
