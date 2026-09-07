import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-ipc-timeout-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'db-service-ipc-timeout-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')

class HungDbServiceChild extends EventEmitter {
  pid = 515151
  connected = true
  killed = false
  killSignal: NodeJS.Signals | undefined
  sentMessageCount = 0
  cacheInvalidationResponseDelayMs: number | undefined

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    this.sentMessageCount += 1
    const record = message as { type?: unknown, requestId?: unknown, operation?: { type?: unknown } }
    if (
      record.type === 'db_service_request'
      && record.operation?.type === 'clear_gateway_runtime_cache'
      && typeof record.requestId === 'string'
      && this.cacheInvalidationResponseDelayMs !== undefined
    ) {
      setTimeout(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: record.requestId,
          ok: true,
          result: { cleared: true }
        })
      }, this.cacheInvalidationResponseDelayMs)
    }
    callback?.()
    return true
  }

  kill(signal?: NodeJS.Signals): boolean {
    this.killed = true
    this.connected = false
    this.killSignal = signal
    return true
  }
}

try {
  const child = new HungDbServiceChild()
  dbServiceIpc.attachDbServiceProcess(child as never)
  child.emit('message', {
    type: 'db_service_ready',
    pid: child.pid,
    httpHost: '127.0.0.1',
    httpPort: 1
  })

  assert.equal(dbServiceIpc.getDbServiceState().ready, true, '测试前 DB service fake child 应处于 ready')

  const timedOutBeforeInvalidation = dbServiceIpc.getDbServiceState().timedOutRequestCount
  child.cacheInvalidationResponseDelayMs = 4_500
  dbServiceIpc.clearDbServiceGatewayRuntimeCache()
  await new Promise((resolve) => setTimeout(resolve, 4_700))
  assert.equal(
    dbServiceIpc.getDbServiceState().timedOutRequestCount,
    timedOutBeforeInvalidation,
    '缓存失效通知应容忍生产观测到的 server 事件循环峰值，不能把 4.5s 的健康 DB service 响应记为超时'
  )
  assert.equal(
    dbServiceIpc.getDbServiceState().pendingRequestCount,
    0,
    '缓存失效健康响应应按 requestId 清理 pending，不能只依赖尚未触发的 10s timeout 假通过'
  )

  await assert.rejects(
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 10 }),
    /本地数据库服务请求超时/,
    'DB service 普通请求超时应拒绝调用'
  )

  const stateAfterTimeout = dbServiceIpc.getDbServiceState()
  assert.equal(child.killed, false, 'DB service 单个普通请求超时不应终止当前 child，避免慢请求放大全局 503')
  assert.equal(child.killSignal, undefined, '单个普通请求超时不应触发终止信号')
  assert.equal(stateAfterTimeout.ready, true, '单个普通请求超时后 DB service ready 状态应保持')
  assert.equal(stateAfterTimeout.pendingRequestCount, 0, '超时后普通请求 pending 应清空')
  assert.equal(stateAfterTimeout.unavailableCircuitOpenUntil, undefined, '单个普通请求超时不应打开 DB service 全局不可用熔断')
  assert(stateAfterTimeout.timedOutRequestCount >= 1, '超时计数应递增，便于运行态排障')

  await assert.rejects(
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 10 }),
    /本地数据库服务请求超时/,
    '后续普通请求仍应独立按自身 timeout 失败，而不是被全局熔断拦截'
  )
  assert.equal(child.sentMessageCount, 3, '缓存失效与单次超时后仍应允许后续请求继续发送给 DB service')

  const pendingRequests = Array.from({ length: 2000 }, () => {
    return dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 }).catch((error) => error)
  })
  await assert.rejects(
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 }),
    /请求队列已满/,
    'DB service pending 请求达到上限后应快速拒绝新请求'
  )
  const stateAfterQueueFull = dbServiceIpc.getDbServiceState()
  assert.equal(stateAfterQueueFull.pendingRequestCount, 2000, '队列满时 pending 数应保持在保护上限')
  assert(stateAfterQueueFull.rejectedRequestCount >= 1, '队列满快速拒绝应记录 rejected 计数')
  assert.equal(stateAfterQueueFull.unavailableCircuitOpenUntil, undefined, '队列满快速拒绝不应打开 DB service 全局不可用熔断')
  await Promise.all(pendingRequests)
  assert.equal(dbServiceIpc.getDbServiceState().pendingRequestCount, 0, '队列保护回归结束后 pending 请求应按 timeout 清空')

  console.log('DB service IPC 超时恢复回归通过：普通请求超时只失败当前请求，pending 达上限会快速拒绝且不打开全局熔断')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}
