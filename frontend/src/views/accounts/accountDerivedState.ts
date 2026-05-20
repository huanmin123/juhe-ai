import type { AccountSummary, GroupOptionSummary, ProviderModelPricing, ProxyProfileOptionSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import { canManageGroupAccounts, canUseAsTrafficMigrationTarget, type AccountGroupIdResolver } from './accountRules'
import { defaultTestModelOptions } from './accountOptions'

export type SelectOption = {
  label: string
  value: string
  disabled?: boolean
}

export function buildTestModelOptions(providerModels: ProviderModelPricing[]): SelectOption[] {
  const models = providerModels.length ? providerModels.map((item) => item.model) : defaultTestModelOptions
  return [...new Set(models)].map((model) => ({ label: model, value: model }))
}

export function targetSystemAccountLabel(systemAccounts: SystemAccountPrincipalSummary[], systemAccountId?: string): string {
  if (!systemAccountId) return '请选择系统账户后再创建'
  const account = systemAccounts.find((item) => item.id === systemAccountId)
  return account?.displayName || account?.username || systemAccountId
}

export function buildProxyOptions(proxies: ProxyProfileOptionSummary[]): SelectOption[] {
  return proxies.map((proxy) => ({
    label: `${proxy.name}（${proxy.type}${proxy.enabled === false ? '，已停用' : ''}）`,
    value: proxy.id,
    disabled: proxy.enabled === false
  }))
}

export function providerNameByCodeMap(providers: Array<{ code: string; name: string }>): Map<string, string> {
  return new Map(providers.map((provider) => [provider.code, provider.name]))
}

export function proxyByIdMap(proxies: ProxyProfileOptionSummary[]): Map<string, ProxyProfileOptionSummary> {
  return new Map(proxies.map((proxy) => [proxy.id, proxy]))
}

export function groupByIdMap(groups: GroupOptionSummary[]): Map<string, GroupOptionSummary> {
  return new Map(groups.map((group) => [group.id, group]))
}

export function accountByIdMap(accounts: AccountSummary[]): Map<string, AccountSummary> {
  return new Map(accounts.map((account) => [account.id, account]))
}

export function groupNameByAccountIdMap(accounts: AccountSummary[], groups: GroupOptionSummary[]): Map<string, string> {
  const map = new Map<string, string>()
  const groupsById = groupByIdMap(groups)
  for (const account of accounts) {
    if (account.boundGroupName) {
      map.set(account.id, account.boundGroupName)
      continue
    }
    if (account.boundGroupId) {
      const boundGroupName = groupsById.get(account.boundGroupId)?.name
      if (boundGroupName) {
        map.set(account.id, boundGroupName)
      }
    }
  }
  return map
}

export function manageableGroupsForProvider(groups: GroupOptionSummary[], providerCode?: string): GroupOptionSummary[] {
  return groups.filter((group) => isManageableGroupForProvider(group, providerCode))
}

export function isManageableGroupForProvider(group: GroupOptionSummary, providerCode?: string): boolean {
  return canManageGroupAccounts(group) && (!providerCode || group.providerCode === providerCode)
}

export function groupOptionsForProvider(groups: GroupOptionSummary[], providerCode?: string): SelectOption[] {
  return manageableGroupsForProvider(groups, providerCode).map((group) => ({ label: group.name, value: group.id }))
}

export function defaultGroupForProvider(groups: GroupOptionSummary[], providerCode: string): GroupOptionSummary | undefined {
  const candidates = manageableGroupsForProvider(groups, providerCode)
  return candidates.find((group) => group.isDefault) ?? candidates[0]
}

export function bindGroupOptionsForAccount(groups: GroupOptionSummary[], account?: AccountSummary): SelectOption[] {
  if (!account) return []
  return groupOptionsForProvider(groups, account.providerCode)
}

export function bindGroupTip(account?: AccountSummary): string {
  const ownerName = account?.ownerSystemAccountName || '其他用户'
  return `授权账户来自 ${ownerName}。绑定到你的同供应商分组后，对应 API Key 才能调度使用。`
}

export function trafficMigrationTargetOptions(accounts: AccountSummary[], source: AccountSummary | undefined, groupIdForAccount: AccountGroupIdResolver, groupNameForAccount: AccountGroupIdResolver): SelectOption[] {
  if (!source) return []
  return accounts
    .filter((account) => canUseAsTrafficMigrationTarget(source, account, groupIdForAccount))
    .map((account) => {
      const groupName = groupNameForAccount(account.id)
      return {
        label: groupName ? `${account.name}（${groupName}）` : account.name,
        value: account.id
      }
    })
}
