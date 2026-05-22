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

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    void message
    this.sentMessageCount += 1
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

  await assert.rejects(
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 10 }),
    /DB service 请求超时/,
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
    /DB service 请求超时/,
    '后续普通请求仍应独立按自身 timeout 失败，而不是被全局熔断拦截'
  )
  assert.equal(child.sentMessageCount, 2, '单次超时后仍应允许后续请求继续发送给 DB service')

  console.log('DB service IPC 超时恢复回归通过：普通请求超时只失败当前请求，不终止 child、不打开全局熔断')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}
