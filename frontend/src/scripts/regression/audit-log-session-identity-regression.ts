import { readFileSync } from 'node:fs'

import type { AuditLogListItem, AuditLogSummary } from '@/types/domain'
import { auditLogFilterCounts, auditLogListParams } from '@/views/audit-logs/auditLogFilters'
import {
  formatConversationKeyPreview,
  identityConflictText,
  sessionConfidenceText,
  sessionResolutionText
} from '@/views/audit-logs/auditLogFormatters'

const pageState = { current: 1, pageSize: 100 }
const conversationKey = '97b1757f4f57600c4b79928f46b09b6a512ad1ffd01bc706e04e17fc9f215fe1'
const params = auditLogListParams({
  accountIdFilter: '',
  conversationKeyFilter: ` ${conversationKey} `,
  outcomeFilter: 'all',
  pathFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: 'all',
  traceIdFilter: '',
  trafficSourceFilter: 'all'
}, pageState)

assert(params.conversationKey === conversationKey, 'conversationKey 必须去除首尾空白后按完整值精确下发')
assert(!('traceId' in params) || params.traceId === undefined, '会话筛选不能伪造成 traceId 查询')
assert(auditLogFilterCounts({
  accountIdFilter: '',
  conversationKeyFilter: conversationKey,
  outcomeFilter: 'all',
  pathFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: 'all',
  traceIdFilter: '',
  trafficSourceFilter: 'all'
}).active === 1, 'conversationKey 必须计入当前筛选状态')

const summary: AuditLogSummary = {
  id: 'audit_1',
  traceId: 'trace_1',
  conversationKey,
  sessionNamespace: 'openai.codex.session',
  sessionSource: 'header:session-id',
  sessionResolution: 'official',
  sessionConfidence: 'authoritative',
  threadKey: 'thread_hmac',
  turnKey: 'turn_hmac',
  agentKey: 'agent_hmac',
  parentResponseKey: 'response_hmac',
  identityConflict: false,
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
assert(listItem.conversationKey === conversationKey, '审计列表项必须包含会话身份字段')
assert(formatConversationKeyPreview(conversationKey) === '97b1757f...9f215fe1', '列表必须短显示 HMAC，避免宽表泄漏完整值')
assert(sessionResolutionText('official') === '官方会话', '官方会话解析结果必须有明确展示')
assert(sessionConfidenceText('authoritative') === '权威', '会话置信度必须有明确展示')
assert(identityConflictText(undefined) === '-', '旧审计记录缺少冲突状态时必须保持未知而非伪造否')

const listSource = source('../../views/audit-logs/AuditLogList.vue')
const viewSource = source('../../views/audit-logs/AuditLogsView.vue')
const detailSource = source('../../views/audit-logs/AuditLogDetailDrawer.vue')
const filterFormSource = source('../../views/audit-logs/AuditLogFilterForm.vue')

assert(listSource.includes("column.key === 'session'"), '审计列表必须提供会话列')
assert(listSource.includes("emit('filter-conversation', record.conversationKey)"), '短会话 Key 必须可以一键筛选')
assert(viewSource.includes('@filter-conversation="filterConversation"'), '审计页面必须接收会话快速筛选事件')
assert(viewSource.includes('conversationKeyFilter.value = normalizedConversationKey'), '会话快速筛选必须写入独立筛选字段')
assert(viewSource.includes("traceIdFilter.value = ''"), '会话快速筛选必须移除单请求 traceId 限制')
assert(viewSource.includes("viewMode.value = 'list'"), '从最近内容搜索点击会话时必须切回可查询的审计列表')
assert(filterFormSource.includes('完整 conversationKey（精确匹配）'), '高级筛选必须明确会话 Key 是完整精确匹配')
for (const label of ['会话 Key', '线程 Key', '轮次 Key', 'Agent Key', '父响应 Key']) {
  assert(detailSource.includes(`label="${label}"`), `审计详情必须显示${label}`)
}

console.log('审计会话身份前端回归通过：精确查询、快速筛选、HMAC 详情与层级字段均已接线')

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message)
}
