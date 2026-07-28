import type { ApiKeySummary, CreatedApiKey } from '@/types/domain'

export function refreshedApiKeyListItem(
  current: ApiKeySummary,
  refreshed: CreatedApiKey
): ApiKeySummary {
  return {
    ...current,
    keyPrefix: refreshed.keyPrefix,
    keySuffix: refreshed.keySuffix,
    revision: refreshed.revision
  }
}
