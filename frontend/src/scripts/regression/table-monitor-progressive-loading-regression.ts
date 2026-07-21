import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../views/table-monitor/TableMonitorView.vue', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')

assert.match(source, /const historyLoaded = ref\(false\)/, '表监控应显式记录趋势历史是否已按需加载')
assert.match(source, /IntersectionObserver/, '表监控趋势应在进入视口后触发历史加载')
assert.match(source, /api\.tableMonitor\.databaseHistory\(/, '表监控仍应保留趋势历史接口')
assert.doesNotMatch(
  source,
  /Promise\.all\(\[\s*api\.tableMonitor\.overview[\s\S]*api\.tableMonitor\.databaseHistory/,
  '表监控首屏不应并行请求 database-history'
)
assert.match(source, /api\.tableMonitor\.overview\(\)/, '表监控首屏概览应使用无日期窗口的最新快照入口')
assert.doesNotMatch(apiSource, /history:\s*\(params: TableMonitorHistoryParams\)/, '前端不应继续暴露未使用的单表历史 API')

console.log('表监控渐进加载回归通过：首屏只读最新概览，趋势历史按视口进入后加载')
