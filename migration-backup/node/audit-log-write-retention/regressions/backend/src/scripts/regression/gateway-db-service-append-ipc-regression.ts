import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, UsageRecordInput } from '../../storage/repositories.js'

runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

class FakeDbServiceProcess extends EventEmitter {
  connected = true
  killed = false
  pid = 52002

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
  type: 'background_worker_usage_records',
  items: [usageRecordFixture('trace-usage-db-service-forward')]
})
fakeDbService.emit('message', {
  type: 'background_worker_audit_logs',
  items: [auditLogFixture('trace-audit-db-service-forward')]
})
await waitForIpcQueueLength('usageRecords', 1)
await waitForIpcQueueLength('auditLogs', 1)
assert.equal(
  backgroundIpc.getBackgroundWorkerState().pendingQueues.usageRecords.queueLength,
  1,
  'server 应把 DB service 转发的使用记录投递到 ingest-worker IPC 队列'
)
assert.equal(
  backgroundIpc.getBackgroundWorkerState().pendingQueues.auditLogs.queueLength,
  1,
  'server 应把 DB service 转发的审计日志投递到 ingest-worker IPC 队列'
)

runtimeConfig.processRole = 'db-service'
const [usageRecordQueue, auditLogQueue] = await Promise.all([
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])
const originalSend = process.send
try {
  const sentMessages: unknown[] = []
  process.send = ((message: unknown, callback?: (error?: Error | null) => void) => {
    sentMessages.push(message)
    callback?.(null)
    return true
  }) as NodeJS.Process['send']

  usageRecordQueue.enqueueUsageRecord(usageRecordFixture('trace-usage-db-service-send'))
  auditLogQueue.enqueueAuditLog(auditLogFixture('trace-audit-db-service-send'))
  assert.equal((sentMessages[0] as { type?: unknown }).type, 'background_worker_usage_records', 'DB service 应发送使用记录 worker IPC 消息')
  assert.equal((sentMessages[1] as { type?: unknown }).type, 'background_worker_audit_logs', 'DB service 应发送审计日志 worker IPC 消息')

  process.send = (() => {
    throw new Error('模拟父进程 IPC 已关闭')
  }) as NodeJS.Process['send']
  const usageDroppedBefore = usageRecordQueue.getUsageRecordQueueRuntime().droppedCount
  const auditDroppedBefore = auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount
  assert.doesNotThrow(() => {
    usageRecordQueue.enqueueUsageRecord(usageRecordFixture('trace-usage-db-service-ipc-closed'))
    auditLogQueue.enqueueAuditLog(auditLogFixture('trace-audit-db-service-ipc-closed'))
  }, 'DB service 使用记录 / 审计日志投递 IPC 断开时不应抛出异常')
  assert.equal(
    usageRecordQueue.getUsageRecordQueueRuntime().droppedCount,
    usageDroppedBefore + 1,
    'DB service 使用记录 IPC 投递失败应累计 droppedCount'
  )
  assert.equal(
    auditLogQueue.getAuditLogQueueRuntime().droppedFailureCount,
    auditDroppedBefore + 1,
    'DB service 审计日志 IPC 投递失败应累计 droppedFailureCount'
  )
} finally {
  process.send = originalSend
}

console.log('网关 DB service append IPC 回归通过：使用记录和审计日志可由 DB service 转发给 server，再进入 ingest-worker 队列；父 IPC 异常不打崩请求链路')

function usageRecordFixture(traceId: string): UsageRecordInput {
  return {
    traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    apiKeyId: 'key_db_service_ipc',
    groupId: 'group_db_service_ipc',
    endpoint: '/v1/chat/completions',
    providerCode: 'gpt',
    model: 'gpt-4o-mini',
    stream: false,
    statusCode: 503,
    success: false,
    durationMs: 10,
    errorMessage: 'mock db service append ipc failure'
  }
}

function auditLogFixture(traceId: string): AuditLogInput {
  return {
    traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    apiKeyId: 'key_db_service_ipc',
    groupId: 'group_db_service_ipc',
    method: 'POST',
    path: '/v1/chat/completions',
    model: 'gpt-4o-mini',
    stream: false,
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 503,
    errorPhase: 'dispatch',
    errorMessage: 'mock db service append ipc failure',
    sampleBucket: 1,
    sampleReason: 'failure',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.010Z',
    attempts: [],
    payloads: [],
    createdAt: '2000-01-01T00:00:00.000Z'
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function waitForIpcQueueLength(
  queue: 'usageRecords' | 'auditLogs',
  expected: number
): Promise<void> {
  const deadline = Date.now() + 1000
  while (Date.now() < deadline) {
    if (backgroundIpc.getBackgroundWorkerState().pendingQueues[queue].queueLength >= expected) {
      return
    }
    await sleep(20)
  }
}
