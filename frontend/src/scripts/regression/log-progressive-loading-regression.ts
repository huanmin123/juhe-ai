import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

function source(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8')
}

// 运行日志页与 F1 索引已于 2026-11-09 下架；本回归只保留审计日志的按需加载契约。
const auditViewSource = source('../../views/audit-logs/AuditLogsView.vue')
const auditModeBridgeSource = source('../../views/audit-logs/useAuditLogModeBridge.ts')
const auditPayloadStateSource = source('../../views/audit-logs/useAuditLogDetailPayload.ts')
const auditDetailDrawerSource = source('../../views/audit-logs/AuditLogDetailDrawer.vue')
const logsApiSource = source('../../api/domains/logs.ts')
const auditDetailSource = source('../../../../backend-go/projects/gateway/internal/logreads/audit_detail.go')

const auditFetchPageSource = auditViewSource.match(/fetchPage: async[\s\S]*?requestSignature:/)?.[0] ?? ''
assert.doesNotMatch(auditFetchPageSource, /refreshAuditRuntimeQuietly/, '审计列表和翻页不得连带请求完整运行态')
assert.doesNotMatch(auditModeBridgeSource, /refreshAuditRuntimeQuietly/, '审计列表刷新和模式切换不得连带请求完整运行态')
assert.doesNotMatch(
  auditViewSource,
  /auditLogs\.runtime|scheduleAuditRuntimeRefresh|refreshAuditRuntimeQuietly|useAuditLogRuntimeAlert/,
  '审计页面无交互时不得请求非首屏必要的完整运行态'
)

const openAuditDetailSource = auditPayloadStateSource.match(/async function openDetail[\s\S]*?async function loadPayload/)?.[0] ?? ''
assert.match(openAuditDetailSource, /payloadRequestId \+= 1/, '切换审计详情时必须作废上一条记录的 payload 请求')
assert.match(openAuditDetailSource, /detail\.value = undefined/, '加载新审计详情时必须立即隐藏旧详情，避免旧 payload 操作串页')
assert.match(openAuditDetailSource, /detail\.value = \{ \.\.\.record, \.\.\.nextDetail \}/, '详情响应必须作为 supplement 与当前列表行合并，不能丢失列表已有展示字段')
assert.doesNotMatch(auditPayloadStateSource, /loadNextPayloadWindow|auditPayloadReadWindowBytes/, '前端不得保留审计正文分段加载状态')
assert.doesNotMatch(auditDetailDrawerSource, /加载下一段|load-next-payload/, '审计详情不得显示下一段交互')
assert.match(logsApiSource, /payload: \(id: string, payloadId: string\)/, '审计 payload API 不应再暴露 offset/limit 参数')
assert.match(logsApiSource, /detail: \(id: string\) => unwrap<AuditLogDetailSupplement>/, '审计详情 API 必须声明为列表行补充字段，不能伪装成完整详情')
assert.doesNotMatch(logsApiSource, /auditLogsApi[\s\S]{0,500}runtime:/, '前端不得保留无人使用且容易重新接入首屏的审计运行态 API')

const auditPayloadHandlerSource = auditDetailSource.match(/func \(d \*ReadsDeps\) handleGetAuditLogPayload[\s\S]*?\n\}/)?.[0] ?? ''
assert.match(
  auditPayloadHandlerSource,
  /GetAuditLogPayload\(r\.Context\(\), r\.PathValue\("id"\), r\.PathValue\("payloadId"\)\)/,
  '管理员审计 payload 接口必须只按日志与 payload 定位读取'
)
assert.doesNotMatch(auditPayloadHandlerSource, /offset|limit|Query\(\)/, '管理员审计 payload 接口不得继续接受窗口参数')
const auditPayloadReaderSource = auditDetailSource.slice(
  auditDetailSource.indexOf('func (s *auditLogSQLReader) GetAuditLogPayload'),
  auditDetailSource.indexOf('// auditBlobWindow mirrors')
)
assert.match(auditPayloadReaderSource, /readBlobWindow\(ctx, bodyBlobID, 0, 0, true\)/, '管理员审计 payload 接口必须一次返回完整正文')

console.log('审计日志按需加载回归通过：列表/翻页不请求完整运行态，payload 一次完整读取')
