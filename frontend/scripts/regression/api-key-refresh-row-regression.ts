import assert from 'node:assert/strict'

import type { ApiKeySummary, ApiKeyUsageListSummary, CreatedApiKey } from '../../src/types/domain'
import { refreshedApiKeyListItem } from '../../src/views/api-keys/apiKeyRefreshRow'

const currentUsage: ApiKeyUsageListSummary = {
  requestCount: 17,
  totalTokens: 34,
  totalCost: 17
}
const current: ApiKeySummary = {
  id: 'key_1',
  revision: 'revision-1',
  name: '当前 Key',
  keyPrefix: 'sk-old',
  keySuffix: 'old-key',
  status: 'active',
  purpose: 'general',
  routeStrategyId: 'route_1',
  quotaLimits: {},
  usage: currentUsage
}
const refreshed: CreatedApiKey = {
  id: current.id,
  revision: 'revision-2',
  keyPrefix: 'sk-new',
  keySuffix: 'new-key',
  key: 'sk-refreshed-secret-0123456789'
}

const next = refreshedApiKeyListItem(current, refreshed)
assert.deepEqual(next.usage, currentUsage, '最小刷新响应不得覆盖当前行 usage')
assert.equal(next.name, current.name, '最小刷新响应不得覆盖未返回的列表字段')
assert.equal(next.keyPrefix, refreshed.keyPrefix, '刷新后必须更新密钥前缀')
assert.equal(next.keySuffix, refreshed.keySuffix, '刷新后必须更新密钥后缀')
assert.equal(next.revision, refreshed.revision, '刷新后必须推进列表 revision')
assert.equal('key' in next, false, '列表行不得保留完整 key')

console.log('API Key refresh row regression passed')
