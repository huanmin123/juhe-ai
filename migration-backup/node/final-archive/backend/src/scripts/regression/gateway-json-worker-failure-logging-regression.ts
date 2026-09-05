import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import type { Request, Response } from 'express'
import { gatewayRawBodyHardLimitBytes } from '../../modules/gateway/request/body.js'

const backendRoot = resolve(import.meta.dirname, '../../../')
const parserPath = resolve(backendRoot, 'dist/modules/gateway/request/json-parser.js')
const loggerPath = resolve(backendRoot, 'dist/shared/logger.js')
const requestContextPath = resolve(backendRoot, 'dist/shared/request-context.js')

assert(existsSync(parserPath), '先运行 pnpm build 生成真实 Node worker 测试所需的 dist 产物')

const [{ logger }, parserModule, {
  getRequestContext,
  requestContextMiddleware,
  withRequestContext
}] = await Promise.all([
  import(pathToFileURL(loggerPath).href) as Promise<typeof import('../../shared/logger.js')>,
  import(pathToFileURL(parserPath).href) as Promise<typeof import('../../modules/gateway/request/json-parser.js')>,
  import(pathToFileURL(requestContextPath).href) as Promise<typeof import('../../shared/request-context.js')>
])
const {
  canQueueGatewayJsonParsedBodyJobForTest,
  normalizeOpenAIOAuthCodexBodyInWorker,
  normalizeOpenAIOAuthCodexParsedBodyInWorker,
  parseGatewayJsonBodyInWorker,
  setGatewayJsonWorkerPoolSizeForTest,
  stopGatewayJsonParseWorker
} = parserModule

assert.equal(
  canQueueGatewayJsonParsedBodyJobForTest(gatewayRawBodyHardLimitBytes),
  true,
  '合法的 64MiB Body 必须能进入 parsed-body worker，队列上限需包含固定作业开销'
)

const capturedErrors: Array<Record<string, unknown>> = []
const capturedWarnings: Array<Record<string, unknown>> = []
const capturedInfos: Array<Record<string, unknown>> = []
const originalError = logger.error
const originalWarn = logger.warn
const originalInfo = logger.info
const originalChild = logger.child
let requestContextBindings: Record<string, unknown> | undefined
let requestStartedFields: Record<string, unknown> | undefined

try {
  logger.error = ((fields: Record<string, unknown>) => {
    capturedErrors.push(fields)
  }) as typeof logger.error
  logger.warn = ((fields: Record<string, unknown>) => {
    capturedWarnings.push(fields)
  }) as typeof logger.warn
  logger.info = ((fields: Record<string, unknown>) => {
    capturedInfos.push(fields)
  }) as typeof logger.info
  logger.child = ((bindings: Record<string, unknown>, options?: unknown) => {
    const child = originalChild.call(logger, bindings, options as never)
    requestContextBindings = bindings
    child.info = ((fields: Record<string, unknown>) => {
      if (fields.event === 'http_request_started') requestStartedFields = fields
    }) as typeof child.info
    return child
  }) as typeof logger.child

  await assert.rejects(
    parseGatewayJsonBodyInWorker(Buffer.from('{"invalid":', 'utf8')),
    /Unexpected end of JSON input/
  )
  assert.equal(
    capturedErrors.filter((event) => event.event === 'gateway_json_parse_worker_failed').length,
    0,
    '客户端提交无效 JSON 属于输入错误，不能记成 worker 基础设施故障'
  )

  const largeParsedBody = {
    model: 'gpt-regression',
    input: 'x'.repeat(512 * 1024),
    reasoning: { effort: 'low' }
  }
  const normalizedForFirstAccount = await normalizeOpenAIOAuthCodexParsedBodyInWorker(
    largeParsedBody,
    Buffer.byteLength(JSON.stringify(largeParsedBody)),
    {
      inputHeaders: {},
      account: {
        apiKey: 'regression-first-key',
        credentials: { reasoning_effort_override: 'high' }
      },
      identity: { systemAccountId: 'system-regression', groupId: 'group-regression' },
      compact: false,
      requestOverrideModelCapabilities: {
        supportedServiceTiers: [],
        supportedReasoningEfforts: ['high', 'medium']
      }
    }
  )
  const normalizedForSecondAccount = await normalizeOpenAIOAuthCodexParsedBodyInWorker(
    largeParsedBody,
    Buffer.byteLength(JSON.stringify(largeParsedBody)),
    {
      inputHeaders: {},
      account: {
        apiKey: 'regression-second-key',
        credentials: { reasoning_effort_override: 'medium' }
      },
      identity: { systemAccountId: 'system-regression', groupId: 'group-regression' },
      compact: false,
      requestOverrideModelCapabilities: {
        supportedServiceTiers: [],
        supportedReasoningEfforts: ['high', 'medium']
      }
    }
  )
  assert.equal((JSON.parse(normalizedForFirstAccount.body ?? '{}') as { reasoning?: { effort?: string } }).reasoning?.effort, 'high')
  assert.equal((JSON.parse(normalizedForSecondAccount.body ?? '{}') as { reasoning?: { effort?: string } }).reasoning?.effort, 'medium')
  assert.equal(largeParsedBody.reasoning.effort, 'low', '生产 worker 结构化克隆不得污染请求级解析对象')
  await assert.rejects(
    normalizeOpenAIOAuthCodexParsedBodyInWorker([], 2, {
      inputHeaders: {},
      account: { apiKey: 'regression-test-key' },
      identity: { systemAccountId: 'system-regression', groupId: 'group-regression' },
      compact: false
    }),
    (error: unknown) => error instanceof Error
      && (error as Error & { code?: string }).code === 'invalid_openai_oauth_codex_request'
  )
  const invalidJsonEvent = capturedInfos.find((event) => (
    event.event === 'gateway_json_worker_job_completed'
      && event.outcome === 'expected_failure'
      && event.reasonCode === 'invalid_json'
  ))
  assert(invalidJsonEvent, '无效 JSON 也必须保留可关联的 worker expected-failure info 现场')
  assert.match(String(invalidJsonEvent.jobId), /^gateway-json-worker:\d+$/)

  await assert.rejects(
    normalizeOpenAIOAuthCodexBodyInWorker(Buffer.from('{"invalid":', 'utf8'), {
      inputHeaders: {},
      account: { apiKey: 'regression-test-key' },
      identity: { systemAccountId: 'system-regression', groupId: 'group-regression' },
      compact: false
    }),
    (error: unknown) => (
      error instanceof Error
        && (error as Error & { code?: string }).code === 'invalid_openai_oauth_codex_request'
    )
  )
  assert.equal(
    capturedErrors.filter((event) => event.event === 'gateway_json_parse_worker_failed').length,
    0,
    '明确编码的适配器输入错误不能记成 worker 基础设施故障'
  )

  const responseHeaders = new Map<string, string>()
  const requestContext = await new Promise<ReturnType<typeof import('../../shared/request-context.js')['getRequestContext']>>((resolveContext) => {
    const request = {
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      ip: '127.0.0.1',
      socket: { remoteAddress: '127.0.0.1' },
      header: () => undefined
    } as unknown as Request
    const response = {
      setHeader: (name: string, value: string) => responseHeaders.set(name.toLowerCase(), value),
      once: () => undefined
    } as unknown as Response
    requestContextMiddleware(request, response, () => resolveContext(getRequestContext()))
  })
  assert(requestContext, 'HTTP 中间件必须建立请求上下文')
  const traceId = requestContext.traceId
  const requestId = (requestContext as { requestId?: string }).requestId
  assert.equal(typeof requestId, 'string', 'HTTP 入口必须生成独立 requestId')
  assert.notEqual(requestId, traceId, 'requestId 必须与整条链路 traceId 区分')
  assert.equal(responseHeaders.get('x-trace-id'), traceId)
  assert.equal(requestContextBindings?.requestId, requestId, 'requestId 必须绑定到 HTTP child logger')
  assert(requestStartedFields, 'HTTP 开始事件必须通过请求 child logger 发出')
  assert.equal(
    Object.hasOwn(requestStartedFields, 'requestId'),
    false,
    'requestId 已由 child binding 提供，事件字段不能重复序列化同名键'
  )

  await assert.rejects(
    withRequestContext(requestContext, () => normalizeOpenAIOAuthCodexBodyInWorker(
      Buffer.from('{"model":"gpt-regression","input":"hello"}', 'utf8'),
      undefined as never
    )),
    /归一化参数缺失/
  )
  const genericWorkerFailure = capturedErrors.find((event) => (
    event.event === 'gateway_json_parse_worker_failed'
      && event.jobType === 'normalize_openai_oauth_codex_body'
  ))
  assert(genericWorkerFailure, 'worker 返回的非已知业务错误必须进入 logger.error 失败通道')
  assert.equal(genericWorkerFailure.failureClass, 'infrastructure')
  const genericWorkerError = genericWorkerFailure.err as { type?: unknown; message?: unknown; stack?: unknown } | undefined
  assert.equal(genericWorkerError?.type, 'Error')
  assert.match(String(genericWorkerError?.message), /归一化参数缺失/)
  assert.match(String(genericWorkerError?.stack), /json-worker\.js/, 'err.stack 必须来自 worker 抛错现场')
  const workerError = genericWorkerFailure.workerError as {
    name?: unknown
    message?: unknown
    stack?: unknown
    cause?: { name?: unknown; message?: unknown; stack?: unknown }
  } | undefined
  assert.equal(workerError?.name, 'Error')
  assert.match(String(workerError?.message), /归一化参数缺失/)
  assert.match(String(workerError?.stack), /json-worker\.js/, 'worker envelope 必须保留原始 worker stack')
  assert.equal(workerError?.cause?.name, 'Error')
  assert.equal(workerError?.cause?.message, 'normalize_input_missing')
  assert.match(String(workerError?.cause?.stack), /json-worker\.js/, 'worker envelope 必须保留原始 cause stack')

  await assert.rejects(
    normalizeOpenAIOAuthCodexParsedBodyInWorker({
      model: 'gpt-regression',
      input: 'hello'
    }, 1024, {
      inputHeaders: {},
      account: {
        id: 'worker-account-scoped-error',
        apiKey: 'token',
        credentials: { service_tier_override: 'invalid value' }
      },
      identity: { systemAccountId: 'system', apiKeyId: 'api-key', groupId: 'group' },
      compact: false,
      requestOverrideModelCapabilities: {
        supportedServiceTiers: ['priority'],
        supportedReasoningEfforts: ['high']
      }
    }),
    (error: unknown) => {
      assert.equal((error as { accountScoped?: unknown }).accountScoped, true, 'worker 返回必须保留 accountScoped')
      return true
    }
  )

  await assert.rejects(
    withRequestContext(requestContext, () => parseGatewayJsonBodyInWorker(
      Buffer.from(JSON.stringify({ input: 'x'.repeat(8 * 1024 * 1024) }), 'utf8'),
      1
    )),
    /worker 1ms 超时/
  )

  const failureEvent = capturedErrors.find((event) => (
    event.event === 'gateway_json_parse_worker_failed'
      && event.jobType === 'parse_json_body'
  ))
  assert(failureEvent, 'worker 超时必须进入 logger.error 失败通道')
  assert.equal(failureEvent.failureClass, 'infrastructure')
  const timeoutError = failureEvent.err as { type?: unknown; message?: unknown; stack?: unknown } | undefined
  assert.equal(timeoutError?.type, 'GatewayJsonWorkerTimeoutError')
  assert.match(String(timeoutError?.message), /worker 1ms 超时/)
  assert.match(String(timeoutError?.stack), /worker 1ms 超时/)
  assert.match(String(failureEvent.jobId), /^gateway-json-worker:\d+$/, 'jobId 必须是稳定字符串标识')
  assert.equal(failureEvent.parentId, requestId, 'parentId 必须引用真实父 HTTP 请求操作 ID')
  assert.notEqual(failureEvent.parentId, failureEvent.traceId)
  assert.equal(
    capturedWarnings.filter((event) => event.event === 'gateway_json_parse_worker_failed').length,
    0,
    '基础设施故障不能落入 warn 普通日志通道'
  )

  await stopGatewayJsonParseWorker()
  setGatewayJsonWorkerPoolSizeForTest(1)
  try {
    const blocker = parseGatewayJsonBodyInWorker(
      Buffer.from(JSON.stringify({ input: 'x'.repeat(32 * 1024 * 1024) }), 'utf8'),
      30_000
    )
    const queuedTimeoutStartedAt = performance.now()
    await assert.rejects(
      parseGatewayJsonBodyInWorker(Buffer.from('{"queued":true}', 'utf8'), 1),
      /worker 1ms 超时/
    )
    assert.ok(performance.now() - queuedTimeoutStartedAt < 1_000, '排队等待必须纳入 worker 任务总超时')
    await blocker
  } finally {
    await stopGatewayJsonParseWorker()
    setGatewayJsonWorkerPoolSizeForTest(undefined)
  }

  const completedValue = await withRequestContext(requestContext, () => parseGatewayJsonBodyInWorker(
    Buffer.from('{"model":"gpt-regression"}', 'utf8')
  )) as { model?: unknown }
  assert.equal(completedValue.model, 'gpt-regression')
  const completedEvent = capturedInfos.find((event) => (
    event.event === 'gateway_json_worker_job_completed'
      && event.outcome === 'success'
      && event.jobType === 'parse_json_body'
      && event.parentId === requestId
  ))
  assert(completedEvent, '普通快速 worker 任务也必须记录 info 完成事件')
  assert.equal(completedEvent.traceId, traceId)
  assert.equal(completedEvent.parentId, requestId)

  const controller = new AbortController()
  const canceledPromise = withRequestContext(requestContext, () => parseGatewayJsonBodyInWorker(
    Buffer.from(JSON.stringify({ input: 'x'.repeat(8 * 1024 * 1024) }), 'utf8'),
    30_000,
    controller.signal
  ))
  controller.abort()
  await assert.rejects(canceledPromise, /任务已取消/)
  const canceledEvent = capturedInfos.find((event) => event.event === 'gateway_json_worker_job_canceled')
  assert(canceledEvent, 'worker 取消必须记录 info 现场')
  assert.equal(canceledEvent.traceId, traceId)
  assert.equal(canceledEvent.parentId, requestId)
} finally {
  logger.error = originalError
  logger.warn = originalWarn
  logger.info = originalInfo
  logger.child = originalChild
  await stopGatewayJsonParseWorker()
}

console.log('gateway-json-worker-failure-logging-regression passed')
