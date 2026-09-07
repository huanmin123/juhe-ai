import assert from 'node:assert/strict'

import { UserRequestLimitCounter } from '../../modules/gateway/runtime/user-request-limit-counter.js'

const counter = new UserRequestLimitCounter()
const settings = {
  gatewayUserRequestLimitPerMinute: 2_000_000,
  gatewayUserRequestLimitPerDay: 2_000_000,
  gatewayUserRequestLimitPerWeek: 2_000_000,
  gatewayUserRequestLimitPerMonth: 2_000_000,
  usageStatsTimezone: 'Asia/Shanghai'
}
const nowMs = Date.parse('2026-07-26T12:00:00.000Z')

for (let index = 0; index < 100_000; index += 1) {
  counter.consume({ systemAccountId: 'benchmark-user', settings, nowMs })
}

const iterations = 1_000_000
const startedAt = process.hrtime.bigint()
let lastAllowed = false
for (let index = 0; index < iterations; index += 1) {
  lastAllowed = counter.consume({ systemAccountId: 'benchmark-user', settings, nowMs }).allowed
}
const durationNs = Number(process.hrtime.bigint() - startedAt)
const averageMicroseconds = durationNs / iterations / 1_000
const requestsPerSecond = iterations / (durationNs / 1_000_000_000)

console.log(JSON.stringify({ iterations, averageMicroseconds, requestsPerSecond }, null, 2))
assert.equal(lastAllowed, true)
assert.ok(averageMicroseconds < 10, `用户请求限流平均耗时 ${averageMicroseconds.toFixed(3)}µs，超过 10µs 门禁`)
