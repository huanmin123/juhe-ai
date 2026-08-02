import fs from 'node:fs'

import {
  auditLogFilterCounts,
  auditLogListParams,
  filterLegacyClientAbortedAuditRows
} from '@/views/audit-logs/auditLogFilters'

const pageState = { current: 1, pageSize: 100 }
const baseFilters = {
  accountIdFilter: '',
  outcomeFilter: 'all' as const,
  pathFilter: '',
  sessionIdFilter: '',
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
  sessionIdFilter: 'session-hidden-by-direct-trace',
  systemAccountFilter: 'user-hidden-by-direct-trace',
  trafficSourceFilter: 'gateway',
  traceIdFilter: ' trace-example '
}, pageState)
assert(traceParams.systemAccountId === undefined, '跨用户 trace 查询不应隐式增加用户范围')
assert(traceParams.traceId === 'trace-example', 'traceId 应去除首尾空白后下发')
assert(traceParams.accountId === undefined, 'traceId 直接定位不应叠加缓存的 AI 账户筛选')
assert(traceParams.outcome === undefined, 'traceId 直接定位不应叠加缓存的结果筛选')
assert(traceParams.path === undefined, 'traceId 直接定位不应叠加缓存的路径筛选')
assert(traceParams.sessionId === undefined, 'traceId 直接定位不应叠加缓存的 sessionId 筛选')
assert(traceParams.trafficSource === undefined, 'traceId 直接定位不应叠加缓存的来源筛选')

const sessionParams = auditLogListParams({
  ...baseFilters,
  systemAccountFilter: 'all',
  sessionIdFilter: ' session-example ',
  traceIdFilter: ''
}, pageState)
assert(sessionParams.sessionId === 'session-example', '会话 ID 应作为独立筛选参数下发')
assert(sessionParams.traceId === undefined, '会话 ID 筛选不得转换为 traceId')

const filterCounts = auditLogFilterCounts({
  ...baseFilters,
  systemAccountFilter: 'all',
  sessionIdFilter: 'session-example',
  traceIdFilter: 'trace-example'
})
assert(filterCounts.active === 1, 'traceId 直接定位时只能显示实际生效的 traceId 筛选')
assert(filterCounts.advanced === 0, 'traceId 直接定位时更多筛选不应计入筛选数量')

const legacyOutcomeParams = auditLogListParams({
  ...baseFilters,
  outcomeFilter: 'client_aborted',
  systemAccountFilter: 'all',
  traceIdFilter: ''
}, pageState)
assert(legacyOutcomeParams.outcome === 'all', '历史 client_aborted 筛选不得提交当前 Node 会静默忽略的 outcome 参数')
assert(
  filterLegacyClientAbortedAuditRows([
    { id: 'legacy', auditOutcome: 'client_aborted' },
    { id: 'current', auditOutcome: 'downstream_closed' }
  ] as never, 'client_aborted').map((item) => item.id).join(',') === 'legacy',
  '历史 client_aborted 必须在前端按原始终态筛选，不得与 downstream_closed 混淆'
)

const viewSource = fs.readFileSync(new URL('../../views/audit-logs/AuditLogsView.vue', import.meta.url), 'utf8')
const filterFormSource = fs.readFileSync(new URL('../../views/audit-logs/AuditLogFilterForm.vue', import.meta.url), 'utf8')
const formattersSource = fs.readFileSync(new URL('../../views/audit-logs/auditLogFormatters.ts', import.meta.url), 'utf8')
const outcomeOptionsSource = fs.readFileSync(new URL('../../views/audit-logs/auditLogTableColumns.ts', import.meta.url), 'utf8')
const modeBridgeSource = fs.readFileSync(new URL('../../views/audit-logs/useAuditLogModeBridge.ts', import.meta.url), 'utf8')
assert(!viewSource.includes('emptyAuditLogListResult'), '审计列表不应在未选择用户时返回前端空结果')
assert(viewSource.includes('systemAccountFilter: allSystemAccountsValue'), '审计列表应默认选择全部系统账户')
assert(viewSource.includes("{ label: '网关请求', value: 'gateway' }"), '审计来源筛选应保留网关请求')
assert(viewSource.includes("{ label: 'AI账户测试', value: 'manual_account_test' }"), '审计来源筛选应保留人工账户测试')
assert(viewSource.includes("{ label: '混合路由选型', value: 'hybrid_scoring' }"), '审计来源筛选应保留混合路由选型')
assert(viewSource.includes("{ label: '回答质量复核', value: 'hybrid_quality_scoring' }"), '审计来源筛选应保留回答质量复核')
for (const excludedSource of ['account_health_check', 'runtime_recovery_probe', 'cooldown_retest']) {
  assert(!viewSource.includes(`value: '${excludedSource}'`), `审计来源筛选不得暴露后台来源：${excludedSource}`)
}
assert((viewSource.match(/v-model:session-id="sessionIdFilter"/g) ?? []).length === 2, '桌面与移动端更多筛选均应绑定会话 ID')
assert(!viewSource.includes('v-model:trace-id="traceIdFilter"'), 'traceId 只能由顶部主搜索框查询，不应出现在更多筛选')
assert(filterFormSource.includes('label="会话 ID"'), '更多筛选表单应显示会话 ID')
assert(formattersSource.includes("client_aborted: '客户端中断（历史）'"), '旧 client_aborted 审计记录必须有明确标签')
assert(outcomeOptionsSource.includes("value: 'client_aborted'"), '旧 client_aborted 审计记录必须保留前端筛选入口')
assert(!filterFormSource.includes('label="traceId"'), '更多筛选表单不应重复显示 traceId')
assert(!filterFormSource.includes('客户端类型'), '更多筛选表单不应提供客户端类型')
assert(!filterFormSource.includes('HTTP 状态码'), '更多筛选表单不应提供 HTTP 状态码')
assert(filterFormSource.includes("(event: 'update:sessionId', value: string): void"), '更多筛选表单应声明会话 ID 更新事件')
assert(filterFormSource.includes("set: (value) => emit('update:sessionId', value)"), '更多筛选表单应回写会话 ID')
assert(modeBridgeSource.includes('input.traceIdFilter.value'), '列表顶部搜索框应绑定 traceId')
assert(!modeBridgeSource.includes('input.sessionIdFilter.value'), '列表顶部搜索框不得绑定会话 ID')
assert(modeBridgeSource.includes('输入完整 traceId，精确查找请求'), '列表顶部搜索提示应明确 traceId 语义')
assert(
  /watch\(traceIdFilter,[\s\S]*?if \(value\.trim\(\)\) clearFiltersForDirectTraceLookup\(\)[\s\S]*?immediate: true, flush: 'sync'/.test(viewSource),
  'traceId 首次恢复、输入、刷新和模式切换前必须同步清除界面中的其他筛选'
)
assert(
  /function clearFiltersForDirectTraceLookup[\s\S]*?pathFilter\.value = defaults\.pathFilter[\s\S]*?sessionIdFilter\.value = defaults\.sessionIdFilter/.test(viewSource),
  'traceId 直接定位必须清除会话 ID 等更多筛选'
)
assert(
  /function applyPageState[\s\S]*?pagination\.pageSize = state\.pagination\.pageSize\s*if \(traceIdFilter\.value\.trim\(\)\) clearFiltersForDirectTraceLookup\(\)/.test(viewSource),
  '从路由或页面缓存恢复 traceId 后必须在全部字段回填完成后再次归一化其他筛选'
)

console.log('审计日志筛选回归通过：traceId 顶部查询、会话 ID 更多筛选，以及直接定位语义均正确')

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
