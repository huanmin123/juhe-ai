import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-large-response-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'large-response.sqlite3')
runtimeConfig.secret = 'large-response-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../modules/gateway/openai-gateway.routes.js'),
  import('../shared/request-context.js'),
  import('../storage/database.js'),
  import('../storage/repositories.js'),
  import('../modules/gateway/usage-record-queue.service.js'),
  import('../modules/audit-logs/audit-log-queue.service.js')
])

const largeFieldSizeBytes = 8 * 1024 * 1024
const gatewayRawBodyLimit = '2mb'
const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: gatewayRawBodyLimit }), captureGatewayRawBody, openAIGatewayRouter)

type RawBodyRequest = Request & { rawBody?: Buffer }

interface UsageRecordListResult {
  items: Array<{
    apiKeyId?: string
    accountId?: string
    statusCode?: number
    success: boolean
    inputTokens?: number
    outputTokens?: number
  }>
  total: number
}

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    upstreamServer = createLargeResponseUpstreamServer()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const group = repositories.createGroup({ name: '大响应回归分组', providerCode: 'openai' })
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: '大响应回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-large-response-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true
    })
    const apiKey = repositories.createApiKeyRecord({
      name: '大响应回归 Key',
      groupId: group.id,
      status: 'active'
    })

    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const beforeHeap = usedHeapBytes()
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${apiKey.key}`,
        'content-type': 'application/json'
      },
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        input: 'large response regression',
        stream: false
      })
    })
    const bodyRead = await readResponseBodyForRegression(response)
    const afterHeap = usedHeapBytes()

    assert(response.status === 200, `大响应网关请求应成功，实际 HTTP ${response.status}: ${bodyRead.preview}`)
    assert(bodyRead.markerFound, '客户端没有收到完整大响应标记')
    assert(bodyRead.bytes > largeFieldSizeBytes, `客户端响应长度异常：${bodyRead.bytes}`)
    assert(upstreamHitCount === 1, `上游命中次数异常：${upstreamHitCount}`)

    usageRecordQueue.flushAllUsageRecordQueue()
    const usageRecords = repositories.listUsageRecords(undefined, { page: 1, pageSize: 20 }) as UsageRecordListResult
    const usageRecord = usageRecords.items.find((item) => item.apiKeyId === apiKey.id && item.accountId === account.id)
    assert(usageRecord, '未写入大响应网关使用记录')
    assert(usageRecord.success === true, '大响应网关使用记录应为成功')
    assert(usageRecord.inputTokens === 3 && usageRecord.outputTokens === 5, '大响应 usage 尾部解析异常')

    const heapDelta = afterHeap - beforeHeap
    assert(heapDelta < largeFieldSizeBytes * 2, `大响应请求后堆内存增长过高：${heapDelta} bytes`)

    console.log(`非流式大响应回归通过：完整转发 ${bodyRead.bytes} bytes，堆增长 ${Math.max(0, heapDelta)} bytes`)
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

let upstreamHitCount = 0
const largeOutputMarker = 'large_response_regression_marker'

function createLargeResponseUpstreamServer(): http.Server {
  return http.createServer((req, res) => {
    upstreamHitCount += 1
    if (req.url !== '/v1/responses') {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ error: { message: 'not found' } }))
      return
    }

    res.writeHead(200, { 'content-type': 'application/json' })
    res.write('{"id":"resp_large","object":"response","status":"completed","model":"gpt-4o-mini","output_text":"')
    res.write('x'.repeat(largeFieldSizeBytes))
    res.write(largeOutputMarker)
    res.write('","usage":{"input_tokens":3,"output_tokens":5}}')
    res.end()
  })
}

function captureGatewayRawBody(req: RawBodyRequest, _res: ExpressResponse, next: NextFunction): void {
  const rawBody = Buffer.isBuffer(req.body) ? Buffer.from(req.body) : Buffer.alloc(0)
  req.rawBody = rawBody
  const contentType = req.headers['content-type'] ?? ''
  if (rawBody.length > 0 && String(contentType).toLowerCase().includes('json')) {
    try {
      req.body = JSON.parse(rawBody.toString('utf8')) as unknown
    } catch {
      req.body = undefined
    }
  } else {
    req.body = undefined
  }
  next()
}

async function readResponseBodyForRegression(response: Response): Promise<{ bytes: number; markerFound: boolean; preview: string }> {
  assert(response.body, '响应体为空')
  const markerBytes = Buffer.from(largeOutputMarker)
  const reader = response.body.getReader()
  let bytes = 0
  let markerFound = false
  let tail = Buffer.alloc(0)
  let preview = ''

  while (true) {
    const result = await reader.read()
    if (result.done) {
      break
    }

    const buffer = Buffer.from(result.value)
    bytes += buffer.length
    if (preview.length < 200) {
      preview += buffer.toString('utf8', 0, Math.min(buffer.length, 200 - preview.length))
    }
    if (!markerFound) {
      const combined = Buffer.concat([tail, buffer], tail.length + buffer.length)
      markerFound = combined.includes(markerBytes)
      tail = combined.subarray(Math.max(0, combined.length - markerBytes.length + 1))
    }
  }

  return { bytes, markerFound, preview }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function usedHeapBytes(): number {
  if (global.gc) {
    global.gc()
  }
  return process.memoryUsage().heapUsed
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\n非流式大响应回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
