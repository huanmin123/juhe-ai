import type { AccountType, ProviderDefinition } from '@/types/domain'
import { createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import type { AccountFormModel } from './accountFormTypes'
import { DEFAULT_ACCOUNT_CONCURRENCY_LIMIT, GPT_VENDOR_CODE, OPENAI_PROVIDER } from './accountOptions'

export function defaultAccountForm(
  providerCode = '',
  type: AccountType = '',
  providers: ProviderDefinition[] = [],
  providerProtocolProfileId = ''
): AccountFormModel {
  const providerList = providers.length ? providers : [OPENAI_PROVIDER]
  const provider = providerList.find((item) => item.code === providerCode)
  const profile = provider?.protocolProfiles.find((item) => item.id === (providerProtocolProfileId || provider.defaultProtocolProfileId))
    ?? provider?.protocolProfiles.find((item) => item.enabled)
    ?? provider?.protocolProfiles[0]
  return {
    providerCode,
    providerProtocolProfileId: profile?.id ?? providerProtocolProfileId,
    name: '',
    type,
    groupId: undefined,
    group: undefined,
    apiKey: '',
    baseUrl: profile?.baseUrl ?? provider?.baseUrl ?? '',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    clientCompatibility: providerCode === GPT_VENDOR_CODE && type === 'oauth' ? 'codex_responses' : 'openai_standard',
    supportedModels: [],
    modelMappings: [],
    proxyProfileId: undefined,
    availabilitySchedule: createAccountAvailabilityScheduleForm(),
    notes: ''
  }
}

export function compactAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
}
