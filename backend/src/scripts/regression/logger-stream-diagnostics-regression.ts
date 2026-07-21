import { strict as assert } from 'node:assert'
import { Writable } from 'node:stream'

import { createObservedLogStreamForTest } from '../../shared/logger.js'

class SlowWritable extends Writable {
  private callbacks: Array<(error?: Error | null) => void> = []
  private released = false

  constructor() {
    super({ highWaterMark: 1 })
  }

  _write(_chunk: Buffer, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    if (this.released) {
      setImmediate(callback)
      return
    }
    this.callbacks.push(callback)
  }

  release(): void {
    this.released = true
    for (const callback of this.callbacks.splice(0)) callback()
  }
}

class FailingWritable extends Writable {
  constructor() {
    super({ highWaterMark: 1 })
  }

  _write(_chunk: Buffer, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    const error = Object.assign(new Error('磁盘空间不足'), {
      code: 'ENOSPC',
      syscall: 'write',
      path: 'C:\\logs\\juhe-ai.log'
    })
    setImmediate(() => callback(error))
  }
}

const uncaughtErrors: unknown[] = []
const onUncaughtException = (error: unknown) => uncaughtErrors.push(error)
process.on('uncaughtException', onUncaughtException)

try {
  const slowDestination = new SlowWritable()
  const emergencyFailureSnapshots: Array<{ level: string; previewBytes: number }> = []
  const slowObserved = createObservedLogStreamForTest([
    { name: 'slow-file', stream: slowDestination }
  ], {
    maxPendingBytes: 32,
    failureReserveBytes: 64,
    maxFailureSnapshotBytes: 32,
    onFailureDrop: (drop) => emergencyFailureSnapshots.push({
      level: drop.level,
      previewBytes: drop.previewBytes
    })
  })

  slowObserved.stream.write(Buffer.from('slow-log-line\n'))
  for (let index = 0; index < 10; index += 1) {
    slowObserved.stream.write(Buffer.from(`ordinary-log-line-${index}\n`))
  }
  slowObserved.stream.write(Buffer.from('{"level":"error"}\n'))
  slowObserved.stream.write(Buffer.from(`${JSON.stringify({
    level: 'error',
    event: 'failure-probe',
    message: 'x'.repeat(100)
  })}\n`))
  slowObserved.stream.write(Buffer.from(`${JSON.stringify({
    level: 'fatal',
    event: 'failure-overflow',
    message: 'y'.repeat(100)
  })}\n`))
  await new Promise<void>((resolve) => setImmediate(resolve))

  const pendingStats = slowObserved.stats()
  assert.equal(pendingStats.needDrain, true)
  assert(pendingStats.backpressureSignalCount >= 1)
  assert(pendingStats.pendingBytes <= 96)
  assert(pendingStats.failurePendingBytes > 0)
  assert(pendingStats.failurePendingBytes <= 64)
  assert(pendingStats.dropCount > 0)
  assert.equal(pendingStats.failureDropCount, 2)
  assert.equal(pendingStats.lastDrop?.level, 'fatal')
  assert((pendingStats.lastDrop?.previewBytes ?? 0) <= 32)
  assert.deepEqual(emergencyFailureSnapshots, [{ level: 'error', previewBytes: 32 }])
  assert.equal(pendingStats.destinations[0]?.name, 'slow-file')
  assert.equal(pendingStats.destinations[0]?.needDrain, true)
  assert(pendingStats.destinations[0]!.pendingBytes <= 96)

  slowDestination.release()
  await new Promise<void>((resolve) => setImmediate(resolve))

  const drainedStats = slowObserved.stats()
  assert.equal(drainedStats.needDrain, false)
  assert.equal(drainedStats.pendingBytes, 0)
  slowObserved.stream.destroy()
  slowDestination.destroy()

  const failingDestination = new FailingWritable()
  const failedObserved = createObservedLogStreamForTest([
    { name: 'failed-file', stream: failingDestination }
  ])
  failedObserved.stream.write(Buffer.from('failed-log-line\n'))
  await new Promise<void>((resolve) => failingDestination.once('close', resolve))
  await new Promise<void>((resolve) => setImmediate(resolve))

  const failedStats = failedObserved.stats()
  assert.equal(uncaughtErrors.length, 0)
  assert.equal(failedStats.errorCount, 1)
  assert.equal(failedStats.degraded, true)
  assert.equal(failedStats.pendingBytes, 0)
  assert.deepEqual(failedStats.lastError, {
    at: failedStats.lastError?.at,
    destination: 'failed-file',
    operation: 'write',
    name: 'Error',
    code: 'ENOSPC',
    message: '磁盘空间不足',
    syscall: 'write',
    path: 'C:\\logs\\juhe-ai.log'
  })
  assert((failedStats.lastError?.message.length ?? 0) <= 1024)
  failedObserved.stream.destroy()
} finally {
  process.off('uncaughtException', onUncaughtException)
}

console.log('logger 流错误与背压诊断回归通过')
