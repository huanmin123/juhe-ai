import { isAdminRole } from '../../domain/types.js'
import { currentSystemAccountId, manageableSystemAccountId, type AccessScope } from '../../storage/access-scope.js'
import {
  findGroupSummary,
  findGroupSummaryAsync,
  findProxy,
  findProxyAsync,
  listGroupOptions,
  listGroupOptionsAsync,
  listProxyOptions,
  listProxyOptionsAsync,
  type GroupOptionSummary,
  type ProxyProfileOptionSummary,
  type ProxyProfileSummary
} from '../../storage/repositories.js'
import { accountImportGroupKey } from './account-import-plan.js'

type ImportAction = 'create' | 'reuse' | 'skip' | 'failed'

export interface AccountImportResolvedOptions {
  createMissingGroups: boolean
  createMissingProxies: boolean
  skipDuplicates: boolean
}

export interface AccountImportResourceContext {
  access?: AccessScope
  options: AccountImportResolvedOptions
  groupLookup: Map<string, GroupOptionSummary | undefined>
  proxyLookup: Map<string, ProxyProfileOptionSummary | undefined>
}

export interface AccountImportResourceAccount {
  providerCode: string
  providerProtocolProfileId?: string
  groupId?: string
  groupName?: string
  proxyRef?: string
  proxyProfileId?: string
}

export interface AccountImportResourceMessageItem {
  messages: string[]
  warnings: string[]
}

export interface AccountImportProxyReferencePlan {
  item: {
    action: ImportAction
    messages: string[]
    warnings: string[]
    proxyProfileId?: string
  }
  proxyProfileId?: string
}

export function importTargetSystemAccountId(access: AccessScope | undefined): string | undefined {
  if (!access) return undefined
  return manageableSystemAccountId(access) ?? currentSystemAccountId(access)
}

export function resolveAccountGroup(
  account: AccountImportResourceAccount,
  context: AccountImportResourceContext,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: Map<string, { providerCode: string, name: string }>,
  item: AccountImportResourceMessageItem
): string | undefined {
  if (account.groupId && account.groupName) {
    item.warnings.push('同时填写 groupId 和 groupName 时优先使用 groupId')
  }
  if (account.groupId) {
    const group = findGroupSummary(account.groupId, context.access)
    if (!group) {
      item.messages.push(`分组不存在或无权使用：${account.groupId}`)
      return undefined
    }
    if (group.providerCode !== account.providerCode) {
      item.messages.push(`分组供应商与账户供应商不一致：${group.name}`)
      return undefined
    }
    return group.id
  }
  if (!account.groupName) {
    item.messages.push('账户 groupId 或 groupName 必填')
    return undefined
  }
  const key = accountImportGroupKey(account.providerCode, account.groupName)
  const existingGroupId = groupIdsByKey.get(key)
  if (existingGroupId) return existingGroupId
  const group = findGroupOptionByName(account.providerCode, account.groupName, context)
  if (group) {
    groupIdsByKey.set(key, group.id)
    return group.id
  }
  if (!context.options.createMissingGroups) {
    item.messages.push(`分组不存在：${account.groupName}`)
    return undefined
  }
  groupNamesToCreate.set(key, {
    providerCode: account.providerCode,
    name: account.groupName
  })
  return undefined
}

export async function resolveAccountGroupAsync(
  account: AccountImportResourceAccount,
  context: AccountImportResourceContext,
  groupIdsByKey: Map<string, string>,
  groupNamesToCreate: Map<string, { providerCode: string, name: string }>,
  item: AccountImportResourceMessageItem
): Promise<string | undefined> {
  if (account.groupId && account.groupName) {
    item.warnings.push('同时填写 groupId 和 groupName 时优先使用 groupId')
  }
  if (account.groupId) {
    const group = await findGroupSummaryAsync(account.groupId, context.access)
    if (!group) {
      item.messages.push(`分组不存在或无权使用：${account.groupId}`)
      return undefined
    }
    if (group.providerCode !== account.providerCode) {
      item.messages.push(`分组供应商与账户供应商不一致：${group.name}`)
      return undefined
    }
    return group.id
  }
  if (!account.groupName) {
    item.messages.push('账户 groupId 或 groupName 必填')
    return undefined
  }
  const key = accountImportGroupKey(account.providerCode, account.groupName)
  const existingGroupId = groupIdsByKey.get(key)
  if (existingGroupId) return existingGroupId
  const group = await findGroupOptionByNameAsync(account.providerCode, account.groupName, context)
  if (group) {
    groupIdsByKey.set(key, group.id)
    return group.id
  }
  if (!context.options.createMissingGroups) {
    item.messages.push(`分组不存在：${account.groupName}`)
    return undefined
  }
  groupNamesToCreate.set(key, {
    providerCode: account.providerCode,
    name: account.groupName
  })
  return undefined
}

export function resolveAccountProxy(
  account: AccountImportResourceAccount,
  proxyByRef: Map<string, AccountImportProxyReferencePlan>,
  item: AccountImportResourceMessageItem
): string | undefined {
  if (account.proxyRef && account.proxyProfileId) {
    item.messages.push('proxyRef 和 proxyProfileId 只能填写一个')
    return undefined
  }
  if (account.proxyProfileId) {
    const proxy = findProxy(account.proxyProfileId)
    if (!proxy) {
      item.messages.push(`代理不存在：${account.proxyProfileId}`)
      return undefined
    }
    if (!proxy.enabled) {
      item.messages.push(`代理已停用：${proxy.name}`)
      return undefined
    }
    return proxy.id
  }
  if (!account.proxyRef) {
    return undefined
  }
  const plannedProxy = proxyByRef.get(account.proxyRef)
  if (plannedProxy) {
    if (plannedProxy.item.action === 'failed') {
      item.messages.push(`代理引用不可用：${account.proxyRef}`)
    }
    if (plannedProxy.item.action === 'skip') {
      item.messages.push(`代理引用未创建：${account.proxyRef}`)
    }
    return plannedProxy.proxyProfileId
  }
  const proxy = findProxy(account.proxyRef)
  if (!proxy) {
    item.messages.push(`代理引用不存在：${account.proxyRef}`)
    return undefined
  }
  if (!proxy.enabled) {
    item.messages.push(`代理已停用：${proxy.name}`)
    return undefined
  }
  return proxy.id
}

export async function resolveAccountProxyAsync(
  account: AccountImportResourceAccount,
  proxyByRef: Map<string, AccountImportProxyReferencePlan>,
  item: AccountImportResourceMessageItem
): Promise<string | undefined> {
  if (account.proxyRef && account.proxyProfileId) {
    item.messages.push('proxyRef 和 proxyProfileId 只能填写一个')
    return undefined
  }
  if (account.proxyProfileId) {
    const proxy = await findProxyAsync(account.proxyProfileId)
    if (!proxy) {
      item.messages.push(`代理不存在：${account.proxyProfileId}`)
      return undefined
    }
    if (!proxy.enabled) {
      item.messages.push(`代理已停用：${proxy.name}`)
      return undefined
    }
    return proxy.id
  }
  if (!account.proxyRef) {
    return undefined
  }
  const plannedProxy = proxyByRef.get(account.proxyRef)
  if (plannedProxy) {
    if (plannedProxy.item.action === 'failed') {
      item.messages.push(`代理引用不可用：${account.proxyRef}`)
    }
    if (plannedProxy.item.action === 'skip') {
      item.messages.push(`代理引用未创建：${account.proxyRef}`)
    }
    return plannedProxy.proxyProfileId
  }
  const proxy = await findProxyAsync(account.proxyRef)
  if (!proxy) {
    item.messages.push(`代理引用不存在：${account.proxyRef}`)
    return undefined
  }
  if (!proxy.enabled) {
    item.messages.push(`代理已停用：${proxy.name}`)
    return undefined
  }
  return proxy.id
}

export function findGroupOptionByName(providerCode: string, name: string, context: AccountImportResourceContext): GroupOptionSummary | undefined {
  const key = accountImportGroupKey(providerCode, name)
  if (context.groupLookup.has(key)) {
    return context.groupLookup.get(key)
  }
  const normalized = name.trim()
  const group = listGroupOptions(context.access, {
    providerCode,
    keyword: name,
    manageableOnly: true,
    limit: 50
  }).find((item) => item.providerCode === providerCode && item.name.trim() === normalized)
  context.groupLookup.set(key, group)
  return group
}

export async function findGroupOptionByNameAsync(providerCode: string, name: string, context: AccountImportResourceContext): Promise<GroupOptionSummary | undefined> {
  const key = accountImportGroupKey(providerCode, name)
  if (context.groupLookup.has(key)) {
    return context.groupLookup.get(key)
  }
  const normalized = name.trim()
  const group = (await listGroupOptionsAsync(context.access, {
    providerCode,
    keyword: name,
    manageableOnly: true,
    limit: 50
  })).find((item) => item.providerCode === providerCode && item.name.trim() === normalized)
  context.groupLookup.set(key, group)
  return group
}

export function findProxyOptionByName(name: string, context: AccountImportResourceContext): ProxyProfileOptionSummary | undefined {
  const key = name.trim()
  if (context.proxyLookup.has(key)) {
    return context.proxyLookup.get(key)
  }
  const proxy = listProxyOptions({ keyword: name, limit: 50 }).find((item) => item.name.trim() === key)
  context.proxyLookup.set(key, proxy)
  return proxy
}

export async function findProxyOptionByNameAsync(name: string, context: AccountImportResourceContext): Promise<ProxyProfileOptionSummary | undefined> {
  const key = name.trim()
  if (context.proxyLookup.has(key)) {
    return context.proxyLookup.get(key)
  }
  const proxy = (await listProxyOptionsAsync({ keyword: name, limit: 50 })).find((item) => item.name.trim() === key)
  context.proxyLookup.set(key, proxy)
  return proxy
}

export function findProxyByName(name: string): ProxyProfileSummary | undefined {
  const key = name.trim()
  const option = listProxyOptions({ keyword: name, limit: 50 }).find((item) => item.name.trim() === key)
  return option ? findProxy(option.id) : undefined
}

export async function findProxyByNameAsync(name: string): Promise<ProxyProfileSummary | undefined> {
  const key = name.trim()
  const option = (await listProxyOptionsAsync({ keyword: name, limit: 50 })).find((item) => item.name.trim() === key)
  return option ? await findProxyAsync(option.id) : undefined
}

export function canCreateImportProxy(context: AccountImportResourceContext, item: AccountImportResourceMessageItem): boolean {
  if (!context.options.createMissingProxies) {
    item.warnings.push('当前导入选项未启用代理创建')
    return false
  }
  if (!isAdminRole(context.access?.role)) {
    item.messages.push('用户侧导入不能创建代理，请由管理员先创建代理')
    return false
  }
  return true
}
