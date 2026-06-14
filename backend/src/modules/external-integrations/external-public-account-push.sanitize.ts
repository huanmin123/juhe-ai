import type { AccountSummary, ApiKeySummary, GroupSummary, SystemAccountSummary } from '../../domain/types.js'
import type {
  PublicAccountDeleteResponse,
  PublicAccountListResponse,
  PublicAccountPushResponse,
  PublicApiKeyListResponse,
  PublicApiKeyResponse,
  PublicApiKeySummary,
  PublicGroupListResponse,
  PublicGroupResponse,
  PublicGroupSummary
} from './external-public-account-push.types.js'

export type PublicResolvedTarget = {
  account: SystemAccountSummary
  created: boolean
}

export function publicTargetSummary(target: PublicResolvedTarget): PublicGroupResponse['target'] {
  return {
    username: target.account.username,
    displayName: target.account.displayName,
    systemAccountId: target.account.id,
    created: target.created
  }
}

export function publicAccountListResponse(
  target: PublicResolvedTarget,
  page: Pick<PublicAccountListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicAccountListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

export function publicGroupResponse(action: PublicGroupResponse['action'], target: PublicResolvedTarget, group: PublicGroupSummary | null): PublicGroupResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicTargetSummary(target),
    group
  }
}

export function publicGroupListResponse(
  target: PublicResolvedTarget,
  page: Pick<PublicGroupListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicGroupListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

export function publicGroupNotFoundResponse(usernameInput?: string): PublicGroupResponse {
  const username = normalizedText(usernameInput) || ''
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target: {
      username,
      displayName: username,
      systemAccountId: '',
      created: false
    },
    group: null
  }
}

export function publicApiKeyListResponse(
  target: PublicResolvedTarget,
  page: Pick<PublicApiKeyListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicApiKeyListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

export function publicApiKeyResponse(action: PublicApiKeyResponse['action'], target: PublicResolvedTarget, apiKey: PublicApiKeySummary | null): PublicApiKeyResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicTargetSummary(target),
    apiKey
  }
}

export function publicApiKeyNotFoundResponse(usernameInput?: string): PublicApiKeyResponse {
  const username = normalizedText(usernameInput) || ''
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target: {
      username,
      displayName: username,
      systemAccountId: '',
      created: false
    },
    apiKey: null
  }
}

export function targetFromInput(usernameInput?: string, groupNameInput?: string): PublicAccountDeleteResponse['target'] {
  const username = normalizedText(usernameInput) ?? ''
  return {
    username,
    displayName: username,
    systemAccountId: '',
    created: false,
    groupId: '',
    groupName: normalizedText(groupNameInput) ?? '',
    groupCreated: false
  }
}

export function notFoundAccountDeleteResponse(target: PublicAccountDeleteResponse['target']): PublicAccountDeleteResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target,
    account: null
  }
}

export function sanitizeAccount(account: AccountSummary): PublicAccountPushResponse['account'] {
  return {
    id: account.id,
    name: account.name,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    type: account.type,
    status: account.status,
    supportedModels: account.supportedModels,
    boundGroupId: account.boundGroupId,
    boundGroupName: account.boundGroupName,
    schedulable: account.schedulable,
    availabilitySchedule: account.availabilitySchedule
  }
}

export function sanitizeGroup(group: GroupSummary): PublicGroupSummary {
  return {
    id: group.id,
    name: group.name,
    providerCode: group.providerCode,
    providerProtocolProfileId: group.providerProtocolProfileId,
    protocolCode: group.protocolCode,
    protocolVersion: group.protocolVersion,
    description: group.description,
    enabled: group.enabled,
    groupType: group.groupType,
    isDefault: group.isDefault
  }
}

export function sanitizeApiKey(apiKey: ApiKeySummary & { key?: string }, options: { includeSecret?: boolean } = {}): PublicApiKeySummary {
  return {
    id: apiKey.id,
    name: apiKey.name,
    keyPrefix: apiKey.keyPrefix,
    key: options.includeSecret ? apiKey.key : undefined,
    status: apiKey.status,
    groupRouteStrategy: apiKey.groupRouteStrategy,
    groupBindings: apiKey.groupBindings,
    expiresAt: apiKey.expiresAt,
    availabilitySchedule: apiKey.availabilitySchedule,
    availabilityScheduleActive: apiKey.availabilityScheduleActive
  }
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
