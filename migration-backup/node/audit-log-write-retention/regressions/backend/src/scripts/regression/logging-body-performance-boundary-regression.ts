import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { serialize } from 'node:v8'

import {
  encodeAuditLogStreamPayload,
  measureAuditLogStreamPayloadBaseBytes,
  measureAuditLogStreamPayloadItemBytes
} from '../../modules/audit-logs/audit-log-stream-codec.js'
import {
  prepareAuditLogForIpcInWorker,
  stopAuditLogTransportWorker
} from '../../modules/audit-logs/audit-log-transport.service.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'
import {
  operationLogSummaryFromPrepared,
  prepareOperationLogInput
} from '../../storage/operation-log-write-input.js'

const auditTransportWorkerSource = readFileSync(
  new URL('../../modules/audit-logs/audit-log-transport-worker.ts', import.meta.url),
  'utf8'
)
const auditPayloadSummarySource = readFileSync(
  new URL('../../modules/audit-logs/audit-payload-summary.ts', import.meta.url),
  'utf8'
)
const publicApiLogCaptureSource = readFileSync(
  new URL('../../modules/public-api-logs/public-api-log-capture.middleware.ts', import.meta.url),
  'utf8'
)
const operationLogWriteInputSource = readFileSync(
  new URL('../../storage/operation-log-write-input.ts', import.meta.url),
  'utf8'
)

const wholeAuditEncodePasses = auditTransportWorkerSource.match(/encodeAuditLogStreamPayload\(/g)?.length ?? 0
assert.equal(wholeAuditEncodePasses, 1, 'Redis transport must encode the final whole audit record exactly once')
assert.match(auditTransportWorkerSource, /class AuditLogTransportByteTracker/, 'audit transport must use incremental byte accounting')
assert.doesNotMatch(
  auditTransportWorkerSource,
  /while\s*\([^)]*(?:encodeAuditLogStreamPayload|auditLogTransportEncodedBytes)/,
  'audit transport must not re-encode the whole record in a shrink loop'
)
assert.doesNotMatch(
  auditPayloadSummarySource,
  /(?:for|while)\s*\([^)]*\)[\s\S]{0,400}JSON\.parse\(payload\.body\)/,
  'an existing audit summary must not be reparsed in a local shrink loop'
)

const auditSizeFixture: AuditLogInput = {
  id: 'audit-size-formula',
  traceId: 'trace-size-formula',
  trafficSource: 'gateway',
  method: 'POST',
  path: '/v1/responses',
  auditOutcome: 'success',
  success: true,
  sampleBucket: 1,
  sampleReason: 'performance_boundary',
  captureStatus: 'complete',
  startedAt: '2026-07-28T00:00:00.000Z',
  endedAt: '2026-07-28T00:00:00.001Z',
  durationMs: 1,
  attempts: [],
  payloads: [
    {
      partType: 'client_request',
      sequenceIndex: 0,
      contentType: 'application/json',
      headers: { 'x-size-fixture': ['quote:"', 'unicode:\u4f60\u597d'] },
      body: Buffer.from('{"input":"buffer"}', 'utf8'),
      captureStatus: 'complete'
    },
    {
      partType: 'upstream_response',
      sequenceIndex: 1,
      contentType: 'application/json',
      body: '{"output":"line\\nquote:\\" unicode:\u4f60\u597d"}',
      captureStatus: 'complete'
    }
  ]
}
// The empty payload array already contributes its two brackets. Replacing it
// adds every encoded item plus one comma between adjacent items.
const measuredAuditBytes = measureAuditLogStreamPayloadBaseBytes(auditSizeFixture)
  + auditSizeFixture.payloads.reduce((total, payload) => total + measureAuditLogStreamPayloadItemBytes(payload), 0)
  + Math.max(0, auditSizeFixture.payloads.length - 1)
assert.equal(
  measuredAuditBytes,
  Buffer.byteLength(encodeAuditLogStreamPayload(auditSizeFixture), 'utf8'),
  'incremental audit byte accounting must exactly equal final UTF-8 JSON encoding for buffers and escaped strings'
)

const ipcBudgetFixture: AuditLogInput = {
  ...auditSizeFixture,
  id: 'audit-ipc-envelope-budget',
  traceId: 'trace-ipc-envelope-budget',
  payloads: Array.from({ length: 5 }, (_, sequenceIndex) => ({
    partType: sequenceIndex === 0 ? 'client_request' as const : 'upstream_response' as const,
    sequenceIndex,
    contentType: 'application/json',
    body: Buffer.alloc(3 * 1024 * 1024, 0x61 + sequenceIndex),
    captureStatus: 'complete' as const
  }))
}
try {
  const ipcPrepared = await prepareAuditLogForIpcInWorker(ipcBudgetFixture)
  const ipcEnvelopeBytes = serialize({ id: Number.MAX_SAFE_INTEGER, ok: true, prepared: ipcPrepared }).byteLength
  assert(
    ipcEnvelopeBytes <= 4 * 1024 * 1024,
    `prepared audit IPC envelope must stay within 4MiB, got ${ipcEnvelopeBytes}`
  )
} finally {
  await stopAuditLogTransportWorker()
}

assert.doesNotMatch(
  publicApiLogCaptureSource,
  /JSON\.parse\(/,
  'public API logging must not parse a response string only to render the log snapshot'
)
assert.doesNotMatch(
  publicApiLogCaptureSource,
  /estimatePayloadSizeBytes/,
  'public API logging must not traverse and stringify the same body before the final bounded snapshot'
)

assert.doesNotMatch(
  operationLogWriteInputSource,
  /operationLogSummaryFromRow|JSON\.parse\(/,
  'operation log writes must build their return value from prepared input instead of reparsing stored JSON'
)

const circular: Record<string, unknown> = {}
circular.self = circular
const prepared = prepareOperationLogInput({
  actorSystemAccountId: 'sys-performance-boundary',
  actorRole: 'admin',
  module: 'regression',
  action: 'create',
  operationKey: 'logging_body_performance_boundary',
  resourceType: 'regression',
  summary: 'logging body performance boundary',
  changes: [{ field: 'circular', label: 'Circular', after: circular }],
  metadata: circular
})
const summary = operationLogSummaryFromPrepared(prepared)
assert.equal(prepared.changesJson, '[]', 'non-serializable operation changes must keep the persisted fallback')
assert.equal(prepared.metadataJson, '{}', 'non-serializable operation metadata must keep the persisted fallback')
assert.deepEqual(summary.changes, [], 'operation log write result must match the persisted changes fallback without parsing JSON')
assert.deepEqual(summary.metadata, {}, 'operation log write result must match the persisted metadata fallback without parsing JSON')

console.log('logging body performance boundary regression passed')
