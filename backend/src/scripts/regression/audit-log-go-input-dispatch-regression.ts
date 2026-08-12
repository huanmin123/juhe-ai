import { strict as assert } from 'node:assert'
import { createHmac } from 'node:crypto'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import type { AuditLogInput } from '../../storage/audit-log-types.js'

const originalEnvironment = new Map<string, string | undefined>()
for (const name of [
  'JUHE_AI_AUDIT_LOG_INPUT_URL',
  'JUHE_AI_AUDIT_LOG_INPUT_SECRET',
  'JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS',
  'JUHE_AI_SECRET',
  'JUHE_AI_LOG_FILE_ENABLED',
  'JUHE_AI_LOG_CONSOLE_ENABLED',
  'NODE_ENV'
]) {
  originalEnvironment.set(name, process.env[name])
}

const businessSecret = 'f3-dispatch-regression-business-secret'
const inputSecret = 'f3-dispatch-regression-input-secret'
process.env.JUHE_AI_SECRET = businessSecret
process.env.JUHE_AI_AUDIT_LOG_INPUT_SECRET = inputSecret
process.env.JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS = '1000'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.NODE_ENV = 'test'

let requestMode: 'success' | 'rejected' | 'timeout' = 'success'
let requestCount = 0
let pendingRequest: Promise<ReceivedRequest> | undefined
let resolveRequest: ((request: ReceivedRequest) => void) | undefined

const server = createServer((request, response) => handleRequest(request, response))
await listen(server)
process.env.JUHE_AI_AUDIT_LOG_INPUT_URL = `http://127.0.0.1:${addressPort(server)}`

try {
  const { dispatchAuditLogToGo, auditLogGoInputMaxBytes, auditLogGoInputPath } = await import(
    '../../modules/audit-logs/audit-log-go-input.service.js'
  )

  const largeInput = auditInput('dispatch-success', [
    Buffer.alloc(3 * 1024 * 1024, 0x61),
    Buffer.alloc(3 * 1024 * 1024, 0x62)
  ])
  requestMode = 'success'
  requestCount = 0
  dispatchAuditLogToGo(largeInput)
  const successRequest = await waitForRequest()
  assert.equal(successRequest.method, 'POST', 'Go 输入必须使用 POST')
  assert.equal(successRequest.url, auditLogGoInputPath, 'Go 输入必须使用固定 loopback path')
  assert.equal(successRequest.contentType, 'application/json', 'Go 输入必须声明 JSON content type')
  assert.equal(successRequest.traceId, largeInput.traceId, 'Go 输入必须传递 trace ID header')
  assert(successRequest.body.byteLength <= auditLogGoInputMaxBytes, 'Go 输入实际 JSON wire body 必须不超过 4MiB')
  assert.equal(Number(successRequest.contentLength), successRequest.body.byteLength, 'Content-Length 必须等于实际 UTF-8 bytes')
  const parsed = JSON.parse(successRequest.body.toString('utf8')) as { schemaVersion?: number; auditLog?: AuditLogInput }
  assert.equal(parsed.schemaVersion, 1, 'Go 输入 envelope schemaVersion 必须为 1')
  assert.equal(parsed.auditLog?.traceId, largeInput.traceId, 'Go 输入 envelope 必须包含原审计记录')
  const expectedSignature = `v1=${createHmac('sha256', inputSecret)
    .update('juhe-ai/audit-log-input/v1')
    .update('\n')
    .update(successRequest.body)
    .digest('hex')}`
  assert.equal(successRequest.signature, expectedSignature, 'Go 输入签名必须覆盖 domain、换行和原始 JSON body')
  assert.equal(requestCount, 1, '204 成功必须只发送一次 one-shot 请求')

  for (const trafficSource of ['account_health_check', 'runtime_recovery_probe', 'cooldown_retest'] as const) {
    requestCount = 0
    dispatchAuditLogToGo({
      ...auditInput(`dispatch-non-persisted-${trafficSource}`, [Buffer.from(trafficSource)]),
      trafficSource
    })
    await delay(50)
    assert.equal(requestCount, 0, `${trafficSource} 不属于持久化审计范围，不得发送 F3 输入请求`)
  }

  requestMode = 'rejected'
  requestCount = 0
  dispatchAuditLogToGo(auditInput('dispatch-rejected', [Buffer.from('rejected')]))
  await waitForRequest()
  await delay(100)
  assert.equal(requestCount, 1, '非 204 响应只记录失败，不得自动重试')

  requestMode = 'timeout'
  requestCount = 0
  dispatchAuditLogToGo(auditInput('dispatch-timeout', [Buffer.from('timeout')]))
  await waitForRequest()
  await delay(1_200)
  assert.equal(requestCount, 1, '超时只记录失败，不得自动重试')

  console.log('F3 Go input dispatch regression passed: loopback POST, exact HMAC/length, 4MiB wire budget, 204 acknowledgement and no retry.')
} finally {
  await close(server)
  for (const [name, value] of originalEnvironment) {
    if (value === undefined) delete process.env[name]
    else process.env[name] = value
  }
}

interface ReceivedRequest {
  method: string | undefined
  url: string | undefined
  contentType: string | undefined
  contentLength: string | undefined
  signature: string | undefined
  traceId: string | undefined
  body: Buffer
}

function headerValue(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value.at(-1) : value
}

function handleRequest(request: IncomingMessage, response: ServerResponse): void {
  requestCount += 1
  const chunks: Buffer[] = []
  request.on('data', (chunk: Buffer | string) => chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)))
  request.on('end', () => {
    const received: ReceivedRequest = {
      method: request.method,
      url: request.url,
      contentType: headerValue(request.headers['content-type']),
      contentLength: headerValue(request.headers['content-length']),
      signature: headerValue(request.headers['x-juhe-ai-signature']),
      traceId: headerValue(request.headers['x-trace-id']),
      body: Buffer.concat(chunks)
    }
    resolveRequest?.(received)
    resolveRequest = undefined
    pendingRequest = undefined
    if (requestMode === 'timeout') return
    response.statusCode = requestMode === 'success' ? 204 : 500
    response.end()
  })
}

function waitForRequest(): Promise<ReceivedRequest> {
  if (pendingRequest) return pendingRequest
  pendingRequest = new Promise<ReceivedRequest>((resolve, reject) => {
    resolveRequest = resolve
    setTimeout(() => {
      if (resolveRequest !== resolve) return
      resolveRequest = undefined
      pendingRequest = undefined
      reject(new Error('等待 Go 输入 fake server 请求超时'))
    }, 2_000).unref()
  })
  return pendingRequest
}

function auditInput(traceId: string, bodies: Buffer[]): AuditLogInput {
  const timestamp = '2026-08-09T00:00:00.000Z'
  return {
    id: `audit-${traceId}`,
    traceId,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'upstream_failed',
    success: false,
    sampleBucket: 1,
    sampleReason: 'f3_dispatch_regression',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    durationMs: 1,
    attempts: [],
    payloads: bodies.map((body, sequenceIndex) => ({
      id: `payload-${traceId}-${sequenceIndex}`,
      partType: sequenceIndex === 0 ? 'client_request' : 'upstream_response',
      sequenceIndex,
      contentType: 'application/json',
      body,
      captureStatus: 'complete'
    }))
  }
}

function listen(serverInstance: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    serverInstance.once('error', reject)
    serverInstance.listen(0, '127.0.0.1', () => {
      serverInstance.off('error', reject)
      resolve()
    })
  })
}

function addressPort(serverInstance: Server): number {
  const address = serverInstance.address()
  if (!address || typeof address === 'string') throw new Error('fake server 未取得 TCP 地址')
  return address.port
}

function close(serverInstance: Server): Promise<void> {
  return new Promise((resolve, reject) => {
    serverInstance.close((error) => error ? reject(error) : resolve())
  })
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
