import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const viewSource = readFileSync(new URL('../../views/stats/StatsView.vue', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../../api/domains/stats.ts', import.meta.url), 'utf8')
const workerSource = readFileSync(new URL('../../../../backend/src/storage/sqlite-read-worker.ts', import.meta.url), 'utf8')
const workerTypesSource = readFileSync(new URL('../../../../backend/src/storage/sqlite-read-worker-pool.types.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../../../backend/src/modules/stats/stats.routes.ts', import.meta.url), 'utf8')

assert.doesNotMatch(viewSource, /api\.(?:stats|myStats)\.usageOverview\(/, '统计首页不得继续调用兼容 usage-overview')
assert.match(viewSource, /usageOverviewSummary\(/, '统计首页首屏必须调用独立 summary')
assert.match(viewSource, /IntersectionObserver/, '三个图表必须按视口触发加载')
assert.match(viewSource, /usageOverviewHourlyTrend\(/, '小时趋势必须使用独立端点')
assert.match(viewSource, /usageOverviewModelDistribution\(/, '模型分布必须使用独立端点')
assert.match(viewSource, /usageOverviewErrors\(/, '错误榜必须使用独立端点')
assert.match(viewSource, /const chartRequestSeq = \{ hourlyTrend: 0, modelDistribution: 0, errors: 0 \}/, '三个图表必须拥有独立请求代次')
assert.match(viewSource, /rangeSignature\(result\.range\) !== rangeSignature\(currentOverview\.range\)/, '图表结果必须校验服务端归一化 range')
assert.match(viewSource, /chartObserver\?\.unobserve\(entry\.target\)/, '图表首次进入视口后不得重复观察并发请求')
assert.match(viewSource, /\.\.\.currentAuthSignature\(\)/, '请求签名必须包含 auth revision 与当前用户身份')
assert.match(viewSource, /onDeactivate:\s*handlePageDeactivate/, 'KeepAlive 失活必须使在途统计请求失效')
assert.match(viewSource, /v-if="summaryError"/, 'summary 必须有独立错误态和重试入口')

for (const path of ['summary', 'hourly-trend', 'model-distribution', 'errors']) {
  assert(routesSource.includes(`'/usage-overview/${path}'`), `Node 必须注册 usage-overview/${path}`)
  assert(apiSource.includes(`/stats/usage-overview/${path}`), `管理端 API 必须暴露 usage-overview/${path}`)
  assert(apiSource.includes(`/my-stats/usage-overview/${path}`), `个人端 API 必须暴露 usage-overview/${path}`)
}
for (const operation of [
  'get_usage_stats_overview_summary_read_only',
  'get_usage_stats_overview_hourly_trend_read_only',
  'get_usage_stats_overview_model_distribution_read_only',
  'get_usage_stats_overview_errors_read_only'
]) {
  assert(workerSource.includes(`case '${operation}'`), `${operation} 必须接入 SQLite read worker`)
  assert(workerTypesSource.includes(`type: '${operation}'`), `${operation} 必须进入 worker operation 类型`)
}

console.log('stats overview progressive loading regression passed')
