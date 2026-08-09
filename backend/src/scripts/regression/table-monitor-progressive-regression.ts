import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

const repositorySource = readFileSync(new URL('../../storage/table-monitor.repository.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/table-monitor/table-monitor.routes.ts', import.meta.url), 'utf8')
const readWorkerTypesSource = readFileSync(new URL('../../storage/sqlite-read-worker-pool.types.ts', import.meta.url), 'utf8')
const overviewRouteSource = routesSource.slice(
  routesSource.indexOf("tableMonitorRouter.get('/overview'"),
  routesSource.indexOf("tableMonitorRouter.post('/non-business-data/cleanup'")
)
const tableHistoryFunctionSource = repositorySource.slice(
  repositorySource.indexOf('export function listTableStorageHistory('),
  repositorySource.indexOf('export async function listTableStorageHistoryAsync(')
)
const cleanupReceiptSource = routesSource.slice(
  routesSource.indexOf('interface NonBusinessDataCleanupReceipt'),
  routesSource.indexOf("tableMonitorRouter.get('/overview'")
)

assert.match(repositorySource, /getTableStorageOverview\(input: TableStorageOverviewInput = \{\}\)/, '表监控应保留概览读取入口')
assert.match(repositorySource, /const database = getTableMonitorDatabase\(\)/, 'SQLite 表监控页面必须只读取 Go 专用输出库')
assert.doesNotMatch(repositorySource, /(collectTableStorageSnapshot|cleanupTableStorageSnapshotsBefore)/, 'Node 不得保留表监控采样或保留清理 writer API')
assert.match(repositorySource, /WITH table_keys AS[\s\S]*ORDER BY latest\.sampled_at DESC, latest\.id DESC/, '表监控概览应先枚举唯一表键，再定点读取每张表最新快照')
assert.match(overviewRouteSource, /await getTableStorageOverviewAsync\(\{\s*page: parsed\.data\.page,\s*pageSize: parsed\.data\.pageSize,\s*keyword: parsed\.data\.keyword\s*\}\)/, '概览路由只应把服务端分页和关键词传入首屏概览查询')
assert.doesNotMatch(overviewRouteSource, /startAt|endAt/, '概览路由不应按整段历史窗口查询')
assert.match(readWorkerTypesSource, /type: 'get_table_storage_overview_read_only'\s+input\?: \{ page\?: number; pageSize\?: number; keyword\?: string \}/, 'SQLite 读 worker 的概览契约也应只保留分页和关键词')
assert.match(repositorySource, /GROUP BY database_role, table_name/, '概览总数必须按唯一表键计算')
assert.match(repositorySource, /LIMIT \? OFFSET \?/, '概览表行必须在存储层分页')
assert.match(tableHistoryFunctionSource, /SELECT \$\{tableStorageHistorySelectColumns\(\)\}/, '单表趋势必须使用独立窄投影')
assert.match(repositorySource, /function tableStorageHistorySelectColumns[\s\S]*'sampled_at'[\s\S]*'row_count'[\s\S]*'total_bytes'/, '单表趋势投影只应包含图表需要的三列')
assert.doesNotMatch(tableHistoryFunctionSource, /table_kind|parent_table_name|index_bytes|growth_bytes|page_count|index_count/, '单表趋势不得读取完整表快照字段')
assert.match(readWorkerTypesSource, /list_table_storage_history_read_only' \} \? TableStorageHistoryPoint\[\]/, 'SQLite read worker 也必须返回单表趋势窄 DTO')
assert.doesNotMatch(cleanupReceiptSource, /deletedRows|deletedFiles|batches|batchSize|maxBatches|hasMore/, '异步清理 HTTP 回执不得返回同步清理统计')
assert.doesNotMatch(routesSource.slice(routesSource.indexOf('const nonBusinessDataCleanupSchema'), routesSource.indexOf('interface NonBusinessDataCleanupReceipt')), /batchSize|maxBatches/, '清理请求体只能携带用户选择的截止时间')
assert.match(routesSource, /maxHistoryPointsPerSeries = 2000/, '历史窗口必须有明确的逐序列最大点数')

console.log('表监控渐进接口回归通过：概览、窄历史和异步清理回执边界明确')
