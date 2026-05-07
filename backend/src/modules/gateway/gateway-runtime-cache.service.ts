import { createAppCache } from '../../shared/cache.js'
import {
  listOpenAIAccountsForGroup,
  resolveGroupUsageAccessMetadata,
  type GroupUsageAccessMetadata,
  type OpenAIAccountSecret
} from '../../storage/repositories.js'
import { clearDbServiceGatewayRuntimeCache, requestDbService } from '../db-service/db-service-ipc.js'
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

export async function readCachedGatewaySettingsAsync(): Promise<GatewaySettings> {
  return await requestDbService({ type: 'read_gateway_settings' })
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

export async function resolveCachedGroupUsageAccessMetadataAsync(groupId: string, systemAccountId: string): Promise<GroupUsageAccessMetadata | undefined> {
  return await requestDbService({
    type: 'resolve_group_usage_access',
    groupId,
    systemAccountId
  })
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

export async function listCachedOpenAIAccountsForGroupAsync(groupId: string, systemAccountId: string): Promise<OpenAIAccountSecret[]> {
  const accounts = await requestDbService({
    type: 'list_openai_accounts_for_group',
    groupId,
    systemAccountId
  })
  return accounts.map(cloneOpenAIAccountSecret)
}

export function clearGatewayRuntimeCache(): void {
  clearGatewayRuntimeCacheLocal()
  clearDbServiceGatewayRuntimeCache()
}

export function clearGatewayRuntimeCacheLocal(): void {
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
