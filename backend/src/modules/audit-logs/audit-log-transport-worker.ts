import { createHash } from 'node:crypto'
import { parentPort } from 'node:worker_threads'

import type { AuditLogInput } from '../../storage/audit-log-types.js'

const {
  encodeAuditLogStreamPayload
} = await import(resolveWorkerModuleUrl('./audit-log-stream-codec')) as typeof import('./audit-log-stream-codec.js')

const auditTransportMaxBytes = 4 * 1024 * 1024
const auditTransportSuccessBodyMaxBytes = 512 * 1024
const auditTransportFailureBodyMaxBytes = 2 * 1024 * 1024

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
    if (message.mode === 'redis_stream') {
      workerPort.postMessage({
        id: message.id,
        ok: true,
        encoded: encodeAuditLogStreamPayload(prepared)
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
  const bodyMaxBytes = input.success
    ? auditTransportSuccessBodyMaxBytes
    : auditTransportFailureBodyMaxBytes
  let estimatedBytes = 2048 + Math.min(input.attempts.length, 16) * 1024
  let changed = input.attempts.length > 16 || input.payloads.length > 32
  const sourcePayloads = input.payloads.length <= 32
    ? input.payloads
    : [...input.payloads.slice(0, 31), input.payloads[input.payloads.length - 1]].filter(Boolean)
  const payloads = sourcePayloads.map((payload) => {
    const headerBytes = estimateHeadersBytes(payload.headers)
    const bodyBytes = auditPayloadBodyBytes(payload.body)
    const shouldDropBody = bodyBytes > bodyMaxBytes
      || estimatedBytes + headerBytes + bodyBytes + 512 > auditTransportMaxBytes
    estimatedBytes += headerBytes + (shouldDropBody ? 0 : bodyBytes) + 512
    if (!shouldDropBody) return payload
    changed = true
    return hashOnlyPayload(payload, bodyBytes)
  })

  if (estimatedBytes > auditTransportMaxBytes) {
    for (let index = 0; index < payloads.length; index += 1) {
      const payload = payloads[index]
      if (!payload?.headers) continue
      estimatedBytes = Math.max(0, estimatedBytes - estimateHeadersBytes(payload.headers))
      payloads[index] = {
        ...payload,
        headers: undefined,
        captureStatus: payload.captureStatus === 'complete' || payload.captureStatus === undefined
          ? 'dropped'
          : payload.captureStatus
      }
      changed = true
      if (estimatedBytes <= auditTransportMaxBytes) break
    }
  }

  return {
    ...truncateAuditLogStrings(input),
    attempts: input.attempts.length <= 16
      ? input.attempts.map(truncateAttemptStrings)
      : [...input.attempts.slice(0, 15), input.attempts[input.attempts.length - 1]].filter(Boolean).map(truncateAttemptStrings),
    payloads,
    captureStatus: changed && input.captureStatus !== 'overflow' ? 'dropped' : input.captureStatus
  }
}

function hashOnlyPayload(
  payload: AuditLogInput['payloads'][number],
  bodyBytes: number
): AuditLogInput['payloads'][number] {
  const bodySha256 = payload.bodySha256 ?? auditPayloadBodySha256(payload.body)
  return {
    ...payload,
    body: undefined,
    bodySha256,
    rawBodySizeBytes: payload.rawBodySizeBytes ?? bodyBytes,
    contentEncoding: undefined,
    captureStatus: bodySha256 ? 'hash_only' : 'dropped'
  }
}

function auditPayloadBodyBytes(body: Buffer | string | undefined): number {
  if (body === undefined) return 0
  return Buffer.isBuffer(body) ? body.byteLength : Buffer.byteLength(body, 'utf8')
}

function auditPayloadBodySha256(body: Buffer | string | undefined): string | undefined {
  if (body === undefined) return undefined
  return createHash('sha256').update(Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')).digest('hex')
}

function estimateHeadersBytes(headers: Record<string, string | string[]> | undefined): number {
  if (!headers) return 0
  return Buffer.byteLength(JSON.stringify(headers), 'utf8')
}

function truncateAuditLogStrings(input: AuditLogInput): AuditLogInput {
  return {
    ...input,
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
