import type { ApiKeySummary, CreatedApiKey } from '@/types/domain'

export function refreshedApiKeyListItem(
  current: ApiKeySummary,
  refreshed: CreatedApiKey
): ApiKeySummary {
  const { key: _key, usageAvailable, ...summary } = refreshed
  return {
    ...summary,
    usage: usageAvailable === false ? current.usage : summary.usage
  }
}
