import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import { createTableMonitorHistoryRequestGate } from '@/views/table-monitor/tableMonitorHistoryRequestGate'

const source = readFileSync(new URL('../../views/table-monitor/TableMonitorView.vue', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../router/index.ts', import.meta.url), 'utf8')
const loadDataSource = source.slice(source.indexOf('async function loadData()'), source.indexOf('async function loadHistoryData()'))

assert.match(source, /const historyLoaded = ref\(false\)/, '表监控应显式记录趋势历史是否已按需加载')
assert.match(source, /数据截至 \{\{ formatDateTime\(overview\.sampledAt\) \}\}[\s\S]*缓存最多复用 1 小时/, '表监控页面必须展示采样时间和缓存复用上限')
assert.match(source, /@refresh="refreshTableMonitor"/, '表监控手动刷新必须走旁路刷新流程')
assert.match(source, /refresh: forceOverviewRefresh\.value \|\| undefined/, '表监控概览请求必须传递旁路刷新标记')
assert.match(routerSource, /path:\s*'\/table-monitor'[\s\S]{0,500}keepAlive:\s*true/, '表监控切换菜单后必须保留页面实例，避免重复首屏加载')
assert.match(source, /overviewLastLoadedAtMs[\s\S]*Date\.now\(\)[\s\S]*tableMonitorOverviewMaxClientAgeMs/, 'KeepAlive 页面回访超过一小时必须重新校验概览快照')
assert.match(source, /const retryOverview = overviewLoadInterrupted[\s\S]*loadOverviewData\(\{ shouldApply: \(\) => pageActive\.value \}\)/, 'KeepAlive 回访超龄或请求中断时只应重新加载概览，不应重新加载已懒加载趋势')
assert.match(source, /overviewLoadInterrupted[\s\S]*loading\.value = false[\s\S]*overviewLoadInterrupted = false[\s\S]*loadOverviewData/, '概览请求在页面失活期间中断后，回访必须重试概览')
assert.match(source, /overviewRefreshInterrupted[\s\S]*forceRetryOverview[\s\S]*forceOverviewRefresh\.value = true/, '旁路刷新在页面失活期间中断后，回访必须继续旁路刷新')
assert.match(source, /historyLoadInterrupted[\s\S]*historyLoading\.value[\s\S]*historyLoadInterrupted = false[\s\S]*loadHistoryData/, '趋势请求在页面失活期间中断后，回访必须重试趋势')
assert.match(source, /IntersectionObserver/, '表监控趋势应在进入视口后触发历史加载')
assert.match(source, /api\.tableMonitor\.databaseHistory\(/, '表监控仍应保留趋势历史接口')
assert.doesNotMatch(
  loadDataSource,
  /api\.tableMonitor\.databaseHistory/,
  '表监控首屏不应并行请求 database-history'
)
assert.match(source, /api\.tableMonitor\.overview\(\{[\s\S]*page: pagination\.current[\s\S]*pageSize: pagination\.pageSize[\s\S]*keyword:/, '表监控概览应使用服务端分页与前缀搜索')
assert.match(apiSource, /history:\s*\(params: TableMonitorHistoryParams\)/, '单表趋势应使用独立按需历史 API')
assert.match(source, /row-clickable[\s\S]*@row-click="openTableHistory"/, '桌面表行点击后才应打开单表趋势')
assert.match(source, /function openTableHistory[\s\S]*tableHistoryOpen\.value = true[\s\S]*loadTableHistoryData/, '单表历史只能由显式表行交互触发')
assert.match(source, /if \(!pageActive\.value \|\| !tableHistoryOpen\.value \|\| !table\) return/, '隐藏或失活页面不得读取单表历史')
assert.match(source, /onDeactivate: deactivateTableMonitorPage/, '页面失活必须作废趋势请求和 observer')
assert.match(source, /useResponsivePagedList<TableStorageOverviewSummary>/, '概览分页应复用带旧响应门的列表协调器')
assert.match(source, /function deactivateTableMonitorPage[\s\S]*invalidatePendingLoads\(\)/, '页面失活必须作废概览分页在途请求')
assert.doesNotMatch(apiSource, /cleanupNonBusinessData:[^\n]*noTimeout/, '仅投递队列的清理请求不应禁用客户端超时')

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
