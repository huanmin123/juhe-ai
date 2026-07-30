import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { connect } from 'node:net'

import express, { type NextFunction, type Request, type Response } from 'express'

import {
  captureGatewayRawBody,
  classifyGatewayRawBodyParserError,
  wrapGatewayRawBodyParser,
  type GatewayRawBodyParserError,
  type GatewayRawBodyParserErrorResponse
} from '../../modules/gateway/request/body-middleware.js'
import type { GatewayRawBodyRequest } from '../../modules/gateway/request/body.js'

const serverSource = readFileSync(new URL('../../server.ts', import.meta.url), 'utf8')
assert.match(serverSource, /const parserFailure = classifyGatewayRawBodyParserError\(error\)/, '生产 raw-body 错误中间件必须使用统一分类结果')
assert.match(serverSource, /wrapGatewayRawBodyParser\(/, '生产 raw-body parser 必须用局部包装器隔离自身错误')
assert.doesNotMatch(
  serverSource,
  /admitSpeedFirstRequestBody,\s*express\.raw\(\{ type: \(\) => true, limit: gatewayRawBodyLimit \}\),\s*handleGatewayRawBodyError/,
  'raw-body 错误处理器不得作为整条前置 middleware 链的通用 error handler'
)
assert.match(serverSource, /res\.status\(statusCode >= 400 && statusCode < 600 \? statusCode : 400\)/, '分类后的 408 必须进入真实 HTTP 状态响应')

const aborted = classifyGatewayRawBodyParserError(Object.assign(new Error('request aborted'), {
  status: 400,
  type: 'request.aborted'
}))
assert.deepEqual(aborted, {
  statusCode: 408,
  message: '请求体上传未完成，请重试',
  errorType: 'request_timeout',
  failureAttribution: 'downstream_closed'
})

const lengthMismatch = classifyGatewayRawBodyParserError(Object.assign(new Error('request size did not match content length'), {
  status: 400,
  type: 'request.size.invalid'
}))
assert.equal(lengthMismatch.statusCode, 408)

const invalidEncoding = classifyGatewayRawBodyParserError(Object.assign(new Error('unsupported charset'), {
  status: 415,
  type: 'charset.unsupported'
}))
assert.equal(invalidEncoding.statusCode, 415)
assert.equal(invalidEncoding.message, '网关请求体无效')
assert.equal(invalidEncoding.failureAttribution, 'gateway_policy')

const tooLarge = classifyGatewayRawBodyParserError(Object.assign(new Error('too large'), {
  status: 413,
  type: 'entity.too.large'
}))
assert.equal(tooLarge.statusCode, 413)
assert.equal(tooLarge.message, '请求体过大')

await testRealHttpBodyLifecycle()
await testPreParserErrorIsolation()

console.log('网关原始请求体解析错误分类回归通过：中断上传可重试，格式和大小错误保持原语义，前置 runtime 错误不会被误分类')

async function testRealHttpBodyLifecycle(): Promise<void> {
  const app = express()
  const parserFailures: Array<{
    parserType?: string
    classification: GatewayRawBodyParserErrorResponse
  }> = []
  let accountPolicySideEffectCount = 0
  let resolveParserFailure: (() => void) | undefined
  const parserFailureObserved = new Promise<void>((resolve) => { resolveParserFailure = resolve })

  app.use(express.raw({ type: () => true, limit: '1mb' }))
  app.use((error: Error & GatewayRawBodyParserError, _req: Request, res: Response, _next: NextFunction) => {
    const classification = classifyGatewayRawBodyParserError(error)
    parserFailures.push({ parserType: error.type, classification })
    resolveParserFailure?.()
    if (!res.headersSent && !res.destroyed) {
      res.status(classification.statusCode).json({
        error: { type: classification.errorType, message: classification.message }
      })
    }
  })
  app.use(captureGatewayRawBody)
  app.post('/v1/responses', (req: GatewayRawBodyRequest, res) => {
    if (req.gatewayRequestBody?.jsonParseStatus === 'invalid_json') {
      res.status(400).json({
        error: { type: 'invalid_request_error', message: '请求体不是合法 JSON' }
      })
      return
    }
    accountPolicySideEffectCount += 1
    res.status(204).end()
  })

  const server = app.listen(0, '127.0.0.1')
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
  try {
    const address = server.address()
    assert(address && typeof address === 'object')

    await sendTruncatedRequest(address.port)
    await Promise.race([
      parserFailureObserved,
      new Promise<never>((_resolve, reject) => setTimeout(() => reject(new Error('未观察到真实 HTTP 请求体中断')), 2_000))
    ])
    const abortedFailure = parserFailures.at(-1)
    assert.equal(abortedFailure?.parserType, 'request.aborted')
    assert.deepEqual(abortedFailure?.classification, {
      statusCode: 408,
      message: '请求体上传未完成，请重试',
      errorType: 'request_timeout',
      failureAttribution: 'downstream_closed'
    })
    assert.equal(accountPolicySideEffectCount, 0, '请求体生命周期失败不得进入账户策略路由')

    const malformedResponse = await sendCompleteRequest(address.port, '{"model":')
    assert.match(malformedResponse, /^HTTP\/1\.1 400 /)
    assert.match(malformedResponse, /invalid_request_error/)
    assert.equal(parserFailures.length, 1, '未知 JSON 应由网关 JSON 边界拒绝，不应伪装成 raw-body 生命周期错误')
    assert.equal(accountPolicySideEffectCount, 0, '未知 JSON 也不得进入账户策略路由')
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }
}

async function testPreParserErrorIsolation(): Promise<void> {
  const app = express()
  let parserErrorCount = 0
  let globalErrorCount = 0
  const parser = wrapGatewayRawBodyParser(
    express.raw({ type: () => true, limit: '1mb' }),
    (error: Error & GatewayRawBodyParserError, _req: Request, res: Response, _next: NextFunction) => {
      parserErrorCount += 1
      const classification = classifyGatewayRawBodyParserError(error)
      res.status(classification.statusCode).json({ error: { message: classification.message } })
    }
  )

  app.use((_req, _res, next) => next(new Error('系统设置缺少字段：textFirstResponseTimeoutSeconds')))
  app.use(parser)
  app.use((_error: Error, _req: Request, res: Response, _next: NextFunction) => {
    globalErrorCount += 1
    res.status(500).json({ message: '服务器内部错误' })
  })

  const server = app.listen(0, '127.0.0.1')
  await new Promise<void>((resolve, reject) => {
    server.once('listening', resolve)
    server.once('error', reject)
  })
  try {
    const address = server.address()
    assert(address && typeof address === 'object')
    const response = await sendCompleteRequest(address.port, '{"model":"gpt-5.4"}')
    assert.match(response, /^HTTP\/1\.1 500 /)
    assert.match(response, /服务器内部错误/)
    assert.equal(parserErrorCount, 0, '前置 runtime 错误不得进入 raw-body parser 错误分类')
    assert.equal(globalErrorCount, 1, '前置 runtime 错误必须由全局错误边界处理')
  } finally {
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
  }
}

async function sendTruncatedRequest(port: number): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const socket = connect(port, '127.0.0.1')
    socket.once('error', reject)
    socket.once('connect', () => {
      socket.write([
        'POST /v1/responses HTTP/1.1',
        'Host: 127.0.0.1',
        'Content-Type: application/json',
        'Content-Length: 128',
        'Connection: close',
        '',
        '{"model":"gpt-5.4"'
      ].join('\r\n'))
      socket.end()
      resolve()
    })
  })
}

async function sendCompleteRequest(port: number, body: string): Promise<string> {
  return await new Promise<string>((resolve, reject) => {
    const chunks: Buffer[] = []
    const socket = connect(port, '127.0.0.1')
    socket.once('error', reject)
    socket.on('data', (chunk: Buffer) => chunks.push(chunk))
    socket.once('end', () => resolve(Buffer.concat(chunks).toString('utf8')))
    socket.once('connect', () => {
      socket.end([
        'POST /v1/responses HTTP/1.1',
        'Host: 127.0.0.1',
        'Content-Type: application/json',
        `Content-Length: ${Buffer.byteLength(body)}`,
        'Connection: close',
        '',
        body
      ].join('\r\n'))
    })
  })
}
