import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const sourceRoot = fileURLToPath(new URL('../..', import.meta.url))
const workspaceRoot = resolve(sourceRoot, '../..')
const view = readFileSync(resolve(sourceRoot, 'views/stats/SystemMetricsStatsView.vue'), 'utf8')
const card = readFileSync(resolve(sourceRoot, 'views/stats/StatsChartCard.vue'), 'utf8')
const api = readFileSync(resolve(sourceRoot, 'api/domains/stats.ts'), 'utf8')
const route = readFileSync(resolve(workspaceRoot, 'backend/src/modules/stats/stats.routes.ts'), 'utf8')
const worker = readFileSync(resolve(workspaceRoot, 'backend/src/storage/sqlite-read-worker.ts'), 'utf8')
const workerTypes = readFileSync(resolve(workspaceRoot, 'backend/src/storage/sqlite-read-worker-pool.types.ts'), 'utf8')
const domainTypes = readFileSync(resolve(sourceRoot, 'types/domain/usage-stats.ts'), 'utf8')
const loadPageDataStart = view.indexOf('function loadPageData(')
const loadPageDataEnd = view.indexOf('function setupRuntimeObservers', loadPageDataStart)
const loadPageDataSource = loadPageDataStart >= 0 && loadPageDataEnd > loadPageDataStart
  ? view.slice(loadPageDataStart, loadPageDataEnd)
  : ''

if (!api.includes("systemMetricsTrend: ") || !api.includes("'/stats/system-metrics/trend'")) throw new Error('system metrics trend API must use a dedicated endpoint')
if (/systemMetrics:\s*\(/.test(api) || /http\.get\('\/stats\/system-metrics'[,)]/.test(api)) throw new Error('unused wide system metrics API must not remain public')
if (!route.includes("statsRouter.get('/system-metrics/trend'")) throw new Error('system metrics trend route missing')
if (/statsRouter\.get\('\/system-metrics',/.test(route)) throw new Error('unused wide system metrics HTTP route must be removed')
if (!route.includes('getSystemMetricsTrendAsync(')) throw new Error('system metrics trend route must use a dedicated narrow repository loader')
if (/system-metrics\/trend[\s\S]*getSystemMetricsOverviewAsync\(/.test(route)) throw new Error('system metrics trend route must not load the wide overview and trim it afterwards')
if (!worker.includes("case 'get_system_metrics_trend_read_only'")) throw new Error('narrow trend loader must run in the SQLite read worker')
if (!workerTypes.includes("type: 'get_system_metrics_trend_read_only'")) throw new Error('narrow trend worker operation must be typed')
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
  if (!route.includes(`/system-metrics/${routePath}`)) throw new Error(`runtime split route missing: ${routePath}`)
}
for (const apiName of ['systemMetricsRuntimeSummary', 'systemMetricsRuntimeJobs', 'systemMetricsRuntimeQueues']) {
  if (!api.includes(`${apiName}:`)) throw new Error(`frontend runtime split API missing: ${apiName}`)
}
if (view.includes('systemMetricsRuntime.value') || api.includes('systemMetricsRuntime:')) throw new Error('view must not retain the retired wide runtime response')
if (!route.includes('systemMetricsRuntimeJobRows(runtime)') || !route.includes('systemMetricsRuntimeQueueRows([')) throw new Error('runtime split routes must pass through explicit response projections')
for (const unusedField of ['runtimeSnapshotSource:', 'runtimeSnapshotObservedAt,', 'gatewayRoutingObservability:', 'ingestWorker:', 'statsWorker:', 'opsWorker:']) {
  if (route.includes(unusedField)) throw new Error(`system metrics runtime must not return unused field ${unusedField}`)
}
if (card.includes('<a-alert')) throw new Error('chart cards must not expose loading failures as page banners')

console.log('system metrics progressive loading regression passed')
