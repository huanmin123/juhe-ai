import { strict as assert } from 'node:assert'

import { auditLogGoInputMaxBytes, prepareAuditLogForGoInput } from '../../modules/audit-logs/audit-log-go-input-budget.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'

const limits = {
  successFullBodyLimitBytes: 512 * 1024,
  problemFullBodyLimitBytes: 2 * 1024 * 1024
}

const successInput = auditInput('success-summary', [Buffer.alloc(600 * 1024, 0x61)], true)
const originalSuccessBody = successInput.payloads[0]?.body
const success = prepareAuditLogForGoInput(successInput, limits)
assert(success.body.byteLength <= auditLogGoInputMaxBytes, '成功审计 Go JSON body 必须严格不超过 4MiB')
assert.equal(successInput.payloads[0]?.body, originalSuccessBody, '预算收敛不得修改 capture 的原始 Buffer')
assert.equal(successInput.payloads[0]?.captureStatus, 'complete', '预算收敛不得修改 capture 的原始状态')
assert.equal(success.input.payloads[0]?.captureStatus, 'summary_only', '成功正文超过 512KB 后必须保留 summary_only')
assert.match(success.input.payloads[0]?.bodySha256 ?? '', /^[a-f0-9]{64}$/, 'summary 必须保留原始 SHA-256')
assert.equal(success.input.captureStatus, 'complete', '正文 summary 不应把整条审计伪标为 dropped')

const aggregateInput = auditInput('aggregate-summary-budget', [
  Buffer.alloc(3 * 1024 * 1024, 0x64),
  Buffer.alloc(3 * 1024 * 1024, 0x65)
], false)
const aggregate = prepareAuditLogForGoInput(aggregateInput, limits)
assert(aggregate.body.byteLength <= auditLogGoInputMaxBytes, '聚合正文必须收敛到 4MiB 内')
assert(aggregate.input.payloads.every((payload) => payload.captureStatus === 'summary_only'), '聚合正文必须保留 summary_only')
assert.equal(aggregate.input.captureStatus, 'complete', '仅 summary 收敛必须保留整条记录的 complete 状态')

const headerInput = auditInput('header-tombstone-budget', [], false)
headerInput.payloads = Array.from({ length: 73 }, (_, sequenceIndex) => ({
  id: `payload-header-${sequenceIndex}`,
  partType: sequenceIndex === 0 ? 'client_request' : 'upstream_response',
  sequenceIndex,
  contentType: 'application/json',
  headers: { 'x-audit-large-header': 'h'.repeat(96 * 1024) },
  captureStatus: 'complete' as const
}))
const header = prepareAuditLogForGoInput(headerInput, limits)
assert(header.body.byteLength <= auditLogGoInputMaxBytes, 'headers 超限必须收敛到 4MiB')
assert.equal(header.input.payloads.length, 73, '逐条 headers 裁剪不得删除 payload 引用')
assert.deepEqual(header.input.payloads.map((payload) => payload.id), headerInput.payloads.map((payload) => payload.id), 'tombstone 必须保留稳定 ID 和顺序')
assert(header.input.payloads.some((payload) => payload.captureStatus === 'dropped' && payload.dropReason === 'transport_budget'), 'headers 裁剪必须留下 transport_budget tombstone')
assert.equal(header.input.captureStatus, 'dropped', '结构性 headers 裁剪必须标记 dropped')

const rangeInput = auditInput('range-tombstone-budget', [], false)
rangeInput.payloads = Array.from({ length: 25_000 }, (_, sequenceIndex) => ({
  id: `payload-range-${sequenceIndex}-${'x'.repeat(160)}`,
  partType: 'upstream_response' as const,
  sequenceIndex,
  contentType: 'application/json',
  captureStatus: 'complete' as const
}))
const range = prepareAuditLogForGoInput(rangeInput, limits)
assert(range.body.byteLength <= auditLogGoInputMaxBytes, '范围 tombstone 必须满足 4MiB 硬预算')
assert.equal(range.input.payloads.length, 1, '逐条 tombstone 元数据超限时必须收敛为单范围 tombstone')
assert.equal(range.input.payloads[0]?.partType, 'gateway_metadata', '范围 tombstone 必须使用 gateway_metadata')
assert.equal(range.input.payloads[0]?.captureStatus, 'dropped', '范围 tombstone 必须显式标记 dropped')
assert.equal(range.input.payloads[0]?.dropReason, 'transport_budget', '范围 tombstone 必须保留 transport_budget 原因')

console.log('F3 Go input budget regression passed: exact 4MiB JSON envelope, immutable capture, summary and transport tombstones.')

function auditInput(traceId: string, bodies: Buffer[], success: boolean): AuditLogInput {
  const timestamp = '2026-08-09T00:00:00.000Z'
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
    startedAt: timestamp,
    endedAt: timestamp,
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
