import { manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase } from './database.js'
import { canManageResourceOwner, groupOwnerAndProvider } from './resource-authorization-helpers.js'

export function canBindApiKeyGroup(groupId: string, systemAccountId: string): boolean {
  const group = apiKeyGroupOwnerAndProvider(groupId)
  return group?.systemAccountId === systemAccountId
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
