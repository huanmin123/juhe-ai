import type { AccountSummary, GroupOptionSummary, ProviderDefinition, ProxyProfileOptionSummary, SystemAccountPrincipalSummary } from '@/types/domain'
import { groupLabelForId } from '@/shared/groupLabelCache'
import { principalLabelForId, type PrincipalSelection } from '@/shared/principalLabelCache'
import { proxySelectOptionLabel } from '@/shared/proxyLabelCache'
import { canManageGroupAccounts, canUseAsTrafficMigrationTarget, type AccountGroupIdResolver } from './accountRules'
import { isGatewayTestableAccountProfile } from './accountProviderCapabilities'

export type SelectOption = {
  label: string
  value: string
  disabled?: boolean
}

export function buildTestModelOptions(
  account?: AccountSummary | AccountSummary[],
  providerDefaultModel = '',
  systemDefaultModel = ''
): SelectOption[] {
  const supportedModels = supportedTestModelsForAccountSelection(account)
  const preferredModels = preferredTestModelsForAccountSelection(account, providerDefaultModel, systemDefaultModel)
  return selectOptions(prioritizeTestModels(supportedModels, preferredModels))
}

export function defaultTestModelForAccountSelection(
  account: AccountSummary | AccountSummary[] | undefined,
  providerDefaultModel = '',
  systemDefaultModel = ''
): string {
  const supportedModels = supportedTestModelsForAccountSelection(account)
  const preferredModels = preferredTestModelsForAccountSelection(account, providerDefaultModel, systemDefaultModel)
  return prioritizeTestModels(supportedModels, preferredModels)[0] ?? ''
}

export function providerDefaultTestModelForAccountSelection(providers: ProviderDefinition[], account: AccountSummary | AccountSummary[] | undefined): string {
  const providerCode = providerCodeForAccountSelection(account)
  if (!providerCode) return ''
  return providers.find((provider) => provider.code === providerCode)?.defaultTestModel?.trim() ?? ''
}

export function providerSystemDefaultTestModelForAccountSelection(providers: ProviderDefinition[], account: AccountSummary | AccountSummary[] | undefined): string {
  const providerCode = providerCodeForAccountSelection(account)
  if (!providerCode) return ''
  const provider = providers.find((item) => item.code === providerCode)
  if (!provider) return ''
  const profileIds = [...new Set(normalizeAccounts(account).map((item) => item.providerProtocolProfileId).filter(Boolean))]
  if (profileIds.length === 1) {
    const profile = provider.protocolProfiles.find((item) => item.id === profileIds[0])
    if (profile?.defaultTestModel?.trim()) return profile.defaultTestModel.trim()
  }
  return provider.systemDefaultTestModel?.trim()
    || provider.protocolProfiles
      .find((item) => item.id === provider.defaultProtocolProfileId)
      ?.defaultTestModel
      ?.trim()
    || ''
}

export function providerCodeForAccountSelection(account: AccountSummary | AccountSummary[] | undefined): string {
  const codes = [...new Set(normalizeAccounts(account).map((item) => item.providerCode).filter(Boolean))]
  return codes.length === 1 ? codes[0] : ''
}

export function isGatewaySupportedTestSelection(account: AccountSummary | AccountSummary[] | undefined): boolean {
  const accounts = normalizeAccounts(account)
  return accounts.length > 0
    && accounts.every((item) => isGatewayTestableAccountProfile(item))
    && hasSingleProviderProfileForAccountSelection(accounts)
}

export function hasSingleProviderProfileForAccountSelection(account: AccountSummary | AccountSummary[] | undefined): boolean {
  const profileKeys = [...new Set(normalizeAccounts(account).map(accountProviderProfileKey))]
  return profileKeys.length === 1
}

function normalizeAccountSupportedModels(account: AccountSummary | AccountSummary[] | undefined): string[] {
  return uniqueTextList(normalizeAccounts(account).flatMap((item) => item.supportedModels ?? []))
}

function supportedTestModelsForAccountSelection(account: AccountSummary | AccountSummary[] | undefined): string[] {
  const accounts = normalizeAccounts(account)
  const supportedModelLists = accounts
    .map((item) => normalizeAccountSupportedModels(item))
    .filter((models) => models.length > 0)
  if (!supportedModelLists.length) return []
  const [firstModels, ...otherModelLists] = supportedModelLists
  return firstModels.filter((model) => otherModelLists.every((models) => models.includes(model)))
}

function preferredTestModelsForAccountSelection(
  account: AccountSummary | AccountSummary[] | undefined,
  providerDefaultModel: string,
  systemDefaultModel: string
): string[] {
  return uniqueTextList([
    accountDefaultTestModelForSelection(account),
    providerDefaultModel,
    systemDefaultModel
  ])
}

function accountDefaultTestModelForSelection(account: AccountSummary | AccountSummary[] | undefined): string {
  const accounts = normalizeAccounts(account)
  if (!accounts.length) return ''
  const models = accounts.map((item) => item.defaultTestModel?.trim() ?? '')
  if (models.some((model) => !model)) return ''
  const uniqueModels = [...new Set(models)]
  return uniqueModels.length === 1 ? uniqueModels[0] : ''
}

function prioritizeTestModels(models: string[], preferredModels: string[]): string[] {
  const availablePreferredModels = preferredModels.filter((model) => models.includes(model))
  return [...availablePreferredModels, ...models.filter((model) => !availablePreferredModels.includes(model))]
}

function selectOptions(models: string[]): SelectOption[] {
  return uniqueTextList(models).map((model) => ({ label: model, value: model }))
}

function uniqueTextList(values: string[]): string[] {
  return [...new Set(values.map((model) => model.trim()).filter(Boolean))]
}

function normalizeAccounts(account: AccountSummary | AccountSummary[] | undefined): AccountSummary[] {
  return Array.isArray(account) ? account : account ? [account] : []
}

function accountProviderProfileKey(account: AccountSummary): string {
  return [
    account.providerCode,
    account.providerProtocolProfileId ?? '',
    account.protocolCode,
    account.protocolVersion
  ].join('\u0001')
}

export function targetSystemAccountLabel(systemAccounts: SystemAccountPrincipalSummary[], systemAccountId?: string, selected?: PrincipalSelection): string {
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (selected?.kind === 'system_account' && selected.id === systemAccountId && selected.name) return selected.name
  const account = systemAccounts.find((item) => item.id === systemAccountId)
  return account?.displayName || principalLabelForId('system_account', systemAccountId) || ''
}

export function buildProxyOptions(proxies: ProxyProfileOptionSummary[]): SelectOption[] {
  return proxies.map((proxy) => ({
    label: proxySelectOptionLabel(proxy),
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
  return canManageGroupAccounts(group)
    && (!providerCode || group.providerCode === providerCode)
}

export function groupOptionsForProvider(groups: GroupOptionSummary[], providerCode?: string): SelectOption[] {
  return manageableGroupsForProvider(groups, providerCode).map((group) => ({ label: group.name, value: group.id }))
}

export function groupOptionsForProviderWithSelected(groups: GroupOptionSummary[], providerCode: string | undefined, selectedIds: Array<string | undefined>): SelectOption[] {
  const options = groupOptionsForProvider(groups, providerCode)
  const merged = new Map(options.map((option) => [option.value, option]))
  for (const id of selectedIds) {
    const normalizedId = id?.trim()
    if (!normalizedId || merged.has(normalizedId)) continue
    const label = groupLabelForId(normalizedId)
    if (label) {
      merged.set(normalizedId, { label, value: normalizedId })
    }
  }
  return [...merged.values()]
}

export function defaultGroupForProvider(groups: GroupOptionSummary[], providerCode: string): GroupOptionSummary | undefined {
  const candidates = manageableGroupsForProvider(groups, providerCode)
  return candidates.find((group) => group.isDefault)
}

export function bindGroupOptionsForAccount(groups: GroupOptionSummary[], account?: AccountSummary): SelectOption[] {
  if (!account) return []
  return groupOptionsForProviderWithSelected(groups, account.providerCode, [account.boundGroupId])
}

export function bindGroupTip(account?: AccountSummary): string {
  const ownerName = account?.ownerSystemAccountName || '其他用户'
  return `授权账户来自 ${ownerName}。绑定到你的兼容分组后，对应 API Key 才能调度使用。`
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
