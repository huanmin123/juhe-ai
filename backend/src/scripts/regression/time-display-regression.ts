import assert from 'node:assert/strict'

import {
  formatResponseTimes,
  formatShanghaiTime,
  normalizeRequestTimes
} from '../../shared/time-display.js'

const instant = '2026-08-15T22:34:49.137Z'
assert.equal(formatShanghaiTime(instant), '2026-08-16 06:34:49.137')

assert.deepEqual(formatResponseTimes({
  checkedAt: instant,
  completedAt: new Date(instant),
  nested: { createdAt: instant },
  unchanged: 'not-a-time'
}), {
  checkedAt: '2026-08-16 06:34:49.137',
  completedAt: '2026-08-16 06:34:49.137',
  nested: { createdAt: '2026-08-16 06:34:49.137' },
  unchanged: 'not-a-time'
})

assert.deepEqual(normalizeRequestTimes({
  expectedUpdatedAt: '2026-08-16 06:34:49.137',
  nested: ['2026-08-16 06:34:49.137', 'not-a-time']
}), {
  expectedUpdatedAt: instant,
  nested: [instant, 'not-a-time']
})

console.log('上海时间展示与 API 回传归一化回归通过')
