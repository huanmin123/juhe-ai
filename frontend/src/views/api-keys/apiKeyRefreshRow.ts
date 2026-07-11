import type { ApiKeySummary, CreatedApiKey } from '@/types/domain'

export function refreshedApiKeyListItem(
  current: ApiKeySummary,
  refreshed: CreatedApiKey
): ApiKeySummary {
  const item: ApiKeySummary & { usageAvailable?: boolean } = {
    ...refreshed,
    usage: refreshed.usageAvailable === false ? current.usage : refreshed.usage
  }
  delete item.key
  delete item.usageAvailable
  return item
}
