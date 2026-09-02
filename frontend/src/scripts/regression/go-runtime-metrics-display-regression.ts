import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const viewSource = readFileSync(new URL('../../views/stats/SystemMetricsStatsView.vue', import.meta.url), 'utf8')
const chartSource = readFileSync(new URL('../../views/stats/statsChartOptions.ts', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const typeSource = readFileSync(new URL('../../types/domain/usage-stats.ts', import.meta.url), 'utf8')

assert.match(apiSource, /goRuntimeTrend:[\s\S]*http\.get\('\/stats\/system-metrics\/go-runtime-trend'/, 'Go runtime must use the same-origin admin API')
assert.match(apiSource, /goRuntimeTrend:[\s\S]*signal: options\?\.signal/, 'Go runtime API must accept AbortSignal')
assert.match(typeSource, /export interface GoRuntimeTrendOverview[\s\S]*runtimeKind: 'go'[\s\S]*timezone: string[\s\S]*items: GoRuntimeTrendItem\[\]/, 'Go runtime overview type must identify the Go runtime and its display timezone')

for (const field of [
  'windowStart', 'windowEnd', 'sampleCount', 'goroutinesAvg', 'goroutinesMax',
  'heapAllocBytesAvg', 'heapAllocBytesMax', 'heapLiveBytesAvg', 'heapLiveBytesMax',
  'heapObjectsAvg', 'heapObjectsMax', 'threadsAvg', 'threadsMax'
]) {
  assert.match(typeSource, new RegExp(`export interface GoRuntimeTrendItem[\\s\\S]*${field}:`), `Go runtime DTO must expose ${field}`)
}

assert.match(viewSource, /Go Runtime 指标趋势/, 'system metrics page must render an independent Go Runtime section')
assert.match(viewSource, /api\.stats\.goRuntimeTrend\(selectedRangeParams\(\), \{ signal: controller\.signal \}\)/, 'Go runtime page must call the dedicated API with AbortSignal')
assert.match(viewSource, /goRuntimeAbortController\?\.abort\(\)/, 'Go runtime requests must be aborted during lifecycle transitions')
assert.match(viewSource, /goRuntimeError[\s\S]*@click="loadGoRuntimeTrend"/, 'Go runtime failures must expose retry action')
assert.match(viewSource, /:empty-description="goRuntimeEmptyDescription"/, 'Go runtime must expose a meaningful empty state')
assert.match(viewSource, /buildGoRuntimeOption\(goRuntimeTrend\.value\.items, goRuntimeTrend\.value\.timezone, goRuntimeChartView\.value\)/, 'Go runtime chart must consume only the Go DTO, configured timezone, and selected low-density view')
assert.match(viewSource, /a-segmented v-model:value="goRuntimeChartView"/, 'Go runtime chart must expose a low-density metric view switcher')
assert.match(viewSource, /goRuntimeViewUnavailable/, 'Go runtime must explain when an older payload omits a selected metric group')
assert.match(chartSource, /hasGoRuntimeChartData/, 'Go runtime must detect unavailable optional metric groups without filling zeroes')
assert.match(viewSource, /disposeChart\(goRuntimeChart\)/, 'Go runtime chart must be disposed with the page lifecycle')

const goChartStart = chartSource.indexOf('export function buildGoRuntimeOption')
const goChartEnd = chartSource.indexOf('export function buildProcessEventLoopOption', goChartStart)
assert.ok(goChartStart >= 0 && goChartEnd > goChartStart, 'Go runtime chart option must be present')
const goChartSource = chartSource.slice(goChartStart, goChartEnd)
const goMetricSource = chartSource.slice(chartSource.indexOf('function goRuntimeSeries'), goChartEnd)
assert.doesNotMatch(goChartSource, /eventLoopLagMs/, 'Go runtime chart must not reuse Node event-loop fields')
for (const field of ['goroutinesAvg', 'goroutinesMax', 'heapAllocBytesAvg', 'heapAllocBytesMax', 'heapLiveBytesAvg', 'heapLiveBytesMax', 'heapObjectsAvg', 'heapObjectsMax', 'threadsAvg', 'threadsMax']) {
  assert.match(goMetricSource, new RegExp(`item\\.${field}`), `Go runtime chart must display ${field}`)
}
for (const field of ['rssBytesAvg', 'schedulerLatencyP95SecondsAvg', 'schedulerLatencyP99SecondsAvg', 'gcPauseP95SecondsAvg', 'gcPauseP99SecondsAvg']) {
  assert.match(goMetricSource, new RegExp(`item\\.${field}`), `Go runtime chart must support optional ${field}`)
}
for (const field of ['cpuPercentAvg', 'fdCountAvg', 'uptimeSecondsAvg']) {
  assert.match(viewSource, new RegExp(`latest\\.${field}`), `Go runtime summary must display optional ${field} when available`)
}
assert.match(goMetricSource, /filter\(\(item\) => item\.data\.some\(\(value\) => value !== null\)\)/, 'Go runtime chart must drop unavailable series instead of plotting zeroes')

console.log('go-runtime-metrics-display-regression passed')
