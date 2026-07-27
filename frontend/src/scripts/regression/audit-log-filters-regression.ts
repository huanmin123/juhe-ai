import fs from 'node:fs'

import { auditLogListParams } from '@/views/audit-logs/auditLogFilters'

const pageState = { current: 1, pageSize: 100 }
const baseFilters = {
  accountIdFilter: '',
  conversationKeyFilter: '',
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
  ...baseFilters,
  systemAccountFilter: 'all',
  traceIdFilter: ' trace-example '
}, pageState)
assert(traceParams.systemAccountId === undefined, '跨用户 trace 查询不应隐式增加用户范围')
assert(traceParams.traceId === 'trace-example', 'traceId 应去除首尾空白后下发')

const viewSource = fs.readFileSync(new URL('../../views/audit-logs/AuditLogsView.vue', import.meta.url), 'utf8')
assert(!viewSource.includes('emptyAuditLogListResult'), '审计列表不应在未选择用户时返回前端空结果')
assert(viewSource.includes('systemAccountFilter: allSystemAccountsValue'), '审计列表应默认选择全部系统账户')

console.log('审计日志筛选回归通过：全局列表和跨用户 traceId 查询均会请求后端')

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
