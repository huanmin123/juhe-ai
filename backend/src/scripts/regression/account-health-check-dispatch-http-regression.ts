import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { readFileSync } from 'node:fs'
import http from 'node:http'
import { gzipSync } from 'node:zlib'

import cors from 'cors'
import express, { type NextFunction, type Request, type Response } from 'express'

import {
  accountHealthCheckDispatchInternalPrefix,
  accountHealthCheckDispatchSignatureDomain,
  createAccountHealthCheckDispatchRouter,
  createAccountHealthCheckDispatchSignature,
  handleAccountHealthCheckDispatchBodyParserError,
  isLoopbackRemoteAddress,
  mountAccountHealthCheckDispatchBridge
} from '../../modules/internal-api/account-health-check-dispatch.routes.js'
import { createHttpCompressionMiddleware } from '../../shared/http-compression.js'
import { createCorsOriginDelegate } from '../../shared/http-security.js'
import { logger } from '../../shared/logger.js'
import { requestContextMiddleware } from '../../shared/request-context.js'

const secret = 'account-health-check-dispatch-http-secret'
const internalApiPrefix = accountHealthCheckDispatchInternalPrefix
const allowedCorsOrigin = 'https://bridge.example'
const originalLoggerLevel = logger.level
const dispatchCalls: Array<{ accountId: string; reason: string; traceId?: string }> = []
const handledErrors: unknown[] = []
const app = express()
const corsMiddleware = cors({
  credentials: true,
  origin: createCorsOriginDelegate({
    cors: {
      allowedOrigins: [allowedCorsOrigin],
      allowAnyOrigin: false
    },
    cookie: {
      secure: false,
      sameSite: 'lax'
    },
    trustProxy: false
  })
})

logger.level = 'silent'
app.set('trust proxy', true)
app.use(requestContextMiddleware)
mountAccountHealthCheckDispatchBridge(app, {
  corsMiddleware,
  compressionMiddleware: createHttpCompressionMiddleware(),
  secret,
  dispatch: (accountId, reason, traceId) => {
    dispatchCalls.push({ accountId, reason, traceId })
    if (accountId === 'dispatch-coalesced') {
      return {
        outcome: 'coalesced' as const,
        decisionCode: 'request_failure_cooldown' as const,
        targetRole: 'ops-worker' as const,
        cooldownRemainingMs: 299_000
      }
    }
    if (accountId === 'dispatch-queue-full') {
      return {
        outcome: 'rejected' as const,
        decisionCode: 'ops_ipc_message_limit' as const,
        targetRole: 'ops-worker' as const,
        queueLength: 5_000,
        queueBytes: 12_345,
        messageBytes: 128,
        maxQueueMessages: 5_000,
        maxQueueBytes: 64 * 1024 * 1024
      }
    }
    if (accountId === 'dispatch-unavailable') {
      return {
        outcome: 'rejected' as const,
        decisionCode: 'ops_ipc_unavailable' as const,
        targetRole: 'ops-worker' as const,
        queueLength: 0,
        queueBytes: 0,
        messageBytes: 0,
        maxQueueMessages: 5_000,
        maxQueueBytes: 64 * 1024 * 1024
      }
    }
    if (accountId === 'dispatch-false') return false
    if (accountId === 'dispatch-throws') {
      throw Object.assign(new Error('dispatch failed'), {
        status: 400,
        type: 'dispatch.failed'
      })
    }
    return true
  }
})
app.use((_req, res) => {
  res.status(418).json({ message: '请求不应离开内部路由' })
})
app.use((error: unknown, _req: Request, res: Response, _next: NextFunction) => {
  handledErrors.push(error)
  res.status(500).json({ message: '服务器内部错误' })
})

const nonLoopbackDispatchCalls: Array<{ accountId: string; reason: string }> = []
const nonLoopbackApp = express()
nonLoopbackApp.use(internalApiPrefix, (req, _res, next) => {
  Object.defineProperty(req.socket, 'remoteAddress', {
    configurable: true,
    value: '192.0.2.10'
  })
  next()
}, createAccountHealthCheckDispatchRouter({
  secret,
  dispatch: (accountId, reason) => {
    nonLoopbackDispatchCalls.push({ accountId, reason })
    return true
  }
}))

const server = app.listen(0, '127.0.0.1')
const nonLoopbackServer = nonLoopbackApp.listen(0, '127.0.0.1')

try {
  await listen(server)
  await listen(nonLoopbackServer)
  const baseUrl = `http://127.0.0.1:${serverPort(server)}`
  const nonLoopbackBaseUrl = `http://127.0.0.1:${serverPort(nonLoopbackServer)}`

  assertSignatureHelper()
  assertLoopbackHelper()
  assertRouterConfiguration()
  assertBodyParserErrorMiddleware()
  assertServerMountingOrder()

  const publicOptions = await request(baseUrl, {
    path: '/__aipublic__/probe',
    method: 'OPTIONS',
    body: Buffer.alloc(0),
    omitSignature: true,
    expectNoStore: false,
    headers: {
      origin: allowedCorsOrigin,
      'access-control-request-method': 'POST'
    }
  })
  assert.equal(publicOptions.statusCode, 204, '非 internal OPTIONS 必须保持通用 CORS 预检响应')
  assert.equal(
    publicOptions.headers['access-control-allow-origin'],
    allowedCorsOrigin,
    '非 internal OPTIONS 必须返回实际 Access-Control-Allow-Origin'
  )
  assert.equal(
    publicOptions.headers['access-control-allow-credentials'],
    'true',
    '非 internal OPTIONS 必须保持 credentials CORS header'
  )
  assert.match(
    publicOptions.headers['access-control-allow-methods'] ?? '',
    /(?:^|,)POST(?:,|$)/,
    '非 internal OPTIONS 必须允许 POST'
  )

  const internalOptions = await request(baseUrl, {
    method: 'OPTIONS',
    body: Buffer.alloc(0),
    omitSignature: true,
    headers: {
      origin: 'https://example.com',
      'access-control-request-method': 'POST'
    }
  })
  assert.equal(internalOptions.statusCode, 404, 'internal OPTIONS 不得被通用 CORS 提前响应 204')
  assert.deepEqual(parseJson(internalOptions), { message: '资源不存在' })
  assert.equal(
    internalOptions.headers['access-control-allow-origin'],
    undefined,
    'internal OPTIONS 不得返回 CORS allow-origin'
  )

  const mountCaseBody = Buffer.from(JSON.stringify({
    accountId: 'mount-case',
    reason: 'activation'
  }))
  const dispatchCountBeforeMountCase = dispatchCalls.length
  const mountCasePost = await request(baseUrl, {
    path: '/__AIINTERNAL__/v1/account-health-check/dispatch',
    body: mountCaseBody
  })
  assert.equal(mountCasePost.statusCode, 404, 'internal mount 前缀大小写变化的 POST 必须返回 404')
  assert.deepEqual(parseJson(mountCasePost), { message: '资源不存在' })
  const mountCaseOptions = await request(baseUrl, {
    path: '/__AIINTERNAL__/v1/account-health-check/dispatch',
    method: 'OPTIONS',
    body: Buffer.alloc(0),
    omitSignature: true,
    headers: {
      origin: 'https://example.com',
      'access-control-request-method': 'POST'
    }
  })
  assert.equal(mountCaseOptions.statusCode, 404, 'internal mount 前缀大小写变化的 OPTIONS 必须返回 404')
  assert.deepEqual(parseJson(mountCaseOptions), { message: '资源不存在' })
  assert.equal(
    mountCaseOptions.headers['access-control-allow-origin'],
    undefined,
    '错误大小写 internal OPTIONS 不得返回 CORS allow-origin'
  )
  assert.equal(dispatchCalls.length, dispatchCountBeforeMountCase, '错误大小写 internal mount 不得触发 dispatch')

  const activationBody = Buffer.from(JSON.stringify({
    accountId: '  account-activation  ',
    reason: 'activation'
  }))
  const activation = await request(baseUrl, {
    body: activationBody,
    headers: {
      'x-forwarded-for': '198.51.100.25',
      'x-trace-id': 'gateway-control-trace-activation',
      'x-juhe-ai-signature': createAccountHealthCheckDispatchSignature(secret, activationBody)
    }
  })
  assert.equal(activation.statusCode, 202, '合法 activation 请求应返回 202')
  assert.equal(activation.body.length, 0, '202 响应必须为空 body')
  assert.deepEqual(dispatchCalls.at(-1), {
    accountId: 'account-activation',
    reason: 'activation',
    traceId: 'gateway-control-trace-activation'
  }, 'dispatch 应收到 trim 后账户 ID 与请求上下文 trace，且不能信任转发头拒绝本机请求')

  const configurationBody = Buffer.from(JSON.stringify({
    accountId: 'account-configuration',
    reason: 'configuration'
  }))
  const configuration = await request(baseUrl, { body: configurationBody })
  assert.equal(configuration.statusCode, 202, '合法 configuration 请求应返回 202')
  assert.deepEqual({
    accountId: dispatchCalls.at(-1)?.accountId,
    reason: dispatchCalls.at(-1)?.reason
  }, {
    accountId: 'account-configuration',
    reason: 'configuration'
  })
  assert.equal(typeof dispatchCalls.at(-1)?.traceId, 'string', 'control dispatch 必须为无显式 header 的请求提供 trace')

  const requestFailureBody = Buffer.from(JSON.stringify({
    accountId: 'account-request-failure',
    reason: 'request_failure'
  }))
  const requestFailure = await request(baseUrl, { body: requestFailureBody })
  assert.equal(requestFailure.statusCode, 202, '合法 request_failure 请求应返回 202')
  assert.deepEqual({
    accountId: dispatchCalls.at(-1)?.accountId,
    reason: dispatchCalls.at(-1)?.reason
  }, {
    accountId: 'account-request-failure',
    reason: 'request_failure'
  })
  assert.equal(typeof dispatchCalls.at(-1)?.traceId, 'string', 'request_failure dispatch 必须保留请求 trace')

  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'account-scheduled', reason: 'scheduled' }))
  }, 400, 'scheduled 原因必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'account-extra', reason: 'activation', extra: true }))
  }, 400, '额外字段必须被拒绝')
  const dispatchCountBeforeBodyTrace = dispatchCalls.length
  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'account-body-trace', reason: 'activation', traceId: 'body-controlled-trace' }))
  }, 400, 'body traceId 不得成为健康检查 IPC 控制输入')
  assert.equal(dispatchCalls.length, dispatchCountBeforeBodyTrace, 'body traceId 被拒绝时不得触发 dispatch')
  await assertStatus(baseUrl, {
    body: Buffer.from('{"accountId":')
  }, 400, 'malformed JSON 必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.alloc(0)
  }, 400, '空 body 必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.from('"account-scalar"')
  }, 400, 'scalar JSON 必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.from('[]')
  }, 400, 'array JSON 必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.from('null')
  }, 400, 'null JSON 必须被拒绝')
  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: '   ', reason: 'activation' }))
  }, 400, '空白 accountId 必须被拒绝')

  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'missing-signature', reason: 'activation' })),
    omitSignature: true
  }, 401, '缺失签名必须返回 401')
  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'bad-signature', reason: 'activation' })),
    signature: `v1=${'0'.repeat(64)}`
  }, 401, '错误签名必须返回 401')
  const invalidSignatureFormatBody = Buffer.from(JSON.stringify({
    accountId: 'invalid-signature-format',
    reason: 'activation'
  }))
  const validSignature = createAccountHealthCheckDispatchSignature(secret, invalidSignatureFormatBody)
  const dispatchCountBeforeInvalidSignatureFormats = dispatchCalls.length
  for (const [signature, message] of [
    [`v2=${validSignature.slice(3)}`, '错误签名版本必须返回 401'],
    [`v1=${'a'.repeat(63)}`, '非 64 位签名必须返回 401'],
    [`v1=${validSignature.slice(3).toUpperCase()}`, 'uppercase hex 签名必须返回 401']
  ] as const) {
    await assertStatus(baseUrl, {
      body: invalidSignatureFormatBody,
      signature
    }, 401, message)
  }
  assert.equal(
    dispatchCalls.length,
    dispatchCountBeforeInvalidSignatureFormats,
    '签名格式错误不得触发 dispatch'
  )
  const duplicateSignatureBody = Buffer.from(JSON.stringify({
    accountId: 'duplicate-signature',
    reason: 'activation'
  }))
  const duplicateSignature = createAccountHealthCheckDispatchSignature(secret, duplicateSignatureBody)
  await assertStatus(baseUrl, {
    body: duplicateSignatureBody,
    signature: [duplicateSignature, duplicateSignature]
  }, 401, '多值签名必须返回 401')

  await assertStatus(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'non-json', reason: 'activation' })),
    contentType: 'text/plain'
  }, 415, '非 JSON content type 必须返回 415')

  const gzipSourceBody = Buffer.from(JSON.stringify({
    accountId: 'gzip-body',
    reason: 'activation'
  }))
  const gzipBody = gzipSync(gzipSourceBody)
  const dispatchCountBeforeGzip = dispatchCalls.length
  await assertStatus(baseUrl, {
    body: gzipBody,
    headers: { 'content-encoding': 'gzip' }
  }, 415, '签压缩字节的 gzip body 必须返回 415')
  await assertStatus(baseUrl, {
    body: gzipBody,
    headers: { 'content-encoding': 'gzip' },
    signature: createAccountHealthCheckDispatchSignature(secret, gzipSourceBody)
  }, 415, '签解压字节的 gzip body 也必须返回 415')
  assert.equal(dispatchCalls.length, dispatchCountBeforeGzip, 'gzip body 不得触发 dispatch')

  const exactLimitBody = createExactJsonBody(1024, 'exact-limit')
  const exactLimit = await request(baseUrl, { body: exactLimitBody })
  assert.equal(exactLimit.statusCode, 202, '恰好 1024 bytes 的合法 JSON 必须成功')
  assert.equal(dispatchCalls.at(-1)?.accountId.length, accountIdLength(exactLimitBody))

  const overLimitBody = createExactJsonBody(1025, 'over-limit')
  await assertStatus(baseUrl, {
    body: overLimitBody
  }, 413, '1025 bytes 的 raw body 必须返回 413')

  const strictPathBody = Buffer.from(JSON.stringify({
    accountId: 'strict-path',
    reason: 'activation'
  }))
  const dispatchCountBeforeStrictPaths = dispatchCalls.length
  await assertStatus(baseUrl, {
    path: `${internalApiPrefix}/v1/Account-health-check/dispatch`,
    body: strictPathBody
  }, 404, 'internal 路径大小写变化必须返回 404')
  await assertStatus(baseUrl, {
    path: `${internalApiPrefix}/v1/account-health-check/dispatch/`,
    body: strictPathBody
  }, 404, 'internal 路径尾斜杠必须返回 404')
  assert.equal(dispatchCalls.length, dispatchCountBeforeStrictPaths, '非严格路径不得触发 dispatch')

  const nonLoopbackBody = Buffer.from(JSON.stringify({
    accountId: 'non-loopback',
    reason: 'activation'
  }))
  const nonLoopback = await request(nonLoopbackBaseUrl, { body: nonLoopbackBody })
  assert.equal(nonLoopback.statusCode, 403, '真实 HTTP 非 loopback socket 必须返回 403')
  assert.deepEqual(parseJson(nonLoopback), { message: '禁止访问' })
  assert.deepEqual(nonLoopbackDispatchCalls, [], '非 loopback 请求不得触发 dispatch')

  const dispatchFalse = await request(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'dispatch-false', reason: 'activation' }))
  })
  assert.equal(dispatchFalse.statusCode, 503, 'dispatch false 必须返回 503')
  assert.deepEqual(parseJson(dispatchFalse), { message: '服务暂不可用' })

  const dispatchCoalesced = await request(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'dispatch-coalesced', reason: 'request_failure' }))
  })
  assert.equal(dispatchCoalesced.statusCode, 202, '请求失败冷却去重仍应表示为已受理')
  assert.equal(dispatchCoalesced.body.length, 0, '去重后的 202 响应必须为空 body')

  const dispatchQueueFull = await request(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'dispatch-queue-full', reason: 'request_failure' }))
  })
  assert.equal(dispatchQueueFull.statusCode, 503, '真实 ops IPC 队列满必须返回 503')
  assert.deepEqual(parseJson(dispatchQueueFull), { message: '服务暂不可用' }, '外部响应不得泄露内部队列状态')

  const dispatchUnavailable = await request(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'dispatch-unavailable', reason: 'request_failure' }))
  })
  assert.equal(dispatchUnavailable.statusCode, 503, 'ops IPC 不可用必须返回 503')
  assert.deepEqual(parseJson(dispatchUnavailable), { message: '服务暂不可用' }, '不可用响应不得泄露内部 decision code')

  const dispatchThrows = await request(baseUrl, {
    body: Buffer.from(JSON.stringify({ accountId: 'dispatch-throws', reason: 'configuration' }))
  })
  assert.equal(dispatchThrows.statusCode, 500, 'dispatch 未处理错误必须返回 500')
  assert.deepEqual(parseJson(dispatchThrows), { message: '服务器内部错误' })
  assert.equal(handledErrors.length, 1, 'dispatch throw 必须进入 app 最终错误 handler')
  assert.equal(
    handledErrors[0] instanceof Error ? handledErrors[0].message : undefined,
    'dispatch failed',
    'app 最终错误 handler 必须收到原始 dispatch 错误'
  )
  assert.equal(
    typeof handledErrors[0] === 'object' && handledErrors[0] !== null
      ? Reflect.get(handledErrors[0], 'status')
      : undefined,
    400,
    '带 4xx status 的 dispatch 错误也必须原样进入 app 最终错误 handler'
  )
  assert.equal(
    typeof handledErrors[0] === 'object' && handledErrors[0] !== null
      ? Reflect.get(handledErrors[0], 'type')
      : undefined,
    'dispatch.failed',
    'dispatch 错误类型不得被 parser error handler 消费'
  )

  const otherInternalPath = await request(baseUrl, {
    path: '/__aiinternal__/v1/other',
    method: 'GET',
    body: Buffer.alloc(0),
    omitSignature: true
  })
  assert.equal(otherInternalPath.statusCode, 404, '其他 internal path 必须在 gateway 前返回 404')
  assert.deepEqual(parseJson(otherInternalPath), { message: '资源不存在' })

  console.log('账户健康检查 dispatch HTTP bridge 回归通过')
} finally {
  await closeServer(nonLoopbackServer)
  await closeServer(server)
  logger.level = originalLoggerLevel
}

function assertSignatureHelper(): void {
  const goldenDomain = 'juhe-ai:account-health-check-dispatch:v1\n'
  const goldenSecret = 'account-health-check-dispatch-golden-secret'
  const goldenBody = Buffer.from('{"accountId":"account-signature","reason":"activation"}', 'utf8')
  const goldenSignature = 'v1=05ad33521b57c2bf26f665c1c216631ffcdd3ad73b2463c9691b1c14e43dc329'
  const independentlyComputedSignature = `v1=${createHmac('sha256', goldenSecret)
    .update(goldenDomain, 'utf8')
    .update(goldenBody)
    .digest('hex')}`

  assert.equal(
    accountHealthCheckDispatchSignatureDomain,
    goldenDomain,
    '导出签名 domain 必须保持固定 literal'
  )
  assert.equal(
    independentlyComputedSignature,
    goldenSignature,
    '独立 crypto 实现必须匹配预计算 golden digest'
  )
  assert.equal(
    createAccountHealthCheckDispatchSignature(goldenSecret, goldenBody),
    goldenSignature,
    '生产签名 helper 必须匹配独立 golden vector'
  )
}

function assertLoopbackHelper(): void {
  for (const address of [
    '127.0.0.1',
    '127.255.255.254',
    '::1',
    '::ffff:127.0.0.1',
    '::ffff:7f00:1'
  ]) {
    assert.equal(isLoopbackRemoteAddress(address), true, `${address} 应识别为 loopback`)
  }
  for (const address of [
    undefined,
    '',
    '192.0.2.10',
    '::2',
    '::ffff:192.0.2.10'
  ]) {
    assert.equal(isLoopbackRemoteAddress(address), false, `${String(address)} 不应识别为 loopback`)
  }
}

function assertRouterConfiguration(): void {
  const source = readFileSync(new URL('../../modules/internal-api/account-health-check-dispatch.routes.ts', import.meta.url), 'utf8')
  assert(
    source.includes('Router({ caseSensitive: true, strict: true })'),
    'internal Router 必须启用 caseSensitive 与 strict'
  )
  assert(source.includes('inflate: false'), 'internal raw body parser 必须显式禁用 inflate')
  assert.match(source, /event: 'account_health_check_dispatch_decision'/, 'control 必须记录健康检查派发决策事件')
  for (const field of [
    'outcome:', 'triggerReason:', 'decisionCode:', 'targetRole:',
    'queueLength:', 'queueBytes:', 'messageBytes:', 'maxQueueMessages:',
    'maxQueueBytes:', 'cooldownRemainingMs:', 'statusCode'
  ]) {
    assert(source.includes(field), `派发决策日志必须包含 ${field}`)
  }
  assert(!/accountId:\s*payload\.accountId/.test(source), 'control 派发决策日志不得写入账户 ID')
}

function assertBodyParserErrorMiddleware(): void {
  const tooLargeError = Object.assign(new Error('sensitive too large detail'), {
    type: 'entity.too.large'
  })
  const tooLarge = runBodyParserErrorMiddleware(tooLargeError)
  assert.equal(tooLarge.statusCode, 413, 'entity.too.large 必须保持 413')
  assert.deepEqual(tooLarge.payload, { message: '请求体过大' })
  assert.equal(tooLarge.forwardedError, undefined, 'entity.too.large 不得进入全局 500')

  const statusError = Object.assign(new Error('sensitive parser detail'), {
    status: 422,
    type: 'entity.parse.failed'
  })
  const statusResult = runBodyParserErrorMiddleware(statusError)
  assert.equal(statusResult.statusCode, 422, '可信 status 4xx 必须保留')
  assert.deepEqual(statusResult.payload, { message: '请求体无效' })
  assert.equal(JSON.stringify(statusResult.payload).includes('sensitive'), false, '4xx 响应不得泄露 parser 详情')
  assert.equal(statusResult.forwardedError, undefined, '可信 status 4xx 不得进入全局 500')

  const statusCodeError = Object.assign(new Error('sensitive size detail'), {
    statusCode: 409,
    type: 'request.size.invalid'
  })
  const statusCodeResult = runBodyParserErrorMiddleware(statusCodeError)
  assert.equal(statusCodeResult.statusCode, 409, '可信 statusCode 4xx 必须保留')
  assert.deepEqual(statusCodeResult.payload, { message: '请求体无效' })
  assert.equal(statusCodeResult.forwardedError, undefined, '可信 statusCode 4xx 不得进入全局 500')

  const serverError = new Error('server failure')
  const serverResult = runBodyParserErrorMiddleware(serverError)
  assert.equal(serverResult.writes, 0, '无 4xx status 的服务端错误不得由 parser middleware 写响应')
  assert.equal(serverResult.forwardedError, serverError, '无 4xx status 的服务端错误必须透传')

  const untrustedStatusError = Object.assign(new Error('invalid status type'), {
    status: '400'
  })
  const untrustedStatusResult = runBodyParserErrorMiddleware(untrustedStatusError)
  assert.equal(untrustedStatusResult.writes, 0, '非整数 status 不得视为可信 4xx')
  assert.equal(untrustedStatusResult.forwardedError, untrustedStatusError, '非整数 status 必须透传')

  const abortedResult = runBodyParserErrorMiddleware(
    Object.assign(new Error('request aborted'), { status: 400, type: 'request.aborted' }),
    { requestAborted: true }
  )
  assert.equal(abortedResult.writes, 0, 'request.aborted 后不得继续写 4xx 响应')
  assert.equal(abortedResult.nextCalls, 0, 'request.aborted client error 不得转成全局 500')

  const abortedTypeResult = runBodyParserErrorMiddleware(
    Object.assign(new Error('request aborted type'), { status: 400, type: 'request.aborted' })
  )
  assert.equal(abortedTypeResult.writes, 0, 'request.aborted parser error type 不得继续写响应')
  assert.equal(abortedTypeResult.nextCalls, 0, 'request.aborted parser error type 不得转成全局 500')

  const destroyedResponseResult = runBodyParserErrorMiddleware(
    Object.assign(new Error('response destroyed'), { statusCode: 400, type: 'request.aborted' }),
    { responseDestroyed: true }
  )
  assert.equal(destroyedResponseResult.writes, 0, 'response destroyed 后不得继续写 4xx 响应')
  assert.equal(destroyedResponseResult.nextCalls, 0, 'response destroyed client error 不得转成全局 500')
}

function runBodyParserErrorMiddleware(
  error: Error,
  options: {
    requestAborted?: boolean
    responseDestroyed?: boolean
  } = {}
): {
  statusCode?: number
  payload?: unknown
  writes: number
  nextCalls: number
  forwardedError?: unknown
} {
  const state: {
    statusCode?: number
    payload?: unknown
    writes: number
    nextCalls: number
    forwardedError?: unknown
  } = {
    writes: 0,
    nextCalls: 0
  }
  const response = {
    destroyed: options.responseDestroyed ?? false,
    headersSent: false,
    writableEnded: false,
    status(statusCode: number) {
      state.writes += 1
      state.statusCode = statusCode
      return response
    },
    json(payload: unknown) {
      state.writes += 1
      state.payload = payload
      return response
    }
  } as unknown as Response
  const request = {
    aborted: options.requestAborted ?? false,
    destroyed: false
  } as Request
  const next = ((forwardedError?: unknown) => {
    state.nextCalls += 1
    state.forwardedError = forwardedError
  }) as NextFunction

  handleAccountHealthCheckDispatchBodyParserError(error, request, response, next)
  return state
}

function assertServerMountingOrder(): void {
  const source = readFileSync(new URL('../../server.ts', import.meta.url), 'utf8')
  const requestContextIndex = source.indexOf('app.use(requestContextMiddleware)')
  const corsWrapperIndex = source.indexOf('const corsMiddleware = cors(')
  const internalBridgeIndex = source.indexOf('mountAccountHealthCheckDispatchBridge(app, {')
  const healthIndex = source.indexOf('app.get(`${systemPrefix}/health`')
  const systemApiIndex = source.indexOf('app.use(systemApiPrefix')
  const gatewayIndex = source.lastIndexOf('preResolveGatewayRuntime')

  assert(requestContextIndex >= 0, 'server.ts 必须保留 request context middleware')
  assert(corsWrapperIndex > requestContextIndex, '通用 CORS wrapper 必须位于 request context 后')
  assert(
    source.includes('corsMiddleware,'),
    'server.ts 必须把原通用 CORS middleware 传给 internal bridge 装配 helper'
  )
  assert(
    source.includes('compressionMiddleware: createHttpCompressionMiddleware()'),
    'server.ts 必须通过共享装配 helper 保持 compression 顺序'
  )
  assert(!source.includes('preflightContinue'), '不得用全局 preflightContinue 改变其他路由 CORS 行为')
  assert(internalBridgeIndex > corsWrapperIndex, '共享 internal bridge 装配必须位于 CORS middleware 创建后')
  assert(internalBridgeIndex < healthIndex, 'internal dispatch bridge 必须位于 system health 前')
  assert(internalBridgeIndex < systemApiIndex, 'internal dispatch bridge 必须位于 system API 前')
  assert(internalBridgeIndex < gatewayIndex, 'internal dispatch bridge 必须位于 gateway 前')
}

function createExactJsonBody(totalBytes: number, accountIdPrefix: string): Buffer {
  const prefix = '{"accountId":"'
  const suffix = '","reason":"activation"}'
  const fixedBytes = Buffer.byteLength(prefix) + Buffer.byteLength(accountIdPrefix) + Buffer.byteLength(suffix)
  assert(totalBytes >= fixedBytes, '目标 JSON 字节数必须容纳固定字段')
  const body = Buffer.from(`${prefix}${accountIdPrefix}${'x'.repeat(totalBytes - fixedBytes)}${suffix}`)
  assert.equal(body.length, totalBytes, `JSON body 必须精确为 ${totalBytes} bytes`)
  return body
}

function accountIdLength(body: Buffer): number {
  const payload = JSON.parse(body.toString('utf8')) as { accountId: string }
  return payload.accountId.length
}

interface RequestOptions {
  path?: string
  method?: string
  body: Buffer
  contentType?: string
  signature?: string | string[]
  omitSignature?: boolean
  expectNoStore?: boolean
  headers?: http.OutgoingHttpHeaders
}

interface HttpResult {
  statusCode: number
  headers: http.IncomingHttpHeaders
  body: Buffer
}

async function assertStatus(
  baseUrl: string,
  options: RequestOptions,
  expectedStatus: number,
  message: string
): Promise<void> {
  const response = await request(baseUrl, options)
  assert.equal(response.statusCode, expectedStatus, message)
}

function request(baseUrl: string, options: RequestOptions): Promise<HttpResult> {
  const path = options.path ?? '/__aiinternal__/v1/account-health-check/dispatch'
  const signature = options.omitSignature
    ? undefined
    : options.signature ?? createAccountHealthCheckDispatchSignature(secret, options.body)
  const headers: http.OutgoingHttpHeaders = {
    'content-length': options.body.length,
    'content-type': options.contentType ?? 'application/json; charset=utf-8',
    ...options.headers
  }
  if (signature !== undefined) {
    headers['x-juhe-ai-signature'] = signature
  }

  return new Promise((resolve, reject) => {
    const req = http.request(`${baseUrl}${path}`, {
      method: options.method ?? 'POST',
      headers
    }, (res) => {
      const chunks: Buffer[] = []
      res.on('data', (chunk: Buffer) => chunks.push(Buffer.from(chunk)))
      res.on('end', () => {
        if (options.expectNoStore !== false) {
          assert.equal(res.headers['cache-control'], 'no-store', `${path} 响应必须 no-store`)
        }
        resolve({
          statusCode: res.statusCode ?? 0,
          headers: res.headers,
          body: Buffer.concat(chunks)
        })
      })
    })
    req.on('error', reject)
    req.end(options.body)
  })
}

function parseJson(response: HttpResult): unknown {
  return JSON.parse(response.body.toString('utf8')) as unknown
}

async function listen(server: http.Server): Promise<void> {
  if (server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return address.port
}
