import type { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../config/runtime.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { getPostgresPool } from './postgres-client.js'
import { expireDueResourceAuthorizationsAsync } from './resource-authorization-write.repository.js'
import { resourceAuthorizationSelectColumns } from './resource-authorization-helpers.js'
import {
  expireDueResourceAuthorizations,
  returnResourceAuthorizationGrant
} from './resource-authorization-write-state.repository.js'
import type { ResourceAuthorizationGrantRow, ResourceAuthorizationRow } from './repository-row-types.js'

export function returnResourceAuthorizationForGrantee(authorizationId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const grant = findReturnableDirectGrantForGrantee(authorizationId, granteeSystemAccountId, database)
  if (!grant) return undefined
  const authorization = findRuntimeAuthorizationForDirectGrant(grant, granteeSystemAccountId, database)
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

export async function returnResourceAuthorizationForGranteeAsync(authorizationId: string, access?: AccessScope): Promise<ResourceAuthorizationRow | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return returnResourceAuthorizationForGrantee(authorizationId, access)
  }
  await expireDueResourceAuthorizationsAsync()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const actor = currentSystemAccountId(access)
  const now = nowIso()
  const authorization = await client.transaction(async (tx) => {
    const grant = await findReturnableDirectGrantForGranteeAsync(authorizationId, granteeSystemAccountId, tx)
    if (!grant) return undefined
    const authorization = await findRuntimeAuthorizationForDirectGrantAsync(grant, granteeSystemAccountId, tx)
    if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
      return undefined
    }
    if (!await hasActiveManualRuntimeAuthorizationSourceAsync(authorization.id, tx)) {
      return undefined
    }
    await returnResourceAuthorizationGrantAsync(grant, actor, tx, now)
    return tx.one<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${resourceAuthorizationReturnTable(tx, 'resource_authorizations')}
      WHERE id = ?
      LIMIT 1
    `, [authorization.id])
  })
  if (authorization) {
    refreshAfterResourceAuthorizationReturnedWrite()
  }
  return authorization
}

export function returnAccountAuthorizationInstanceForGrantee(accountId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const row = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id
      FROM accounts
      WHERE id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NOT NULL
      LIMIT 1
    `)
    .get(accountId, granteeSystemAccountId) as unknown as { id?: string; system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.authorization_instance_authorization_id) return undefined
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(row.authorization_instance_authorization_id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const grant = findReturnableDirectGrantForRuntimeAuthorization(authorization, granteeSystemAccountId, database)
  if (!grant) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

export async function returnAccountAuthorizationInstanceForGranteeAsync(accountId: string, access?: AccessScope): Promise<ResourceAuthorizationRow | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return returnAccountAuthorizationInstanceForGrantee(accountId, access)
  }
  await expireDueResourceAuthorizationsAsync()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const actor = currentSystemAccountId(access)
  const now = nowIso()
  const authorization = await client.transaction(async (tx) => {
    const row = await tx.one<{ id?: string; system_account_id?: string; authorization_instance_authorization_id?: string | null }>(`
      SELECT id, system_account_id, authorization_instance_authorization_id
      FROM ${resourceAuthorizationReturnTable(tx, 'accounts')}
      WHERE id = ?
        AND system_account_id = ?
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NOT NULL
      LIMIT 1
    `, [accountId, granteeSystemAccountId])
    if (!row?.authorization_instance_authorization_id) return undefined

    const authorization = await tx.one<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${resourceAuthorizationReturnTable(tx, 'resource_authorizations')}
      WHERE id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `, [row.authorization_instance_authorization_id, granteeSystemAccountId])
    if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
      return undefined
    }
    if (!await hasActiveManualRuntimeAuthorizationSourceAsync(authorization.id, tx)) {
      return undefined
    }
    const grant = await findReturnableDirectGrantForRuntimeAuthorizationAsync(authorization, granteeSystemAccountId, tx)
    if (!grant) return undefined
    await returnResourceAuthorizationGrantAsync(grant, actor, tx, now)
    return tx.one<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${resourceAuthorizationReturnTable(tx, 'resource_authorizations')}
      WHERE id = ?
      LIMIT 1
    `, [authorization.id])
  })
  if (authorization) {
    refreshAfterResourceAuthorizationReturnedWrite()
  }
  return authorization
}

export function returnGroupAuthorizationForGrantee(groupId: string, access?: AccessScope): ResourceAuthorizationRow | undefined {
  expireDueResourceAuthorizations()
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const database = getBusinessDatabase()
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE resource_type = 'group'
        AND resource_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(groupId, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  if (!hasActiveManualRuntimeAuthorizationSource(authorization.id, database)) {
    return undefined
  }
  const grant = findReturnableDirectGrantForRuntimeAuthorization(authorization, granteeSystemAccountId, database)
  if (!grant) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    returnResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshAfterResourceAuthorizationReturnedWrite()
  return database
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorization.id) as unknown as ResourceAuthorizationRow | undefined
}

export async function returnGroupAuthorizationForGranteeAsync(groupId: string, access?: AccessScope): Promise<ResourceAuthorizationRow | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return returnGroupAuthorizationForGrantee(groupId, access)
  }
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId) return undefined
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const actor = currentSystemAccountId(access)
  const now = nowIso()
  const authorization = await client.transaction(async (tx) => {
    const authorization = await tx.one<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${resourceAuthorizationReturnTable(tx, 'resource_authorizations')}
      WHERE resource_type = 'group'
        AND resource_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `, [groupId, granteeSystemAccountId])
    if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
      return undefined
    }
    if (!await hasActiveManualRuntimeAuthorizationSourceAsync(authorization.id, tx)) {
      return undefined
    }
    const grant = await findReturnableDirectGrantForRuntimeAuthorizationAsync(authorization, granteeSystemAccountId, tx)
    if (!grant) return undefined
    await returnResourceAuthorizationGrantAsync(grant, actor, tx, now)
    return tx.one<ResourceAuthorizationRow>(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM ${resourceAuthorizationReturnTable(tx, 'resource_authorizations')}
      WHERE id = ?
      LIMIT 1
    `, [authorization.id])
  })
  if (authorization) {
    refreshAfterResourceAuthorizationReturnedWrite()
  }
  return authorization
}

function findReturnableDirectGrantForGrantee(authorizationId: string, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationGrantRow | undefined {
  return database
    .prepare(`
      SELECT grant_row.*
      FROM resource_authorization_grants grant_row
      INNER JOIN resource_authorizations runtime_authorization
        ON runtime_authorization.resource_type = grant_row.resource_type
        AND runtime_authorization.resource_id = grant_row.resource_id
        AND runtime_authorization.resource_owner_system_account_id = grant_row.resource_owner_system_account_id
        AND runtime_authorization.grantee_system_account_id = grant_row.grantee_system_account_id
      WHERE runtime_authorization.id = ?
        AND grant_row.grantee_type = 'system_account'
        AND grant_row.grantee_system_account_id = ?
        AND grant_row.status NOT IN ('revoked', 'returned')
      LIMIT 1
    `)
    .get(authorizationId, granteeSystemAccountId) as unknown as ResourceAuthorizationGrantRow | undefined
}

function findReturnableDirectGrantForRuntimeAuthorization(authorization: ResourceAuthorizationRow, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationGrantRow | undefined {
  return database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE resource_type = ?
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
        AND grantee_type = 'system_account'
        AND grantee_system_account_id = ?
        AND status NOT IN ('revoked', 'returned')
      LIMIT 1
    `)
    .get(authorization.resource_type, authorization.resource_id, authorization.resource_owner_system_account_id, granteeSystemAccountId) as unknown as ResourceAuthorizationGrantRow | undefined
}

function findRuntimeAuthorizationForDirectGrant(grant: ResourceAuthorizationGrantRow, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationRow | undefined {
  return database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE resource_type = ?
        AND resource_id = ?
        AND resource_owner_system_account_id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(grant.resource_type, grant.resource_id, grant.resource_owner_system_account_id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
}

async function findReturnableDirectGrantForGranteeAsync(authorizationId: string, granteeSystemAccountId: string, client: DatabaseClient): Promise<ResourceAuthorizationGrantRow | undefined> {
  return client.one<ResourceAuthorizationGrantRow>(`
    SELECT grant_row.*
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_grants')} grant_row
    INNER JOIN ${resourceAuthorizationReturnTable(client, 'resource_authorizations')} runtime_authorization
      ON runtime_authorization.resource_type = grant_row.resource_type
      AND runtime_authorization.resource_id = grant_row.resource_id
      AND runtime_authorization.resource_owner_system_account_id = grant_row.resource_owner_system_account_id
      AND runtime_authorization.grantee_system_account_id = grant_row.grantee_system_account_id
    WHERE runtime_authorization.id = ?
      AND grant_row.grantee_type = 'system_account'
      AND grant_row.grantee_system_account_id = ?
      AND grant_row.status NOT IN ('revoked', 'returned')
    LIMIT 1
  `, [authorizationId, granteeSystemAccountId])
}

async function findRuntimeAuthorizationForDirectGrantAsync(grant: ResourceAuthorizationGrantRow, granteeSystemAccountId: string, client: DatabaseClient): Promise<ResourceAuthorizationRow | undefined> {
  return client.one<ResourceAuthorizationRow>(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND resource_owner_system_account_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [grant.resource_type, grant.resource_id, grant.resource_owner_system_account_id, granteeSystemAccountId])
}

function hasActiveManualRuntimeAuthorizationSource(authorizationId: string, database: DatabaseSync): boolean {
  const row = database
    .prepare(`
      SELECT id
      FROM resource_authorization_sources
      WHERE authorization_id = ?
        AND source_type = 'manual'
        AND status = 'active'
      LIMIT 1
    `)
    .get(authorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

async function hasActiveManualRuntimeAuthorizationSourceAsync(authorizationId: string, client: DatabaseClient): Promise<boolean> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_sources')}
    WHERE authorization_id = ?
      AND source_type = 'manual'
      AND status = 'active'
    LIMIT 1
  `, [authorizationId])
  return Boolean(row?.id)
}

async function findReturnableDirectGrantForRuntimeAuthorizationAsync(authorization: ResourceAuthorizationRow, granteeSystemAccountId: string, client: DatabaseClient): Promise<ResourceAuthorizationGrantRow | undefined> {
  return client.one<ResourceAuthorizationGrantRow>(`
    SELECT *
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_grants')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND resource_owner_system_account_id = ?
      AND grantee_type = 'system_account'
      AND grantee_system_account_id = ?
      AND status NOT IN ('revoked', 'returned')
    LIMIT 1
  `, [authorization.resource_type, authorization.resource_id, authorization.resource_owner_system_account_id, granteeSystemAccountId])
}

async function returnResourceAuthorizationGrantAsync(grant: ResourceAuthorizationGrantRow, actor: string, client: DatabaseClient, now: string): Promise<void> {
  await client.execute(`
    UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorization_grants')}
    SET status = 'returned',
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [actor, now, now, grant.id])
  await client.execute(`
    UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorization_sources')}
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, ?),
        ended_reason = COALESCE(ended_reason, 'grantee_returned'),
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE authorization_id IN (
        SELECT id
        FROM ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
        WHERE resource_type = ?
          AND resource_id = ?
          AND resource_owner_system_account_id = ?
          AND grantee_system_account_id = ?
      )
      AND source_type = 'manual'
      AND status IN ('active', 'superseded')
  `, [
    now,
    actor,
    now,
    now,
    grant.resource_type,
    grant.resource_id,
    grant.resource_owner_system_account_id,
    grant.grantee_system_account_id
  ])
  const runtime = await client.one<ResourceAuthorizationRow>(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND resource_owner_system_account_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [grant.resource_type, grant.resource_id, grant.resource_owner_system_account_id, grant.grantee_system_account_id])
  if (!runtime) return
  await refreshResourceAuthorizationEffectiveSourceAsync(runtime.id, actor, now, client, {
    noActiveSourceReason: 'grantee_returned',
    preserveExpiredWhenNoActiveSource: false,
    terminalStatus: 'returned'
  })
}

async function refreshResourceAuthorizationEffectiveSourceAsync(
  authorizationId: string,
  actor: string,
  now: string,
  client: DatabaseClient,
  options: {
    noActiveSourceReason?: string
    preserveExpiredWhenNoActiveSource?: boolean
    terminalStatus?: 'revoked' | 'returned'
  } = {}
): Promise<void> {
  const activeTeamSource = await client.one<{ source_team_id?: string | null }>(`
    SELECT ras.source_team_id
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationReturnTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationReturnTable(client, 'resource_authorization_grants')} trg
      ON trg.resource_type = ra.resource_type
      AND trg.resource_id = ra.resource_id
      AND trg.grantee_type = 'team'
      AND trg.grantee_team_id = ras.source_team_id
      AND trg.status = 'active'
      AND (trg.expires_at IS NULL OR trg.expires_at > ?)
    WHERE ras.authorization_id = ?
      AND ras.source_type = 'team'
      AND ras.status = 'active'
    ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
    LIMIT 1
  `, [now, authorizationId])

  if (activeTeamSource?.source_team_id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'team',
          effective_source_team_id = ?,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [now, activeTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId])
    return
  }

  const pausedTeamSource = await client.one<{ source_team_id?: string | null }>(`
    SELECT ras.source_team_id
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationReturnTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationReturnTable(client, 'resource_authorization_grants')} trg
      ON trg.resource_type = ra.resource_type
      AND trg.resource_id = ra.resource_id
      AND trg.grantee_type = 'team'
      AND trg.grantee_team_id = ras.source_team_id
      AND trg.status = 'paused'
    WHERE ras.authorization_id = ?
      AND ras.source_type = 'team'
      AND ras.status = 'active'
    ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
    LIMIT 1
  `, [authorizationId])

  if (pausedTeamSource?.source_team_id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            ELSE 'paused'
          END,
          effective_source_type = 'team',
          effective_source_team_id = ?,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE 'authorization_paused' END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [now, pausedTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId])
    return
  }

  const activeManualSource = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationReturnTable(client, 'resource_authorization_sources')}
    WHERE authorization_id = ?
      AND source_type = 'manual'
      AND status = 'active'
    ORDER BY activated_at ASC, created_at ASC, id ASC
    LIMIT 1
  `, [authorizationId])

  if (activeManualSource?.id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'manual',
          effective_source_team_id = NULL,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `, [now, now, actor, now, now, now, now, now, authorizationId])
    return
  }

  const preserveExpiredWhenNoActiveSource = options.preserveExpiredWhenNoActiveSource === false ? 0 : 1
  const noActiveSourceReason = options.noActiveSourceReason ?? null
  const hasNoActiveSourceReason = noActiveSourceReason ? 1 : 0
  const terminalStatus = options.terminalStatus ?? 'revoked'
  await client.execute(`
    UPDATE ${resourceAuthorizationReturnTable(client, 'resource_authorizations')}
    SET status = CASE WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'expired' ELSE ? END,
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_by, ?) END,
        revoked_at = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_at, ?) END,
        revoked_reason = CASE
          WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired'
          WHEN ? = 1 THEN CAST(? AS text)
          ELSE COALESCE(revoked_reason, 'no_active_source')
        END,
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [
    preserveExpiredWhenNoActiveSource,
    now,
    terminalStatus,
    hasNoActiveSourceReason,
    actor,
    actor,
    hasNoActiveSourceReason,
    now,
    now,
    preserveExpiredWhenNoActiveSource,
    now,
    hasNoActiveSourceReason,
    noActiveSourceReason,
    now,
    now,
    authorizationId
  ])
}

function resourceAuthorizationReturnTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function refreshAfterResourceAuthorizationReturnedWrite(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_returned' })
  }
  clearGatewayApiKeyValidationCache()
  invalidateGroupAccountIdsCache()
  clearResourceAuthorizationLookupCaches()
  notifyGatewayRuntimeCacheInvalidation('resource_authorization_returned')
  notifyAuthorizationQuotaCacheInvalidation('resource_authorization_returned')
}
