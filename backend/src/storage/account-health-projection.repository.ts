import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import type { AccountHealthJobsOutcome, AccountHealthJobsProjection } from './account-health-jobs-outcome.repository.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'

export type AccountHealthProjectionDisposition = 'applied' | 'stale' | 'ignored' | 'rejected'

export interface AccountHealthProjectionResult {
  outcomeId: string
  accountId: string
  inputVersion: number
  disposition: AccountHealthProjectionDisposition
  changed: boolean
  reason?: string
}

interface ReceiptRow {
  outcome_id: string
  account_id: string
  input_version: number | string | bigint
  disposition: AccountHealthProjectionDisposition
  reason: string | null
}

interface AccountFenceRow {
  id: string
  status: string
  config_revision: number | string | bigint
  dispatch_revision: number | string | bigint
  authorization_instance_source_account_id: string | null
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_generation: string | null
}

interface InputVersionRow {
  current_version: number | string | bigint
}

type ProjectableOutcome = AccountHealthJobsOutcome & { projection: AccountHealthJobsProjection }

const transitionKinds = new Set([
  'activation_success',
  'health_success',
  'health_failure',
  'activation_error',
  'temporary_unavailable',
  'cooldown_success',
  'cooldown_defer',
  'cooldown_failure'
])

const allowedProjectionValueKeys = new Set([
  'last_health_check_at',
  'last_health_success_at',
  'last_health_check_status_code',
  'last_health_check_error_code',
  'last_health_check_error_message',
  'health_check_failure_count'
])

// This repository is deliberately not a task runner. Go owns every J1 task,
// retry, lease and outcome. Node only applies a completed, immutable outcome
// through the business SQLite single writer or the same PG transaction path.
export function projectAccountHealthJobsOutcome(
  outcome: AccountHealthJobsOutcome,
  database: DatabaseSync = getBusinessDatabase()
): AccountHealthProjectionResult {
  const base = resultBase(outcome)
  let changed = false
  let result: AccountHealthProjectionResult | undefined
  runInDatabaseTransaction(() => {
    const existing = findReceiptSqlite(database, outcome.outcome_id)
    if (existing) {
      result = receiptResult(existing)
      return
    }
    const validation = validateProjection(outcome)
    if (validation.kind === 'terminal') {
      insertReceiptSqlite(database, base, validation.disposition, validation.reason)
      result = { ...base, disposition: validation.disposition, changed: false, reason: validation.reason }
      return
    }
    const account = findAccountFenceSqlite(database, outcome.account_id)
    const fenceReason = sqliteFenceMismatchReason(database, account, validation.outcome)
    if (fenceReason) {
      insertReceiptSqlite(database, base, 'stale', fenceReason)
      result = { ...base, disposition: 'stale', changed: false, reason: fenceReason }
      return
    }
    const update = sqliteProjectionUpdate(database, validation.outcome)
    if (!update) {
      insertReceiptSqlite(database, base, 'stale', 'projection_compare_and_set_missed')
      result = { ...base, disposition: 'stale', changed: false, reason: 'projection_compare_and_set_missed' }
      return
    }
    changed = true
    if (projectionChangesAvailability(validation.outcome.projection.transition_kind)) {
      refreshGroupAccountStatsAfterWrite({ accountIds: [outcome.account_id], reason: 'j1_account_health_projection' })
    }
    insertReceiptSqlite(database, base, 'applied', null)
    result = { ...base, disposition: 'applied', changed: true }
  }, database)
  if (changed) invalidateAccountLookupCache(outcome.account_id)
  return result ?? { ...base, disposition: 'rejected', changed: false, reason: 'projection_transaction_completed_without_result' }
}

export async function projectAccountHealthJobsOutcomeAsync(
  client: DatabaseClient,
  outcome: AccountHealthJobsOutcome
): Promise<AccountHealthProjectionResult> {
  const base = resultBase(outcome)
  let changed = false
  const result = await client.transaction<AccountHealthProjectionResult>(async (tx) => {
    const existing = await findReceiptAsync(tx, outcome.outcome_id)
    if (existing) return receiptResult(existing)
    const validation = validateProjection(outcome)
    if (validation.kind === 'terminal') {
      await insertReceiptAsync(tx, base, validation.disposition, validation.reason)
      return { ...base, disposition: validation.disposition, changed: false, reason: validation.reason }
    }
    const account = await findAccountFenceAsync(tx, outcome.account_id)
    const fenceReason = await asyncFenceMismatchReason(tx, account, validation.outcome)
    if (fenceReason) {
      await insertReceiptAsync(tx, base, 'stale', fenceReason)
      return { ...base, disposition: 'stale', changed: false, reason: fenceReason }
    }
    const update = await asyncProjectionUpdate(tx, validation.outcome)
    if (!update) {
      await insertReceiptAsync(tx, base, 'stale', 'projection_compare_and_set_missed')
      return { ...base, disposition: 'stale', changed: false, reason: 'projection_compare_and_set_missed' }
    }
    changed = true
    if (projectionChangesAvailability(validation.outcome.projection.transition_kind)) {
      await refreshGroupAccountStatsAfterWriteAsync({ accountIds: [outcome.account_id], reason: 'j1_account_health_projection' }, tx)
    }
    await insertReceiptAsync(tx, base, 'applied', null)
    return { ...base, disposition: 'applied', changed: true }
  })
  if (changed) invalidateAccountLookupCache(outcome.account_id)
  return result
}

function validateProjection(outcome: AccountHealthJobsOutcome): { kind: 'projectable'; outcome: ProjectableOutcome } | { kind: 'terminal'; disposition: 'ignored' | 'rejected'; reason: string } {
  if (!outcome.projection) return { kind: 'terminal', disposition: 'ignored', reason: 'outcome_has_no_account_projection' }
  const projection = outcome.projection
  if (!transitionKinds.has(projection.transition_kind)) return rejected('projection_transition_not_allowed')
  if (projection.target_account_id !== outcome.account_id
    || projection.input_version !== outcome.input_version
    || projection.config_revision !== outcome.config_revision
    || projection.dispatch_revision !== outcome.dispatch_revision) {
    return rejected('projection_top_level_fence_mismatch')
  }
  if (!projection.expected_account_status) return rejected('projection_expected_account_status_missing')
  if (projection.values) {
    for (const key of Object.keys(projection.values)) {
      if (!allowedProjectionValueKeys.has(key)) return rejected(`projection_value_not_allowed:${key}`)
    }
  }
  const transition = projection.transition_kind
  if ((transition === 'activation_success' || transition === 'activation_error') && projection.expected_account_status !== 'pending_test') {
    return rejected('projection_activation_expected_status_invalid')
  }
  if ((transition === 'health_success' || transition === 'temporary_unavailable') && projection.expected_account_status !== 'active') {
    return rejected('projection_active_expected_status_invalid')
  }
  if (transition === 'health_failure' && projection.expected_account_status !== 'active' && projection.expected_account_status !== 'pending_test') {
    return rejected('projection_health_failure_expected_status_invalid')
  }
  if (transition.startsWith('cooldown_') && projection.expected_account_status !== 'temporary_unavailable' && projection.expected_account_status !== 'rate_limited') {
    return rejected('projection_cooldown_expected_status_invalid')
  }
  const requiresNextDue = transition !== 'activation_error'
  if (requiresNextDue && !outcome.next_due_at) return rejected('projection_next_due_missing')
  if ((transition === 'temporary_unavailable' || transition === 'cooldown_defer' || transition === 'cooldown_failure') && !projection.cooldown_fence) {
    return rejected('projection_output_cooldown_fence_missing')
  }
  if (transition.startsWith('cooldown_') && !projection.expected_cooldown_fence) {
    return rejected('projection_expected_cooldown_fence_missing')
  }
  if (projection.expected_cooldown_fence && projection.source_config_revision !== undefined
    && projection.expected_cooldown_fence.source_config_revision !== projection.source_config_revision) {
    return rejected('projection_expected_cooldown_source_fence_mismatch')
  }
  if (projection.cooldown_fence && projection.source_config_revision !== undefined
    && projection.cooldown_fence.source_config_revision !== projection.source_config_revision) {
    return rejected('projection_output_cooldown_source_fence_mismatch')
  }
  if (!outcomeMatchesTransition(outcome, transition)) return rejected('projection_outcome_transition_mismatch')
  return { kind: 'projectable', outcome: outcome as ProjectableOutcome }
}

function rejected(reason: string): { kind: 'terminal'; disposition: 'rejected'; reason: string } {
  return { kind: 'terminal', disposition: 'rejected', reason }
}

function outcomeMatchesTransition(outcome: AccountHealthJobsOutcome, transition: string): boolean {
  if (transition === 'activation_success' || transition === 'health_success' || transition === 'cooldown_success') return outcome.outcome === 'complete_success'
  if (transition === 'cooldown_defer') return outcome.outcome === 'framing_complete_neutral' || outcome.outcome === 'probe_task_failure'
  if (transition === 'cooldown_failure') return outcome.outcome === 'upstream_failure'
  return outcome.outcome === 'framing_complete_neutral' || outcome.outcome === 'upstream_failure'
}

function resultBase(outcome: AccountHealthJobsOutcome): Pick<AccountHealthProjectionResult, 'outcomeId' | 'accountId' | 'inputVersion'> {
  return { outcomeId: outcome.outcome_id, accountId: outcome.account_id, inputVersion: outcome.input_version }
}

function receiptResult(row: ReceiptRow): AccountHealthProjectionResult {
  return {
    outcomeId: row.outcome_id,
    accountId: row.account_id,
    inputVersion: integer(row.input_version, 'receipt.input_version'),
    disposition: row.disposition,
    changed: false,
    ...(row.reason ? { reason: row.reason } : {})
  }
}

function findReceiptSqlite(database: DatabaseSync, outcomeId: string): ReceiptRow | undefined {
  return database.prepare('SELECT outcome_id, account_id, input_version, disposition, reason FROM account_health_projection_receipts WHERE outcome_id = ?').get(outcomeId) as ReceiptRow | undefined
}

async function findReceiptAsync(client: DatabaseClient, outcomeId: string): Promise<ReceiptRow | undefined> {
  return await client.one<ReceiptRow>(`SELECT outcome_id, account_id, input_version, disposition, reason FROM ${table(client, 'account_health_projection_receipts')} WHERE outcome_id = ?`, [outcomeId])
}

function insertReceiptSqlite(
  database: DatabaseSync,
  base: Pick<AccountHealthProjectionResult, 'outcomeId' | 'accountId' | 'inputVersion'>,
  disposition: AccountHealthProjectionDisposition,
  reason: string | null
): void {
  database.prepare('INSERT INTO account_health_projection_receipts(outcome_id, account_id, input_version, disposition, reason, applied_at) VALUES (?, ?, ?, ?, ?, ?)')
    .run(base.outcomeId, base.accountId, base.inputVersion, disposition, reason, nowIso())
}

async function insertReceiptAsync(
  client: DatabaseClient,
  base: Pick<AccountHealthProjectionResult, 'outcomeId' | 'accountId' | 'inputVersion'>,
  disposition: AccountHealthProjectionDisposition,
  reason: string | null
): Promise<void> {
  await client.execute(`INSERT INTO ${table(client, 'account_health_projection_receipts')}(outcome_id, account_id, input_version, disposition, reason, applied_at) VALUES (?, ?, ?, ?, ?, ?)`, [base.outcomeId, base.accountId, base.inputVersion, disposition, reason, nowIso()])
}

function findAccountFenceSqlite(database: DatabaseSync, accountId: string): AccountFenceRow | undefined {
  return database.prepare(`SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id, cooldown_retest_observation_started_at, cooldown_retest_generation FROM accounts WHERE id = ? AND deleted_at IS NULL`).get(accountId) as AccountFenceRow | undefined
}

async function findAccountFenceAsync(client: DatabaseClient, accountId: string): Promise<AccountFenceRow | undefined> {
  return await client.one<AccountFenceRow>(`SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id, cooldown_retest_observation_started_at, cooldown_retest_generation FROM ${table(client, 'accounts')} WHERE id = ? AND deleted_at IS NULL FOR UPDATE`, [accountId])
}

function sqliteFenceMismatchReason(database: DatabaseSync, account: AccountFenceRow | undefined, outcome: ProjectableOutcome): string | undefined {
  const version = database.prepare('SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = ?').get(outcome.account_id) as InputVersionRow | undefined
  let sourceRevision: number | undefined
  if (outcome.projection.source_config_revision !== undefined && account?.authorization_instance_source_account_id) {
    const source = database.prepare('SELECT config_revision FROM accounts WHERE id = ? AND deleted_at IS NULL').get(account.authorization_instance_source_account_id) as { config_revision: number | string | bigint } | undefined
    if (source) sourceRevision = integer(source.config_revision, 'source.config_revision')
  }
  return fenceMismatchReason(account, version, sourceRevision, outcome)
}

async function asyncFenceMismatchReason(client: DatabaseClient, account: AccountFenceRow | undefined, outcome: ProjectableOutcome): Promise<string | undefined> {
  const version = await client.one<InputVersionRow>(`SELECT current_version FROM ${table(client, 'account_health_jobs_input_versions')} WHERE account_id = ? FOR UPDATE`, [outcome.account_id])
  let sourceRevision: number | undefined
  if (outcome.projection.source_config_revision !== undefined && account?.authorization_instance_source_account_id) {
    const source = await client.one<{ config_revision: number | string | bigint }>(`SELECT config_revision FROM ${table(client, 'accounts')} WHERE id = ? AND deleted_at IS NULL FOR UPDATE`, [account.authorization_instance_source_account_id])
    if (source) sourceRevision = integer(source.config_revision, 'source.config_revision')
  }
  return fenceMismatchReason(account, version, sourceRevision, outcome)
}

function fenceMismatchReason(account: AccountFenceRow | undefined, version: InputVersionRow | undefined, sourceRevision: number | undefined, outcome: ProjectableOutcome): string | undefined {
  if (!account) return 'account_missing_or_deleted'
  if (!version || integer(version.current_version, 'input_version.current_version') !== outcome.input_version) return 'input_version_stale'
  if (integer(account.config_revision, 'account.config_revision') !== outcome.config_revision) return 'config_revision_stale'
  if (integer(account.dispatch_revision, 'account.dispatch_revision') !== outcome.dispatch_revision) return 'dispatch_revision_stale'
  if (account.status !== outcome.projection.expected_account_status) return 'expected_account_status_stale'
  if (outcome.projection.source_config_revision !== undefined) {
    if (!account.authorization_instance_source_account_id) return 'source_account_missing'
    if (sourceRevision === undefined) return 'source_account_missing_or_deleted'
    if (sourceRevision !== outcome.projection.source_config_revision) return 'source_config_revision_stale'
  } else if (account.authorization_instance_source_account_id) {
    return 'source_config_revision_missing'
  }
  const expectedCooldown = outcome.projection.expected_cooldown_fence
  if (expectedCooldown) {
    if (account.cooldown_retest_observation_started_at !== expectedCooldown.observation_started_at) return 'cooldown_observation_stale'
    if (account.cooldown_retest_generation !== expectedCooldown.generation) return 'cooldown_generation_stale'
  }
  return undefined
}

function sqliteProjectionUpdate(database: DatabaseSync, outcome: ProjectableOutcome): boolean {
  const statement = buildProjectionUpdate('accounts', 'account_health_jobs_input_versions', outcome)
  const result = database.prepare(statement.sql).run(...statement.params)
  return Number(result.changes ?? 0) === 1
}

async function asyncProjectionUpdate(client: DatabaseClient, outcome: ProjectableOutcome): Promise<boolean> {
  const statement = buildProjectionUpdate(table(client, 'accounts'), table(client, 'account_health_jobs_input_versions'), outcome)
  const result = await client.execute(statement.sql, statement.params)
  return result.changes === 1
}

function buildProjectionUpdate(accountsTable: string, versionsTable: string, outcome: ProjectableOutcome): { sql: string; params: Array<string | number | null> } {
  const projection = outcome.projection
  const transition = projection.transition_kind
  const updates: string[] = ['updated_at = ?']
  const params: Array<string | number | null> = [nowIso()]
  const set = (column: string, value: string | number | null): void => {
    updates.push(`${column} = ?`)
    params.push(value)
  }
  const health = (): void => {
    set('last_health_check_at', outcome.observed_at)
    set('next_health_check_at', outcome.next_due_at ?? null)
    set('last_health_check_status_code', outcome.status_code ?? null)
    set('last_health_check_trace_id', limited(outcome.request_id, 200))
  }
  const healthSuccess = (): void => {
    health()
    set('last_health_success_at', outcome.observed_at)
    set('health_check_failure_count', 0)
    set('health_check_failure_started_at', null)
    set('last_health_check_error_code', null)
    set('last_health_check_error_message', null)
  }
  const healthFailure = (): void => {
    health()
    set('health_check_failure_count', outcome.failure_count ?? 0)
    set('health_check_failure_started_at', outcome.failure_started_at ?? outcome.observed_at)
    set('last_health_check_error_code', limited(outcome.error_code, 200))
    set('last_health_check_error_message', limited(outcome.error_message, 2_000))
  }
  const clearCooldown = (): void => {
    set('cooldown_until', null)
    set('cooldown_retest_failure_count', 0)
    set('cooldown_retest_observation_started_at', null)
    set('cooldown_retest_generation', null)
    set('cooldown_retest_last_at', null)
    set('cooldown_retest_last_status_code', null)
  }
  const setCooldown = (fence: NonNullable<AccountHealthJobsProjection['cooldown_fence']>): void => {
    set('cooldown_until', outcome.next_due_at ?? null)
    set('cooldown_retest_failure_count', outcome.failure_count ?? 0)
    set('cooldown_retest_observation_started_at', fence.observation_started_at)
    set('cooldown_retest_generation', fence.generation)
    set('cooldown_retest_last_at', outcome.observed_at)
    set('cooldown_retest_last_status_code', outcome.status_code ?? null)
  }
  const setLastError = (): void => {
    set('last_error_code', limited(outcome.error_code, 200))
    set('last_error_message', limited(outcome.error_message, 2_000))
    set('last_error_trace_id', limited(outcome.request_id, 200))
  }
  const clearLastError = (): void => {
    set('last_error_code', null)
    set('last_error_message', null)
    set('last_error_trace_id', null)
  }

  switch (transition) {
    case 'activation_success':
      set('status', 'active')
      set('schedulable', 1)
      clearCooldown()
      clearLastError()
      healthSuccess()
      break
    case 'health_success':
      healthSuccess()
      break
    case 'health_failure':
      healthFailure()
      break
    case 'activation_error':
      set('status', 'error')
      set('schedulable', 0)
      clearCooldown()
      setLastError()
      healthFailure()
      break
    case 'temporary_unavailable':
      set('status', 'temporary_unavailable')
      set('schedulable', 1)
      setCooldown(requiredCooldownFence(projection))
      setLastError()
      healthFailure()
      break
    case 'cooldown_success':
      set('status', 'active')
      set('schedulable', 1)
      clearCooldown()
      clearLastError()
      healthSuccess()
      break
    case 'cooldown_defer':
      setCooldown(requiredCooldownFence(projection))
      healthFailure()
      break
    case 'cooldown_failure':
      setCooldown(requiredCooldownFence(projection))
      setLastError()
      healthFailure()
      break
    default:
      throw new Error(`J1 未知 projection transition: ${transition}`)
  }

  const guards = [
    'target.id = ?',
    'target.deleted_at IS NULL',
    'target.status = ?',
    'target.config_revision = ?',
    'target.dispatch_revision = ?',
    `EXISTS (SELECT 1 FROM ${versionsTable} AS input_version WHERE input_version.account_id = target.id AND input_version.current_version = ?)`
  ]
  params.push(outcome.account_id, projection.expected_account_status, outcome.config_revision, outcome.dispatch_revision, outcome.input_version)
  if (projection.source_config_revision === undefined) {
    guards.push('target.authorization_instance_source_account_id IS NULL')
  } else {
    guards.push(`EXISTS (SELECT 1 FROM ${accountsTable} AS source_account WHERE source_account.id = target.authorization_instance_source_account_id AND source_account.deleted_at IS NULL AND source_account.config_revision = ?)`)
    params.push(projection.source_config_revision)
  }
  if (projection.expected_cooldown_fence) {
    guards.push('target.cooldown_retest_observation_started_at = ?', 'target.cooldown_retest_generation = ?')
    params.push(projection.expected_cooldown_fence.observation_started_at, projection.expected_cooldown_fence.generation)
  }
  return {
    sql: `UPDATE ${accountsTable} AS target SET ${updates.join(', ')} WHERE ${guards.join(' AND ')}`,
    params
  }
}

function requiredCooldownFence(projection: AccountHealthJobsProjection): NonNullable<AccountHealthJobsProjection['cooldown_fence']> {
  if (!projection.cooldown_fence) throw new Error('J1 projection 缺少输出 cooldown fence')
  return projection.cooldown_fence
}

function projectionChangesAvailability(transition: string): boolean {
  return transition === 'activation_success'
    || transition === 'activation_error'
    || transition === 'temporary_unavailable'
    || transition === 'cooldown_success'
}

function table(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
}

function integer(value: number | string | bigint, field: string): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 1) throw new Error(`${field} 不是正安全整数`)
  return normalized
}

function limited(value: string | undefined, maximum: number): string | null {
  const normalized = value?.trim()
  return normalized ? normalized.slice(0, maximum) : null
}
