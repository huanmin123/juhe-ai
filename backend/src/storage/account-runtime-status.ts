import type { AccountStatus, AuthorizationStatus } from '../domain/types.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { maxAccountExpirySweepBatchSize } from './account-sweep-limits.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { markAllGroupAccountStatsDirty, markAllGroupAccountStatsDirtyAsync } from './usage-stats.repository.js'

export const currentIsoSql = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

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
      SELECT id
      FROM accounts
      WHERE account_expires_at IS NOT NULL
        AND account_expires_at <= ?
        AND deleted_at IS NULL
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
    .all(now, ...scope.params, batchSize) as unknown as Array<{ id: string }>
  const expiredIds = rows.map((row) => row.id).filter(Boolean)
  if (!expiredIds.length) return 0

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
  const changed = Number(result.changes ?? 0)
  if (changed > 0) {
    markAllGroupAccountStatsDirty('account_expired')
    notifyGatewayRuntimeCacheInvalidation('account_expired')
  }
  return changed
}

export async function disableExpiredAccountsAsync(access?: AccessScope, limit = maxAccountExpirySweepBatchSize, client?: DatabaseClient): Promise<number> {
  const scope = buildSystemAccountScopeClause(access)
  const now = nowIso()
  const requestedBatchSize = Math.trunc(limit)
  const batchSize = Number.isFinite(requestedBatchSize)
    ? Math.max(1, Math.min(requestedBatchSize, maxAccountExpirySweepBatchSize))
    : maxAccountExpirySweepBatchSize
  const databaseClient = client ?? createPostgresDatabaseClient(await getPostgresPool())
  const rows = await databaseClient.query<{ id: string }>(`
    SELECT id
    FROM ${accountRuntimeStatusTable(databaseClient, 'accounts')}
    WHERE account_expires_at IS NOT NULL
      AND account_expires_at <= ?
      AND deleted_at IS NULL
      AND (
        status <> 'disabled'
        OR schedulable <> 0
        OR cooldown_until IS NOT NULL
        OR last_error_code IS NOT NULL
        OR last_error_message IS NULL
      )${scope.clause}
    ORDER BY account_expires_at ASC, updated_at ASC, id ASC
    LIMIT ?
  `, [now, ...scope.params, batchSize])
  const expiredIds = rows.map((row) => row.id).filter(Boolean)
  if (!expiredIds.length) return 0
  const result = await databaseClient.execute(`
    UPDATE ${accountRuntimeStatusTable(databaseClient, 'accounts')}
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
    await markAllGroupAccountStatsDirtyAsync('account_expired', databaseClient)
    notifyGatewayRuntimeCacheInvalidation('account_expired')
  }
  return changed
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
