import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const repositorySource = readFileSync(new URL('../../storage/table-monitor.repository.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/table-monitor/table-monitor.routes.ts', import.meta.url), 'utf8')
const readWorkerTypesSource = readFileSync(new URL('../../storage/sqlite-read-worker-pool.types.ts', import.meta.url), 'utf8')
const overviewRouteSource = routesSource.slice(
  routesSource.indexOf("tableMonitorRouter.get('/overview'"),
  routesSource.indexOf("tableMonitorRouter.post('/non-business-data/cleanup'")
)

assert.match(repositorySource, /getTableStorageOverview\(input: TableStorageOverviewInput = \{\}\)/, '表监控应保留概览读取入口')
assert.match(repositorySource, /latest_table|MAX\(sampled_at\)|ORDER BY sampled_at DESC, id DESC/, '表监控概览应按最新快照读取')
assert.match(overviewRouteSource, /await getTableStorageOverviewAsync\(\{\s*limit: parsed\.data\.limit\s*\}\)/, '概览路由不应把日期窗口传入首屏概览查询')
assert.doesNotMatch(overviewRouteSource, /startAt|endAt/, '概览路由不应按整段历史窗口查询')
assert.match(readWorkerTypesSource, /type: 'get_table_storage_overview_read_only'\s+input\?: \{ limit\?: number \}/, 'SQLite 读 worker 的概览契约也应只保留 limit')

console.log('表监控渐进接口回归通过：概览与历史趋势读取边界明确')
