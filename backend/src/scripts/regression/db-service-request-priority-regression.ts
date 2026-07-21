import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { withRequestContext, type RequestContext } from '../../shared/request-context.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-request-priority-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'db-service-request-priority-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')
const backgroundIpc = await import('../../modules/background/background-ipc.js')
const gatewayDbService = await import('../../modules/gateway/runtime/gateway-db-service-request.js')

class CapturingDbServiceChild extends EventEmitter {
  pid = 515252
  connected = true
  sentMessages: unknown[] = []

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    this.sentMessages.push(message)
    callback?.()
    const record = message as { type?: string; requestId?: string; jobId?: string }
    if (record.type === 'db_service_request' && record.requestId) {
      setImmediate(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: record.requestId,
          jobId: record.jobId,
          ok: true,
          result: {
            pid: this.pid,
            ready: true,
            processRole: 'db-service',
            pendingRequestCount: 0,
            handledRequestCount: 1,
            failedRequestCount: 0
          }
        })
      })
    }
    return true
  }

  kill(): boolean {
    this.connected = false
    return true
  }
}

try {
  const child = new CapturingDbServiceChild()
  dbServiceIpc.attachDbServiceProcess(child as unknown as ChildProcess)
  child.emit('message', {
    type: 'db_service_ready',
    pid: child.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  await dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 })
  const directMessage = requestMessages(child)[0]
  assert.equal(directMessage?.priority, undefined, '普通业务 DB service 请求不应默认带显式 priority，仍由操作类型决定优先级')
  assert.equal(directMessage?.requestId, directMessage?.jobId, '非 HTTP 调用必须保持原 requestId 等于本次 IPC UUID 的兼容语义')
  assert.equal(directMessage?.operationId, directMessage?.jobId, '非 HTTP 调用也必须以 IPC UUID 标识 operationId')
  assert.equal(directMessage?.parentId, undefined, '非 HTTP 调用没有根请求时不得伪造 parentId')

  await dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000, priority: 'low' })
  const explicitMessage = requestMessages(child)[1]
  assert.equal(explicitMessage?.priority, 'low', 'requestDbService 显式 priority 必须进入 IPC 消息')

  await backgroundIpc.requestBackgroundWorkerDbService({ type: 'status' }, 1000)
  const backgroundReadMessage = requestMessages(child)[2]
  assert.equal(backgroundReadMessage?.priority, undefined, '后台纯读 DB service 请求不应被来源身份强制降为 low')

  await backgroundIpc.requestBackgroundWorkerDbService({
    type: 'persist_openai_codex_usage_headers',
    accountId: 'acct_priority_regression',
    headers: {},
    source: 'regression'
  }, 1000)
  const backgroundWriteMessage = requestMessages(child)[3]
  assert.equal(backgroundWriteMessage?.priority, 'low', 'server 进程内后台 DB service 写入请求必须以 low priority 入队')

  await gatewayDbService.requestGatewayDbService({ type: 'status' }, { timeoutMs: 1000 })
  const gatewayReadMessage = requestMessages(child)[4]
  assert.equal(gatewayReadMessage?.priority, undefined, '网关纯读 DB service 请求不应被封装层强制降为 low')

  await gatewayDbService.requestGatewayDbService({
    type: 'clear_account_stream_failure_state',
    accountId: 'acct_priority_regression'
  }, {
    timeoutMs: 1000,
    priority: 'low'
  })
  const gatewayWriteMessage = requestMessages(child)[5]
  assert.equal(gatewayWriteMessage?.priority, 'low', '网关写副作用 DB service 请求必须透传显式 low priority')

  const rootRequestContext: RequestContext = {
    traceId: 'trace-db-service-correlation',
    requestId: 'request-db-service-correlation',
    startedAt: Date.now(),
    monotonicStartedAt: performance.now(),
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    logger
  }
  await withRequestContext(rootRequestContext, () => dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 }))
  const correlatedMessage = requestMessages(child)[6]
  assert.equal(correlatedMessage?.traceId, rootRequestContext.traceId, 'DB service IPC 必须传播根 HTTP traceId')
  assert.equal(correlatedMessage?.requestId, rootRequestContext.requestId, 'DB service IPC requestId 必须是根 HTTP requestId')
  assert.equal(typeof correlatedMessage?.jobId, 'string', 'DB service IPC 必须为本次 IPC 分配独立 jobId')
  assert.notEqual(correlatedMessage?.jobId, correlatedMessage?.requestId, 'HTTP 请求内 DB service IPC jobId 不得复用根 requestId')
  assert.equal(correlatedMessage?.operationId, correlatedMessage?.jobId, 'DB service IPC operationId 必须标识本次 IPC 操作')
  assert.equal(correlatedMessage?.parentId, rootRequestContext.requestId, 'DB service IPC parentId 必须回指根 HTTP requestId')

  await withRequestContext(rootRequestContext, () => Promise.all([
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 }),
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 })
  ]))
  const concurrentMessages = requestMessages(child).slice(7, 9)
  assert.equal(concurrentMessages.length, 2, '同一 HTTP 请求内两个并发 DB service IPC 都必须发出')
  assert(concurrentMessages.every((message) => message.requestId === rootRequestContext.requestId), '并发 IPC 必须共享根 HTTP requestId')
  assert.notEqual(concurrentMessages[0]?.jobId, concurrentMessages[1]?.jobId, '并发 IPC 必须使用不同 jobId，避免 pending 响应串线')

  console.log('DB service 请求优先级回归通过：纯读请求保持默认优先级，显式 priority 保留，后台/网关写副作用请求显式 low')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}

function requestMessages(child: CapturingDbServiceChild): Array<Record<string, unknown>> {
  return child.sentMessages.filter((message): message is Record<string, unknown> => {
    return typeof message === 'object'
      && message !== null
      && (message as { type?: unknown }).type === 'db_service_request'
  })
}
