import { parentPort, workerData } from 'node:worker_threads'

import type { AuditLogInput } from '../../storage/audit-log-types.js'

const {
  encodeAuditLogStreamPayload
} = await import(resolveWorkerModuleUrl('./audit-log-stream-codec')) as typeof import('./audit-log-stream-codec.js')
const {
  summarizeAuditPayloadForLimit
} = await import(resolveWorkerModuleUrl('./audit-payload-summary')) as typeof import('./audit-payload-summary.js')

const auditTransportMaxBytes = 4 * 1024 * 1024
const auditTransportMinimumSummaryWindowBytes = 4 * 1024
interface AuditLogTransportWorkerData {
  successFullBodyLimitBytes: number
  problemFullBodyLimitBytes: number
}

const transportSettings = workerData as AuditLogTransportWorkerData

type AuditLogTransportMode = 'ipc' | 'redis_stream'

interface AuditLogTransportWorkerRequest {
  id: number
  mode: AuditLogTransportMode
  input: AuditLogInput
}

if (!parentPort) {
  throw new Error('审计传输 worker 缺少 parentPort')
}
const workerPort = parentPort

workerPort.on('message', (message: AuditLogTransportWorkerRequest) => {
  try {
    const input = rehydrateAuditLogBuffers(message.input)
    const prepared = prepareAuditLogForTransport(input)
    const encoded = encodeAuditLogStreamPayload(prepared)
    if (encodedTransportBytes(encoded) > auditTransportMaxBytes) {
      throw new Error('审计传输 worker 输出超过 4MiB 消息预算')
    }
    if (message.mode === 'redis_stream') {
      workerPort.postMessage({
        id: message.id,
        ok: true,
        encoded
      })
      return
    }
    workerPort.postMessage({
      id: message.id,
      ok: true,
      prepared
    })
  } catch (error) {
    workerPort.postMessage({
      id: message.id,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
})

function rehydrateAuditLogBuffers(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    payloads: input.payloads.map((payload) => ({
      ...payload,
      body: rehydrateBuffer(payload.body)
    }))
  }
}

function prepareAuditLogForTransport(input: AuditLogInput): AuditLogInput {
  const bodyMaxBytes = input.auditOutcome === 'success'
    ? transportSettings.successFullBodyLimitBytes
    : transportSettings.problemFullBodyLimitBytes
  let structureDropped = input.attempts.length > 16 || input.payloads.length > 32
  const sourcePayloads = input.payloads.length <= 32
    ? input.payloads
    : [...input.payloads.slice(0, 31), input.payloads[input.payloads.length - 1]].filter(Boolean)
  const payloads = sourcePayloads.map((payload) => {
    const preparedPayload = truncatePayloadStrings({ ...payload })
    summarizeAuditPayloadForLimit(preparedPayload, bodyMaxBytes)
    return preparedPayload
  })
  let prepared: AuditLogInput = {
    ...truncateAuditLogStrings(input),
    attempts: input.attempts.length <= 16
      ? input.attempts.map(truncateAttemptStrings)
      : [...input.attempts.slice(0, 15), input.attempts[input.attempts.length - 1]].filter(Boolean).map(truncateAttemptStrings),
    payloads,
    captureStatus: structureDropped && input.captureStatus !== 'overflow' ? 'dropped' : input.captureStatus
  }

  if (auditLogTransportEncodedBytes(prepared) <= auditTransportMaxBytes) {
    return prepared
  }

  const bodyIndexes = prepared.payloads
    .map((payload, index) => ({ index, bytes: auditPayloadBodyBytes(payload.body) }))
    .filter((item) => item.bytes > 0)
    .sort((left, right) => right.bytes - left.bytes)
  for (const item of bodyIndexes) {
    const payload = prepared.payloads[item.index]
    if (!payload) continue
    summarizeAuditPayloadForLimit(payload, bodyMaxBytes, {
      force: true,
      includeGatewayMetadata: true,
      reason: 'transport_message_budget'
    })
    if (auditLogTransportEncodedBytes(prepared) <= auditTransportMaxBytes) {
      return prepared
    }
  }

  prepared = shrinkAuditPayloadSummariesToTransportBudget(prepared, bodyMaxBytes)
  if (auditLogTransportEncodedBytes(prepared) <= auditTransportMaxBytes) {
    return prepared
  }

  const headerIndexes = prepared.payloads
    .map((payload, index) => ({ index, bytes: estimateHeadersBytes(payload.headers) }))
    .filter((item) => item.bytes > 0)
    .sort((left, right) => right.bytes - left.bytes)
  for (const item of headerIndexes) {
    const payload = prepared.payloads[item.index]
    if (!payload?.headers) continue
    prepared.payloads[item.index] = { ...payload, headers: undefined }
    structureDropped = true
    if (auditLogTransportEncodedBytes(prepared) <= auditTransportMaxBytes) {
      return markAuditLogStructureDropped(prepared)
    }
  }

  while (prepared.payloads.length > 2 && auditLogTransportEncodedBytes(prepared) > auditTransportMaxBytes) {
    prepared = {
      ...prepared,
      payloads: [
        ...prepared.payloads.slice(0, Math.floor(prepared.payloads.length / 2)),
        ...prepared.payloads.slice(Math.floor(prepared.payloads.length / 2) + 1)
      ]
    }
    structureDropped = true
  }
  if (auditLogTransportEncodedBytes(prepared) > auditTransportMaxBytes) {
    for (const payload of prepared.payloads) {
      if (auditLogTransportEncodedBytes(prepared) <= auditTransportMaxBytes) break
      summarizeAuditPayloadForLimit(payload, 0, {
        force: true,
        includeGatewayMetadata: true,
        reason: 'transport_message_budget'
      })
      structureDropped = true
    }
  }
  if (structureDropped) {
    prepared = markAuditLogStructureDropped(prepared)
  }
  if (auditLogTransportEncodedBytes(prepared) > auditTransportMaxBytes) {
    prepared = markAuditLogStructureDropped({
      ...prepared,
      attempts: [],
      payloads: []
    })
  }
  return prepared
}

function shrinkAuditPayloadSummariesToTransportBudget(input: AuditLogInput, initialLimitBytes: number): AuditLogInput {
  const prepared = input
  let summaryLimitBytes = Math.max(auditTransportMinimumSummaryWindowBytes, Math.trunc(initialLimitBytes))
  while (auditLogTransportEncodedBytes(prepared) > auditTransportMaxBytes && summaryLimitBytes > auditTransportMinimumSummaryWindowBytes) {
    summaryLimitBytes = Math.max(auditTransportMinimumSummaryWindowBytes, Math.floor(summaryLimitBytes / 2))
    for (const payload of prepared.payloads) {
      if (payload.captureStatus !== 'summary_only') continue
      summarizeAuditPayloadForLimit(payload, summaryLimitBytes, {
        force: true,
        includeGatewayMetadata: true,
        reason: 'transport_message_budget'
      })
    }
  }
  return prepared
}

function markAuditLogStructureDropped(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    captureStatus: input.captureStatus === 'overflow' ? 'overflow' : 'dropped'
  }
}

function auditPayloadBodyBytes(body: Buffer | string | undefined): number {
  if (body === undefined) return 0
  return Buffer.isBuffer(body) ? body.byteLength : Buffer.byteLength(body, 'utf8')
}

function estimateHeadersBytes(headers: Record<string, string | string[]> | undefined): number {
  if (!headers) return 0
  return Buffer.byteLength(JSON.stringify(headers), 'utf8')
}

function truncatePayloadStrings(payload: AuditLogInput['payloads'][number]): AuditLogInput['payloads'][number] {
  return {
    ...payload,
    id: truncateOptionalString(payload.id, 256),
    attemptTempId: truncateOptionalString(payload.attemptTempId, 256),
    contentType: truncateOptionalString(payload.contentType, 512),
    contentEncoding: truncateOptionalString(payload.contentEncoding, 128),
    createdAt: truncateOptionalString(payload.createdAt, 128)
  }
}

function auditLogTransportEncodedBytes(input: AuditLogInput): number {
  return encodedTransportBytes(encodeAuditLogStreamPayload(input))
}

function encodedTransportBytes(encoded: string): number {
  return Buffer.byteLength(encoded, 'utf8')
}

function truncateAuditLogStrings(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
    conversationKey: truncateOptionalString(input.conversationKey, 256),
    sessionNamespace: truncateOptionalString(input.sessionNamespace, 256),
    sessionSource: truncateOptionalString(input.sessionSource, 256),
    sessionResolution: truncateOptionalString(input.sessionResolution, 64),
    sessionConfidence: truncateOptionalString(input.sessionConfidence, 64),
    threadKey: truncateOptionalString(input.threadKey, 256),
    turnKey: truncateOptionalString(input.turnKey, 256),
    agentKey: truncateOptionalString(input.agentKey, 256),
    parentResponseKey: truncateOptionalString(input.parentResponseKey, 256),
    path: truncateString(input.path, 2048),
    queryString: truncateOptionalString(input.queryString, 4096),
    model: truncateOptionalString(input.model, 512),
    upstreamModel: truncateOptionalString(input.upstreamModel, 512),
    pricingModel: truncateOptionalString(input.pricingModel, 512),
    modelMappingSource: truncateOptionalString(input.modelMappingSource, 128),
    clientIp: truncateOptionalString(input.clientIp, 256),
    userAgent: truncateOptionalString(input.userAgent, 2048),
    errorPhase: truncateOptionalString(input.errorPhase, 256),
    errorCode: truncateOptionalString(input.errorCode, 512),
    errorMessage: truncateOptionalString(input.errorMessage, 4096),
    sampleReason: truncateString(input.sampleReason, 1024)
  }
}

function truncateAttemptStrings(attempt: AuditLogInput['attempts'][number]): AuditLogInput['attempts'][number] {
  return {
    ...attempt,
    proxyUrl: truncateOptionalString(attempt.proxyUrl, 2048),
    upstreamUrl: truncateString(attempt.upstreamUrl, 4096),
    errorPhase: truncateOptionalString(attempt.errorPhase, 256),
    errorCode: truncateOptionalString(attempt.errorCode, 512),
    errorMessage: truncateOptionalString(attempt.errorMessage, 4096)
  }
}

function truncateOptionalString(value: string | undefined, maxBytes: number): string | undefined {
  return typeof value === 'string' ? truncateString(value, maxBytes) : undefined
}

function truncateString(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) return value
  return `${Buffer.from(value).subarray(0, Math.max(0, maxBytes - 32)).toString('utf8')}...[truncated]`
}

function rehydrateBuffer(value: unknown): Buffer | string | undefined {
  if (Buffer.isBuffer(value) || typeof value === 'string' || value === undefined) {
    return value
  }
  if (value instanceof Uint8Array) {
    return Buffer.from(value.buffer, value.byteOffset, value.byteLength)
  }
  return undefined
}

function resolveWorkerModuleUrl(relativePathWithoutExtension: string): string {
  const extension = import.meta.url.endsWith('.ts') ? '.ts' : '.js'
  return new URL(`${relativePathWithoutExtension}${extension}`, import.meta.url).href
}
