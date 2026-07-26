import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const viewSource = readFileSync(new URL('../../views/stats/StatsView.vue', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const chartSource = readFileSync(new URL('../../views/stats/statsChartOptions.ts', import.meta.url), 'utf8')
const workerSource = readFileSync(new URL('../../../../backend/src/storage/sqlite-read-worker.ts', import.meta.url), 'utf8')
const workerTypesSource = readFileSync(new URL('../../../../backend/src/storage/sqlite-read-worker-pool.types.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../../../backend/src/modules/stats/stats.routes.ts', import.meta.url), 'utf8')

assert.doesNotMatch(viewSource, /api\.(?:stats|myStats)\.usageOverview\(/, '统计首页不得继续调用兼容 usage-overview')
assert.match(viewSource, /usageOverviewSummary\(/, '统计首页首屏必须调用独立 summary')
assert.match(viewSource, /IntersectionObserver/, '四个图表必须按视口触发加载')
assert.match(viewSource, /const defaultDateRange = \(\) => recentDateRange\(MAX_RANGE_DAYS\)/, '统计概览浏览器初始展示必须为近 31 天')
assert.match(viewSource, /usageOverviewDailyTrend\(\{ \.\.\.rangeParams, systemAccountId \}\)/, '管理端日趋势必须携带当前筛选范围')
assert.match(viewSource, /api\.myStats\.usageOverviewDailyTrend\(rangeParams\)/, '个人端日趋势必须携带当前筛选范围')
assert.match(viewSource, /const signature = JSON\.stringify\(\[pageSeq, \.\.\.currentAuthSignature\(\), isManagementView\.value \? 'admin' : 'self', systemAccountId \?\? '', rangeParams\.startDate \?\? '', rangeParams\.endDate \?\? ''\]\)/, '日趋势请求签名必须包含页面序号、身份、统计主体和日期筛选')
assert.match(viewSource, /resetDailyTrend\(\)[\s\S]*if \(dailyTrendLoaded\.value\) void loadDailyTrend\(options\.force === true\)/, '日期筛选刷新必须失效并重新请求日趋势')
assert.match(viewSource, /usageOverviewHourlyTrend\(/, '小时趋势必须使用独立端点')
assert.match(viewSource, /usageOverviewModelDistribution\(/, '模型分布必须使用独立端点')
assert.match(viewSource, /usageOverviewErrors\(/, '错误榜必须使用独立端点')
for (const description of [
  '请求和失败按次数统计；平均总耗时取网关均值。',
  '按模型汇总 Token 消耗；没有 Token 的记录会用请求次数参与展示。',
  '统计窗口内失败请求按错误码聚合；悬浮可查看状态码和错误信息。'
]) {
  assert(!viewSource.includes(description), `统计概览不应展示说明文案：${description}`)
}
assert.match(viewSource, /const chartRequestSeq = \{ hourlyTrend: 0, modelDistribution: 0, errors: 0 \}/, '三个图表必须拥有独立请求代次')
assert.match(viewSource, /rangeSignature\(result\.range\) !== rangeSignature\(currentOverview\.range\)/, '图表结果必须校验服务端归一化 range')
assert.match(viewSource, /chartObserver\?\.unobserve\(entry\.target\)/, '图表首次进入视口后不得重复观察并发请求')
assert.match(viewSource, /\.\.\.currentAuthSignature\(\)/, '请求签名必须包含 auth revision 与当前用户身份')
assert.match(viewSource, /onDeactivate:\s*handlePageDeactivate/, 'KeepAlive 失活必须使在途统计请求失效')
assert.doesNotMatch(viewSource, /<a-alert[\s\S]*summaryError/, 'summary 加载失败不得显示页面横幅')

for (const path of ['summary', 'hourly-trend', 'model-distribution', 'errors']) {
  assert(routesSource.includes(`'/usage-overview/${path}'`), `Node 必须注册 usage-overview/${path}`)
  assert(apiSource.includes(`/stats/usage-overview/${path}`), `管理端 API 必须暴露 usage-overview/${path}`)
  assert(apiSource.includes(`/my-stats/usage-overview/${path}`), `个人端 API 必须暴露 usage-overview/${path}`)
}
assert(routesSource.includes("'/usage-overview/daily-trend'"), 'Node 必须注册 usage-overview/daily-trend')
assert(apiSource.includes('/stats/usage-overview/daily-trend'), '管理端 API 必须暴露 usage-overview/daily-trend')
assert(apiSource.includes('/my-stats/usage-overview/daily-trend'), '个人端 API 必须暴露 usage-overview/daily-trend')
for (const operation of [
  'get_usage_stats_overview_summary_read_only',
  'get_usage_stats_overview_daily_trend_read_only',
  'get_usage_stats_overview_hourly_trend_read_only',
  'get_usage_stats_overview_model_distribution_read_only',
  'get_usage_stats_overview_errors_read_only'
]) {
  assert(workerSource.includes(`case '${operation}'`), `${operation} 必须接入 SQLite read worker`)
  assert(workerTypesSource.includes(`type: '${operation}'`), `${operation} 必须进入 worker operation 类型`)
}

const usageTrendOptionSource = chartSource.slice(
  chartSource.indexOf('export function buildUsageTrendOption'),
  chartSource.indexOf('export function buildDailyConsumptionOption')
)
assert.doesNotMatch(usageTrendOptionSource, /Token 消耗|item\.totalTokens|name: 'Token'/, '小时趋势图不得继续渲染 Token 系列或 Token 纵轴')
const dailyTrendOptionSource = chartSource.slice(
  chartSource.indexOf('export function buildDailyConsumptionOption'),
  chartSource.indexOf('export function buildModelDistributionOption')
)
assert.match(dailyTrendOptionSource, /type: 'bar'/, '筛选范围日消耗必须使用柱状图')
assert.match(dailyTrendOptionSource, /value: item\.totalTokens/, '日消耗柱高必须只由 Token 决定')
assert.doesNotMatch(dailyTrendOptionSource, /value: item\.totalCost|yAxisIndex/, '成本不得成为柱高或独立纵轴')
assert.doesNotMatch(chartSource, /formatCompactInteger\(totalTokens\).*formatInteger\(totalTokens\)/, '日趋势 tooltip 不得在阶梯单位后重复展示精确 Token 数量')

console.log('stats overview progressive loading regression passed')
