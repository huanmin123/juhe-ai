import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  isGatewayScannedJsonBody,
  type GatewayRawBodyRequest
} from '../request/body.js'
import {
  extractGatewayJsonBodyMetadata,
  type GatewayJsonBodyMetadata
} from '../request/json-metadata-scanner.js'
import {
  gatewaySerializedJsonObject,
  serializeGatewayJsonObject
} from '../request/serialized-json-body.js'

type UpstreamBody = Buffer | string

type GatewayUpstreamPreparationRequest = GatewayRawBodyRequest & {
  gatewayAnthropicMessagesBodyCache?: {
    sourceBody: UpstreamBody
    preparedBody: UpstreamBody
  }
  gatewayPreparedBodyMetadataCache?: {
    sourceBody: UpstreamBody
    metadata: GatewayJsonBodyMetadata
  }
}

export function prepareAnthropicMessagesBodyForAttempt(
  req: Request,
  headers: Headers,
  upstreamUrl: string,
  body: UpstreamBody | Record<string, unknown> | undefined
): UpstreamBody | undefined {
  if (body === undefined) return undefined

  if (!Buffer.isBuffer(body) && typeof body !== 'string') {
    return serializeGatewayJsonObject(
      isAnthropicMessagesRequest(headers, upstreamUrl)
        ? normalizeAnthropicMessagesBody(body).body
        : body
    )
  }
  if (!isAnthropicMessagesRequest(headers, upstreamUrl)) {
    return body
  }

  const request = req as GatewayUpstreamPreparationRequest
  if (Buffer.isBuffer(body) && request.rawBody === body) {
    return body
  }
  const cached = request.gatewayAnthropicMessagesBodyCache
  if (cached?.sourceBody === body) {
    return cached.preparedBody
  }

  const parsed = gatewaySerializedJsonObject(body) ?? parseJsonBody(body)
  const preparedBody = prepareParsedAnthropicBody(body, parsed)
  request.gatewayAnthropicMessagesBodyCache = {
    sourceBody: body,
    preparedBody
  }
  return preparedBody
}

export function preparedUpstreamBodyMetadata(
  req: Request,
  body: UpstreamBody | undefined
): GatewayJsonBodyMetadata | undefined {
  if (body === undefined) return undefined

  const request = req as GatewayUpstreamPreparationRequest
  if (Buffer.isBuffer(body) && request.rawBody === body) {
    const state = getGatewayRequestBodyState(req)
    // Scanned bodies already received the complete top-level metadata scan in
    // body admission. Parsed/synthetic states may not include wire-specific
    // fields such as output_config.effort, so they keep the exact-body fallback.
    if (state && isGatewayScannedJsonBody(req)) {
      return {
        model: state.model,
        stream: state.stream,
        serviceTier: state.serviceTier,
        reasoningEffort: state.reasoningEffort,
        maxOutputTokens: state.maxOutputTokens,
        imageGeneration: state.imageGeneration,
        imageGenerationForced: state.imageGenerationForced,
        strictOutputRequirement: state.strictOutputRequirement,
        codexCompactionTrigger: state.codexCompactionTrigger
      }
    }
  }

  const cached = request.gatewayPreparedBodyMetadataCache
  if (cached?.sourceBody === body) {
    return cached.metadata
  }
  const bodyBuffer = Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
  const metadata = extractGatewayJsonBodyMetadata(bodyBuffer)
  request.gatewayPreparedBodyMetadataCache = {
    sourceBody: body,
    metadata
  }
  return metadata
}

function prepareParsedAnthropicBody(body: UpstreamBody, parsed: unknown): UpstreamBody {
  if (!isJsonRecord(parsed)) return body
  const normalized = normalizeAnthropicMessagesBody(parsed)
  if (!normalized.changed) return body
  return Buffer.isBuffer(body)
    ? serializeGatewayJsonObject(normalized.body)
    : JSON.stringify(normalized.body)
}

function parseJsonBody(body: UpstreamBody): unknown {
  try {
    return JSON.parse(Buffer.isBuffer(body) ? body.toString('utf8') : body) as unknown
  } catch {
    return undefined
  }
}

function normalizeAnthropicMessagesBody(
  body: Record<string, unknown>
): { body: Record<string, unknown>; changed: boolean } {
  let output = body
  const write = (key: string, value: unknown) => {
    if (output === body) output = { ...body }
    output[key] = value
  }
  if (body.stream === false) {
    if (output === body) output = { ...body }
    delete output.stream
  }
  if (Array.isArray(body.messages)) {
    let messages: unknown[] | undefined
    for (let index = 0; index < body.messages.length; index += 1) {
      const original = body.messages[index]
      const normalized = normalizeAnthropicMessage(original)
      if (normalized === original) continue
      messages ??= [...body.messages]
      messages[index] = normalized
    }
    if (messages) write('messages', messages)
  }
  return { body: output, changed: output !== body }
}

function normalizeAnthropicMessage(value: unknown): unknown {
  if (!isJsonRecord(value) || !Array.isArray(value.content)) return value
  if (!value.content.every(isPlainAnthropicTextBlock)) return value
  return {
    ...value,
    content: value.content.map((block) => block.text).join('')
  }
}

function isPlainAnthropicTextBlock(value: unknown): value is { type: 'text'; text: string } {
  return isJsonRecord(value)
    && value.type === 'text'
    && typeof value.text === 'string'
    && Object.keys(value).every((key) => key === 'type' || key === 'text')
}

function isAnthropicMessagesRequest(headers: Headers, upstreamUrl: string): boolean {
  return isAnthropicMessagesRequestHeaders(headers) && isAnthropicMessagesPath(upstreamUrl)
}

function isAnthropicMessagesRequestHeaders(headers: Headers): boolean {
  return Boolean(headers.get('anthropic-version'))
    && (
      Boolean(headers.get('x-api-key'))
      || Boolean(headers.get('anthropic-api-key'))
      || Boolean(headers.get('authorization'))
    )
}

function isAnthropicMessagesPath(upstreamUrl: string): boolean {
  try {
    return (new URL(upstreamUrl).pathname.replace(/^\/v1(?=\/|$)/, '') || '/') === '/messages'
  } catch {
    return false
  }
}

function isJsonRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
