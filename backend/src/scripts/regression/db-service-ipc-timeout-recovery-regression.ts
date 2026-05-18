import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-ipc-timeout-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
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
  assert.equal(child.killed, true, 'DB service 普通请求超时应终止当前 child 触发 supervisor 重启')
  assert.equal(child.killSignal, 'SIGTERM', '超时恢复应使用 SIGTERM 结束异常 child')
  assert.equal(stateAfterTimeout.ready, false, '超时后 DB service ready 状态应关闭')
  assert.equal(stateAfterTimeout.pendingRequestCount, 0, '超时后普通请求 pending 应清空')
  assert(stateAfterTimeout.timedOutRequestCount >= 1, '超时计数应递增，便于运行态排障')

  await assert.rejects(
    dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 10 }),
    /DB service (暂时不可用|未就绪|请求队列已满)/,
    '异常 child 被终止后，后续请求应快速失败等待 supervisor 重新挂载'
  )
  assert.equal(child.sentMessageCount, 1, '超时后的请求不应继续发送给同一个异常 child')

  console.log('DB service IPC 超时恢复回归通过：普通请求超时会终止异常 child 并快速失败')
} finally {
  rmSync(tempRoot, { recursive: true, force: true })
}
