import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { canonicalizeRfc3339Instant, requiredRfc3339Instant } from '../../shared/rfc3339.js'
import { strictDateTimeQueryValue } from '../../shared/query-values.js'
import { createLogEventEnvelope } from '../../shared/logging/log-event-contract.js'

const instant = '2026-08-15T22:34:49.137Z'
assert.equal(canonicalizeRfc3339Instant(instant), instant)
assert.equal(canonicalizeRfc3339Instant('2026-08-16T06:34:49.137+08:00'), instant)
assert.equal(canonicalizeRfc3339Instant('2026-08-16T06:34:49.1+08:00'), '2026-08-15T22:34:49.100Z')
assert.equal(canonicalizeRfc3339Instant('2026-08-16T06:34:49.137'), undefined)
assert.equal(canonicalizeRfc3339Instant('2026-08-16 06:34:49.137'), undefined)
assert.equal(canonicalizeRfc3339Instant('2026-02-30T06:34:49Z'), undefined)
assert.throws(() => requiredRfc3339Instant('2026-08-16T06:34:49.137'), /RFC3339/)
assert.equal(strictDateTimeQueryValue('2026-08-16T06:34:49.137+08:00'), instant)
assert.throws(() => strictDateTimeQueryValue('2026-08-16T06:34:49.137'), { statusCode: 400 })
assert.equal(createLogEventEnvelope({
  time: '2026-08-16T06:34:49.137+08:00',
  level: 'info',
  service: 'test',
  role: 'test',
  event: 'strict_time_regression'
}).time, instant)
assert.throws(() => createLogEventEnvelope({
  time: '2026-08-16T06:34:49.137',
  level: 'info',
  service: 'test',
  role: 'test',
  event: 'strict_time_regression'
}), /RFC3339/)

const systemApiSource = readFileSync(resolve('src/modules/system-api/system-api-app.ts'), 'utf8')
const serverSource = readFileSync(resolve('src/server.ts'), 'utf8')
const loggerSource = readFileSync(resolve('src/shared/logger.ts'), 'utf8')
const logEventSource = readFileSync(resolve('src/shared/logging/log-event-contract.ts'), 'utf8')

assert.doesNotMatch(systemApiSource, /displayTimeResponseMiddleware/)
assert.doesNotMatch(systemApiSource, /normalizeDisplayTimeRequestMiddleware/)
assert.equal(existsSync(resolve('src/shared/time-display.ts')), false)
assert.doesNotMatch(loggerSource, /formatShanghaiNow|Asia\/Shanghai/)
assert.doesNotMatch(logEventSource, /formatShanghaiNow|Asia\/Shanghai/)
assert.match(systemApiSource, /checkedAt: new Date\(\)\.toISOString\(\)/)
assert.match(serverSource, /checkedAt: new Date\(\)\.toISOString\(\)/)

const utcRfc3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/
assert.match(new Date().toISOString(), utcRfc3339)

console.log('UTC API 时间传输、严格 RFC3339 输入与 UTC 日志回归通过')
