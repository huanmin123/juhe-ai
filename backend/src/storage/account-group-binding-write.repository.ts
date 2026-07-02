import type { AccountSummary, GroupSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { findAccountSummary, findAccountSummaryAsync } from './account-summary.repository.js'
import type { AccessScope } from './access-scope.js'
import { getBusinessDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { findGroupSummary } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import {
  accountSystemAccountId,
  activeAccountAuthorization,
  activeResourceAuthorization,
  activeResourceAuthorizationById,
  canManageResourceOwner,
  groupOwnerAndProvider
} from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'

function canUseAccount(accountId: string, systemAccountId: string): boolean {
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id) return false
  if (row.authorization_instance_authorization_id) {
    return Boolean(activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId))
  }
  if (row.system_account_id === systemAccountId) return true
  return Boolean(activeResourceAuthorization('account', accountId, systemAccountId))
}

function authorizationInstanceRuntimeAuthorization(accountId: string, systemAccountId: string, database = getBusinessDatabase()): ResourceAuthorizationRow | undefined {
  const row = database
    .prepare('SELECT authorization_instance_authorization_id FROM accounts WHERE id = ? AND system_account_id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId, systemAccountId) as unknown as { authorization_instance_authorization_id?: string | null } | undefined
  return row?.authorization_instance_authorization_id
    ? activeResourceAuthorizationById(row.authorization_instance_authorization_id, systemAccountId)
    : undefined
}

function accountBindingAuthorizationId(accountId: string, systemAccountId: string, account?: AccountSummary): string | undefined {
  if (account?.accountAuthorizationId) {
    return activeResourceAuthorizationById(account.accountAuthorizationId, systemAccountId)?.id
  }
  const instanceAuthorization = authorizationInstanceRuntimeAuthorization(accountId, systemAccountId)
  if (instanceAuthorization?.id) {
    return instanceAuthorization.id
  }
  const ownerId = accountSystemAccountId(accountId)
  if (ownerId && ownerId !== systemAccountId) {
    return activeAccountAuthorization(accountId, systemAccountId)?.id
  }
  return undefined
}

function accountBindingRequiresAuthorization(accountId: string, systemAccountId: string, account?: AccountSummary): boolean {
  if (account?.accessType === 'authorized' || account?.accountAuthorizationId || account?.authorizationInstanceSourceAccountId) {
    return true
  }
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(accountId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id) return false
  return row.system_account_id !== systemAccountId || Boolean(row.authorization_instance_authorization_id)
}

export function accountEnabledGroupId(accountId: string, systemAccountId: string): string | undefined {
  const row = getBusinessDatabase()
    .prepare(`
      SELECT group_id
      FROM group_accounts
      WHERE account_id = ?
        AND system_account_id = ?
        AND enabled = 1
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
      LIMIT 1
    `)
    .get(accountId, systemAccountId) as unknown as { group_id?: string } | undefined
  return row?.group_id
}

async function accountEnabledGroupIdAsync(client: DatabaseClient, accountId: string, systemAccountId: string): Promise<string | undefined> {
  const row = await client.one<{ group_id?: string }>(`
    SELECT group_id
    FROM ${accountGroupBindingTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, group_id ASC, account_id ASC
    LIMIT 1
  `, [accountId, systemAccountId])
  return row?.group_id
}

async function groupOwnerAndProviderAsync(client: DatabaseClient, groupId: string): Promise<{ systemAccountId: string; providerCode: string } | undefined> {
  const row = await client.one<{ system_account_id?: string; provider_code?: string }>(`
    SELECT system_account_id, provider_code
    FROM ${accountGroupBindingTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  return row?.system_account_id && row.provider_code
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code
      }
    : undefined
}

function validAccountIdsForGroup(providerCode: string, accountIds: string[], systemAccountId: string): string[] {
  const uniqueIds = [...new Set(accountIds)]
  const accountsById = new Map<string, { provider_code?: string }>()
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(uniqueIds, 900)) {
    const rows = database.prepare(`
      SELECT id, provider_code
      FROM accounts
      WHERE system_account_id = ?
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).all(systemAccountId, ...chunk) as Array<{ id?: string; provider_code?: string }>
    for (const row of rows) {
      if (row.id) {
        accountsById.set(row.id, row)
      }
    }
  }
  return uniqueIds.filter((accountId) => {
    const account = accountsById.get(accountId)
    return account?.provider_code === providerCode
      && canUseAccount(accountId, systemAccountId)
  })
}

export function setAccountGroup(
  accountId: string,
  groupId: string | null,
  access?: AccessScope
): AccountSummary | undefined {
  const database = getBusinessDatabase()
  if (!groupId) {
    return undefined
  }
  const group = groupOwnerAndProvider(groupId)
  if (!group || !canManageResourceOwner(group.systemAccountId, access)) {
    return undefined
  }
  const current = findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
  if (!current) {
    return undefined
  }
  if (!canUseAccount(accountId, group.systemAccountId)) {
    return undefined
  }
  if (group.providerCode !== current.providerCode) {
    return undefined
  }
  const accountAuthorizationId = accountBindingAuthorizationId(accountId, group.systemAccountId, current)
  if (accountBindingRequiresAuthorization(accountId, group.systemAccountId, current) && !accountAuthorizationId) {
    return undefined
  }

  const previousGroupId = accountEnabledGroupId(accountId, group.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, group.systemAccountId)
  const now = nowIso()
  database
    .prepare(`
      INSERT INTO group_accounts (
        system_account_id, group_id, account_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled,
        enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        local_priority = excluded.local_priority,
        local_super_priority_enabled = excluded.local_super_priority_enabled,
        local_fallback_enabled = excluded.local_fallback_enabled,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(
      group.systemAccountId,
      groupId,
      accountId,
      accountAuthorizationId ?? null,
      current.priority,
      current.superPriorityEnabled ? 1 : 0,
      current.fallbackEnabled ? 1 : 0,
      now,
      now
    )
  refreshGroupAccountStatsAfterWrite({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  notifyGatewayRuntimeCacheInvalidation('group_account_binding')

  return findAccountSummary(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
}

export async function setAccountGroupAsync(
  accountId: string,
  groupId: string | null,
  access?: AccessScope
): Promise<AccountSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return setAccountGroup(accountId, groupId, access)
  }
  if (!groupId) {
    return undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const group = await groupOwnerAndProviderAsync(client, groupId)
  if (!group || !canManageResourceOwner(group.systemAccountId, access)) {
    return undefined
  }
  const current = await findAccountSummaryAsync(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
  if (!current || current.accessType === 'authorized' || current.accountAuthorizationId) {
    return undefined
  }
  if (group.providerCode !== current.providerCode) {
    return undefined
  }

  const previousGroupId = await accountEnabledGroupIdAsync(client, accountId, group.systemAccountId)
  const now = nowIso()
  await client.transaction(async (tx) => {
    await tx.execute(`
      DELETE FROM ${accountGroupBindingTable(tx, 'group_accounts')}
      WHERE account_id = ?
        AND system_account_id = ?
    `, [accountId, group.systemAccountId])
    await tx.execute(`
      INSERT INTO ${accountGroupBindingTable(tx, 'group_accounts')} (
        system_account_id, group_id, account_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled,
        enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, NULL, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        local_priority = excluded.local_priority,
        local_super_priority_enabled = excluded.local_super_priority_enabled,
        local_fallback_enabled = excluded.local_fallback_enabled,
        enabled = 1,
        updated_at = excluded.updated_at
    `, [
      group.systemAccountId,
      groupId,
      accountId,
      current.priority,
      current.superPriorityEnabled ? 1 : 0,
      current.fallbackEnabled ? 1 : 0,
      now,
      now
    ])
  })
  await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  notifyGatewayRuntimeCacheInvalidation('group_account_binding')

  return await findAccountSummaryAsync(accountId, { systemAccountId: group.systemAccountId, role: 'user' })
}

function accountGroupBindingTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function addAccountToGroup(groupId: string, accountId: string): GroupSummary | undefined {
  const database = getBusinessDatabase()
  const current = groupOwnerAndProvider(groupId)
  if (!current) {
    return undefined
  }
  if (!canManageResourceOwner(current.systemAccountId)) {
    return undefined
  }
  if (!validAccountIdsForGroup(current.providerCode, [accountId], current.systemAccountId).includes(accountId)) {
    return undefined
  }
  const account = findAccountSummary(accountId, { systemAccountId: current.systemAccountId, role: 'user' })
  if (!account) {
    return undefined
  }
  const accountAuthorizationId = accountBindingAuthorizationId(accountId, current.systemAccountId)
  if (accountBindingRequiresAuthorization(accountId, current.systemAccountId) && !accountAuthorizationId) {
    return undefined
  }
  const now = nowIso()
  const previousGroupId = accountEnabledGroupId(accountId, current.systemAccountId)
  database.prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?').run(accountId, current.systemAccountId)
  database
    .prepare(`
      INSERT INTO group_accounts (
        system_account_id, group_id, account_id, account_authorization_id,
        local_priority, local_super_priority_enabled, local_fallback_enabled,
        enabled, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        account_authorization_id = excluded.account_authorization_id,
        local_priority = excluded.local_priority,
        local_super_priority_enabled = excluded.local_super_priority_enabled,
        local_fallback_enabled = excluded.local_fallback_enabled,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(
      current.systemAccountId,
      groupId,
      accountId,
      accountAuthorizationId ?? null,
      account.priority,
      account.superPriorityEnabled ? 1 : 0,
      account.fallbackEnabled ? 1 : 0,
      now,
      now
    )
  refreshGroupAccountStatsAfterWrite({ groupIds: [previousGroupId, groupId], reason: 'group_account_binding' })
  if (previousGroupId && previousGroupId !== groupId) {
    invalidateGroupAccountIdsCache(previousGroupId)
  }
  invalidateGroupAccountIdsCache(groupId)
  notifyGatewayRuntimeCacheInvalidation('group_account_binding')
  return findGroupSummary(groupId)
}
