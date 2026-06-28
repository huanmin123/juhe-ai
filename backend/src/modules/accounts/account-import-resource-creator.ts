import { type AccessScope } from '../../storage/access-scope.js'
import {
  createGroup,
  createGroupAsync,
  createProxy,
  createProxyAsync
} from '../../storage/repositories.js'
import { errorMessage, type AccountImportProxyType } from './account-import-field-parser.js'
import {
  accountImportGroupKey,
  type AccountImportGroupCreateMap
} from './account-import-plan.js'
import { findGroupOptionByName, findGroupOptionByNameAsync, findProxyByName, findProxyByNameAsync } from './account-import-resource-resolver.js'
import type { AccountImportItem, AccountImportProxyItem, AccountImportSummary } from './account-import.service.js'

export interface AccountImportResourceCreatePlan {
  result: {
    summary: AccountImportSummary
  }
  accounts: AccountImportResourceAccountPlan[]
  proxies: AccountImportResourceProxyPlan[]
  groupIdsByKey: Map<string, string>
  groupNamesToCreate: AccountImportGroupCreateMap
}

export interface AccountImportResourceAccountPlan {
  source: {
    groupName?: string
    providerCode: string
    providerProtocolProfileId?: string
    proxyRef?: string
  }
  item: AccountImportItem
  proxyProfileId?: string
}

export interface AccountImportResourceProxyPlan {
  source: {
    ref: string
    name: string
    type: AccountImportProxyType
    host: string
    port: number
    username?: string
    password?: string
    description?: string
    enabled: boolean
  }
  item: AccountImportProxyItem
  proxyProfileId?: string
}

export function createPlannedImportProxies(plan: AccountImportResourceCreatePlan, access: AccessScope): Map<string, string> {
  const created = new Map<string, string>()
  for (const proxy of plan.proxies) {
    if (proxy.item.action === 'reuse' && proxy.proxyProfileId) {
      created.set(proxy.source.ref, proxy.proxyProfileId)
      continue
    }
    if (proxy.item.action !== 'create') continue
    try {
      const createdProxy = createProxy({
        name: proxy.source.name,
        description: proxy.source.description,
        type: proxy.source.type,
        host: proxy.source.host,
        port: proxy.source.port,
        username: proxy.source.username,
        password: proxy.source.password,
        enabled: proxy.source.enabled
      }, access)
      proxy.proxyProfileId = createdProxy.id
      proxy.item.proxyProfileId = createdProxy.id
      proxy.item.messages = ['已创建代理']
      created.set(proxy.source.ref, createdProxy.id)
    } catch (error) {
      const existing = findProxyByName(proxy.source.name)
      if (existing) {
        proxy.item.action = 'reuse'
        proxy.item.proxyProfileId = existing.id
        proxy.item.messages = ['代理名称已存在，已复用现有代理']
        proxy.item.warnings.push(errorMessage(error))
        proxy.proxyProfileId = existing.id
        created.set(proxy.source.ref, existing.id)
        plan.result.summary.proxies.create -= 1
        plan.result.summary.proxies.reuse += 1
        continue
      }
      proxy.item.action = 'failed'
      proxy.item.messages = [errorMessage(error)]
      plan.result.summary.proxies.create -= 1
      plan.result.summary.proxies.failed += 1
    }
  }
  return created
}

export async function createPlannedImportProxiesAsync(plan: AccountImportResourceCreatePlan, access: AccessScope): Promise<Map<string, string>> {
  const created = new Map<string, string>()
  for (const proxy of plan.proxies) {
    if (proxy.item.action === 'reuse' && proxy.proxyProfileId) {
      created.set(proxy.source.ref, proxy.proxyProfileId)
      continue
    }
    if (proxy.item.action !== 'create') continue
    try {
      const createdProxy = await createProxyAsync({
        name: proxy.source.name,
        description: proxy.source.description,
        type: proxy.source.type,
        host: proxy.source.host,
        port: proxy.source.port,
        username: proxy.source.username,
        password: proxy.source.password,
        enabled: proxy.source.enabled
      }, access)
      proxy.proxyProfileId = createdProxy.id
      proxy.item.proxyProfileId = createdProxy.id
      proxy.item.messages = ['已创建代理']
      created.set(proxy.source.ref, createdProxy.id)
    } catch (error) {
      const existing = await findProxyByNameAsync(proxy.source.name)
      if (existing) {
        proxy.item.action = 'reuse'
        proxy.item.proxyProfileId = existing.id
        proxy.item.messages = ['代理名称已存在，已复用现有代理']
        proxy.item.warnings.push(errorMessage(error))
        proxy.proxyProfileId = existing.id
        created.set(proxy.source.ref, existing.id)
        plan.result.summary.proxies.create -= 1
        plan.result.summary.proxies.reuse += 1
        continue
      }
      proxy.item.action = 'failed'
      proxy.item.messages = [errorMessage(error)]
      plan.result.summary.proxies.create -= 1
      plan.result.summary.proxies.failed += 1
    }
  }
  return created
}

export function failAccountsWithUnresolvedImportProxy(plan: AccountImportResourceCreatePlan): void {
  const proxyByRef = new Map(plan.proxies.map((proxy) => [proxy.source.ref, proxy]))
  for (const account of plan.accounts) {
    if (account.item.action !== 'create' || !account.source.proxyRef || account.proxyProfileId) continue
    const proxy = proxyByRef.get(account.source.proxyRef)
    if (!proxy) continue
    account.item.action = 'failed'
    account.item.messages = [`代理创建失败，账户未导入：${account.source.proxyRef}`]
    plan.result.summary.accounts.create -= 1
    plan.result.summary.accounts.failed += 1
  }
}

export function createPlannedImportGroups(plan: AccountImportResourceCreatePlan, access: AccessScope): void {
  for (const [key, group] of plan.groupNamesToCreate) {
    try {
      const created = createGroup({
        providerCode: group.providerCode,
        name: group.name,
        description: '由账户导入自动创建'
      }, access)
      plan.groupIdsByKey.set(key, created.id)
    } catch (error) {
      const existing = findGroupOptionByName(group.providerCode, group.providerProtocolProfileId, group.name, {
        access,
        options: {
          createMissingGroups: true,
          createMissingProxies: true,
          skipDuplicates: true
        },
        groupLookup: new Map(),
        proxyLookup: new Map()
      })
      if (existing) {
        plan.groupIdsByKey.set(key, existing.id)
        plan.result.summary.groups.create -= 1
        plan.result.summary.groups.reuse += 1
        continue
      }
      plan.result.summary.groups.create -= 1
      plan.result.summary.groups.failed += 1
      for (const account of plan.accounts) {
        if (
          account.source.groupName &&
          account.source.providerCode &&
          accountImportGroupKey(account.source.providerCode, account.source.groupName) === key &&
          account.item.action === 'create'
        ) {
          account.item.action = 'failed'
          account.item.messages = [errorMessage(error)]
          plan.result.summary.accounts.create -= 1
          plan.result.summary.accounts.failed += 1
        }
      }
    }
  }
}

export async function createPlannedImportGroupsAsync(plan: AccountImportResourceCreatePlan, access: AccessScope): Promise<void> {
  for (const [key, group] of plan.groupNamesToCreate) {
    try {
      const created = await createGroupAsync({
        providerCode: group.providerCode,
        name: group.name,
        description: '由账户导入自动创建'
      }, access)
      plan.groupIdsByKey.set(key, created.id)
    } catch (error) {
      const existing = await findGroupOptionByNameAsync(group.providerCode, group.providerProtocolProfileId, group.name, {
        access,
        options: {
          createMissingGroups: true,
          createMissingProxies: true,
          skipDuplicates: true
        },
        groupLookup: new Map(),
        proxyLookup: new Map()
      })
      if (existing) {
        plan.groupIdsByKey.set(key, existing.id)
        plan.result.summary.groups.create -= 1
        plan.result.summary.groups.reuse += 1
        continue
      }
      plan.result.summary.groups.create -= 1
      plan.result.summary.groups.failed += 1
      for (const account of plan.accounts) {
        if (
          account.source.groupName &&
          account.source.providerCode &&
          accountImportGroupKey(account.source.providerCode, account.source.groupName) === key &&
          account.item.action === 'create'
        ) {
          account.item.action = 'failed'
          account.item.messages = [errorMessage(error)]
          plan.result.summary.accounts.create -= 1
          plan.result.summary.accounts.failed += 1
        }
      }
    }
  }
}
