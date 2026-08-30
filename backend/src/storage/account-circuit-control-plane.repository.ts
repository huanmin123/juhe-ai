import { createHash, randomUUID } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export const accountCircuitProjectionKey = 'account_circuit_runtime_v1'

export type AccountCircuitIncidentState =
  | 'CLOSED'
  | 'SUSPECT'
  | 'OPEN'
  | 'HALF_OPEN'
  | 'RECOVERING'
  | 'PERSISTING'
  | 'SHADOWED_BY_PERSISTENT'

export type AccountCircuitScopeKind = 'account' | 'key' | 'protocol_model' | 'key_model'
export type AccountCircuitFailureClass =
  | 'connect_failed'
  | 'timeout_before_complete'
  | 'read_interrupted'
  | 'incomplete_response'
  | 'explicit_policy'
export type AccountCircuitLeasePurpose = 'confirmation' | 'half_open' | 'recovery' | 'cooldown_retest' | 'background_probe'
export type AccountCircuitOutboxEventType = 'dispatch_revision_changed' | 'incident_changed'
export type AccountCircuitOutboxStatus = 'pending' | 'processing' | 'dispatched'

export interface AccountCircuitDispatchRevision {
  accountId: string
  dispatchRevision: number
  projectedDispatchRevision: number
}

export interface AccountCircuitIncidentRecord {
  circuitScopeKey: string
  accountId: string
  accountRuntimeKey: string
  scopeKind: AccountCircuitScopeKind
  keyFingerprint?: string
  protocolCode?: string
  requestLane?: string
  modelFamily?: string
  clientModel?: string
  capabilityHash?: string
  credentialSourceAccountId?: string
  clientEndpointFamily?: string
  finalUpstreamModel?: string
  upstreamEndpointMode?: string
  incidentId: string
  parentIncidentId?: string
  childIncidentIds: string[]
  causedByTerminalOutcomeId?: string
  state: AccountCircuitIncidentState
  failureScope?: AccountCircuitScopeKind
  generation: number
  dispatchRevision: number
  ledgerRevision: number
  projectedLedgerRevision: number
  transitionId: string
  cooldownObservationGeneration: number
  openUntilMs?: number
  nextTransitionAtMs?: number
  leaseId?: string
  leasePurpose?: AccountCircuitLeasePurpose
  leaseOwnerRunId?: string
  leaseUntilMs?: number
  attemptStartedAtMs?: number
  attemptHardDeadlineMs?: number
  upstreamAttemptObserved: boolean
  backoffLevel: number
  consecutiveFailures: number
  confirmationFailuresRequired: number
  confirmationFailureEvidenceKeys: string[]
  recoveringSuccesses: number
  lastFailureClass?: AccountCircuitFailureClass
  retainedUntilMs?: number
  createdAtMs: number
  updatedAtMs: number
}

export interface AccountCircuitOutboxRecord {
  eventId: string
  projectionKey: string
  dedupeKey: string
  eventType: AccountCircuitOutboxEventType
  accountId: string
  accountRuntimeKey: string
  circuitScopeKey?: string
  incidentId?: string
  transitionId: string
  dispatchRevision: number
  generation?: number
  ledgerRevision?: number
  status: AccountCircuitOutboxStatus
  availableAtMs: number
  claimToken?: string
  claimedBy?: string
  claimUntilMs?: number
  attemptCount: number
  lastErrorClass?: string
  acknowledgedAtMs?: number
  createdAtMs: number
  updatedAtMs: number
}

export interface AdvanceAccountCircuitDispatchRevisionInput {
  accountId: string
  accountRuntimeKey: string
  transitionId: string
  nowMs?: number
}

export interface AdvanceAccountCircuitDispatchRevisionResult {
  status: 'applied' | 'idempotent'
  accountId: string
  accountRuntimeKey: string
  dispatchRevision: number
  transitionId: string
}

export interface CompareAndSetAccountCircuitIncidentInput {
  accountId: string
  accountRuntimeKey: string
  circuitScopeKey: string
  scopeKind: AccountCircuitScopeKind
  keyFingerprint?: string
  protocolCode?: string
  requestLane?: string
  modelFamily?: string
  clientModel?: string
  capabilityHash?: string
  credentialSourceAccountId?: string
  clientEndpointFamily?: string
  finalUpstreamModel?: string
  upstreamEndpointMode?: string
  incidentId: string
  parentIncidentId?: string
  childIncidentIds?: string[]
  causedByTerminalOutcomeId?: string
  state: AccountCircuitIncidentState
  failureScope?: AccountCircuitScopeKind
  generation: number
  dispatchRevision: number
  expectedLedgerRevision: number | null
  transitionId: string
  cooldownObservationGeneration?: number
  openUntilMs?: number
  nextTransitionAtMs?: number
  leaseId?: string
  leasePurpose?: AccountCircuitLeasePurpose
  leaseOwnerRunId?: string
  leaseUntilMs?: number
  attemptStartedAtMs?: number
  attemptHardDeadlineMs?: number
  upstreamAttemptObserved?: boolean
  backoffLevel?: number
  consecutiveFailures?: number
  confirmationFailuresRequired?: number
  confirmationFailureEvidenceKeys?: string[]
  recoveringSuccesses?: number
  lastFailureClass?: AccountCircuitFailureClass
  retainedUntilMs?: number
  stateUpdatedAtMs?: number
  nowMs?: number
}

export interface CompareAndSetAccountCircuitIncidentResult {
  status: 'applied' | 'idempotent' | 'cas_conflict' | 'stale_dispatch_revision' | 'account_not_found'
  incident?: AccountCircuitIncidentRecord
  currentDispatchRevision: number
}

export interface AccountCircuitIncidentRebuildPage {
  items: AccountCircuitIncidentRecord[]
  nextCursor?: { updatedAtMs: number; circuitScopeKey: string }
}

export interface AccountCircuitProjectionGaps {
  dispatchRevisionGaps: AccountCircuitDispatchRevision[]
  incidentGaps: AccountCircuitIncidentRecord[]
}

export interface AccountCircuitControlPlaneCleanupResult {
  deletedIncidents: number
  deletedOutbox: number
}

interface AccountDispatchRevisionRow {
  id: string
  dispatch_revision: number | bigint | string
  circuit_projection_revision: number | bigint | string
  deleted_at: string | null
}

interface AccountCircuitIncidentRow {
  circuit_scope_key: string
  account_id: string
  account_runtime_key: string
  scope_kind: string
  key_fingerprint: string | null
  protocol_code: string | null
  request_lane: string | null
  model_family: string | null
  client_model: string | null
  capability_hash: string | null
  credential_source_account_id: string | null
  client_endpoint_family: string | null
  final_upstream_model: string | null
  upstream_endpoint_mode: string | null
  incident_id: string
  parent_incident_id: string | null
  child_incident_ids_json: string
  caused_by_terminal_outcome_id: string | null
  state: string
  failure_scope: string | null
  generation: number | bigint | string
  dispatch_revision: number | bigint | string
  ledger_revision: number | bigint | string
  projected_ledger_revision: number | bigint | string
  transition_id: string
  cooldown_observation_generation: number | bigint | string
  open_until_ms: number | bigint | string | null
  next_transition_at_ms: number | bigint | string | null
  lease_id: string | null
  lease_purpose: string | null
  lease_owner_run_id: string | null
  lease_until_ms: number | bigint | string | null
  attempt_started_at_ms: number | bigint | string | null
  attempt_hard_deadline_ms: number | bigint | string | null
  upstream_attempt_observed: number | bigint | string | boolean
  backoff_level: number | bigint | string
  consecutive_failures: number | bigint | string
  confirmation_failures_required: number | bigint | string
  confirmation_failure_evidence_keys_json: string
  recovering_successes: number | bigint | string
  last_failure_class: string | null
  retained_until_ms: number | bigint | string | null
  created_at_ms: number | bigint | string
  updated_at_ms: number | bigint | string
}

interface AccountCircuitOutboxRow {
  event_id: string
  projection_key: string
  dedupe_key: string
  event_type: string
  account_id: string
  account_runtime_key: string
  circuit_scope_key: string | null
  incident_id: string | null
  transition_id: string
  dispatch_revision: number | bigint | string
  generation: number | bigint | string | null
  ledger_revision: number | bigint | string | null
  status: string
  available_at_ms: number | bigint | string
  claim_token: string | null
  claimed_by: string | null
  claim_until_ms: number | bigint | string | null
  attempt_count: number | bigint | string
  last_error_class: string | null
  acknowledged_at_ms: number | bigint | string | null
  created_at_ms: number | bigint | string
  updated_at_ms: number | bigint | string
}

interface NormalizedIncidentMutation extends Omit<
  CompareAndSetAccountCircuitIncidentInput,
  'childIncidentIds' | 'confirmationFailuresRequired' | 'confirmationFailureEvidenceKeys' | 'stateUpdatedAtMs' | 'nowMs'
> {
  childIncidentIds: string[]
  confirmationFailuresRequired: number
  confirmationFailureEvidenceKeys: string[]
  nowMs: number
  cooldownObservationGeneration: number
  upstreamAttemptObserved: boolean
  backoffLevel: number
  consecutiveFailures: number
  recoveringSuccesses: number
  stateUpdatedAtMs: number
}

const businessSchemaName = 'juhe_business'
const incidentColumnList = `
  circuit_scope_key, account_id, account_runtime_key, scope_kind, key_fingerprint,
  protocol_code, request_lane, model_family, client_model, capability_hash,
  credential_source_account_id, client_endpoint_family, final_upstream_model,
  upstream_endpoint_mode, incident_id, parent_incident_id,
  child_incident_ids_json, caused_by_terminal_outcome_id, state, failure_scope,
  generation, dispatch_revision, ledger_revision, projected_ledger_revision,
  transition_id, cooldown_observation_generation, open_until_ms, next_transition_at_ms,
  lease_id, lease_purpose, lease_owner_run_id, lease_until_ms, attempt_started_at_ms,
  attempt_hard_deadline_ms, upstream_attempt_observed, backoff_level,
  consecutive_failures, confirmation_failures_required, confirmation_failure_evidence_keys_json,
  recovering_successes, last_failure_class, retained_until_ms,
  created_at_ms, updated_at_ms
`
const outboxColumnList = `
  event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
  circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
  ledger_revision, status, available_at_ms, claim_token, claimed_by, claim_until_ms,
  attempt_count, last_error_class, acknowledged_at_ms, created_at_ms, updated_at_ms
`

export async function advanceAccountCircuitDispatchRevision(
  input: AdvanceAccountCircuitDispatchRevisionInput
): Promise<AdvanceAccountCircuitDispatchRevisionResult> {
  return advanceAccountCircuitDispatchRevisionInClient(await accountCircuitDatabaseClient(), input)
}

export async function advanceAccountCircuitDispatchRevisionInClient(
  client: DatabaseClient,
  rawInput: AdvanceAccountCircuitDispatchRevisionInput
): Promise<AdvanceAccountCircuitDispatchRevisionResult> {
  return client.transaction((tx) => advanceAccountCircuitDispatchRevisionInTransaction(tx, rawInput))
}

export async function advanceAccountCircuitDispatchRevisionInTransaction(
  client: DatabaseClient,
  rawInput: AdvanceAccountCircuitDispatchRevisionInput
): Promise<AdvanceAccountCircuitDispatchRevisionResult> {
  const input = normalizeDispatchRevisionInput(rawInput)
  const account = await lockAccountDispatchRevision(client, input.accountId)
  if (!account) throw new Error(`AI 账户不存在：${input.accountId}`)

  const dedupeKey = `dispatch:${input.transitionId}`
  const replay = await findOutboxByDedupeKey(client, dedupeKey)
  if (replay) {
    assertOutboxReplayIdentity(replay, 'dispatch_revision_changed', input.accountId, input.accountRuntimeKey)
    return {
      status: 'idempotent',
      accountId: replay.account_id,
      accountRuntimeKey: replay.account_runtime_key,
      dispatchRevision: integerValue(replay.dispatch_revision, 'dispatch_revision'),
      transitionId: replay.transition_id
    }
  }

  const accounts = businessTable(client, 'accounts')
  const revised = await client.one<AccountDispatchRevisionRow>(`
    UPDATE ${accounts}
    SET dispatch_revision = dispatch_revision + 1
    WHERE id = ?
    RETURNING id, dispatch_revision, circuit_projection_revision
  `, [input.accountId])
  if (!revised) throw new Error(`AI 账户不存在：${input.accountId}`)
  const dispatchRevision = integerValue(revised.dispatch_revision, 'dispatch_revision')
  await insertOutbox(client, {
    eventId: randomUUID(),
    dedupeKey,
    eventType: 'dispatch_revision_changed',
    accountId: input.accountId,
    accountRuntimeKey: input.accountRuntimeKey,
    transitionId: input.transitionId,
    dispatchRevision,
    nowMs: input.nowMs
  })
  return {
    status: 'applied',
    accountId: input.accountId,
    accountRuntimeKey: input.accountRuntimeKey,
    dispatchRevision,
    transitionId: input.transitionId
  }
}

export async function advanceAccountCircuitDispatchRevisionFamilyInTransaction(
  client: DatabaseClient,
  rawInput: AdvanceAccountCircuitDispatchRevisionInput
): Promise<AdvanceAccountCircuitDispatchRevisionResult[]> {
  const input = normalizeDispatchRevisionInput(rawInput)
  const accounts = businessTable(client, 'accounts')
  const instances = await client.query<{ id: string }>(`
    SELECT id
    FROM ${accounts}
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
    ORDER BY id ASC${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [input.accountId])
  const accountIds = [input.accountId, ...instances.map((row) => requiredText(row.id, 256, 'authorizedAccountId'))]
  const results: AdvanceAccountCircuitDispatchRevisionResult[] = []
  for (const accountId of accountIds) {
    results.push(await advanceAccountCircuitDispatchRevisionInTransaction(client, {
      accountId,
      accountRuntimeKey: accountId,
      transitionId: accountId === input.accountId
        ? input.transitionId
        : familyDispatchTransitionId(input.transitionId, accountId),
      nowMs: input.nowMs
    }))
  }
  return results
}

export function advanceAccountCircuitDispatchRevisionInSqliteTransaction(
  database: DatabaseSync,
  rawInput: AdvanceAccountCircuitDispatchRevisionInput
): AdvanceAccountCircuitDispatchRevisionResult {
  const input = normalizeDispatchRevisionInput(rawInput)
  const dedupeKey = `dispatch:${input.transitionId}`
  const replay = database.prepare(`
    SELECT ${outboxColumnList}
    FROM account_circuit_outbox
    WHERE dedupe_key = ?
  `).get(dedupeKey) as unknown as AccountCircuitOutboxRow | undefined
  if (replay) {
    assertOutboxReplayIdentity(replay, 'dispatch_revision_changed', input.accountId, input.accountRuntimeKey)
    return {
      status: 'idempotent',
      accountId: replay.account_id,
      accountRuntimeKey: replay.account_runtime_key,
      dispatchRevision: integerValue(replay.dispatch_revision, 'dispatch_revision'),
      transitionId: replay.transition_id
    }
  }

  const revised = database.prepare(`
    UPDATE accounts
    SET dispatch_revision = dispatch_revision + 1
    WHERE id = ?
    RETURNING id, dispatch_revision, circuit_projection_revision
  `).get(input.accountId) as unknown as AccountDispatchRevisionRow | undefined
  if (!revised) throw new Error(`AI 账户不存在：${input.accountId}`)
  const dispatchRevision = integerValue(revised.dispatch_revision, 'dispatch_revision')
  database.prepare(`
    INSERT INTO account_circuit_outbox (
      event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
      circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
      ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms
    ) VALUES (?, ?, ?, 'dispatch_revision_changed', ?, ?, NULL, NULL, ?, ?, NULL, NULL, 'pending', ?, 0, ?, ?)
  `).run(
    randomUUID(),
    accountCircuitProjectionKey,
    dedupeKey,
    input.accountId,
    input.accountRuntimeKey,
    input.transitionId,
    dispatchRevision,
    input.nowMs,
    input.nowMs,
    input.nowMs
  )
  return {
    status: 'applied',
    accountId: input.accountId,
    accountRuntimeKey: input.accountRuntimeKey,
    dispatchRevision,
    transitionId: input.transitionId
  }
}

export function advanceAccountCircuitDispatchRevisionFamilyInSqliteTransaction(
  database: DatabaseSync,
  rawInput: AdvanceAccountCircuitDispatchRevisionInput
): AdvanceAccountCircuitDispatchRevisionResult[] {
  const input = normalizeDispatchRevisionInput(rawInput)
  const instances = database.prepare(`
    SELECT id
    FROM accounts
    WHERE authorization_instance_source_account_id = ?
      AND deleted_at IS NULL
    ORDER BY id ASC
  `).all(input.accountId) as unknown as Array<{ id: string }>
  const accountIds = [input.accountId, ...instances.map((row) => requiredText(row.id, 256, 'authorizedAccountId'))]
  return accountIds.map((accountId) => advanceAccountCircuitDispatchRevisionInSqliteTransaction(database, {
    accountId,
    accountRuntimeKey: accountId,
    transitionId: accountId === input.accountId
      ? input.transitionId
      : familyDispatchTransitionId(input.transitionId, accountId),
    nowMs: input.nowMs
  }))
}

export async function compareAndSetAccountCircuitIncident(
  input: CompareAndSetAccountCircuitIncidentInput
): Promise<CompareAndSetAccountCircuitIncidentResult> {
  return compareAndSetAccountCircuitIncidentInClient(await accountCircuitDatabaseClient(), input)
}

/** Reads one incident for bridge CAS/reconciliation without exposing response data. */
export async function getAccountCircuitIncidentByScopeKey(
  circuitScopeKey: string
): Promise<AccountCircuitIncidentRecord | undefined> {
  return getAccountCircuitIncidentByScopeKeyInClient(await accountCircuitDatabaseClient(), circuitScopeKey)
}

export async function getAccountCircuitDispatchRevision(accountId: string): Promise<number> {
  return getAccountCircuitDispatchRevisionInClient(await accountCircuitDatabaseClient(), accountId)
}

export async function getAccountCircuitDispatchRevisionInClient(client: DatabaseClient, accountId: string): Promise<number> {
  const id = requiredText(accountId, 256, 'accountId')
  const accounts = businessTable(client, 'accounts')
  const row = await client.one<{ dispatch_revision: number | bigint | string }>(
    `SELECT dispatch_revision FROM ${accounts} WHERE id = ?`, [id]
  )
  if (!row) throw new Error(`AI 账户不存在：${id}`)
  return integerValue(row.dispatch_revision, 'dispatch_revision')
}

export async function getAccountCircuitIncidentByScopeKeyInClient(
  client: DatabaseClient,
  circuitScopeKey: string
): Promise<AccountCircuitIncidentRecord | undefined> {
  const scopeKey = requiredText(circuitScopeKey, 2048, 'circuitScopeKey')
  const incidents = businessTable(client, 'account_circuit_incidents')
  const row = await client.one<AccountCircuitIncidentRow>(`
    SELECT ${incidentColumnList}
    FROM ${incidents} circuit_incident
    WHERE circuit_scope_key = ?
  `, [scopeKey])
  return row ? mapIncidentRow(row) : undefined
}

export async function compareAndSetAccountCircuitIncidentInClient(
  client: DatabaseClient,
  rawInput: CompareAndSetAccountCircuitIncidentInput
): Promise<CompareAndSetAccountCircuitIncidentResult> {
  const input = normalizeIncidentMutation(rawInput)
  return client.transaction(async (tx) => {
    const account = await lockAccountDispatchRevision(tx, input.accountId)
    // Physical cleanup cascades the circuit ledger after logical deletion. A
    // late runtime observation must be terminal instead of becoming a retry.
    if (!account) return { status: 'account_not_found', currentDispatchRevision: 0 }
    const currentDispatchRevision = integerValue(account.dispatch_revision, 'dispatch_revision')
    if (account.deleted_at !== null) {
      return { status: 'account_not_found', currentDispatchRevision }
    }
    if (currentDispatchRevision !== input.dispatchRevision) {
      return { status: 'stale_dispatch_revision', currentDispatchRevision }
    }

    const dedupeKey = `incident:${input.transitionId}`
    const replay = await findOutboxByDedupeKey(tx, dedupeKey)
    if (replay) {
      assertOutboxReplayIdentity(replay, 'incident_changed', input.accountId, input.accountRuntimeKey, input.circuitScopeKey)
      return {
        status: 'idempotent',
        currentDispatchRevision,
        incident: await findIncidentByScopeKey(tx, input.circuitScopeKey)
      }
    }

    const current = await lockIncidentByScopeKey(tx, input.circuitScopeKey)
    const currentLedgerRevision = current?.ledgerRevision
    if ((input.expectedLedgerRevision === null && current)
      || (input.expectedLedgerRevision !== null && currentLedgerRevision !== input.expectedLedgerRevision)) {
      return { status: 'cas_conflict', currentDispatchRevision, incident: current }
    }
    if (current && current.accountId !== input.accountId) {
      throw new Error('账户 circuit scope key 已被其他账户占用')
    }
    if (current && current.generation > input.generation) {
      return { status: 'cas_conflict', currentDispatchRevision, incident: current }
    }

    const ledgerRevision = (currentLedgerRevision ?? 0) + 1
    const incident = await upsertIncident(tx, input, current, ledgerRevision)
    await insertOutbox(tx, {
      eventId: randomUUID(),
      dedupeKey,
      eventType: 'incident_changed',
      accountId: input.accountId,
      accountRuntimeKey: input.accountRuntimeKey,
      circuitScopeKey: input.circuitScopeKey,
      incidentId: input.incidentId,
      transitionId: input.transitionId,
      dispatchRevision: input.dispatchRevision,
      generation: input.generation,
      ledgerRevision,
      nowMs: input.nowMs
    })
    return { status: 'applied', currentDispatchRevision, incident }
  })
}

export async function claimAccountCircuitOutbox(
  input: { ownerId: string; nowMs?: number; leaseMs: number; limit: number }
): Promise<AccountCircuitOutboxRecord[]> {
  return claimAccountCircuitOutboxInClient(await accountCircuitDatabaseClient(), input)
}

export async function claimAccountCircuitOutboxInClient(
  client: DatabaseClient,
  input: { ownerId: string; nowMs?: number; leaseMs: number; limit: number }
): Promise<AccountCircuitOutboxRecord[]> {
  const ownerId = requiredText(input.ownerId, 128, 'ownerId')
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const leaseMs = positiveInteger(input.leaseMs, 'leaseMs', 60 * 60_000)
  const limit = positiveInteger(input.limit, 'limit', 500)
  return client.transaction(async (tx) => {
    const outbox = businessTable(tx, 'account_circuit_outbox')
    const rows = await tx.query<AccountCircuitOutboxRow>(`
      SELECT ${outboxColumnList}
      FROM ${outbox}
      WHERE (status = 'pending' AND available_at_ms <= ?)
         OR (status = 'processing' AND claim_until_ms <= ?)
      ORDER BY available_at_ms ASC, created_at_ms ASC, event_id ASC
      LIMIT ?${tx.driver === 'postgres' ? ' FOR UPDATE SKIP LOCKED' : ''}
    `, [nowMs, nowMs, limit])
    const claimed: AccountCircuitOutboxRecord[] = []
    for (const row of rows) {
      const claimToken = randomUUID()
      const claimUntilMs = nowMs + leaseMs
      const result = await tx.execute(`
        UPDATE ${outbox}
        SET status = 'processing', claim_token = ?, claimed_by = ?, claim_until_ms = ?,
            attempt_count = attempt_count + 1, updated_at_ms = ?
        WHERE event_id = ?
          AND ((status = 'pending' AND available_at_ms <= ?)
            OR (status = 'processing' AND claim_until_ms <= ?))
      `, [claimToken, ownerId, claimUntilMs, nowMs, row.event_id, nowMs, nowMs])
      if (result.changes !== 1) continue
      claimed.push(mapOutboxRow({
        ...row,
        status: 'processing',
        claim_token: claimToken,
        claimed_by: ownerId,
        claim_until_ms: claimUntilMs,
        attempt_count: integerValue(row.attempt_count, 'attempt_count') + 1,
        updated_at_ms: nowMs
      }))
    }
    return claimed
  })
}

export async function acknowledgeAccountCircuitOutbox(
  input: { eventId: string; projectionKey: string; claimToken: string; acknowledgedAtMs?: number }
): Promise<boolean> {
  return acknowledgeAccountCircuitOutboxInClient(await accountCircuitDatabaseClient(), input)
}

export async function acknowledgeAccountCircuitOutboxInClient(
  client: DatabaseClient,
  input: { eventId: string; projectionKey: string; claimToken: string; acknowledgedAtMs?: number }
): Promise<boolean> {
  const eventId = requiredText(input.eventId, 256, 'eventId')
  const projectionKey = requiredText(input.projectionKey, 128, 'projectionKey')
  const claimToken = requiredText(input.claimToken, 256, 'claimToken')
  const acknowledgedAtMs = nonNegativeInteger(input.acknowledgedAtMs ?? Date.now(), 'acknowledgedAtMs')
  return client.transaction(async (tx) => {
    const outbox = businessTable(tx, 'account_circuit_outbox')
    const row = await tx.one<AccountCircuitOutboxRow>(`
      SELECT ${outboxColumnList}
      FROM ${outbox}
      WHERE event_id = ?${tx.driver === 'postgres' ? ' FOR UPDATE' : ''}
    `, [eventId])
    if (!row || row.projection_key !== projectionKey) {
      return false
    }
    if (row.status === 'dispatched') return true
    if (row.status !== 'processing' || row.claim_token !== claimToken) return false
    const updated = await tx.execute(`
      UPDATE ${outbox}
      SET status = 'dispatched', claim_token = NULL, claimed_by = NULL, claim_until_ms = NULL,
          acknowledged_at_ms = ?, last_error_class = NULL, updated_at_ms = ?
      WHERE event_id = ? AND status = 'processing' AND claim_token = ? AND projection_key = ?
    `, [acknowledgedAtMs, acknowledgedAtMs, eventId, claimToken, projectionKey])
    if (updated.changes !== 1) return false

    const dispatchRevision = integerValue(row.dispatch_revision, 'dispatch_revision')
    if (row.event_type === 'dispatch_revision_changed') {
      const accounts = businessTable(tx, 'accounts')
      await tx.execute(`
        UPDATE ${accounts}
        SET circuit_projection_revision = CASE
          WHEN circuit_projection_revision < ? THEN ?
          ELSE circuit_projection_revision
        END
        WHERE id = ? AND dispatch_revision >= ?
      `, [dispatchRevision, dispatchRevision, row.account_id, dispatchRevision])
    } else if (row.event_type === 'incident_changed' && row.circuit_scope_key && row.incident_id && row.ledger_revision !== null) {
      const incidents = businessTable(tx, 'account_circuit_incidents')
      const ledgerRevision = integerValue(row.ledger_revision, 'ledger_revision')
      await tx.execute(`
        UPDATE ${incidents}
        SET projected_ledger_revision = CASE
          WHEN projected_ledger_revision < ? THEN ?
          ELSE projected_ledger_revision
        END
        WHERE circuit_scope_key = ? AND incident_id = ? AND ledger_revision >= ?
      `, [ledgerRevision, ledgerRevision, row.circuit_scope_key, row.incident_id, ledgerRevision])
    }
    return true
  })
}

export async function releaseAccountCircuitOutboxForReplay(
  input: { eventId: string; claimToken: string; errorClass: string; nowMs?: number; retryDelayMs: number }
): Promise<boolean> {
  return releaseAccountCircuitOutboxForReplayInClient(await accountCircuitDatabaseClient(), input)
}

export async function releaseAccountCircuitOutboxForReplayInClient(
  client: DatabaseClient,
  input: { eventId: string; claimToken: string; errorClass: string; nowMs?: number; retryDelayMs: number }
): Promise<boolean> {
  const eventId = requiredText(input.eventId, 256, 'eventId')
  const claimToken = requiredText(input.claimToken, 256, 'claimToken')
  const errorClass = normalizedErrorClass(input.errorClass)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const retryDelayMs = nonNegativeInteger(input.retryDelayMs, 'retryDelayMs', 24 * 60 * 60_000)
  const clientOutbox = businessTable(client, 'account_circuit_outbox')
  const result = await client.execute(`
    UPDATE ${clientOutbox}
    SET status = 'pending', available_at_ms = ?, claim_token = NULL, claimed_by = NULL,
        claim_until_ms = NULL, last_error_class = ?, updated_at_ms = ?
    WHERE event_id = ? AND status = 'processing' AND claim_token = ?
  `, [nowMs + retryDelayMs, errorClass, nowMs, eventId, claimToken])
  return result.changes === 1
}

export async function listAccountCircuitIncidentsForRebuild(
  input: { nowMs?: number; afterUpdatedAtMs?: number; afterCircuitScopeKey?: string; limit: number }
): Promise<AccountCircuitIncidentRebuildPage> {
  return listAccountCircuitIncidentsForRebuildInClient(await accountCircuitDatabaseClient(), input)
}

export async function listAccountCircuitIncidentsByRuntimeKeys(
  accountRuntimeKeys: string[],
  options: { includeRetainedClosed?: boolean; nowMs?: number } = {}
): Promise<AccountCircuitIncidentRecord[]> {
  return listAccountCircuitIncidentsByRuntimeKeysInClient(await accountCircuitDatabaseClient(), accountRuntimeKeys, options)
}

export async function listAccountCircuitIncidentsByRuntimeKeysInClient(
  client: DatabaseClient,
  accountRuntimeKeys: string[],
  options: { includeRetainedClosed?: boolean; nowMs?: number } = {}
): Promise<AccountCircuitIncidentRecord[]> {
  const keys = [...new Set(accountRuntimeKeys.map((key) => requiredText(key, 1024, 'accountRuntimeKey')))]
  if (keys.length === 0) return []
  if (keys.length > 100) throw new Error('账户 circuit 摘要单次最多查询 100 个运行态键')
  const incidents = businessTable(client, 'account_circuit_incidents')
  const accounts = businessTable(client, 'accounts')
  const includeRetainedClosed = options.includeRetainedClosed === true
  const nowMs = nonNegativeInteger(options.nowMs ?? Date.now(), 'nowMs')
  const rows = await client.query<AccountCircuitIncidentRow>(`
    SELECT ${incidentColumnList}
    FROM ${incidents} circuit_incident
    WHERE account_runtime_key IN (${keys.map(() => '?').join(', ')})
      AND ${includeRetainedClosed ? '(state <> \'CLOSED\' OR retained_until_ms > ?)' : "state <> 'CLOSED'"}
      AND dispatch_revision = (
        SELECT current_account.dispatch_revision
        FROM ${accounts} current_account
        WHERE current_account.id = circuit_incident.account_id
      )
    ORDER BY account_runtime_key ASC, updated_at_ms ASC, circuit_scope_key ASC
  `, includeRetainedClosed ? [...keys, nowMs] : keys)
  return rows.map(mapIncidentRow)
}

export async function listAccountCircuitIncidentsForRebuildInClient(
  client: DatabaseClient,
  input: { nowMs?: number; afterUpdatedAtMs?: number; afterCircuitScopeKey?: string; limit: number }
): Promise<AccountCircuitIncidentRebuildPage> {
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const afterUpdatedAtMs = integerAtLeast(input.afterUpdatedAtMs ?? -1, -1, 'afterUpdatedAtMs')
  const afterCircuitScopeKey = optionalCursorText(input.afterCircuitScopeKey, 2048, 'afterCircuitScopeKey')
  const limit = positiveInteger(input.limit, 'limit', 500)
  const incidents = businessTable(client, 'account_circuit_incidents')
  const accounts = businessTable(client, 'accounts')
  const rows = await client.query<AccountCircuitIncidentRow>(`
    SELECT ${incidentColumnList}
    FROM ${incidents} circuit_incident
    WHERE (state <> 'CLOSED' OR retained_until_ms > ?)
      AND dispatch_revision = (
        SELECT current_account.dispatch_revision
        FROM ${accounts} current_account
        WHERE current_account.id = circuit_incident.account_id
      )
      AND (updated_at_ms > ? OR (updated_at_ms = ? AND circuit_scope_key > ?))
    ORDER BY updated_at_ms ASC, circuit_scope_key ASC
    LIMIT ?
  `, [nowMs, afterUpdatedAtMs, afterUpdatedAtMs, afterCircuitScopeKey, limit])
  const items = rows.map(mapIncidentRow)
  const last = items[items.length - 1]
  return {
    items,
    ...(last && items.length === limit
      ? { nextCursor: { updatedAtMs: last.updatedAtMs, circuitScopeKey: last.circuitScopeKey } }
      : {})
  }
}

export async function listAccountCircuitProjectionGaps(
  input: { afterAccountId?: string; afterUpdatedAtMs?: number; afterCircuitScopeKey?: string; limit: number }
): Promise<AccountCircuitProjectionGaps> {
  return listAccountCircuitProjectionGapsInClient(await accountCircuitDatabaseClient(), input)
}

export async function listAccountCircuitProjectionGapsInClient(
  client: DatabaseClient,
  input: { afterAccountId?: string; afterUpdatedAtMs?: number; afterCircuitScopeKey?: string; limit: number }
): Promise<AccountCircuitProjectionGaps> {
  const afterAccountId = optionalCursorText(input.afterAccountId, 256, 'afterAccountId')
  const afterUpdatedAtMs = integerAtLeast(input.afterUpdatedAtMs ?? -1, -1, 'afterUpdatedAtMs')
  const afterCircuitScopeKey = optionalCursorText(input.afterCircuitScopeKey, 2048, 'afterCircuitScopeKey')
  const limit = positiveInteger(input.limit, 'limit', 500)
  const accounts = businessTable(client, 'accounts')
  const incidents = businessTable(client, 'account_circuit_incidents')
  const [accountRows, incidentRows] = await Promise.all([
    client.query<AccountDispatchRevisionRow>(`
      SELECT id, dispatch_revision, circuit_projection_revision
      FROM ${accounts}
      WHERE circuit_projection_revision < dispatch_revision AND id > ?
      ORDER BY id ASC
      LIMIT ?
    `, [afterAccountId, limit]),
    client.query<AccountCircuitIncidentRow>(`
      SELECT ${incidentColumnList}
      FROM ${incidents} circuit_incident
      WHERE projected_ledger_revision < ledger_revision
        AND dispatch_revision = (
          SELECT current_account.dispatch_revision
          FROM ${accounts} current_account
          WHERE current_account.id = circuit_incident.account_id
        )
        AND (updated_at_ms > ? OR (updated_at_ms = ? AND circuit_scope_key > ?))
      ORDER BY updated_at_ms ASC, circuit_scope_key ASC
      LIMIT ?
    `, [afterUpdatedAtMs, afterUpdatedAtMs, afterCircuitScopeKey, limit])
  ])
  return {
    dispatchRevisionGaps: accountRows.map((row) => ({
      accountId: row.id,
      dispatchRevision: integerValue(row.dispatch_revision, 'dispatch_revision'),
      projectedDispatchRevision: integerValue(row.circuit_projection_revision, 'circuit_projection_revision')
    })),
    incidentGaps: incidentRows.map(mapIncidentRow)
  }
}

export async function cleanupAccountCircuitControlPlane(
  input: { nowMs?: number; outboxAcknowledgedBeforeMs: number; limit: number }
): Promise<AccountCircuitControlPlaneCleanupResult> {
  return cleanupAccountCircuitControlPlaneInClient(await accountCircuitDatabaseClient(), input)
}

export async function cleanupAccountCircuitControlPlaneInClient(
  client: DatabaseClient,
  input: { nowMs?: number; outboxAcknowledgedBeforeMs: number; limit: number }
): Promise<AccountCircuitControlPlaneCleanupResult> {
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const outboxAcknowledgedBeforeMs = nonNegativeInteger(input.outboxAcknowledgedBeforeMs, 'outboxAcknowledgedBeforeMs')
  const limit = positiveInteger(input.limit, 'limit', 500)
  return client.transaction(async (tx) => {
    const outbox = businessTable(tx, 'account_circuit_outbox')
    const incidents = businessTable(tx, 'account_circuit_incidents')
    const outboxRows = await tx.query<{ event_id: string }>(`
      SELECT event_id
      FROM ${outbox}
      WHERE status = 'dispatched' AND acknowledged_at_ms <= ?
      ORDER BY acknowledged_at_ms ASC, event_id ASC
      LIMIT ?${tx.driver === 'postgres' ? ' FOR UPDATE SKIP LOCKED' : ''}
    `, [outboxAcknowledgedBeforeMs, limit])
    const deletedOutbox = await deleteRowsByTextIds(tx, outbox, 'event_id', outboxRows.map((row) => row.event_id))

    const incidentRows = await tx.query<{ circuit_scope_key: string }>(`
      SELECT incidents.circuit_scope_key
      FROM ${incidents} incidents
      WHERE incidents.state = 'CLOSED'
        AND incidents.retained_until_ms <= ?
        AND incidents.projected_ledger_revision >= incidents.ledger_revision
        AND NOT EXISTS (
          SELECT 1
          FROM ${outbox} pending_outbox
          WHERE pending_outbox.circuit_scope_key = incidents.circuit_scope_key
            AND pending_outbox.status <> 'dispatched'
        )
      ORDER BY incidents.retained_until_ms ASC, incidents.updated_at_ms ASC, incidents.circuit_scope_key ASC
      LIMIT ?${tx.driver === 'postgres' ? ' FOR UPDATE OF incidents SKIP LOCKED' : ''}
    `, [nowMs, limit])
    const deletedIncidents = await deleteRowsByTextIds(
      tx,
      incidents,
      'circuit_scope_key',
      incidentRows.map((row) => row.circuit_scope_key)
    )
    return { deletedIncidents, deletedOutbox }
  })
}

async function lockAccountDispatchRevision(client: DatabaseClient, accountId: string): Promise<AccountDispatchRevisionRow | undefined> {
  const accounts = businessTable(client, 'accounts')
  return client.one<AccountDispatchRevisionRow>(`
    SELECT id, dispatch_revision, circuit_projection_revision, deleted_at
    FROM ${accounts}
    WHERE id = ?${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [accountId])
}

async function findOutboxByDedupeKey(client: DatabaseClient, dedupeKey: string): Promise<AccountCircuitOutboxRow | undefined> {
  const outbox = businessTable(client, 'account_circuit_outbox')
  return client.one<AccountCircuitOutboxRow>(`
    SELECT ${outboxColumnList}
    FROM ${outbox}
    WHERE projection_key = ? AND dedupe_key = ?
  `, [accountCircuitProjectionKey, dedupeKey])
}

async function findIncidentByScopeKey(client: DatabaseClient, circuitScopeKey: string): Promise<AccountCircuitIncidentRecord | undefined> {
  const incidents = businessTable(client, 'account_circuit_incidents')
  const row = await client.one<AccountCircuitIncidentRow>(`
    SELECT ${incidentColumnList}
    FROM ${incidents}
    WHERE circuit_scope_key = ?
  `, [circuitScopeKey])
  return row ? mapIncidentRow(row) : undefined
}

async function lockIncidentByScopeKey(client: DatabaseClient, circuitScopeKey: string): Promise<AccountCircuitIncidentRecord | undefined> {
  const incidents = businessTable(client, 'account_circuit_incidents')
  const row = await client.one<AccountCircuitIncidentRow>(`
    SELECT ${incidentColumnList}
    FROM ${incidents}
    WHERE circuit_scope_key = ?${client.driver === 'postgres' ? ' FOR UPDATE' : ''}
  `, [circuitScopeKey])
  return row ? mapIncidentRow(row) : undefined
}

async function upsertIncident(
  client: DatabaseClient,
  input: NormalizedIncidentMutation,
  current: AccountCircuitIncidentRecord | undefined,
  ledgerRevision: number
): Promise<AccountCircuitIncidentRecord> {
  const incidents = businessTable(client, 'account_circuit_incidents')
  const projectedLedgerRevision = current?.projectedLedgerRevision ?? 0
  const createdAtMs = current?.createdAtMs ?? input.stateUpdatedAtMs
  const params = [
    input.circuitScopeKey,
    input.accountId,
    input.accountRuntimeKey,
    input.scopeKind,
    input.keyFingerprint ?? null,
    input.protocolCode ?? null,
    input.requestLane ?? null,
    input.modelFamily ?? null,
    input.clientModel ?? null,
    input.capabilityHash ?? null,
    input.credentialSourceAccountId ?? null,
    input.clientEndpointFamily ?? null,
    input.finalUpstreamModel ?? null,
    input.upstreamEndpointMode ?? null,
    input.incidentId,
    input.parentIncidentId ?? null,
    JSON.stringify(input.childIncidentIds),
    input.causedByTerminalOutcomeId ?? null,
    input.state,
    input.failureScope ?? null,
    input.generation,
    input.dispatchRevision,
    ledgerRevision,
    projectedLedgerRevision,
    input.transitionId,
    input.cooldownObservationGeneration,
    input.openUntilMs ?? null,
    input.nextTransitionAtMs ?? null,
    input.leaseId ?? null,
    input.leasePurpose ?? null,
    input.leaseOwnerRunId ?? null,
    input.leaseUntilMs ?? null,
    input.attemptStartedAtMs ?? null,
    input.attemptHardDeadlineMs ?? null,
    input.upstreamAttemptObserved ? 1 : 0,
    input.backoffLevel,
    input.consecutiveFailures,
    input.confirmationFailuresRequired,
    JSON.stringify(input.confirmationFailureEvidenceKeys),
    input.recoveringSuccesses,
    input.lastFailureClass ?? null,
    input.retainedUntilMs ?? null,
    createdAtMs,
    input.stateUpdatedAtMs
  ]
  const placeholders = params.map(() => '?').join(', ')
  const row = await client.one<AccountCircuitIncidentRow>(`
    INSERT INTO ${incidents} (${incidentColumnList})
    VALUES (${placeholders})
    ON CONFLICT (circuit_scope_key) DO UPDATE SET
      account_id = excluded.account_id,
      account_runtime_key = excluded.account_runtime_key,
      scope_kind = excluded.scope_kind,
      key_fingerprint = excluded.key_fingerprint,
      protocol_code = excluded.protocol_code,
      request_lane = excluded.request_lane,
      model_family = excluded.model_family,
      client_model = excluded.client_model,
      capability_hash = excluded.capability_hash,
      credential_source_account_id = excluded.credential_source_account_id,
      client_endpoint_family = excluded.client_endpoint_family,
      final_upstream_model = excluded.final_upstream_model,
      upstream_endpoint_mode = excluded.upstream_endpoint_mode,
      incident_id = excluded.incident_id,
      parent_incident_id = excluded.parent_incident_id,
      child_incident_ids_json = excluded.child_incident_ids_json,
      caused_by_terminal_outcome_id = excluded.caused_by_terminal_outcome_id,
      state = excluded.state,
      failure_scope = excluded.failure_scope,
      generation = excluded.generation,
      dispatch_revision = excluded.dispatch_revision,
      ledger_revision = excluded.ledger_revision,
      projected_ledger_revision = excluded.projected_ledger_revision,
      transition_id = excluded.transition_id,
      cooldown_observation_generation = excluded.cooldown_observation_generation,
      open_until_ms = excluded.open_until_ms,
      next_transition_at_ms = excluded.next_transition_at_ms,
      lease_id = excluded.lease_id,
      lease_purpose = excluded.lease_purpose,
      lease_owner_run_id = excluded.lease_owner_run_id,
      lease_until_ms = excluded.lease_until_ms,
      attempt_started_at_ms = excluded.attempt_started_at_ms,
      attempt_hard_deadline_ms = excluded.attempt_hard_deadline_ms,
      upstream_attempt_observed = excluded.upstream_attempt_observed,
      backoff_level = excluded.backoff_level,
      consecutive_failures = excluded.consecutive_failures,
      confirmation_failures_required = excluded.confirmation_failures_required,
      confirmation_failure_evidence_keys_json = excluded.confirmation_failure_evidence_keys_json,
      recovering_successes = excluded.recovering_successes,
      last_failure_class = excluded.last_failure_class,
      retained_until_ms = excluded.retained_until_ms,
      updated_at_ms = excluded.updated_at_ms
    RETURNING ${incidentColumnList}
  `, params)
  if (!row) throw new Error('账户 circuit incident 写入后未返回记录')
  return mapIncidentRow(row)
}

async function insertOutbox(client: DatabaseClient, input: {
  eventId: string
  dedupeKey: string
  eventType: AccountCircuitOutboxEventType
  accountId: string
  accountRuntimeKey: string
  circuitScopeKey?: string
  incidentId?: string
  transitionId: string
  dispatchRevision: number
  generation?: number
  ledgerRevision?: number
  nowMs: number
}): Promise<void> {
  const outbox = businessTable(client, 'account_circuit_outbox')
  await client.execute(`
    INSERT INTO ${outbox} (
      event_id, projection_key, dedupe_key, event_type, account_id, account_runtime_key,
      circuit_scope_key, incident_id, transition_id, dispatch_revision, generation,
      ledger_revision, status, available_at_ms, attempt_count, created_at_ms, updated_at_ms
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, 0, ?, ?)
  `, [
    input.eventId,
    accountCircuitProjectionKey,
    input.dedupeKey,
    input.eventType,
    input.accountId,
    input.accountRuntimeKey,
    input.circuitScopeKey ?? null,
    input.incidentId ?? null,
    input.transitionId,
    input.dispatchRevision,
    input.generation ?? null,
    input.ledgerRevision ?? null,
    input.nowMs,
    input.nowMs,
    input.nowMs
  ])
}

function assertOutboxReplayIdentity(
  row: AccountCircuitOutboxRow,
  eventType: AccountCircuitOutboxEventType,
  accountId: string,
  accountRuntimeKey: string,
  circuitScopeKey?: string
): void {
  if (row.event_type !== eventType
    || row.account_id !== accountId
    || row.account_runtime_key !== accountRuntimeKey
    || (circuitScopeKey !== undefined && row.circuit_scope_key !== circuitScopeKey)) {
    throw new Error('账户 circuit outbox dedupe key 与既有事件身份冲突')
  }
}

function mapIncidentRow(row: AccountCircuitIncidentRow): AccountCircuitIncidentRecord {
  return {
    circuitScopeKey: row.circuit_scope_key,
    accountId: row.account_id,
    accountRuntimeKey: row.account_runtime_key,
    scopeKind: row.scope_kind as AccountCircuitScopeKind,
    ...(row.key_fingerprint ? { keyFingerprint: row.key_fingerprint } : {}),
    ...(row.protocol_code ? { protocolCode: row.protocol_code } : {}),
    ...(row.request_lane ? { requestLane: row.request_lane } : {}),
    ...(row.model_family ? { modelFamily: row.model_family } : {}),
    ...(row.client_model ? { clientModel: row.client_model } : {}),
    ...(row.capability_hash ? { capabilityHash: row.capability_hash } : {}),
    ...(row.credential_source_account_id ? { credentialSourceAccountId: row.credential_source_account_id } : {}),
    ...(row.client_endpoint_family ? { clientEndpointFamily: row.client_endpoint_family } : {}),
    ...(row.final_upstream_model ? { finalUpstreamModel: row.final_upstream_model } : {}),
    ...(row.upstream_endpoint_mode ? { upstreamEndpointMode: row.upstream_endpoint_mode } : {}),
    incidentId: row.incident_id,
    ...(row.parent_incident_id ? { parentIncidentId: row.parent_incident_id } : {}),
    childIncidentIds: parseBoundedIdArray(row.child_incident_ids_json),
    ...(row.caused_by_terminal_outcome_id ? { causedByTerminalOutcomeId: row.caused_by_terminal_outcome_id } : {}),
    state: row.state as AccountCircuitIncidentState,
    ...(row.failure_scope ? { failureScope: row.failure_scope as AccountCircuitScopeKind } : {}),
    generation: integerValue(row.generation, 'generation'),
    dispatchRevision: integerValue(row.dispatch_revision, 'dispatch_revision'),
    ledgerRevision: integerValue(row.ledger_revision, 'ledger_revision'),
    projectedLedgerRevision: integerValue(row.projected_ledger_revision, 'projected_ledger_revision'),
    transitionId: row.transition_id,
    cooldownObservationGeneration: integerValue(row.cooldown_observation_generation, 'cooldown_observation_generation'),
    ...optionalIntegerProperty('openUntilMs', row.open_until_ms),
    ...optionalIntegerProperty('nextTransitionAtMs', row.next_transition_at_ms),
    ...(row.lease_id ? { leaseId: row.lease_id } : {}),
    ...(row.lease_purpose ? { leasePurpose: row.lease_purpose as AccountCircuitLeasePurpose } : {}),
    ...(row.lease_owner_run_id ? { leaseOwnerRunId: row.lease_owner_run_id } : {}),
    ...optionalIntegerProperty('leaseUntilMs', row.lease_until_ms),
    ...optionalIntegerProperty('attemptStartedAtMs', row.attempt_started_at_ms),
    ...optionalIntegerProperty('attemptHardDeadlineMs', row.attempt_hard_deadline_ms),
    upstreamAttemptObserved: booleanValue(row.upstream_attempt_observed),
    backoffLevel: integerValue(row.backoff_level, 'backoff_level'),
    consecutiveFailures: integerValue(row.consecutive_failures, 'consecutive_failures'),
    confirmationFailuresRequired: integerValue(row.confirmation_failures_required, 'confirmation_failures_required'),
    confirmationFailureEvidenceKeys: parseConfirmationFailureEvidenceKeys(
      row.confirmation_failure_evidence_keys_json,
      integerValue(row.confirmation_failures_required, 'confirmation_failures_required')
    ),
    recoveringSuccesses: integerValue(row.recovering_successes, 'recovering_successes'),
    ...(row.last_failure_class ? { lastFailureClass: row.last_failure_class as AccountCircuitFailureClass } : {}),
    ...optionalIntegerProperty('retainedUntilMs', row.retained_until_ms),
    createdAtMs: integerValue(row.created_at_ms, 'created_at_ms'),
    updatedAtMs: integerValue(row.updated_at_ms, 'updated_at_ms')
  }
}

function mapOutboxRow(row: AccountCircuitOutboxRow): AccountCircuitOutboxRecord {
  return {
    eventId: row.event_id,
    projectionKey: row.projection_key,
    dedupeKey: row.dedupe_key,
    eventType: row.event_type as AccountCircuitOutboxEventType,
    accountId: row.account_id,
    accountRuntimeKey: row.account_runtime_key,
    ...(row.circuit_scope_key ? { circuitScopeKey: row.circuit_scope_key } : {}),
    ...(row.incident_id ? { incidentId: row.incident_id } : {}),
    transitionId: row.transition_id,
    dispatchRevision: integerValue(row.dispatch_revision, 'dispatch_revision'),
    ...optionalIntegerProperty('generation', row.generation),
    ...optionalIntegerProperty('ledgerRevision', row.ledger_revision),
    status: row.status as AccountCircuitOutboxStatus,
    availableAtMs: integerValue(row.available_at_ms, 'available_at_ms'),
    ...(row.claim_token ? { claimToken: row.claim_token } : {}),
    ...(row.claimed_by ? { claimedBy: row.claimed_by } : {}),
    ...optionalIntegerProperty('claimUntilMs', row.claim_until_ms),
    attemptCount: integerValue(row.attempt_count, 'attempt_count'),
    ...(row.last_error_class ? { lastErrorClass: row.last_error_class } : {}),
    ...optionalIntegerProperty('acknowledgedAtMs', row.acknowledged_at_ms),
    createdAtMs: integerValue(row.created_at_ms, 'created_at_ms'),
    updatedAtMs: integerValue(row.updated_at_ms, 'updated_at_ms')
  }
}

function normalizeDispatchRevisionInput(input: AdvanceAccountCircuitDispatchRevisionInput): Required<AdvanceAccountCircuitDispatchRevisionInput> {
  return {
    accountId: requiredText(input.accountId, 256, 'accountId'),
    accountRuntimeKey: requiredText(input.accountRuntimeKey, 1024, 'accountRuntimeKey'),
    transitionId: requiredText(input.transitionId, 256, 'transitionId'),
    nowMs: nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  }
}

function familyDispatchTransitionId(transitionId: string, accountId: string): string {
  return `dispatch-family:${createHash('sha256').update(transitionId).update('\0').update(accountId).digest('hex')}`
}

function normalizeIncidentMutation(input: CompareAndSetAccountCircuitIncidentInput): NormalizedIncidentMutation {
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const stateUpdatedAtMs = nonNegativeInteger(input.stateUpdatedAtMs ?? nowMs, 'stateUpdatedAtMs')
  const state = incidentState(input.state)
  const scopeKind = circuitScopeKind(input.scopeKind, 'scopeKind')
  const keyFingerprint = optionalText(input.keyFingerprint, 256, 'keyFingerprint')
  const protocolCode = optionalText(input.protocolCode, 64, 'protocolCode')
  const requestLane = optionalText(input.requestLane, 64, 'requestLane')
  const modelFamily = optionalText(input.modelFamily, 256, 'modelFamily')
  const clientModel = optionalText(input.clientModel, 256, 'clientModel')
  const capabilityHash = optionalText(input.capabilityHash, 128, 'capabilityHash')
  const credentialSourceAccountId = optionalText(input.credentialSourceAccountId, 256, 'credentialSourceAccountId')
  const clientEndpointFamily = optionalText(input.clientEndpointFamily, 128, 'clientEndpointFamily')
  const finalUpstreamModel = optionalText(input.finalUpstreamModel, 256, 'finalUpstreamModel')
  const upstreamEndpointMode = optionalText(input.upstreamEndpointMode, 128, 'upstreamEndpointMode')
  assertScopeShape(scopeKind, {
    keyFingerprint,
    protocolCode,
    requestLane,
    modelFamily,
    clientModel,
    capabilityHash,
    credentialSourceAccountId,
    clientEndpointFamily,
    finalUpstreamModel,
    upstreamEndpointMode
  })
  const retainedUntilMs = optionalNonNegativeInteger(input.retainedUntilMs, 'retainedUntilMs')
  if (state === 'CLOSED') {
    if (retainedUntilMs === undefined || retainedUntilMs < nowMs) {
      throw new Error('CLOSED tombstone 必须提供不早于 nowMs 的 retainedUntilMs')
    }
  } else if (retainedUntilMs !== undefined) {
    throw new Error('仅 CLOSED tombstone 可以设置 retainedUntilMs')
  }
  const leaseId = optionalText(input.leaseId, 256, 'leaseId')
  const leasePurpose = input.leasePurpose === undefined ? undefined : leasePurposeValue(input.leasePurpose)
  const leaseOwnerRunId = optionalText(input.leaseOwnerRunId, 256, 'leaseOwnerRunId')
  const leaseUntilMs = optionalNonNegativeInteger(input.leaseUntilMs, 'leaseUntilMs')
  const leaseFieldCount = [leaseId, leasePurpose, leaseOwnerRunId, leaseUntilMs].filter((value) => value !== undefined).length
  if (leaseFieldCount !== 0 && leaseFieldCount !== 4) {
    throw new Error('leaseId / leasePurpose / leaseOwnerRunId / leaseUntilMs 必须同时提供或同时省略')
  }
  const confirmationFailuresRequired = positiveInteger(
    input.confirmationFailuresRequired ?? 1,
    'confirmationFailuresRequired',
    5
  )
  const consecutiveFailures = nonNegativeInteger(input.consecutiveFailures ?? 0, 'consecutiveFailures', 5)
  if (consecutiveFailures > confirmationFailuresRequired) {
    throw new Error('consecutiveFailures 不能超过 confirmationFailuresRequired')
  }
  const confirmationFailureEvidenceKeys = confirmationEvidenceKeys(
    input.confirmationFailureEvidenceKeys ?? [],
    confirmationFailuresRequired
  )
  return {
    accountId: requiredText(input.accountId, 256, 'accountId'),
    accountRuntimeKey: requiredText(input.accountRuntimeKey, 1024, 'accountRuntimeKey'),
    circuitScopeKey: requiredText(input.circuitScopeKey, 2048, 'circuitScopeKey'),
    scopeKind,
    ...(keyFingerprint ? { keyFingerprint } : {}),
    ...(protocolCode ? { protocolCode } : {}),
    ...(requestLane ? { requestLane } : {}),
    ...(modelFamily ? { modelFamily } : {}),
    ...(clientModel ? { clientModel } : {}),
    ...(capabilityHash ? { capabilityHash } : {}),
    ...(credentialSourceAccountId ? { credentialSourceAccountId } : {}),
    ...(clientEndpointFamily ? { clientEndpointFamily } : {}),
    ...(finalUpstreamModel ? { finalUpstreamModel } : {}),
    ...(upstreamEndpointMode ? { upstreamEndpointMode } : {}),
    incidentId: requiredText(input.incidentId, 256, 'incidentId'),
    ...optionalTextProperty('parentIncidentId', input.parentIncidentId, 256),
    childIncidentIds: boundedIdArray(input.childIncidentIds ?? []),
    ...optionalTextProperty('causedByTerminalOutcomeId', input.causedByTerminalOutcomeId, 256),
    state,
    ...(input.failureScope === undefined ? {} : { failureScope: circuitScopeKind(input.failureScope, 'failureScope') }),
    generation: nonNegativeInteger(input.generation, 'generation'),
    dispatchRevision: positiveInteger(input.dispatchRevision, 'dispatchRevision'),
    expectedLedgerRevision: input.expectedLedgerRevision === null
      ? null
      : positiveInteger(input.expectedLedgerRevision, 'expectedLedgerRevision'),
    transitionId: requiredText(input.transitionId, 256, 'transitionId'),
    cooldownObservationGeneration: nonNegativeInteger(input.cooldownObservationGeneration ?? 0, 'cooldownObservationGeneration'),
    ...optionalIntegerInput('openUntilMs', input.openUntilMs),
    ...optionalIntegerInput('nextTransitionAtMs', input.nextTransitionAtMs),
    ...(leaseId ? { leaseId } : {}),
    ...(leasePurpose ? { leasePurpose } : {}),
    ...(leaseOwnerRunId ? { leaseOwnerRunId } : {}),
    ...(leaseUntilMs !== undefined ? { leaseUntilMs } : {}),
    ...optionalIntegerInput('attemptStartedAtMs', input.attemptStartedAtMs),
    ...optionalIntegerInput('attemptHardDeadlineMs', input.attemptHardDeadlineMs),
    upstreamAttemptObserved: input.upstreamAttemptObserved === true,
    backoffLevel: nonNegativeInteger(input.backoffLevel ?? 0, 'backoffLevel'),
    consecutiveFailures,
    confirmationFailuresRequired,
    confirmationFailureEvidenceKeys,
    recoveringSuccesses: nonNegativeInteger(input.recoveringSuccesses ?? 0, 'recoveringSuccesses'),
    ...(input.lastFailureClass === undefined ? {} : { lastFailureClass: failureClass(input.lastFailureClass) }),
    ...(retainedUntilMs !== undefined ? { retainedUntilMs } : {}),
    stateUpdatedAtMs,
    nowMs
  }
}

function boundedIdArray(values: string[]): string[] {
  if (!Array.isArray(values) || values.length > 64) throw new Error('childIncidentIds 最多包含 64 项')
  const normalized = values.map((value, index) => requiredText(value, 256, `childIncidentIds[${index}]`))
  return [...new Set(normalized)]
}

function parseBoundedIdArray(value: string): string[] {
  try {
    const parsed = JSON.parse(value)
    return boundedIdArray(parsed)
  } catch {
    throw new Error('持久化 childIncidentIds 不是合法有界数组')
  }
}

function confirmationEvidenceKeys(values: string[], confirmationFailuresRequired: number): string[] {
  if (!Array.isArray(values) || values.length > confirmationFailuresRequired + 1) {
    throw new Error(`confirmationFailureEvidenceKeys 最多包含 ${confirmationFailuresRequired + 1} 项`)
  }
  const normalized = values.map((value, index) => {
    const key = requiredText(value, 64, `confirmationFailureEvidenceKeys[${index}]`).toLowerCase()
    if (!/^[a-f0-9]{64}$/.test(key)) throw new Error('confirmationFailureEvidenceKeys 只能包含 SHA256')
    return key
  })
  return [...new Set(normalized)]
}

function parseConfirmationFailureEvidenceKeys(value: string, confirmationFailuresRequired: number): string[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    throw new Error('持久化 confirmationFailureEvidenceKeys 不是合法 JSON')
  }
  if (!Array.isArray(parsed) || !parsed.every((item): item is string => typeof item === 'string')) {
    throw new Error('持久化 confirmationFailureEvidenceKeys 不是合法数组')
  }
  return confirmationEvidenceKeys(parsed, confirmationFailuresRequired)
}

function assertScopeShape(
  scopeKind: AccountCircuitScopeKind,
  input: {
    keyFingerprint?: string
    protocolCode?: string
    requestLane?: string
    modelFamily?: string
    clientModel?: string
    capabilityHash?: string
    credentialSourceAccountId?: string
    clientEndpointFamily?: string
    finalUpstreamModel?: string
    upstreamEndpointMode?: string
  }
): void {
  const hasKeyModelFields = input.clientModel && input.capabilityHash && input.credentialSourceAccountId
    && input.clientEndpointFamily && input.finalUpstreamModel && input.upstreamEndpointMode
  if (scopeKind === 'account'
    && !input.keyFingerprint && !input.protocolCode && !input.requestLane && !input.modelFamily
    && !input.clientModel && !input.capabilityHash && !input.credentialSourceAccountId
    && !input.clientEndpointFamily && !input.finalUpstreamModel && !input.upstreamEndpointMode) return
  if (scopeKind === 'key' && input.keyFingerprint && !input.protocolCode && !input.requestLane && !input.modelFamily
    && !input.clientModel && !input.capabilityHash && !input.credentialSourceAccountId
    && !input.clientEndpointFamily && !input.finalUpstreamModel && !input.upstreamEndpointMode) return
  if (scopeKind === 'protocol_model' && !input.keyFingerprint && input.protocolCode && input.requestLane && input.modelFamily
    && !input.clientModel && !input.capabilityHash && !input.credentialSourceAccountId
    && !input.clientEndpointFamily && !input.finalUpstreamModel && !input.upstreamEndpointMode) return
  if (scopeKind === 'key_model' && input.keyFingerprint && hasKeyModelFields
    && !input.protocolCode && !input.requestLane && !input.modelFamily) return
  throw new Error(`账户 circuit ${scopeKind} 作用域字段组合无效`)
}

function incidentState(value: string): AccountCircuitIncidentState {
  const allowed = new Set<AccountCircuitIncidentState>(['CLOSED', 'SUSPECT', 'OPEN', 'HALF_OPEN', 'RECOVERING', 'PERSISTING', 'SHADOWED_BY_PERSISTENT'])
  if (!allowed.has(value as AccountCircuitIncidentState)) throw new Error(`无效账户 circuit state：${value}`)
  return value as AccountCircuitIncidentState
}

function circuitScopeKind(value: string, name: string): AccountCircuitScopeKind {
  if (value !== 'account' && value !== 'key' && value !== 'protocol_model' && value !== 'key_model') throw new Error(`${name} 无效：${value}`)
  return value
}

function failureClass(value: string): AccountCircuitFailureClass {
  const allowed = new Set<AccountCircuitFailureClass>(['connect_failed', 'timeout_before_complete', 'read_interrupted', 'incomplete_response', 'explicit_policy'])
  if (!allowed.has(value as AccountCircuitFailureClass)) throw new Error(`无效 lastFailureClass：${value}`)
  return value as AccountCircuitFailureClass
}

function leasePurposeValue(value: string): AccountCircuitLeasePurpose {
  const allowed = new Set<AccountCircuitLeasePurpose>(['confirmation', 'half_open', 'recovery', 'cooldown_retest', 'background_probe'])
  if (!allowed.has(value as AccountCircuitLeasePurpose)) throw new Error(`无效 leasePurpose：${value}`)
  return value as AccountCircuitLeasePurpose
}

function normalizedErrorClass(value: string): string {
  const normalized = requiredText(value, 64, 'errorClass')
  if (!/^[a-z0-9][a-z0-9_.:-]*$/i.test(normalized)) throw new Error('errorClass 只能保存有界机器类别')
  return normalized
}

function requiredText(value: unknown, maxLength: number, name: string): string {
  if (typeof value !== 'string') throw new Error(`${name} 必须是字符串`)
  const normalized = value.trim()
  if (!normalized || normalized.length > maxLength) throw new Error(`${name} 长度必须为 1..${maxLength}`)
  return normalized
}

function optionalText(value: unknown, maxLength: number, name: string): string | undefined {
  if (value === undefined || value === null) return undefined
  return requiredText(value, maxLength, name)
}

function optionalCursorText(value: unknown, maxLength: number, name: string): string {
  if (value === undefined || value === null || value === '') return ''
  return requiredText(value, maxLength, name)
}

function nonNegativeInteger(value: unknown, name: string, maxValue = Number.MAX_SAFE_INTEGER): number {
  return integerAtLeast(value, 0, name, maxValue)
}

function positiveInteger(value: unknown, name: string, maxValue = Number.MAX_SAFE_INTEGER): number {
  return integerAtLeast(value, 1, name, maxValue)
}

function integerAtLeast(value: unknown, minimum: number, name: string, maxValue = Number.MAX_SAFE_INTEGER): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < minimum || value > maxValue) {
    throw new Error(`${name} 必须是 ${minimum}..${maxValue} 的安全整数`)
  }
  return value
}

function optionalNonNegativeInteger(value: unknown, name: string): number | undefined {
  if (value === undefined || value === null) return undefined
  return nonNegativeInteger(value, name)
}

function integerValue(value: number | bigint | string, name: string): number {
  const normalized = typeof value === 'number' ? value : Number(value)
  if (!Number.isSafeInteger(normalized)) throw new Error(`${name} 超出安全整数范围`)
  return normalized
}

function booleanValue(value: number | bigint | string | boolean): boolean {
  return value === true || value === 1 || value === 1n || value === '1'
}

function optionalIntegerProperty<Key extends string>(key: Key, value: number | bigint | string | null): Partial<Record<Key, number>> {
  return value === null ? {} : { [key]: integerValue(value, key) } as Partial<Record<Key, number>>
}

function optionalIntegerInput<Key extends string>(key: Key, value: unknown): Partial<Record<Key, number>> {
  const normalized = optionalNonNegativeInteger(value, key)
  return normalized === undefined ? {} : { [key]: normalized } as Partial<Record<Key, number>>
}

function optionalTextProperty<Key extends string>(key: Key, value: unknown, maxLength: number): Partial<Record<Key, string>> {
  const normalized = optionalText(value, maxLength, key)
  return normalized === undefined ? {} : { [key]: normalized } as Partial<Record<Key, string>>
}

async function deleteRowsByTextIds(
  client: DatabaseClient,
  tableName: string,
  columnName: string,
  ids: string[]
): Promise<number> {
  if (ids.length === 0) return 0
  const column = client.dialect.quoteIdentifier(columnName)
  const result = await client.execute(
    `DELETE FROM ${tableName} WHERE ${column} IN (${ids.map(() => '?').join(', ')})`,
    ids
  )
  return result.changes
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

async function accountCircuitDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}
