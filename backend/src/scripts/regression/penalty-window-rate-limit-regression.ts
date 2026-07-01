import { strict as assert } from 'node:assert'

import {
  consumePenaltyWindowRateLimit,
  createPenaltyWindowRateLimitStore
} from '../../modules/rate-limit/penalty-window-rate-limit.js'

const store = createPenaltyWindowRateLimitStore({
  name: 'penalty_window_rate_limit_regression',
  maxPenaltyMs: 10 * 60_000
})
const rule = { windowSeconds: 60, maxRequests: 2 }
const scopeKey = 'regression-client'
const now = Date.parse('2026-01-01T00:00:00.000Z')

assert.equal(consumePenaltyWindowRateLimit({ store, scopeKey, rules: [rule], nowMs: now }).allowed, true)
assert.equal(consumePenaltyWindowRateLimit({ store, scopeKey, rules: [rule], nowMs: now + 1000 }).allowed, true)

const firstBlocked = consumePenaltyWindowRateLimit({ store, scopeKey, rules: [rule], nowMs: now + 2000 })
assert.equal(firstBlocked.allowed, false, '首次超限应被拒绝')
assert.equal(firstBlocked.retryAfterSeconds, 60, '首次超限惩罚应等于窗口长度')

const secondBlocked = consumePenaltyWindowRateLimit({ store, scopeKey, rules: [rule], nowMs: now + 3000 })
assert.equal(secondBlocked.allowed, false, '惩罚期内再次请求应继续拒绝')
assert.equal(secondBlocked.retryAfterSeconds, 120, '惩罚期内再次触发应把 Retry-After 翻倍')

const thirdBlocked = consumePenaltyWindowRateLimit({ store, scopeKey, rules: [rule], nowMs: now + 4000 })
assert.equal(thirdBlocked.allowed, false, '连续惩罚期请求应继续拒绝')
assert.equal(thirdBlocked.retryAfterSeconds, 240, '连续触发惩罚应继续按 2 倍递增')

console.log('penalty window rate limit regression passed')
