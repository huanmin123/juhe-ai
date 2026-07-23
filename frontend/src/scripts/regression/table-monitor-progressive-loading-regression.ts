import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { createTableMonitorHistoryRequestGate } from '@/views/table-monitor/tableMonitorHistoryRequestGate'

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

const gate = createTableMonitorHistoryRequestGate()
const firstA = gate.begin('A')
const requestB = gate.begin('B')
assert.equal(firstA.isCurrent('A'), false, 'A→B 后旧 A 不得提交')
assert.equal(requestB.isCurrent('B'), true, 'A→B 后 B 应拥有提交权')
const secondA = gate.begin('A')
assert.equal(requestB.isCurrent('B'), false, 'A→B→A 后旧 B 不得提交')
assert.equal(firstA.isCurrent('A'), false, 'A→B→A 后第一次 A 不得重新获得提交权')
assert.equal(secondA.isCurrent('A'), true, 'A→B→A 后第二次 A 应拥有提交权')
gate.invalidate()
assert.equal(secondA.isCurrent('A'), false, '页面卸载后在途请求不得提交')

console.log('表监控渐进加载回归通过：首屏只读最新概览，趋势历史按视口进入后加载')
