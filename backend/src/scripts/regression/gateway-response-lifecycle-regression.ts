import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import type { NextFunction, Request, Response } from 'express'
import type { Logger } from 'pino'
import type { AuditCaptureContext } from '../../modules/gateway/audit/capture.service.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'
import type { RequestContext } from '../../shared/request-context.js'

process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'ingest-worker'
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_QUEUE_DRIVER = 'memory'

const { captureGatewayRawBody } = await import('../../modules/gateway/request/body-middleware.js')
const {
  clearGatewayRequestBodyInFlightForTest,
  getGatewayRequestBodyInFlightState
} = await import('../../modules/gateway/request/body.js')
const { attachAccountSlotRelease } = await import('../../modules/gateway/routes.js')
const { createAuditCapture, observeGatewayHttpCompletion } = await import('../../modules/gateway/audit/capture.service.js')
const { withRequestContext } = await import('../../shared/request-context.js')
const { sendGatewayFailureResponse } = await import('../../modules/gateway/response/failure-response.js')
const { gatewayErrorPayload } = await import('../../modules/gateway/response/responses.js')
const auditLogQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const usageRecordQueue = await import('../../modules/gateway/usage/record-queue.service.js')
const failureUsageFinalization = await import('../../modules/gateway/usage/failure-finalization.service.js')

class MockResponse extends EventEmitter {
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

  getHeader(name: string): string | number | readonly string[] | undefined {
    return this.headers.get(name.toLowerCase())
  }

  getHeaders(): Record<string, string | number | readonly string[]> {
    return Object.fromEntries(this.headers)
  }
}

clearGatewayRequestBodyInFlightForTest()

const rawBody = Buffer.from(JSON.stringify({ model: 'gpt-5.6-sol', input: 'lease lifecycle' }), 'utf8')
const request = Object.assign(new EventEmitter(), {
  body: rawBody,
  headers: { 'content-type': 'application/json' },
  method: 'POST',
  path: '/responses',
  originalUrl: '/v1/responses',
  aborted: false
}) as unknown as GatewayRawBodyRequest
const response = new MockResponse() as unknown as Response
let nextCalled = false
let bytesObservedInsideHandler = 0

await captureGatewayRawBody(request, response, (() => {
  nextCalled = true
  bytesObservedInsideHandler = getGatewayRequestBodyInFlightState().currentBytes
}) as NextFunction)

assert.equal(nextCalled, true, '合法请求体应进入网关业务处理')
assert.equal(bytesObservedInsideHandler, rawBody.byteLength, '进入业务处理时请求体 lease 必须仍然有效')
assert.equal(getGatewayRequestBodyInFlightState().currentBytes, rawBody.byteLength, '业务处理中不得提前释放请求体 lease')

response.emit('finish')
assert.equal(getGatewayRequestBodyInFlightState().currentBytes, 0, 'HTTP finish 后应释放请求体 lease')
assert.equal(getGatewayRequestBodyInFlightState().requestCount, 0, 'HTTP finish 后请求体 lease 计数应归零')

const accountResponse = new MockResponse() as unknown as Response
let accountReleaseCount = 0
const releaseAccountSlot = attachAccountSlotRelease(accountResponse, () => {
  accountReleaseCount += 1
})
accountResponse.emit('finish')
assert.equal(accountReleaseCount, 1, 'HTTP finish 应立即释放账户并发槽')
releaseAccountSlot()
accountResponse.emit('close')
assert.equal(accountReleaseCount, 1, '账户并发槽释放必须幂等')

const timingResponse = new MockResponse() as unknown as Response
const httpCompletion = observeGatewayHttpCompletion(timingResponse)
assert.equal(observeGatewayHttpCompletion(timingResponse), httpCompletion, '同一响应必须复用同一个 HTTP 完成观察器')
timingResponse.emit('finish')
const observedCompletedAtMs = httpCompletion.completedAtMs()
await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 10))
assert.equal(await httpCompletion.wait(), observedCompletedAtMs, '监听后再等待必须复用真实 HTTP finish 时间，不能混入后置副作用耗时')

const auditStageEvents: Record<string, unknown>[] = []
const auditStageLogger = {
  info(fields: Record<string, unknown>) {
    if (fields.event === 'gateway.request.stage' && fields.stage === 'audit.finalize') {
      auditStageEvents.push(fields)
    }
  }
} as unknown as Logger
const auditResponse = new MockResponse() as unknown as Response
const auditContextStartedAt = performance.now()
const auditRequestContext: RequestContext = {
  traceId: 'trace-audit-finalize-http-boundary',
  startedAt: Date.now(),
  monotonicStartedAt: auditContextStartedAt,
  method: 'POST',
  path: '/v1/responses',
  originalUrl: '/v1/responses',
  logger: auditStageLogger
}
withRequestContext(auditRequestContext, () => {
  const delayedAuditCapture = createAuditCapture({
    req: {
      body: { model: 'gpt-5.6-sol', stream: false },
      headers: {},
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      header: () => undefined
    } as unknown as Request,
    res: auditResponse,
    traceId: auditRequestContext.traceId,
    startedAtMs: auditRequestContext.startedAt,
    captureMode: 'metadata_only'
  })
  delayedAuditCapture.finalize({ outcome: 'success', success: true, statusCode: 200 })
})
await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 60))
let auditHttpFinishOffsetMs = 0
withRequestContext(auditRequestContext, () => {
  auditHttpFinishOffsetMs = performance.now() - auditContextStartedAt
  auditResponse.emit('finish')
})
assert.equal(auditStageEvents.length, 1, '延迟 HTTP finish 后必须且只能记录一次 audit.finalize 阶段')
const auditFinalizeStage = auditStageEvents[0]!
assert(
  Number(auditFinalizeStage.startedOffsetMs) >= auditHttpFinishOffsetMs - 2,
  'audit.finalize 必须在真实 HTTP finish 后才开始计时，不能把客户端发送等待计入阶段耗时'
)
assert(
  Number(auditFinalizeStage.durationMs)
    <= Number(auditFinalizeStage.endedOffsetMs) - auditHttpFinishOffsetMs + 2,
  'audit.finalize 阶段耗时只能覆盖组装与入队，不能包含延迟的 HTTP completion 等待'
)
auditLogQueue.clearAuditLogQueueForTest()

usageRecordQueue.clearUsageRecordQueueForTest()
const failureRequest = {
  body: { model: 'gpt-5.6-sol', stream: false },
  headers: {},
  method: 'POST',
  path: '/responses',
  originalUrl: '/v1/responses',
  header: () => undefined
} as unknown as Request
const failureResponse = new MockResponse() as unknown as Response
const finalizeOrder: string[] = []
const auditCapture = {
  finalize: () => {
    finalizeOrder.push((failureResponse as unknown as MockResponse).body ? 'response_sent_before_audit_finalize' : 'audit_finalized_before_response')
  }
} as unknown as AuditCaptureContext
const failureStartedAt = Date.now() - 250

await sendGatewayFailureResponse({
  req: failureRequest,
  res: failureResponse,
  auditCapture,
  usageContext: {
    traceId: 'trace-failure-response-lifecycle',
    trafficSource: 'gateway',
    systemAccountId: 'sys_failure_response_lifecycle',
    apiKeyId: 'key_failure_response_lifecycle',
    groupId: 'group_failure_response_lifecycle',
    endpoint: '/v1/responses',
    requestSnapshot: {
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      traceId: 'trace-failure-response-lifecycle',
      headers: {}
    }
  },
  startedAt: failureStartedAt,
  statusCode: 503,
  responsePayload: gatewayErrorPayload('上游暂时不可用，请重试', 'service_unavailable'),
  audit: {
    outcome: 'upstream_failed',
    errorPhase: 'dispatch',
    errorCode: 'service_unavailable'
  }
})

assert.equal((failureResponse as unknown as MockResponse).statusCode, 503, '失败响应必须先写给客户端')
assert.deepEqual(finalizeOrder, ['response_sent_before_audit_finalize'], '审计 finalize 必须发生在错误响应写出之后')
assert.equal(usageRecordQueue.pendingUsageRecordCount(), 0, 'HTTP 尚未 finish/close 时不得提前写入失败使用记录')
assert.equal(failureUsageFinalization.getPendingGatewayFailureUsageFinalizationCount(), 1, '等待 HTTP finish 的失败 usage 收尾必须登记到统一 pending 集合')
await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 20))
assert.equal(usageRecordQueue.pendingUsageRecordCount(), 0, '使用记录异步收尾等待不得反向阻塞或延迟客户端错误响应')

const finishWindowStart = Date.now()
failureResponse.emit('finish')
const finishWindowEnd = Date.now()
await waitFor(() => usageRecordQueue.pendingUsageRecordCount() === 1)
assert.equal(await failureUsageFinalization.waitForGatewayFailureUsageFinalizationsIdle(2_000), true, 'HTTP finish 后失败 usage 收尾应在有界时间内排空')
assert.equal(failureUsageFinalization.getPendingGatewayFailureUsageFinalizationCount(), 0, '失败 usage 收尾完成后必须清理 pending 登记')
const queuedFailureUsage = usageRecordQueue.peekPendingUsageRecordForTest()
assert(queuedFailureUsage, 'HTTP finish 后应异步投递失败使用记录')
const usageCompletedAtMs = Date.parse(queuedFailureUsage.createdAt ?? '')
assert(
  usageCompletedAtMs >= finishWindowStart && usageCompletedAtMs <= finishWindowEnd,
  '失败使用记录 completedAt 必须取真实 HTTP finish/close 时间'
)
assert.equal(
  queuedFailureUsage.durationMs,
  usageCompletedAtMs - failureStartedAt,
  '失败使用记录 duration 必须截止到同一次 HTTP finish/close'
)
usageRecordQueue.clearUsageRecordQueueForTest()

let releaseBoundedUsageTasks: (() => void) | undefined
let startedBoundedUsageTasks = 0
const boundedUsageGate = new Promise<void>((resolvePromise) => {
  releaseBoundedUsageTasks = resolvePromise
})
let acceptedBoundedUsageTasks = 0
for (let index = 0; index < 2_081; index += 1) {
  if (failureUsageFinalization.dispatchGatewayUsageFinalization({
    taskFactory: async () => {
      startedBoundedUsageTasks += 1
      await boundedUsageGate
    },
    bytes: 1
  })) {
    acceptedBoundedUsageTasks += 1
  }
}
assert.equal(acceptedBoundedUsageTasks, 2_080, 'usage 异步收尾必须在数量上限处拒绝多余投递')
await new Promise<void>((resolvePromise) => setImmediate(resolvePromise))
assert.equal(startedBoundedUsageTasks, 32, 'usage 异步收尾并发必须受上限保护')
releaseBoundedUsageTasks?.()
assert.equal(await failureUsageFinalization.waitForGatewayFailureUsageFinalizationsIdle(2_000), true, '有界 usage 收尾队列应可排空')
const usageFinalizationRuntime = failureUsageFinalization.getGatewayUsageFinalizationRuntime()
assert.equal(usageFinalizationRuntime.droppedCount, 1, '有界 usage 收尾溢出必须暴露累计 dropped 计数')
assert.equal(usageFinalizationRuntime.pendingCount, 0, '有界 usage 收尾排空后运行态 pending 必须归零')
assert.equal(usageFinalizationRuntime.queuedBytes, 0, '有界 usage 收尾排空后运行态 queued bytes 必须归零')

clearGatewayRequestBodyInFlightForTest()
console.log('网关响应生命周期回归通过：错误响应先返回，usage 异步收尾，lease、并发槽和耗时统一以 HTTP finish/close 为边界')

async function waitFor(predicate: () => boolean): Promise<void> {
  const deadline = Date.now() + 2_000
  while (!predicate()) {
    assert(Date.now() < deadline, '等待失败使用记录异步入队超时')
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
}
