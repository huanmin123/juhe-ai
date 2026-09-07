import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { projectAccountHealthJobsOutcome, projectAccountHealthJobsOutcomeAsync } from './account-health-projection.repository.js'
import {
  currentAccountHealthJobsInputVersion,
  currentAccountHealthJobsInputVersionAsync,
  reserveAccountHealthJobsInputVersion,
  reserveAccountHealthJobsInputVersionAsync
} from './account-health-jobs-input-version.repository.js'
import type { AccountHealthJobsOutcome } from './account-health-jobs-outcome.repository.js'
import { getPostgresPool } from './postgres-client.js'

/**
 * Regression fixture only. It does not recreate the retired Node health
 * worker: it manufactures a completed Go outcome and applies it through the
 * production fenced projector. Application code must never import this file.
 */
export function projectAccountHealthFixtureSuccess(
  accountId: string,
  input: AccountHealthFixtureSuccessInput = {}
): boolean {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL regression fixture must use projectAccountHealthFixtureSuccessAsync')
  }
  const database = getBusinessDatabase()
  const account = database.prepare(`
    SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
  `).get(accountId) as FixtureAccountRow | undefined
  if (!account || (account.status !== 'active' && account.status !== 'pending_test')) return false
  const inputVersion = currentAccountHealthJobsInputVersion(account.id, database)
    ?? reserveAccountHealthJobsInputVersion(account.id, database)
  return projectAccountHealthJobsOutcome(
    successOutcome(account, inputVersion, input, sourceConfigRevisionForFixtureAccount(database, account)),
    database
  ).changed
}

export async function projectAccountHealthFixtureSuccessAsync(
  accountId: string,
  input: AccountHealthFixtureSuccessInput = {}
): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') return projectAccountHealthFixtureSuccess(accountId, input)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const account = await client.one<FixtureAccountRow>(`
    SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id
    FROM juhe_business.accounts
    WHERE id = ? AND deleted_at IS NULL
  `, [accountId])
  if (!account || (account.status !== 'active' && account.status !== 'pending_test')) return false
  const inputVersion = await currentAccountHealthJobsInputVersionAsync(client, account.id)
    ?? await reserveAccountHealthJobsInputVersionAsync(client, account.id)
  const sourceConfigRevision = account.authorization_instance_source_account_id
    ? (await client.one<{ config_revision: number | string | bigint }>(`
        SELECT config_revision
        FROM juhe_business.accounts
        WHERE id = ? AND deleted_at IS NULL
        LIMIT 1
      `, [account.authorization_instance_source_account_id]))?.config_revision
    : undefined
  return (await projectAccountHealthJobsOutcomeAsync(
    client,
    successOutcome(account, inputVersion, input, sourceConfigRevision === undefined ? undefined : integer(sourceConfigRevision, 'source.config_revision'))
  )).changed
}

export function projectAccountHealthFixtureFailure(
  accountId: string,
  input: AccountHealthFixtureFailureInput
): AccountHealthFixtureFailureResult {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL regression fixture must project outcomes through the async projector')
  }
  const database = getBusinessDatabase()
  const account = database.prepare(`
    SELECT id, status, config_revision, dispatch_revision, authorization_instance_source_account_id, health_check_failure_count
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
  `).get(accountId) as FixtureFailureAccountRow | undefined
  if (!account || (account.status !== 'active' && account.status !== 'pending_test')) {
    return { changed: false, failureCount: 0, reachedThreshold: false, checkedAt: validIso(input.observedAt) ?? nowIso() }
  }
  const inputVersion = currentAccountHealthJobsInputVersion(account.id, database)
    ?? reserveAccountHealthJobsInputVersion(account.id, database)
  const checkedAt = validIso(input.observedAt) ?? nowIso()
  const failureCount = nonNegativeInteger(account.health_check_failure_count, 'health_check_failure_count') + 1
  const configRevision = integer(account.config_revision, 'config_revision')
  const dispatchRevision = integer(account.dispatch_revision, 'dispatch_revision')
  const intervalHours = Number.isFinite(input.intervalHours) && Number(input.intervalHours) > 0 ? Number(input.intervalHours) : 1
  const transition = account.status === 'pending_test' ? 'activation_error' : 'health_failure'
  const sourceConfigRevision = sourceConfigRevisionForFixtureAccount(database, account)
  const outcome: AccountHealthJobsOutcome = {
    outcome_id: `fixture-health-failure:${account.id}:${inputVersion}:${checkedAt}:${failureCount}`,
    request_id: `fixture-health-request:${account.id}:${inputVersion}:${checkedAt}:${failureCount}`,
    account_id: account.id,
    outcome: 'framing_complete_neutral',
    observed_at: checkedAt,
    input_version: inputVersion,
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    status_code: input.statusCode,
    error_code: input.errorCode,
    error_message: input.errorMessage,
    next_due_at: new Date(Date.parse(checkedAt) + intervalHours * 60 * 60 * 1_000).toISOString(),
    failure_count: failureCount,
    failure_started_at: checkedAt,
    projection: {
      target_account_id: account.id,
      transition_kind: transition,
      input_version: inputVersion,
      config_revision: configRevision,
      dispatch_revision: dispatchRevision,
      ...(sourceConfigRevision === undefined ? {} : { source_config_revision: sourceConfigRevision }),
      expected_account_status: account.status as 'active' | 'pending_test'
    }
  }
  const changed = projectAccountHealthJobsOutcome(outcome, database).changed
  return {
    changed,
    failureCount,
    reachedThreshold: failureCount >= Math.max(1, Math.trunc(input.failureThreshold ?? Number.MAX_SAFE_INTEGER)),
    checkedAt
  }
}

interface FixtureAccountRow {
  id: string
  status: 'active' | 'pending_test' | string
  config_revision: number | string | bigint
  dispatch_revision: number | string | bigint
  authorization_instance_source_account_id?: string | null
}

interface FixtureFailureAccountRow extends FixtureAccountRow {
  health_check_failure_count: number | string | bigint
}

interface AccountHealthFixtureSuccessInput {
  checkedAt?: string
  statusCode?: number
  intervalHours?: number
  jitterMinutes?: number
  failureThreshold?: number
  expectedConfigRevision?: number
  scheduleBalanceAutoDetection?: boolean
  traceId?: string
}

interface AccountHealthFixtureFailureInput {
  observedAt?: string
  statusCode?: number
  intervalHours?: number
  jitterMinutes?: number
  failureThreshold?: number
  errorCode?: string
  errorMessage?: string
  countTowardsThreshold?: boolean
}

interface AccountHealthFixtureFailureResult {
  changed: boolean
  failureCount: number
  reachedThreshold: boolean
  checkedAt: string
}

function successOutcome(
  account: FixtureAccountRow,
  inputVersion: number,
  input: AccountHealthFixtureSuccessInput,
  sourceConfigRevision?: number
): AccountHealthJobsOutcome {
  const observedAt = validIso(input.checkedAt) ?? nowIso()
  const intervalHours = Number.isFinite(input.intervalHours) && Number(input.intervalHours) > 0
    ? Number(input.intervalHours)
    : 1
  const transition = account.status === 'pending_test' ? 'activation_success' : 'health_success'
  const configRevision = integer(account.config_revision, 'config_revision')
  const dispatchRevision = integer(account.dispatch_revision, 'dispatch_revision')
  return {
    outcome_id: `fixture-health-success:${account.id}:${inputVersion}:${observedAt}`,
    request_id: `fixture-health-request:${account.id}:${inputVersion}:${observedAt}`,
    account_id: account.id,
    outcome: 'complete_success',
    observed_at: observedAt,
    input_version: inputVersion,
    config_revision: configRevision,
    dispatch_revision: dispatchRevision,
    status_code: input.statusCode ?? 200,
    next_due_at: new Date(Date.parse(observedAt) + intervalHours * 60 * 60 * 1_000).toISOString(),
    projection: {
      target_account_id: account.id,
      transition_kind: transition,
      input_version: inputVersion,
      config_revision: configRevision,
      dispatch_revision: dispatchRevision,
      ...(sourceConfigRevision === undefined ? {} : { source_config_revision: sourceConfigRevision }),
      expected_account_status: account.status as 'active' | 'pending_test'
    }
  }
}

function sourceConfigRevisionForFixtureAccount(database: DatabaseSync, account: FixtureAccountRow): number | undefined {
  const sourceId = account.authorization_instance_source_account_id
  if (!sourceId) return undefined
  const source = database.prepare(`
    SELECT config_revision
    FROM accounts
    WHERE id = ? AND deleted_at IS NULL
    LIMIT 1
  `).get(sourceId) as { config_revision?: number | string | bigint } | undefined
  return source?.config_revision === undefined
    ? undefined
    : integer(source.config_revision, 'source.config_revision')
}

function validIso(value: string | undefined): string | undefined {
  if (!value || Number.isNaN(Date.parse(value))) return undefined
  return new Date(value).toISOString()
}

function integer(value: number | string | bigint, field: string): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 1) throw new Error(`J1 regression fixture ${field} 无效`)
  return normalized
}

function nonNegativeInteger(value: number | string | bigint, field: string): number {
  const normalized = Number(value)
  if (!Number.isSafeInteger(normalized) || normalized < 0) throw new Error(`J1 regression fixture ${field} 无效`)
  return normalized
}
