import { currentSystemAccountId } from './access-scope.js'
import { getDatabase } from './database.js'
import { accountSystemAccountId, activeResourceAuthorization, groupSystemAccountId } from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import type { ResourceAuthorizationSourceType } from '../domain/types.js'

export type UsageAccessMetadata = {
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

type UsageAccessMetadataInput = {
  systemAccountId: string
  groupId?: string
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
}

export function systemAccountIdForUsage(input: { apiKeyId?: string; groupId?: string; accountId?: string }): string {
  const database = getDatabase()
  if (input.apiKeyId) {
    const row = database.prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(input.apiKeyId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.groupId) {
    const systemAccountId = groupSystemAccountId(input.groupId)
    if (systemAccountId) return systemAccountId
  }
  if (input.accountId) {
    const systemAccountId = accountSystemAccountId(input.accountId)
    if (systemAccountId) return systemAccountId
  }
  return currentSystemAccountId()
}

export function usageAccessMetadata(input: UsageAccessMetadataInput): UsageAccessMetadata {
  const groupOwnerSystemAccountId = input.groupOwnerSystemAccountId ?? (input.groupId ? groupSystemAccountId(input.groupId) : undefined)
  const groupAuthorization = input.groupAuthorizationId
    ? undefined
    : input.groupId && groupOwnerSystemAccountId !== input.systemAccountId
      ? activeResourceAuthorization('group', input.groupId, input.systemAccountId)
      : undefined
  const groupAuthorizationId = input.groupAuthorizationId ?? groupAuthorization?.id
  const groupAuthorizationSnapshot = groupAuthorizationId
    ? input.groupAuthorizationId === groupAuthorization?.id
      ? groupAuthorization
      : resourceAuthorizationSnapshot(groupAuthorizationId)
    : undefined
  const groupAccessType = input.groupAccessType
    ?? (groupOwnerSystemAccountId
      ? groupOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAuthorization
          ? 'authorized'
          : undefined
      : undefined)
  const accountOwnerSystemAccountId = input.accountOwnerSystemAccountId ?? (input.accountId ? accountSystemAccountId(input.accountId) : undefined)
  const accountAuthorization = input.accountAuthorizationId
    ? undefined
    : input.accountId && accountOwnerSystemAccountId !== input.systemAccountId && groupAccessType !== 'authorized'
      ? activeResourceAuthorization('account', input.accountId, input.systemAccountId)
      : undefined
  const accountAuthorizationId = accountAccessTypeCandidate(input, accountOwnerSystemAccountId, groupAccessType, groupOwnerSystemAccountId, accountAuthorization)
  const accountAuthorizationSnapshot = accountAuthorizationId
    ? input.accountAuthorizationId === accountAuthorization?.id
      ? accountAuthorization
      : resourceAuthorizationSnapshot(accountAuthorizationId)
    : undefined
  const accountAccessType = input.accountAccessType
    ?? (accountOwnerSystemAccountId
      ? accountOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
          ? 'group_authorized'
          : accountAuthorization
            ? 'account_authorized'
            : undefined
      : undefined)
  return {
    accountOwnerSystemAccountId,
    groupOwnerSystemAccountId,
    accountAccessType,
    groupAccessType,
    accountAuthorizationId: accountAccessType === 'account_authorized' ? accountAuthorizationId : undefined,
    accountAuthorizationSourceType: accountAccessType === 'account_authorized'
      ? input.accountAuthorizationSourceType ?? accountAuthorizationSnapshot?.effective_source_type ?? undefined
      : undefined,
    accountAuthorizationSourceTeamId: accountAccessType === 'account_authorized'
      ? input.accountAuthorizationSourceTeamId ?? accountAuthorizationSnapshot?.effective_source_team_id ?? undefined
      : undefined,
    groupAuthorizationId,
    groupAuthorizationSourceType: groupAuthorizationId
      ? input.groupAuthorizationSourceType ?? groupAuthorizationSnapshot?.effective_source_type ?? undefined
      : undefined,
    groupAuthorizationSourceTeamId: groupAuthorizationId
      ? input.groupAuthorizationSourceTeamId ?? groupAuthorizationSnapshot?.effective_source_team_id ?? undefined
      : undefined
  }
}

function accountAccessTypeCandidate(
  input: {
    systemAccountId: string
    accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
    accountAuthorizationId?: string
  },
  accountOwnerSystemAccountId: string | undefined,
  groupAccessType: 'owner' | 'authorized' | undefined,
  groupOwnerSystemAccountId: string | undefined,
  accountAuthorization: ResourceAuthorizationRow | undefined
): string | undefined {
  const accountAccessType = input.accountAccessType
    ?? (accountOwnerSystemAccountId
      ? accountOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
          ? 'group_authorized'
          : accountAuthorization
            ? 'account_authorized'
            : undefined
      : undefined)
  return accountAccessType === 'account_authorized' ? input.accountAuthorizationId ?? accountAuthorization?.id : undefined
}

function resourceAuthorizationSnapshot(authorizationId: string): ResourceAuthorizationRow | undefined {
  return getDatabase()
    .prepare('SELECT * FROM resource_authorizations WHERE id = ? LIMIT 1')
    .get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
}
