import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

runtimeConfig.processRole = 'server'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const auditLogQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const auditLogTransport = await import('../../modules/audit-logs/audit-log-transport.service.js')
const backgroundIpc = await import('../../modules/background/background-ipc.js')

type WorkerMessage = Parameters<typeof backgroundIpc.sendBackgroundWorkerMessage>[0]

class FakeWorkerProcess extends EventEmitter {
  connected = true
  killed = false
  pid = 42002
  sentMessages: WorkerMessage[] = []

  send(message: WorkerMessage, callback?: (error?: Error | null) => void): boolean {
    this.sentMessages.push(message)
    callback?.(null)
    return true
  }

  kill(): boolean {
    this.killed = true
    this.connected = false
    return true
  }

  ready(): void {
    this.emit('message', { type: 'background_worker_ready', pid: this.pid })
  }
}

const largeBody = buildTrapLikeLargeString()
const fakeWorker = new FakeWorkerProcess()
backgroundIpc.attachBackgroundWorkerProcess(fakeWorker as unknown as ChildProcess, { role: 'ingest-worker' })
fakeWorker.ready()
fakeWorker.sentMessages = []

const beforeSuccessfulFallback = auditLogQueue.getAuditLogQueueRuntime()
const beforeSuccessfulTransport = auditLogTransport.getAuditLogTransportRuntime()
auditLogQueue.enqueueAuditLog(buildCapacityRejectedAudit('fallback-success', largeBody))
assert.equal(await auditLogQueue.waitForAuditLogServerDispatchIdle(), true, '容量降级审计应在超时前完成二次投递')

const auditMessages = fakeWorker.sentMessages
  .filter((message): message is Extract<WorkerMessage, { type: 'background_worker_audit_logs' }> => message.type === 'background_worker_audit_logs')
assert.equal(auditMessages.length, 1, '首次 transport 容量拒绝后应向 ingest-worker 投递一条降级审计')
const fallback = auditMessages[0].items[0]
assert(fallback, '降级审计消息必须保留同一条审计记录')
assert.equal(fallback.id, 'audit-fallback-success', '降级投递必须保留原审计 ID')
assert.equal(fallback.traceId, 'trace-fallback-success', '降级投递必须保留 trace ID')
assert.equal(fallback.captureStatus, 'dropped', '正文因容量保护移除后整条审计必须标记为 dropped')
assert.equal(fallback.payloads[0]?.body, undefined, '容量降级消息不得继续携带大正文')
assert.equal(fallback.payloads[0]?.captureStatus, 'dropped', '无既有 hash 的正文移除后 payload 必须标记为 dropped')
assert.equal(fallback.payloads[0]?.rawBodySizeBytes, largeBody.length, '容量降级必须保留已知原始正文大小')
assert.equal(fallback.payloads[0]?.headers?.['x-regression-retained'], 'yes', '容量降级必须保留预算内 headers')
assert(
  Buffer.byteLength(String(fallback.payloads[0]?.headers?.['x-regression-truncated'] ?? ''), 'utf8') <= 2048,
  '容量降级必须截断单个超大 header value'
)
assert(Buffer.byteLength(JSON.stringify(auditMessages[0]), 'utf8') < 256 * 1024, '容量降级元数据消息必须保持在 server 内联预算内')

const afterSuccessfulFallback = auditLogQueue.getAuditLogQueueRuntime()
const afterSuccessfulTransport = auditLogTransport.getAuditLogTransportRuntime()
assert.equal(afterSuccessfulFallback.droppedFailureCount, beforeSuccessfulFallback.droppedFailureCount, '降级审计成功入队时不得计为整条丢弃')
assert.equal(afterSuccessfulTransport.rejectedCount, beforeSuccessfulTransport.rejectedCount + 1, '首次大正文必须真实触发 transport 容量拒绝')

fakeWorker.connected = false
fakeWorker.emit('exit', 0, null)
for (let index = 0; index < 5000; index += 1) {
  const queued = backgroundIpc.sendOperationLogsToWorker([{
    actorSystemAccountId: 'sys_admin',
    actorRole: 'admin',
    module: 'regression',
    action: 'audit_dispatch_estimate_boundary_fill',
    operationKey: 'regression.audit_dispatch_estimate_boundary',
    resourceType: 'background_worker',
    summary: `填充 regular IPC 队列 ${index}`
  }])
  assert.equal(queued, true, `regular IPC 队列填充 ${index} 应成功`)
}

const before = auditLogQueue.getAuditLogQueueRuntime()
const beforeFailedTransport = auditLogTransport.getAuditLogTransportRuntime()
auditLogQueue.enqueueAuditLog(buildCapacityRejectedAudit('fallback-failure', largeBody))
assert.equal(await auditLogQueue.waitForAuditLogServerDispatchIdle(), true, '容量降级二次失败应在超时前完成记账')
const after = auditLogQueue.getAuditLogQueueRuntime()
const afterFailedTransport = auditLogTransport.getAuditLogTransportRuntime()
assert.equal(afterFailedTransport.rejectedCount, beforeFailedTransport.rejectedCount + 1, '二次失败场景的原始大正文仍应先触发 transport 容量拒绝')
assert.equal(after.droppedFailureCount, before.droppedFailureCount + 1, '只有降级元数据二次投递失败后才应计为整条审计丢弃')
assert.equal(after.droppedOverflowCount, before.droppedOverflowCount + 1, '降级元数据二次投递失败应计入 overflow')
assert.equal(after.queueLength, 0, 'server 角色不能把审计日志写入本地队列')

console.log('审计日志 dispatch 容量回归通过：大正文拒绝后保留有界元数据入队，二次投递失败才计 dropped')

await auditLogTransport.stopAuditLogTransportWorker()

function buildTrapLikeLargeString(): string {
  return 'x'.repeat(16 * 1024 * 1024)
}

function buildCapacityRejectedAudit(suffix: string, body: string): AuditLogInput {
  const timestamp = '2000-01-01T00:00:00.000Z'
  return {
    id: `audit-${suffix}`,
    traceId: `trace-${suffix}`,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'transport_capacity_regression',
    errorMessage: '审计 transport 容量降级回归',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    attempts: [],
    payloads: [{
      id: `payload-${suffix}`,
      partType: 'upstream_response',
      sequenceIndex: 0,
      contentType: 'text/plain',
      headers: {
        'x-regression-retained': 'yes',
        'x-regression-truncated': 'h'.repeat(100 * 1024)
      },
      body,
      rawBodySizeBytes: body.length,
      captureStatus: 'complete'
    }]
  }
}
