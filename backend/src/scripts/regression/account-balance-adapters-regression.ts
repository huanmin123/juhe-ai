import assert from 'node:assert/strict'

import {
  parseCustomBalance,
  parseLiteLlmBalance,
  parseNewApiBalance,
  parseSub2ApiBalance,
  parseUserBalance
} from '../../modules/accounts/account-balance-adapters.js'

assert.deepEqual(parseSub2ApiBalance({ mode: 'quota_limited', remaining: 7.31, unit: 'USD' }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'api_key_quota'
})
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', remaining: -1, unit: 'USD' }), {
  status: 'unlimited', basis: 'subscription'
})
assert.deepEqual(parseSub2ApiBalance({ mode: 'unrestricted', planName: '钱包余额', remaining: '12.5', unit: 'USD' }), {
  status: 'fresh', remainingUsd: '12.500000', rawRemaining: '12.5', rawUnit: 'usd', basis: 'wallet'
})
assert.throws(() => parseSub2ApiBalance({ remaining: 7.31, unit: 'CNY' }), /USD/)

assert.deepEqual(parseNewApiBalance({ data: { total_available: 3655000 } }, { quotaPerUnit: 500000 }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '3655000', rawUnit: 'quota', basis: 'api_key_quota'
})
assert.deepEqual(parseNewApiBalance({ data: { unlimited_quota: true } }, { quotaPerUnit: 500000 }), {
  status: 'unlimited', basis: 'api_key_quota'
})
assert.throws(() => parseNewApiBalance({ data: {} }, { quotaPerUnit: 500000 }), /total_available/)

assert.deepEqual(parseLiteLlmBalance({ info: { max_budget: '10', spend: '2.69' } }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'budget'
})
assert.deepEqual(parseLiteLlmBalance({ info: { spend: 2.69 } }), {
  status: 'unsupported', basis: 'budget'
})

assert.deepEqual(parseUserBalance({ balance: '7.31', is_active: true }), {
  status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'wallet'
})
assert.throws(() => parseUserBalance({ is_active: true }), /balance/)
assert.throws(() => parseUserBalance({ balance: '-1' }), /负数/)

assert.deepEqual(parseCustomBalance(
  { data: { balance: '731' } },
  { path: '/balance', remainingPointer: '/data/balance', divisor: '100' }
), { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '731', rawUnit: 'usd', basis: 'custom' })
assert.deepEqual(parseCustomBalance(
  { total: '10', used: '2.69' },
  { path: '/usage', totalPointer: '/total', usedPointer: '/used' }
), { status: 'fresh', remainingUsd: '7.310000', rawRemaining: '7.31', rawUnit: 'usd', basis: 'custom' })
assert.throws(() => parseCustomBalance(
  { balance: '-1' },
  { path: '/balance', remainingPointer: '/balance' }
), /负数/)

console.log('account balance adapters regression passed')
