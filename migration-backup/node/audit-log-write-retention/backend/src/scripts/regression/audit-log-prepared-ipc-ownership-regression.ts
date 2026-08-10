import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { serialize } from 'node:v8'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'

runtimeConfig.processRole = 'server'
runtimeConfig.queueDriver = 'memory'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const backgroundIpc = await import('../../modules/background/background-ipc.js')
const auditLogQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const auditTransport = await import('../../modules/audit-logs/audit-log-transport.service.js')
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

const fakeWorker = new FakeWorkerProcess()
backgroundIpc.attachBackgroundWorkerProcess(fakeWorker as unknown as ChildProcess, { role: 'ingest-worker' })
fakeWorker.ready()
fakeWorker.sentMessages = []

try {
  auditLogQueue.enqueueAuditLog(buildSuccessAudit())
  assert.equal(
    await auditLogQueue.waitForAuditLogServerDispatchIdle(),
    true,
    'production audit dispatch must finish before the regression timeout'
  )
  const preparedMessage = lastAuditMessage(fakeWorker.sentMessages)
  const deliveredPayload = preparedMessage.items[0]?.payloads[0]
  assert.equal(preparedMessage.items[0]?.captureStatus, 'complete', 'transport summary must not mark the whole audit record dropped')
  assert.equal(deliveredPayload?.captureStatus, 'summary_only', 'prepared summary must not be downgraded to hash_only')
  assert.equal(typeof deliveredPayload?.body, 'string', 'prepared summary body must survive background IPC')
  assert.match(deliveredPayload?.bodySha256 ?? '', /^[a-f0-9]{64}$/, 'prepared summary must retain the original body hash')
  assert(
    serialize(preparedMessage).byteLength <= 4 * 1024 * 1024,
    'complete prepared background IPC envelope must stay within 4MiB'
  )

  fakeWorker.sentMessages = []
  assert.equal(
    backgroundIpc.sendAuditLogsToWorker([buildUnpreparedOversizeAudit()]),
    true,
    'legacy sender must retain bounded protection for unprepared audit input'
  )
  const fallbackPayload = lastAuditMessage(fakeWorker.sentMessages).items[0]?.payloads[0]
  assert.equal(fallbackPayload?.body, undefined, 'legacy sender must remove an unprepared oversized body')
  assert.equal(fallbackPayload?.captureStatus, 'hash_only', 'legacy sender must retain hash-only evidence')

  console.log('audit log prepared IPC ownership regression passed')
} finally {
  await auditTransport.stopAuditLogTransportWorker()
}

function buildSuccessAudit(): AuditLogInput {
  return auditInput('prepared-summary', true, Buffer.alloc(600 * 1024, 0x61))
}

function buildUnpreparedOversizeAudit(): AuditLogInput {
  return auditInput('legacy-fallback', false, Buffer.alloc(4 * 1024 * 1024, 0x62))
}

function auditInput(suffix: string, success: boolean, body: Buffer): AuditLogInput {
  const timestamp = '2026-07-28T00:00:00.000Z'
  return {
    id: `audit-${suffix}`,
    traceId: `trace-${suffix}`,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: success ? 'success' : 'upstream_failed',
    success,
    sampleBucket: 1,
    sampleReason: 'prepared_ipc_ownership_regression',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    durationMs: 1,
    attempts: [],
    payloads: [{
      partType: 'client_request',
      sequenceIndex: 0,
      contentType: 'application/json',
      body,
      captureStatus: 'complete'
    }]
  }
}

function lastAuditMessage(messages: WorkerMessage[]): Extract<WorkerMessage, { type: 'background_worker_audit_logs' }> {
  const message = [...messages].reverse().find(
    (item): item is Extract<WorkerMessage, { type: 'background_worker_audit_logs' }> => item.type === 'background_worker_audit_logs'
  )
  assert(message, 'fake worker must receive an audit log IPC message')
  return message
}
