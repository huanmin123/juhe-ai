import { manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase } from './database.js'
import { activeGroupAuthorization, canManageResourceOwner, groupOwnerAndProvider } from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'

export function canUseApiKeyGroup(groupId: string, systemAccountId: string): boolean {
  const group = apiKeyGroupOwnerAndProvider(groupId)
  if (group?.systemAccountId === systemAccountId) return true
  return Boolean(activeGroupAuthorization(groupId, systemAccountId))
}

export function apiKeyGroupAuthorization(groupOwnerSystemAccountId: string, groupId: string, systemAccountId: string): ResourceAuthorizationRow | undefined {
  return groupOwnerSystemAccountId !== systemAccountId ? activeGroupAuthorization(groupId, systemAccountId) : undefined
}

export function apiKeyGroupOwnerAndProvider(groupId: string): ReturnType<typeof groupOwnerAndProvider> {
  return groupOwnerAndProvider(groupId)
}

export function apiKeySystemAccountId(apiKeyId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

export function canManageApiKeyOwner(ownerSystemAccountId: string, access?: AccessScope): boolean {
  const scopedOwnerId = manageableSystemAccountId(access)
  return scopedOwnerId ? scopedOwnerId === ownerSystemAccountId : canManageResourceOwner(ownerSystemAccountId, access)
}
