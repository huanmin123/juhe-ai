import { strict as assert } from 'node:assert'

import { auditLogQueueOverflowDropIndex } from '../../modules/audit-logs/audit-log-queue.service.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'

const finalizedSuccess = auditLog('finalized-success', true, 'finalized')
const inProgress = auditLog('stream-in-progress', true, 'in_progress')
const failedTerminal = auditLog('failed-terminal', false, 'finalized')

assert.equal(
  auditLogQueueOverflowDropIndex([queued(inProgress), queued(finalizedSuccess), queued(failedTerminal)]),
  1,
  '队列满时必须先丢弃已终态成功记录，保留进行中流式审计和失败终态'
)
assert.equal(
  auditLogQueueOverflowDropIndex([queued(inProgress), queued(failedTerminal)]),
  0,
  '没有已终态成功记录时才允许丢弃进行中占位'
)
assert.equal(
  auditLogQueueOverflowDropIndex([queued(failedTerminal)]),
  0,
  '队列只剩失败终态时仍必须给容量保护一个确定的兜底删除目标'
)

console.log('审计进行中占位队列优先级回归通过：队列溢出先删除终态成功记录')

function auditLog(id: string, success: boolean, lifecycleStatus: 'in_progress' | 'finalized'): AuditLogInput {
  return {
    id,
    lifecycleStatus,
    traceId: id,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: success ? 'success' : 'stream_failed',
    success,
    sampleBucket: 0,
    sampleReason: lifecycleStatus,
    captureStatus: 'metadata_only',
    startedAt: '2026-08-05T00:00:00.000Z',
    endedAt: '2026-08-05T00:00:00.000Z',
    attempts: [],
    payloads: []
  }
}

function queued(input: AuditLogInput): { input: AuditLogInput; success: boolean } {
  return { input, success: input.success }
}
