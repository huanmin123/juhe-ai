import { randomBytes } from 'node:crypto'

import type { AccountStatus, AccountSummary, ApiKeySummary, GroupSummary, SystemAccountSummary } from '../../domain/types.js'
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
  findSystemAccountByUsername,
  listAccountsPage,
  listApiKeys,
  listApiKeysPage,
  listGroupOptions,
  listProviders,
  setAccountGroup,
  updateAccount,
  updateApiKey,
  updateGroup
} from '../../storage/repositories.js'
import { getBusinessDatabase, newId, nowIso, runInDatabaseTransaction } from '../../storage/database.js'
import { submitAccountRelatedCleanup } from '../accounts/account-cleanup.service.js'
import { submitApiKeyRelatedCleanup } from '../api-keys/api-key-cleanup.service.js'

export interface PublicAccountPushInput {
  targetUsername: string
  targetDisplayName?: string
  targetGroupName: string
  providerCode?: string
  name: string
  type?: string
  baseUrl: string
  apiKey: string
  supportedModels?: string[]
  status?: 'active' | 'disabled'
  concurrencyLimit?: number
  priority?: number
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
  externalId?: string
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
    type: string
    status: AccountStatus
    supportedModels?: string[]
    boundGroupId?: string
    boundGroupName?: string
    schedulable: boolean
    availabilitySchedule?: AccountSummary['availabilitySchedule']
  }
  externalId?: string
}

export interface PublicAccountDeleteInput {
  targetUsername: string
  targetGroupName: string
  providerCode?: string
  accountId?: string
  name?: string
  externalId?: string
}

export interface PublicAccountDeleteResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'deleted' | 'not_found' | 'mock'
  target: PublicAccountPushResponse['target']
  account: PublicAccountPushResponse['account'] | null
  externalId?: string
}

export interface PublicGroupAddInput {
  targetUsername: string
  targetDisplayName?: string
  name: string
  providerCode?: string
  description?: string
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupUpdateInput {
  targetUsername: string
  groupId?: string
  name?: string
  providerCode?: string
  description?: string | null
  enabled?: boolean
  groupType?: 'personal' | 'high_concurrency'
}

export interface PublicGroupDeleteInput {
  targetUsername: string
  groupId?: string
  name?: string
  providerCode?: string
}

export interface PublicGroupResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'existing' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  group: PublicGroupSummary | null
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
  targetUsername: string
  apiKeyId?: string
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
  targetUsername: string
  apiKeyId?: string
  name?: string
}

export interface PublicApiKeyResponse {
  source: 'stats' | 'mock'
  generatedAt: string
  action: 'created' | 'updated' | 'deleted' | 'not_found' | 'mock'
  target: Omit<PublicAccountPushResponse['target'], 'groupId' | 'groupName' | 'groupCreated'>
  apiKey: PublicApiKeySummary | null
}

interface PublicGroupSummary {
  id: string
  name: string
  providerCode: string
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

type PublicAccountWriteMode = 'create' | 'update'

export async function addPublicWelfareAccount(input: PublicAccountPushInput): Promise<PublicAccountPushResponse> {
  return writePublicWelfareAccount(input, 'create', await autoCreatedTargetPasswordHash())
}

export function updatePublicWelfareAccount(input: PublicAccountPushInput): PublicAccountPushResponse {
  return writePublicWelfareAccount(input, 'update')
}

function writePublicWelfareAccount(input: PublicAccountPushInput, mode: PublicAccountWriteMode, targetPasswordHash?: string): PublicAccountPushResponse {
  const providerCode = normalizedText(input.providerCode) || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  assertSupportedPushAccountType(input.type, provider.accountTypes)

  return runInDatabaseTransaction(() => {
    const target = mode === 'update' ? findPublicTarget(input.targetUsername) : ensureTargetSystemAccount(input, targetPasswordHash)
    if (!target) {
      throw new Error(`目标用户不存在：${normalizedText(input.targetUsername) ?? ''}`)
    }
    assertTargetActive(target.account)

    const access = { systemAccountId: target.account.id, role: 'user' as const }
    const targetGroup = mode === 'update'
      ? requireExistingTargetGroup({ access, providerCode, groupName: input.targetGroupName })
      : ensureTargetGroup({ access, providerCode, groupName: input.targetGroupName })
    const existing = findTargetAccountByExternalId({
      access,
      providerCode,
      groupId: targetGroup.group.id,
      externalId: input.externalId
    }) ?? findTargetAccount({
      access,
      providerCode,
      groupId: targetGroup.group.id,
      name: input.name
    })
    if (mode === 'create' && existing) {
      throw new Error(`账号已存在：${existing.name}`)
    }
    if (mode === 'update' && !existing) {
      throw new Error('账号不存在')
    }
    const accountInput = accountInputForPush(input, providerCode, targetGroup.group.id)
    const account = existing
      ? updateAccount(existing.id, accountInput, access) ?? existing
      : createAccount(accountInput, access)
    upsertExternalAccountPushRecord({
      systemAccountId: target.account.id,
      providerCode,
      groupId: targetGroup.group.id,
      externalId: input.externalId,
      accountId: account.id
    })

    return {
      source: 'stats',
      generatedAt: new Date().toISOString(),
      action: existing ? 'updated' : 'created',
      target: {
        username: target.account.username,
        displayName: target.account.displayName,
        systemAccountId: target.account.id,
        created: target.created,
        groupId: targetGroup.group.id,
        groupName: targetGroup.group.name,
        groupCreated: targetGroup.created
      },
      account: sanitizeAccount(account),
      externalId: normalizedText(input.externalId)
    }
  }, getBusinessDatabase())
}

export function deletePublicWelfareAccount(input: PublicAccountDeleteInput): PublicAccountDeleteResponse {
  const providerCode = normalizedText(input.providerCode) || 'openai'
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }

  const username = normalizedText(input.targetUsername)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const groupName = normalizedText(input.targetGroupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const accountId = normalizedText(input.accountId)
  const name = normalizedText(input.name)
  const externalId = normalizedText(input.externalId)
  if (!accountId && !name && !externalId) {
    throw new Error('删除账号时必须提供 accountId、name 或 externalId')
  }

  const fallbackTarget = targetFromInput(username, groupName)
  const targetAccount = findSystemAccountByUsername(username)
  if (!targetAccount) {
    return notFoundAccountDeleteResponse(input, fallbackTarget)
  }

  const access: TargetAccess = { systemAccountId: targetAccount.id, role: 'user' }
  const targetGroup = findExistingTargetGroup({
    access,
    providerCode,
    groupName
  })
  const resolvedTarget = {
    username: targetAccount.username,
    displayName: targetAccount.displayName,
    systemAccountId: targetAccount.id,
    created: false,
    groupId: targetGroup?.id ?? '',
    groupName,
    groupCreated: false
  }
  if (!targetGroup) {
    return notFoundAccountDeleteResponse(input, resolvedTarget)
  }

  const account = findTargetAccountByExternalId({
    access,
    providerCode,
    groupId: targetGroup.id,
    externalId
  }) ?? findTargetAccountById({
    access,
    providerCode,
    groupId: targetGroup.id,
    accountId
  }) ?? (name
    ? findTargetAccount({
      access,
      providerCode,
      groupId: targetGroup.id,
      name
    })
    : undefined)

  if (!account) {
    return notFoundAccountDeleteResponse(input, resolvedTarget)
  }

  const deletedAccount = sanitizeAccount(account)
  const deleteResult = deleteAccountWithRelatedCleanup(account.id, access)
  if (!deleteResult.deleted) {
    throw new Error('目标账号无法删除，可能正在作为授权实例使用')
  }
  if (deleteResult.cleanupTarget) {
    submitAccountRelatedCleanup(deleteResult.cleanupTarget)
  }

  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'deleted',
    target: resolvedTarget,
    account: deletedAccount,
    externalId
  }
}

export async function addPublicGroup(input: PublicGroupAddInput): Promise<PublicGroupResponse> {
  const providerCode = normalizedText(input.providerCode) || 'openai'
  assertProviderEnabled(providerCode)
  const targetPasswordHash = await autoCreatedTargetPasswordHash()
  return runInDatabaseTransaction(() => {
    const target = ensureTargetSystemAccount(input, targetPasswordHash)
    assertTargetActive(target.account)
    const access = targetAccess(target.account.id)
    const existing = resolvePublicGroup(access, { name: input.name, providerCode })
    if (existing) {
      return publicGroupResponse('existing', target, sanitizeGroup(existing))
    }
    const group = createGroup({
      name: input.name,
      providerCode,
      description: input.description,
      enabled: input.enabled,
      groupType: input.groupType ?? 'personal'
    }, access)
    return publicGroupResponse('created', target, sanitizeGroup(group))
  }, getBusinessDatabase())
}

export function updatePublicGroup(input: PublicGroupUpdateInput): PublicGroupResponse {
  const target = findPublicTarget(input.targetUsername)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const access = targetAccess(target.account.id)
  const group = resolvePublicGroup(access, {
    groupId: input.groupId,
    name: input.name,
    providerCode: input.providerCode
  })
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  if (input.providerCode) {
    assertProviderEnabled(input.providerCode)
  }
  const updated = updateGroup(group.id, {
    name: input.name,
    providerCode: input.providerCode,
    description: input.description,
    enabled: input.enabled,
    groupType: input.groupType
  }, access)
  if (!updated) {
    return publicGroupResponse('not_found', target, null)
  }
  return publicGroupResponse('updated', target, sanitizeGroup(updated))
}

export function deletePublicGroup(input: PublicGroupDeleteInput): PublicGroupResponse {
  const target = findPublicTarget(input.targetUsername)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const access = targetAccess(target.account.id)
  const group = resolvePublicGroup(access, input)
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  const deletedGroup = sanitizeGroup(group)
  const result = deleteGroup(group.id, access)
  return publicGroupResponse(result.deleted ? 'deleted' : 'not_found', target, result.deleted ? deletedGroup : null)
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
  const target = findPublicTarget(input.targetUsername)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const access = targetAccess(target.account.id)
  const apiKey = resolvePublicApiKey(access, input)
  if (!apiKey) {
    return publicApiKeyResponse('not_found', target, null)
  }
  const updated = updateApiKey(apiKey.id, publicApiKeyPayload(input, true), access)
  return publicApiKeyResponse(updated ? 'updated' : 'not_found', target, updated ? sanitizeApiKey(updated) : null)
}

export function deletePublicApiKey(input: PublicApiKeyDeleteInput): PublicApiKeyResponse {
  const target = findPublicTarget(input.targetUsername)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const access = targetAccess(target.account.id)
  const apiKey = resolvePublicApiKey(access, input)
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

export function mockPublicWelfareAccountPush(input: PublicAccountPushInput): PublicAccountPushResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'openai'
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
      name: input.name,
      providerCode,
      type: 'api_key',
      status: input.status === 'disabled' ? 'disabled' : 'active',
      supportedModels: normalizedStringList(input.supportedModels),
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: input.status !== 'disabled'
    },
    externalId: normalizedText(input.externalId)
  }
}

export function mockPublicWelfareAccountDelete(input: PublicAccountDeleteInput): PublicAccountDeleteResponse {
  const generatedAt = new Date().toISOString()
  const username = normalizedText(input.targetUsername) || 'huanmin'
  const groupName = normalizedText(input.targetGroupName) || '福利'
  const providerCode = normalizedText(input.providerCode) || 'openai'
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
      name: normalizedText(input.name) || accountId,
      providerCode,
      type: 'api_key',
      status: 'disabled',
      boundGroupId: 'mock_group_welfare',
      boundGroupName: groupName,
      schedulable: false
    },
    externalId: normalizedText(input.externalId)
  }
}

export function mockPublicGroupAdd(input: PublicGroupAddInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: normalizedText(input.providerCode) || 'openai',
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
    providerCode: normalizedText(input.providerCode) || 'openai',
    description: normalizedText(input.description),
    enabled: input.enabled !== false,
    groupType: input.groupType ?? 'personal',
    isDefault: false
  })
}

export function mockPublicGroupDelete(input: PublicGroupDeleteInput): PublicGroupResponse {
  return publicMockGroupResponse('mock', input.targetUsername, {
    id: normalizedText(input.groupId) || 'mock_group_public',
    name: normalizedText(input.name) || '公开接口分组',
    providerCode: normalizedText(input.providerCode) || 'openai',
    enabled: true,
    groupType: 'personal',
    isDefault: false
  })
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
    name: normalizedText(input.name) || '公开接口 API Key',
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

function assertProviderEnabled(providerCode: string): void {
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
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

function publicTargetSummary(target: ResolvedTarget): PublicGroupResponse['target'] {
  return {
    username: target.account.username,
    displayName: target.account.displayName,
    systemAccountId: target.account.id,
    created: target.created
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

function publicGroupNotFoundResponse(usernameInput: string): PublicGroupResponse {
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

function publicApiKeyResponse(action: PublicApiKeyResponse['action'], target: ResolvedTarget, apiKey: PublicApiKeySummary | null): PublicApiKeyResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action,
    target: publicTargetSummary(target),
    apiKey
  }
}

function publicApiKeyNotFoundResponse(usernameInput: string): PublicApiKeyResponse {
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

function publicMockGroupResponse(action: PublicGroupResponse['action'], usernameInput: string, group: PublicGroupSummary): PublicGroupResponse {
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

function publicMockApiKeyResponse(action: PublicApiKeyResponse['action'], usernameInput: string, apiKey: PublicApiKeySummary): PublicApiKeyResponse {
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

function resolvePublicGroup(access: TargetAccess, input: { groupId?: string; name?: string; providerCode?: string }): GroupSummary | undefined {
  const groupId = normalizedText(input.groupId)
  if (groupId) {
    return findGroupSummary(groupId, access)
  }
  const name = normalizedText(input.name)
  if (!name) {
    return undefined
  }
  const providerCode = normalizedText(input.providerCode) || 'openai'
  return findExistingTargetGroup({ access, providerCode, groupName: name })
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
  return payload
}

function resolvePublicApiKey(access: TargetAccess, input: { apiKeyId?: string; name?: string }): ApiKeySummary | undefined {
  const apiKeyId = normalizedText(input.apiKeyId)
  if (apiKeyId) {
    return findApiKeySummary(apiKeyId, access)
  }
  const name = normalizedText(input.name)
  if (!name) {
    return undefined
  }
  return listApiKeysPage(access, { keyword: name, page: 1, pageSize: 20 }).items
    .find((item) => sameText(item.name, name))
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
  groupName: string
}): ResolvedGroup {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = listGroupOptions(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))

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
  groupName: string
}): GroupSummary | undefined {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = listGroupOptions(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  }).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))
  return existing ? findGroupSummary(existing.id, input.access) : undefined
}

function findTargetAccountByExternalId(input: {
  access: TargetAccess
  providerCode: string
  groupId: string
  externalId?: string
}): AccountSummary | undefined {
  const externalId = normalizedText(input.externalId)
  if (!externalId) {
    return undefined
  }
  const row = getBusinessDatabase().prepare(`
    SELECT push_records.account_id AS id
    FROM external_integration_account_push_records push_records
    INNER JOIN accounts ON accounts.id = push_records.account_id
    INNER JOIN group_accounts ON group_accounts.account_id = accounts.id
      AND group_accounts.group_id = push_records.group_id
    WHERE push_records.system_account_id = ?
      AND push_records.provider_code = ?
      AND push_records.group_id = ?
      AND push_records.external_id = ?
      AND accounts.system_account_id = push_records.system_account_id
      AND accounts.provider_code = push_records.provider_code
    LIMIT 1
  `).get(
    input.access.systemAccountId,
    input.providerCode,
    input.groupId,
    externalId
  ) as { id?: string } | undefined
  return row?.id ? findAccountSummary(row.id, input.access) : undefined
}

function findTargetAccountById(input: {
  access: TargetAccess
  providerCode: string
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
      AND group_accounts.group_id = ?
    LIMIT 1
  `).get(
    accountId,
    input.access.systemAccountId,
    input.providerCode,
    input.groupId
  ) as { id?: string } | undefined
  return row?.id ? findAccountSummary(row.id, input.access) : undefined
}

function upsertExternalAccountPushRecord(input: {
  systemAccountId: string
  providerCode: string
  groupId: string
  externalId?: string
  accountId: string
}): void {
  const externalId = normalizedText(input.externalId)
  if (!externalId) return
  const now = nowIso()
  getBusinessDatabase().prepare(`
    INSERT INTO external_integration_account_push_records (
      id, system_account_id, provider_code, group_id, external_id, account_id, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, provider_code, group_id, external_id)
    DO UPDATE SET account_id = excluded.account_id, updated_at = excluded.updated_at
  `).run(
    newId('eapush'),
    input.systemAccountId,
    input.providerCode,
    input.groupId,
    externalId,
    input.accountId,
    now,
    now
  )
}

function findTargetAccount(input: {
  access: TargetAccess
  providerCode: string
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
    groupId: input.groupId,
    page: 1,
    pageSize: 20
  }).items.find((item) => item.providerCode === input.providerCode && sameText(item.name, name))
}

function targetFromInput(username: string, groupName: string): PublicAccountDeleteResponse['target'] {
  return {
    username,
    displayName: username,
    systemAccountId: '',
    created: false,
    groupId: '',
    groupName,
    groupCreated: false
  }
}

function notFoundAccountDeleteResponse(
  input: PublicAccountDeleteInput,
  target: PublicAccountDeleteResponse['target']
): PublicAccountDeleteResponse {
  return {
    source: 'stats',
    generatedAt: new Date().toISOString(),
    action: 'not_found',
    target,
    account: null,
    externalId: normalizedText(input.externalId)
  }
}

function accountInputForPush(input: PublicAccountPushInput, providerCode: string, groupId: string): Record<string, unknown> {
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
    providerCode,
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: baseUrl
    },
    supportedModels: normalizedStringList(input.supportedModels),
    status: input.status === 'disabled' ? 'disabled' : 'active',
    concurrencyLimit: boundedInteger(input.concurrencyLimit, 1, 100_000),
    priority: boundedInteger(input.priority, 0, 100_000) ?? 0,
    schedulable: input.status !== 'disabled',
    groupId,
    notes: pushNotes(input)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'availabilitySchedule')) {
    payload.availabilitySchedule = input.availabilitySchedule
  }
  return payload
}

function assertSupportedPushAccountType(value: unknown, providerAccountTypes: readonly string[]): void {
  const type = normalizedText(value) || 'api_key'
  if (type !== 'api_key') {
    throw new Error('账号新增仅支持 API Key 账户')
  }
  if (!providerAccountTypes.includes('api_key')) {
    throw new Error('当前供应商不支持 API Key 账户')
  }
}

function sanitizeAccount(account: AccountSummary): PublicAccountPushResponse['account'] {
  return {
    id: account.id,
    name: account.name,
    providerCode: account.providerCode,
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
    availabilitySchedule: apiKey.availabilitySchedule
  }
}

function pushNotes(input: PublicAccountPushInput): string | undefined {
  const parts = [
    normalizedText(input.notes),
    normalizedText(input.externalId) ? `externalId=${normalizedText(input.externalId)}` : undefined
  ].filter((item): item is string => Boolean(item))
  return parts.length ? parts.join('\n') : undefined
}

function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizedStringList(values: readonly string[] | undefined): string[] | undefined {
  const normalized = [...new Set((values ?? []).map((item) => item.trim()).filter(Boolean))]
  return normalized.length ? normalized : undefined
}

function boundedInteger(value: unknown, min: number, max: number): number | undefined {
  const number = Math.trunc(Number(value))
  if (!Number.isFinite(number)) {
    return undefined
  }
  return Math.min(max, Math.max(min, number))
}

function sameText(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase()
}
