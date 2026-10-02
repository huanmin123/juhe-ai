import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const viewSource = readFileSync(new URL('../../views/stats/SystemMetricsStatsView.vue', import.meta.url), 'utf8')
const chartSource = readFileSync(new URL('../../views/stats/statsChartOptions.ts', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const typeSource = readFileSync(new URL('../../types/domain/usage-stats.ts', import.meta.url), 'utf8')
const jobsCardSource = readFileSync(new URL('../../views/stats/StatsBackgroundJobsCard.vue', import.meta.url), 'utf8')
const jobsLabelsSource = readFileSync(new URL('../../views/stats/statsBackgroundJobs.ts', import.meta.url), 'utf8')
const healthSource = readFileSync(new URL('../../views/stats/systemMetricsHealth.ts', import.meta.url), 'utf8')

// —— API：Go runtime 专用端点 + AbortSignal ——
assert.match(apiSource, /goRuntimeTrend:[\s\S]*http\.get\('\/stats\/system-metrics\/go-runtime-trend'/, 'Go runtime must use the same-origin admin API')
assert.match(apiSource, /goRuntimeTrend:[\s\S]*signal: options\?\.signal/, 'Go runtime API must accept AbortSignal')
assert.match(apiSource, /systemMetricsRuntimeJobs:[\s\S]*status\?: string/, 'runtime jobs API must expose the optional status filter')

// —— 类型：新契约 overview（roles + samplingEnabled，扁平 role/items 删除） ——
assert.match(
  typeSource,
  /export interface GoRuntimeTrendOverview \{[\s\S]*?runtimeKind: 'go'[\s\S]*?timezone: string[\s\S]*?samplingEnabled: boolean[\s\S]*?roles: GoRuntimeTrendRoleTrend\[\]\n\}/,
  'Go runtime overview type must carry samplingEnabled and per-role trends'
)
const overviewBody = typeSource.match(/export interface GoRuntimeTrendOverview \{([\s\S]*?)\n\}/)?.[1] ?? ''
assert.doesNotMatch(overviewBody, /role:|items:/, 'the flat overview-level role/items fields must be removed in favor of roles[]')
assert.match(
  typeSource,
  /export interface GoRuntimeTrendRoleTrend \{\s*role: GoRuntimeTrendRole\s*items: GoRuntimeTrendItem\[\]\s*\}/,
  'per-role trend must pair a gateway|jobs role with its own items'
)

for (const field of [
  'windowStart', 'windowEnd', 'sampleCount', 'goroutinesAvg', 'goroutinesMax',
  'heapAllocBytesAvg', 'heapAllocBytesMax', 'heapLiveBytesAvg', 'heapLiveBytesMax',
  'heapObjectsAvg', 'heapObjectsMax', 'threadsAvg', 'threadsMax'
]) {
  assert.match(typeSource, new RegExp(`export interface GoRuntimeTrendItem[\\s\\S]*${field}:`), `Go runtime DTO must expose ${field}`)
}

// —— 视图：Go Runtime 卡与采样开关空态 ——
assert.match(viewSource, /Go Runtime 指标趋势/, 'system metrics page must render an independent Go Runtime section')
assert.match(viewSource, /api\.stats\.goRuntimeTrend\(rangeParams, \{ signal: controller\.signal \}\)/, 'Go runtime page must call the dedicated API with AbortSignal')
assert.match(viewSource, /goRuntimeAbortController\?\.abort\(\)/, 'Go runtime requests must be aborted during lifecycle transitions')
assert.match(viewSource, /goRuntimeError[\s\S]*@click="loadGoRuntimeTrend"/, 'Go runtime failures must expose retry action')
assert.match(viewSource, /:empty-description="goRuntimeEmptyDescription"/, 'Go runtime must expose a meaningful empty state')
assert.match(viewSource, /goRuntimeSamplingDisabled/, 'Go runtime must read the samplingEnabled contract flag')
assert.match(viewSource, /a-result[\s\S]*?Go Runtime 指标采样未启用[\s\S]*?JUHE_AI_GO_RUNTIME_METRICS_STORE=disabled/, 'sampling-disabled state must explain that monitoring is factory-on and only an explicit disabled config turns it off')
assert.match(viewSource, /监控默认跟随主存储开启/, 'sampling guidance must state the default-on follow-main-store semantics')
assert.match(viewSource, /移除该配置或改为 sqlite\/postgres 并重启 gateway\/jobs/, 'sampling guidance must explain the re-enable path')

// —— 视图：角色 tabs + 三视图 segmented + 单角色图表 ——
assert.match(viewSource, /a-tabs v-if="goRuntimeRoles\.length" v-model:active-key="goRuntimeActiveRole"/, 'Go runtime must render gateway/jobs role tabs')
assert.match(viewSource, /a-tab-pane v-for="roleTrend in goRuntimeRoles"/, 'role tabs must iterate the roles contract array')
assert.match(viewSource, /a-segmented v-model:value="goRuntimeChartView"/, 'Go runtime chart must expose a metric group switcher')
assert.match(viewSource, /value: 'concurrency'[\s\S]*value: 'memory'[\s\S]*value: 'resource'/, 'Go runtime must expose concurrency, memory, and CPU metric groups')
assert.match(viewSource, /watch\(goRuntimeActiveRole, \(\) => renderCharts\(\)\)/, 'switching the role tab must re-render the chart')
assert.match(viewSource, /buildGoRuntimeOption\(activeGoRuntimeRoleItems\.value, goRuntimeTrend\.value\.timezone, goRuntimeChartView\.value\)/, 'Go runtime chart must consume only the active role items, configured timezone, and selected view')
assert.match(viewSource, /goRuntimeViewUnavailable/, 'Go runtime must explain when an older payload omits a selected metric group')
assert.match(viewSource, /disposeChart\(goRuntimeChart\)/, 'Go runtime chart must be disposed with the page lifecycle')

// —— 视图：最新摘要 chips（CPU 单核/Heap/Goroutines/线程/FD/运行时长，缺失不显示） ——
for (const field of ['cpuPercentAvg', 'heapAllocBytesAvg', 'goroutinesAvg', 'threadsAvg', 'fdCountAvg', 'uptimeSecondsAvg']) {
  assert.match(viewSource, new RegExp(`latest\\.${field}`), `Go runtime summary must display optional ${field} when available`)
}
assert.match(viewSource, /label: 'CPU（单核）'/, 'cpuPercentAvg chip must keep the single-core CPU wording')
assert.doesNotMatch(viewSource, /latest\.gomaxprocsAvg/, 'Go runtime summary must not resurrect the GOMAXPROCS chip')

// —— 图表配置：三视图 + 单角色签名 + 全 null 过滤 ——
assert.match(chartSource, /export type GoRuntimeChartView = 'concurrency' \| 'memory' \| 'resource'/, 'Go runtime chart views must be concurrency, memory, and resource')
assert.match(chartSource, /export function buildGoRuntimeOption\(items: GoRuntimeTrendItem\[\]/, 'buildGoRuntimeOption must take a single-role items array')
assert.match(chartSource, /export function hasGoRuntimeChartData\(items: GoRuntimeTrendItem\[\]/, 'hasGoRuntimeChartData must detect unavailable metric groups for the single role')

const goChartStart = chartSource.indexOf('export function buildGoRuntimeOption')
const goChartEnd = chartSource.indexOf('function goRuntimeTooltip', goChartStart)
assert.ok(goChartStart >= 0 && goChartEnd > goChartStart, 'Go runtime chart option must be present')
const goChartSource = chartSource.slice(goChartStart, goChartEnd)
const goMetricSource = chartSource.slice(chartSource.indexOf('function goRuntimeSeries'), goChartEnd)
assert.doesNotMatch(goChartSource, /eventLoopLagMs/, 'Go runtime chart must not reuse Node event-loop fields')
for (const field of ['goroutinesAvg', 'goroutinesMax', 'heapAllocBytesAvg', 'heapAllocBytesMax', 'heapLiveBytesAvg', 'heapLiveBytesMax', 'heapObjectsAvg', 'heapObjectsMax', 'threadsAvg', 'threadsMax', 'gomaxprocsAvg']) {
  assert.match(goMetricSource, new RegExp(`item\\.${field}`), `Go runtime chart must display ${field}`)
}
assert.match(goMetricSource, /name: 'RSS 平均 \(MiB\)', yAxisIndex: 0, read: \(item\) => bytesToMiB\(item\.rssBytesAvg\)/, 'memory view must plot RSS averages in MiB')
assert.match(goMetricSource, /name: 'RSS 峰值 \(MiB\)', yAxisIndex: 0, read: \(item\) => bytesToMiB\(item\.rssBytesMax\)/, 'memory view must plot RSS peaks in MiB')
assert.match(goMetricSource, /name: 'FD 平均（个）', read: \(item\) => item\.fdCountAvg/, 'concurrency view must include FD averages')
assert.match(goMetricSource, /name: 'FD 峰值（个）', read: \(item\) => item\.fdCountMax/, 'concurrency view must include FD peaks')
assert.match(goMetricSource, /name: 'CPU 平均（%）', read: \(item\) => item\.cpuPercentAvg/, 'resource view must plot CPU percent averages')
assert.match(goMetricSource, /name: 'CPU 峰值（%）', read: \(item\) => item\.cpuPercentMax/, 'resource view must plot CPU percent peaks')
assert.doesNotMatch(goMetricSource, /schedulerLatency|gcPauseP9/, 'Go runtime chart must not keep Prometheus percentile views the trend library never provides')
assert.match(goMetricSource, /filter\(\(item\) => item\.data\.some\(\(value\) => value !== null\)\)/, 'Go runtime chart must drop unavailable series instead of plotting zeroes')
assert.match(goChartSource, /xAxis: \{ type: 'category', data: items\.map\(\(item\) => goRuntimeWindowLabel\(item\.windowStart, timezone\)\)/, 'x axis must be built only from the single role windows')

// —— 第一层：进程状态双卡 ——
assert.match(viewSource, /title="进程状态"/, 'the process status card must replace the retired runtime status card')
assert.match(viewSource, /class="health-process-list"/, 'gateway and jobs process cards must stack as full-width rows (2026-10 重设计：整宽横条卡取代失衡双栏)')
assert.match(viewSource, /health-line-unhealthy/, 'process cards must lead with an unhealthy-items line so real failures stand out')
assert.match(viewSource, /healthConclusionColor\(process\.display\.conclusion\)/, 'each process card must show an overall conclusion tag')
assert.match(viewSource, /健康面不可达/, 'unreachable health must surface its reason inside the process card')
assert.match(healthSource, /accountCircuitRuntimeReady: '账户熔断运行时'/, 'readiness label map must follow the gateway payload keys')
assert.match(healthSource, /sessionRetentionReady: '会话保留'/, 'readiness label map must cover session retention')
assert.match(healthSource, /if \(value === 'active'\) return '主用'/, 'ownerMode text values must map active to the primary wording')
assert.match(healthSource, /if \(conclusion === 'partial'\) return 'warning'/, 'readiness conclusion colors must cover the partial state')
assert.match(healthSource, /unhealthyLabels\.length > 0 \? 'partial' : 'ok'/, 'conclusion must be partial only for enabled-but-not-ready items; disabled-by-deployment features are expected, not degraded')

// —— 第三层：后台任务状态过滤与中文任务名 ——
assert.match(jobsCardSource, /label: '成功', value: 'completed'/, 'status filter must expose completed')
assert.match(jobsCardSource, /label: '失败', value: 'failed'/, 'status filter must expose failed')
assert.match(jobsCardSource, /label: '运行中', value: 'running'/, 'status filter must expose running')
assert.match(jobsCardSource, /label: '排队中', value: 'queued'/, 'status filter must expose queued')
assert.match(jobsCardSource, /label: '已跳过', value: 'skipped'/, 'status filter must expose skipped')
assert.match(jobsCardSource, /\(event: 'status-change', status: string\)/, 'status selection must be emitted to the parent')
assert.match(jobsCardSource, /\(event: 'refresh'\): void/, 'the jobs card must expose its own refresh action')
assert.match(jobsCardSource, /backgroundJobNameText\(record\.jobName\)/, 'job names must render through the Chinese label mapping')
assert.match(jobsLabelsSource, /'account-list-availability-projection-maintenance': '账户列表可用性投影维护'/, 'job name mapping must cover the projection maintenance job')
assert.match(viewSource, /status: backgroundJobStatus\.value \|\| undefined/, 'jobs requests must default to the unfiltered status when omitted')
assert.match(viewSource, /backgroundJobPage\.value = 1/, 'status changes must reset pagination to the first page')

console.log('go-runtime-metrics-display-regression passed')
