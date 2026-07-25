import assert from 'node:assert/strict'

import {
  parseCustomBalance,
  parseLiteLlmBalance,
  parseNewApiBalance,
  parseOpenAiCompatibleBillingBalance,
  parseOpenAiCompatibleBillingStatus,
  parseSub2ApiBalance,
  parseUserBalance
} from '../../modules/accounts/account-balance-adapters.js'

assert.deepEqual(parseSub2ApiBalance({ mode: 'quota_limited', remaining: 7.31, unit: 'USD' }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'api_key_quota'
})
assert.deepEqual(parseSub2ApiBalance({ mode: 'quota_limited', remaining: 0, unit: 'USD' }), {
  status: 'fresh', remainingUsd: '0.000000', rawRemaining: '0', rawUnit: 'usd', basis: 'api_key_quota'
}, 'Sub2API 配额耗尽仍是可解析的零余额')
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', remaining: -1, unit: 'USD' }), {
  status: 'unlimited', basis: 'subscription'
})
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', planName: '钱包余额', remaining: '12.5', unit: 'USD' }), {
  status: 'fresh', remainingUsd: '12.500000', rawRemaining: '12.5', rawUnit: 'usd', basis: 'wallet'
})
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', planName: '钱包余额', remaining: '-0.25003741', balance: '-0.25003741', unit: 'USD' }), {
  status: 'fresh', remainingUsd: '-0.250037', rawRemaining: '-0.25003741', rawUnit: 'usd', basis: 'wallet'
}, 'Sub2API 风格钱包透支余额必须保留实际负值')
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', balance: '3.5', unit: 'USD' }), {
  status: 'fresh', remainingUsd: '3.500000', rawRemaining: '3.5', rawUnit: 'usd', basis: 'wallet'
}, '兼容仅返回 balance 的钱包响应')
assert.throws(() => parseSub2ApiBalance({ remaining: 7.31, unit: 'CNY' }), /USD/)

assert.deepEqual(parseNewApiBalance({ data: { total_available: 3655000 } }, { quotaPerUnit: 500000 }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '3655000', rawUnit: 'quota', basis: 'api_key_quota'
})
assert.deepEqual(parseNewApiBalance({ data: { total_available: 0 } }, { quotaPerUnit: 500000 }), {
  status: 'fresh', remainingUsd: '0.000000', rawRemaining: '0', rawUnit: 'quota', basis: 'api_key_quota'
}, 'New API 配额耗尽仍是可解析的零余额')
assert.deepEqual(parseNewApiBalance({ data: { total_available: -125000 } }, { quotaPerUnit: 500000 }), {
  status: 'fresh', remainingUsd: '-0.250000', rawRemaining: '-125000', rawUnit: 'quota', basis: 'api_key_quota'
}, 'New API 透支额度必须保留实际负值')
assert.deepEqual(parseNewApiBalance({ data: { unlimited_quota: true } }, { quotaPerUnit: 500000 }), {
  status: 'unsupported', basis: 'api_key_quota'
})
assert.throws(() => parseNewApiBalance({ data: {} }, { quotaPerUnit: 500000 }), /total_available/)

assert.deepEqual(parseOpenAiCompatibleBillingBalance(
  { object: 'billing_subscription', hard_limit_usd: '10' },
  { object: 'list', total_usage: '269' },
  { rawUnit: 'usd' }
), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'api_key_quota'
})
assert.deepEqual(parseOpenAiCompatibleBillingBalance(
  { object: 'billing_subscription', hard_limit_usd: '0' },
  { object: 'list', total_usage: '0' },
  { rawUnit: 'usd' }
), {
  status: 'fresh', remainingUsd: '0.000000', rawRemaining: '0', rawUnit: 'usd', basis: 'api_key_quota'
}, 'OpenAI 兼容账单接口余额耗尽仍是可解析的零余额')
assert.deepEqual(parseOpenAiCompatibleBillingBalance(
  { object: 'billing_subscription', hard_limit_usd: '10' },
  { object: 'list', total_usage: '1025' },
  { rawUnit: 'usd' }
), {
  status: 'fresh', remainingUsd: '-0.250000', rawRemaining: '-0.25', rawUnit: 'usd', basis: 'api_key_quota'
}, 'OpenAI 兼容账单接口透支必须保留实际负值')
assert.deepEqual(parseOpenAiCompatibleBillingBalance(
  { object: 'billing_subscription', hard_limit_usd: '100000000' },
  { object: 'list', total_usage: '0' },
  { rawUnit: 'usd' }
), {
  status: 'unsupported', basis: 'api_key_quota', errorMessage: '上游 API Key 为无限额度，无法确认实际可用余额'
}, '无限额度占位值不能被显示为真实余额')
assert.deepEqual(parseOpenAiCompatibleBillingBalance(
  { object: 'billing_subscription', hard_limit_usd: '53.625' },
  { object: 'list', total_usage: '807.75' },
  { rawUnit: 'cny', divisor: '7.5' }
), {
  status: 'fresh', remainingUsd: '6.073000', rawRemaining: '45.5475', rawUnit: 'cny', basis: 'api_key_quota'
}, 'New API 的 CNY 展示单位必须按上游汇率换算')
assert.deepEqual(parseOpenAiCompatibleBillingStatus({ success: true, data: { quota_display_type: 'USD' } }), {
  rawUnit: 'usd'
})
assert.deepEqual(parseOpenAiCompatibleBillingStatus({ success: true, data: { quota_display_type: 'CNY', usd_exchange_rate: '7.5' } }), {
  rawUnit: 'cny', divisor: '7.5'
})
assert.deepEqual(parseOpenAiCompatibleBillingStatus({ success: true, data: { display_in_currency: false, quota_per_unit: 500000 } }), {
  rawUnit: 'quota', divisor: '500000'
})
assert.deepEqual(parseOpenAiCompatibleBillingStatus({ success: true, data: { quota_display_type: 'TOKENS' } }), {
  snapshot: {
    status: 'unsupported', basis: 'api_key_quota', errorMessage: '上游余额展示单位为 TOKENS，无法安全换算为美元'
  }
})

assert.deepEqual(parseLiteLlmBalance({ info: { max_budget: '10', spend: '2.69' } }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'budget'
})
assert.deepEqual(parseLiteLlmBalance({ info: { max_budget: '10', spend: '10' } }), {
  status: 'fresh', remainingUsd: '0.000000', rawRemaining: '0', rawUnit: 'usd', basis: 'budget'
}, 'LiteLLM 预算用尽仍是可解析的零余额')
assert.deepEqual(parseLiteLlmBalance({ info: { max_budget: '10', spend: '10.25' } }), {
  status: 'fresh', remainingUsd: '-0.250000', rawRemaining: '-0.25', rawUnit: 'usd', basis: 'budget'
}, 'LiteLLM 超额预算必须保留实际负值')
assert.deepEqual(parseLiteLlmBalance({ info: { spend: 2.69 } }), {
  status: 'unsupported', basis: 'budget'
})

assert.deepEqual(parseUserBalance({ balance: '7.31', is_active: true }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'wallet'
})
assert.deepEqual(parseUserBalance({ balance: 0, is_active: true }), {
  status: 'fresh', remainingUsd: '0.000000', rawRemaining: '0', rawUnit: 'usd', basis: 'wallet'
}, '钱包余额耗尽仍是可解析的零余额')
assert.deepEqual(parseUserBalance({ balance: '-1' }), {
  status: 'fresh', remainingUsd: '-1.000000', rawRemaining: '-1', rawUnit: 'usd', basis: 'wallet'
}, '通用钱包透支必须保留实际负值')
assert.throws(() => parseUserBalance({ is_active: true }), /balance/)

assert.deepEqual(parseCustomBalance(
  { data: { balance: '731' } },
  { path: '/balance', remainingPointer: '/data/balance', divisor: '100' }
), { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '731', rawUnit: 'usd', basis: 'custom' })
assert.deepEqual(parseCustomBalance(
  { total: '10', used: '2.69' },
  { path: '/usage', totalPointer: '/total', usedPointer: '/used' }
), { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'custom' })
assert.deepEqual(parseCustomBalance(
  { balance: '-1' },
  { path: '/balance', remainingPointer: '/balance' }
), { status: 'fresh', remainingUsd: '-1.000000', rawRemaining: '-1', rawUnit: 'usd', basis: 'custom' })

console.log('account balance adapters regression passed')
