import { currentSystemAccountId } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { accountSystemAccountId, activeResourceAuthorization, groupSystemAccountId, resourceAuthorizationSelectColumns } from './resource-authorization-helpers.js'
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

export interface UsageAccessLookupContext {
  apiKeySystemAccountIds: Map<string, string>
  groupSystemAccountIds: Map<string, string>
  accountSystemAccountIds: Map<string, string>
  accountMetadataById: Map<string, UsageAccountMetadata>
}

type UsageAccessLookupInput = {
  apiKeyId?: string
  groupId?: string
  accountId?: string
}

export interface UsageAccountMetadata {
  systemAccountId: string
  authorizationInstanceSourceAccountId?: string
  authorizationInstanceAuthorizationId?: string
  authorizationInstanceOwnerSystemAccountId?: string
}

export function buildUsageAccessLookupContext(inputs: UsageAccessLookupInput[]): UsageAccessLookupContext {
  const accountMetadataById = loadUsageAccountMetadata(uniqueIds(inputs.map((input) => input.accountId)))
  return {
    apiKeySystemAccountIds: loadOwnerSystemAccountIds('api_keys', uniqueIds(inputs.map((input) => input.apiKeyId))),
    groupSystemAccountIds: loadOwnerSystemAccountIds('groups', uniqueIds(inputs.map((input) => input.groupId))),
    accountSystemAccountIds: new Map([...accountMetadataById].map(([id, row]) => [id, row.systemAccountId])),
    accountMetadataById
  }
}

export function usageApiKeyExists(apiKeyId: string, context?: UsageAccessLookupContext): boolean {
  if (context) {
    return context.apiKeySystemAccountIds.has(apiKeyId)
  }
  const row = getBusinessDatabase().prepare('SELECT id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

export function systemAccountIdForUsage(input: UsageAccessLookupInput, context?: UsageAccessLookupContext): string {
  const database = getBusinessDatabase()
  if (input.apiKeyId) {
    const cachedSystemAccountId = context?.apiKeySystemAccountIds.get(input.apiKeyId)
    if (cachedSystemAccountId) return cachedSystemAccountId
    const row = database.prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(input.apiKeyId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.groupId) {
    const cachedSystemAccountId = context?.groupSystemAccountIds.get(input.groupId)
    if (cachedSystemAccountId) return cachedSystemAccountId
    const systemAccountId = groupSystemAccountId(input.groupId)
    if (systemAccountId) return systemAccountId
  }
  if (input.accountId) {
    const cachedSystemAccountId = context?.accountSystemAccountIds.get(input.accountId)
    if (cachedSystemAccountId) return cachedSystemAccountId
    const systemAccountId = loadUsageAccountMetadata([input.accountId]).get(input.accountId)?.systemAccountId
      ?? accountSystemAccountId(input.accountId)
    if (systemAccountId) return systemAccountId
  }
  return currentSystemAccountId()
}

export function usageAccessMetadata(input: UsageAccessMetadataInput, context?: UsageAccessLookupContext): UsageAccessMetadata {
  const groupOwnerSystemAccountId = input.groupOwnerSystemAccountId ?? (input.groupId ? context?.groupSystemAccountIds.get(input.groupId) ?? groupSystemAccountId(input.groupId) : undefined)
  const groupAuthorization = input.groupAuthorizationId
    ? undefined
    : input.groupId && groupOwnerSystemAccountId !== input.systemAccountId
      ? activeResourceAuthorization('group', input.groupId, input.systemAccountId)
      : undefined
  const groupAuthorizationId = input.groupAuthorizationId ?? groupAuthorization?.id
  const groupAuthorizationSnapshot = groupAuthorizationId
    ? input.groupAuthorizationId && input.groupAuthorizationId === groupAuthorization?.id
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
  const accountMetadata = input.accountId
    ? context?.accountMetadataById.get(input.accountId) ?? loadUsageAccountMetadata([input.accountId]).get(input.accountId)
    : undefined
  const instanceAuthorizationId = accountMetadata?.authorizationInstanceAuthorizationId
  const isAuthorizationInstance = Boolean(instanceAuthorizationId)
  const accountOwnerSystemAccountId = input.accountOwnerSystemAccountId
    ?? (isAuthorizationInstance
      ? accountMetadata?.authorizationInstanceOwnerSystemAccountId
      : accountMetadata?.systemAccountId ?? (input.accountId ? accountSystemAccountId(input.accountId) : undefined))
  const accountAuthorization = input.accountAuthorizationId || instanceAuthorizationId
    ? undefined
    : input.accountId && accountOwnerSystemAccountId !== input.systemAccountId && groupAccessType !== 'authorized'
      ? activeResourceAuthorization('account', input.accountId, input.systemAccountId)
      : undefined
  const accountAccessType = input.accountAccessType
    ?? (isAuthorizationInstance
      ? 'account_authorized'
      : accountOwnerSystemAccountId
        ? accountOwnerSystemAccountId === input.systemAccountId
          ? 'owner'
          : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
            ? 'group_authorized'
            : accountAuthorization
              ? 'account_authorized'
              : undefined
        : undefined)
  const accountAuthorizationId = accountAccessType === 'account_authorized'
    ? input.accountAuthorizationId ?? instanceAuthorizationId ?? accountAuthorization?.id
    : undefined
  const accountAuthorizationSnapshot = accountAuthorizationId
    ? input.accountAuthorizationId && input.accountAuthorizationId === accountAuthorization?.id
      ? accountAuthorization
      : resourceAuthorizationSnapshot(accountAuthorizationId)
    : undefined
  const effectiveAccountOwnerSystemAccountId = accountAccessType === 'account_authorized'
    ? accountOwnerSystemAccountId ?? accountAuthorizationSnapshot?.resource_owner_system_account_id
    : accountOwnerSystemAccountId
  return {
    accountOwnerSystemAccountId: effectiveAccountOwnerSystemAccountId,
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

function loadUsageAccountMetadata(ids: string[]): Map<string, UsageAccountMetadata> {
  const output = new Map<string, UsageAccountMetadata>()
  if (!ids.length) return output
  for (const chunk of chunkValues(ids, 900)) {
    const rows = getBusinessDatabase()
      .prepare(`
        SELECT id, system_account_id, authorization_instance_source_account_id,
          authorization_instance_authorization_id, authorization_instance_owner_system_account_id
        FROM accounts
        WHERE id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as unknown as Array<{
        id?: string
        system_account_id?: string
        authorization_instance_source_account_id?: string | null
        authorization_instance_authorization_id?: string | null
        authorization_instance_owner_system_account_id?: string | null
      }>
    for (const row of rows) {
      if (!row.id || !row.system_account_id) continue
      output.set(row.id, {
        systemAccountId: row.system_account_id,
        authorizationInstanceSourceAccountId: row.authorization_instance_source_account_id ?? undefined,
        authorizationInstanceAuthorizationId: row.authorization_instance_authorization_id ?? undefined,
        authorizationInstanceOwnerSystemAccountId: row.authorization_instance_owner_system_account_id ?? undefined
      })
    }
  }
  return output
}

function loadOwnerSystemAccountIds(tableName: 'api_keys' | 'groups' | 'accounts', ids: string[]): Map<string, string> {
  const output = new Map<string, string>()
  if (!ids.length) return output
  for (const chunk of chunkValues(ids, 900)) {
    const rows = getBusinessDatabase()
      .prepare(`SELECT id, system_account_id FROM ${tableName} WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as unknown as Array<{ id?: string; system_account_id?: string }>
    for (const row of rows) {
      if (row.id && row.system_account_id) {
        output.set(row.id, row.system_account_id)
      }
    }
  }
  return output
}

function uniqueIds(values: Array<string | undefined>): string[] {
  return [...new Set(values.filter((value): value is string => Boolean(value?.trim())))]
}

function resourceAuthorizationSnapshot(authorizationId: string): ResourceAuthorizationRow | undefined {
  return getBusinessDatabase()
    .prepare(`SELECT ${resourceAuthorizationSelectColumns()} FROM resource_authorizations WHERE id = ? LIMIT 1`)
    .get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
}
