import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { KeyedChildProcessPool } from '../../shared/keyed-child-process-pool.js'

interface TestOperation {
  id: string
}

interface WorkerMessage {
  requestId: string
  operation: TestOperation
}

class FakeWorker extends EventEmitter {
  connected = true
  killed = false
  exitCode: number | null = null
  received: WorkerMessage[] = []

  constructor(private readonly mode: 'stall' | 'reply') {
    super()
  }

  send(message: WorkerMessage, callback?: (error?: Error | null) => void): boolean {
    this.received.push(message)
    callback?.(null)
    if (this.mode === 'reply') {
      setImmediate(() => {
        this.emit('message', {
          requestId: message.requestId,
          ok: true,
          result: `handled:${message.operation.id}`
        })
      })
    }
    return true
  }

  disconnect(): void {
    this.connected = false
    this.exitCode = 0
    setImmediate(() => this.emit('exit', 0))
  }

  kill(): boolean {
    this.killed = true
    this.connected = false
    this.exitCode = 0
    setImmediate(() => this.emit('exit', 0))
    return true
  }
}

const workers: FakeWorker[] = []
const pool = new KeyedChildProcessPool<TestOperation>({
  name: 'watchdog-test',
  createWorker: () => {
    const worker = new FakeWorker(workers.length === 0 ? 'stall' : 'reply')
    workers.push(worker)
    return worker as unknown as ChildProcess
  },
  targetSize: () => 1,
  queueMaxItems: () => 10,
  shardIndexForOperation: () => 0,
  operationType: (operation) => operation.id,
  runTimeoutMs: () => 25
})
const keepAlive = setInterval(() => undefined, 50)

try {
  const first = pool.request({ id: 'stall-first-job' })
  const second = pool.request({ id: 'second-job' })

  await assert.rejects(first, /watchdog-test writer 操作超时 25ms/, '卡死 active job 应按 watchdog 超时失败')
  const secondResult = await second
  assert.equal(secondResult, 'handled:second-job', 'watchdog 重启 worker 后应继续处理队列里的后续 job')

  const runtime = pool.runtime()
  assert.equal(runtime.timedOutJobs, 1, 'watchdog 应记录超时 job 数')
  assert.equal(runtime.failedJobs, 1, '超时 job 应计入失败数')
  assert.equal(runtime.restartedWorkers, 1, 'watchdog 应重启卡死 worker')
  assert.equal(runtime.handledJobs, 1, '重启后的 worker 应成功处理第二个 job')
  assert.equal(runtime.queueLength, 0, '队列应被清空')
  assert.equal(runtime.activeJobs, 0, '超时后不应残留 active job')
  assert.equal(workers.length, 2, '首个 worker 卡死后应创建替换 worker')
  assert.equal(workers[0]?.connected, false, '卡死 worker 应被断开')

  const spreadWorkers: FakeWorker[] = []
  const spreadPool = new KeyedChildProcessPool<TestOperation>({
    name: 'least-loaded-test',
    createWorker: () => {
      const worker = new FakeWorker('reply')
      spreadWorkers.push(worker)
      return worker as unknown as ChildProcess
    },
    targetSize: () => 2,
    queueMaxItems: () => 10,
    shardIndexForOperation: () => 0,
    operationType: (operation) => operation.id,
    slotSelection: 'least-loaded'
  })
  try {
    await Promise.all([
      spreadPool.request({ id: 'same-key-a' }),
      spreadPool.request({ id: 'same-key-b' })
    ])
    assert.equal(spreadWorkers.length, 2, 'least-loaded 模式应初始化目标 worker 数')
    assert.equal(spreadWorkers[0]?.received.length, 1, 'least-loaded 模式不应把同 key 读请求全部压到第一个 worker')
    assert.equal(spreadWorkers[1]?.received.length, 1, 'least-loaded 模式应把连续同 key 读请求分散到空闲 worker')
  } finally {
    await spreadPool.close()
  }

  console.log('keyed child process pool watchdog 回归通过：active job 超时后释放槽位、重启 worker 并继续处理队列')
} finally {
  clearInterval(keepAlive)
  await pool.close()
}
