import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const routesSource = readFileSync(new URL('../../modules/stats/stats.routes.ts', import.meta.url), 'utf8')
const runtimeSource = readFileSync(new URL('../../config/runtime.ts', import.meta.url), 'utf8')
const routeStart = routesSource.indexOf("statsRouter.get('/system-metrics/go-runtime-trend'")
const routeEnd = routesSource.indexOf("statsRouter.get('/system-metrics/runtime/summary'", routeStart)
assert.ok(routeStart >= 0 && routeEnd > routeStart, 'Go runtime admin proxy route must be present')
const route = routesSource.slice(routeStart, routeEnd)
const schemaStart = routesSource.indexOf('const goRuntimeTrendItemSchema')
const schemaEnd = routesSource.indexOf('const goRuntimeTrendPayloadSchema', schemaStart)
assert.ok(schemaStart >= 0 && schemaEnd > schemaStart, 'Go runtime trend schema must be present')
const schema = routesSource.slice(schemaStart, schemaEnd)

assert.match(route, /statsRouter\.get\('\/system-metrics\/go-runtime-trend', requireAdmin, async/, 'Go runtime trend must remain behind admin authentication')
assert.match(route, /runtimeConfig\.goRuntimeMetricsUrl/, 'proxy must use the configured loopback Go metrics origin')
assert.match(route, /\/__aisys__\/api\/stats\/go-runtime-trend/, 'proxy must target the jobs loopback trend endpoint')
assert.match(route, /searchParams\.set\('from'/, 'proxy must pass the normalized start instant')
assert.match(route, /searchParams\.set\('to'/, 'proxy must pass the normalized end instant')
assert.match(route, /goRuntimeTrendPayloadSchema\.safeParse\(await response\.json\(\)\)/, 'proxy must validate the full Go trend payload schema')
assert.match(route, /payload\.success/, 'proxy must reject non-Go or malformed payloads')
assert.match(route, /timezone,/, 'proxy must return the configured usage timezone for chart labels')
assert.match(route, /res\.status\(503\)/, 'unavailable or malformed Go metrics must be visible as 503')
assert.match(route, /res\.json\(ok\(/, 'proxy response must use the existing admin API envelope')
for (const field of ['cpuPercentAvg', 'rssBytesAvg', 'fdCountAvg', 'uptimeSecondsAvg', 'gcPauseP95SecondsAvg', 'schedulerLatencyP99SecondsAvg']) {
  assert.match(schema, new RegExp(`${field}: z\\.number\\(\\)\\.finite\\(\\)\\.nonnegative\\(\\)`), `proxy schema must accept optional Go field ${field}`)
}
assert.match(runtimeSource, /goRuntimeMetricsUrl: loopbackHttpOriginConfig\(/, 'Go metrics origin must be constrained to loopback HTTP')

console.log('go-runtime-metrics-admin-proxy-regression passed')
