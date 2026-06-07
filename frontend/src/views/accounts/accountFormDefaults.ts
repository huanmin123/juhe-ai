import type { AccountType, ProviderDefinition } from '@/types/domain'
import { createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import type { AccountFormModel } from './accountFormTypes'
import { DEFAULT_ACCOUNT_CONCURRENCY_LIMIT, OPENAI_PROVIDER } from './accountOptions'

export function defaultAccountForm(
  providerCode = '',
  type: AccountType = '',
  providers: ProviderDefinition[] = []
): AccountFormModel {
  const providerList = providers.length ? providers : [OPENAI_PROVIDER]
  const provider = providerList.find((item) => item.code === providerCode)
  return {
    providerCode,
    name: '',
    type,
    groupId: undefined,
    group: undefined,
    apiKey: '',
    baseUrl: provider?.baseUrl ?? '',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    clientCompatibility: providerCode === 'openai' && type === 'oauth' ? 'codex_responses' : 'openai_standard',
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
