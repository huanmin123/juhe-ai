import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const sourceRoot = fileURLToPath(new URL('../..', import.meta.url))
const workspaceRoot = resolve(sourceRoot, '../..')
const view = readFileSync(resolve(sourceRoot, 'views/stats/SystemMetricsStatsView.vue'), 'utf8')
const card = readFileSync(resolve(sourceRoot, 'views/stats/StatsChartCard.vue'), 'utf8')
const api = readFileSync(resolve(sourceRoot, 'api/domains/stats.ts'), 'utf8')
// Node backend 已归档（X02），sqlite-read-worker.ts 契约源未随归档保留，
// stats.routes.ts 归档快照也已死；system-metrics 路由与查询契约的现役等价
// 实现是 Go gateway statreads（statreads.go 注册路由，systemmetrics.go 承载
// 趋势与运行时分节读取；Node SQLite read worker 概念无 Go 对应物）。
const route = readFileSync(resolve(workspaceRoot, 'backend-go/projects/gateway/internal/statreads/statreads.go'), 'utf8')
const systemMetrics = readFileSync(resolve(workspaceRoot, 'backend-go/projects/gateway/internal/statreads/systemmetrics.go'), 'utf8')
const systemMetricsTrendHandlerSource = systemMetrics.slice(
  systemMetrics.indexOf('func (d *Deps) systemMetricsTrendHandler'),
  systemMetrics.indexOf('func (d *Deps) processEventLoopTrendLatestRows')
)
const domainTypes = readFileSync(resolve(sourceRoot, 'types/domain/usage-stats.ts'), 'utf8')
const loadPageDataStart = view.indexOf('function loadPageData(')
const loadPageDataEnd = view.indexOf('function setupRuntimeObservers', loadPageDataStart)
const loadPageDataSource = loadPageDataStart >= 0 && loadPageDataEnd > loadPageDataStart
  ? view.slice(loadPageDataStart, loadPageDataEnd)
  : ''

if (!api.includes("systemMetricsTrend: ") || !api.includes("'/stats/system-metrics/trend'")) throw new Error('system metrics trend API must use a dedicated endpoint')
if (/systemMetrics:\s*\(/.test(api) || /http\.get\('\/stats\/system-metrics'[,)]/.test(api)) throw new Error('unused wide system metrics API must not remain public')
if (!route.includes('"/system-metrics/trend"')) throw new Error('system metrics trend route missing')
if (route.includes('"/system-metrics",')) throw new Error('unused wide system metrics HTTP route must be removed')
// Node getSystemMetricsTrendAsync + get_system_metrics_trend_read_only
// read-worker operation 的等价契约：窄趋势 handler 直读统计库
// system_metrics_trend_windows 预聚合窗口表。
if (!systemMetricsTrendHandlerSource.includes('statsTable("system_metrics_trend_windows")')) throw new Error('system metrics trend route must use the dedicated narrow trend window loader')
if (/runtimeSnapshot|gatewayRoutingObservability|SystemMetricsOverview/.test(systemMetricsTrendHandlerSource)) throw new Error('system metrics trend route must not load the wide overview and trim it afterwards')
if (domainTypes.includes('export interface SystemMetricsOverview')) throw new Error('frontend must not retain the retired wide system metrics DTO')
if (!view.includes('api.stats.systemMetricsTrend(rangeParams, { signal: controller.signal })')) throw new Error('system metrics view must request the narrow trend DTO with cancellation')
if (view.includes('Promise.all([\n      api.stats.systemMetrics(')) throw new Error('trend request must not be blocked by usage-window loading')
if (!view.includes(':error="trendError"') || !view.includes(':on-retry="loadData"')) throw new Error('trend cards must expose retry state')
if (!view.includes(':error="backgroundJobsError"') || !view.includes(':on-retry="loadBackgroundJobs"')) throw new Error('background jobs must expose a targeted retry state')
if (!view.includes(':error="backgroundQueuesError"') || !view.includes(':on-retry="loadBackgroundQueues"')) throw new Error('background queues must expose a targeted retry state')
const activated = view.match(/onActivated\((?:async )?\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
if (!/onActivated\((?:async )?\(\) =>/.test(view) || !activated.includes('setupRuntimeObservers()')) throw new Error('KeepAlive activation must restore runtime observation')
if (/loadPageData|loadUsageStatsWindow|forceUsageWindow/.test(activated)) throw new Error('KeepAlive activation must not reload system metrics')
if (!loadPageDataSource.includes('const windowLoad = loadUsageStatsWindow(')) throw new Error('initial page load must start usage-window independently')
if (!loadPageDataSource.includes('const currentPageLoadGeneration = ++pageLoadGeneration')) throw new Error('page loads must advance an independent generation')
if (!/await windowLoad\s+if \(currentPageLoadGeneration !== pageLoadGeneration\) return/.test(loadPageDataSource)) throw new Error('usage-window completion must verify the current page-load generation before starting APIs')
if (loadPageDataSource.indexOf('const windowLoad = loadUsageStatsWindow(') >= loadPageDataSource.indexOf('return loadTrendData()')) throw new Error('usage-window loading must start before the trend request')
if (loadPageDataSource.includes('force, viewScope') || loadPageDataSource.includes('force: true')) throw new Error('business refresh must reuse cached usage-window metadata')
if (!view.includes('ref="backgroundJobsSectionRef"') || !view.includes('ref="backgroundQueuesSectionRef"') || !view.includes('new IntersectionObserver(')) throw new Error('runtime sections must load only when their individual section approaches the viewport')
if (!loadPageDataSource.includes('if (backgroundJobsSectionLoaded.value) void loadBackgroundJobs()')) throw new Error('page mount must not eagerly request background jobs before its section is visible')
if (!loadPageDataSource.includes('if (backgroundQueuesSectionLoaded.value) void loadBackgroundQueues()')) throw new Error('page mount must not eagerly request background queues before its section is visible')
if (!view.includes('disconnectRuntimeObservers()') || !view.includes('backgroundJobsRequestSeq += 1') || !view.includes('backgroundQueuesRequestSeq += 1')) throw new Error('deactivation must stop runtime observation and invalidate old responses')
for (const token of ['trendAbortController?.abort()', 'runtimeSummaryAbortController?.abort()', 'backgroundJobsAbortController?.abort()', 'backgroundQueuesAbortController?.abort()']) {
  if (!view.includes(token)) throw new Error(`superseded or deactivated system metrics requests must abort ${token}`)
}
if (!view.includes('{ signal: controller.signal }')) throw new Error('system metrics requests must pass AbortSignal through the domain API')
if (!view.includes('watch(() => authState.revision.value')) throw new Error('identity changes must invalidate system metrics')
if (!view.includes('pageLoadGeneration += 1')) throw new Error('identity changes and deactivation must invalidate page loads waiting on usage-window metadata')
for (const token of ['systemMetrics.value = undefined', 'runtimeSummary.value = undefined', 'backgroundJobsResult.value = undefined', 'backgroundQueuesResult.value = undefined']) {
  if (!view.includes(token)) throw new Error(`identity changes must clear privileged state: ${token}`)
}
if (!view.includes('disposed || !pageActive.value')) throw new Error('queued viewport callbacks must not request runtime after deactivation or unmount')
for (const routePath of ['runtime/summary', 'runtime/jobs', 'runtime/queues']) {
  if (!route.includes(`"/system-metrics/${routePath}"`)) throw new Error(`runtime split route missing: ${routePath}`)
}
for (const apiName of ['systemMetricsRuntimeSummary', 'systemMetricsRuntimeJobs', 'systemMetricsRuntimeQueues']) {
  if (!api.includes(`${apiName}:`)) throw new Error(`frontend runtime split API missing: ${apiName}`)
}
if (view.includes('systemMetricsRuntime.value') || api.includes('systemMetricsRuntime:')) throw new Error('view must not retain the retired wide runtime response')
// Node systemMetricsRuntimeJobRows / systemMetricsRuntimeQueueRows 投影的等价
// 契约：jobs/queues 分节 handler 经 paginateSystemMetricsRows 构造显式行投影。
const runtimeJobsHandlerSource = systemMetrics.slice(
  systemMetrics.indexOf('func (d *Deps) runtimeJobsHandler'),
  systemMetrics.indexOf('func (d *Deps) runtimeQueuesHandler')
)
const runtimeQueuesHandlerSource = systemMetrics.slice(
  systemMetrics.indexOf('func (d *Deps) runtimeQueuesHandler'),
  systemMetrics.indexOf('// parseRuntimePageQuery mirrors')
)
if (!runtimeJobsHandlerSource.includes('paginateSystemMetricsRows([]any{}') || !runtimeQueuesHandlerSource.includes('paginateSystemMetricsRows([]any{}')) throw new Error('runtime split routes must pass through explicit response projections')
const runtimeSummaryHandlerSource = systemMetrics.slice(
  systemMetrics.indexOf('func (d *Deps) runtimeSummaryHandler'),
  systemMetrics.indexOf('func (d *Deps) runtimeJobsHandler')
)
for (const unusedField of ['runtimeSnapshotSource', 'runtimeSnapshotObservedAt', 'gatewayRoutingObservability']) {
  if (runtimeSummaryHandlerSource.includes(unusedField)) throw new Error(`system metrics runtime must not return unused field ${unusedField}`)
}
if (card.includes('<a-alert')) throw new Error('chart cards must not expose loading failures as page banners')

console.log('system metrics progressive loading regression passed')
