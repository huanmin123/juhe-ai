const auditIdentityRedactionMarker = '[redacted-session-identity]'
const auditIdentityBodyOmittedMarker = '[audit-json-body-omitted:identity-redaction-unavailable]'
const auditIdentityJsonParseMaxBytes = 256 * 1024
const auditIdentityRedactionMaxDepth = 32
const auditIdentityRedactionMaxNodes = 20_000

const auditIdentityJsonKeys = new Set([
  'session_id',
  'session-id',
  'sessionid',
  'conversation_id',
  'conversation-id',
  'conversationid',
  'thread_id',
  'thread-id',
  'threadid',
  'turn_id',
  'turn-id',
  'turnid',
  'agent_id',
  'agent-id',
  'agentid',
  'parent_agent_id',
  'parent-agent-id',
  'parentagentid',
  'parent_session_id',
  'parent-session-id',
  'parentsessionid',
  'parent_thread_id',
  'parent-thread-id',
  'parentthreadid',
  'forked_from_thread_id',
  'forked-from-thread-id',
  'previous_response_id',
  'previous-response-id',
  'previousresponseid',
  'previous_interaction_id',
  'previous-interaction-id',
  'previousinteractionid',
  'parent_response_id',
  'parent-response-id',
  'parentresponseid',
  'client_request_id',
  'client-request-id',
  'clientrequestid',
  'prompt_cache_key',
  'prompt-cache-key',
  'promptcachekey',
  'x-codex-turn-metadata'
])

interface AuditIdentityRedactionContext {
  depth: number
  nodes: number
  seen: WeakSet<object>
}

export function redactAuditSessionIdentityJson(value: unknown): unknown {
  return redactAuditSessionIdentityJsonValue(value, {
    depth: 0,
    nodes: 0,
    seen: new WeakSet<object>()
  })
}

export function redactAuditSessionIdentityRequestBody(input: {
  body?: unknown
  rawBody?: Buffer
  contentType?: string
  contentEncoding?: string
}): Buffer | string | undefined {
  return redactAuditSessionIdentityRequestBodyResult(input).body
}

export function redactAuditSessionIdentityRequestBodyResult(input: {
  body?: unknown
  rawBody?: Buffer
  contentType?: string
  contentEncoding?: string
}): { body: Buffer | string | undefined; omittedForSafety: boolean } {
  if (isAuditSessionIdentityEventStream(input.contentType)) {
    return redactAuditSessionIdentityEventStream(input.rawBody, input.contentEncoding)
  }
  if (!isAuditSessionIdentityJsonRequest(input.contentType, input.body)) {
    return { body: input.rawBody, omittedForSafety: false }
  }

  const parsedBody = usableStructuredBody(input.body)
    ?? parseBoundedAuditJsonBody(input.rawBody, input.contentEncoding)
  if (parsedBody === undefined) {
    return {
      body: encodeAuditBody(input.rawBody, JSON.stringify({
        _audit_body_omitted: auditIdentityBodyOmittedMarker
      })),
      omittedForSafety: true
    }
  }
  return {
    body: encodeAuditBody(
      input.rawBody,
      JSON.stringify(redactAuditSessionIdentityJson(parsedBody))
    ),
    omittedForSafety: false
  }
}

export function isAuditSessionIdentityStructuredPayload(contentType: string | undefined, body: unknown): boolean {
  return isAuditSessionIdentityJsonRequest(contentType, body)
    || isAuditSessionIdentityEventStream(contentType)
}

export function isAuditSessionIdentityJsonRequest(contentType: string | undefined, body: unknown): boolean {
  if (Array.isArray(body) || isAuditJsonObject(body)) return true
  const normalized = contentType?.split(';', 1)[0]?.trim().toLowerCase()
  return normalized === 'application/json' || Boolean(normalized?.endsWith('+json'))
}

function redactAuditSessionIdentityJsonValue(
  value: unknown,
  context: AuditIdentityRedactionContext,
  parentKey?: string
): unknown {
  context.nodes += 1
  if (context.nodes > auditIdentityRedactionMaxNodes || context.depth > auditIdentityRedactionMaxDepth) {
    return '[audit-value-omitted:redaction-limit]'
  }
  if (Array.isArray(value)) {
    if (context.seen.has(value)) return '[audit-value-omitted:cycle]'
    context.seen.add(value)
    context.depth += 1
    const output = value.map((item) => redactAuditSessionIdentityJsonValue(item, context, parentKey))
    context.depth -= 1
    return output
  }
  if (!isAuditJsonObject(value)) return value
  if (context.seen.has(value)) return '[audit-value-omitted:cycle]'
  context.seen.add(value)
  context.depth += 1
  const output: Record<string, unknown> = {}
  for (const [key, item] of Object.entries(value)) {
    const normalizedKey = normalizeAuditIdentityJsonKey(key)
    if (auditIdentityJsonKeys.has(normalizedKey) || normalizedKey === 'conversation') {
      output[key] = auditIdentityRedactionMarker
      continue
    }
    if (normalizedKey === 'user_id' && normalizeAuditIdentityJsonKey(parentKey ?? '') === 'metadata') {
      output[key] = redactClaudeMetadataUserId(item, context)
      continue
    }
    output[key] = redactAuditSessionIdentityJsonValue(item, context, normalizedKey)
  }
  context.depth -= 1
  return output
}

function redactClaudeMetadataUserId(
  value: unknown,
  context: AuditIdentityRedactionContext
): unknown {
  if (typeof value !== 'string') {
    return redactAuditSessionIdentityJsonValue(value, context, 'user_id')
  }
  try {
    const parsed = JSON.parse(value) as unknown
    if (!isAuditJsonObject(parsed) || !containsAuditSessionIdentityKey(parsed)) return value
    return JSON.stringify(redactAuditSessionIdentityJsonValue(parsed, context, 'user_id'))
  } catch {
    return value
  }
}

function containsAuditSessionIdentityKey(value: Record<string, unknown>): boolean {
  return Object.keys(value).some((key) => auditIdentityJsonKeys.has(normalizeAuditIdentityJsonKey(key)))
}

function normalizeAuditIdentityJsonKey(value: string): string {
  return value.trim().toLowerCase().replace(/[\s.]+/g, '_')
}

function usableStructuredBody(value: unknown): unknown | undefined {
  if (Array.isArray(value)) return value
  if (!isAuditJsonObject(value)) return undefined
  return Object.keys(value).length > 0 ? value : undefined
}

function parseBoundedAuditJsonBody(rawBody: Buffer | undefined, contentEncoding: string | undefined): unknown | undefined {
  if (!rawBody?.byteLength || rawBody.byteLength > auditIdentityJsonParseMaxBytes) return undefined
  if (contentEncoding?.trim() && contentEncoding.trim().toLowerCase() !== 'identity') return undefined
  try {
    return JSON.parse(rawBody.toString('utf8')) as unknown
  } catch {
    return undefined
  }
}

function redactAuditSessionIdentityEventStream(
  rawBody: Buffer | undefined,
  contentEncoding: string | undefined
): { body: Buffer | string | undefined; omittedForSafety: boolean } {
  if (!rawBody?.byteLength
    || rawBody.byteLength > auditIdentityJsonParseMaxBytes
    || (contentEncoding?.trim() && contentEncoding.trim().toLowerCase() !== 'identity')) {
    return {
      body: encodeAuditBody(rawBody, JSON.stringify({
        _audit_body_omitted: auditIdentityBodyOmittedMarker
      })),
      omittedForSafety: true
    }
  }
  const redacted = rawBody.toString('utf8').split(/(\r?\n)/).map((part) => {
    if (part === '\n' || part === '\r\n' || part === '') return part
    if (part.startsWith('event:') || /^retry:\s*\d+\s*$/.test(part)) return part
    if (part.startsWith('id:') || part.startsWith(':')) {
      return `${part.split(':', 1)[0]}: ${auditIdentityRedactionMarker}`
    }
    if (!part.startsWith('data:')) return auditIdentityRedactionMarker
    const data = part.slice(5).trimStart()
    if (data === '[DONE]') return part
    try {
      return `data: ${JSON.stringify(redactAuditSessionIdentityJson(JSON.parse(data) as unknown))}`
    } catch {
      return `data: ${auditIdentityRedactionMarker}`
    }
  }).join('')
  return { body: Buffer.from(redacted, 'utf8'), omittedForSafety: false }
}

function isAuditSessionIdentityEventStream(contentType: string | undefined): boolean {
  return contentType?.split(';', 1)[0]?.trim().toLowerCase() === 'text/event-stream'
}

function encodeAuditBody(rawBody: Buffer | undefined, value: string): Buffer | string {
  return rawBody ? Buffer.from(value, 'utf8') : value
}

function isAuditJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
