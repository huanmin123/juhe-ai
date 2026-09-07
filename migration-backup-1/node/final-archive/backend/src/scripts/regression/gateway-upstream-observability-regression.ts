import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { readFile } from 'node:fs/promises'
import type { Request, Response } from 'express'
import type { Logger } from 'pino'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const {
  getRequestContext,
  requestContextMiddleware
} = await import('../../shared/request-context.js')
const metrics = await import('../../shared/prometheus-metrics.js')
const { sendGatewayFailureResponse } = await import('../../modules/gateway/response/failure-response.js')
const { gatewayErrorPayload } = await import('../../modules/gateway/response/responses.js')
const { prepareUpstreamResponseForDownstream } = await import('../../modules/gateway/response/downstream-headers.js')

class MockResponse extends EventEmitter {
  locals: Record<string, unknown> = {}
  destroyed = false
  writableEnded = false
  writableFinished = false
  headersSent = false
  statusCode = 200
  body: unknown
  private readonly headers = new Map<string, string | number | readonly string[]>()

  status(statusCode: number): this {
    this.statusCode = statusCode
    return this
  }

  json(body: unknown): this {
    this.body = body
    this.headersSent = true
    this.writableEnded = true
    return this
  }

  setHeader(name: string, value: string | number | readonly string[]): this {
    this.headers.set(name.toLowerCase(), value)
    return this
  }

  hasHeader(name: string): boolean {
    return this.headers.has(name.toLowerCase())
  }

  getHeaders(): Record<string, string | number | readonly string[]> {
    return Object.fromEntries(this.headers)
  }
}

metrics.resetPrometheusMetricsForTest()
const completionLogs: Array<{ level: string; fields: Record<string, unknown> }> = []
const completionLogger = {
  info(fields: Record<string, unknown>) {
    completionLogs.push({ level: 'info', fields })
  },
  warn(fields: Record<string, unknown>) {
    completionLogs.push({ level: 'warn', fields })
  },
  error(fields: Record<string, unknown>) {
    completionLogs.push({ level: 'error', fields })
  },
  debug(fields: Record<string, unknown>) {
    completionLogs.push({ level: 'debug', fields })
  }
} as unknown as Logger
const request = {
  body: { model: 'gpt-5.6-terra', stream: false },
  headers: {},
  method: 'POST',
  path: '/v1/responses',
  originalUrl: '/v1/responses',
  ip: '127.0.0.1',
  socket: { remoteAddress: '127.0.0.1' },
  header: () => undefined
} as unknown as Request
const response = new MockResponse() as unknown as Response
let submitted: Promise<void> | undefined

requestContextMiddleware(request, response, () => {
  const context = getRequestContext()
  assert(context, '网关失败响应前必须建立请求上下文')
  context.logger = completionLogger
  submitted = sendGatewayFailureResponse({
    req: request,
    res: response,
    auditCapture: { finalize: () => undefined } as unknown as AuditCaptureContext,
    usageContext: {
      traceId: context.traceId,
      trafficSource: 'gateway',
      systemAccountId: 'sys_upstream_observability',
      apiKeyId: 'key_upstream_observability',
      groupId: 'group_upstream_observability',
      endpoint: '/v1/responses',
      requestSnapshot: {
        method: 'POST',
        path: '/v1/responses',
        originalUrl: '/v1/responses',
        traceId: context.traceId,
        headers: {}
      }
    },
    startedAt: Date.now() - 100,
    statusCode: 503,
    responsePayload: gatewayErrorPayload('上游暂时不可用，请重试', 'service_unavailable'),
    audit: {
      outcome: 'upstream_failed',
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable'
    },
    recordUsage: false
  })
})

assert(submitted, '上游失败响应必须在请求上下文中提交')
await submitted
response.emit('finish')

assert.equal(
  completionLogs.filter((item) => item.level === 'error').length,
  0,
  '终态上游 503 不得记为网关 error 日志'
)
const completedLog = completionLogs.find((item) => item.fields.event === 'http_request_completed')
assert.equal(completedLog?.level, 'warn', '终态上游 503 必须保留为 warn 级诊断')
assert.equal(completedLog?.fields.failureScope, 'upstream', '完成日志必须标注有界上游失败来源')
assert.match(
  metrics.renderPrometheusMetrics(),
  /juhe_ai_http_requests_total\{[^}]*failure_scope="upstream"[^}]*outcome="completed"[^}]*status_class="5xx"[^}]*\} 1/
)

for (const statusCode of [502, 503, 504]) {
  metrics.resetPrometheusMetricsForTest()
  completionLogs.length = 0
  const passthroughResponse = new MockResponse() as unknown as Response
  let passthroughSubmitted: Promise<void> | undefined
  requestContextMiddleware(request, passthroughResponse, () => {
    const context = getRequestContext()
    assert(context, '上游透传前必须建立请求上下文')
    context.logger = completionLogger
    prepareUpstreamResponseForDownstream(
      passthroughResponse,
      { status: statusCode, ok: false, headers: new Headers(), body: null },
      false
    )
    passthroughSubmitted = Promise.resolve()
  })
  assert(passthroughSubmitted, `上游 ${statusCode} 透传必须完成请求上下文提交`)
  await passthroughSubmitted
  passthroughResponse.emit('finish')
  assert.equal(completionLogs.filter((item) => item.level === 'error').length, 0, `上游透传 ${statusCode} 不得记为网关 error 日志`)
  const passthroughLog = completionLogs.find((item) => item.fields.event === 'http_request_completed')
  assert.equal(passthroughLog?.level, 'warn', `上游透传 ${statusCode} 必须保留为 warn 级诊断`)
  assert.equal(passthroughLog?.fields.failureScope, 'upstream', `上游透传 ${statusCode} 必须标记 upstream`)
  assert.match(
    metrics.renderPrometheusMetrics(),
    new RegExp(`juhe_ai_http_requests_total\\{[^}]*failure_scope="upstream"[^}]*outcome="completed"[^}]*status_class="5xx"[^}]*\\} 1`)
  )
}

const finalizationSource = await readFile(new URL('../../modules/gateway/response/finalization.ts', import.meta.url), 'utf8')
const nonStreamInspectionSource = await readFile(new URL('../../modules/gateway/response/non-stream-json-inspection.ts', import.meta.url), 'utf8')
for (const [source, expectedDirectResponses, name] of [
  [finalizationSource, 3, 'finalization'],
  [nonStreamInspectionSource, 2, 'non-stream inspection']
] as const) {
  const directResponses = source.match(/sendGatewayErrorResponse\(/g) ?? []
  const upstreamMarks = source.match(/markRequestHttpMetricFailureScope\('upstream'\)/g) ?? []
  assert.equal(directResponses.length, expectedDirectResponses, `${name} 上游终态响应数量变化时必须更新回归契约`)
  assert.equal(upstreamMarks.length, expectedDirectResponses, `${name} 的每个上游终态响应都必须标记为 upstream`)
}

console.log('gateway upstream observability regression passed')
