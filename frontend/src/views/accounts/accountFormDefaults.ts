import type { AccountType, ProviderDefinition } from '@/types/domain'
import { defaultProviderProtocolProfileId, isHybridProviderCode, preferredDefaultProviderCode } from '@/shared/providerProtocol'
import { createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import { defaultAccountEndpointModes } from './accountEndpointModes'
import { defaultAccountHealthCheckEndpointMode } from './accountHealthCheckEndpointMode'
import { accountProviderProtocolKind, defaultAccountClientCompatibilityForProvider } from './accountProviderCapabilities'
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
  const supportedModels: string[] = []
  const supportedEndpointModes = defaultAccountEndpointModes(resolvedProviderCode, resolvedType, undefined, { provider, protocolProfile: profile })
  const defaultOAuthMode = resolvedType === 'oauth' && accountProviderProtocolKind(profile) === 'anthropic_v1'
    ? 'access_token'
    : 'manual'
  return {
    providerCode: resolvedProviderCode,
    providerProtocolProfileId: profile?.id || providerProtocolProfileId || defaultProviderProtocolProfileId(provider),
    name: '',
    type: resolvedType,
    groupId: undefined,
    group: undefined,
    apiKey: '',
    apiKeys: [''],
    apiKeyStrategy: 'failover',
    apiKeyWeights: [1],
    baseUrl: isHybridProviderCode(resolvedProviderCode) ? '' : profile?.baseUrl ?? provider?.baseUrl ?? '',
    accessToken: '',
    refreshToken: '',
    googleClientId: '',
    googleClientSecret: '',
    googleQuotaProjectId: '',
    oauthType: 'code_assist',
    tierId: 'gcp_standard',
    projectId: '',
    oauthMode: defaultOAuthMode,
    ssoTokens: '',
    callbackUrl: '',
    accountExpiresAt: undefined,
    concurrencyLimit: DEFAULT_ACCOUNT_CONCURRENCY_LIMIT,
    priority: 0,
    privilege: 'normal',
    status: 'pending_test',
    statusSelectionExplicit: false,
    clientCompatibility,
    supportedEndpointModes,
    supportedModels,
    healthCheckModel: '',
    healthCheckEndpointMode: defaultAccountHealthCheckEndpointMode(resolvedProviderCode, profile?.id ?? '', supportedEndpointModes),
    temporaryUnavailableContinuousProbeEnabled: true,
    lockDeathTimeoutSeconds: 300,
    lockRetryIntervalSeconds: 5,
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
    balanceQueryDivisor: '',
    errorHandlingRuleOverrides: []
  }
}

export function compactAccountCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(credentials).filter(([, value]) => value !== undefined && value !== ''))
}
