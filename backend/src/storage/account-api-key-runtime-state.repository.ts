import type { AccountApiKeyRuntimeSelectionState, AccountApiKeyRuntimeStatus } from './account-api-key-rotation.js'
import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled } from './account-api-key-rotation.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { runtimeConfig } from '../config/runtime.js'
import { canonicalizeRfc3339Instant, requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'
import type { OpenAIAccountSecret } from './openai-account-selector.types.js'
import type { AccountApiKeyRuntimeDetail } from '../domain/types.js'
import {
  API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE,
  API_KEY_QUOTA_GENERIC_ERROR_CODE,
  apiKeyQuotaRecoveryModeFromErrorCode,
  type ApiKeyQuotaRecoveryMode
} from '../modules/gateway/policy/api-key-quota-recovery.js'

export interface AccountApiKeyRuntimeFailureInput {
  account: OpenAIAccountSecret
  status?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  statusCode?: number
  errorCode?: string
  errorMessage?: string
  traceId?: string
  cooldownUntil?: string
  quotaRecoveryMode?: ApiKeyQuotaRecoveryMode
  breakQuotaRecoveryWindow?: boolean
  observedAt?: string
  expectedStatus?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  expectedNextProbeAt?: string
  expectedStateUpdatedAt?: string
  expectedAccountConfigRevision?: number
  expectedProbeClaimToken?: string
}

export interface AccountApiKeyRuntimeWriteResult {
  changed: boolean
  skippedReason?: string
}

export interface AccountApiKeyRuntimeProbeDeferInput {
  account: OpenAIAccountSecret
  expectedStatus: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  expectedNextProbeAt?: string
  delaySeconds: number
  observedAt?: string
  expectedStateUpdatedAt?: string
  expectedAccountConfigRevision?: number
  expectedProbeClaimToken?: string
  breakQuotaRecoveryWindow?: boolean
}

export interface AccountApiKeyRuntimeSuccessInput {
  observedAt?: string
  expectedStatus?: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  expectedNextProbeAt?: string
  expectedStateUpdatedAt?: string
  expectedAccountConfigRevision?: number
  expectedProbeClaimToken?: string
}

export interface AccountApiKeyRuntimeProbeCandidate {
  accountId: string
  accountName: string
  keyFingerprint: string
  keyIndex: number
  apiKey: string
  status: Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>
  nextProbeAt: string
  stateUpdatedAt: string
  accountConfigRevision: number
  probeClaimToken: string
  probeClaimedUntil: string
  recoveryStartedAt?: string
  lastErrorCode?: string
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
  lastFailureAt?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  lastTraceId?: string
}

export interface AccountApiKeyRuntimeRevalidateResult {
  changed: number
  eligible: boolean
  reason?: 'account_not_found' | 'account_not_active' | 'account_unschedulable' | 'config_revision_conflict' | 'not_supported' | 'no_revalidatable_key'
}

interface AccountApiKeyRuntimeRow {
  account_id: string
  key_fingerprint: string
  key_index: number
  status: AccountApiKeyRuntimeStatus
  cooldown_until: string | null
  next_probe_at: string | null
  probe_backoff_seconds: number | null
  recovery_started_at: string | null
  last_error_code: string | null
  updated_at: string
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
  last_trace_id: string | null
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
  next_probe_at: string
  updated_at: string
  recovery_started_at: string | null
  last_error_code: string | null
  account_name: string
  provider_code: string
  protocol_code: string
  protocol_version: string
  type: string
  credentials_encrypted: string
  config_revision: number
  probe_claim_token: string | null
  probe_claimed_until: string | null
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
const probeClaimLeaseSeconds = 10 * 60
const probeCandidateScanLimit = runtimeConfig.background.accountApiKeyProbeCandidateScanLimit
const businessSchemaName = 'juhe_business'
const accountApiKeyRuntimeProbeCandidateStatuses = ['unverified', 'temporary_unavailable', 'rate_limited'] as const
const accountApiKeyRuntimeProbeCandidateStatusSql = accountApiKeyRuntimeProbeCandidateStatuses.map((status) => `'${status}'`).join(', ')

function isAccountApiKeyRuntimeProbeCandidateStatus(
  status: AccountApiKeyRuntimeStatus
): status is typeof accountApiKeyRuntimeProbeCandidateStatuses[number] {
  return (accountApiKeyRuntimeProbeCandidateStatuses as readonly AccountApiKeyRuntimeStatus[]).includes(status)
}

type AccountApiKeyExpectedProbeStateInput = Pick<
  AccountApiKeyRuntimeSuccessInput,
  'expectedStatus' | 'expectedNextProbeAt' | 'expectedStateUpdatedAt' | 'expectedAccountConfigRevision' | 'expectedProbeClaimToken'
>

interface AccountApiKeyExpectedProbeStateFence {
  provided: boolean
  sql: string
  params: string[]
  invalidReason?: string
}

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

export async function loadAccountApiKeyRuntimeStatesForAccountInClient(
  client: DatabaseClient,
  accountId: string
): Promise<AccountApiKeyRuntimeSelectionState[]> {
  const rows = await client.query<{
    key_fingerprint: string
    key_index: number
    status: AccountApiKeyRuntimeStatus
    cooldown_until: string | null
    next_probe_at: string | null
  }>(`
    SELECT key_fingerprint, key_index, status, cooldown_until, next_probe_at
    FROM ${accountApiKeyRuntimeStatesTable(client)}
    WHERE account_id = ?
  `, [accountId])
  return rows.map((row) => ({
    keyFingerprint: row.key_fingerprint,
    status: row.status,
    keyIndex: Number.isInteger(row.key_index) ? row.key_index : undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    nextProbeAt: row.next_probe_at ?? undefined
  }))
}

/** Mark all probe-eligible non-active keys due immediately, without touching disabled keys or valid leases. */
export async function revalidateAccountApiKeyRuntimePoolAsync(input: {
  accountId: string
  expectedConfigRevision: number
}): Promise<AccountApiKeyRuntimeRevalidateResult> {
  const accountId = input.accountId.trim()
  if (!accountId || !Number.isSafeInteger(input.expectedConfigRevision) || input.expectedConfigRevision < 1) {
    throw new Error('重新验证 API Key 池参数无效')
  }
  const now = nowIso()
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const database = getBusinessDatabase()
    const accountRow = database.prepare(`SELECT provider_code, protocol_code, protocol_version, type, status, schedulable, config_revision, credentials_encrypted FROM accounts WHERE id = ? AND deleted_at IS NULL`).get(accountId) as { provider_code: string; protocol_code: string | null; protocol_version: string | null; type: string; status: string; schedulable: number; config_revision: number; credentials_encrypted: string } | undefined
    if (!accountRow) return { changed: 0, eligible: false, reason: 'account_not_found' }
    if (Number(accountRow.config_revision) !== input.expectedConfigRevision) return { changed: 0, eligible: false, reason: 'config_revision_conflict' }
    if (accountRow.status !== 'active') return { changed: 0, eligible: false, reason: 'account_not_active' }
    if (Number(accountRow.schedulable) !== 1) return { changed: 0, eligible: false, reason: 'account_unschedulable' }
    let credentials: Record<string, unknown>
    try { credentials = decryptJson<Record<string, unknown>>(accountRow.credentials_encrypted) } catch { credentials = {} }
    const eligible = isAccountApiKeyPoolIsolationEnabled({ providerCode: accountRow.provider_code, protocolCode: accountRow.protocol_code, protocolVersion: accountRow.protocol_version, type: accountRow.type, credentials })
    const keyFingerprints = accountApiKeyEntries(credentials).map((entry) => entry.fingerprint)
    if (!eligible || keyFingerprints.length < 2) return { changed: 0, eligible: false, reason: 'not_supported' }
    const result = database.prepare(`
      UPDATE account_api_key_runtime_states
      SET status = CASE WHEN status = 'error' THEN 'unverified' ELSE status END,
          recovery_started_at = NULL,
          cooldown_until = NULL,
          next_probe_at = ?, last_attempt_at = ?, updated_at = ?
      WHERE account_id = ?
        AND key_fingerprint IN (${sqlPlaceholders(keyFingerprints.length)})
        AND status NOT IN ('active', 'disabled')
        AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
        AND EXISTS (
          SELECT 1 FROM accounts
          WHERE accounts.id = account_api_key_runtime_states.account_id
            AND accounts.config_revision = ?
            AND accounts.status = 'active'
            AND accounts.schedulable = 1
            AND accounts.deleted_at IS NULL
        )
    `).run(now, now, now, accountId, ...keyFingerprints, now, input.expectedConfigRevision)
    const changed = Number(result.changes ?? 0)
    if (changed === 0) {
      const current = database.prepare(`SELECT status, schedulable, config_revision FROM accounts WHERE id = ? AND deleted_at IS NULL`).get(accountId) as { status: string; schedulable: number; config_revision: number } | undefined
      if (!current) return { changed: 0, eligible: false, reason: 'account_not_found' }
      if (Number(current.config_revision) !== input.expectedConfigRevision) return { changed: 0, eligible: false, reason: 'config_revision_conflict' }
      if (current.status !== 'active') return { changed: 0, eligible: false, reason: 'account_not_active' }
      if (Number(current.schedulable) !== 1) return { changed: 0, eligible: false, reason: 'account_unschedulable' }
      const candidate = database.prepare(`
        SELECT 1 FROM account_api_key_runtime_states
        WHERE account_id = ?
          AND key_fingerprint IN (${sqlPlaceholders(keyFingerprints.length)})
          AND status NOT IN ('active', 'disabled')
          AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
        LIMIT 1
      `).get(accountId, ...keyFingerprints, now)
      if (!candidate) return { changed: 0, eligible: false, reason: 'no_revalidatable_key' }
      const retry = database.prepare(`
        UPDATE account_api_key_runtime_states
        SET status = CASE WHEN status = 'error' THEN 'unverified' ELSE status END,
            recovery_started_at = NULL,
            cooldown_until = NULL,
            next_probe_at = ?, last_attempt_at = ?, updated_at = ?
        WHERE account_id = ?
          AND key_fingerprint IN (${sqlPlaceholders(keyFingerprints.length)})
          AND status NOT IN ('active', 'disabled')
          AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
          AND EXISTS (
            SELECT 1 FROM accounts
            WHERE accounts.id = account_api_key_runtime_states.account_id
              AND accounts.config_revision = ?
              AND accounts.status = 'active'
              AND accounts.schedulable = 1
              AND accounts.deleted_at IS NULL
          )
      `).run(now, now, now, accountId, ...keyFingerprints, now, input.expectedConfigRevision)
      const retried = Number(retry.changes ?? 0)
      if (retried <= 0) return { changed: 0, eligible: false, reason: 'no_revalidatable_key' }
      markRuntimeStateChanged(accountId)
      return { changed: retried, eligible: true }
    }
    if (changed > 0) markRuntimeStateChanged(accountId)
    return { changed, eligible: true }
  }
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const accountRow = await client.one<{ provider_code: string; protocol_code: string | null; protocol_version: string | null; type: string; status: string; schedulable: number; config_revision: number; credentials_encrypted: string }>(`SELECT provider_code, protocol_code, protocol_version, type, status, schedulable, config_revision, credentials_encrypted FROM ${accountApiKeyRuntimeBusinessTable(client, 'accounts')} WHERE id = ? AND deleted_at IS NULL`, [accountId])
  if (!accountRow) return { changed: 0, eligible: false, reason: 'account_not_found' }
  if (Number(accountRow.config_revision) !== input.expectedConfigRevision) return { changed: 0, eligible: false, reason: 'config_revision_conflict' }
  if (accountRow.status !== 'active') return { changed: 0, eligible: false, reason: 'account_not_active' }
  if (Number(accountRow.schedulable) !== 1) return { changed: 0, eligible: false, reason: 'account_unschedulable' }
  let credentials: Record<string, unknown>
  try { credentials = decryptJson<Record<string, unknown>>(accountRow.credentials_encrypted) } catch { credentials = {} }
  const eligible = isAccountApiKeyPoolIsolationEnabled({ providerCode: accountRow.provider_code, protocolCode: accountRow.protocol_code, protocolVersion: accountRow.protocol_version, type: accountRow.type, credentials })
  const keyFingerprints = accountApiKeyEntries(credentials).map((entry) => entry.fingerprint)
  if (!eligible || keyFingerprints.length < 2) return { changed: 0, eligible: false, reason: 'not_supported' }
  const statesTable = accountApiKeyRuntimeStatesTable(client)
  const accountsTable = accountApiKeyRuntimeBusinessTable(client, 'accounts')
  const result = await client.execute(`
    UPDATE ${statesTable} AS states
    SET status = CASE WHEN states.status = 'error' THEN 'unverified' ELSE states.status END,
        recovery_started_at = NULL,
        cooldown_until = NULL,
        next_probe_at = ?, last_attempt_at = ?, updated_at = ?
    WHERE states.account_id = ?
      AND states.key_fingerprint IN (${keyFingerprints.map(() => '?').join(', ')})
      AND states.status NOT IN ('active', 'disabled')
      AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
      AND EXISTS (
        SELECT 1 FROM ${accountsTable} accounts
        WHERE accounts.id = states.account_id
          AND accounts.config_revision = ?
          AND accounts.status = 'active'
          AND accounts.schedulable = 1
          AND accounts.deleted_at IS NULL
      )
  `, [now, now, now, accountId, ...keyFingerprints, now, input.expectedConfigRevision])
  const changed = Number(result.changes ?? 0)
  if (changed === 0) {
    const current = await client.one<{ status: string; schedulable: number; config_revision: number }>(`SELECT status, schedulable, config_revision FROM ${accountsTable} WHERE id = ? AND deleted_at IS NULL`, [accountId])
    if (!current) return { changed: 0, eligible: false, reason: 'account_not_found' }
    if (Number(current.config_revision) !== input.expectedConfigRevision) return { changed: 0, eligible: false, reason: 'config_revision_conflict' }
    if (current.status !== 'active') return { changed: 0, eligible: false, reason: 'account_not_active' }
    if (Number(current.schedulable) !== 1) return { changed: 0, eligible: false, reason: 'account_unschedulable' }
    const candidate = await client.one(`
      SELECT 1 FROM ${statesTable}
      WHERE account_id = ?
        AND key_fingerprint IN (${keyFingerprints.map(() => '?').join(', ')})
        AND status NOT IN ('active', 'disabled')
        AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
      LIMIT 1
    `, [accountId, ...keyFingerprints, now])
    if (!candidate) return { changed: 0, eligible: false, reason: 'no_revalidatable_key' }
    const retry = await client.execute(`
      UPDATE ${statesTable} AS states
      SET status = CASE WHEN states.status = 'error' THEN 'unverified' ELSE states.status END,
          recovery_started_at = NULL,
          cooldown_until = NULL,
          next_probe_at = ?, last_attempt_at = ?, updated_at = ?
      WHERE states.account_id = ?
        AND states.key_fingerprint IN (${keyFingerprints.map(() => '?').join(', ')})
        AND states.status NOT IN ('active', 'disabled')
        AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
        AND EXISTS (
          SELECT 1 FROM ${accountsTable} accounts
          WHERE accounts.id = states.account_id
            AND accounts.config_revision = ?
            AND accounts.status = 'active'
            AND accounts.schedulable = 1
            AND accounts.deleted_at IS NULL
        )
    `, [now, now, now, accountId, ...keyFingerprints, now, input.expectedConfigRevision])
    const retried = Number(retry.changes ?? 0)
    if (retried <= 0) return { changed: 0, eligible: false, reason: 'no_revalidatable_key' }
    await markRuntimeStateChangedAsync(client, accountId)
    return { changed: retried, eligible: true }
  }
  if (changed > 0) await markRuntimeStateChangedAsync(client, accountId)
  return { changed, eligible: true }
}

export async function initializeAddedAccountApiKeyRuntimeStatesInClient(
  client: DatabaseClient,
  input: {
    accountId: string
    systemAccountId: string
    providerCode: string
    protocolCode?: string
    protocolVersion?: string
    type: string
    currentCredentials: Record<string, unknown>
    nextCredentials: Record<string, unknown>
    now: string
  }
): Promise<boolean> {
  if (!isAccountApiKeyPoolIsolationEnabled({
    providerCode: input.providerCode,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    type: input.type,
    credentials: input.nextCredentials
  })) {
    return false
  }
  const currentFingerprints = new Set(accountApiKeyEntries(input.currentCredentials).map((entry) => entry.fingerprint))
  const nextEntries = accountApiKeyEntries(input.nextCredentials)
  let changed = false
  for (const entry of nextEntries) {
    if (currentFingerprints.has(entry.fingerprint)) {
      const result = await client.execute(`
        UPDATE ${accountApiKeyRuntimeStatesTable(client)}
        SET key_index = ?, updated_at = ?
        WHERE account_id = ?
          AND key_fingerprint = ?
          AND key_index <> ?
      `, [entry.index, input.now, input.accountId, entry.fingerprint, entry.index])
      changed = Number(result.changes ?? 0) > 0 || changed
      continue
    }
    await client.execute(`
      INSERT INTO ${accountApiKeyRuntimeStatesTable(client)} (
        id, system_account_id, account_id, key_fingerprint, key_index,
        status, failure_count, consecutive_failures, success_count,
        cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
        last_attempt_at, last_success_at, last_failure_at, last_error_code,
        last_error_message, last_trace_id, last_probe_at,
        probe_claim_token, probe_claimed_until, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, 'unverified', 0, 0, 0, NULL, ?, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)
      ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        key_index = excluded.key_index,
        status = 'unverified',
        failure_count = 0,
        consecutive_failures = 0,
        success_count = 0,
        cooldown_until = NULL,
        next_probe_at = excluded.next_probe_at,
        probe_backoff_seconds = 0,
        recovery_started_at = NULL,
        last_attempt_at = NULL,
        last_success_at = NULL,
        last_failure_at = NULL,
        last_error_code = NULL,
        last_error_message = NULL,
        last_trace_id = NULL,
        last_probe_at = NULL,
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = excluded.updated_at
    `, [
      newId('account_api_key_runtime_state'),
      input.systemAccountId,
      input.accountId,
      entry.fingerprint,
      entry.index,
      input.now,
      input.now,
      input.now
    ])
    changed = true
  }
  return changed
}

export function initializeAddedAccountApiKeyRuntimeStates(input: {
  accountId: string
  systemAccountId: string
  providerCode: string
  protocolCode?: string
  protocolVersion?: string
  type: string
  currentCredentials: Record<string, unknown>
  nextCredentials: Record<string, unknown>
  now: string
}): boolean {
  if (!isAccountApiKeyPoolIsolationEnabled({
    providerCode: input.providerCode,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    type: input.type,
    credentials: input.nextCredentials
  })) {
    return false
  }
  const currentFingerprints = new Set(accountApiKeyEntries(input.currentCredentials).map((entry) => entry.fingerprint))
  const nextEntries = accountApiKeyEntries(input.nextCredentials)
  let changed = false
  const statement = getBusinessDatabase().prepare(`
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_failure_at, last_error_code,
      last_error_message, last_trace_id, last_probe_at,
      probe_claim_token, probe_claimed_until, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 'unverified', 0, 0, 0, NULL, ?, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = 'unverified',
      failure_count = 0,
      consecutive_failures = 0,
      success_count = 0,
      cooldown_until = NULL,
      next_probe_at = excluded.next_probe_at,
      probe_backoff_seconds = 0,
      recovery_started_at = NULL,
      last_attempt_at = NULL,
      last_success_at = NULL,
      last_failure_at = NULL,
      last_error_code = NULL,
      last_error_message = NULL,
      last_trace_id = NULL,
      last_probe_at = NULL,
      probe_claim_token = NULL,
      probe_claimed_until = NULL,
      updated_at = excluded.updated_at
  `)
  const updateIndexStatement = getBusinessDatabase().prepare(`
    UPDATE account_api_key_runtime_states
    SET key_index = ?, updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint = ?
      AND key_index <> ?
  `)
  for (const entry of nextEntries) {
    if (currentFingerprints.has(entry.fingerprint)) {
      const result = updateIndexStatement.run(entry.index, input.now, input.accountId, entry.fingerprint, entry.index)
      changed = Number(result.changes ?? 0) > 0 || changed
      continue
    }
    statement.run(
      newId('account_api_key_runtime_state'),
      input.systemAccountId,
      input.accountId,
      entry.fingerprint,
      entry.index,
      input.now,
      input.now,
      input.now
    )
    changed = true
  }
  return changed
}

export function listAccountApiKeyRuntimeStatesDueForProbe(limit = 20): AccountApiKeyRuntimeProbeCandidate[] {
  const normalizedLimit = Math.max(1, Math.min(100, Math.trunc(limit)))
  const now = nowIso()
  const database = getBusinessDatabase()
  const rows = database
    .prepare(`
      SELECT states.account_id, states.key_fingerprint, states.key_index, states.status, states.next_probe_at,
        states.updated_at, states.recovery_started_at, states.last_error_code,
        accounts.name AS account_name, accounts.provider_code, accounts.protocol_code, accounts.protocol_version,
        accounts.type, accounts.credentials_encrypted, accounts.config_revision,
        states.probe_claim_token, states.probe_claimed_until
      FROM account_api_key_runtime_states states
      JOIN accounts ON accounts.id = states.account_id
        WHERE states.status IN (${accountApiKeyRuntimeProbeCandidateStatusSql})
        AND states.next_probe_at IS NOT NULL
        AND states.next_probe_at <= ?
        AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
      ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
      LIMIT ?
    `)
    .all(now, now, now, probeCandidateScanLimit) as unknown as AccountApiKeyRuntimeProbeRow[]
  const candidates = accountApiKeyRuntimeProbeCandidatesFromRows(rows, probeCandidateScanLimit)
  return claimAccountApiKeyRuntimeProbeCandidatesSync(database, candidates, normalizedLimit, now)
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
      states.updated_at, states.recovery_started_at, states.last_error_code,
      accounts.name AS account_name, accounts.provider_code, accounts.protocol_code, accounts.protocol_version,
      accounts.type, accounts.credentials_encrypted, accounts.config_revision,
      states.probe_claim_token, states.probe_claimed_until
    FROM ${accountApiKeyRuntimeStatesTable(client)} states
    JOIN ${accountApiKeyRuntimeBusinessTable(client, 'accounts')} accounts ON accounts.id = states.account_id
    WHERE states.status IN (${accountApiKeyRuntimeProbeCandidateStatusSql})
      AND states.next_probe_at IS NOT NULL
      AND states.next_probe_at <= ?
      AND (states.probe_claimed_until IS NULL OR states.probe_claimed_until <= ?)
      AND accounts.deleted_at IS NULL
      AND accounts.status = 'active'
      AND accounts.schedulable = 1
      AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > ?)
    ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
    LIMIT ?
  `, [now, now, now, probeCandidateScanLimit])
  const candidates = accountApiKeyRuntimeProbeCandidatesFromRows(rows, probeCandidateScanLimit)
  return await claimAccountApiKeyRuntimeProbeCandidatesAsync(client, candidates, normalizedLimit, now)
}

function accountApiKeyRuntimeProbeCandidatesFromRows(rows: AccountApiKeyRuntimeProbeRow[], limit: number): AccountApiKeyRuntimeProbeCandidate[] {
  const normalizedLimit = Math.max(1, Math.min(probeCandidateScanLimit, Math.trunc(limit)))
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
    if (!Number.isSafeInteger(row.config_revision) || row.config_revision < 1) {
      continue
    }
    output.push({
      accountId: row.account_id,
      accountName: row.account_name,
      keyFingerprint: row.key_fingerprint,
      keyIndex: Number.isInteger(row.key_index) ? row.key_index : entry.index,
      apiKey: entry.key,
      status: row.status,
      nextProbeAt: row.next_probe_at,
      stateUpdatedAt: row.updated_at,
      accountConfigRevision: row.config_revision,
      probeClaimToken: row.probe_claim_token ?? '',
      probeClaimedUntil: row.probe_claimed_until ?? '',
      recoveryStartedAt: row.recovery_started_at ?? undefined,
      lastErrorCode: row.last_error_code ?? undefined
    })
    if (output.length >= normalizedLimit) {
      break
    }
  }
  return output
}

function claimAccountApiKeyRuntimeProbeCandidatesSync(
  database: ReturnType<typeof getBusinessDatabase>,
  candidates: AccountApiKeyRuntimeProbeCandidate[],
  limit: number,
  now: string
): AccountApiKeyRuntimeProbeCandidate[] {
  const claimed: AccountApiKeyRuntimeProbeCandidate[] = []
  const claimedUntil = new Date(
    requiredRfc3339Timestamp(now, '账号 API Key 探测当前时间') + probeClaimLeaseSeconds * 1000
  ).toISOString()
  for (const candidate of candidates) {
    if (claimed.length >= limit) break
    const token = newId('account_api_key_probe_claim')
    const result = database.prepare(`
      UPDATE account_api_key_runtime_states
      SET probe_claim_token = ?, probe_claimed_until = ?
      WHERE account_id = ?
        AND key_fingerprint = ?
        AND status = ?
        AND next_probe_at = ?
        AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
    `).run(
      token,
      claimedUntil,
      candidate.accountId,
      candidate.keyFingerprint,
      candidate.status,
      candidate.nextProbeAt,
      now
    )
    if (Number(result.changes ?? 0) !== 1) continue
    claimed.push({ ...candidate, probeClaimToken: token, probeClaimedUntil: claimedUntil })
  }
  return claimed
}

async function claimAccountApiKeyRuntimeProbeCandidatesAsync(
  client: DatabaseClient,
  candidates: AccountApiKeyRuntimeProbeCandidate[],
  limit: number,
  now: string
): Promise<AccountApiKeyRuntimeProbeCandidate[]> {
  return await client.transaction(async (tx) => {
    const claimed: AccountApiKeyRuntimeProbeCandidate[] = []
    const claimedUntil = new Date(
      requiredRfc3339Timestamp(now, '账号 API Key 探测当前时间') + probeClaimLeaseSeconds * 1000
    ).toISOString()
    for (const candidate of candidates) {
      if (claimed.length >= limit) break
      const token = newId('account_api_key_probe_claim')
      const result = await tx.execute(`
        UPDATE ${accountApiKeyRuntimeStatesTable(tx)}
        SET probe_claim_token = ?, probe_claimed_until = ?
        WHERE account_id = ?
          AND key_fingerprint = ?
          AND status = ?
          AND next_probe_at = ?
          AND (probe_claimed_until IS NULL OR probe_claimed_until <= ?)
      `, [
        token,
        claimedUntil,
        candidate.accountId,
        candidate.keyFingerprint,
        candidate.status,
        candidate.nextProbeAt,
        now
      ])
      if (result.changes !== 1) continue
      claimed.push({ ...candidate, probeClaimToken: token, probeClaimedUntil: claimedUntil })
    }
    return claimed
  })
}

export function loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds: string[]): Map<string, AccountApiKeyRuntimeSummary> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeSummary>()
  const rows = accountApiKeyRuntimeSummaryRows(ids)
  const statesByAccountId = loadAccountApiKeyRuntimeDetailRowsByAccountIds(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeSummariesFromRows(rows, statesByAccountId)
}

export async function loadAccountApiKeyRuntimeSummariesByAccountIdsAsync(accountIds: string[]): Promise<Map<string, AccountApiKeyRuntimeSummary>> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return loadAccountApiKeyRuntimeSummariesByAccountIds(accountIds)
  }
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map<string, AccountApiKeyRuntimeSummary>()
  const rows = await accountApiKeyRuntimeSummaryRowsAsync(ids)
  const statesByAccountId = await loadAccountApiKeyRuntimeDetailRowsByAccountIdsAsync(rows.map((row) => row.sourceAccountId))
  return accountApiKeyRuntimeSummariesFromRows(rows, statesByAccountId)
}

function accountApiKeyRuntimeSummariesFromRows(
  rows: AccountApiKeyRuntimeSummarySourceRow[],
  statesByAccountId: Map<string, AccountApiKeyRuntimeDetailRow[]>
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
      (statesByAccountId.get(row.sourceAccountId) ?? []).map((state) => [state.key_fingerprint, state])
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
      if (
        state.next_probe_at
        && isAccountApiKeyRuntimeProbeCandidateStatus(state.status)
        && (!summary.nextProbeAt || state.next_probe_at < summary.nextProbeAt)
      ) {
        summary.nextProbeAt = state.next_probe_at
      }
    }
    const latestFailure = entries
      .map((entry) => statesByFingerprint.get(entry.fingerprint))
      .filter((state): state is AccountApiKeyRuntimeDetailRow => Boolean(state?.last_failure_at))
      .sort((left, right) => {
        const byTime = right.last_failure_at!.localeCompare(left.last_failure_at!)
        if (byTime !== 0) return byTime
        const byIndex = left.key_index - right.key_index
        return byIndex !== 0 ? byIndex : left.key_fingerprint.localeCompare(right.key_fingerprint)
      })[0]
    if (latestFailure?.last_failure_at) {
      summary.lastFailureAt = latestFailure.last_failure_at
      summary.lastErrorCode = latestFailure.last_error_code ?? undefined
      summary.lastErrorMessage = runtimeErrorMessageForResponse(latestFailure.last_error_message)
      summary.lastTraceId = runtimeTraceIdForResponse(latestFailure.last_trace_id)
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
        lastErrorMessage: runtimeErrorMessageForResponse(state?.last_error_message),
        lastTraceId: runtimeTraceIdForResponse(state?.last_trace_id)
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
  const expectedFence = expectedProbeStateFence(input)
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const configFence = expectedAccountConfigRevisionFence(
    input,
    'accounts',
    'account_api_key_runtime_states.account_id'
  )
  const database = getBusinessDatabase()
  const existing = database
    .prepare(`
      SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, probe_backoff_seconds,
        recovery_started_at, last_error_code, updated_at
      FROM account_api_key_runtime_states
      WHERE account_id = ?
        AND key_fingerprint = ?
      LIMIT 1
    `)
    .get(target.accountId, target.keyFingerprint) as unknown as AccountApiKeyRuntimeRow | undefined
  if (existing?.status === 'disabled') {
    return { changed: false, skippedReason: 'key_disabled' }
  }
  if (expectedFence.provided && !existing) {
    return { changed: false, skippedReason: 'stale_probe_state' }
  }

  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextBackoffSeconds = nextProbeBackoffSeconds(existing?.probe_backoff_seconds)
  const status = normalizeFailureStatus(input.status)
  const cooldownUntil = input.cooldownUntil === undefined
    ? undefined
    : requiredRfc3339Instant(input.cooldownUntil, 'cooldownUntil')
  const nextProbeAt = cooldownUntil !== undefined && status === 'rate_limited'
    ? cooldownUntil
    : new Date(Date.now() + nextBackoffSeconds * 1000).toISOString()
  const errorCode = input.errorCode
    ?? (input.quotaRecoveryMode === 'explicit_reset'
      ? API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE
      : input.quotaRecoveryMode === 'generic'
        ? API_KEY_QUOTA_GENERIC_ERROR_CODE
        : typeof input.statusCode === 'number' ? `http_${input.statusCode}` : null)
  const errorMessage = sanitizeRuntimeErrorMessage(input.errorMessage ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败'))
  const recoveryStartedAt = quotaRecoveryStartedAt({
    mode: input.quotaRecoveryMode,
    existing,
    observedAt,
    breakWindow: input.breakQuotaRecoveryWindow === true
  })
  const recoveryStartedAtSql = input.breakQuotaRecoveryWindow === true
    ? '?'
    : input.quotaRecoveryMode === undefined
    ? 'COALESCE(recovery_started_at, ?)'
    : '?'

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
              recovery_started_at = ${recoveryStartedAtSql},
              last_attempt_at = ?,
              last_failure_at = ?,
              last_error_code = ?,
              last_error_message = ?,
              last_trace_id = ?,
              probe_claim_token = NULL,
              probe_claimed_until = NULL,
              updated_at = ?
          WHERE account_id = ?
            AND key_fingerprint = ?
            AND status <> 'disabled'
            AND (last_attempt_at IS NULL OR last_attempt_at < ?)
            ${expectedFence.sql}
            ${configFence.sql}
        `)
        .run(
          target.systemAccountId,
          target.keyIndex,
          status,
          nextProbeAt,
          nextProbeAt,
          nextBackoffSeconds,
          ...(input.breakQuotaRecoveryWindow === true || input.quotaRecoveryMode !== undefined
            ? [recoveryStartedAt]
            : [now]),
          observedAt,
          observedAt,
          errorCode,
          errorMessage,
          normalizeRuntimeTraceId(input.traceId),
          now,
          target.accountId,
          target.keyFingerprint,
          observedAt,
          ...expectedFence.params,
          ...configFence.params
        )
    : database
        .prepare(`
          INSERT INTO account_api_key_runtime_states (
            id, system_account_id, account_id, key_fingerprint, key_index,
            status, failure_count, consecutive_failures, success_count,
            cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
            last_attempt_at, last_failure_at, last_error_code, last_error_message,
            last_trace_id,
            created_at, updated_at
          )
          VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
          recoveryStartedAt,
          observedAt,
          observedAt,
          errorCode,
          errorMessage,
          normalizeRuntimeTraceId(input.traceId),
          now,
          now
        )

  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    markRuntimeStateChanged(target.accountId)
  }
  return {
    changed,
    ...(expectedFence.provided && !changed ? { skippedReason: 'stale_probe_state' } : {})
  }
}

export async function recordAccountApiKeyRuntimeFailureAsync(input: AccountApiKeyRuntimeFailureInput): Promise<AccountApiKeyRuntimeWriteResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountApiKeyRuntimeFailure(input)
  }
  const target = accountApiKeyRuntimeTarget(input.account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const expectedFence = expectedProbeStateFence(input, 'current_state.')
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const configFence = expectedAccountConfigRevisionFence(
    input,
    accountApiKeyRuntimeBusinessTable(client, 'accounts'),
    'current_state.account_id',
    true
  )
  const existing = await client.one<AccountApiKeyRuntimeRow>(`
    SELECT account_id, key_fingerprint, key_index, status, cooldown_until, next_probe_at, probe_backoff_seconds,
      recovery_started_at, last_error_code, updated_at
    FROM ${accountApiKeyRuntimeStatesTable(client)}
    WHERE account_id = ?
      AND key_fingerprint = ?
    LIMIT 1
  `, [target.accountId, target.keyFingerprint])
  if (existing?.status === 'disabled') {
    return { changed: false, skippedReason: 'key_disabled' }
  }
  if (expectedFence.provided && !existing) {
    return { changed: false, skippedReason: 'stale_probe_state' }
  }

  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextBackoffSeconds = nextProbeBackoffSeconds(existing?.probe_backoff_seconds)
  const status = normalizeFailureStatus(input.status)
  const cooldownUntil = input.cooldownUntil === undefined
    ? undefined
    : requiredRfc3339Instant(input.cooldownUntil, 'cooldownUntil')
  const nextProbeAt = cooldownUntil !== undefined && status === 'rate_limited'
    ? cooldownUntil
    : new Date(Date.now() + nextBackoffSeconds * 1000).toISOString()
  const errorCode = input.errorCode
    ?? (input.quotaRecoveryMode === 'explicit_reset'
      ? API_KEY_QUOTA_EXPLICIT_RESET_ERROR_CODE
      : input.quotaRecoveryMode === 'generic'
        ? API_KEY_QUOTA_GENERIC_ERROR_CODE
        : typeof input.statusCode === 'number' ? `http_${input.statusCode}` : null)
  const errorMessage = sanitizeRuntimeErrorMessage(input.errorMessage ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败'))
  const recoveryStartedAt = quotaRecoveryStartedAt({
    mode: input.quotaRecoveryMode,
    existing,
    observedAt,
    breakWindow: input.breakQuotaRecoveryWindow === true
  })
  const table = accountApiKeyRuntimeStatesTable(client)
  const atomicNextBackoffSql = `(CASE
    WHEN current_state.probe_backoff_seconds > 0
      THEN LEAST(${maxProbeBackoffSeconds}, current_state.probe_backoff_seconds * 2)
    ELSE ${initialProbeBackoffSeconds}
  END)`
  const atomicNextProbeAtSql = `to_char(
    (statement_timestamp() + ${atomicNextBackoffSql} * INTERVAL '1 second') AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
  )`
  const preserveExplicitCooldownSql = cooldownUntil !== undefined && status === 'rate_limited' ? 'TRUE' : 'FALSE'

  if (expectedFence.provided) {
    const fencedResult = await client.execute(`
      UPDATE ${table} AS current_state
      SET system_account_id = ?,
          key_index = ?,
          status = ?,
          failure_count = current_state.failure_count + 1,
          consecutive_failures = current_state.consecutive_failures + 1,
          cooldown_until = CASE
            WHEN ${preserveExplicitCooldownSql} THEN ?
            ELSE ${atomicNextProbeAtSql}
          END,
          next_probe_at = CASE
            WHEN ${preserveExplicitCooldownSql} THEN ?
            ELSE ${atomicNextProbeAtSql}
          END,
          probe_backoff_seconds = ${atomicNextBackoffSql},
          recovery_started_at = ${input.breakQuotaRecoveryWindow === true
            ? '?'
            : input.quotaRecoveryMode === undefined ? 'COALESCE(current_state.recovery_started_at, ?)' : '?'},
          last_attempt_at = ?,
          last_failure_at = ?,
          last_error_code = ?,
          last_error_message = ?,
          last_trace_id = ?,
          probe_claim_token = NULL,
          probe_claimed_until = NULL,
          updated_at = ?
      WHERE current_state.account_id = ?
        AND current_state.key_fingerprint = ?
        AND current_state.status <> 'disabled'
        AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at < ?)
        ${expectedFence.sql}
        ${configFence.sql}
    `, [
      target.systemAccountId,
      target.keyIndex,
      status,
      nextProbeAt,
      nextProbeAt,
      ...(input.breakQuotaRecoveryWindow === true || input.quotaRecoveryMode !== undefined
        ? [recoveryStartedAt]
        : [now]),
      observedAt,
      observedAt,
      errorCode,
      errorMessage,
      normalizeRuntimeTraceId(input.traceId),
      now,
      target.accountId,
      target.keyFingerprint,
      observedAt,
      ...expectedFence.params,
      ...configFence.params
    ])
    const changed = Number(fencedResult.changes ?? 0) > 0
    if (changed) {
      await markRuntimeStateChangedAsync(client, target.accountId)
    }
    return { changed, ...(!changed ? { skippedReason: 'stale_probe_state' } : {}) }
  }

  const result = await client.execute(`
    INSERT INTO ${table} AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_failure_at, last_error_code, last_error_message,
      last_trace_id,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT (account_id, key_fingerprint) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      key_index = excluded.key_index,
      status = excluded.status,
      failure_count = current_state.failure_count + 1,
      consecutive_failures = current_state.consecutive_failures + 1,
      cooldown_until = CASE
        WHEN ${preserveExplicitCooldownSql} THEN excluded.cooldown_until
        ELSE ${atomicNextProbeAtSql}
      END,
      next_probe_at = CASE
        WHEN ${preserveExplicitCooldownSql} THEN excluded.next_probe_at
        ELSE ${atomicNextProbeAtSql}
      END,
      probe_backoff_seconds = ${atomicNextBackoffSql},
      recovery_started_at = CASE
        WHEN ${input.breakQuotaRecoveryWindow === true || input.quotaRecoveryMode !== undefined ? 'FALSE' : 'TRUE'}
          THEN excluded.recovery_started_at
        ELSE COALESCE(current_state.recovery_started_at, excluded.recovery_started_at)
      END,
      last_attempt_at = excluded.last_attempt_at,
      last_failure_at = excluded.last_failure_at,
      last_error_code = excluded.last_error_code,
      last_error_message = excluded.last_error_message,
      last_trace_id = excluded.last_trace_id,
      probe_claim_token = NULL,
      probe_claimed_until = NULL,
      updated_at = excluded.updated_at
    WHERE current_state.status <> 'disabled'
      AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at < excluded.last_attempt_at)
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
    recoveryStartedAt,
    observedAt,
    observedAt,
    errorCode,
    errorMessage,
    normalizeRuntimeTraceId(input.traceId),
    now,
    now
  ])

  const changed = Number(result.changes ?? 0) > 0
  if (changed) {
    await markRuntimeStateChangedAsync(client, target.accountId)
  }
  return { changed }
}

export function deferAccountApiKeyRuntimeProbe(input: AccountApiKeyRuntimeProbeDeferInput): AccountApiKeyRuntimeWriteResult {
  const target = accountApiKeyRuntimeTarget(input.account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const expectedNextProbeAt = normalizeExpectedProbeAt(input.expectedNextProbeAt)
  if (!expectedNextProbeAt) {
    return {
      changed: false,
      skippedReason: input.expectedNextProbeAt === undefined ? 'missing_expected_probe_at' : 'invalid_expected_probe_at'
    }
  }
  const expectedFence = expectedProbeStateFence({
    ...input,
    expectedNextProbeAt
  })
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const configFence = expectedAccountConfigRevisionFence(
    input,
    'accounts',
    'account_api_key_runtime_states.account_id'
  )
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextProbeAt = new Date(Date.now() + normalizeProbeDeferSeconds(input.delaySeconds) * 1000).toISOString()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE account_api_key_runtime_states
      SET next_probe_at = ?,
          last_attempt_at = ?,
          probe_claim_token = NULL,
          probe_claimed_until = NULL,
          recovery_started_at = CASE WHEN ${input.breakQuotaRecoveryWindow === true ? 'TRUE' : 'FALSE'} THEN NULL ELSE recovery_started_at END,
          updated_at = ?
      WHERE account_id = ?
        AND key_fingerprint = ?
        AND (last_attempt_at IS NULL OR last_attempt_at <= ?)
        ${expectedFence.sql}
        ${configFence.sql}
    `)
    .run(
      nextProbeAt,
      observedAt,
      now,
      target.accountId,
      target.keyFingerprint,
      observedAt,
      ...expectedFence.params,
      ...configFence.params
    )
  const changed = Number(result.changes ?? 0) > 0
  return { changed, ...(!changed ? { skippedReason: 'stale_probe_state' } : {}) }
}

export async function deferAccountApiKeyRuntimeProbeAsync(input: AccountApiKeyRuntimeProbeDeferInput): Promise<AccountApiKeyRuntimeWriteResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return deferAccountApiKeyRuntimeProbe(input)
  }
  const target = accountApiKeyRuntimeTarget(input.account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const expectedNextProbeAt = normalizeExpectedProbeAt(input.expectedNextProbeAt)
  if (!expectedNextProbeAt) {
    return {
      changed: false,
      skippedReason: input.expectedNextProbeAt === undefined ? 'missing_expected_probe_at' : 'invalid_expected_probe_at'
    }
  }
  const expectedFence = expectedProbeStateFence({
    ...input,
    expectedNextProbeAt
  })
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const nextProbeAt = new Date(Date.now() + normalizeProbeDeferSeconds(input.delaySeconds) * 1000).toISOString()
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const configFence = expectedAccountConfigRevisionFence(
    input,
    accountApiKeyRuntimeBusinessTable(client, 'accounts'),
    'current_state.account_id',
    true
  )
  const result = await client.execute(`
    UPDATE ${accountApiKeyRuntimeStatesTable(client)} AS current_state
    SET next_probe_at = ?,
        last_attempt_at = ?,
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        recovery_started_at = CASE WHEN ${input.breakQuotaRecoveryWindow === true ? 'TRUE' : 'FALSE'} THEN NULL ELSE current_state.recovery_started_at END,
        updated_at = ?
    WHERE account_id = ?
      AND key_fingerprint = ?
      AND (last_attempt_at IS NULL OR last_attempt_at <= ?)
      ${expectedFence.sql}
      ${configFence.sql}
  `, [
    nextProbeAt,
    observedAt,
    now,
    target.accountId,
    target.keyFingerprint,
    observedAt,
    ...expectedFence.params,
    ...configFence.params
  ])
  const changed = Number(result.changes ?? 0) > 0
  return { changed, ...(!changed ? { skippedReason: 'stale_probe_state' } : {}) }
}

export function recordAccountApiKeyRuntimeSuccess(account: OpenAIAccountSecret, input: AccountApiKeyRuntimeSuccessInput = {}): AccountApiKeyRuntimeWriteResult {
  if (input.expectedStatus === 'error') {
    return { changed: false, skippedReason: 'manual_restore_required' }
  }
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const expectedFence = expectedProbeStateFence(input)
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const configFence = expectedAccountConfigRevisionFence(
    input,
    'accounts',
    'account_api_key_runtime_states.account_id'
  )
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  if (expectedFence.provided) {
    const fencedResult = getBusinessDatabase()
      .prepare(`
        UPDATE account_api_key_runtime_states
        SET system_account_id = ?,
            key_index = ?,
            status = 'active',
            consecutive_failures = 0,
            success_count = success_count + 1,
            cooldown_until = NULL,
            next_probe_at = NULL,
            probe_backoff_seconds = 0,
            recovery_started_at = NULL,
            last_attempt_at = ?,
            last_success_at = ?,
            last_error_code = NULL,
            last_error_message = NULL,
            last_trace_id = NULL,
            probe_claim_token = NULL,
            probe_claimed_until = NULL,
            updated_at = ?
        WHERE account_id = ?
          AND key_fingerprint = ?
          AND status NOT IN ('disabled', 'error')
          AND (last_attempt_at IS NULL OR last_attempt_at <= ?)
          ${expectedFence.sql}
          ${configFence.sql}
      `)
      .run(
        target.systemAccountId,
        target.keyIndex,
        observedAt,
        observedAt,
        now,
        target.accountId,
        target.keyFingerprint,
        observedAt,
        ...expectedFence.params,
        ...configFence.params
      )
    const changed = Number(fencedResult.changes ?? 0) > 0
    if (changed) {
      markRuntimeStateChanged(target.accountId)
    }
    return { changed, ...(!changed ? { skippedReason: 'stale_probe_state' } : {}) }
  }
  const result = getBusinessDatabase()
    .prepare(`
      INSERT INTO account_api_key_runtime_states (
        id, system_account_id, account_id, key_fingerprint, key_index,
        status, failure_count, consecutive_failures, success_count,
        cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
        last_attempt_at, last_success_at, last_error_code, last_error_message,
        last_trace_id,
        created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, NULL, ?, ?)
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
        last_trace_id = NULL,
        probe_claim_token = NULL,
        probe_claimed_until = NULL,
        updated_at = excluded.updated_at
      WHERE account_api_key_runtime_states.status NOT IN ('disabled', 'error')
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

export async function recordAccountApiKeyRuntimeSuccessAsync(account: OpenAIAccountSecret, input: AccountApiKeyRuntimeSuccessInput = {}): Promise<AccountApiKeyRuntimeWriteResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return recordAccountApiKeyRuntimeSuccess(account, input)
  }
  if (input.expectedStatus === 'error') {
    return { changed: false, skippedReason: 'manual_restore_required' }
  }
  const target = accountApiKeyRuntimeTarget(account)
  if (!target) {
    return { changed: false, skippedReason: 'not_api_key_pool_account' }
  }
  const expectedFence = expectedProbeStateFence(input, 'current_state.')
  if (expectedFence.invalidReason) {
    return { changed: false, skippedReason: expectedFence.invalidReason }
  }
  const now = nowIso()
  const observedAt = normalizeObservedAt(input.observedAt, now)
  const client = await getAccountApiKeyRuntimeStateDatabaseClient()
  const configFence = expectedAccountConfigRevisionFence(
    input,
    accountApiKeyRuntimeBusinessTable(client, 'accounts'),
    'current_state.account_id',
    true
  )
  if (expectedFence.provided) {
    const fencedResult = await client.execute(`
      UPDATE ${accountApiKeyRuntimeStatesTable(client)} AS current_state
      SET system_account_id = ?,
          key_index = ?,
          status = 'active',
          consecutive_failures = 0,
          success_count = current_state.success_count + 1,
          cooldown_until = NULL,
          next_probe_at = NULL,
          probe_backoff_seconds = 0,
          recovery_started_at = NULL,
          last_attempt_at = ?,
          last_success_at = ?,
          last_error_code = NULL,
          last_error_message = NULL,
          last_trace_id = NULL,
          probe_claim_token = NULL,
          probe_claimed_until = NULL,
          updated_at = ?
      WHERE current_state.account_id = ?
        AND current_state.key_fingerprint = ?
        AND current_state.status NOT IN ('disabled', 'error')
        AND (current_state.last_attempt_at IS NULL OR current_state.last_attempt_at <= ?)
        ${expectedFence.sql}
        ${configFence.sql}
    `, [
      target.systemAccountId,
      target.keyIndex,
      observedAt,
      observedAt,
      now,
      target.accountId,
      target.keyFingerprint,
      observedAt,
      ...expectedFence.params,
      ...configFence.params
    ])
    const changed = Number(fencedResult.changes ?? 0) > 0
    if (changed) {
      await markRuntimeStateChangedAsync(client, target.accountId)
    }
    return { changed, ...(!changed ? { skippedReason: 'stale_probe_state' } : {}) }
  }
  const result = await client.execute(`
    INSERT INTO ${accountApiKeyRuntimeStatesTable(client)} AS current_state (
      id, system_account_id, account_id, key_fingerprint, key_index,
      status, failure_count, consecutive_failures, success_count,
      cooldown_until, next_probe_at, probe_backoff_seconds, recovery_started_at,
      last_attempt_at, last_success_at, last_error_code, last_error_message,
      last_trace_id,
      created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 'active', 0, 0, 1, NULL, NULL, 0, NULL, ?, ?, NULL, NULL, NULL, ?, ?)
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
      last_trace_id = NULL,
      probe_claim_token = NULL,
      probe_claimed_until = NULL,
      updated_at = excluded.updated_at
    WHERE current_state.status NOT IN ('disabled', 'error')
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
  const normalizedFallback = requiredRfc3339Instant(fallback, 'observedAt fallback')
  if (value === undefined) return normalizedFallback
  const normalizedValue = requiredRfc3339Instant(value, 'observedAt')
  return new Date(Math.min(
    requiredRfc3339Timestamp(normalizedValue, 'observedAt'),
    requiredRfc3339Timestamp(normalizedFallback, 'observedAt fallback')
  )).toISOString()
}

function accountApiKeyRuntimeTarget(account: OpenAIAccountSecret): AccountApiKeyRuntimeTarget | undefined {
  if (account.apiKeyRuntimeStateDisabled) return undefined
  const keyFingerprint = account.selectedApiKeyFingerprint?.trim()
  if (!keyFingerprint) return undefined
  const apiKeys = account.apiKeys ?? []
  const credentials = {
    ...account.credentials,
    api_key: account.apiKey,
    ...(apiKeys.length ? { api_keys: apiKeys } : {})
  }
  if (!isAccountApiKeyPoolIsolationEnabled({
    providerCode: account.providerCode,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    apiKeys,
    credentials
  })) {
    return undefined
  }
  if (!accountApiKeyEntries(credentials).some((entry) => entry.fingerprint === keyFingerprint)) {
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
          last_error_code, last_error_message, last_trace_id
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
        last_error_code, last_error_message, last_trace_id
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

function quotaRecoveryStartedAt(input: {
  mode: ApiKeyQuotaRecoveryMode | undefined
  existing: Pick<AccountApiKeyRuntimeRow, 'status' | 'recovery_started_at' | 'last_error_code'> | undefined
  observedAt: string
  breakWindow?: boolean
}): string | null {
  if (input.breakWindow) return null
  if (input.mode === 'explicit_reset') return null
  if (input.mode === 'generic') {
    const previousMode = apiKeyQuotaRecoveryModeFromErrorCode(input.existing?.last_error_code ?? undefined)
    if (previousMode === 'generic' && (input.existing?.status === 'rate_limited' || input.existing?.status === 'error')) {
      return input.existing?.recovery_started_at ?? input.observedAt
    }
    return input.observedAt
  }
  return input.existing?.recovery_started_at ?? input.observedAt
}

function nextProbeBackoffSeconds(previous: number | null | undefined): number {
  const value = typeof previous === 'number' && Number.isFinite(previous) && previous > 0
    ? Math.trunc(previous)
    : 0
  return value > 0
    ? Math.min(maxProbeBackoffSeconds, value * 2)
    : initialProbeBackoffSeconds
}

function normalizeExpectedProbeAt(value: string | undefined): string | undefined {
  return value === undefined ? undefined : canonicalizeRfc3339Instant(value)
}

function requiredRfc3339Timestamp(value: string, label: string): number {
  const timestamp = rfc3339InstantMilliseconds(value)
  if (timestamp === undefined) {
    throw new Error(`${label}必须是带 Z 或数值 offset 的 RFC3339 时间`)
  }
  return timestamp
}

function expectedProbeStateFence(
  input: AccountApiKeyExpectedProbeStateInput,
  columnPrefix = ''
): AccountApiKeyExpectedProbeStateFence {
  const predicates: string[] = []
  const params: string[] = []
  if (input.expectedStatus !== undefined) {
    predicates.push(`${columnPrefix}status = ?`)
    params.push(input.expectedStatus)
  }
  if (input.expectedNextProbeAt !== undefined) {
    const expectedNextProbeAt = normalizeExpectedProbeAt(input.expectedNextProbeAt)
    if (!expectedNextProbeAt) {
      return { provided: true, sql: '', params: [], invalidReason: 'invalid_expected_probe_at' }
    }
    predicates.push(`${columnPrefix}next_probe_at = ?`)
    params.push(expectedNextProbeAt)
  }
  if (input.expectedStateUpdatedAt !== undefined) {
    const expectedStateUpdatedAt = normalizeExpectedProbeAt(input.expectedStateUpdatedAt)
    if (!expectedStateUpdatedAt) {
      return { provided: true, sql: '', params: [], invalidReason: 'invalid_expected_state_updated_at' }
    }
    predicates.push(`${columnPrefix}updated_at = ?`)
    params.push(expectedStateUpdatedAt)
  }
  if (input.expectedProbeClaimToken !== undefined) {
    const expectedProbeClaimToken = input.expectedProbeClaimToken.trim()
    if (!expectedProbeClaimToken) {
      return { provided: true, sql: '', params: [], invalidReason: 'invalid_expected_probe_claim_token' }
    }
    predicates.push(`${columnPrefix}probe_claim_token = ?`)
    params.push(expectedProbeClaimToken)
  }
  if (
    input.expectedAccountConfigRevision !== undefined
    && (!Number.isSafeInteger(input.expectedAccountConfigRevision) || input.expectedAccountConfigRevision < 1)
  ) {
    return { provided: true, sql: '', params: [], invalidReason: 'invalid_expected_account_config_revision' }
  }
  return {
    provided: predicates.length > 0 || input.expectedAccountConfigRevision !== undefined,
    sql: predicates.map((predicate) => `\n      AND ${predicate}`).join(''),
    params
  }
}

function expectedAccountConfigRevisionFence(
  input: AccountApiKeyExpectedProbeStateInput,
  accountsTable: string,
  stateAccountIdColumn: string,
  lockAccountRow = false
): { sql: string; params: number[] } {
  if (input.expectedAccountConfigRevision === undefined) {
    return { sql: '', params: [] }
  }
  return {
    sql: `\n      AND EXISTS (\n        SELECT 1\n        FROM ${accountsTable} probe_account\n        WHERE probe_account.id = ${stateAccountIdColumn}\n          AND probe_account.deleted_at IS NULL\n          AND probe_account.config_revision = ?${lockAccountRow ? '\n        FOR UPDATE' : ''}\n      )`,
    params: [input.expectedAccountConfigRevision]
  }
}

function normalizeProbeDeferSeconds(value: number): number {
  return Math.max(initialProbeBackoffSeconds, Math.min(maxProbeBackoffSeconds, Math.trunc(value)))
}

function sanitizeRuntimeErrorMessage(value: string): string {
  return value.replace(/\s+/g, ' ').trim().slice(0, 1000) || '上游请求失败'
}

function runtimeErrorMessageForResponse(value: string | null | undefined): string | undefined {
  const text = value?.replace(/\s+/g, ' ').trim()
  return text ? text.slice(0, 240) : undefined
}

function normalizeRuntimeTraceId(value: string | undefined): string | null {
  const text = value?.trim()
  return text ? text.slice(0, 200) : null
}

function runtimeTraceIdForResponse(value: string | null | undefined): string | undefined {
  return normalizeRuntimeTraceId(value ?? undefined) ?? undefined
}

function keySuffixForRuntimeDisplay(key: string): string | undefined {
  const normalized = key.trim()
  return normalized ? normalized.slice(-4) : undefined
}

function positiveInteger(value: number | null | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0
}
