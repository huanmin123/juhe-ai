import type { AccountType, ProviderDefinition } from '@/types/domain'
import { defaultProviderProtocolProfileId, isHybridProviderCode, preferredDefaultProviderCode } from '@/shared/providerProtocol'
import { createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import { defaultAccountEndpointModes } from './accountEndpointModes'
import { defaultAccountHealthCheckEndpointMode } from './accountHealthCheckEndpointMode'
import { defaultAccountClientCompatibilityForProvider } from './accountProviderCapabilities'
import type { AccountFormModel } from './accountFormTypes'
import { DEFAULT_ACCOUNT_CONCURRENCY_LIMIT, FALLBACK_PROVIDERS } from './accountOptions'

export function defaultAccountClientCompatibility(
  providerCode: string,
  providers: ProviderDefinition[] = [],
  providerProtocolProfileId = ''
) {
  const providerList = providers.length ? providers : FALLBACK_PROVIDERS
  const provider = providerList.find((item) => item.code === providerCode)
  const profile = provider?.protocolProfiles.find((item) => item.id === (providerProtocolProfileId || provider.defaultProtocolProfileId))
    ?? provider?.protocolProfiles.find((item) => item.enabled)
    ?? provider?.protocolProfiles[0]
  return defaultAccountClientCompatibilityForProvider({ provider, profile })
}

export function defaultAccountForm(
  providerCode = '',
  type: AccountType = '',
  providers: ProviderDefinition[] = [],
  providerProtocolProfileId = ''
): AccountFormModel {
  const providerList = providers.length ? providers : FALLBACK_PROVIDERS
  const resolvedProviderCode = providerCode || preferredDefaultProviderCode(providerList)
  const provider = providerList.find((item) => item.code === resolvedProviderCode)
  const profile = provider?.protocolProfiles.find((item) => item.id === (providerProtocolProfileId || provider.defaultProtocolProfileId))
    ?? provider?.protocolProfiles.find((item) => item.enabled)
    ?? provider?.protocolProfiles[0]
  const accountTypes = profile?.accountTypes ?? provider?.accountTypes ?? []
  const resolvedType = type || (accountTypes.includes('api_key') ? 'api_key' : accountTypes[0] ?? '')
  const clientCompatibility = defaultAccountClientCompatibility(resolvedProviderCode, providerList, profile?.id)
  const supportedModels = defaultSupportedModelsForProvider(provider)
  const supportedEndpointModes = defaultAccountEndpointModes(resolvedProviderCode, resolvedType, undefined, { provider, protocolProfile: profile })
  return {
    providerCode: resolvedProviderCode,
    providerProtocolProfileId: profile?.id || providerProtocolProfileId || defaultProviderProtocolProfileId(provider),
    name: '',
    type: resolvedType,
    groupId: undefined,
    group: undefined,
    apiKey: '',
    apiKeys: [''],
    apiKeyStrategy: 'round_robin',
    apiKeyWeights: [1],
    baseUrl: isHybridProviderCode(resolvedProviderCode) ? '' : profile?.baseUrl ?? provider?.baseUrl ?? '',
    accessToken: '',
    refreshToken: '',
    oauthMode: 'manual',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    clientCompatibility,
    supportedEndpointModes,
    supportedModels,
    healthCheckModel: defaultHealthCheckModelForProvider(provider, profile, supportedModels),
    healthCheckEndpointMode: defaultAccountHealthCheckEndpointMode(resolvedProviderCode, profile?.id ?? '', supportedEndpointModes),
    serviceTierOverride: '',
    reasoningEffortOverride: '',
    modelMappings: [],
    tags: [],
    proxyProfileId: undefined,
    availabilitySchedule: createAccountAvailabilityScheduleForm(),
    notes: '',
    balanceQueryEnabled: false,
    balanceQueryAdapter: 'builtin',
    balanceQueryPreferredBuiltinAdapter: undefined,
    balanceQueryIntervalMinutes: 5,
    balanceQueryCustomPath: '',
    balanceQueryRemainingPointer: '',
    balanceQueryTotalPointer: '',
    balanceQueryUsedPointer: '',
    balanceQueryDivisor: ''
  }
}

function defaultSupportedModelsForProvider(provider: ProviderDefinition | undefined): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of [provider?.defaultHealthCheckModel, ...(provider?.defaultSupportedModels ?? [])]) {
    const model = item?.trim() ?? ''
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push(model)
  }
  return output
}

function defaultHealthCheckModelForProvider(
  provider: ProviderDefinition | undefined,
  profile: ProviderDefinition['protocolProfiles'][number] | undefined,
  supportedModels: string[]
): string {
  for (const value of [provider?.defaultHealthCheckModel, profile?.defaultHealthCheckModel, ...supportedModels]) {
    const model = value?.trim() ?? ''
    if (model && supportedModels.includes(model)) return model
  }
  return ''
}

export function compactAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
}
