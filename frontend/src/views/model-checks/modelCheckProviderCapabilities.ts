import { isOpenAIProtocolProfile } from '@/shared/providerProtocol'
import type { AccountOptionSummary } from '@/types/domain'

export type ModelCheckAccountProfile = Pick<
  AccountOptionSummary,
  'id' | 'name' | 'providerCode' | 'protocolCode' | 'protocolVersion'
>

export function canRunModelCheckForAccount(account: ModelCheckAccountProfile | undefined): boolean {
  return isOpenAIProtocolProfile(account)
}

export function canSelectModelCheckAccount(
  account: ModelCheckAccountProfile,
  options: { excludedAccountId?: string } = {}
): boolean {
  if (!canRunModelCheckForAccount(account)) return false
  if (options.excludedAccountId && account.id === options.excludedAccountId) return false
  return Boolean(account.name.trim())
}

