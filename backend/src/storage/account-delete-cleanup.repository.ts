import type { DatabaseSync } from 'node:sqlite'

import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, currentSystemAccountId, type AccessScope } from './access-scope.js'
import { cleanupDeletedAccountRelatedRecordData as cleanupDeletedAccountRelatedRecordDataTarget, type DeletedAccountRecordCleanupTarget } from './account-record-cleanup.js'
import { deleteAccountTagBindingsForAccounts } from './account-tags.repository.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { resourceAuthorizationSelectColumns } from './resource-authorization-helpers.js'
import { cleanupInactiveAuthorizationBindings, revokeResourceAuthorizationGrant, returnResourceAuthorizationGrant } from './resource-authorization-write-state.repository.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import type { ResourceAuthorizationGrantRow, ResourceAuthorizationRow } from './repository-row-types.js'
import { markAllGroupAccountStatsDirty, markGroupAccountStatsDirty, markGroupAccountStatsDirtyByAccountIds } from './usage-stats.repository.js'

const internalAccountReadAccess: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const deletedAccountPhysicalCleanupRetentionMonths = 1
const deletedAccountPhysicalCleanupBatchSize = 20

function refreshGroupAccountStatsAfterWrite(input: {
  groupIds?: Array<string | null | undefined>
  accountIds?: Array<string | null | undefined>
  all?: boolean
  reason?: string
} = {}): void {
  const reason = input.reason ?? 'business_write'
  if (input.all) {
    markAllGroupAccountStatsDirty(reason)
    return
  }
  if (input.groupIds?.length) {
    markGroupAccountStatsDirty(input.groupIds, reason)
  }
  if (input.accountIds?.length) {
    markGroupAccountStatsDirtyByAccountIds(input.accountIds, reason)
  }
  if (!input.groupIds?.length && !input.accountIds?.length) {
    markAllGroupAccountStatsDirty(reason)
  }
}

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}

function invalidateAuthorizationRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
  notifyAuthorizationQuotaCacheInvalidation(reason)
}

export interface AccountDeleteResult {
  deleted: boolean
}

interface AccountDeleteRow {
  id: string
  system_account_id: string
  authorization_instance_authorization_id?: string | null
  authorization_instance_source_account_id?: string | null
  deleted_at?: string | null
}

interface DeletedAccountCleanupCandidateRow extends AccountDeleteRow {
  updated_at?: string | null
}

interface OrphanedAuthorizationInstanceCleanupRow extends AccountDeleteRow {
  source_deleted_at?: string | null
  resource_deleted_at?: string | null
}

interface DeletedAccountRelatedAccountRow {
  id?: string | null
  authorization_instance_authorization_id?: string | null
}

interface DeletedAccountCleanupAuthorizationRow {
  id?: string | null
  resource_id?: string | null
  grantee_system_account_id?: string | null
}

interface DeletedAccountCleanupTeamSourceRow {
  authorization_id?: string | null
  source_team_id?: string | null
}

interface ExpiredDeletedAccountBusinessCleanupTarget extends DeletedAccountRecordCleanupTarget {
  accountIds: string[]
  authorizationIds: string[]
  grantIds: string[]
}

export interface ExpiredDeletedAccountCleanupOptions {
  cutoffDeletedAt?: string
  limit?: number
}

export interface ExpiredDeletedAccountCleanupResult {
  cutoffDeletedAt: string
  orphanedAuthorizationInstances: number
  attempted: number
  completed: number
  deferred: number
  failed: number
  deletedRows: number
  physicallyDeletedAccounts: number
  physicallyDeletedAuthorizations: number
  physicallyDeletedGrants: number
  physicallyDeletedGroupBindings: number
}

export function deleteAccountWithRelatedCleanup(id: string, access?: AccessScope): AccountDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id, authorization_instance_source_account_id, deleted_at
      FROM accounts
      WHERE id = ?
        AND deleted_at IS NULL${scope.clause}
    `)
    .get(id, ...scope.params) as unknown as AccountDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }
  if (row.authorization_instance_authorization_id) {
    throw new Error('授权账户请使用归还操作')
  }
  const actor = currentSystemAccountId(access)
  const deletedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    let deletedAccountIds: string[] = []
    revokeAccountAuthorizationsForDeletedResource(database, row.id, actor, deletedAt)
    deletedAccountIds = logicallyDeleteSourceAccountWithInstances(database, row, actor, deletedAt)
    commitDatabaseTransaction(database, transactionStarted)
    if (deletedAccountIds.length > 0) {
      refreshGroupAccountStatsAfterWrite({ all: true, reason: 'account_deleted' })
      for (const accountId of deletedAccountIds) {
        invalidateAccountLookupCache(accountId)
      }
      invalidateGroupAccountIdsCache()
      clearResourceAuthorizationLookupCaches()
      invalidateGatewayRuntimeAfterBusinessWrite('account_deleted')
      invalidateAuthorizationRuntimeAfterBusinessWrite('account_deleted')
    }
    return {
      deleted: deletedAccountIds.length > 0
    }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function logicallyDeleteSourceAccountWithInstances(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): string[] {
  const instanceRows = database
    .prepare(`
      SELECT id
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
        AND deleted_at IS NULL
      ORDER BY created_at ASC, id ASC
    `)
    .all(row.id) as unknown as Array<{ id?: string | null }>
  const accountIds = uniqueNonEmpty([row.id, ...instanceRows.map((instance) => instance.id)])
  return logicallyDeleteAccounts(database, accountIds, actor, deletedAt)
}

function logicallyDeleteAccounts(database: DatabaseSync, accountIds: string[], actor: string, deletedAt: string): string[] {
  const ids = uniqueNonEmpty(accountIds)
  if (!ids.length) return []
  const deletedIds: string[] = []
  const selectDeletedRows = database.prepare('SELECT id FROM accounts WHERE id = ? AND deleted_at = ? LIMIT 1')
  for (const chunk of chunkValues(ids, 900)) {
    database.prepare(`
      UPDATE accounts
      SET status = 'disabled',
          schedulable = 0,
          cooldown_until = NULL,
          deleted_at = ?,
          deleted_by = ?,
          updated_at = ?
      WHERE deleted_at IS NULL
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).run(deletedAt, actor, deletedAt, ...chunk)
    for (const id of chunk) {
      const deletedRow = selectDeletedRows.get(id, deletedAt) as unknown as { id?: string } | undefined
      if (deletedRow?.id) {
        deletedIds.push(deletedRow.id)
      }
    }
  }
  deleteAccountTagBindingsForAccounts(deletedIds, database)
  return deletedIds
}

function uniqueNonEmpty(values: Array<string | null | undefined>): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const normalized = typeof value === 'string' ? value.trim() : ''
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function revokeAuthorizationInstanceForDeletedAccount(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): void {
  const authorizationId = row.authorization_instance_authorization_id
  if (!authorizationId) return
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND grantee_system_account_id = ?
      LIMIT 1
    `)
    .get(authorizationId, row.system_account_id) as unknown as ResourceAuthorizationRow | undefined
  if (authorization) {
    const directGrants = database
      .prepare(`
        SELECT *
        FROM resource_authorization_grants
        WHERE resource_type = ?
          AND resource_id = ?
          AND resource_owner_system_account_id = ?
          AND grantee_type = 'system_account'
          AND grantee_system_account_id = ?
          AND status NOT IN ('revoked', 'returned')
      `)
      .all(
        authorization.resource_type,
        authorization.resource_id,
        authorization.resource_owner_system_account_id,
        authorization.grantee_system_account_id
      ) as unknown as ResourceAuthorizationGrantRow[]
    for (const grant of directGrants) {
      returnResourceAuthorizationGrant(grant, actor, database, deletedAt)
    }
  }
  database
    .prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'account_deleted'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND status IN ('active', 'superseded')
    `)
    .run(deletedAt, actor, deletedAt, deletedAt, authorizationId)
  database
    .prepare(`
      UPDATE resource_authorizations
      SET status = 'returned',
          effective_source_type = NULL,
          effective_source_team_id = NULL,
          revoked_by = ?,
          revoked_at = ?,
          revoked_reason = 'account_deleted',
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `)
    .run(actor, deletedAt, deletedAt, deletedAt, authorizationId)
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

function revokeAccountAuthorizationsForDeletedResource(database: DatabaseSync, accountId: string, actor: string, deletedAt: string): void {
  const grants = database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE resource_type = 'account'
        AND resource_id = ?
        AND status NOT IN ('revoked', 'returned')
      ORDER BY created_at ASC, id ASC
    `)
    .all(accountId) as unknown as ResourceAuthorizationGrantRow[]
  for (const grant of grants) {
    revokeResourceAuthorizationGrant(grant, actor, database, deletedAt)
  }
}

export function cleanupExpiredLogicallyDeletedAccounts(options: ExpiredDeletedAccountCleanupOptions = {}): ExpiredDeletedAccountCleanupResult {
  const database = getBusinessDatabase()
  const cutoffDeletedAt = options.cutoffDeletedAt?.trim() || deletedAccountPhysicalCleanupCutoffIso()
  const limit = Math.max(1, Math.min(Math.trunc(options.limit ?? deletedAccountPhysicalCleanupBatchSize), 200))
  const result: ExpiredDeletedAccountCleanupResult = {
    cutoffDeletedAt,
    orphanedAuthorizationInstances: 0,
    attempted: 0,
    completed: 0,
    deferred: 0,
    failed: 0,
    deletedRows: 0,
    physicallyDeletedAccounts: 0,
    physicallyDeletedAuthorizations: 0,
    physicallyDeletedGrants: 0,
    physicallyDeletedGroupBindings: 0
  }
  const orphanedInstanceIds = logicallyDeleteOrphanedAuthorizationInstancesForDeletedSources(database, limit)
  result.orphanedAuthorizationInstances = orphanedInstanceIds.length
  if (orphanedInstanceIds.length > 0) {
    refreshGroupAccountStatsAfterWrite({ all: true, reason: 'orphaned_authorization_instance_deleted' })
    for (const accountId of orphanedInstanceIds) {
      invalidateAccountLookupCache(accountId)
    }
    invalidateGroupAccountIdsCache()
    clearResourceAuthorizationLookupCaches()
    invalidateGatewayRuntimeAfterBusinessWrite('orphaned_authorization_instance_deleted')
    invalidateAuthorizationRuntimeAfterBusinessWrite('orphaned_authorization_instance_deleted')
  }
  const candidates = listExpiredDeletedAccountCleanupCandidates(database, cutoffDeletedAt, limit)
  for (const candidate of candidates) {
    result.attempted += 1
    try {
      const target = buildExpiredDeletedAccountBusinessCleanupTarget(database, candidate)
      const recordCleanup = cleanupDeletedAccountRelatedRecordDataTarget(target)
      result.deletedRows += recordCleanup.deletedRows
      if (recordCleanup.hasMore || recordCleanup.blockedReason) {
        result.deferred += 1
        continue
      }
      const businessCleanup = physicallyDeleteExpiredDeletedAccountBusinessRows(database, target)
      result.physicallyDeletedAccounts += businessCleanup.accounts
      result.physicallyDeletedAuthorizations += businessCleanup.authorizations
      result.physicallyDeletedGrants += businessCleanup.grants
      result.physicallyDeletedGroupBindings += businessCleanup.groupBindings
      result.completed += 1
      if (businessCleanup.accounts > 0 || businessCleanup.authorizations > 0 || businessCleanup.groupBindings > 0 || businessCleanup.grants > 0) {
        refreshGroupAccountStatsAfterWrite({ all: true, reason: 'expired_deleted_account_cleanup' })
        for (const accountId of target.accountIds) {
          invalidateAccountLookupCache(accountId)
        }
        invalidateGroupAccountIdsCache()
        clearResourceAuthorizationLookupCaches()
        invalidateGatewayRuntimeAfterBusinessWrite('expired_deleted_account_cleanup')
        invalidateAuthorizationRuntimeAfterBusinessWrite('expired_deleted_account_cleanup')
      }
    } catch {
      result.failed += 1
    }
  }
  return result
}

function logicallyDeleteOrphanedAuthorizationInstancesForDeletedSources(database: DatabaseSync, limit: number): string[] {
  const rows = database
    .prepare(`
      SELECT accounts.id, accounts.system_account_id,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_source_account_id,
        accounts.deleted_at,
        source_accounts.deleted_at AS source_deleted_at,
        resource_accounts.deleted_at AS resource_deleted_at
      FROM accounts
      LEFT JOIN resource_authorizations ra
        ON ra.id = accounts.authorization_instance_authorization_id
      LEFT JOIN accounts source_accounts
        ON source_accounts.id = accounts.authorization_instance_source_account_id
      LEFT JOIN accounts resource_accounts
        ON resource_accounts.id = ra.resource_id
      WHERE accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NOT NULL
        AND (
          ra.id IS NULL
          OR ra.resource_type <> 'account'
          OR (accounts.authorization_instance_source_account_id IS NOT NULL AND source_accounts.id IS NULL)
          OR source_accounts.deleted_at IS NOT NULL
          OR resource_accounts.id IS NULL
          OR resource_accounts.deleted_at IS NOT NULL
        )
      ORDER BY accounts.updated_at ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(limit) as unknown as OrphanedAuthorizationInstanceCleanupRow[]
  if (!rows.length) return []

  const actor = internalAccountReadAccess.systemAccountId
  const fallbackDeletedAt = nowIso()
  const deletedIds: string[] = []
  for (const row of rows) {
    const deletedAt = fallbackDeletedAt
    const transactionStarted = beginDatabaseTransaction(database)
    try {
      revokeAuthorizationInstanceForDeletedSourceAccount(database, row, actor, deletedAt)
      deletedIds.push(...logicallyDeleteAccounts(database, [row.id], actor, deletedAt))
      commitDatabaseTransaction(database, transactionStarted)
    } catch (error) {
      rollbackDatabaseTransaction(database, transactionStarted)
      throw error
    }
  }
  return uniqueNonEmpty(deletedIds)
}

function revokeAuthorizationInstanceForDeletedSourceAccount(database: DatabaseSync, row: AccountDeleteRow, actor: string, deletedAt: string): void {
  const authorizationId = row.authorization_instance_authorization_id
  if (!authorizationId) return
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
      LIMIT 1
    `)
    .get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (authorization?.resource_type === 'account' && authorization.resource_id) {
    revokeAccountAuthorizationsForDeletedResource(database, authorization.resource_id, actor, deletedAt)
  }
  database
    .prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'account_deleted'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND status IN ('active', 'superseded')
    `)
    .run(deletedAt, actor, deletedAt, deletedAt, authorizationId)
  database
    .prepare(`
      UPDATE resource_authorizations
      SET status = 'revoked',
          effective_source_type = NULL,
          effective_source_team_id = NULL,
          revoked_by = COALESCE(revoked_by, ?),
          revoked_at = COALESCE(revoked_at, ?),
          revoked_reason = COALESCE(revoked_reason, 'account_deleted'),
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
        AND status <> 'returned'
    `)
    .run(actor, deletedAt, deletedAt, deletedAt, authorizationId)
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

function listExpiredDeletedAccountCleanupCandidates(
  database: DatabaseSync,
  cutoffDeletedAt: string,
  limit: number
): DeletedAccountCleanupCandidateRow[] {
  const rootRows = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id,
        authorization_instance_source_account_id, deleted_at, updated_at
      FROM accounts
      WHERE deleted_at IS NOT NULL
        AND deleted_at <= ?
        AND authorization_instance_authorization_id IS NULL
      ORDER BY deleted_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(cutoffDeletedAt, limit) as unknown as DeletedAccountCleanupCandidateRow[]
  const remaining = limit - rootRows.length
  if (remaining <= 0) return rootRows
  const instanceRows = database
    .prepare(`
      SELECT child.id, child.system_account_id, child.authorization_instance_authorization_id,
        child.authorization_instance_source_account_id, child.deleted_at, child.updated_at
      FROM accounts child
      LEFT JOIN accounts source_accounts ON source_accounts.id = child.authorization_instance_source_account_id
      WHERE child.deleted_at IS NOT NULL
        AND child.deleted_at <= ?
        AND child.authorization_instance_authorization_id IS NOT NULL
        AND (
          child.authorization_instance_source_account_id IS NULL
          OR source_accounts.id IS NULL
          OR source_accounts.deleted_at IS NULL
          OR source_accounts.deleted_at > ?
        )
      ORDER BY child.deleted_at ASC, child.updated_at ASC, child.id ASC
      LIMIT ?
    `)
    .all(cutoffDeletedAt, cutoffDeletedAt, remaining) as unknown as DeletedAccountCleanupCandidateRow[]
  return [...rootRows, ...instanceRows]
}

function buildExpiredDeletedAccountBusinessCleanupTarget(
  database: DatabaseSync,
  row: DeletedAccountCleanupCandidateRow
): ExpiredDeletedAccountBusinessCleanupTarget {
  const isAuthorizationInstance = Boolean(row.authorization_instance_authorization_id)
  const relatedRows = isAuthorizationInstance
    ? []
    : database
      .prepare(`
        SELECT id, authorization_instance_authorization_id
        FROM accounts
        WHERE authorization_instance_source_account_id = ?
        ORDER BY created_at ASC, id ASC
      `)
      .all(row.id) as unknown as DeletedAccountRelatedAccountRow[]
  const relatedAccountIds = uniqueNonEmpty(relatedRows.map((relatedRow) => relatedRow.id))
  const accountIds = uniqueNonEmpty([row.id, ...relatedAccountIds])
  const authorizationInstanceIdsByAuthorizationId = new Map<string, string>()
  if (row.authorization_instance_authorization_id) {
    authorizationInstanceIdsByAuthorizationId.set(row.authorization_instance_authorization_id, row.id)
  }
  for (const relatedRow of relatedRows) {
    const authorizationId = typeof relatedRow.authorization_instance_authorization_id === 'string'
      ? relatedRow.authorization_instance_authorization_id.trim()
      : ''
    const accountId = typeof relatedRow.id === 'string' ? relatedRow.id.trim() : ''
    if (authorizationId && accountId) {
      authorizationInstanceIdsByAuthorizationId.set(authorizationId, accountId)
    }
  }
  const authorizationRows = loadDeletedAccountCleanupAuthorizationRows(database, accountIds, [...authorizationInstanceIdsByAuthorizationId.keys()])
  const loadedAuthorizationIds = uniqueNonEmpty(authorizationRows.map((authorizationRow) => authorizationRow.id))
  const activeAuthorizationIds = isAuthorizationInstance
    ? loadActiveDeletedAccountCleanupAuthorizationInstanceIds(database, loadedAuthorizationIds)
    : new Set<string>()
  const authorizationIds = loadedAuthorizationIds.filter((authorizationId) => !activeAuthorizationIds.has(authorizationId))
  const authorizationResourceIdById = new Map(
    authorizationRows
      .map((authorizationRow) => [String(authorizationRow.id ?? ''), String(authorizationRow.resource_id ?? '')] as const)
      .filter(([authorizationId, resourceId]) => Boolean(authorizationId && resourceId))
  )
  const teamScopeIds = loadDeletedAccountCleanupTeamScopeIds(database, authorizationIds, authorizationInstanceIdsByAuthorizationId, authorizationResourceIdById, row.id)
  const grantIds = isAuthorizationInstance
    ? loadDeletedAuthorizationInstanceGrantIds(database, authorizationIds)
    : loadDeletedSourceAccountGrantIds(database, accountIds)
  return {
    accountId: row.id,
    systemAccountId: row.system_account_id,
    relatedAccountIds,
    accountIds,
    authorizationIds,
    teamScopeIds,
    grantIds
  }
}

function loadDeletedAccountCleanupAuthorizationRows(
  database: DatabaseSync,
  accountIds: string[],
  authorizationInstanceAuthorizationIds: string[]
): DeletedAccountCleanupAuthorizationRow[] {
  const rows = new Map<string, DeletedAccountCleanupAuthorizationRow>()
  for (const chunk of chunkValues(uniqueNonEmpty(accountIds), 900)) {
    const chunkRows = database
      .prepare(`
        SELECT id, resource_id, grantee_system_account_id
        FROM resource_authorizations
        WHERE resource_type = 'account'
          AND resource_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupAuthorizationRow[]
    for (const row of chunkRows) {
      if (row.id) rows.set(row.id, row)
    }
  }
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationInstanceAuthorizationIds), 900)) {
    const chunkRows = database
      .prepare(`
        SELECT id, resource_id, grantee_system_account_id
        FROM resource_authorizations
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupAuthorizationRow[]
    for (const row of chunkRows) {
      if (row.id) rows.set(row.id, row)
    }
  }
  return [...rows.values()]
}

function loadActiveDeletedAccountCleanupAuthorizationInstanceIds(database: DatabaseSync, authorizationIds: string[]): Set<string> {
  const output = new Set<string>()
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    const rows = database
      .prepare(`
        SELECT DISTINCT authorization_instance_authorization_id
        FROM accounts
        WHERE authorization_instance_authorization_id IN (${sqlPlaceholders(chunk.length)})
          AND deleted_at IS NULL
      `)
      .all(...chunk) as unknown as Array<{ authorization_instance_authorization_id?: string | null }>
    for (const row of rows) {
      const authorizationId = String(row.authorization_instance_authorization_id ?? '').trim()
      if (authorizationId) output.add(authorizationId)
    }
  }
  return output
}

function loadDeletedAccountCleanupTeamScopeIds(
  database: DatabaseSync,
  authorizationIds: string[],
  authorizationInstanceIdsByAuthorizationId: Map<string, string>,
  authorizationResourceIdById: Map<string, string>,
  fallbackAccountId: string
): string[] {
  const teamScopeIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    const rows = database
      .prepare(`
        SELECT authorization_id, source_team_id
        FROM resource_authorization_sources
        WHERE authorization_id IN (${sqlPlaceholders(chunk.length)})
          AND source_team_id IS NOT NULL
      `)
      .all(...chunk) as unknown as DeletedAccountCleanupTeamSourceRow[]
    for (const row of rows) {
      const authorizationId = String(row.authorization_id ?? '').trim()
      const teamId = String(row.source_team_id ?? '').trim()
      if (!authorizationId || !teamId) continue
      const accountId = authorizationInstanceIdsByAuthorizationId.get(authorizationId)
        ?? authorizationResourceIdById.get(authorizationId)
        ?? fallbackAccountId
      teamScopeIds.push(`${accountId}:${teamId}`)
    }
  }
  return uniqueNonEmpty(teamScopeIds)
}

function loadDeletedSourceAccountGrantIds(database: DatabaseSync, accountIds: string[]): string[] {
  const grantIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(accountIds), 900)) {
    grantIds.push(...(database
      .prepare(`
        SELECT id
        FROM resource_authorization_grants
        WHERE resource_type = 'account'
          AND resource_id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{ id?: string | null }>)
      .map((row) => String(row.id ?? '')))
  }
  return uniqueNonEmpty(grantIds)
}

function loadDeletedAuthorizationInstanceGrantIds(database: DatabaseSync, authorizationIds: string[]): string[] {
  const grantIds: string[] = []
  for (const chunk of chunkValues(uniqueNonEmpty(authorizationIds), 900)) {
    grantIds.push(...(database
      .prepare(`
        SELECT DISTINCT grants.id
        FROM resource_authorization_grants grants
        INNER JOIN resource_authorizations authorizations
          ON authorizations.resource_type = grants.resource_type
          AND authorizations.resource_id = grants.resource_id
          AND authorizations.resource_owner_system_account_id = grants.resource_owner_system_account_id
          AND grants.grantee_type = 'system_account'
          AND grants.grantee_system_account_id = authorizations.grantee_system_account_id
        INNER JOIN resource_authorization_sources sources
          ON sources.authorization_id = authorizations.id
          AND sources.source_type = 'manual'
        WHERE authorizations.id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{ id?: string | null }>)
      .map((row) => String(row.id ?? '')))
  }
  return uniqueNonEmpty(grantIds)
}

function physicallyDeleteExpiredDeletedAccountBusinessRows(
  database: DatabaseSync,
  target: ExpiredDeletedAccountBusinessCleanupTarget
): {
  accounts: number
  authorizations: number
  grants: number
  groupBindings: number
} {
  const accountIds = uniqueNonEmpty(target.accountIds)
  const relatedAccountIds = accountIds.filter((accountId) => accountId !== target.accountId)
  const authorizationIds = uniqueNonEmpty(target.authorizationIds)
  const grantIds = uniqueNonEmpty(target.grantIds)
  const result = {
    accounts: 0,
    authorizations: 0,
    grants: 0,
    groupBindings: 0
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const chunk of chunkValues(accountIds, 900)) {
      result.groupBindings += statementChanges(database.prepare(`DELETE FROM group_accounts WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
      database.prepare(`DELETE FROM account_supported_models WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
      database.prepare(`DELETE FROM account_model_mappings WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
      database.prepare(`DELETE FROM account_tag_bindings WHERE account_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
    }
    for (const chunk of chunkValues(authorizationIds, 900)) {
      result.groupBindings += statementChanges(database.prepare(`DELETE FROM group_accounts WHERE account_authorization_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
      database.prepare(`DELETE FROM resource_authorization_sources WHERE authorization_id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk)
    }
    for (const chunk of chunkValues(grantIds, 900)) {
      result.grants += statementChanges(database.prepare(`DELETE FROM resource_authorization_grants WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    for (const chunk of chunkValues(relatedAccountIds, 900)) {
      result.accounts += statementChanges(database.prepare(`DELETE FROM accounts WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    result.accounts += statementChanges(database.prepare('DELETE FROM accounts WHERE id = ?').run(target.accountId))
    for (const chunk of chunkValues(authorizationIds, 900)) {
      result.authorizations += statementChanges(database.prepare(`DELETE FROM resource_authorizations WHERE id IN (${sqlPlaceholders(chunk.length)})`).run(...chunk))
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  return result
}

function deletedAccountPhysicalCleanupCutoffIso(nowMs = Date.now()): string {
  const cutoff = new Date(nowMs)
  cutoff.setUTCMonth(cutoff.getUTCMonth() - deletedAccountPhysicalCleanupRetentionMonths)
  return cutoff.toISOString()
}

function statementChanges(result: { changes?: number | bigint }): number {
  return Number(result.changes ?? 0)
}
