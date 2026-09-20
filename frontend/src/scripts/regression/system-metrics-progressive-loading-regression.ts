import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const sourceRoot = fileURLToPath(new URL('../..', import.meta.url))
const workspaceRoot = resolve(sourceRoot, '../..')
const view = readFileSync(resolve(sourceRoot, 'views/stats/SystemMetricsStatsView.vue'), 'utf8')
const card = readFileSync(resolve(sourceRoot, 'views/stats/StatsChartCard.vue'), 'utf8')
const api = readFileSync(resolve(sourceRoot, 'api/domains/stats.ts'), 'utf8')
// Node backend 已归档（X02），system-metrics 契约的现役实现是 Go gateway
// statreads（statreads.go 注册路由，systemmetrics.go 承载趋势与运行时读取）。
// 前端只消费 go-runtime-trend / health-snapshot / runtime/jobs 三个分节接口。
const route = readFileSync(resolve(workspaceRoot, 'backend-go/projects/gateway/internal/statreads/statreads.go'), 'utf8')
const loadPageDataStart = view.indexOf('function loadPageData(')
const loadPageDataEnd = view.indexOf('function setupRuntimeObservers', loadPageDataStart)
const loadPageDataSource = loadPageDataStart >= 0 && loadPageDataEnd > loadPageDataStart
  ? view.slice(loadPageDataStart, loadPageDataEnd)
  : ''

if (!api.includes("goRuntimeTrend: ") || !api.includes("'/stats/system-metrics/go-runtime-trend'")) throw new Error('go runtime trend API must use a dedicated endpoint')
if (!api.includes("systemMetricsHealthSnapshot: ") || !api.includes("'/stats/system-metrics/health-snapshot'")) throw new Error('health snapshot API must use a dedicated endpoint')
if (!api.includes("systemMetricsRuntimeJobs: ") || !api.includes("'/stats/system-metrics/runtime/jobs'")) throw new Error('runtime jobs API must use a dedicated endpoint')
for (const retiredApi of ['systemMetricsTrend:', 'systemMetricsRuntimeSummary:', 'systemMetricsRuntimeQueues:']) {
  if (api.includes(retiredApi)) throw new Error(`retired Node-era system metrics API must not remain: ${retiredApi}`)
}
if (/systemMetrics:\s*\(/.test(api) || /http\.get\('\/stats\/system-metrics'[,)]/.test(api)) throw new Error('unused wide system metrics API must not remain public')
for (const routePath of ['go-runtime-trend', 'runtime/jobs', 'health-snapshot']) {
  if (!route.includes(`"/system-metrics/${routePath}"`)) throw new Error(`system metrics route missing: ${routePath}`)
}
if (!view.includes('api.stats.goRuntimeTrend(rangeParams, { signal: controller.signal })')) throw new Error('go runtime trend view must request the DTO with cancellation')
if (!view.includes('api.stats.systemMetricsHealthSnapshot({ signal: controller.signal })')) throw new Error('health snapshot must be requested with cancellation')
if (!view.includes(':error="backgroundJobsError"') || !view.includes(':on-retry="loadBackgroundJobs"')) throw new Error('background jobs must expose a targeted retry state')
const activated = view.match(/onActivated\((?:async )?\(\) => \{[\s\S]*?\n\}\)/)?.[0] ?? ''
if (!/onActivated\((?:async )?\(\) =>/.test(view) || !activated.includes('setupRuntimeObservers()')) throw new Error('KeepAlive activation must restore runtime observation')
if (/loadPageData|loadUsageStatsWindow|forceUsageWindow/.test(activated)) throw new Error('KeepAlive activation must not reload system metrics')
if (!loadPageDataSource.includes('const windowLoad = loadUsageStatsWindow(')) throw new Error('initial page load must start usage-window independently')
if (!loadPageDataSource.includes('const currentPageLoadGeneration = ++pageLoadGeneration')) throw new Error('page loads must advance an independent generation')
if (!/await windowLoad\s+if \(currentPageLoadGeneration !== pageLoadGeneration\) return/.test(loadPageDataSource)) throw new Error('usage-window completion must verify the current page-load generation before starting APIs')
if (loadPageDataSource.indexOf('const windowLoad = loadUsageStatsWindow(') >= loadPageDataSource.indexOf('return loadTrendData()')) throw new Error('usage-window loading must start before the trend request')
if (loadPageDataSource.includes('force, viewScope') || loadPageDataSource.includes('force: true')) throw new Error('business refresh must reuse cached usage-window metadata')
if (!view.includes('ref="backgroundJobsSectionRef"') || !view.includes('new IntersectionObserver(')) throw new Error('runtime sections must load only when their individual section approaches the viewport')
if (!loadPageDataSource.includes('if (backgroundJobsSectionLoaded.value) void loadBackgroundJobs()')) throw new Error('page mount must not eagerly request background jobs before its section is visible')
if (!view.includes('disconnectRuntimeObservers()') || !view.includes('backgroundJobsRequestSeq += 1')) throw new Error('deactivation must stop runtime observation and invalidate old responses')
for (const token of ['goRuntimeAbortController?.abort()', 'healthSnapshotAbortController?.abort()', 'backgroundJobsAbortController?.abort()']) {
  if (!view.includes(token)) throw new Error(`superseded or deactivated system metrics requests must abort ${token}`)
}
if (!view.includes('{ signal: controller.signal }')) throw new Error('system metrics requests must pass AbortSignal through the domain API')
if (!view.includes('watch(() => authState.revision.value')) throw new Error('identity changes must invalidate system metrics')
if (!view.includes('pageLoadGeneration += 1')) throw new Error('identity changes and deactivation must invalidate page loads waiting on usage-window metadata')
for (const token of ['goRuntimeTrend.value = undefined', 'healthSnapshot.value = undefined', 'backgroundJobsResult.value = undefined']) {
  if (!view.includes(token)) throw new Error(`identity changes must clear privileged state: ${token}`)
}
if (!view.includes('disposed || !pageActive.value')) throw new Error('queued viewport callbacks must not request runtime after deactivation or unmount')
if (view.includes('systemMetricsRuntime.value') || api.includes('systemMetricsRuntime:')) throw new Error('view must not retain the retired wide runtime response')
if (card.includes('<a-alert')) throw new Error('chart cards must not expose loading failures as page banners')

console.log('system metrics progressive loading regression passed')
