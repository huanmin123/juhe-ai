import assert from 'node:assert/strict'

import type { AccountUsageSummary, ApiKeySummary, CreatedApiKey } from '../../src/types/domain'
import { refreshedApiKeyListItem } from '../../src/views/api-keys/apiKeyRefreshRow'

const currentUsage = usageSummary(17)
const refreshedUsage = usageSummary(3)
const current = apiKeySummary(currentUsage)

const unavailable = refreshedApiKeyListItem(current, refreshedApiKey(false, refreshedUsage))
assert.deepEqual(unavailable.usage, currentUsage, 'usageAvailable=false 时必须保留当前行 usage')
assert.equal(unavailable.name, '已刷新 Key', 'usage enrichment 失败时仍应采用已提交的核心字段')
assert.equal('key' in unavailable, false, '列表行不得保留完整 key')

const available = refreshedApiKeyListItem(current, refreshedApiKey(true, refreshedUsage))
assert.deepEqual(available.usage, refreshedUsage, 'usageAvailable=true 时必须采用响应 usage')
assert.equal('key' in available, false, '成功 enrichment 后列表行仍不得保留完整 key')

const legacy = refreshedApiKeyListItem(current, refreshedApiKey(undefined, refreshedUsage))
assert.deepEqual(legacy.usage, refreshedUsage, '缺省 usageAvailable 时必须保持采用响应 usage 的兼容语义')

console.log('API Key refresh row regression passed')

function apiKeySummary(usage: AccountUsageSummary): ApiKeySummary {
  return {
    id: 'key_1',
    name: '当前 Key',
    keyPrefix: 'sk-old',
    keySuffix: 'old-key',
    status: 'active',
    routeStrategyId: 'route_1',
    quotaLimits: {},
    usage
  }
}

function refreshedApiKey(
  usageAvailable: boolean | undefined,
  usage: AccountUsageSummary
): CreatedApiKey {
  return {
    ...apiKeySummary(usage),
    name: '已刷新 Key',
    keyPrefix: 'sk-new',
    keySuffix: 'new-key',
    key: 'sk-refreshed-secret-0123456789',
    usageAvailable
  }
}

function usageSummary(requestCount: number): AccountUsageSummary {
  return {
    requestCount,
    inputTokens: requestCount,
    outputTokens: requestCount,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: requestCount * 2,
    totalCost: requestCount
  }
}
