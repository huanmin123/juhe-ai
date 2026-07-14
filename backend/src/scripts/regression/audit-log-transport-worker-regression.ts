import { strict as assert } from 'node:assert'

import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { decodeAuditLogStreamPayload } from '../../modules/audit-logs/audit-log-stream-codec.js'
import {
  encodeAuditLogForRedisStreamInWorker,
  getAuditLogTransportRuntime,
  stopAuditLogTransportWorker
} from '../../modules/audit-logs/audit-log-transport.service.js'

const mib = 1024 * 1024
const transportMaxBytes = 4 * mib
const summaryEdgeBytes = 256 * 1024
const largeBody = Buffer.from(JSON.stringify({
  model: 'gpt-5.6-sol',
  requestKind: 'audit-transport-summary',
  input: 'a'.repeat(32 * mib)
}), 'utf8')
const input = auditInput('trace-audit-transport-large', [largeBody], false)

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
  assert(Buffer.byteLength(encoded, 'utf8') <= transportMaxBytes, 'Redis Stream codec 后的实际 UTF-8 输出必须严格不超过 4MiB')
  const decoded = decodeAuditLogStreamPayload(encoded)
  const payload = decoded.payloads[0]
  assert(payload, '大审计编码后应保留 payload 元数据')
  assert.equal(payload.captureStatus, 'summary_only', '超过保全档位的大正文必须保存 summary_only，不得退化为 hash_only')
  assert.match(payload.bodySha256 ?? '', /^[a-f0-9]{64}$/, '摘要记录应保留完整 SHA-256')
  assert.equal(payload.rawBodySizeBytes, largeBody.byteLength, '摘要记录应保留原始正文大小')
  assert.equal(typeof payload.body, 'string', '摘要正文应作为结构化 JSON 保存')
  const summary = JSON.parse(String(payload.body)) as Record<string, unknown>
  assert.equal(summary.type, 'audit_payload_summary', '大正文必须保留审计摘要结构')
  assert.equal(summary.retainedHeadBytes, summaryEdgeBytes, '大正文摘要必须保留前 256KB 窗口')
  assert.equal(summary.retainedTailBytes, summaryEdgeBytes, '大正文摘要必须保留后 256KB 窗口')
  assert.equal(Buffer.from(String(summary.headBase64), 'base64').byteLength, summaryEdgeBytes, '头部窗口 base64 必须可还原为 256KB')
  assert.equal(Buffer.from(String(summary.tailBase64), 'base64').byteLength, summaryEdgeBytes, '尾部窗口 base64 必须可还原为 256KB')
  const jsonSummary = summary.json as Record<string, unknown>
  assert.equal(jsonSummary.topLevelType, 'object', '超大 JSON 不完整解析时仍应保留顶层类型')
  assert((jsonSummary.topLevelKeys as string[]).includes('model'), '超大 JSON 摘要应保留可推断的顶层 key')
  assert.equal(decoded.captureStatus, 'complete', '正文按既定摘要契约保全时不应把整条审计伪标为 dropped')

  const aggregateEncoded = await encodeAuditLogForRedisStreamInWorker(
    auditInput('trace-audit-transport-codec-budget', [
      Buffer.alloc(Math.floor(1.9 * mib), 0x62),
      Buffer.alloc(Math.floor(1.9 * mib), 0x63)
    ], false)
  )
  assert(
    Buffer.byteLength(aggregateEncoded, 'utf8') <= transportMaxBytes,
    '两个 1.9MiB 正文经 codec/base64 放大后仍必须严格不超过 4MiB'
  )
  const aggregatePayloads = decodeAuditLogStreamPayload(aggregateEncoded).payloads
  assert(aggregatePayloads.some((item) => item.captureStatus === 'summary_only'), '聚合输出越界时应在 worker 内按摘要契约收敛正文')
  assert(aggregatePayloads.every((item) => item.captureStatus !== 'hash_only'), '聚合输出收敛不得退化为 hash_only')

  const successEncoded = await encodeAuditLogForRedisStreamInWorker(
    auditInput('trace-audit-transport-success-budget', [Buffer.alloc(600 * 1024, 0x64)], true)
  )
  const successPayload = decodeAuditLogStreamPayload(successEncoded).payloads[0]
  assert.equal(successPayload?.captureStatus, 'summary_only', '成功审计超过 512KB 保留预算后应转为摘要')
  assert.match(successPayload?.bodySha256 ?? '', /^[a-f0-9]{64}$/, '成功审计摘要应保留 SHA-256')
  assert(Buffer.byteLength(successEncoded, 'utf8') <= transportMaxBytes, '成功审计最终编码也必须遵守 4MiB 硬预算')

  const retryInput = auditInput('trace-audit-transport-success-after-retry', [Buffer.alloc(600 * 1024, 0x65)], true)
  retryInput.auditOutcome = 'success_after_retry'
  const retryPayload = decodeAuditLogStreamPayload(await encodeAuditLogForRedisStreamInWorker(retryInput)).payloads[0]
  assert.equal(retryPayload?.captureStatus, 'complete', '重试后成功属于问题链路，必须使用 2MB 问题正文上限')

  const runtime = getAuditLogTransportRuntime()
  assert.equal(runtime.queuedJobs, 0, '审计编码完成后队列应清空')
  assert.equal(runtime.activeJobs, 0, '审计编码完成后不应残留活跃任务')
  assert.equal(runtime.completedCount, 4, '四条审计编码任务应全部完成')
  assert.equal(runtime.failedCount, 0, '审计编码任务不应失败')

  console.log('审计传输 worker 回归通过：最终 codec 输出严格受 4MiB 约束，大正文保留 256KB 头尾窗口和 JSON 结构摘要')
} finally {
  await stopAuditLogTransportWorker()
}

function auditInput(traceId: string, bodies: Buffer[], success: boolean): AuditLogInput {
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
    payloads: bodies.map((body, sequenceIndex) => ({
      id: `payload-${traceId}-${sequenceIndex}`,
      partType: sequenceIndex === 0 ? 'client_request' : 'upstream_response',
      sequenceIndex,
      contentType: 'application/json',
      body,
      captureStatus: 'complete'
    }))
  }
}
