import type { AccountType, ProviderDefinition } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import { DEFAULT_ACCOUNT_CONCURRENCY_LIMIT, FALLBACK_PROVIDER } from './accountOptions'

export function defaultAccountForm(
  providerCode = '',
  type: AccountType = '',
  providers: ProviderDefinition[] = []
): AccountFormModel {
  const providerList = providers.length ? providers : [FALLBACK_PROVIDER]
  const provider = providerList.find((item) => item.code === providerCode) ?? (providerCode ? FALLBACK_PROVIDER : undefined)
  return {
    providerCode,
    name: '',
    type,
    groupId: undefined,
    apiKey: '',
    baseUrl: provider?.baseUrl ?? 'https://api.openai.com/v1',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    supportedModels: [],
    proxyProfileId: undefined,
    notes: ''
  }
}

export function compactAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
}
