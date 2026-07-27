import fs from 'node:fs'

import { auditLogListParams } from '@/views/audit-logs/auditLogFilters'

const pageState = { current: 1, pageSize: 100 }
const baseFilters = {
  accountIdFilter: '',
  outcomeFilter: 'all' as const,
  pathFilter: '',
  statusCodeFilter: '',
  trafficSourceFilter: 'all' as const
}

const globalParams = auditLogListParams({
  ...baseFilters,
  systemAccountFilter: 'all',
  traceIdFilter: ''
}, pageState)
assert(globalParams.systemAccountId === undefined, '全部系统账户不应下发 systemAccountId')

const traceParams = auditLogListParams({
  accountIdFilter: 'account-hidden-by-direct-trace',
  outcomeFilter: 'failure',
  pathFilter: '/v1/chat/completions',
  statusCodeFilter: '500',
  trafficSourceFilter: 'gateway',
  systemAccountFilter: 'user-hidden-by-direct-trace',
  traceIdFilter: ' trace-example '
}, pageState)
assert(traceParams.systemAccountId === undefined, '跨用户 trace 查询不应隐式增加用户范围')
assert(traceParams.traceId === 'trace-example', 'traceId 应去除首尾空白后下发')
assert(traceParams.accountId === undefined, 'traceId 直接定位不应叠加缓存的 AI 账户筛选')
assert(traceParams.outcome === undefined, 'traceId 直接定位不应叠加缓存的结果筛选')
assert(traceParams.path === undefined, 'traceId 直接定位不应叠加缓存的路径筛选')
assert(traceParams.statusCode === undefined, 'traceId 直接定位不应叠加缓存的状态码筛选')
assert(traceParams.trafficSource === undefined, 'traceId 直接定位不应叠加缓存的来源筛选')

const viewSource = fs.readFileSync(new URL('../../views/audit-logs/AuditLogsView.vue', import.meta.url), 'utf8')
assert(!viewSource.includes('emptyAuditLogListResult'), '审计列表不应在未选择用户时返回前端空结果')
assert(viewSource.includes('systemAccountFilter: allSystemAccountsValue'), '审计列表应默认选择全部系统账户')
assert(
  /watch\(traceIdFilter,[\s\S]*?if \(value\.trim\(\)\) clearAdvancedFiltersForTraceLookup\(\)[\s\S]*?immediate: true, flush: 'sync'/.test(viewSource),
  'traceId 首次恢复、输入、刷新和模式切换前必须同步清除界面高级筛选'
)
assert(
  /function applyPageState[\s\S]*?pagination\.pageSize = state\.pagination\.pageSize\s*if \(traceIdFilter\.value\.trim\(\)\) clearAdvancedFiltersForTraceLookup\(\)/.test(viewSource),
  '从路由或页面缓存恢复 traceId 后必须在全部字段回填完成后再次归一化高级筛选'
)

console.log('审计日志筛选回归通过：traceId 直接定位不会叠加缓存的高级筛选')

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
