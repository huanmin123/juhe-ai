import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import type { BackgroundWorkerProcessRole, BackgroundWorkerRuntimeSnapshot } from '../../modules/background/background-ipc.js'

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
  responseDelayMs = 0
  failSend = false
  role: BackgroundWorkerProcessRole

  constructor(pid: number, role: BackgroundWorkerProcessRole = 'worker') {
    super()
    this.pid = pid
    this.role = role
  }

  send(message: WorkerMessage, callback?: (error?: Error | null) => void): boolean {
    if (this.failSend) {
      callback?.(new Error('模拟 worker IPC 发送失败'))
      return true
    }
    callback?.(null)
    if (this.respondToStatus && message.type === 'background_worker_status_request') {
      setTimeout(() => {
        this.emit('message', {
          type: 'background_worker_status_response',
          requestId: (message as StatusRequest).requestId,
          snapshot: buildWorkerSnapshot(this.pid, this.role)
        })
      }, this.responseDelayMs)
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
    this.emit('message', { type: 'background_worker_ready', pid: this.pid, workerRole: this.role })
  }
}

const firstWorker = attachReadyWorker(41001)
const firstSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(firstSnapshot?.pid, firstWorker.pid, '正常 IPC 回包应返回当前 worker snapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().lastSnapshot?.pid, firstWorker.pid, '正常回包可以更新 lastSnapshot')

firstWorker.exit()
const missingWorkerSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(missingWorkerSnapshot, undefined, 'worker 不存在时不能把 lastSnapshot 当作当前 snapshot 返回')
assert.equal(backgroundIpc.getBackgroundWorkerState().lastSnapshot?.pid, firstWorker.pid, 'lastSnapshot 可作为状态留存，但不能作为当前请求结果')

const timeoutWorker = attachReadyWorker(41002)
timeoutWorker.respondToStatus = false
const timedOutSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(10)
assert.equal(timedOutSnapshot, undefined, 'worker snapshot 请求超时时不能返回留存 lastSnapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().timedOutSnapshotRequestCount, 1, '超时应计入 snapshot 请求超时指标')
timeoutWorker.exit()

const brokenWorker = attachReadyWorker(41003)
brokenWorker.failSend = true
const brokenSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(50)
assert.equal(brokenSnapshot, undefined, 'worker IPC 发送失败断开时不能返回留存 lastSnapshot')
brokenWorker.exit()

const delayedIngestWorker = attachReadyWorker(41501, 'ingest-worker')
delayedIngestWorker.responseDelayMs = 20
const delayedIngestSnapshot = await backgroundIpc.requestIngestWorkerSnapshot(50)
assert.equal(delayedIngestSnapshot?.pid, delayedIngestWorker.pid, 'ingest-worker 在容错窗口内延迟回包仍应返回当前 snapshot')
delayedIngestWorker.responseDelayMs = 50
const timedOutIngestSnapshot = await backgroundIpc.requestIngestWorkerSnapshot(10)
assert.equal(timedOutIngestSnapshot, undefined, 'ingest-worker 超过容错窗口仍应 fail-closed 返回不可用')
delayedIngestWorker.exit()

const opsWorker = attachReadyWorker(42001, 'ops-worker')
const opsSnapshot = await backgroundIpc.requestOpsWorkerSnapshot(50)
assert.equal(opsSnapshot?.pid, opsWorker.pid, 'ops-worker 正常 IPC 回包应返回当前 snapshot')
assert.equal(backgroundIpc.getBackgroundWorkerState().opsWorker?.lastSnapshot?.pid, opsWorker.pid, 'ops-worker 正常回包可以更新 lastSnapshot')
opsWorker.exit()
const missingOpsSnapshot = await backgroundIpc.requestOpsWorkerSnapshot(50)
assert.equal(missingOpsSnapshot, undefined, 'ops-worker 不存在时不能把 lastSnapshot 当作当前 snapshot 返回')
assert.equal(backgroundIpc.getBackgroundWorkerState().opsWorker?.lastSnapshot?.pid, opsWorker.pid, 'ops-worker lastSnapshot 可留存但不能作为当前请求结果')

let acceptedFillCount = 0
for (let index = 0; index < 6000; index += 1) {
  if (!backgroundIpc.sendRecordMaintenanceJobsToWorker([recordMaintenanceJob(index)])) {
    break
  }
  acceptedFillCount += 1
}
assert(acceptedFillCount > 0, '队列饱和前应至少接受一条维护任务')
assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.queueLength, 5000, 'ingest recordMaintenance IPC 队列应填充到当前保护上限')
const rejectedMaintenanceCountBeforeSaturation = backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.rejectedCount ?? 0
const rejectedMaintenanceJob = backgroundIpc.sendRecordMaintenanceJobsToWorker([recordMaintenanceJob(6001)])
assert.equal(rejectedMaintenanceJob, false, 'ingest recordMaintenance IPC 队列饱和时维护任务应快速拒绝')
assert.equal(backgroundIpc.getBackgroundWorkerState().pendingQueues.recordMaintenance.rejectedCount, rejectedMaintenanceCountBeforeSaturation + 1, 'ingest recordMaintenance 队列饱和应计入维护任务拒绝指标')

console.log('后台 worker snapshot current-only 回归通过：不可观测时返回 undefined，不复用留存快照')

function attachReadyWorker(pid: number, role: BackgroundWorkerProcessRole = 'worker'): FakeWorkerProcess {
  const worker = new FakeWorkerProcess(pid, role)
  backgroundIpc.attachBackgroundWorkerProcess(worker as unknown as ChildProcess, { role })
  worker.ready()
  return worker
}

function buildWorkerSnapshot(pid: number, workerRole: BackgroundWorkerProcessRole): BackgroundWorkerRuntimeSnapshot {
  const queue = { queueLength: 0, queueBytes: 0 }
  return {
    pid,
    ready: true,
    processRole: 'worker',
    workerRole,
    jobs: [],
    usageRecordQueue: { ...queue },
    operationLogQueue: { ...queue },
    publicApiLogQueue: { ...queue },
    recordMaintenanceQueue: { ...queue },
    auditLogQueue: { ...queue },
    runtimeLogIndexQueue: { ...queue, retentionDays: 3 }
  }
}

function recordMaintenanceJob(index: number) {
  return {
    type: 'usage_records_cleanup' as const,
    id: `snapshot_current_only_${index}`,
    cutoffAt: '2000-01-01T00:00:00.000Z',
    batchSize: 100,
    maxBatches: 1
  }
}
