import type { GroupSummary, SystemAccountSummary } from '../../domain/types.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { invalidateSystemAccountLookupCache } from '../../storage/repository-lookups.js'
import {
  createGroup,
  createGroupAsync,
  createGroupInClientAsync,
  createSystemAccountWithPasswordHash,
  createSystemAccountWithPasswordHashAsync,
  createSystemAccountWithPasswordHashInClientAsync,
  findGroupSummary,
  findGroupSummaryAsync,
  findGroupSummaryInClientAsync,
  findSystemAccountById,
  findSystemAccountByIdAsync,
  findSystemAccountByUsername,
  findSystemAccountByUsernameAsync,
  findSystemAccountByUsernameInClientAsync,
  listGroupOptions,
  listGroupOptionsAsync,
  listGroupOptionsInClientAsync
} from '../../storage/repositories.js'

export type PublicPushResolvedTarget = {
  account: SystemAccountSummary
  created: boolean
}

export type PublicPushResolvedGroup = {
  group: GroupSummary
  created: boolean
}

export type PublicPushTargetAccess = {
  systemAccountId: string
  role: 'user'
}

export function assertTargetActive(account: SystemAccountSummary): void {
  if (account.status !== 'active') {
    throw new Error(`目标用户已停用：${account.username}`)
  }
}

export function targetAccess(systemAccountId: string): PublicPushTargetAccess {
  return { systemAccountId, role: 'user' }
}

export function findPublicTarget(usernameInput: string): PublicPushResolvedTarget | undefined {
  const username = normalizedText(usernameInput)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const account = findSystemAccountByUsername(username)
  return account ? { account, created: false } : undefined
}

export async function findPublicTargetAsync(usernameInput: string): Promise<PublicPushResolvedTarget | undefined> {
  const username = normalizedText(usernameInput)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const account = await findSystemAccountByUsernameAsync(username)
  return account ? { account, created: false } : undefined
}

export function requirePublicTarget(usernameInput: string): PublicPushResolvedTarget {
  const target = findPublicTarget(usernameInput)
  if (!target) {
    throw new Error(`目标用户不存在：${normalizedText(usernameInput) ?? ''}`)
  }
  return target
}

export async function requirePublicTargetAsync(usernameInput: string): Promise<PublicPushResolvedTarget> {
  const target = await findPublicTargetAsync(usernameInput)
  if (!target) {
    throw new Error(`目标用户不存在：${normalizedText(usernameInput) ?? ''}`)
  }
  return target
}

export function ensureTargetSystemAccount(input: { targetUsername: string; targetDisplayName?: string }, passwordHash?: string): PublicPushResolvedTarget {
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

export async function ensureTargetSystemAccountAsync(input: { targetUsername: string; targetDisplayName?: string }, passwordHash?: string): Promise<PublicPushResolvedTarget> {
  const username = normalizedText(input.targetUsername)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const existing = await findSystemAccountByUsernameAsync(username)
  if (existing) {
    return { account: existing, created: false }
  }

  const displayName = normalizedText(input.targetDisplayName) || username
  if (!passwordHash) {
    throw new Error('自动创建目标用户缺少密码哈希')
  }
  const account = await createSystemAccountWithPasswordHashAsync({
    username,
    displayName,
    description: '由公开接口自动创建',
    role: 'user',
    status: 'active',
    mustChangePassword: true
  }, passwordHash)
  return { account, created: true }
}

export async function ensureTargetSystemAccountInClientAsync(
  client: DatabaseClient,
  input: { targetUsername: string; targetDisplayName?: string },
  passwordHash?: string
): Promise<PublicPushResolvedTarget> {
  const username = normalizedText(input.targetUsername)
  if (!username) {
    throw new Error('目标用户不能为空')
  }
  const existing = await findSystemAccountByUsernameInClientAsync(client, username)
  if (existing) {
    return { account: existing, created: false }
  }

  const displayName = normalizedText(input.targetDisplayName) || username
  if (!passwordHash) {
    throw new Error('自动创建目标用户缺少密码哈希')
  }
  const account = await createSystemAccountWithPasswordHashInClientAsync(client, {
    username,
    displayName,
    description: '由公开接口自动创建',
    role: 'user',
    status: 'active',
    mustChangePassword: true
  }, passwordHash)
  invalidateSystemAccountLookupCache(account.id)
  return { account, created: true }
}

export function resolvePublicOwnedResourceTarget(usernameInput: string | undefined, ownerSystemAccountId: string): PublicPushResolvedTarget | undefined {
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

export async function resolvePublicOwnedResourceTargetAsync(usernameInput: string | undefined, ownerSystemAccountId: string): Promise<PublicPushResolvedTarget | undefined> {
  const username = normalizedText(usernameInput)
  const account = username
    ? await findSystemAccountByUsernameAsync(username)
    : await findSystemAccountByIdAsync(ownerSystemAccountId)
  if (!account || account.id !== ownerSystemAccountId) {
    return undefined
  }
  return {
    account,
    created: false
  }
}

export function resolvePublicGroup(
  access: PublicPushTargetAccess,
  input: { groupId?: string; name?: string; providerCode?: string }
): GroupSummary | undefined {
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
  return findExistingTargetGroup({ access, providerCode, groupName: name })
}

export async function resolvePublicGroupAsync(
  access: PublicPushTargetAccess,
  input: { groupId?: string; name?: string; providerCode?: string }
): Promise<GroupSummary | undefined> {
  const groupId = normalizedText(input.groupId)
  if (groupId) {
    return await findGroupSummaryAsync(groupId, access)
  }
  const name = normalizedText(input.name)
  if (!name) {
    return undefined
  }
  const providerCode = normalizedText(input.providerCode)
  if (!providerCode) {
    return undefined
  }
  return await findExistingTargetGroupAsync({ access, providerCode, groupName: name })
}

export function resolveAccountListGroupId(
  access: PublicPushTargetAccess,
  input: { providerCode?: string; groupId?: string; targetGroupName?: string }
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
  return findExistingTargetGroup({ access, providerCode, groupName })?.id ?? '__public_group_not_found__'
}

export async function resolveAccountListGroupIdAsync(
  access: PublicPushTargetAccess,
  input: { providerCode?: string; groupId?: string; targetGroupName?: string }
): Promise<string | undefined> {
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
  return (await findExistingTargetGroupAsync({ access, providerCode, groupName }))?.id ?? '__public_group_not_found__'
}

export function ensureTargetGroup(input: {
  access: PublicPushTargetAccess
  providerCode: string
  groupName: string
}): PublicPushResolvedGroup {
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

export async function ensureTargetGroupAsync(input: {
  access: PublicPushTargetAccess
  providerCode: string
  groupName: string
}): Promise<PublicPushResolvedGroup> {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = (await listGroupOptionsAsync(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  })).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))

  if (existing) {
    const group = await findGroupSummaryAsync(existing.id, input.access)
    if (!group) {
      throw new Error('目标分组不存在')
    }
    return {
      group,
      created: false
    }
  }

  return {
    group: await createGroupAsync({
      name: groupName,
      providerCode: input.providerCode,
      description: '由公开接口自动创建',
      enabled: true,
      groupType: 'personal'
    }, input.access),
    created: true
  }
}

export async function ensureTargetGroupInClientAsync(
  client: DatabaseClient,
  input: {
    access: PublicPushTargetAccess
    providerCode: string
    groupName: string
  }
): Promise<PublicPushResolvedGroup> {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = await findExistingTargetGroupInClientAsync(client, input)

  if (existing) {
    return {
      group: existing,
      created: false
    }
  }

  return {
    group: await createGroupInClientAsync(client, {
      name: groupName,
      providerCode: input.providerCode,
      description: '由公开接口自动创建',
      enabled: true,
      groupType: 'personal'
    }, input.access),
    created: true
  }
}

export function findExistingTargetGroup(input: {
  access: PublicPushTargetAccess
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

export async function findExistingTargetGroupAsync(input: {
  access: PublicPushTargetAccess
  providerCode: string
  groupName: string
}): Promise<GroupSummary | undefined> {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = (await listGroupOptionsAsync(input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  })).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))
  return existing ? await findGroupSummaryAsync(existing.id, input.access) : undefined
}

export async function findExistingTargetGroupInClientAsync(
  client: DatabaseClient,
  input: {
    access: PublicPushTargetAccess
    providerCode: string
    groupName: string
  }
): Promise<GroupSummary | undefined> {
  const groupName = normalizedText(input.groupName)
  if (!groupName) {
    throw new Error('目标分组不能为空')
  }
  const existing = (await listGroupOptionsInClientAsync(client, input.access, {
    keyword: groupName,
    providerCode: input.providerCode,
    limit: 20
  })).find((item) => item.providerCode === input.providerCode && sameText(item.name, groupName))
  return existing ? await findGroupSummaryInClientAsync(client, existing.id, input.access) : undefined
}

export function normalizedText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

export function sameText(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase()
}
