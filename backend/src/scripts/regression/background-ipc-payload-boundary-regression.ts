import { strict as assert } from 'node:assert'
import type { ChildProcess } from 'node:child_process'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, OperationLogInput } from '../../storage/repositories.js'

runtimeConfig.processRole = 'server'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
logger.level = 'silent'

const backgroundIpc = await import('../../modules/background/background-ipc.js')

type WorkerMessage = Parameters<typeof backgroundIpc.sendBackgroundWorkerMessage>[0]

class FakeWorkerProcess extends EventEmitter {
  connected = true
  killed = false
  pid = 42001
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

const largeAuditAccepted = backgroundIpc.sendAuditLogsToWorker([{
  traceId: 'trace-background-ipc-large-audit',
  method: 'POST',
  path: '/v1/responses',
  auditOutcome: 'gateway_failed',
  success: false,
  finalStatusCode: 502,
  errorPhase: 'upstream',
  errorCode: 'large_audit_payload',
  errorMessage: '后台 IPC 大审计 payload 边界回归',
  sampleBucket: 0,
  sampleReason: 'regression',
  captureStatus: 'complete',
  startedAt: '2000-01-01T00:00:00.000Z',
  endedAt: '2000-01-01T00:00:00.000Z',
  attempts: [],
  payloads: [{
    partType: 'upstream_response',
    body: Buffer.alloc(4 * 1024 * 1024, 97),
    contentType: 'text/plain',
    captureStatus: 'complete'
  }]
}])
assert.equal(largeAuditAccepted, true, 'server 审计投递应先裁剪大 payload，再进入 IPC 队列')
let state = backgroundIpc.getBackgroundWorkerState()
assert.equal(state.pendingQueues.auditLogs.queueLength, 1, '大审计消息应保留元数据并进入审计 IPC 队列')
assert(
  (state.pendingQueues.auditLogs.queueBytes ?? 0) < 1024 * 1024,
  '大审计 body 不应进入 server 到 worker 的 IPC 消息体'
)

let metadataGetterArmed = false
const operationLog = {
  actorSystemAccountId: 'sys_admin',
  actorRole: 'admin',
  module: 'regression',
  action: 'ipc_state_constant_cost',
  operationKey: 'regression.background_ipc_payload_boundary',
  resourceType: 'operation_log',
  summary: 'IPC 状态读取常量成本回归'
} as OperationLogInput
Object.defineProperty(operationLog, 'metadata', {
  enumerable: true,
  get() {
    if (metadataGetterArmed) {
      throw new Error('IPC 状态读取不应重新遍历队列消息 payload')
    }
    return { queuedAt: '2000-01-01T00:00:00.000Z' }
  }
})

assert.equal(backgroundIpc.sendOperationLogsToWorker([operationLog]), true, '操作日志应进入 regular IPC 队列')
metadataGetterArmed = true
state = backgroundIpc.getBackgroundWorkerState()
assert.equal(state.pendingQueues.operationLogs.queueLength, 1, 'IPC 状态应读取维护好的队列计数，不重新估算消息体')

const hugeUsageAccepted = backgroundIpc.sendUsageRecordsToWorker([{
  traceId: 'trace-background-ipc-huge-usage',
  trafficSource: 'gateway',
  systemAccountId: 'sys_admin',
  endpoint: 'POST /v1/responses',
  providerCode: 'gpt',
  success: true,
  stream: false,
  statusCode: 200,
  requestSnapshot: { oversized: 'x'.repeat(3 * 1024 * 1024) },
  createdAt: '2000-01-01T00:00:00.000Z'
}])
assert.equal(hugeUsageAccepted, false, '单条过大的 usage IPC 消息应被快速拒绝，避免请求进程序列化大对象')
state = backgroundIpc.getBackgroundWorkerState()
assert.equal(state.pendingQueues.usageRecords.rejectedCount, 1, '过大 usage IPC 消息应记录拒绝指标')

const fakeWorker = new FakeWorkerProcess()
backgroundIpc.attachBackgroundWorkerProcess(fakeWorker as unknown as ChildProcess, { role: 'ingest-worker' })
fakeWorker.ready()
fakeWorker.sentMessages = []
assert.equal(backgroundIpc.sendAuditLogsToWorker([buildMediumAudit()]), true, '2MB 内的审计 body 应保留原文投递给 worker')
assert.equal(backgroundIpc.sendAuditLogsToWorker([buildLargeAudit('without-hash')]), true, '无 hash 的大审计应可降级投递')
assert.equal(backgroundIpc.sendAuditLogsToWorker([buildLargeAudit('with-hash', 'sha256-large-body')]), true, '已有 hash 的大审计应可 hash_only 投递')
assert.equal(backgroundIpc.sendAuditLogsToWorker([buildOversizedMetadataAudit()]), true, 'headers / attempts 过大的审计应继续二次降级后投递')
const auditMessages = fakeWorker.sentMessages
  .filter((message): message is Extract<WorkerMessage, { type: 'background_worker_audit_logs' }> => message.type === 'background_worker_audit_logs')
assert.equal(auditMessages.length, 4, 'fake worker 应收到一条完整中等审计和三条降级后的审计消息')
const mediumPayload = auditMessages[0].items[0]?.payloads[0]
assert(Buffer.isBuffer(mediumPayload?.body), '2MB 内的审计 body 不应被 IPC 边界裁剪')
assert.equal((mediumPayload?.body as Buffer).byteLength, 768 * 1024, '2MB 内的审计 body 应按原始大小投递')
assert.equal(mediumPayload?.captureStatus, 'complete', '2MB 内的审计 body 应保持 complete')
const trimmedLargePayload = auditMessages[1].items[0]?.payloads[0]
assert.equal(trimmedLargePayload?.body, undefined, '降级审计不应向 worker IPC 发送超大 body')
assert.equal(trimmedLargePayload?.captureStatus, 'hash_only', '降级超大 body 时应保留 hash_only 状态')
assert.equal(trimmedLargePayload?.bodySha256, sha256Buffer(Buffer.alloc(4 * 1024 * 1024, 97)), '降级超大 body 时应补充 bodySha256')
assert.equal(auditMessages[2].items[0]?.payloads[0]?.captureStatus, 'hash_only', '已有 hash 时丢弃 body 应保持 hash_only')
assert.equal(auditMessages[2].items[0]?.payloads[0]?.bodySha256, 'sha256-large-body', 'hash_only 降级必须保留既有 bodySha256')
const metadataTrimmedAudit = auditMessages[3].items[0]
assert.equal(metadataTrimmedAudit?.captureStatus, 'dropped', 'metadata 过大时审计日志本身应标记为已降级')
assert((metadataTrimmedAudit?.attempts.length ?? 0) <= 16, '过多 attempts 应在 IPC 投递前裁剪')
assert((metadataTrimmedAudit?.payloads.length ?? 0) <= 32, `过多 payloads 应在 IPC 投递前裁剪，actual=${metadataTrimmedAudit?.payloads.length ?? 0}`)
assert.equal(metadataTrimmedAudit?.payloads[0]?.headers, undefined, '过大的 payload headers 应在 IPC 投递前丢弃')
assert.equal(metadataTrimmedAudit?.payloads[0]?.body, undefined, 'metadata 二次降级后不应携带 body')

console.log('后台 IPC payload 边界回归通过：2MB 内审计 body 保留投递，超大审计 body 会在投递前降级，状态读取不重新遍历队列，超大 usage 消息快速拒绝')

function buildMediumAudit(): AuditLogInput {
  return {
    traceId: 'trace-background-ipc-medium-audit',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'gateway_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'medium_audit_payload',
    errorMessage: '后台 IPC 中等审计 payload 原文保留回归',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: [{
      partType: 'upstream_request',
      body: Buffer.alloc(768 * 1024, 97),
      contentType: 'application/json',
      captureStatus: 'complete'
    }]
  }
}

function buildLargeAudit(suffix: string, bodySha256?: string): AuditLogInput {
  return {
    traceId: `trace-background-ipc-large-audit-${suffix}`,
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'gateway_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'large_audit_payload',
    errorMessage: '后台 IPC 大审计 payload 降级状态回归',
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: [],
    payloads: [{
      partType: 'upstream_response',
      body: Buffer.alloc(4 * 1024 * 1024, 97),
      bodySha256,
      contentType: 'text/plain',
      captureStatus: 'complete'
    }]
  }
}

function buildOversizedMetadataAudit(): AuditLogInput {
  const largeHeader = 'h'.repeat(128 * 1024)
  const largeContentType = `application/json;${'m'.repeat(4096)}`
  const payloads: AuditLogInput['payloads'] = [
    ...Array.from({ length: 48 }, (_, index) => ({
      partType: index % 2 === 0 ? 'upstream_request' as const : 'upstream_response' as const,
      attemptTempId: `attempt_${index}`,
      headers: {
        'x-large-header': largeHeader
      },
      contentType: 'application/json',
      captureStatus: 'complete' as const
    })),
    ...Array.from({ length: 10000 }, (_, index) => ({
      partType: 'gateway_metadata' as const,
      contentType: largeContentType,
      captureStatus: 'complete' as const,
      sequenceIndex: 1000 + index
    }))
  ]
  return {
    traceId: 'trace-background-ipc-large-audit-metadata',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'large_audit_metadata',
    errorMessage: 'x'.repeat(1024 * 1024),
    sampleBucket: 0,
    sampleReason: 'regression',
    captureStatus: 'complete',
    startedAt: '2000-01-01T00:00:00.000Z',
    endedAt: '2000-01-01T00:00:00.000Z',
    attempts: Array.from({ length: 40 }, (_, index) => ({
      attemptIndex: index,
      upstreamMethod: 'POST',
      upstreamUrl: `https://api.openai.com/v1/responses?metadata=${'u'.repeat(128 * 1024)}`,
      success: false,
      errorPhase: 'upstream',
      errorCode: 'large_audit_metadata',
      errorMessage: 'e'.repeat(128 * 1024),
      startedAt: '2000-01-01T00:00:00.000Z',
      endedAt: '2000-01-01T00:00:00.001Z',
      durationMs: 1
    })),
    payloads
  }
}

function sha256Buffer(buffer: Buffer): string {
  return createHash('sha256').update(buffer).digest('hex')
}
