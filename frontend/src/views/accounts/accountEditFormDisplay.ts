import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountType, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'

import {
  accountTypeDescription,
  accountTypeText,
  accountTypeTitle as buildAccountTypeTitle
} from './accountFormatters'

type ProviderDisplaySource = Pick<ProviderDefinition, 'code' | 'name'>

export interface AccountTypeChoice {
  value: AccountType
  label: string
  description: string
  tag: string
}

interface AccountEditModalTitleOptions {
  cloningSourceId?: string
  editingAuthorizedAccount: boolean
  editingId?: string
  editingSystemAccountLabel?: string
  providerCode?: string
  providerProtocolProfileId?: string
  providers: ProviderDisplaySource[]
  targetSystemAccountLabel?: string
  type: AccountType
}

export function accountTypeSortWeight(type: AccountType): number {
  if (type === 'api_key') return 0
  if (type === 'oauth') return 1
  return 2
}

export function accountEditProviderName(providerCode: string | undefined, providers: ProviderDisplaySource[]): string {
  return providerDisplayName(providerCode, providers)
}

export function accountEditAccountTypeTitle(providerCode: string, type: AccountType, providers: ProviderDisplaySource[]): string {
  return buildAccountTypeTitle(accountEditProviderName(providerCode, providers), type)
}

export function accountEditCreateModalTitle(baseTitle: string, targetLabel?: string): string {
  return targetLabel ? `${baseTitle}（${targetLabel}）` : baseTitle
}

export function accountEditEditingModalTitle(baseTitle: string, accountLabel?: string): string {
  return accountLabel ? `${baseTitle}（系统账户：${accountLabel}）` : baseTitle
}

export function accountTypeChoicesForProfile(
  profile: ProviderProtocolProfileDefinition | undefined,
  providerCode: string,
  providers: ProviderDisplaySource[]
): AccountTypeChoice[] {
  return [...(profile?.accountTypes ?? [])]
    .map((type) => ({
      value: type,
      label: accountEditAccountTypeTitle(providerCode, type, providers),
      description: accountTypeDescription(providerCode, type),
      tag: accountTypeText(type)
    }))
    .sort((left, right) => accountTypeSortWeight(left.value) - accountTypeSortWeight(right.value))
}

export function accountEditModalTitle(options: AccountEditModalTitleOptions): string {
  if (options.editingAuthorizedAccount) {
    return accountEditEditingModalTitle('编辑授权账户', options.editingSystemAccountLabel)
  }
  if (options.editingId) {
    return accountEditEditingModalTitle('编辑账户', options.editingSystemAccountLabel)
  }
  if (options.cloningSourceId) return '克隆账户'
  if (!options.providerCode) {
    return accountEditCreateModalTitle('添加账户', options.targetSystemAccountLabel)
  }
  if (!options.providerProtocolProfileId) {
    return accountEditCreateModalTitle(`添加 ${accountEditProviderName(options.providerCode, options.providers)} 账户`, options.targetSystemAccountLabel)
  }
  if (!options.type) {
    return accountEditCreateModalTitle(`添加 ${accountEditProviderName(options.providerCode, options.providers)} 账户`, options.targetSystemAccountLabel)
  }
  return accountEditCreateModalTitle(
    `添加 ${accountEditAccountTypeTitle(options.providerCode, options.type, options.providers)} 账户`,
    options.targetSystemAccountLabel
  )
}
