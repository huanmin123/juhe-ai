import { type AccountType, type ProviderDefinition } from '../../domain/types.js'
import { optionalServerDateTimeIso } from '../../storage/value-utils.js'
import { type AccountImportStatus } from './account-import-field-parser.js'

export interface AccountImportProviderContext {
  providerByCode: Map<string, ProviderDefinition>
}

export interface AccountImportProviderAccount {
  name: string
  providerCode: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  type: AccountType
  status: AccountImportStatus
  concurrencyLimit?: number
  accountExpiresAt?: string
  messages: string[]
}

export function validateImportAccountProviderAndBasics(account: AccountImportProviderAccount, context: AccountImportProviderContext): void {
  if (!account.name) account.messages.push('账户名称不能为空')
  const provider = context.providerByCode.get(account.providerCode)
  if (!provider) {
    account.messages.push(`不支持的供应商：${account.providerCode}`)
  } else if (!provider.enabled) {
    account.messages.push(`供应商已停用：${account.providerCode}`)
  } else {
    const profile = resolveImportAccountProtocolProfile(account, provider)
    if (profile && !profile.accountTypes.includes(account.type)) {
      account.messages.push(`供应商协议档案 ${profile.name} 不支持账户类型 ${account.type}`)
    }
  }
  if (account.status !== 'active' && account.status !== 'pending_test' && account.status !== 'disabled') {
    account.messages.push('账户状态仅支持 active、pending_test 或 disabled')
  }
  if (account.concurrencyLimit !== undefined && account.concurrencyLimit < 1) {
    account.messages.push('concurrencyLimit 必须大于 0')
  }
  if (account.accountExpiresAt && !optionalServerDateTimeIso(account.accountExpiresAt)) {
    account.messages.push('accountExpiresAt 必须是有效时间字符串')
  }
}

function resolveImportAccountProtocolProfile(account: AccountImportProviderAccount, provider: ProviderDefinition): ProviderDefinition['protocolProfiles'][number] | undefined {
  const requestedProfileId = account.providerProtocolProfileId?.trim()
  if (!requestedProfileId) {
    account.messages.push('账户 providerProtocolProfileId 不能为空')
    return undefined
  }
  const profile = provider.protocolProfiles.find((item) => item.id === requestedProfileId)
  if (!profile) {
    account.messages.push(`供应商 ${account.providerCode} 未配置协议档案`)
    return undefined
  }
  if (profile.providerCode !== account.providerCode) {
    account.messages.push(`协议档案 ${profile.id} 不属于供应商 ${account.providerCode}`)
    return undefined
  }
  if (!profile.enabled) {
    account.messages.push(`供应商协议档案已停用：${profile.name}`)
    return undefined
  }
  account.providerProtocolProfileId = profile.id
  account.protocolCode = profile.protocolCode
  account.protocolVersion = profile.protocolVersion
  return profile
}
