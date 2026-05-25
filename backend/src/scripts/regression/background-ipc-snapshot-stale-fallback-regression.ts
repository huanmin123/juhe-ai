import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import type { OperationLogInput } from '../../storage/repositories.js'
import type { BackgroundWorkerRuntimeSnapshot } from '../../modules/background/background-ipc.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'server'

const backgroundIpc = await import('../../modules/background/background-ipc.js')

type WorkerMessage = Parameters<typeof backgroundIpc.sendBackgroundWorkerMessage>[0]
type StatusRequest = Extract<WorkerMessage, { type: 'background_worker_status_request' }>

class FakeWorkerProcess extends EventEmitter {
  connected = true
  killed = false
  pid: number
  respondToStatus = true
  failSend = false

  constructor(pid: number) {
    super()
    this.pid = pid
  }

  send(message: WorkerMessage, callback?: (error?: Error | null) => void): boolean {
    if (this.failSend) {
      callback?.(new Error('模拟 worker IPC 发送失败'))
      return true
    }
    callback?.(null)
    if (this.respondToStatus && message.type === 'background_worker_status_request') {
      setImmediate(() => {
        this.emit('message', {
          type: 'background_worker_status_response',
          requestId: (message as StatusRequest).requestId,
          snapshot: buildWorkerSnapshot(this.pid)
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

  exit(): void {
    this.connected = false
    this.emit('exit', 0, null)
  }

  ready(): void {
    this.emit('message', { type: 'background_worker_ready', pid: this.pid })
  }
}

const firstWorker = attachReadyWorker(41001)
const firstSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(firstSnapshot?.pid, firstWorker.pid, '正常 IPC 回包应返回当前 worker snapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().lastSnapshot?.pid, firstWorker.pid, '正常回包可以更新 lastSnapshot')

firstWorker.exit()
const missingWorkerSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(missingWorkerSnapshot, undefined, 'worker 不存在时不能把 lastSnapshot 当作当前 snapshot 返回')
assert.equal(backgroundIpc.getBackgroundWorkerState().lastSnapshot?.pid, firstWorker.pid, 'lastSnapshot 可作为状态留存，但不能作为请求兜底结果')

const timeoutWorker = attachReadyWorker(41002)
timeoutWorker.respondToStatus = false
const timedOutSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(10)
assert.equal(timedOutSnapshot, undefined, 'worker snapshot 请求超时时不能返回 stale lastSnapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().timedOutSnapshotRequestCount, 1, '超时应计入 snapshot 请求超时指标')
timeoutWorker.exit()

const brokenWorker = attachReadyWorker(41003)
brokenWorker.failSend = true
const brokenSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(brokenSnapshot, undefined, 'worker IPC 发送失败断开时不能返回 stale lastSnapshot')
brokenWorker.exit()

const saturatedWorker = new FakeWorkerProcess(41004)
backgroundIpc.attachBackgroundWorkerProcess(saturatedWorker as unknown as ChildProcess)
for (let index = 0; index < 5000; index += 1) {
  assert.equal(backgroundIpc.sendOperationLogsToWorker([operationLog(index)]), true, `旧队列上限预填充 ${index} 应成功`)
}
const queuedSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(10)
assert.equal(queuedSnapshot, undefined, 'worker 未就绪导致 snapshot 请求排队超时时不能返回 stale lastSnapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().timedOutSnapshotRequestCount, 2, '排队超时应计入 snapshot 请求超时指标')
assert.equal(backgroundIpc.getBackgroundWorkerState().rejectedSnapshotRequestCount, 0, '超过旧队列上限不应计入 snapshot 请求拒绝指标')
saturatedWorker.exit()

console.log('后台 worker snapshot stale fallback 回归通过：不可观测时返回 undefined，不复用旧快照')

function attachReadyWorker(pid: number): FakeWorkerProcess {
  const worker = new FakeWorkerProcess(pid)
  backgroundIpc.attachBackgroundWorkerProcess(worker as unknown as ChildProcess)
  worker.ready()
  return worker
}

function buildWorkerSnapshot(pid: number): BackgroundWorkerRuntimeSnapshot {
  const queue = { queueLength: 0, queueBytes: 0 }
  return {
    pid,
    ready: true,
    processRole: 'worker',
    jobs: [],
    usageRecordQueue: { ...queue },
    operationLogQueue: { ...queue },
    recordMaintenanceQueue: { ...queue },
    auditLogQueue: { ...queue },
    runtimeLogIndexQueue: { ...queue, retentionDays: 3 }
  }
}

function operationLog(index: number): OperationLogInput {
  return {
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'snapshot_stale_fallback',
    operationKey: 'regression.background_ipc_snapshot_stale_fallback',
    resourceType: 'background_worker',
    summary: `后台 worker snapshot stale fallback 队列填充 ${index}`
  }
}
