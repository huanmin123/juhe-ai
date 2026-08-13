import { strict as assert } from 'node:assert'
import { createHmac } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

const original = new Map<string, string | undefined>()
for (const name of [
  'JUHE_AI_OPERATION_LOG_INPUT_URL',
  'JUHE_AI_OPERATION_LOG_INPUT_SECRET',
  'JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS',
  'JUHE_AI_LOG_FILE_ENABLED',
  'JUHE_AI_LOG_CONSOLE_ENABLED',
  'NODE_ENV'
]) original.set(name, process.env[name])

const secret = 'f4-operation-log-go-rpc-regression-secret'
process.env.JUHE_AI_OPERATION_LOG_INPUT_SECRET = secret
process.env.JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS = '1000'
process.env.JUHE_AI_LOG_FILE_ENABLED = 'false'
process.env.JUHE_AI_LOG_CONSOLE_ENABLED = 'false'
process.env.NODE_ENV = 'test'

let responseStatus = 204
let requests: ReceivedRequest[] = []
const server = createServer(handleRequest)
await listen(server)
process.env.JUHE_AI_OPERATION_LOG_INPUT_URL = `http://127.0.0.1:${addressPort(server)}`

try {
  const { dispatchOperationLogToGo, listOperationLogsFromGo, normalizeOperationLogRpcPayload, operationLogGoInputPath } = await import('../../modules/operation-logs/operation-log-go-input.service.js')

  const createdAt = '2026-08-13T00:00:00.000Z'
  await dispatchOperationLogToGo({
    id: 'oplog-fixed-id',
    createdAt,
    actorSystemAccountId: 'sys-admin',
    actorRole: 'admin',
    module: 'accounts',
    action: 'update',
    operationKey: 'accounts.update',
    resourceType: 'account',
    summary: 'fixed input'
  })
  assert.equal(requests.length, 1, 'F4 写入必须为单次提交')
  const write = requests[0]
  assert.equal(write.method, 'POST')
  assert.equal(write.url, operationLogGoInputPath)
  assert.ok(write.timestamp, 'F4 RPC 必须发送签名 timestamp')
  assert.ok(write.nonce, 'F4 RPC 必须发送签名 nonce')
  assert.equal(write.signature, signature(write.timestamp!, write.nonce!, write.body), 'F4 签名必须覆盖固定 domain、timestamp、nonce 与原始 JSON')
  const writeEnvelope = JSON.parse(write.body.toString('utf8')) as { schemaVersion: number; operationLog: { id: string; createdAt: string } }
  assert.equal(writeEnvelope.schemaVersion, 1)
  assert.equal(writeEnvelope.operationLog.id, 'oplog-fixed-id', 'Node 不得重写稳定 ID')
  assert.equal(writeEnvelope.operationLog.createdAt, createdAt, 'Node 不得重写稳定 createdAt')

  const cyclic: { self?: unknown } = {}
  cyclic.self = cyclic
  const safePayload = normalizeOperationLogRpcPayload({ schemaVersion: 1, operationLog: { metadata: cyclic, changes: [cyclic] } }) as { operationLog: { metadata: unknown; changes: unknown[] } }
  assert.deepEqual(safePayload.operationLog.metadata, {}, '不可序列化 metadata 必须在 Node 发送边界降级为空对象')
  assert.deepEqual(safePayload.operationLog.changes, [], '不可序列化 changes 必须在 Node 发送边界降级为空数组')

  await listOperationLogsFromGo({ page: 1, pageSize: 20 }, 'viewer-1')
  assert.equal(requests.length, 2)
  const list = JSON.parse(requests[1].body.toString('utf8')) as { options: { viewerId?: string } }
  assert.equal(list.options.viewerId, 'viewer-1', '个人列表必须透传已认证 viewerId')

  responseStatus = 500
  await assert.rejects(() => dispatchOperationLogToGo({
    id: 'oplog-rejected',
    createdAt,
    actorSystemAccountId: 'sys-admin',
    actorRole: 'admin',
    module: 'accounts',
    action: 'update',
    operationKey: 'accounts.update',
    resourceType: 'account',
    summary: 'rejected'
  }))
  assert.equal(requests.length, 3, '失败不得触发 retry、fallback 或双写')

  const serviceSource = readFileSync(new URL('../../modules/operation-logs/operation-log.service.ts', import.meta.url), 'utf8')
  const routesSource = readFileSync(new URL('../../modules/operation-logs/operation-logs.routes.ts', import.meta.url), 'utf8')
  const externalIntegrationSource = readFileSync(new URL('../../modules/external-integrations/external-integrations.routes.ts', import.meta.url), 'utf8')
  assert.match(serviceSource, /void dispatchOperationLogToGo\([\s\S]*?\.catch\(/, '同步业务路径必须 best-effort 提交 Go')
  assert.match(serviceSource, /producer: 'node_operation_log_service'[\s\S]*module: input\.module[\s\S]*action: input\.action[\s\S]*errorClass:/, 'Node 日志失败必须记录 producer、module/action 与错误类别')
  assert.match(serviceSource, /const operationLogId = input\.id \?\? newId\('oplog'\)/, 'Node 日志失败必须记录实际生成的 operation log ID')
  assert.doesNotMatch(serviceSource, /enqueueOperationLog|operation-log-queue|createOperationLog/, '操作日志 service 不得回退 Node queue 或 repository')
  assert.match(routesSource, /listOperationLogsFromGo\(parseOperationLogListOptions\(req\.query, false\), context\.systemAccountId\)/, '个人列表必须从已认证上下文传递 viewerId')
  assert.match(routesSource, /getOperationLogDetailFromGo\(req\.params\.id, context\.systemAccountId\)/, '个人详情必须从已认证上下文传递 viewerId')
  assert.doesNotMatch(routesSource, /listOperationLogsForViewerAsync|getOperationLogDetailSupplementForViewerAsync|listOperationLogsAsync|getOperationLogDetailSupplementAsync/, '读取路由不得保留 Node read repository')
  assert.match(externalIntegrationSource, /recordOperationLogAsync/, '外部集成写入成功后必须复用 F4 Go dispatch service')
  assert.doesNotMatch(externalIntegrationSource, /createOperationLogAsync/, '外部集成不得直写 Node operation log repository')
  console.log('F4 Go RPC regression passed: signed 204 one-shot write, fixed ID/createdAt, viewer list wiring, and no retry/fallback.')
} finally {
  await close(server)
  for (const [name, value] of original) {
    if (value === undefined) delete process.env[name]
    else process.env[name] = value
  }
}

interface ReceivedRequest {
  method: string | undefined
  url: string | undefined
  signature: string | undefined
  timestamp: string | undefined
  nonce: string | undefined
  body: Buffer
}

function handleRequest(request: IncomingMessage, response: ServerResponse): void {
  const chunks: Buffer[] = []
  request.on('data', (chunk: Buffer | string) => chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)))
  request.on('end', () => {
    const body = Buffer.concat(chunks)
    requests.push({
      method: request.method,
      url: request.url,
      signature: headerValue(request.headers['x-juhe-ai-signature']),
      timestamp: headerValue(request.headers['x-juhe-ai-timestamp']),
      nonce: headerValue(request.headers['x-juhe-ai-nonce']),
      body
    })
    if (request.url?.endsWith('/list')) {
      response.setHeader('content-type', 'application/json')
      response.end(JSON.stringify({ items: [], total: 0, hasMore: false, page: 1, pageSize: 20 }))
      return
    }
    response.statusCode = responseStatus
    response.end()
  })
}

function signature(timestamp: string, nonce: string, body: Buffer): string {
  return `v1=${createHmac('sha256', secret).update('juhe-ai/operation-log-input/v1').update('\n').update(timestamp).update('\n').update(nonce).update('\n').update(body).digest('hex')}`
}

function headerValue(value: string | string[] | undefined): string | undefined {
  return Array.isArray(value) ? value.at(-1) : value
}

function listen(serverInstance: Server): Promise<void> {
  return new Promise((resolve, reject) => serverInstance.listen(0, '127.0.0.1', (error?: Error) => error ? reject(error) : resolve()))
}

function addressPort(serverInstance: Server): number {
  const address = serverInstance.address()
  if (!address || typeof address === 'string') throw new Error('fake F4 Go RPC server has no TCP address')
  return address.port
}

function close(serverInstance: Server): Promise<void> {
  return new Promise((resolve, reject) => serverInstance.close((error) => error ? reject(error) : resolve()))
}
