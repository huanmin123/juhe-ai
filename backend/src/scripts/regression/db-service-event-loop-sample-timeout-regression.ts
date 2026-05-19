import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'
logger.level = 'silent'

const dbServiceIpc = await import('../../modules/db-service/db-service-ipc.js')

class HungDbServiceChild extends EventEmitter {
  pid = 616161
  connected = true
  killed = false
  killSignal: NodeJS.Signals | undefined
  sampleRequestCount = 0

  send(message: unknown, callback?: (error?: Error | null) => void): boolean {
    if (isProcessEventLoopRequest(message)) {
      this.sampleRequestCount += 1
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

const child = new HungDbServiceChild()
dbServiceIpc.attachDbServiceProcess(child as never)
child.emit('message', {
  type: 'db_service_ready',
  pid: child.pid,
  httpHost: '127.0.0.1',
  httpPort: 1
})

const first = await dbServiceIpc.requestDbServiceProcessEventLoopSample(10)
assert.equal(first, undefined, '首次 DB service 事件循环采样超时应返回 undefined')
assert.equal(child.killed, false, '单次采样超时不应立刻终止 DB service')
assert.equal(dbServiceIpc.getDbServiceState().processEventLoopTimeoutStreak, 1, '采样超时 streak 应递增')

const second = await dbServiceIpc.requestDbServiceProcessEventLoopSample(10)
assert.equal(second, undefined, '第二次 DB service 事件循环采样超时应返回 undefined')
assert.equal(child.killed, false, '连续采样超时不应终止 DB service')
assert.equal(dbServiceIpc.getDbServiceState().processEventLoopTimeoutStreak, 2, '连续采样超时 streak 应累计')

const third = await dbServiceIpc.requestDbServiceProcessEventLoopSample(10)
assert.equal(third, undefined, '第三次 DB service 事件循环采样超时应返回 undefined')
const stateAfterTimeouts = dbServiceIpc.getDbServiceState()
assert.equal(child.killed, false, '监控采样连续超时也不应终止 DB service child')
assert.equal(child.killSignal, undefined, '监控采样超时不应触发终止信号')
assert.equal(stateAfterTimeouts.ready, true, '监控采样超时不应关闭 DB service ready 状态')
assert.equal(stateAfterTimeouts.pendingProcessEventLoopRequestCount, 0, '连续采样超时后采样 pending 应清空')
assert.equal(stateAfterTimeouts.timedOutProcessEventLoopRequestCount, 3, '采样超时计数应记录所有超时')
assert.equal(stateAfterTimeouts.processEventLoopTimeoutStreak, 3, '采样超时 streak 应记录连续缺失次数')
assert.equal(child.sampleRequestCount, 3, '测试应实际发送三次采样请求')

console.log('DB service 事件循环采样超时回归通过：采样超时只记录指标，不打断 DB service')

function isProcessEventLoopRequest(value: unknown): value is { type: 'db_service_process_event_loop_request'; requestId: string } {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).type === 'db_service_process_event_loop_request'
    && typeof (value as Record<string, unknown>).requestId === 'string'
}
