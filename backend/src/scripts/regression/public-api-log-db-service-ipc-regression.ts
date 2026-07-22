import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

class FakeDbServiceProcess extends EventEmitter {
  connected = true
  killed = false
  pid = 52001

  send(_message: unknown, callback?: (error?: Error | null) => void): boolean {
    callback?.(null)
    return true
  }

  kill(): boolean {
    this.killed = true
    this.connected = false
    return true
  }
}

runtimeConfig.processRole = 'server'
const [backgroundIpc, dbServiceIpc] = await Promise.all([
  import('../../modules/background/background-ipc.js'),
  import('../../modules/db-service/db-service-ipc.js')
])

const fakeDbService = new FakeDbServiceProcess()
dbServiceIpc.attachDbServiceProcess(fakeDbService as unknown as ChildProcess)
fakeDbService.emit('message', {
  type: 'background_worker_public_api_logs',
  items: [publicApiLogFixture('trace-public-api-db-service-forward')]
})
await waitForPublicApiLogIpcQueueLength(backgroundIpc, 1)
assert.equal(
  backgroundIpc.getBackgroundWorkerState().pendingQueues.publicApiLogs.queueLength,
  1,
  'server 应把 DB service 转发的公开接口日志投递到 ingest-worker IPC 队列'
)

runtimeConfig.processRole = 'db-service'
const publicApiLogQueue = await import('../../modules/public-api-logs/public-api-log-queue.service.js')
const originalSend = process.send
try {
  let sentMessage: unknown
  process.send = ((message: unknown, callback?: (error?: Error | null) => void) => {
    sentMessage = message
    callback?.(null)
    return true
  }) as NodeJS.Process['send']

  assert.equal(
    publicApiLogQueue.enqueuePublicApiLog(publicApiLogFixture('trace-public-api-db-service-send')),
    true,
    'DB service 公开接口日志应通过父进程 IPC 投递'
  )
  assert.equal((sentMessage as { type?: unknown }).type, 'background_worker_public_api_logs', 'DB service 应发送公开接口日志 worker IPC 消息')

  process.send = (() => {
    throw new Error('模拟父进程 IPC 已关闭')
  }) as NodeJS.Process['send']
  const before = publicApiLogQueue.getPublicApiLogQueueRuntime().droppedCount
  assert.doesNotThrow(() => {
    assert.equal(
      publicApiLogQueue.enqueuePublicApiLog(publicApiLogFixture('trace-public-api-db-service-ipc-closed')),
      false,
      '父进程 IPC 同步异常时应返回 false'
    )
  }, 'DB service 公开接口日志投递 IPC 断开时不应抛出异常')
  assert.equal(
    publicApiLogQueue.getPublicApiLogQueueRuntime().droppedCount,
    before + 1,
    'DB service 公开接口日志 IPC 投递失败应累计 droppedCount'
  )
} finally {
  process.send = originalSend
}

console.log('公开接口日志 DB service IPC 回归通过：DB service 可把公开接口日志转发给 server，再进入 ingest-worker 队列')

function publicApiLogFixture(traceId: string): PublicApiLogInput {
  return {
    traceId,
    method: 'GET',
    path: '/__aipublic__/group/list',
    statusCode: 200,
    success: true,
    durationMs: 1,
    requestData: {},
    responseData: {},
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function waitForPublicApiLogIpcQueueLength(
  backgroundIpc: typeof import('../../modules/background/background-ipc.js'),
  expected: number
): Promise<void> {
  const deadline = Date.now() + 1000
  while (Date.now() < deadline) {
    if (backgroundIpc.getBackgroundWorkerState().pendingQueues.publicApiLogs.queueLength >= expected) {
      return
    }
    await sleep(20)
  }
}
