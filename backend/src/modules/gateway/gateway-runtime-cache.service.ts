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

interface CacheEntry<T> {
  value: T
  expiresAtMs: number
}

let gatewaySettingsCache: CacheEntry<GatewaySettings> | undefined
const groupUsageAccessCache = new Map<string, CacheEntry<GroupUsageAccessMetadata | undefined>>()
const openAIAccountsCache = new Map<string, CacheEntry<OpenAIAccountSecret[]>>()

export function readCachedGatewaySettings(): GatewaySettings {
  const now = Date.now()
  if (gatewaySettingsCache && gatewaySettingsCache.expiresAtMs > now) {
    return gatewaySettingsCache.value
  }
  const value = readGatewaySettings()
  gatewaySettingsCache = { value, expiresAtMs: now + gatewaySettingsTtlMs }
  return value
}

export function resolveCachedGroupUsageAccessMetadata(groupId: string, systemAccountId: string): GroupUsageAccessMetadata | undefined {
  const cacheKey = `${groupId}:${systemAccountId}`
  const now = Date.now()
  const cached = groupUsageAccessCache.get(cacheKey)
  if (cached && cached.expiresAtMs > now) {
    return cached.value
  }
  const value = resolveGroupUsageAccessMetadata(groupId, systemAccountId)
  groupUsageAccessCache.set(cacheKey, { value, expiresAtMs: now + groupUsageAccessTtlMs })
  return value
}

export function listCachedOpenAIAccountsForGroup(groupId: string, systemAccountId: string): OpenAIAccountSecret[] {
  const cacheKey = `${groupId}:${systemAccountId}`
  const now = Date.now()
  const cached = openAIAccountsCache.get(cacheKey)
  if (cached && cached.expiresAtMs > now) {
    return cached.value.map(cloneOpenAIAccountSecret)
  }
  const value = listOpenAIAccountsForGroup(groupId, systemAccountId)
  openAIAccountsCache.set(cacheKey, { value, expiresAtMs: now + openAIAccountsTtlMs })
  return value.map(cloneOpenAIAccountSecret)
}

export function clearGatewayRuntimeCache(): void {
  gatewaySettingsCache = undefined
  groupUsageAccessCache.clear()
  openAIAccountsCache.clear()
}

function cloneOpenAIAccountSecret(account: OpenAIAccountSecret): OpenAIAccountSecret {
  return {
    ...account,
    credentials: { ...account.credentials }
  }
}
