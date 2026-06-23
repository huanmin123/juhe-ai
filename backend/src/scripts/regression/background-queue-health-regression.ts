import { strict as assert } from 'node:assert'

import { buildBackgroundQueueHealthSnapshot } from '../../modules/background/background-queue-health.service.js'
import type { DbServiceServerRuntimeSnapshot } from '../../modules/db-service/db-service-types.js'

testUnavailableRuntime()
testNormalRuntime()
testBackloggedRuntime()
testStatsRecordMaintenanceBackloggedRuntime()
testDegradedRuntime()

console.log('后台队列健康快照回归通过：worker 本地队列和 server IPC 队列可统一识别不可用、积压、丢弃、拒绝和 flush 失败')

function testUnavailableRuntime(): void {
  const health = buildBackgroundQueueHealthSnapshot(undefined)
  assert.equal(health.available, false)
  assert.equal(health.status, 'unavailable')
  assert.equal(health.workerSnapshotAvailable, false)
  assert.equal(health.serverIpcQueueAvailable, false)
  assert(health.reasons.includes('server_runtime_unavailable'))
  assert(health.workerQueues.every((queue) => queue.status === 'unavailable'))
  assert(health.serverIpcQueues.every((queue) => queue.status === 'unavailable'))
  assert.equal(health.summary.unavailableCount, health.workerQueues.length + health.serverIpcQueues.length)
}

function testNormalRuntime(): void {
  const health = buildBackgroundQueueHealthSnapshot(buildRuntimeSnapshot())
  assert.equal(health.available, true)
  assert.equal(health.status, 'normal')
  assert.equal(health.summary.degradedCount, 0)
  assert.equal(health.summary.backloggedCount, 0)
  assert.equal(health.summary.unavailableCount, 0)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'usageRecords')?.queueLength, 2)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'usageRecords')?.oldestQueuedMs, 250)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'usageRecords')?.writerPoolEnabled, true)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'usageRecords')?.writerPoolHandledJobs, 9)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'usageRecords')?.pendingWriteRequestCount, 0)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'recordMaintenanceIngest')?.queueLength, 3)
  assert.equal(health.workerQueues.find((queue) => queue.key === 'recordMaintenanceStats')?.queueLength, 4)
  assert.equal(health.serverIpcQueues.find((queue) => queue.key === 'usageRecords')?.queueLength, 1)
  assert.equal(health.summary.pendingWriteRequestCount, 0)
  assert.equal(health.summary.writerPoolQueuedCount, 0)
  assert.equal(health.summary.writerPoolActiveJobs, 0)
}

function testBackloggedRuntime(): void {
  const runtime = buildRuntimeSnapshot()
  runtime.ingestWorker!.snapshot!.usageRecordQueue.queueLength = 1000
  runtime.ingestWorker!.snapshot!.usageRecordQueue.queueBytes = 256 * 1024
  const health = buildBackgroundQueueHealthSnapshot(runtime)
  const usageQueue = health.workerQueues.find((queue) => queue.key === 'usageRecords')
  assert.equal(health.status, 'backlogged')
  assert.equal(usageQueue?.status, 'backlogged')
  assert.deepEqual(usageQueue?.reasons, ['queue_backlogged'])
  assert.equal(health.summary.backloggedCount, 1)
}

function testStatsRecordMaintenanceBackloggedRuntime(): void {
  const runtime = buildRuntimeSnapshot()
  runtime.statsWorker!.snapshot!.recordMaintenanceQueue.queueLength = 1000
  runtime.statsWorker!.snapshot!.recordMaintenanceQueue.queueBytes = 256 * 1024
  const health = buildBackgroundQueueHealthSnapshot(runtime)
  const recordMaintenanceQueue = health.workerQueues.find((queue) => queue.key === 'recordMaintenanceStats')
  assert.equal(health.status, 'backlogged')
  assert.equal(recordMaintenanceQueue?.status, 'backlogged')
  assert.deepEqual(recordMaintenanceQueue?.reasons, ['queue_backlogged'])
  assert.equal(health.summary.backloggedCount, 1)
}

function testDegradedRuntime(): void {
  const runtime = buildRuntimeSnapshot()
  runtime.ingestWorker!.snapshot!.auditLogQueue.droppedFailureCount = 2
  runtime.ingestWorker!.snapshot!.auditLogQueue.flushFailureCount = 1
  runtime.ingestWorker!.snapshot!.auditLogQueue.flushLastError = 'SQLITE_BUSY'
  runtime.ingestWorker!.snapshot!.usageRecordQueue.slowFlushCount = 1
  runtime.ingestWorker!.snapshot!.usageRecordQueue.writerPoolFailedJobs = 1
  runtime.ingestWorker!.snapshot!.usageRecordQueue.writerPoolQueueLength = 1000
  runtime.ingestWorker!.snapshot!.usageRecordQueue.writerPoolActiveJobs = 2
  runtime.ingestWorker!.pendingWriteRequestCount = 2
  runtime.ingestWorker!.oldestPendingWriteMs = 6000
  runtime.worker!.pendingQueues!.usageRecords.rejectedCount = 3

  const health = buildBackgroundQueueHealthSnapshot(runtime)
  const usageQueue = health.workerQueues.find((queue) => queue.key === 'usageRecords')
  const auditQueue = health.workerQueues.find((queue) => queue.key === 'auditLogs')
  const usageIpcQueue = health.serverIpcQueues.find((queue) => queue.key === 'usageRecords')
  assert.equal(health.status, 'degraded')
  assert.equal(usageQueue?.status, 'degraded')
  assert(usageQueue?.reasons.includes('queue_slow_flush'))
  assert(usageQueue?.reasons.includes('writer_pool_degraded'))
  assert(usageQueue?.reasons.includes('writer_pool_backlogged'))
  assert(usageQueue?.reasons.includes('pending_write_backlogged'))
  assert.equal(usageQueue?.pendingWriteRequestCount, 2)
  assert.equal(usageQueue?.oldestPendingWriteMs, 6000)
  assert.equal(usageQueue?.writerPoolFailedJobs, 1)
  assert.equal(usageQueue?.writerPoolQueueLength, 1000)
  assert.equal(auditQueue?.status, 'degraded')
  assert.equal(auditQueue?.droppedCount, 2)
  assert.equal(auditQueue?.flushFailureCount, 1)
  assert.equal(auditQueue?.flushLastError, 'SQLITE_BUSY')
  assert(auditQueue?.reasons.includes('queue_dropped'))
  assert(auditQueue?.reasons.includes('queue_flush_failed'))
  assert.equal(usageIpcQueue?.status, 'degraded')
  assert.equal(usageIpcQueue?.rejectedCount, 3)
  assert.deepEqual(usageIpcQueue?.reasons, ['ipc_rejected'])
  assert.equal(health.summary.degradedCount, 3)
  assert.equal(health.summary.droppedCount, 2)
  assert.equal(health.summary.rejectedCount, 3)
  assert.equal(health.summary.flushFailureCount, 1)
  assert.equal(health.summary.pendingWriteRequestCount, 2)
  assert.equal(health.summary.writerPoolQueuedCount, 1000)
  assert.equal(health.summary.writerPoolActiveJobs, 2)
}

function buildRuntimeSnapshot(): DbServiceServerRuntimeSnapshot {
  return {
    worker: {
      pid: 1001,
      ready: true,
      pendingMessageCount: 1,
      pendingMessageBytes: 1024,
      pendingQueues: {
        usageRecords: queue({ queueLength: 1, queueBytes: 1024 }),
        auditLogs: queue(),
        operationLogs: queue(),
        publicApiLogs: queue(),
        recordMaintenance: queue(),
        runtimeLogLines: queue(),
        statusRequests: queue(),
        processEventLoopRequests: queue(),
        processEventLoopResponses: queue(),
        gatewayRuntimeCacheInvalidations: queue(),
        other: queue()
      },
      snapshot: {
        pid: 1002,
        ready: true,
        workerRole: 'worker',
        jobs: [],
        usageRecordQueue: queue(),
        auditLogQueue: queue(),
        operationLogQueue: queue(),
        publicApiLogQueue: queue(),
        recordMaintenanceQueue: queue(),
        runtimeLogIndexQueue: {
          ...queue(),
          retentionDays: 30
        }
      }
    },
    maintenanceWorker: {
      pid: 1005,
      ready: true,
      pendingMessageCount: 0,
      pendingMessageBytes: 0,
      pendingQueues: {
        usageRecords: queue(),
        auditLogs: queue(),
        operationLogs: queue(),
        publicApiLogs: queue(),
        recordMaintenance: queue(),
        runtimeLogLines: queue(),
        statusRequests: queue(),
        processEventLoopRequests: queue(),
        processEventLoopResponses: queue(),
        gatewayRuntimeCacheInvalidations: queue(),
        other: queue()
      },
      snapshot: {
        pid: 1006,
        ready: true,
        workerRole: 'maintenance-worker',
        jobs: [],
        recordMaintenanceQueue: queue()
      }
    },
    ingestWorker: {
      pid: 1003,
      ready: true,
      pendingMessageCount: 0,
      pendingMessageBytes: 0,
      pendingWriteRequestCount: 0,
      oldestPendingWriteMs: 0,
      pendingQueues: {
        usageRecords: queue(),
        auditLogs: queue(),
        operationLogs: queue(),
        publicApiLogs: queue(),
        recordMaintenance: queue(),
        runtimeLogLines: queue(),
        statusRequests: queue(),
        processEventLoopRequests: queue(),
        processEventLoopResponses: queue(),
        gatewayRuntimeCacheInvalidations: queue(),
        other: queue()
      },
      snapshot: {
        pid: 1004,
        ready: true,
        workerRole: 'ingest-worker',
        jobs: [],
        usageRecordQueue: queue({
          queueLength: 2,
          queueBytes: 2048,
          oldestQueuedMs: 250,
          lastFlushMs: 12,
          maxFlushMs: 20,
          slowFlushCount: 0,
          writerPoolEnabled: true,
          writerPoolWorkerCount: 4,
          writerPoolQueueLength: 0,
          writerPoolActiveJobs: 0,
          writerPoolHandledJobs: 9,
          writerPoolFailedJobs: 0,
          writerPoolRejectedJobs: 0,
          writerPoolOldestQueuedMs: 0,
          writerPoolMaxQueueWaitMs: 3,
          writerPoolMaxRunMs: 15
        }),
        auditLogQueue: queue(),
        operationLogQueue: queue(),
        publicApiLogQueue: queue(),
        recordMaintenanceQueue: queue({ queueLength: 3, queueBytes: 3072 }),
        runtimeLogIndexQueue: {
          ...queue(),
          retentionDays: 30
        }
      }
    },
    statsWorker: {
      pid: 1007,
      ready: true,
      pendingWriteRequestCount: 0,
      oldestPendingWriteMs: 0,
      snapshot: {
        pid: 1008,
        ready: true,
        workerRole: 'stats-worker',
        jobs: [],
        recordMaintenanceQueue: queue({ queueLength: 4, queueBytes: 4096 })
      }
    }
  }
}

function queue(input: Record<string, unknown> = {}) {
  return {
    queueLength: 0,
    queueBytes: 0,
    droppedCount: 0,
    droppedOverflowCount: 0,
    droppedOversizeCount: 0,
    rejectedCount: 0,
    flushFailureCount: 0,
    ...input
  }
}
