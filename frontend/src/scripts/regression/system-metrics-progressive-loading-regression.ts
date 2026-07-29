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
const loadPageDataEnd = view.indexOf('async function loadRuntimeData', loadPageDataStart)
const loadPageDataSource = loadPageDataStart >= 0 && loadPageDataEnd > loadPageDataStart
  ? view.slice(loadPageDataStart, loadPageDataEnd)
  : ''

if (!api.includes("systemMetricsTrend: ") || !api.includes("'/stats/system-metrics/trend'")) throw new Error('system metrics trend API must use a dedicated endpoint')
if (!route.includes("statsRouter.get('/system-metrics/trend'")) throw new Error('system metrics trend route missing')
if (!route.includes('getSystemMetricsTrendAsync(')) throw new Error('system metrics trend route must use a dedicated narrow repository loader')
if (/system-metrics\/trend[\s\S]*getSystemMetricsOverviewAsync\(/.test(route)) throw new Error('system metrics trend route must not load the wide overview and trim it afterwards')
if (!worker.includes("case 'get_system_metrics_trend_read_only'")) throw new Error('narrow trend loader must run in the SQLite read worker')
if (!workerTypes.includes("type: 'get_system_metrics_trend_read_only'")) throw new Error('narrow trend worker operation must be typed')
if (!view.includes('api.stats.systemMetricsTrend(rangeParams)')) throw new Error('system metrics view must request the narrow trend DTO')
if (view.includes('Promise.all([\n      api.stats.systemMetrics(')) throw new Error('trend request must not be blocked by usage-window loading')
if (!view.includes(':error="trendError"') || !view.includes(':on-retry="loadData"')) throw new Error('trend cards must expose retry state')
if (!view.includes(':error="runtimeError"') || !view.includes(':on-retry="loadRuntimeData"')) throw new Error('runtime cards must expose retry state')
if (!/onActivated\((?:async )?\(\) =>/.test(view) || !view.includes('needsReloadOnActivate')) throw new Error('KeepAlive activation must reload stale system metrics')
if (!loadPageDataSource.includes('void loadUsageStatsWindow(') || loadPageDataSource.includes('await loadUsageStatsWindow(')) throw new Error('initial page load must start usage-window independently')
if (loadPageDataSource.indexOf('void loadUsageStatsWindow(') >= loadPageDataSource.indexOf('return loadData()')) throw new Error('usage-window loading must not delay the trend request')
if (loadPageDataSource.includes('force, viewScope') || loadPageDataSource.includes('force: true')) throw new Error('business refresh must reuse cached usage-window metadata')
if (!view.includes('ref="runtimeSectionRef"') || !view.includes('new IntersectionObserver(')) throw new Error('runtime must load only when its section approaches the viewport')
if (!loadPageDataSource.includes('if (runtimeSectionLoaded.value) void loadRuntimeData()')) throw new Error('page mount must not eagerly request runtime before its section is visible')
if (!view.includes('runtimeObserver?.disconnect()') || !view.includes('runtimeRequestSeq += 1')) throw new Error('deactivation must stop runtime observation and invalidate old responses')
if (!view.includes('watch(() => authState.revision.value')) throw new Error('identity changes must invalidate and reload system metrics')
if (!view.includes('systemMetrics.value = undefined') || !view.includes('systemMetricsRuntime.value = undefined')) throw new Error('identity changes must clear privileged system metrics before replacement requests')
if (!view.includes('disposed || !pageActive.value')) throw new Error('queued viewport callbacks must not request runtime after deactivation or unmount')
for (const unusedField of ['runtimeSnapshotSource:', 'runtimeSnapshotObservedAt,', 'gatewayRoutingObservability:', 'ingestWorker:', 'statsWorker:', 'opsWorker:']) {
  if (route.includes(unusedField)) throw new Error(`system metrics runtime must not return unused field ${unusedField}`)
}
if (!route.includes('backgroundJobs?.map(systemMetricsRuntimeJobRow)')) throw new Error('runtime jobs must pass through an explicit response projection')
for (const unusedField of ['initialDelayMs?:', 'stablePhaseOffsetMs?:', 'scheduleMode?:', 'overlapPolicy?:', 'timeoutMs?:', 'overdueMs?:', 'runningSince?:', 'lastScheduledAt?:', 'lastSkipAt?:', 'lastSkipReason?:', 'consecutiveFailureCount?:', '[key: string]: unknown']) {
  if (domainTypes.includes(unusedField)) throw new Error(`frontend system metrics DTO must not declare omitted field ${unusedField}`)
}
if (card.includes('<a-alert')) throw new Error('chart cards must not expose loading failures as page banners')

console.log('system metrics progressive loading regression passed')
