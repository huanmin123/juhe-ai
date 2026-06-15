import type { DatabaseSync } from 'node:sqlite'

import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
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

function findReturnableDirectGrantForGrantee(authorizationId: string, granteeSystemAccountId: string, database: DatabaseSync): ResourceAuthorizationGrantRow | undefined {
  return database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE id = ?
        AND grantee_type = 'system_account'
        AND grantee_system_account_id = ?
        AND status NOT IN ('revoked', 'returned')
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

function refreshAfterResourceAuthorizationReturnedWrite(): void {
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_returned' })
  invalidateGroupAccountIdsCache()
  clearResourceAuthorizationLookupCaches()
  notifyGatewayRuntimeCacheInvalidation('resource_authorization_returned')
  notifyAuthorizationQuotaCacheInvalidation('resource_authorization_returned')
}
