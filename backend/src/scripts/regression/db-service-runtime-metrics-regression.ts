import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import type { DbServiceParentMessage, DbServiceRuntimeSnapshot } from '../../modules/db-service/db-service-types.js'

class FakeDbServiceProcess extends EventEmitter {
  connected = true
  killed = false

  constructor(
    readonly pid: number,
    private readonly snapshot: DbServiceRuntimeSnapshot
  ) {
    super()
  }

  send(message: DbServiceParentMessage, callback?: (error?: Error | null) => void): boolean {
    callback?.(null)
    if (message.type === 'db_service_request' && message.operation.type === 'status') {
      setImmediate(() => {
        this.emit('message', {
          type: 'db_service_response',
          requestId: message.requestId,
          ok: true,
          result: this.snapshot
        })
      })
    }
    return true
  }

  kill(): boolean {
    this.killed = true
    this.connected = false
    return true
  }

  ready(): void {
    this.emit('message', {
      type: 'db_service_ready',
      pid: this.pid,
      httpHost: this.snapshot.httpHost,
      httpPort: this.snapshot.httpPort
    })
  }
}

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false

const handlers = await import('../../modules/db-service/db-service-handlers.js')

handlers.setDbServiceQueueRuntimeProvider(() => ({
  queuedRequestCount: 7,
  queuedHighRequestCount: 1,
  queuedNormalRequestCount: 4,
  queuedLowRequestCount: 2,
  oldestQueuedMs: 900,
  lastQueueWaitMs: 12,
  maxQueueWaitMs: 34
}))

const localSnapshot = handlers.buildDbServiceRuntimeSnapshot(51001)
assert.equal(localSnapshot.queuedRequestCount, 7, 'DB service status snapshot 应包含总排队数量')
assert.equal(localSnapshot.queuedHighRequestCount, 1, 'DB service status snapshot 应包含高优先级排队数量')
assert.equal(localSnapshot.oldestQueuedMs, 900, 'DB service status snapshot 应包含最老排队等待时间')

await handlers.handleDbServiceOperation({ type: 'status' })
const handledSnapshot = handlers.buildDbServiceRuntimeSnapshot(51001)
assert(handledSnapshot.lastExecMs !== undefined && handledSnapshot.lastExecMs >= 0, 'DB service status snapshot 应记录最近执行耗时')
assert(handledSnapshot.maxExecMs !== undefined && handledSnapshot.maxExecMs >= handledSnapshot.lastExecMs!, 'DB service status snapshot 应记录最大执行耗时')

runtimeConfig.processRole = 'server'
const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')
const fakeChild = new FakeDbServiceProcess(51002, {
  ...handledSnapshot,
  slowOpCount: 2,
  lastSlowOpType: 'list_runtime_logs',
  lastSlowOpMs: 650,
  lastSlowOpAt: '2026-06-23T00:00:00.000Z'
})
dbServiceIpc.attachDbServiceProcess(fakeChild as unknown as ChildProcess)
fakeChild.ready()
const remoteSnapshot = await dbServiceIpc.requestDbService({ type: 'status' }, { timeoutMs: 1000 })
assert.equal(remoteSnapshot?.queuedRequestCount, 7, '父进程请求 DB service status 应返回 queue metrics')
const state = dbServiceIpc.getDbServiceState()
assert.equal(state.lastSnapshot?.queuedRequestCount, 7, '父进程 DB service state 应缓存 queue metrics')
assert.equal(state.lastSnapshot?.lastExecMs, handledSnapshot.lastExecMs, '父进程 DB service state 应缓存执行耗时')
assert.equal(state.lastSnapshot?.slowOpCount, 2, '父进程 DB service state 应缓存慢操作计数')
assert.equal(state.pendingDatasetWriteRequestCount, 0, '父进程 DB service state 应暴露 dataset write pending 数')
assert.equal(state.oldestDatasetWriteRequestMs, 0, '父进程 DB service state 应暴露 dataset write 最老等待时间')

console.log('DB service runtime metrics 回归通过：queue/exec/slow/pending dataset writer 指标可生成并进入父进程状态')
