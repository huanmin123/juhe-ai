import { randomBytes } from 'node:crypto'

import type { AccountSummary, GroupSummary, ProviderDefinition, ProviderProtocolProfileDefinition } from '../../domain/types.js'
import { hashPasswordAsync } from '../../storage/crypto.js'
import {
  createAccount,
  createApiKeyRecord,
  createGroup,
  deleteAccountWithRelatedCleanup,
  deleteApiKeyWithRelatedCleanup,
  deleteGroup,
  findAccountSummary,
  findApiKeySummary,
  findGroupSummary,
  listAccountsPage,
  listApiKeysPage,
  listGroupsPage,
  listProviders,
  updateAccount,
  updateApiKey,
  updateGroup
} from '../../storage/repositories.js'
import { getBusinessDatabase, runInDatabaseTransaction } from '../../storage/database.js'
import { submitApiKeyRelatedCleanup } from '../api-keys/api-key-cleanup.service.js'
import {
  accountCreateInputForPush,
  accountPartialUpdateInputForPush,
  publicApiKeyPayload,
  publicGroupUpdatePayload
} from './external-public-account-push.payload.js'
import {
  notFoundAccountDeleteResponse,
  publicAccountListResponse,
  publicApiKeyListResponse,
  publicApiKeyNotFoundResponse,
  publicApiKeyResponse,
  publicGroupListResponse,
  publicGroupNotFoundResponse,
  publicGroupResponse,
  sanitizeAccount,
  sanitizeApiKey,
  sanitizeGroup,
  targetFromInput
} from './external-public-account-push.sanitize.js'
import {
  assertTargetActive,
  ensureTargetGroup,
  ensureTargetSystemAccount,
  findExistingTargetGroup,
  findPublicTarget,
  normalizedText,
  requirePublicTarget,
  resolveAccountListGroupId,
  resolvePublicGroup,
  resolvePublicOwnedResourceTarget,
  sameText,
  targetAccess
} from './external-public-account-push.target.js'
import type {
  PublicAccountDeleteInput,
  PublicAccountDeleteResponse,
  PublicAccountListInput,
  PublicAccountListResponse,
  PublicAccountPushInput,
  PublicAccountPushResponse,
  PublicAccountUpdateInput,
  PublicApiKeyAddInput,
  PublicApiKeyDeleteInput,
  PublicApiKeyListInput,
  PublicApiKeyListResponse,
  PublicApiKeyResponse,
  PublicApiKeyUpdateInput,
  PublicGroupAddInput,
  PublicGroupDeleteInput,
  PublicGroupListInput,
  PublicGroupListResponse,
  PublicGroupResponse,
  PublicGroupUpdateInput
} from './external-public-account-push.types.js'

export type {
  PublicAccountDeleteInput,
  PublicAccountDeleteResponse,
  PublicAccountListInput,
  PublicAccountListItem,
  PublicAccountListResponse,
  PublicAccountPushInput,
  PublicAccountPushResponse,
  PublicAccountUpdateInput,
  PublicApiKeyAddInput,
  PublicApiKeyDeleteInput,
  PublicApiKeyListInput,
  PublicApiKeyListResponse,
  PublicApiKeyResponse,
  PublicApiKeySummary,
  PublicApiKeyUpdateInput,
  PublicGroupAddInput,
  PublicGroupDeleteInput,
  PublicGroupListInput,
  PublicGroupListResponse,
  PublicGroupResponse,
  PublicGroupSummary,
  PublicGroupUpdateInput
} from './external-public-account-push.types.js'

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

    const access = targetAccess(target.account.id)
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

async function autoCreatedTargetPasswordHash(): Promise<string> {
  return hashPasswordAsync(randomBytes(18).toString('base64url'))
}

function findTargetAccountById(input: {
  access: ReturnType<typeof targetAccess>
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

function resolvePublicAccountGroupFilter(
  input: { targetGroupName?: string; providerCode?: string; providerProtocolProfileId?: string },
  account: AccountSummary,
  access: ReturnType<typeof targetAccess>
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
  access: ReturnType<typeof targetAccess>
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

function hasPublicInput(input: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(input, key)
}
