import type { AccountStatus, AuthorizationStatus } from '../domain/types.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { maxAccountExpirySweepBatchSize } from './account-sweep-limits.js'
import { getBusinessDatabase, nowIso, runInDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { markAllGroupAccountStatsDirty, markAllGroupAccountStatsDirtyAsync } from './usage-stats.repository.js'
import {
  reserveAndEnqueueAccountHealthJobsInputInTransaction,
  reserveAndEnqueueAccountHealthJobsInputInTransactionAsync
} from './account-health-jobs-input-outbox.repository.js'
import {
  enqueueAccountHealthJobsInputsForAuthorizationSourceInTransaction,
  enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync
} from './account-health-jobs-input-authorization-fanout.repository.js'

export const currentIsoSql = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

interface ExpiredAccountInputFence {
  id: string
  config_revision: number
  dispatch_revision: number
}

export function disableExpiredAccounts(access?: AccessScope, limit = maxAccountExpirySweepBatchSize): number {
  const scope = buildSystemAccountScopeClause(access)
  const now = nowIso()
  const requestedBatchSize = Math.trunc(limit)
  const batchSize = Number.isFinite(requestedBatchSize)
    ? Math.max(1, Math.min(requestedBatchSize, maxAccountExpirySweepBatchSize))
    : maxAccountExpirySweepBatchSize
  const database = getBusinessDatabase()
  const rows = database
    .prepare(`
      SELECT id, config_revision, dispatch_revision
      FROM accounts
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND deleted_at IS NULL
        AND (last_error_code IS NULL OR last_error_code <> 'account_expired')
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
      ORDER BY account_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(now, ...scope.params, batchSize) as unknown as ExpiredAccountInputFence[]
  const expiredAccounts = rows.filter((row) => row.id)
  const expiredIds = expiredAccounts.map((row) => row.id)
  if (!expiredIds.length) return 0

  const changed = runInDatabaseTransaction(() => {
    const result = database
      .prepare(`
        UPDATE accounts
        SET status = 'disabled',
            schedulable = 0,
            cooldown_until = NULL,
            last_error_code = 'account_expired',
            last_error_message = ?,
            cooldown_retest_failure_count = 0,
            cooldown_retest_observation_started_at = NULL,
            cooldown_retest_generation = NULL,
            cooldown_retest_last_at = NULL,
            cooldown_retest_last_status_code = NULL,
            updated_at = ?
        WHERE id IN (${sqlPlaceholders(expiredIds.length)})
          AND deleted_at IS NULL
      `)
      .run('账户套餐已过期，已自动停用', now, ...expiredIds)
    const count = Number(result.changes ?? 0)
    if (count > 0) {
      for (const account of expiredAccounts) {
        enqueueExpiredAccountHealthInput(account, database)
      }
    }
    return count
  }, database)
  if (changed > 0) {
    markAllGroupAccountStatsDirty('account_expired')
    notifyGatewayRuntimeCacheInvalidation('account_expired')
  }
  return changed
}

export async function disableExpiredAccountsAsync(access?: AccessScope, limit = maxAccountExpirySweepBatchSize): Promise<number> {
  const scope = buildSystemAccountScopeClause(access)
  const now = nowIso()
  const requestedBatchSize = Math.trunc(limit)
  const batchSize = Number.isFinite(requestedBatchSize)
    ? Math.max(1, Math.min(requestedBatchSize, maxAccountExpirySweepBatchSize))
    : maxAccountExpirySweepBatchSize
  const databaseClient = createPostgresDatabaseClient(await getPostgresPool())
  const changed = await databaseClient.transaction(async (tx) => {
    const rows = await tx.query<ExpiredAccountInputFence>(`
      SELECT id, config_revision, dispatch_revision
      FROM ${accountRuntimeStatusTable(tx, 'accounts')}
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND deleted_at IS NULL
        AND last_error_code IS DISTINCT FROM 'account_expired'
        AND (
          status <> 'disabled'
          OR schedulable <> 0
          OR cooldown_until IS NOT NULL
          OR last_error_code IS NOT NULL
          OR last_error_message IS NULL
        )${scope.clause}
      ORDER BY account_expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
      FOR UPDATE SKIP LOCKED
    `, [now, ...scope.params, batchSize])
    const expiredAccounts = rows.filter((row) => row.id)
    const expiredIds = expiredAccounts.map((row) => row.id)
    if (!expiredIds.length) return 0
    const result = await tx.execute(`
      UPDATE ${accountRuntimeStatusTable(tx, 'accounts')}
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          last_error_code = 'account_expired',
          last_error_message = ?,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_generation = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          updated_at = ?
      WHERE id IN (${expiredIds.map(() => '?').join(', ')})
        AND deleted_at IS NULL
    `, ['账户套餐已过期，已自动停用', now, ...expiredIds])
    const changed = Number(result.changes ?? 0)
    if (changed > 0) {
      for (const account of expiredAccounts) {
        await enqueueExpiredAccountHealthInputAsync(account, tx)
      }
      await markAllGroupAccountStatsDirtyAsync('account_expired', tx)
    }
    return changed
  })
  if (changed > 0) {
    notifyGatewayRuntimeCacheInvalidation('account_expired')
  }
  return changed
}

function enqueueExpiredAccountHealthInput(account: ExpiredAccountInputFence, database: ReturnType<typeof getBusinessDatabase>): void {
  const configRevision = requirePositiveRevision(account.config_revision, account.id, 'config_revision')
  const dispatchRevision = requirePositiveRevision(account.dispatch_revision, account.id, 'dispatch_revision')
  reserveAndEnqueueAccountHealthJobsInputInTransaction({
    accountId: account.id,
    configRevision,
    dispatchRevision,
    kind: 'tombstone',
    reason: 'account_runtime_expired'
  }, database)
  enqueueAccountHealthJobsInputsForAuthorizationSourceInTransaction({
    resource_type: 'account',
    resource_id: account.id
  }, 'account_runtime_expired', database)
}

async function enqueueExpiredAccountHealthInputAsync(account: ExpiredAccountInputFence, client: DatabaseClient): Promise<void> {
  const configRevision = requirePositiveRevision(account.config_revision, account.id, 'config_revision')
  const dispatchRevision = requirePositiveRevision(account.dispatch_revision, account.id, 'dispatch_revision')
  await reserveAndEnqueueAccountHealthJobsInputInTransactionAsync(client, {
    accountId: account.id,
    configRevision,
    dispatchRevision,
    kind: 'tombstone',
    reason: 'account_runtime_expired'
  })
  await enqueueAccountHealthJobsInputsForAuthorizationSourceInTransactionAsync(client, {
    resource_type: 'account',
    resource_id: account.id
  }, 'account_runtime_expired')
}

function requirePositiveRevision(value: number, accountId: string, field: string): number {
  if (!Number.isSafeInteger(value) || value < 1) {
    throw new Error(`过期账户 ${accountId} 缺少有效的 J1 ${field}`)
  }
  return value
}

function accountRuntimeStatusTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function authorizationRuntimeBlockingStatus(status?: AuthorizationStatus | null, expiresAt?: string | null): AccountStatus | undefined {
  if (status && status !== 'active') return 'disabled'
  if (isResourceAuthorizationExpired(expiresAt)) return 'disabled'
  return undefined
}
