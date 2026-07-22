import type { AuditLogInput } from '../../storage/audit-log-types.js'

const capacityFallbackMaxAttempts = 16
const capacityFallbackMaxPayloads = 32
const capacityFallbackMaxHeaderCount = 64
const capacityFallbackHeaderBudgetBytes = 64 * 1024

export function buildAuditLogTransportCapacityFallback(input: AuditLogInput): AuditLogInput {
  const headerBudget = { remainingBytes: capacityFallbackHeaderBudgetBytes }
  const attempts = limitItems(input.attempts, capacityFallbackMaxAttempts)
    .map((attempt) => ({
      ...attempt,
      id: truncateOptionalString(attempt.id, 256),
      tempId: truncateOptionalString(attempt.tempId, 256),
      accountId: truncateOptionalString(attempt.accountId, 256),
      accountOwnerSystemAccountId: truncateOptionalString(attempt.accountOwnerSystemAccountId, 256),
      groupId: truncateOptionalString(attempt.groupId, 256),
      proxyUrl: truncateOptionalString(attempt.proxyUrl, 2048),
      providerCode: truncateOptionalString(attempt.providerCode, 256),
      model: truncateOptionalString(attempt.model, 512),
      upstreamModel: truncateOptionalString(attempt.upstreamModel, 512),
      pricingModel: truncateOptionalString(attempt.pricingModel, 512),
      modelMappingSource: truncateOptionalString(attempt.modelMappingSource, 128),
      upstreamMethod: truncateString(attempt.upstreamMethod, 64),
      upstreamUrl: truncateString(attempt.upstreamUrl, 4096),
      errorPhase: truncateOptionalString(attempt.errorPhase, 256),
      errorCode: truncateOptionalString(attempt.errorCode, 512),
      errorMessage: truncateOptionalString(attempt.errorMessage, 4096),
      startedAt: truncateString(attempt.startedAt, 128),
      endedAt: truncateOptionalString(attempt.endedAt, 128)
    }))
  const payloads = limitItems(input.payloads, capacityFallbackMaxPayloads)
    .map((payload) => {
      const boundedHeaders = boundHeaders(payload.headers, headerBudget)
      const bodyWasCaptured = payload.body !== undefined
      return {
        ...payload,
        id: truncateOptionalString(payload.id, 256),
        attemptTempId: truncateOptionalString(payload.attemptTempId, 256),
        contentType: truncateOptionalString(payload.contentType, 512),
        contentEncoding: undefined,
        headers: boundedHeaders.headers,
        body: undefined,
        rawBodySizeBytes: payload.rawBodySizeBytes ?? (Buffer.isBuffer(payload.body) ? payload.body.byteLength : undefined),
        captureStatus: bodyWasCaptured
          ? payload.bodySha256 ? 'hash_only' as const : 'dropped' as const
          : boundedHeaders.truncated && (payload.captureStatus === undefined || payload.captureStatus === 'complete')
            ? 'dropped' as const
            : payload.captureStatus,
        createdAt: truncateOptionalString(payload.createdAt, 128)
      }
    })
  return {
    ...input,
    id: truncateOptionalString(input.id, 256),
    traceId: truncateString(input.traceId, 256),
    systemAccountId: truncateOptionalString(input.systemAccountId, 256),
    apiKeyId: truncateOptionalString(input.apiKeyId, 256),
    groupId: truncateOptionalString(input.groupId, 256),
    accountId: truncateOptionalString(input.accountId, 256),
    providerCode: truncateOptionalString(input.providerCode, 256),
    method: truncateString(input.method, 64),
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
    sampleReason: truncateString(input.sampleReason, 1024),
    startedAt: truncateString(input.startedAt, 128),
    endedAt: truncateString(input.endedAt, 128),
    httpCompletedAt: truncateOptionalString(input.httpCompletedAt, 128),
    createdAt: truncateOptionalString(input.createdAt, 128),
    attempts,
    payloads,
    captureStatus: input.captureStatus === 'overflow' ? 'overflow' : 'dropped'
  }
}

function limitItems<T>(items: T[], maxItems: number): T[] {
  if (items.length <= maxItems) return items
  return [...items.slice(0, maxItems - 1), items[items.length - 1]].filter((item): item is T => item !== undefined)
}

function boundHeaders(
  headers: Record<string, string | string[]> | undefined,
  budget: { remainingBytes: number }
): { headers?: Record<string, string | string[]>; truncated: boolean } {
  if (!headers) return { truncated: false }
  const output: Record<string, string | string[]> = {}
  let retainedCount = 0
  let truncated = false
  for (const rawName in headers) {
    if (!Object.prototype.hasOwnProperty.call(headers, rawName)) continue
    if (retainedCount >= capacityFallbackMaxHeaderCount || budget.remainingBytes <= 0) {
      truncated = true
      break
    }
    const name = truncateString(rawName, 256)
    const rawValue = headers[rawName]
    const value = Array.isArray(rawValue)
      ? rawValue.slice(0, 8).map((item) => truncateString(item, 2048))
      : truncateString(rawValue, 2048)
    const entryBytes = Buffer.byteLength(JSON.stringify([name, value]), 'utf8')
    if (entryBytes > budget.remainingBytes) {
      truncated = true
      break
    }
    output[name] = value
    budget.remainingBytes -= entryBytes
    retainedCount += 1
    truncated ||= name !== rawName || (Array.isArray(rawValue) && rawValue.length > 8)
      || (Array.isArray(rawValue)
        ? rawValue.some((item, index) => value[index] !== item)
        : value !== rawValue)
  }
  return {
    headers: retainedCount > 0 ? output : undefined,
    truncated
  }
}

function truncateOptionalString(value: string | undefined, maxBytes: number): string | undefined {
  return typeof value === 'string' ? truncateString(value, maxBytes) : undefined
}

function truncateString(value: string, maxBytes: number): string {
  if (maxBytes <= 0 || value.length === 0) return ''
  const maxChars = Math.min(value.length, maxBytes)
  const candidate = value.slice(0, maxChars)
  if (Buffer.byteLength(candidate, 'utf8') <= maxBytes) return candidate
  let low = 0
  let high = maxChars
  while (low < high) {
    const middle = Math.ceil((low + high) / 2)
    if (Buffer.byteLength(value.slice(0, middle), 'utf8') <= maxBytes) {
      low = middle
    } else {
      high = middle - 1
    }
  }
  return value.slice(0, low)
}
