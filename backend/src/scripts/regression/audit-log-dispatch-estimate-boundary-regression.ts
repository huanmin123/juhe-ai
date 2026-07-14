import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

runtimeConfig.processRole = 'server'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const auditLogQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const auditLogTransport = await import('../../modules/audit-logs/audit-log-transport.service.js')
const backgroundIpc = await import('../../modules/background/background-ipc.js')

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

const largeBody = buildTrapLikeLargeString()
const before = auditLogQueue.getAuditLogQueueRuntime()
auditLogQueue.enqueueAuditLog({
  traceId: 'trace-audit-dispatch-estimate-boundary',
  method: 'POST',
  path: '/v1/responses',
  auditOutcome: 'gateway_failed',
  success: false,
  finalStatusCode: 503,
  errorPhase: 'gateway',
  errorCode: 'worker_ipc_unavailable',
  errorMessage: '审计日志 dispatch 估算边界回归',
  sampleBucket: 0,
  sampleReason: 'regression',
  captureStatus: 'complete',
  startedAt: '2000-01-01T00:00:00.000Z',
  endedAt: '2000-01-01T00:00:00.000Z',
  attempts: [],
  payloads: [{
    partType: 'gateway_error',
    body: largeBody,
    contentType: 'text/plain',
    captureStatus: 'complete'
  }]
})
assert.equal(await auditLogTransport.waitForAuditLogTransportIdle(), true, '审计传输 worker 应在超时前完成有界降级')
const after = auditLogQueue.getAuditLogQueueRuntime()
assert.equal(after.droppedFailureCount, before.droppedFailureCount + 1, 'IPC 队列饱和时 server 审计日志应记录失败丢弃')
assert.equal(after.queueLength, 0, 'server 角色不能把审计日志写入本地队列')

console.log('审计日志 dispatch 估算边界回归通过：IPC 饱和时 dropped 估算不会完整扫描大字符串 payload')

await auditLogTransport.stopAuditLogTransportWorker()

function buildTrapLikeLargeString(): string {
  return 'x'.repeat(4 * 1024 * 1024)
}
