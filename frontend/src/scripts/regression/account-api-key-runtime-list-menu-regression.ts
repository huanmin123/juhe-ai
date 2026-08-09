import assert from 'node:assert/strict'

import type { AccountListItem } from '@/types/domain'
import { accountMenuItems } from '@/views/accounts/accountRules'

const unavailableOwner = account({
  apiKeyRuntime: {
    total: 2,
    active: 0,
    temporaryUnavailable: 0,
    rateLimited: 0,
    error: 0,
    disabled: 0,
    unavailable: 2,
    allUnavailable: true
  }
})
assert.deepEqual(
  Object.keys(unavailableOwner.apiKeyRuntime ?? {}).sort(),
  ['active', 'allUnavailable', 'disabled', 'error', 'rateLimited', 'temporaryUnavailable', 'total', 'unavailable'],
  '普通 owner 菜单仅依赖公开 Key 池汇总字段'
)
assert.equal(
  accountMenuItems(unavailableOwner).some((item) => item.key === 'revalidate-api-key-runtime'),
  true,
  'owner 列表行收到 unverified 等非 disabled 不可用 Key 汇总时必须展示重新验证入口'
)

const allDisabledOwner = account({
  apiKeyRuntime: {
    total: 2,
    active: 0,
    temporaryUnavailable: 0,
    rateLimited: 0,
    error: 0,
    disabled: 2,
    unavailable: 2,
    allUnavailable: true
  }
})
assert.equal(
  accountMenuItems(allDisabledOwner).some((item) => item.key === 'revalidate-api-key-runtime'),
  false,
  '全部 Key 已 disabled 时不得展示重新验证入口'
)

console.log('account-api-key-runtime-list-menu 前端回归通过')

function account(overrides: Partial<AccountListItem>): AccountListItem {
  return {
    id: 'account-key-runtime-menu',
    providerCode: 'gpt',
    name: 'Key 运行态菜单回归账户',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: {},
    healthCheckModel: 'gpt-5.5',
    healthCheckEndpointMode: 'chat_completions',
    schedulable: true,
    todayUsage: { requestCount: 0, totalTokens: 0, totalCost: 0 },
    accessType: 'owner',
    ...overrides
  }
}
