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
assert.match(preCommitFunction, /if \(!input\.res\.headersSent\) \{[\s\S]*?statusCode: 503,[\s\S]*?outcome: 'stream_failed'/, '预提交流式失败必须走 503 普通错误响应并保留流失败审计')
assert.doesNotMatch(preCommitFunction, /input\.res\.status\(200\)/, '预提交流式失败不得伪造 HTTP 200 SSE 成功响应')
assert.match(preCommitFunction, /if \(!input\.res\.headersSent\)[\s\S]*?return[\s\S]*?writeGatewayStreamFailureEvent/, '仅已提交响应才允许继续写协议 SSE 失败事件')

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
  errorMessage: '下游连接提前关闭，触发方未识别'
}, '无既有根因的下游关闭必须记录为中性终态')

const retryThenClose = resolveAuditFinalization({
  outcome: 'downstream_closed',
  success: false,
  errorPhase: 'downstream',
  errorCode: 'downstream_connection_closed',
  errorMessage: '下游连接提前关闭，触发方未识别'
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
  metadata: {
    trigger: 'unknown_unproven',
    clientActionConfirmed: false
  }
}, '下游关闭元信息不得归责客户端')
capture.cancel()

assert.match(formatterSource, /downstream_closed: '下游连接关闭（触发方未识别）'/, '新下游关闭终态必须使用中性文案')
assert.match(formatterSource, /client_aborted: '下游连接关闭（历史记录）'/, '历史 client_aborted 记录必须使用中性文案')
assert.match(formatterSource, /return success \? String\(statusCode\) : `\$\{statusCode\}（语义失败）`/, 'HTTP 200 的语义失败必须有可见失败标识')
assert.match(listSource, /statusText\(record\.finalStatusCode, record\.success\)/, '审计列表必须使用语义状态展示')

console.log('网关流式失败与下游关闭归因回归通过')
