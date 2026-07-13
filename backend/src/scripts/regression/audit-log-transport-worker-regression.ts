import { strict as assert } from 'node:assert'

import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { decodeAuditLogStreamPayload } from '../../modules/audit-logs/audit-log-stream-codec.js'
import {
  encodeAuditLogForRedisStreamInWorker,
  getAuditLogTransportRuntime,
  stopAuditLogTransportWorker
} from '../../modules/audit-logs/audit-log-transport.service.js'

const largeBody = Buffer.alloc(32 * 1024 * 1024, 0x61)
const input = auditInput('trace-audit-transport-large', largeBody, false)

try {
  let immediateRan = false
  const immediate = new Promise<void>((resolve) => {
    setImmediate(() => {
      immediateRan = true
      resolve()
    })
  })
  const encodedPromise = encodeAuditLogForRedisStreamInWorker(input)
  await immediate
  assert.equal(immediateRan, true, '大审计编码期间事件循环应继续执行')

  const encoded = await encodedPromise
  const decoded = decodeAuditLogStreamPayload(encoded)
  const payload = decoded.payloads[0]
  assert(payload, '大审计编码后应保留 payload 元数据')
  assert.equal(payload.body, undefined, '超过传输预算的大正文不得进入 Redis Stream')
  assert.equal(payload.rawBodySizeBytes, largeBody.byteLength, '降级记录应保留原始正文大小')
  assert.equal(payload.captureStatus, 'hash_only', '超过传输预算的大正文应明确标记 hash_only')
  assert.match(payload.bodySha256 ?? '', /^[a-f0-9]{64}$/, '降级记录应保留完整 SHA-256')
  assert.equal(decoded.captureStatus, 'dropped', '正文传输降级后主审计记录应标记 dropped')
  assert(encoded.length < 1024 * 1024, '32MB 大正文降级后的 Redis 消息应保持有界')

  const successEncoded = await encodeAuditLogForRedisStreamInWorker(
    auditInput('trace-audit-transport-success-budget', Buffer.alloc(600 * 1024, 0x62), true)
  )
  const successPayload = decodeAuditLogStreamPayload(successEncoded).payloads[0]
  assert.equal(successPayload?.captureStatus, 'hash_only', '成功审计超过 512KB 保留预算后应降级')
  assert.match(successPayload?.bodySha256 ?? '', /^[a-f0-9]{64}$/, '成功审计降级后应保留 SHA-256')

  const runtime = getAuditLogTransportRuntime()
  assert.equal(runtime.queuedJobs, 0, '审计编码完成后队列应清空')
  assert.equal(runtime.activeJobs, 0, '审计编码完成后不应残留活跃任务')
  assert.equal(runtime.completedCount, 2, '两条审计编码任务应全部完成')
  assert.equal(runtime.failedCount, 0, '审计编码任务不应失败')

  console.log('审计传输 worker 回归通过：32MB 正文的哈希、降级和 JSON/base64 编码均在有界 worker 中完成')
} finally {
  await stopAuditLogTransportWorker()
}

function auditInput(traceId: string, body: Buffer, success: boolean): AuditLogInput {
  const startedAt = '2026-07-14T00:00:00.000Z'
  return {
    id: `audit-${traceId}`,
    traceId,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    auditOutcome: success ? 'success' : 'upstream_failed',
    success,
    sampleBucket: 1,
    sampleReason: success ? 'success_hot_full_retention' : 'full_capture',
    captureStatus: 'complete',
    startedAt,
    endedAt: startedAt,
    durationMs: 1,
    attempts: [],
    payloads: [{
      id: `payload-${traceId}`,
      partType: 'client_request',
      sequenceIndex: 0,
      contentType: 'application/json',
      body,
      captureStatus: 'complete'
    }]
  }
}
