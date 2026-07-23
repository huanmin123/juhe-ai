import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const sourceRoot = fileURLToPath(new URL('../..', import.meta.url))
const workspaceRoot = resolve(sourceRoot, '../..')
const view = readFileSync(resolve(sourceRoot, 'views/stats/SystemMetricsStatsView.vue'), 'utf8')
const card = readFileSync(resolve(sourceRoot, 'views/stats/StatsChartCard.vue'), 'utf8')
const api = readFileSync(resolve(sourceRoot, 'api/domains/stats.ts'), 'utf8')
const route = readFileSync(resolve(workspaceRoot, 'backend/src/modules/stats/stats.routes.ts'), 'utf8')

if (!api.includes("systemMetricsTrend: ") || !api.includes("'/stats/system-metrics/trend'")) throw new Error('system metrics trend API must use a dedicated endpoint')
if (!route.includes("statsRouter.get('/system-metrics/trend'")) throw new Error('system metrics trend route missing')
if (!view.includes('api.stats.systemMetricsTrend(rangeParams)')) throw new Error('system metrics view must request the narrow trend DTO')
if (view.includes('Promise.all([\n      api.stats.systemMetrics(')) throw new Error('trend request must not be blocked by usage-window loading')
if (!view.includes(':error="trendError"') || !view.includes(':on-retry="loadData"')) throw new Error('trend cards must expose retry state')
if (!view.includes(':error="runtimeError"') || !view.includes(':on-retry="loadRuntimeData"')) throw new Error('runtime cards must expose retry state')
if (!view.includes('onActivated(() =>') || !view.includes('needsReloadOnActivate')) throw new Error('KeepAlive activation must reload stale system metrics')
if (!view.includes('void loadUsageStatsWindow().then')) throw new Error('initial page load must start usage-window independently')
if (card.includes('<a-alert')) throw new Error('chart cards must not expose loading failures as page banners')

console.log('system metrics progressive loading regression passed')
