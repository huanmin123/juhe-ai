import { readFileSync } from 'node:fs'

import type { AuditLogDetail, AuditLogListItem, AuditLogSummary } from '@/types/domain'
import { auditLogFilterCounts, auditLogListParams } from '@/views/audit-logs/auditLogFilters'
import {
  formatSessionIdPreview,
  sessionClientTypeText
} from '@/views/audit-logs/auditLogFormatters'

const pageState = { current: 1, pageSize: 100 }
const sessionId = '019bbd45-a2db-70e1-aeee-c5bed7b69d8c'
const conversationKey = '97b1757f4f57600c4b79928f46b09b6a512ad1ffd01bc706e04e17fc9f215fe1'
const params = auditLogListParams({
  accountIdFilter: '',
  outcomeFilter: 'all',
  pathFilter: '',
  sessionClientTypeFilter: ' codex ',
  sessionIdFilter: ` ${sessionId} `,
  statusCodeFilter: '',
  systemAccountFilter: 'all',
  traceIdFilter: '',
  trafficSourceFilter: 'all'
}, pageState)

assert(params.sessionId === sessionId, 'sessionId 必须去除首尾空白后按完整值精确下发')
assert(params.sessionClientType === 'codex', 'sessionClientType 必须去除首尾空白后精确下发')
assert(!('traceId' in params) || params.traceId === undefined, '会话筛选不能伪造成 traceId 查询')
assert(auditLogFilterCounts({
  accountIdFilter: '',
  outcomeFilter: 'all',
  pathFilter: '',
  sessionClientTypeFilter: '',
  sessionIdFilter: sessionId,
  statusCodeFilter: '',
  systemAccountFilter: 'all',
  traceIdFilter: '',
  trafficSourceFilter: 'all'
}).active === 1, 'sessionId 必须计入当前筛选状态')
const clientTypeCounts = auditLogFilterCounts({
  accountIdFilter: '',
  outcomeFilter: 'all',
  pathFilter: '',
  sessionClientTypeFilter: 'claude_code',
  sessionIdFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: 'all',
  traceIdFilter: '',
  trafficSourceFilter: 'all'
})
assert(clientTypeCounts.active === 1 && clientTypeCounts.advanced === 1, '客户端类型必须计入高级筛选状态')

const summary: AuditLogSummary = {
  id: 'audit_1',
  traceId: 'trace_1',
  sessionId,
  sessionClientType: 'codex',
  trafficSource: 'gateway',
  method: 'POST',
  path: '/v1/responses',
  stream: false,
  auditOutcome: 'success',
  success: true,
  sampleBucket: 1,
  sampleReason: 'success',
  attemptCount: 1,
  payloadCount: 0,
  rawPayloadBytes: 0,
  compressedPayloadBytes: 0,
  compressionSavedBytes: 0,
  captureStatus: 'complete',
  startedAt: '2026-07-27T00:00:00.000Z',
  endedAt: '2026-07-27T00:00:00.000Z',
  createdAt: '2026-07-27T00:00:00.000Z'
}
const listItem: AuditLogListItem = summary
const detail: AuditLogDetail = {
  ...summary,
  conversationKey,
  attempts: [],
  payloads: []
}
assert(listItem.sessionId === sessionId, '审计列表项必须包含原始 sessionId')
assert(listItem.sessionClientType === 'codex', '审计列表项必须包含识别 sessionId 的客户端类型')
assert(detail.conversationKey === conversationKey, '内部 conversationKey 必须只保留在详情模型用于诊断')
assert(formatSessionIdPreview(sessionId) === '019bbd45...d7b69d8c', '列表应紧凑展示长 sessionId')
assert(sessionClientTypeText('codex') === 'Codex', '客户端类型必须有明确展示')

const listSource = source('../../views/audit-logs/AuditLogList.vue')
const viewSource = source('../../views/audit-logs/AuditLogsView.vue')
const detailSource = source('../../views/audit-logs/AuditLogDetailDrawer.vue')
const filterFormSource = source('../../views/audit-logs/AuditLogFilterForm.vue')

assert(listSource.includes("column.key === 'session'"), '审计列表必须提供会话列')
assert(listSource.includes("emit('filter-session', record.sessionId)"), '会话 ID 必须可以一键精确筛选')
assert(viewSource.includes('@filter-session="filterSession"'), '审计页面必须接收会话快速筛选事件')
assert(viewSource.includes('applyPageState({ ...defaultAuditLogsPageState(), sessionIdFilter: normalizedSessionId })'), '会话快速筛选必须清空其它限制并只保留 sessionId，确保返回全部请求')
assert(viewSource.includes('输入完整会话 ID，精确查找全部请求') || source('../../views/audit-logs/useAuditLogModeBridge.ts').includes('输入完整会话 ID，精确查找全部请求'), '主搜索入口必须明确按完整 sessionId 精确查询')
assert(filterFormSource.includes('客户端类型'), '高级筛选必须提供客户端类型')
assert(filterFormSource.includes(':options="sessionClientTypeOptions"'), '客户端类型必须使用固定选择器而非自由输入')
assert(filterFormSource.includes("{ label: 'Codex', value: 'codex' }"), '客户端类型选择器必须支持 Codex')
assert(filterFormSource.includes("{ label: 'Claude Code', value: 'claude_code' }"), '客户端类型选择器必须支持 Claude Code')
assert(detailSource.includes('label="会话 ID"'), '审计详情必须显示会话 ID')
assert(detailSource.includes('label="客户端类型"'), '审计详情必须显示客户端类型')
assert(detailSource.includes('label="内部会话 Key"'), 'conversationKey 只能作为内部诊断字段展示')
for (const removedField of ['线程 Key', '轮次 Key', 'Agent Key', '父响应 Key', '会话归一化']) {
  assert(!detailSource.includes(removedField), `审计详情不得继续展示${removedField}`)
}

console.log('审计会话身份前端回归通过：原始 sessionId 精确查询、客户端类型和内部诊断字段均已接线')

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
