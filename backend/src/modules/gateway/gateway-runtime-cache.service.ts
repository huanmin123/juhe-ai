import { createAppCache } from '../../shared/cache.js'
import {
  listOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { readGatewaySettings, type GatewaySettings } from './account-error-policy.service.js'

const gatewaySettingsTtlMs = 1000
const groupUsageAccessTtlMs = 1000
const openAIAccountsTtlMs = 1000

const gatewaySettingsCache = createAppCache<string, GatewaySettings>({
  name: 'gateway:settings',
  max: 1,
  ttlMs: gatewaySettingsTtlMs
})

const groupUsageAccessCache = createAppCache<string, GroupUsageAccessMetadata | false>({
  name: 'gateway:group-usage-access',
  max: 1000,
  ttlMs: groupUsageAccessTtlMs
})

const openAIAccountsCache = createAppCache<string, OpenAIAccountSecret[]>({
  name: 'gateway:openai-accounts',
  max: 1000,
  ttlMs: openAIAccountsTtlMs
})

export function readCachedGatewaySettings(): GatewaySettings {
  const cached = gatewaySettingsCache.get('current')
  if (cached) {
    return cached
  }
  const value = readGatewaySettings()
  gatewaySettingsCache.set('current', value)
  return value
}

export function resolveCachedGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = groupUsageAccessCache.get(cacheKey)
  if (cached !== undefined) {
    return cached || undefined
  }
  const value = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  groupUsageAccessCache.set(cacheKey, value ?? false)
  return value
}

export function listCachedOpenAIAccountsForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret[] {
  const cacheKey = gatewayCacheKey(groupId, systemAccountId)
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached) {
    return cached.map(cloneOpenAIAccountSecret)
  }
  const value = listOpenAIAccountsForGroup(groupId, systemAccountId)
  openAIAccountsCache.set(cacheKey, value)
  return value.map(cloneOpenAIAccountSecret)
}

export function clearGatewayRuntimeCache(): void {
  gatewaySettingsCache.clear()
  groupUsageAccessCache.clear()
  openAIAccountsCache.clear()
}

function gatewayCacheKey(groupId: string, systemAccountId: string): string {
  return `${groupId}:${systemAccountId}`
}

function cloneOpenAIAccountSecret(account: OpenAIAccountSecret): OpenAIAccountSecret {
  return {
    ...account,
    credentials: { ...account.credentials }
  }
}
