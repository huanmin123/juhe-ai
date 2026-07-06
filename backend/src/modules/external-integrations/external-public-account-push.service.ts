import { randomBytes } from 'node:crypto'

import type { AccountSummary, GroupSummary, ProviderDefinition, ProviderProtocolProfileDefinition } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { hashPasswordAsync } from '../../storage/crypto.js'
import {
  createAccount,
  createAccountInClientAsync,
  createApiKeyRecord,
  createApiKeyRecordAsync,
  createGroup,
  createGroupInClientAsync,
  deleteAccountWithRelatedCleanup,
  deleteAccountWithRelatedCleanupAsync,
  deleteApiKeyWithRelatedCleanup,
  deleteApiKeyWithRelatedCleanupAsync,
  deleteGroup,
  deleteGroupAsync,
  findAccountSummary,
  findAccountSummaryAsync,
  findApiKeySummary,
  findApiKeySummaryAsync,
  findGroupSummary,
  findGroupSummaryAsync,
  listAccountsPage,
  listAccountsPageAsync,
  listApiKeysPage,
  listApiKeysPageAsync,
  listGroupsPage,
  listGroupsPageAsync,
  listProviders,
  listProvidersAsync,
  updateAccount,
  updateAccountAsync,
  updateApiKey,
  updateApiKeyAsync,
  updateGroup,
  updateGroupAsync
} from '../../storage/repositories.js'
import { getBusinessDatabase, runInDatabaseTransaction } from '../../storage/database.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { getPostgresPool } from '../../storage/postgres-client.js'
import { submitApiKeyRelatedCleanup, submitApiKeyRelatedCleanupAsync } from '../api-keys/api-key-cleanup.service.js'
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
  ensureTargetGroupInClientAsync,
  ensureTargetSystemAccount,
  ensureTargetSystemAccountInClientAsync,
  findExistingTargetGroup,
  findExistingTargetGroupAsync,
  findExistingTargetGroupInClientAsync,
  findPublicTarget,
  findPublicTargetAsync,
  normalizedText,
  requirePublicTarget,
  requirePublicTargetAsync,
  resolveAccountListGroupId,
  resolveAccountListGroupIdAsync,
  resolvePublicGroup,
  resolvePublicOwnedResourceTarget,
  resolvePublicOwnedResourceTargetAsync,
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

const publicResourceOwnerLookupAccess = {
  systemAccountId: '__public_resource_owner_lookup__',
  role: 'super_admin' as const
}

export async function addPublicWelfareAccountAsync(input: PublicAccountPushInput): Promise<PublicAccountPushResponse> {
  const targetPasswordHash = await autoCreatedTargetPasswordHash()
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return writePublicWelfareAccount(input, targetPasswordHash)
  }
  return await writePublicWelfareAccountAsync(input, targetPasswordHash)
}

export function updatePublicWelfareAccount(input: PublicAccountUpdateInput): PublicAccountPushResponse {
  return updatePublicWelfareAccountById(input)
}

export async function updatePublicWelfareAccountAsync(input: PublicAccountUpdateInput): Promise<PublicAccountPushResponse> {
  return await updatePublicWelfareAccountByIdAsync(input)
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
    const targetGroup = ensureTargetGroup({ access, providerCode, groupName: input.targetGroupName })
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

async function writePublicWelfareAccountAsync(input: PublicAccountPushInput, targetPasswordHash?: string): Promise<PublicAccountPushResponse> {
  const providerCode = requiredProviderCode(input.providerCode)
  const provider = (await listProvidersAsync()).find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  const providerProfile = requireProviderProtocolProfile(provider, input.providerProtocolProfileId)
  assertSupportedPushAccountType(input.type, providerProfile.accountTypes)

  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => {
    const target = await ensureTargetSystemAccountInClientAsync(tx, input, targetPasswordHash)
    assertTargetActive(target.account)

    const access = targetAccess(target.account.id)
    const targetGroup = await ensureTargetGroupInClientAsync(tx, { access, providerCode, groupName: input.targetGroupName })
    const existing = await findTargetAccountAsync({
      access,
      providerCode,
      providerProtocolProfileId: providerProfile.id,
      groupId: targetGroup.group.id,
      name: input.name
    })
    if (existing) {
      throw new Error(`账号已存在：${existing.name}`)
    }
    const account = await createAccountInClientAsync(tx, accountCreateInputForPush(input, providerCode, providerProfile.id, targetGroup.group.id), access)

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
  })
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

async function updatePublicWelfareAccountByIdAsync(input: PublicAccountUpdateInput): Promise<PublicAccountPushResponse> {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    throw new Error('账号修改必须提供 accountId')
  }

  const accountOwner = await findPublicAccountOwnerByIdAsync(accountId)
  if (!accountOwner) {
    throw new Error('账号不存在')
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, accountOwner.systemAccountId)
  if (!target) {
    throw new Error('账号不存在')
  }
  assertTargetActive(target.account)

  const access = targetAccess(target.account.id)
  const existing = await findAccountSummaryAsync(accountId, access)
  if (!existing) {
    throw new Error('账号不存在')
  }
  if (existing.type !== 'api_key') {
    throw new Error('公开账号修改仅支持 API Key 账户')
  }

  const providerProfile = await assertProviderEnabledAsync(existing.providerCode, existing.providerProtocolProfileId)
  if (hasPublicInput(input, 'type')) {
    assertSupportedPushAccountType(input.type, providerProfile.accountTypes)
  }
  const targetGroup = await resolvePublicAccountGroupFilterAsync(input, existing, access)
  const payload = accountPartialUpdateInputForPush(input, existing)
  const updated = await updateAccountAsync(existing.id, payload, access)
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

export async function deletePublicWelfareAccountAsync(input: PublicAccountDeleteInput): Promise<PublicAccountDeleteResponse> {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    throw new Error('删除账号时必须提供 accountId')
  }

  const fallbackTarget = targetFromInput(input.targetUsername, input.targetGroupName)
  const accountOwner = await findPublicAccountOwnerByIdAsync(accountId)
  if (!accountOwner) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, accountOwner.systemAccountId)
  if (!target) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const account = await findAccountSummaryAsync(accountId, access)
  if (!account) {
    return notFoundAccountDeleteResponse(fallbackTarget)
  }
  if (account.type !== 'api_key') {
    throw new Error('公开账号删除仅支持 API Key 账户')
  }
  const targetGroup = await resolvePublicAccountGroupFilterAsync(input, account, access)
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
  const deleteResult = await deleteAccountWithRelatedCleanupAsync(account.id, access)
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

export async function listPublicWelfareAccountsAsync(input: PublicAccountListInput): Promise<PublicAccountListResponse> {
  const target = await requirePublicTargetAsync(input.targetUsername)
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const providerCode = normalizedText(input.providerCode)
  const providerProtocolProfileId = await resolveOptionalProviderProtocolProfileIdAsync(providerCode, input.providerProtocolProfileId)
  const groupId = await resolveAccountListGroupIdAsync(access, {
    providerCode,
    groupId: input.groupId,
    targetGroupName: input.targetGroupName
  })
  const page = await listAccountsPageAsync(access, {
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

export async function addPublicGroupAsync(input: PublicGroupAddInput): Promise<PublicGroupResponse> {
  const providerCode = requiredProviderCode(input.providerCode)
  if (runtimeConfig.databaseDriver !== 'postgres') {
    assertProviderCodeEnabled(providerCode)
    const targetPasswordHash = await autoCreatedTargetPasswordHash()
    return runInDatabaseTransaction(() => {
      const target = ensureTargetSystemAccount(input, targetPasswordHash)
      assertTargetActive(target.account)
      const access = targetAccess(target.account.id)
      const existing = resolvePublicGroup(access, { name: input.name, providerCode })
      if (existing) {
        return publicGroupResponse('existing', target, sanitizeGroup(existing))
      }
      const groupInput: Record<string, unknown> = {
        name: input.name,
        providerCode,
        description: input.description,
        groupType: input.groupType ?? 'personal'
      }
      if (input.enabled !== undefined) groupInput.enabled = input.enabled
      const group = createGroup(groupInput, access)
      return publicGroupResponse('created', target, sanitizeGroup(group))
    }, getBusinessDatabase())
  }

  await assertProviderCodeEnabledAsync(providerCode)
  const targetPasswordHash = await autoCreatedTargetPasswordHash()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return await client.transaction(async (tx) => {
    const target = await ensureTargetSystemAccountInClientAsync(tx, input, targetPasswordHash)
    assertTargetActive(target.account)
    const access = targetAccess(target.account.id)
    const existing = await findExistingTargetGroupInClientAsync(tx, { access, providerCode, groupName: input.name })
    if (existing) {
      return publicGroupResponse('existing', target, sanitizeGroup(existing))
    }
    const groupInput: Record<string, unknown> = {
      name: input.name,
      providerCode,
      description: input.description,
      groupType: input.groupType ?? 'personal'
    }
    if (input.enabled !== undefined) groupInput.enabled = input.enabled
    const group = await createGroupInClientAsync(tx, groupInput, access)
    return publicGroupResponse('created', target, sanitizeGroup(group))
  })
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
    assertProviderCodeEnabled(input.providerCode)
  }
  const updated = updateGroup(group.id, publicGroupUpdatePayload(input), access)
  if (!updated) {
    return publicGroupResponse('not_found', target, null)
  }
  return publicGroupResponse('updated', target, sanitizeGroup(updated))
}

export async function updatePublicGroupAsync(input: PublicGroupUpdateInput): Promise<PublicGroupResponse> {
  const groupId = normalizedText(input.groupId)
  if (!groupId) {
    throw new Error('分组修改必须提供 groupId')
  }
  const owner = await findPublicGroupOwnerByIdAsync(groupId)
  if (!owner) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const group = await findGroupSummaryAsync(groupId, access)
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  if (input.providerCode) {
    await assertProviderCodeEnabledAsync(input.providerCode)
  }
  const updated = await updateGroupAsync(group.id, publicGroupUpdatePayload(input), access)
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

export async function deletePublicGroupAsync(input: PublicGroupDeleteInput): Promise<PublicGroupResponse> {
  const groupId = normalizedText(input.groupId)
  if (!groupId) {
    throw new Error('分组删除必须提供 groupId')
  }
  const owner = await findPublicGroupOwnerByIdAsync(groupId)
  if (!owner) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicGroupNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const group = await findGroupSummaryAsync(groupId, access)
  if (!group) {
    return publicGroupResponse('not_found', target, null)
  }
  const deletedGroup = sanitizeGroup(group)
  const result = await deleteGroupAsync(group.id, access)
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

export async function listPublicGroupsAsync(input: PublicGroupListInput): Promise<PublicGroupListResponse> {
  const target = await requirePublicTargetAsync(input.targetUsername)
  assertTargetActive(target.account)
  const page = await listGroupsPageAsync(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    keyword: normalizedText(input.keyword),
    providerCode: normalizedText(input.providerCode),
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

export async function addPublicApiKeyAsync(input: PublicApiKeyAddInput): Promise<PublicApiKeyResponse> {
  const target = await findPublicTargetAsync(input.targetUsername)
  if (!target) {
    throw new Error(`目标用户不存在：${input.targetUsername}`)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const payload = publicApiKeyPayload(input)
  const apiKey = await createApiKeyRecordAsync(payload, access)
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

export async function updatePublicApiKeyAsync(input: PublicApiKeyUpdateInput): Promise<PublicApiKeyResponse> {
  const apiKeyId = normalizedText(input.apiKeyId)
  if (!apiKeyId) {
    throw new Error('API Key 修改必须提供 apiKeyId')
  }
  const owner = await findPublicApiKeyOwnerByIdAsync(apiKeyId)
  if (!owner) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const apiKey = await findApiKeySummaryAsync(apiKeyId, access)
  if (!apiKey) {
    return publicApiKeyResponse('not_found', target, null)
  }
  const updated = await updateApiKeyAsync(apiKey.id, publicApiKeyPayload(input, true), access)
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

export async function deletePublicApiKeyAsync(input: PublicApiKeyDeleteInput): Promise<PublicApiKeyResponse> {
  const apiKeyId = normalizedText(input.apiKeyId)
  if (!apiKeyId) {
    throw new Error('API Key 删除必须提供 apiKeyId')
  }
  const owner = await findPublicApiKeyOwnerByIdAsync(apiKeyId)
  if (!owner) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  const target = await resolvePublicOwnedResourceTargetAsync(input.targetUsername, owner.systemAccountId)
  if (!target) {
    return publicApiKeyNotFoundResponse(input.targetUsername)
  }
  assertTargetActive(target.account)
  const access = targetAccess(target.account.id)
  const apiKey = await findApiKeySummaryAsync(apiKeyId, access)
  if (!apiKey) {
    return publicApiKeyResponse('not_found', target, null)
  }
  const deletedApiKey = sanitizeApiKey(apiKey)
  const result = await deleteApiKeyWithRelatedCleanupAsync(apiKey.id, access)
  if (result.cleanupTarget) {
    await submitApiKeyRelatedCleanupAsync(result.cleanupTarget)
  }
  return publicApiKeyResponse(result.deleted ? 'deleted' : 'not_found', target, result.deleted ? deletedApiKey : null)
}

export function listPublicApiKeys(input: PublicApiKeyListInput): PublicApiKeyListResponse {
  const target = requirePublicTarget(input.targetUsername)
  assertTargetActive(target.account)
  const page = listApiKeysPage(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    routeStrategyId: normalizedText(input.routeStrategyId),
    keyword: normalizedText(input.keyword),
    status: input.status
  })
  return publicApiKeyListResponse(target, {
    page: page.page,
    pageSize: page.pageSize,
    pageUpperBound: page.total,
    hasMore: page.hasMore,
    items: page.items.map((apiKey) => sanitizeApiKey(apiKey))
  })
}

export async function listPublicApiKeysAsync(input: PublicApiKeyListInput): Promise<PublicApiKeyListResponse> {
  const target = await requirePublicTargetAsync(input.targetUsername)
  assertTargetActive(target.account)
  const page = await listApiKeysPageAsync(targetAccess(target.account.id), {
    page: input.page,
    pageSize: input.pageSize,
    routeStrategyId: normalizedText(input.routeStrategyId),
    keyword: normalizedText(input.keyword),
    status: input.status
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

function assertProviderCodeEnabled(providerCode: string): ProviderDefinition {
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return provider
}

async function assertProviderEnabledAsync(providerCode: string, providerProtocolProfileId?: string): Promise<ProviderProtocolProfileDefinition> {
  const provider = (await listProvidersAsync()).find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return requireProviderProtocolProfile(provider, providerProtocolProfileId)
}

async function assertProviderCodeEnabledAsync(providerCode: string): Promise<ProviderDefinition> {
  const provider = (await listProvidersAsync()).find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${providerCode}`)
  }
  return provider
}

function requireProviderProtocolProfile(provider: ProviderDefinition, providerProtocolProfileId?: string): ProviderProtocolProfileDefinition {
  const profileId = normalizedText(providerProtocolProfileId)
  if (!profileId) {
    throw new Error('providerProtocolProfileId 不能为空')
  }
  const profile = provider.protocolProfiles.find((item) => item.id === profileId)
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
  if (!providerProtocolProfileId) {
    return undefined
  }
  const provider = listProviders().find((item) => item.code === providerCode)
  if (!provider) {
    throw new Error(`不支持的供应商：${providerCode}`)
  }
  return requireProviderProtocolProfile(provider, providerProtocolProfileId).id
}

async function resolveOptionalProviderProtocolProfileIdAsync(providerCodeInput?: string, providerProtocolProfileIdInput?: string): Promise<string | undefined> {
  const providerCode = normalizedText(providerCodeInput)
  const providerProtocolProfileId = normalizedText(providerProtocolProfileIdInput)
  if (!providerCode && !providerProtocolProfileId) return undefined
  if (!providerCode) {
    throw new Error('按协议档案查询时必须提供 providerCode')
  }
  if (!providerProtocolProfileId) {
    return undefined
  }
  const provider = (await listProvidersAsync()).find((item) => item.code === providerCode)
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

async function findTargetAccountByIdAsync(input: {
  access: ReturnType<typeof targetAccess>
  providerCode: string
  providerProtocolProfileId?: string
  groupId: string
  accountId?: string
}): Promise<AccountSummary | undefined> {
  const accountId = normalizedText(input.accountId)
  if (!accountId) {
    return undefined
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const page = await listAccountsPageAsync(input.access, {
      ids: [accountId],
      providerCode: input.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      groupId: input.groupId,
      page: 1,
      pageSize: 1
    })
    return page.items.find((account) => account.id === accountId)
  }

  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ id?: string }>(`
    SELECT accounts.id AS id
    FROM juhe_business.accounts accounts
    INNER JOIN juhe_business.group_accounts group_accounts ON group_accounts.account_id = accounts.id
    WHERE accounts.id = ?
      AND accounts.system_account_id = ?
      AND accounts.provider_code = ?
      AND (? IS NULL OR accounts.provider_protocol_profile_id = ?)
      AND group_accounts.group_id = ?
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `, [
    accountId,
    input.access.systemAccountId,
    input.providerCode,
    input.providerProtocolProfileId ?? null,
    input.providerProtocolProfileId ?? null,
    input.groupId
  ])
  return row?.id ? await findAccountSummaryAsync(row.id, input.access) : undefined
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

async function findPublicAccountOwnerByIdAsync(accountId: string): Promise<{ id: string; systemAccountId: string } | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const account = await findAccountSummaryAsync(accountId, publicResourceOwnerLookupAccess)
    return account?.systemAccountId ? { id: account.id, systemAccountId: account.systemAccountId } : undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ id: string; systemAccountId: string }>(`
    SELECT id, system_account_id AS "systemAccountId"
    FROM juhe_business.accounts
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [accountId])
  return row ? { id: row.id, systemAccountId: row.systemAccountId } : undefined
}

function findPublicGroupOwnerById(groupId: string): { id: string; systemAccountId: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, system_account_id AS systemAccountId
    FROM groups
    WHERE id = ?
    LIMIT 1
  `).get(groupId) as { id: string; systemAccountId: string } | undefined
}

async function findPublicGroupOwnerByIdAsync(groupId: string): Promise<{ id: string; systemAccountId: string } | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const group = await findGroupSummaryAsync(groupId, publicResourceOwnerLookupAccess)
    return group?.systemAccountId ? { id: group.id, systemAccountId: group.systemAccountId } : undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ id: string; systemAccountId: string }>(`
    SELECT id, system_account_id AS "systemAccountId"
    FROM juhe_business.groups
    WHERE id = ?
    LIMIT 1
  `, [groupId])
  return row ? { id: row.id, systemAccountId: row.systemAccountId } : undefined
}

function findPublicApiKeyOwnerById(apiKeyId: string): { id: string; systemAccountId: string } | undefined {
  return getBusinessDatabase().prepare(`
    SELECT id, system_account_id AS systemAccountId
    FROM api_keys
    WHERE id = ?
    LIMIT 1
  `).get(apiKeyId) as { id: string; systemAccountId: string } | undefined
}

async function findPublicApiKeyOwnerByIdAsync(apiKeyId: string): Promise<{ id: string; systemAccountId: string } | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const apiKey = await findApiKeySummaryAsync(apiKeyId, publicResourceOwnerLookupAccess)
    return apiKey?.systemAccountId ? { id: apiKey.id, systemAccountId: apiKey.systemAccountId } : undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<{ id: string; systemAccountId: string }>(`
    SELECT id, system_account_id AS "systemAccountId"
    FROM juhe_business.api_keys
    WHERE id = ?
    LIMIT 1
  `, [apiKeyId])
  return row ? { id: row.id, systemAccountId: row.systemAccountId } : undefined
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
  const providerProtocolProfileId = resolveOptionalProviderProtocolProfileId(providerCode || account.providerCode, input.providerProtocolProfileId)
  if (providerProtocolProfileId && providerProtocolProfileId !== account.providerProtocolProfileId) {
    throw new Error('账号不存在')
  }
  const accountProviderProtocolProfileId = normalizedText(account.providerProtocolProfileId)
  if (!accountProviderProtocolProfileId) {
    throw new Error('账号 providerProtocolProfileId 不能为空')
  }

  const groupName = normalizedText(input.targetGroupName)
  if (!groupName) {
    return account.boundGroupId ? findGroupSummary(account.boundGroupId, access) : undefined
  }

  const group = findExistingTargetGroup({
    access,
    providerCode: account.providerCode,
    groupName
  })
  if (!group) {
    throw new Error('账号不存在')
  }
  const accountInGroup = findTargetAccountById({
    access,
    providerCode: account.providerCode,
    providerProtocolProfileId: accountProviderProtocolProfileId,
    groupId: group.id,
    accountId: account.id
  })
  if (!accountInGroup) {
    throw new Error('账号不存在')
  }
  return group
}

async function resolvePublicAccountGroupFilterAsync(
  input: { targetGroupName?: string; providerCode?: string; providerProtocolProfileId?: string },
  account: AccountSummary,
  access: ReturnType<typeof targetAccess>
): Promise<GroupSummary | undefined> {
  const providerCode = normalizedText(input.providerCode)
  if (providerCode && providerCode !== account.providerCode) {
    throw new Error('账号不存在')
  }
  const providerProtocolProfileId = await resolveOptionalProviderProtocolProfileIdAsync(providerCode || account.providerCode, input.providerProtocolProfileId)
  if (providerProtocolProfileId && providerProtocolProfileId !== account.providerProtocolProfileId) {
    throw new Error('账号不存在')
  }
  const accountProviderProtocolProfileId = normalizedText(account.providerProtocolProfileId)
  if (!accountProviderProtocolProfileId) {
    throw new Error('账号 providerProtocolProfileId 不能为空')
  }

  const groupName = normalizedText(input.targetGroupName)
  if (!groupName) {
    return account.boundGroupId ? await findGroupSummaryAsync(account.boundGroupId, access) : undefined
  }

  const group = await findExistingTargetGroupAsync({
    access,
    providerCode: account.providerCode,
    groupName
  })
  if (!group) {
    throw new Error('账号不存在')
  }
  const accountInGroup = await findTargetAccountByIdAsync({
    access,
    providerCode: account.providerCode,
    providerProtocolProfileId: accountProviderProtocolProfileId,
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

async function findTargetAccountAsync(input: {
  access: ReturnType<typeof targetAccess>
  providerCode: string
  providerProtocolProfileId: string
  groupId: string
  name: string
}): Promise<AccountSummary | undefined> {
  const name = normalizedText(input.name)
  if (!name) {
    throw new Error('账户名称不能为空')
  }
  const page = await listAccountsPageAsync(input.access, {
    keyword: name,
    providerCode: input.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    groupId: input.groupId,
    page: 1,
    pageSize: 20
  })
  return page.items.find((item) => item.providerCode === input.providerCode && item.providerProtocolProfileId === input.providerProtocolProfileId && sameText(item.name, name))
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
