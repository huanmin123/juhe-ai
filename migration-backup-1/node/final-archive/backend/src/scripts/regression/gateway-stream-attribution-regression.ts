import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const gatewayRoot = resolve(import.meta.dirname, '../..')
const routesSource = readFileSync(resolve(gatewayRoot, 'modules/gateway/routes.ts'), 'utf8')
const formatterSource = readFileSync(resolve(gatewayRoot, '../../frontend/src/views/audit-logs/auditLogFormatters.ts'), 'utf8')
const listSource = readFileSync(resolve(gatewayRoot, '../../frontend/src/views/audit-logs/AuditLogList.vue'), 'utf8')

const preCommitFunction = routesSource.slice(
  routesSource.indexOf('async function sendPreCommitStreamRetryExhaustedResponse'),
  routesSource.indexOf('async function rememberCodexTurnFailureWhenClientRetryIsVisible')
)
assert.match(preCommitFunction, /statusCode: 503,[\s\S]*?outcome: 'stream_failed'/, '预提交流式失败必须走 503 普通错误响应并保留流失败审计')
assert.match(preCommitFunction, /preCommitFailureSignal !== 'protocol_error_event'[\s\S]*?statusCode: 503,[\s\S]*?return/, '普通客户端的预提交流式失败必须走 503 响应')
assert.match(preCommitFunction, /preCommitFailureSignal === 'protocol_error_event'|preCommitFailureSignal !== 'protocol_error_event'[\s\S]*?if \(!input\.res\.headersSent\)[\s\S]*?input\.res\.status\(200\)/, '仅明确要求协议错误终态的客户端才允许以 SSE 写出可重试错误')

const { createAuditCapture, resolveAuditFinalization } = await import('../../modules/gateway/audit/capture.service.js')

const rootFailure = resolveAuditFinalization({
  outcome: 'stream_failed',
  success: false,
  errorPhase: 'stream',
  errorCode: 'upstream_retryable_error',
  errorMessage: '上游在首个可见输出前失败'
}, true, false)
assert.deepEqual(rootFailure, {
  outcome: 'stream_failed',
  success: false,
  errorPhase: 'stream',
  errorCode: 'upstream_retryable_error',
  errorMessage: '上游在首个可见输出前失败'
}, '下游关闭不能覆盖既有流失败根因')

const closeOnly = resolveAuditFinalization({ outcome: 'success', success: true }, true, false)
assert.deepEqual(closeOnly, {
  outcome: 'downstream_closed',
  success: false,
  errorPhase: 'downstream',
  errorCode: 'downstream_connection_closed',
  errorMessage: '下游连接关闭'
}, '无既有根因的下游关闭必须记录为中性终态')

const retryThenClose = resolveAuditFinalization({
  outcome: 'downstream_closed',
  success: false,
  errorPhase: 'downstream',
  errorCode: 'downstream_connection_closed',
  errorMessage: '下游连接关闭'
}, true, true, {
  errorPhase: 'stream',
  errorCode: 'upstream_stream_interrupted',
  errorMessage: '上游流式响应 30s 内未返回任何新数据'
})
assert.deepEqual(retryThenClose, {
  outcome: 'stream_failed',
  success: false,
  errorPhase: 'stream',
  errorCode: 'upstream_stream_interrupted',
  errorMessage: '上游流式响应 30s 内未返回任何新数据'
}, '先发生的上游流失败不能被后续下游关闭覆盖')

const capture = createAuditCapture({
  req: {
    body: { model: 'gpt-5.6-sol', stream: true },
    headers: {},
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    header: () => undefined
  } as never,
  traceId: 'trace-gateway-stream-attribution',
  startedAtMs: Date.now(),
  captureMode: 'metadata_only'
})
capture.markDownstreamClosed()
const payloads = (capture as unknown as { payloads: Array<{ body?: string }> }).payloads
const downstreamClosePayload = payloads
  .map((payload) => JSON.parse(payload.body ?? '{}'))
  .find((payload) => payload.label === 'downstream_connection_closed')
assert.deepEqual(downstreamClosePayload, {
  type: 'gateway_metadata',
  label: 'downstream_connection_closed',
  metadata: {}
}, '下游关闭元信息只能保留统一事件')
capture.cancel()

assert.match(formatterSource, /downstream_closed: '下游连接关闭'/, '新下游关闭终态必须使用统一文案')
assert.doesNotMatch(formatterSource, /触发方未识别|下游连接关闭（历史记录）/, '下游关闭展示不得附带内部归因分类')
assert.match(formatterSource, /if \(!success\) return '失败'/, '失败记录不得把 HTTP 200 显示为成功状态码')
assert.match(formatterSource, /return success \? String\(statusCode\) : `HTTP \$\{statusCode\}（头已提交）`/, 'HTTP 200 只能作为详情中的已提交传输状态保留')
assert.match(listSource, /statusText\(record\.finalStatusCode, record\.success\)/, '审计列表必须使用语义状态展示')

console.log('网关流式失败与下游关闭归因回归通过')
