import { randomBytes } from 'node:crypto'

import type { AccountStatus, AccountSummary, ApiKeySummary, GroupSummary, ProviderDefinition, ProviderProtocolProfileDefinition, SystemAccountSummary } from '../../domain/types.js'
import { hashPasswordAsync } from '../../storage/crypto.js'
import {
  createAccount,
  createApiKeyRecord,
  createGroup,
  createSystemAccountWithPasswordHash,
  deleteAccountWithRelatedCleanup,
  deleteApiKeyWithRelatedCleanup,
  deleteGroup,
  findAccountSummary,
  findApiKeySummary,
  findGroupSummary,
  findSystemAccountById,
  findSystemAccountByUsername,
  listAccountsPage,
  listApiKeysPage,
  listGroupsPage,
  listGroupOptions,
  listProviders,
  updateAccount,
  updateApiKey,
  updateGroup
} from '../../storage/repositories.js'
import { getBusinessDatabase, runInDatabaseTransaction } from '../../storage/database.js'
import { submitApiKeyRelatedCleanup } from '../api-keys/api-key-cleanup.service.js'

export interface PublicAccountPushInput {
  targetUsername: string
  targetDisplayName?: string
  targetGroupName: string
  providerCode: string
  providerProtocolProfileId?: string
  name: string
  type: 'api_key'
  baseUrl: string
  apiKey: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface PublicAccountUpdateInput {
  accountId: string
  targetUsername?: string
  targetDisplayName?: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  name?: string
  type?: 'api_key'
  baseUrl?: string
  apiKey?: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export interface PublicAccountPushResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'mock'
  target: {
    username: string
    displayName: string
    systemAccountId: string
    created: boolean
    groupId: string
    groupName: string
    groupCreated: boolean
  }
  account: {
    id: string
    name: string
    providerCode: string
    providerProtocolProfileId?: string
    protocolCode?: string
    protocolVersion?: string
    type: string
    status: AccountStatus
    supportedModels?: string[]
    boundGroupId?: string
    boundGroupName?: string
    schedulable: boolean
    availabilitySchedule?: AccountSummary['availabilitySchedule']
  }
}

export interface PublicAccountDeleteInput {
  accountId: string
  targetUsername?: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
}

export interface PublicAccountDeleteResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'deleted' | 'not_found' | 'mock'
  target: PublicAccountPushResponse['target']
  account: PublicAccountPushResponse['account'] | null
}

export interface PublicAccountListInput {
  targetUsername: string
  targetGroupName?: string
  providerCode?: string
  providerProtocolProfileId?: string
  groupId?: string
  keyword?: string
  type?: string
  status?: string
  schedulable?: 'all' | 'enabled' | 'disabled' | 'cooling'
  page?: number
  pageSize?: number
}

export type PublicAccountListItem = PublicAccountPushResponse['account'] & {
  concurrencyLimit: number
  priority: number
}

export interface PublicAccountListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicAccountListItem[]
}

export interface PublicGroupAddInput {
  targetUsername: string
  targetDisplayName?: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  description?: string
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupUpdateInput {
  targetUsername?: string
  groupId: string
  name?: string
  providerCode?: string
  providerProtocolProfileId?: string
  description?: string | null
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupDeleteInput {
  targetUsername?: string
  groupId: string
}

export interface PublicGroupResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'existing' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  group: PublicGroupSummary | null
}

export interface PublicGroupListInput {
  targetUsername: string
  keyword?: string
  providerCode?: string
  providerProtocolProfileId?: string
  page?: number
  pageSize?: number
}

export interface PublicGroupListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: PublicGroupResponse['target']
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicGroupSummary[]
}

export interface PublicApiKeyAddInput {
  targetUsername: string
  name: string
  description?: string | null
  groupBindings?: Array<{ groupId: string; priority?: number; weight?: number; status?: 'active' | 'disabled' }>
  groupRouteStrategy?: string
  status?: 'active' | 'disabled'
  expiresAt?: string
  quotaLimits?: Record<string, unknown> | null
  availabilitySchedule?: Record<string, unknown> | null
}

export interface PublicApiKeyUpdateInput {
  targetUsername?: string
  apiKeyId: string
  name?: string
  description?: string | null
  groupBindings?: Array<{ groupId: string; priority?: number; weight?: number; status?: 'active' | 'disabled' }>
  groupRouteStrategy?: string
  status?: 'active' | 'disabled'
  expiresAt?: string | null
  quotaLimits?: Record<string, unknown> | null
  availabilitySchedule?: Record<string, unknown> | null
}

export interface PublicApiKeyDeleteInput {
  targetUsername?: string
  apiKeyId: string
}

export interface PublicApiKeyResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  apiKey: PublicApiKeySummary | null
}

export interface PublicApiKeyListInput {
  targetUsername: string
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
  page?: number
  pageSize?: number
}

export interface PublicApiKeyListResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  target: PublicApiKeyResponse['target']
  page: number
  pageSize: number
  pageUpperBound: number
  hasMore: boolean
  items: PublicApiKeySummary[]
}

interface PublicGroupSummary {
  id: string
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  description?: string
  enabled: boolean
  groupType: string
  isDefault: boolean
}

interface PublicApiKeySummary {
  id: string
  name: string
  keyPrefix: string
  key?: string
  status: 'active' | 'disabled'
  groupRouteStrategy: string
  groupBindings: ApiKeySummary['groupBindings']
  expiresAt?: string
  availabilitySchedule?: ApiKeySummary['availabilitySchedule']
  availabilityScheduleActive?: ApiKeySummary['availabilityScheduleActive']
}

type ResolvedTarget = {
  account: SystemAccountSummary
  created: boolean
}

type ResolvedGroup = {
  group: GroupSummary
  created: boolean
}

type TargetAccess = {
  systemAccountId: string
  role: 'user'
}

export async function addPublicWelfareAccount(input: PublicAccountPushInput): Promise<PublicAccountPushResponse> {
  return writePublicWelfareAccount(input, await autoCreatedTargetPasswordHash())
}

export function updatePublicWelfareAccount(input: PublicAccountUpdateInput): PublicAccountPushResponse {
  return updatePublicWelfareAccountById(input)
}

function writePublicWelfareAccount(input: PublicAccountPushInput, targetPasswordHash?: string): PublicAccountPushResponse {
  const providerCode = requiredProviderCode(input.providerCode)
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  const providerProfile = requireProviderProtocolProfile(provider, input.providerProtocolProfileId)
  assertSupportedPushAccountType(input.type, providerProfile.accountTypes)

  return runInDatabaseTransaction(() => {
    const target = ensureTargetSystemAccount(input, targetPasswordHash)
    assertTargetActive(target.account)

    const access = { systemAccountId: target.account.id, role: 'user' as const }
    const targetGroup = ensureTargetGroup({ access, providerCode, providerProtocolProfileId: providerProfile.id, groupName: input.targetGroupName })
    const existing = findTargetAccount({
      access,
      providerCode,
      providerProtocolProfileId: providerProfile.id,
      groupId: targetGroup.group.id,
      name: input.name
    })
    if (existing) {
      throw new Error(`账号已存在：${existing.name}`)
    }
    const account = createAccount(accountCreateInputForPush(input, providerCode, providerProfile.id, targetGroup.group.id), access)

    return {
      source: 'stats',
      generatedAt: new Date().toISOString(),
      action: 'created',
      target: {
        username: target.account.username,
        displayName: target.account.displayName,
        systemAccountId: target.account.id,
        created: target.created,
        groupId: targetGroup.group.id,
        groupName: targetGroup.group.name,
        groupCreated: targetGroup.created
      },
      account: sanitizeAccount(account)
    }
  }, getBusinessDatabase())
}

function updatePublicWelfareAccountById(input: PublicAccountUpdateInput): PublicAccountPushResponse {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    throw new Error('账号修改必须提供 accountId')
  }

  return runInDatabaseTransaction(() => {
    const accountOwner = findPublicAccountOwnerById(accountId)
    if (!accountOwner) {
      throw new Error('账号不存在')
    }
    const target = resolvePublicOwnedResourceTarget(input.targetUsername, accountOwner.systemAccountId)
    if (!target) {
      throw new Error('账号不存在')
    }
    assertTargetActive(target.account)

    const access = targetAccess(target.account.id)
    const existing = findAccountSummary(accountId, access)
    if (!existing) {
      throw new Error('账号不存在')
    }
    if (existing.type !== 'api_key') {
      throw new Error('公开账号修改仅支持 API Key 账户')
    }

    const providerProfile = assertProviderEnabled(existing.providerCode, existing.providerProtocolProfileId)
    if (hasPublicInput(input, 'type')) {
      assertSupportedPushAccountType(input.type, providerProfile.accountTypes)
    }
    const targetGroup = resolvePublicAccountGroupFilter(input, existing, access)
    const payload = accountPartialUpdateInputForPush(input, existing)
    const updated = updateAccount(existing.id, payload, access)
    if (!updated) {
      throw new Error('账号不存在')
    }

    return {
      source: 'stats',
      generatedAt: new Date().toISOString(),
      action: 'updated',
      target: {
        username: target.account.username,
        displayName: target.account.displayName,
        systemAccountId: target.account.id,
        created: false,
        groupId: targetGroup?.id ?? updated.boundGroupId ?? '',
        groupName: targetGroup?.name ?? updated.boundGroupName ?? '',
        groupCreated: false
      },
      account: sanitizeAccount(updated)
    }
  }, getBusinessDatabase())
}

export function deletePublicWelfareAccount(input: PublicAccountDeleteInput): PublicAccountDeleteResponse {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    throw new Error('删除账号时必须提供 accountId')
  }

  const fallbackTarget = targetFromInput(input.targetUsername, input.targetGroupName)
  const accountOwner = findPublicAccountOwnerById(accountId)
  if (!accountOwner) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  const target = resolvePublicOwnedResourceTarget(input.targetUsername, accountOwner.systemAccountId)
  if (!target) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const account = findAccountSummary(accountId, access)
  if (!account) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  if (account.type !== 'api_key') {
    throw new Error('公开账号删除仅支持 API Key 账户')
  }
  const targetGroup = resolvePublicAccountGroupFilter(input, account, access)
  const resolvedTarget = {
    username: target.account.username,
    displayName: target.account.displayName,
    systemAccountId: target.account.id,
    created: false,
    groupId: targetGroup?.id ?? account.boundGroupId ?? '',
    groupName: targetGroup?.name ?? account.boundGroupName ?? normalizedText(input.targetGroupName) ?? '',
    groupCreated: false
  }

  const deletedAccount = sanitizeAccount(account)
  const deleteResult = deleteAccountWithRelatedCleanup(account.id, access)
  if (!deleteResult.deleted) {
    throw new Error('目标账号无法删除，可能正在作为授权实例使用')
  }

  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'deleted',
    target: resolvedTarget,
    account: deletedAccount
  }
}

export function listPublicWelfareAccounts(input: PublicAccountListInput): PublicAccountListResponse {
  const target = requirePublicTarget(input.targetUsername)
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const providerCode = normalizedText(input.providerCode)
  const providerProtocolProfileId = resolveOptionalProviderProtocolProfileId(providerCode, input.providerProtocolProfileId)
  const groupId = resolveAccountListGroupId(access, {
    providerCode,
    providerProtocolProfileId,
    groupId: input.groupId,
    targetGroupName: input.targetGroupName
  })
  const page = listAccountsPage(access, {
    page: input.page,
    pageSize: input.pageSize,
    keyword: normalizedText(input.keyword),
    providerCode,
    providerProtocolProfileId,
    groupId,
    type: normalizedText(input.type),
    status: normalizedText(input.status),
    schedulable: input.schedulable
  })
  return publicAccountListResponse(target, {
    page: page.page,
    pageSize: page.pageSize,
    pageUpperBound: page.total,
    hasMore: page.hasMore,
    items: page.items.map((account) => ({
      ...sanitizeAccount(account),
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority
    }))
  })
}

export async function addPublicGroup(input: PublicGroupAddInput): Promise<PublicGroupResponse> {
  const providerCode = requiredProviderCode(input.providerCode)
  const providerProfile = assertProviderEnabled(providerCode, input.providerProtocolProfileId)
  const targetPasswordHash = await autoCreatedTargetPasswordHash()
  return runInDatabaseTransaction(() => {
    const target = ensureTargetSystemAccount(input, targetPasswordHash)
    assertTargetActive(target.account)
    const access = targetAccess(target.account.id)
    const existing = resolvePublicGroup(access, { name: input.name, providerCode, providerProtocolProfileId: providerProfile.id })
    if (existing) {
      return publicGroupResponse('existing', target, sanitizeGroup(existing))
    }
    const group = createGroup({
      name: input.name,
      providerCode,
      providerProtocolProfileId: providerProfile.id,
      description: input.description,
      enabled: input.enabled,
      groupType: input.groupType ?? 'personal'
    }, access)
    return publicGroupResponse('created', target, sanitizeGroup(group))
  }, getBusinessDatabase())
}

export function updatePublicGroup(input: PublicGroupUpdateInput): PublicGroupResponse {
  const groupId = normalizedText(input.groupId)
  if (!groupId) {
    throw new Error('分组修改必须提供 groupId')
  }
  const owner = findPublicGroupOwnerById(groupId)
  if (!owner) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const target = resolvePublicOwnedResourceTarget(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const group = findGroupSummary(groupId, access)
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  if (input.providerCode) {
    assertProviderEnabled(input.providerCode, input.providerProtocolProfileId)
  }
  const updated = updateGroup(group.id, publicGroupUpdatePayload(input), access)
  if (!updated) {
    return publicGroupResponse('not_found', target, null)
  }
  return publicGroupResponse('updated', target, sanitizeGroup(updated))
}

export function deletePublicGroup(input: PublicGroupDeleteInput): PublicGroupResponse {
  const groupId = normalizedText(input.groupId)
  if (!groupId) {
    throw new Error('分组删除必须提供 groupId')
  }
  const owner = findPublicGroupOwnerById(groupId)
  if (!owner) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const target = resolvePublicOwnedResourceTarget(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const group = findGroupSummary(groupId, access)
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  const deletedGroup = sanitizeGroup(group)
  const result = deleteGroup(group.id, access)
  return publicGroupResponse(result.deleted ? 'deleted' : 'not_found', target, result.deleted ? deletedGroup : null)
}

export function listPublicGroups(input: PublicGroupListInput): PublicGroupListResponse {
  const target = requirePublicTarget(input.targetUsername)
  assertTargetActive(target.account)
  const page = listGroupsPage(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    keyword: normalizedText(input.keyword),
    providerCode: normalizedText(input.providerCode),
    providerProtocolProfileId: resolveOptionalProviderProtocolProfileId(input.providerCode, input.providerProtocolProfileId),
    manageableOnly: true
  })
  return publicGroupListResponse(target, {
    page: page.page,
    pageSize: page.pageSize,
    pageUpperBound: page.total,
    hasMore: page.hasMore,
    items: page.items.map(sanitizeGroup)
  })
}

export function addPublicApiKey(input: PublicApiKeyAddInput): PublicApiKeyResponse {
  const target = findPublicTarget(input.targetUsername)
  if (!target) {
    throw new Error(`目标用户不存在：${input.targetUsername}`)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const payload = publicApiKeyPayload(input)
  const apiKey = createApiKeyRecord(payload, access)
  return publicApiKeyResponse('created', target, sanitizeApiKey(apiKey, { includeSecret: true }))
}

export function updatePublicApiKey(input: PublicApiKeyUpdateInput): PublicApiKeyResponse {
  const apiKeyId = normalizedText(input.apiKeyId)
  if (!apiKeyId) {
    throw new Error('API Key 修改必须提供 apiKeyId')
  }
  const owner = findPublicApiKeyOwnerById(apiKeyId)
  if (!owner) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const target = resolvePublicOwnedResourceTarget(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const apiKey = findApiKeySummary(apiKeyId, access)
  if (!apiKey) {
    return publicApiKeyResponse('not_found', target, null)
  }
  const updated = updateApiKey(apiKey.id, publicApiKeyPayload(input, true), access)
  return publicApiKeyResponse(updated ? 'updated' : 'not_found', target, updated ? sanitizeApiKey(updated) : null)
}

export function deletePublicApiKey(input: PublicApiKeyDeleteInput): PublicApiKeyResponse {
  const apiKeyId = normalizedText(input.apiKeyId)
  if (!apiKeyId) {
    throw new Error('API Key 删除必须提供 apiKeyId')
  }
  const owner = findPublicApiKeyOwnerById(apiKeyId)
  if (!owner) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const target = resolvePublicOwnedResourceTarget(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const apiKey = findApiKeySummary(apiKeyId, access)
  if (!apiKey) {
    return publicApiKeyResponse('not_found', target, null)
  }
  const deletedApiKey = sanitizeApiKey(apiKey)
  const result = deleteApiKeyWithRelatedCleanup(apiKey.id, access)
  if (result.cleanupTarget) {
    submitApiKeyRelatedCleanup(result.cleanupTarget)
  }
  return publicApiKeyResponse(result.deleted ? 'deleted' : 'not_found', target, result.deleted ? deletedApiKey : null)
}

export function listPublicApiKeys(input: PublicApiKeyListInput): PublicApiKeyListResponse {
  const target = requirePublicTarget(input.targetUsername)
  assertTargetActive(target.account)
  const page = listApiKeysPage(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    keyword: normalizedText(input.keyword),
    status: input.status,
    groupId: normalizedText(input.groupId)
  })
  return publicApiKeyListResponse(target, {
    page: page.page,
    pageSize: page.pageSize,
    pageUpperBound: page.total,
    hasMore: page.hasMore,
    items: page.items.map((apiKey) => sanitizeApiKey(apiKey))
  })
}

export function mockPublicWelfareAccountPush(input: PublicAccountPushInput | PublicAccountUpdateInput): PublicAccountPushResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'gpt'
  const accountName = normalizedText(input.name) || '公益站测试账号'
  return {
    source: 'mock',
    generatedAt,
    action: 'mock',
    target: {
      username,
      displayName: normalizedText(input.targetDisplayName) || username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false,
      groupId: 'mock_group_welfare',
      groupName,
      groupCreated: false
    },
    account: {
      id: 'mock_account_public_welfare',
      name: accountName,
      providerCode,
      type: 'api_key',
      status: input.status === 'disabled' ? 'disabled' : 'active',
      supportedModels: normalizedStringList(input.supportedModels),
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: input.status !== 'disabled'
    }
  }
}

export function mockPublicWelfareAccountList(input: PublicAccountListInput): PublicAccountListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'mock_provider'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: 'mock_account_public_welfare',
        name: normalizedText(input.keyword) || '公益站测试账号',
        providerCode,
        type: 'api_key',
        status: 'active',
        supportedModels: ['gpt-5.5'],
        boundGroupId: normalizedText(input.groupId) || 'mock_group_welfare',
        boundGroupName: groupName,
        schedulable: true,
        concurrencyLimit: 20,
        priority: 0
      }
    ]
  }
}

export function mockPublicWelfareAccountDelete(input: PublicAccountDeleteInput): PublicAccountDeleteResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'gpt'
  const accountId = normalizedText(input.accountId) || 'mock_account_public_welfare'
  return {
    source: 'mock',
    generatedAt,
    action: 'mock',
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false,
      groupId: 'mock_group_welfare',
      groupName,
      groupCreated: false
    },
    account: {
      id: accountId,
      name: accountId,
      providerCode,
      type: 'api_key',
      status: 'disabled',
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: false
    }
  }
}

export function mockPublicGroupAdd(input: PublicGroupAddInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: requiredProviderCode(input.providerCode),
    description: normalizedText(input.description),
    enabled: input.enabled !== false,
    groupType: input.groupType ?? 'personal',
    isDefault: false
  })
}

export function mockPublicGroupUpdate(input: PublicGroupUpdateInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: normalizedText(input.groupId) || 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: normalizedText(input.providerCode) ?? '',
    description: normalizedText(input.description),
    enabled: input.enabled !== false,
    groupType: input.groupType ?? 'personal',
    isDefault: false
  })
}

export function mockPublicGroupDelete(input: PublicGroupDeleteInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: normalizedText(input.groupId) || 'mock_group_public',
    name: '公开接口分组',
    providerCode: 'mock_provider',
    enabled: true,
    groupType: 'personal',
    isDefault: false
  })
}

export function mockPublicGroupList(input: PublicGroupListInput): PublicGroupListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: 'mock_group_public',
        name: normalizedText(input.keyword) || '公开接口分组',
        providerCode: normalizedText(input.providerCode) || 'mock_provider',
        enabled: true,
        groupType: 'personal',
        isDefault: false
      }
    ]
  }
}

export function mockPublicApiKeyAdd(input: PublicApiKeyAddInput): PublicApiKeyResponse {
  const groupBindings = mockPublicApiKeyGroupBindings(input.groupBindings)
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: 'mock_api_key_public',
    name: normalizedText(input.name) || '公开接口 API Key',
    keyPrefix: 'juis_mock',
    key: 'juis_mock_public_api_key',
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupRouteStrategy: normalizedText(input.groupRouteStrategy) || 'priority_failover',
    groupBindings,
    expiresAt: normalizedText(input.expiresAt)
  })
}

export function mockPublicApiKeyList(input: PublicApiKeyListInput): PublicApiKeyListResponse {
  const username = normalizedText(input.targetUsername) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    page: input.page ?? 1,
    pageSize: input.pageSize ?? 20,
    pageUpperBound: 1,
    hasMore: false,
    items: [
      {
        id: normalizedText(input.groupId) ? 'mock_api_key_public_bound' : 'mock_api_key_public',
        name: normalizedText(input.keyword) || '公开接口 API Key',
        keyPrefix: 'juis_mock',
        status: input.status === 'disabled' ? 'disabled' : 'active',
        groupRouteStrategy: 'priority_failover',
        groupBindings: mockPublicApiKeyGroupBindings(input.groupId ? [{ groupId: input.groupId }] : undefined)
      }
    ]
  }
}

export function mockPublicApiKeyUpdate(input: PublicApiKeyUpdateInput): PublicApiKeyResponse {
  const groupBindings = mockPublicApiKeyGroupBindings(input.groupBindings)
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: normalizedText(input.apiKeyId) || 'mock_api_key_public',
    name: normalizedText(input.name) || '公开接口 API Key',
    keyPrefix: 'juis_mock',
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupRouteStrategy: normalizedText(input.groupRouteStrategy) || 'priority_failover',
    groupBindings,
    expiresAt: normalizedText(input.expiresAt)
  })
}

export function mockPublicApiKeyDelete(input: PublicApiKeyDeleteInput): PublicApiKeyResponse {
  return publicMockApiKeyResponse('mock', input.targetUsername, {
    id: normalizedText(input.apiKeyId) || 'mock_api_key_public',
    name: '公开接口 API Key',
    keyPrefix: 'juis_mock',
    status: 'disabled',
    groupRouteStrategy: 'priority_failover',
    groupBindings: mockPublicApiKeyGroupBindings()
  })
}

function mockPublicApiKeyGroupBindings(input: PublicApiKeyAddInput['groupBindings'] = []): ApiKeySummary['groupBindings'] {
  const bindings = input.length ? input : [{ groupId: 'mock_group_public', priority: 1, status: 'active' as const }]
  return bindings.map((binding, index) => ({
    id: `mock_api_key_group_binding_${index + 1}`,
    groupId: binding.groupId,
    groupName: binding.groupId === 'mock_group_public' ? '公开接口分组' : binding.groupId,
    priority: binding.priority ?? index + 1,
    weight: binding.weight ?? 1,
    status: binding.status ?? 'active',
    groupEnabled: true
  }))
}

function assertProviderEnabled(providerCode: string, providerProtocolProfileId?: string): ProviderProtocolProfileDefinition {
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return requireProviderProtocolProfile(provider, providerProtocolProfileId)
}

function requireProviderProtocolProfile(provider: ProviderDefinition, providerProtocolProfileId?: string): ProviderProtocolProfileDefinition {
  const profileId = normalizedText(providerProtocolProfileId)
  const profile = profileId
    ? provider.protocolProfiles.find((item) => item.id === profileId)
    : provider.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
      ?? provider.protocolProfiles.find((item) => item.enabled)
      ?? provider.protocolProfiles[0]
  if (!profile) {
    throw new Error(`供应商未配置协议档案：${provider.code}`)
  }
  if (profile.providerCode !== provider.code) {
    throw new Error(`协议档案 ${profile.id} 不属于供应商 ${provider.code}`)
  }
  if (!profile.enabled) {
    throw new Error(`供应商协议档案已停用：${profile.name}`)
  }
  return profile
}

function resolveOptionalProviderProtocolProfileId(providerCodeInput?: string, providerProtocolProfileIdInput?: string): string | undefined {
  const providerCode = normalizedText(providerCodeInput)
  const providerProtocolProfileId = normalizedText(providerProtocolProfileIdInput)
  if (!providerCode && !providerProtocolProfileId) return undefined
  if (!providerCode) {
    throw new Error('按协议档案查询时必须提供 providerCode')
  }
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  return requireProviderProtocolProfile(provider, providerProtocolProfileId).id
}

function assertTargetActive(account: SystemAccountSummary): void {
  if (account.status !== 'active') {
    throw new Error(`目标用户已停用：${account.username}`)
  }
}

function targetAccess(systemAccountId: string): TargetAccess {
  return { systemAccountId, role: 'user' }
}

function findPublicTarget(usernameInput: string): ResolvedTarget | undefined {
  const username = normalizedText(usernameInput)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const account = findSystemAccountByUsername(username)
  return account ? { account, created: false } : undefined
}

function requirePublicTarget(usernameInput: string): ResolvedTarget {
  const target = findPublicTarget(usernameInput)
  if (!target) {
    throw new Error(`目标用户不存在：${normalizedText(usernameInput) ?? ''}`)
  }
  return target
}

function publicTargetSummary(target: ResolvedTarget): PublicGroupResponse['target'] {
  return {
    username: target.account.username,
    displayName: target.account.displayName,
    systemAccountId: target.account.id,
    created: target.created
  }
}

function publicAccountListResponse(
  target: ResolvedTarget,
  page: Pick<PublicAccountListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicAccountListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

function publicGroupResponse(action: PublicGroupResponse['action'], target: ResolvedTarget, group: PublicGroupSummary | null): PublicGroupResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicTargetSummary(target),
    group
  }
}

function publicGroupListResponse(
  target: ResolvedTarget,
  page: Pick<PublicGroupListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicGroupListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

function publicGroupNotFoundResponse(usernameInput?: string): PublicGroupResponse {
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

function publicApiKeyListResponse(
  target: ResolvedTarget,
  page: Pick<PublicApiKeyListResponse, 'page' | 'pageSize' | 'pageUpperBound' | 'hasMore' | 'items'>
): PublicApiKeyListResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    target: publicTargetSummary(target),
    ...page
  }
}

function publicApiKeyResponse(action: PublicApiKeyResponse['action'], target: ResolvedTarget, apiKey: PublicApiKeySummary | null): PublicApiKeyResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicTargetSummary(target),
    apiKey
  }
}

function publicApiKeyNotFoundResponse(usernameInput?: string): PublicApiKeyResponse {
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

function publicMockGroupResponse(action: PublicGroupResponse['action'], usernameInput: string | undefined, group: PublicGroupSummary): PublicGroupResponse {
  const username = normalizedText(usernameInput) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    action,
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    group
  }
}

function publicMockApiKeyResponse(action: PublicApiKeyResponse['action'], usernameInput: string | undefined, apiKey: PublicApiKeySummary): PublicApiKeyResponse {
  const username = normalizedText(usernameInput) || 'huanmin'
  return {
    source: 'mock',
    generatedAt: new Date().toISOString(),
    action,
    target: {
      username,
      displayName: username,
      systemAccountId: 'mock_system_account_huanmin',
      created: false
    },
    apiKey
  }
}

function resolvePublicGroup(access: TargetAccess, input: { groupId?: string; name?: string; providerCode?: string; providerProtocolProfileId?: string }): GroupSummary | undefined {
  const groupId = normalizedText(input.groupId)
  if (groupId) {
    return findGroupSummary(groupId, access)
  }
  const name = normalizedText(input.name)
  if (!name) {
    return undefined
  }
  const providerCode = normalizedText(input.providerCode)
  if (!providerCode) {
    return undefined
  }
  const providerProtocolProfileId = resolveOptionalProviderProtocolProfileId(providerCode, input.providerProtocolProfileId)
  return findExistingTargetGroup({ access, providerCode, providerProtocolProfileId, groupName: name })
}

function resolveAccountListGroupId(
  access: TargetAccess,
  input: { providerCode?: string; providerProtocolProfileId?: string; groupId?: string; targetGroupName?: string }
): string | undefined {
  const groupId = normalizedText(input.groupId)
  if (groupId) {
    return groupId
  }
  const groupName = normalizedText(input.targetGroupName)
  if (!groupName) {
    return undefined
  }
  const providerCode = normalizedText(input.providerCode)
  if (!providerCode) {
    throw new Error('按目标分组名称查询账号时必须提供 providerCode')
  }
  const providerProtocolProfileId = resolveOptionalProviderProtocolProfileId(providerCode, input.providerProtocolProfileId)
  return findExistingTargetGroup({ access, providerCode, providerProtocolProfileId, groupName })?.id ?? '__public_group_not_found__'
}

function publicApiKeyPayload(input: PublicApiKeyAddInput | PublicApiKeyUpdateInput, partial = false): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if ('name' in input && input.name !== undefined) payload.name = input.name
  if ('description' in input && input.description !== undefined) payload.description = input.description
  if ('status' in input && input.status !== undefined) payload.status = input.status
  if ('expiresAt' in input && input.expiresAt !== undefined) payload.expiresAt = input.expiresAt
  if ('quotaLimits' in input && input.quotaLimits !== undefined) payload.quotaLimits = input.quotaLimits
  if ('availabilitySchedule' in input && input.availabilitySchedule !== undefined) payload.availabilitySchedule = input.availabilitySchedule
  if ('groupRouteStrategy' in input && input.groupRouteStrategy !== undefined) payload.groupRouteStrategy = input.groupRouteStrategy
  if (input.groupBindings?.length) {
    payload.groupBindings = input.groupBindings
  }
  if (!partial && !payload.groupBindings) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  if (partial && Object.keys(payload).length === 0) {
    throw new Error('API Key 修改至少提供一个要修改的字段')
  }
  return payload
}

function publicGroupUpdatePayload(input: PublicGroupUpdateInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (input.name !== undefined) payload.name = input.name
  if (input.providerCode !== undefined) payload.providerCode = input.providerCode
  if (input.providerProtocolProfileId !== undefined) payload.providerProtocolProfileId = input.providerProtocolProfileId
  if (input.description !== undefined) payload.description = input.description
  if (input.enabled !== undefined) payload.enabled = input.enabled
  if (input.groupType !== undefined) payload.groupType = input.groupType
  if (Object.keys(payload).length === 0) {
    throw new Error('分组修改至少提供一个要修改的字段')
  }
  return payload
}

function ensureTargetSystemAccount(input: { targetUsername: string; targetDisplayName?: string }, passwordHash?: string): ResolvedTarget {
  const username = normalizedText(input.targetUsername)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const existing = findSystemAccountByUsername(username)
  if (existing) {
    return { account: existing, created: false }
  }

  const displayName = normalizedText(input.targetDisplayName) || username
  if (!passwordHash) {
    throw new Error('自动创建目标用户缺少密码哈希')
  }
  const account = createSystemAccountWithPasswordHash({
    username,
    displayName,
    description: '由公开接口自动创建',
    role: 'user',
    status: 'active',
    mustChangePassword: true
  }, passwordHash)
  return { account, created: true }
}

async function autoCreatedTargetPasswordHash(): Promise<string> {
  return hashPasswordAsync(randomBytes(18).toString('base64url'))
}

function ensureTargetGroup(input: {
  access: TargetAccess
  providerCode: string
  providerProtocolProfileId: string
  groupName: string
}): ResolvedGroup {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = listGroupOptions(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && item.providerProtocolProfileId === input.providerProtocolProfileId && sameText(item.name, groupName))

  if (existing) {
    const group = findGroupSummary(existing.id, input.access)
    if (!group) {
      throw new Error('目标分组不存在')
    }
    return {
      group,
      created: false
    }
  }

  return {
    group: createGroup({
      name: groupName,
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      description: '由公开接口自动创建',
      enabled: true,
      groupType: 'personal'
    }, input.access),
    created: true
  }
}

function requireExistingTargetGroup(input: {
  access: TargetAccess
  providerCode: string
  providerProtocolProfileId: string
  groupName: string
}): ResolvedGroup {
  const group = findExistingTargetGroup(input)
  if (!group) {
    throw new Error(`目标分组不存在：${normalizedText(input.groupName) ?? ''}`)
  }
  return {
    group,
    created: false
  }
}

function findExistingTargetGroup(input: {
  access: TargetAccess
  providerCode: string
  providerProtocolProfileId?: string
  groupName: string
}): GroupSummary | undefined {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = listGroupOptions(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && (!input.providerProtocolProfileId || item.providerProtocolProfileId === input.providerProtocolProfileId) && sameText(item.name, groupName))
  return existing ? findGroupSummary(existing.id, input.access) : undefined
}

function findTargetAccountById(input: {
  access: TargetAccess
  providerCode: string
  providerProtocolProfileId?: string
  groupId: string
  accountId?: string
}): AccountSummary | undefined {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    return undefined
  }
  const row = getBusinessDatabase().prepare(`
    SELECT accounts.id AS id
    FROM accounts
    INNER JOIN group_accounts ON group_accounts.account_id = accounts.id
    WHERE accounts.id = ?
      AND accounts.system_account_id = ?
      AND accounts.provider_code = ?
      AND (? IS NULL OR accounts.provider_protocol_profile_id = ?)
      AND group_accounts.group_id = ?
    LIMIT 1
  `).get(
    accountId,
    input.access.systemAccountId,
    input.providerCode,
    input.providerProtocolProfileId ?? null,
    input.providerProtocolProfileId ?? null,
    input.groupId
  ) as { id?: string } | undefined
  return row?.id ? findAccountSummary(row.id, input.access) : undefined
}

function findPublicAccountOwnerById(accountId: string): { id: string; systemAccountId: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, system_account_id AS systemAccountId
    FROM accounts
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `).get(accountId) as { id: string; systemAccountId: string } | undefined
}

function findPublicGroupOwnerById(groupId: string): { id: string; systemAccountId: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, system_account_id AS systemAccountId
    FROM groups
    WHERE id = ?
    LIMIT 1
  `).get(groupId) as { id: string; systemAccountId: string } | undefined
}

function findPublicApiKeyOwnerById(apiKeyId: string): { id: string; systemAccountId: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, system_account_id AS systemAccountId
    FROM api_keys
    WHERE id = ?
    LIMIT 1
  `).get(apiKeyId) as { id: string; systemAccountId: string } | undefined
}

function resolvePublicOwnedResourceTarget(usernameInput: string | undefined, ownerSystemAccountId: string): ResolvedTarget | undefined {
  const username = normalizedText(usernameInput)
  const account = username
    ? findSystemAccountByUsername(username)
    : findSystemAccountById(ownerSystemAccountId)
  if (!account || account.id !== ownerSystemAccountId) {
    return undefined
  }
  return {
    account,
    created: false
  }
}

function resolvePublicAccountGroupFilter(
  input: { targetGroupName?: string; providerCode?: string; providerProtocolProfileId?: string },
  account: AccountSummary,
  access: TargetAccess
): GroupSummary | undefined {
  const providerCode = normalizedText(input.providerCode)
  if (providerCode && providerCode !== account.providerCode) {
    throw new Error('账号不存在')
  }
  const providerProtocolProfileId = normalizedText(input.providerProtocolProfileId)
  if (providerProtocolProfileId && providerProtocolProfileId !== account.providerProtocolProfileId) {
    throw new Error('账号不存在')
  }

  const groupName = normalizedText(input.targetGroupName)
  if (!groupName) {
    return account.boundGroupId ? findGroupSummary(account.boundGroupId, access) : undefined
  }

  const group = findExistingTargetGroup({
    access,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    groupName
  })
  if (!group) {
    throw new Error('账号不存在')
  }
  const accountInGroup = findTargetAccountById({
    access,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? '',
    groupId: group.id,
    accountId: account.id
  })
  if (!accountInGroup) {
    throw new Error('账号不存在')
  }
  return group
}

function findTargetAccount(input: {
  access: TargetAccess
  providerCode: string
  providerProtocolProfileId: string
  groupId: string
  name: string
}): AccountSummary | undefined {
  const name = normalizedText(input.name)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  return listAccountsPage(input.access, {
    keyword: name,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    groupId: input.groupId,
    page: 1,
    pageSize: 20
  }).items.find((item) => item.providerCode === input.providerCode && item.providerProtocolProfileId === input.providerProtocolProfileId && sameText(item.name, name))
}

function targetFromInput(usernameInput?: string, groupNameInput?: string): PublicAccountDeleteResponse['target'] {
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

function notFoundAccountDeleteResponse(target: PublicAccountDeleteResponse['target']): PublicAccountDeleteResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target,
    account: null
  }
}

function accountCreateInputForPush(input: PublicAccountPushInput, providerCode: string, providerProtocolProfileId: string, groupId: string): Record<string, unknown> {
  return {
    ...accountWriteInputForPush(input),
    providerCode,
    providerProtocolProfileId,
    type: input.type,
    groupId,
    status: input.status === 'disabled' ? 'disabled' : 'pending_test',
    schedulable: false
  }
}

function accountWriteInputForPush(input: PublicAccountPushInput): Record<string, unknown> {
  const name = normalizedText(input.name)
  const baseUrl = normalizedText(input.baseUrl)
  const apiKey = normalizedText(input.apiKey)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  if (!baseUrl) {
    throw new Error('Base URL 不能为空')
  }
  if (!apiKey) {
    throw new Error('API Key 不能为空')
  }

  const payload: Record<string, unknown> = {
    name,
    credentials: {
      api_key: apiKey,
      base_url: baseUrl
    }
  }
  if (hasPublicInput(input, 'supportedModels')) {
    payload.supportedModels = normalizedStringList(input.supportedModels) ?? []
  }
  if (hasPublicInput(input, 'status')) {
    payload.status = input.status === 'disabled' ? 'disabled' : 'active'
    payload.schedulable = input.status !== 'disabled'
  }
  if (hasPublicInput(input, 'concurrencyLimit')) {
    payload.concurrencyLimit = boundedInteger(input.concurrencyLimit, 1, 100_000)
  }
  if (hasPublicInput(input, 'priority')) {
    payload.priority = boundedInteger(input.priority, 0, 100_000) ?? 0
  }
  if (hasPublicInput(input, 'notes')) {
    payload.notes = pushNotes(input)
  }
  if (hasPublicInput(input, 'availabilitySchedule')) {
    payload.availabilitySchedule = input.availabilitySchedule
  }
  return payload
}

function accountPartialUpdateInputForPush(input: PublicAccountUpdateInput, current: AccountSummary): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (hasPublicInput(input, 'name')) {
    const name = normalizedText(input.name)
    if (!name) {
      throw new Error('账户名称不能为空')
    }
    payload.name = name
  }
  if (hasPublicInput(input, 'apiKey') || hasPublicInput(input, 'baseUrl')) {
    payload.credentials = accountCredentialsForPartialUpdate(input, current)
  }
  if (hasPublicInput(input, 'supportedModels')) {
    payload.supportedModels = normalizedStringList(input.supportedModels) ?? []
  }
  if (hasPublicInput(input, 'status')) {
    payload.status = input.status === 'disabled' ? 'disabled' : 'active'
    payload.schedulable = input.status !== 'disabled'
  }
  if (hasPublicInput(input, 'concurrencyLimit')) {
    payload.concurrencyLimit = boundedInteger(input.concurrencyLimit, 1, 100_000)
  }
  if (hasPublicInput(input, 'priority')) {
    payload.priority = boundedInteger(input.priority, 0, 100_000) ?? 0
  }
  if (hasPublicInput(input, 'notes')) {
    payload.notes = pushNotes(input)
  }
  if (hasPublicInput(input, 'availabilitySchedule')) {
    payload.availabilitySchedule = input.availabilitySchedule
  }
  if (Object.keys(payload).length === 0) {
    throw new Error('账号修改至少提供一个要修改的字段')
  }
  return payload
}

function accountCredentialsForPartialUpdate(input: PublicAccountUpdateInput, current: AccountSummary): Record<string, unknown> {
  const currentCredentials = current.credentials as Record<string, unknown>
  const apiKey = hasPublicInput(input, 'apiKey')
    ? normalizedText(input.apiKey)
    : normalizedText(currentCredentials.api_key)
  const baseUrl = hasPublicInput(input, 'baseUrl')
    ? normalizedText(input.baseUrl)
    : normalizedText(currentCredentials.base_url)
  if (!baseUrl) {
    throw new Error('Base URL 不能为空')
  }
  if (!apiKey) {
    throw new Error('API Key 不能为空')
  }
  return {
    ...currentCredentials,
    api_key: apiKey,
    base_url: baseUrl
  }
}

function assertSupportedPushAccountType(value: unknown, providerAccountTypes: readonly string[]): void {
  const type = normalizedText(value)
  if (!type) {
    throw new Error('账号类型不能为空')
  }
  if (type !== 'api_key') {
    throw new Error('账号新增仅支持 API Key 账户')
  }
  if (!providerAccountTypes.includes('api_key')) {
    throw new Error('当前供应商不支持 API Key 账户')
  }
}

function requiredProviderCode(value: string | undefined): string {
  const providerCode = normalizedText(value)
  if (!providerCode) {
    throw new Error('供应商编码不能为空')
  }
  return providerCode
}

function sanitizeAccount(account: AccountSummary): PublicAccountPushResponse['account'] {
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

function sanitizeGroup(group: GroupSummary): PublicGroupSummary {
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

function sanitizeApiKey(apiKey: ApiKeySummary & { key?: string }, options: { includeSecret?: boolean } = {}): PublicApiKeySummary {
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

function pushNotes(input: Pick<PublicAccountPushInput, 'notes'>): string | undefined {
  return normalizedText(input.notes)
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedStringList(values: readonly string[] | undefined): string[] | undefined {
  const normalized = [...new Set((values ?? []).map((item) => item.trim()).filter(Boolean))]
  return normalized.length ? normalized : undefined
}

function boundedInteger(value: unknown, min: number, max: number): number | undefined {
  if (value === undefined) {
    return undefined
  }
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    throw new Error(`数值字段必须是 ${min} 到 ${max} 之间的整数`)
  }
  return value
}

function sameText(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase()
}

function hasPublicInput(input: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}
