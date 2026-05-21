import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'

const backgroundIpc = await import('../../modules/background/background-ipc.js')
const originalSend = process.send
const originalExit = process.exit

let exitCallCount = 0
let exitCode: string | number | null | undefined

try {
  process.exit = ((code?: string | number | null | undefined): never => {
    exitCallCount += 1
    exitCode = code
    return undefined as never
  }) as typeof process.exit

  process.send = (() => {
    throw new Error('模拟 worker 父进程 IPC 同步发送失败')
  }) as NodeJS.Process['send']

  const samplesAfterThrow = await backgroundIpc.requestServerProcessEventLoopSamples(1000)
  assert.equal(samplesAfterThrow, undefined, '父进程 IPC 同步发送失败时应清理 pending 并返回不可用')
  assert.equal(exitCallCount, 1, '父进程 IPC 同步发送失败应退出 worker 等待 supervisor 重启')
  assert.equal(exitCode, 1, '父进程 IPC 同步发送失败应使用失败退出码')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingProcessEventLoopRequestCount, 0, '同步发送失败后 event-loop sample pending 应清空')
  assert.equal(backgroundIpc.getBackgroundWorkerState().failedProcessEventLoopRequestCount, 1, '同步发送失败应计入 event-loop sample 失败计数')

  process.send = ((_message: unknown, callback?: (error?: Error | null) => void) => {
    callback?.(new Error('模拟 worker 父进程 IPC 异步发送失败'))
    return true
  }) as NodeJS.Process['send']

  const samplesAfterCallbackError = await backgroundIpc.requestServerProcessEventLoopSamples(1000)
  assert.equal(samplesAfterCallbackError, undefined, '父进程 IPC 异步发送失败时应清理 pending 并返回不可用')
  assert.equal(exitCallCount, 2, '父进程 IPC 异步发送失败也应退出 worker 等待 supervisor 重启')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingProcessEventLoopRequestCount, 0, '异步发送失败后 event-loop sample pending 应清空')
  assert.equal(backgroundIpc.getBackgroundWorkerState().failedProcessEventLoopRequestCount, 2, '异步发送失败应计入 event-loop sample 失败计数')

  process.send = (() => true) as NodeJS.Process['send']

  const samplesAfterTimeout = await backgroundIpc.requestServerProcessEventLoopSamples(10)
  assert.equal(samplesAfterTimeout, undefined, '父进程 IPC 采样超时时应返回不可用，不能伪装成空样本')
  assert.equal(exitCallCount, 2, '父进程 IPC 采样超时不应额外退出 worker')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingProcessEventLoopRequestCount, 0, '采样超时后 event-loop sample pending 应清空')
  assert.equal(backgroundIpc.getBackgroundWorkerState().timedOutProcessEventLoopRequestCount, 1, '采样超时应计入 event-loop sample 超时计数')

  console.log('后台 worker 父进程 IPC 事件循环采样回归通过：发送失败/超时会清 pending，并用 undefined 表达不可用')
} finally {
  process.send = originalSend
  process.exit = originalExit
}
