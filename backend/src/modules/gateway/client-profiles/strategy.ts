import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { getGatewayRequestBodyState, type GatewayRawBodyRequest } from '../request/body.js'
import { requestStream } from '../request/metadata.js'

export type OpenAIGatewayClientProfile = 'codex' | 'generic_openai'
export type OpenAIGatewayDownstreamProtocol = 'responses_sse' | 'chat_completions_sse' | 'json' | 'unknown_stream'
export type OpenAIGatewayUpstreamAdapter = 'openai_api_key' | 'openai_oauth_codex' | 'openai_mixed'

export interface OpenAIGatewayClientStrategyIdentity {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  endpoint: string
}

export interface OpenAIGatewayCodexTurnContext {
  turnId: string
  sessionId?: string
  threadId?: string
  rawBodyHash: string
  stateKey: string
}

export interface OpenAIGatewayClientStrategyContext {
  clientProfile: OpenAIGatewayClientProfile
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
  upstreamAdapter: OpenAIGatewayUpstreamAdapter
  codexTurn?: OpenAIGatewayCodexTurnContext
  allowCodexStreamClientRetry: boolean
  allowCodexTurnAccountAvoidance: boolean
}

interface CodexTurnMetadata {
  turnId?: string
  sessionId?: string
  threadId?: string
}

const gatewayClientStrategyFullHashMaxBytes = 256 * 1024
const gatewayClientStrategyHashSampleCount = 16
const gatewayClientStrategyHashSampleBytes = 4 * 1024
const gatewayClientStrategyBodyHashMaxDepth = 8
const gatewayClientStrategyBodyHashMaxObjectKeys = 80
const gatewayClientStrategyBodyHashMaxArrayItems = 120
const gatewayClientStrategyBodyHashMaxNodes = 5000
const gatewayClientStrategyBodyHashMaxStringChars = 16 * 1024
const gatewayClientStrategyBodyHashStringEdgeChars = 4 * 1024

export function resolveOpenAIGatewayClientStrategy(
  req: Request,
  identity: OpenAIGatewayClientStrategyIdentity
): OpenAIGatewayClientStrategyContext {
  const downstreamProtocol = resolveOpenAIGatewayDownstreamProtocol(req)
  const codexMetadata = parseCodexTurnMetadata(req.header('x-codex-turn-metadata'))
  const canUseCodexProfile = downstreamProtocol === 'responses_sse' && Boolean(codexMetadata?.turnId)
  const codexTurn = canUseCodexProfile && codexMetadata?.turnId
    ? buildCodexTurnContext(req, identity, codexMetadata)
    : undefined

  return {
    clientProfile: codexTurn ? 'codex' : 'generic_openai',
    downstreamProtocol,
    upstreamAdapter: 'openai_mixed',
    codexTurn,
    allowCodexStreamClientRetry: Boolean(codexTurn),
    allowCodexTurnAccountAvoidance: Boolean(codexTurn)
  }
}

export function resolveOpenAIGatewayDownstreamProtocol(req: Request): OpenAIGatewayDownstreamProtocol {
  const normalizedPath = normalizedOpenAIRequestPath(req)
  const acceptsEventStream = requestAcceptsEventStream(req)
  const streamRequested = requestStream(req) || acceptsEventStream
  if (req.method.toUpperCase() === 'POST' && normalizedPath === '/responses' && streamRequested) {
    return 'responses_sse'
  }
  if (req.method.toUpperCase() === 'POST' && normalizedPath === '/chat/completions' && streamRequested) {
    return 'chat_completions_sse'
  }
  if (streamRequested || acceptsEventStream) {
    return 'unknown_stream'
  }
  return 'json'
}

export function openAIGatewayClientStrategyAuditMetadata(
  strategy: OpenAIGatewayClientStrategyContext
): Record<string, unknown> {
  return {
    clientProfile: strategy.clientProfile,
    downstreamProtocol: strategy.downstreamProtocol,
    upstreamAdapter: strategy.upstreamAdapter,
    codexTurnIdPresent: Boolean(strategy.codexTurn?.turnId),
    codexSessionIdPresent: Boolean(strategy.codexTurn?.sessionId),
    codexThreadIdPresent: Boolean(strategy.codexTurn?.threadId),
    codexRawBodyHash: strategy.codexTurn?.rawBodyHash,
    codexTurnStateKey: strategy.codexTurn?.stateKey,
    allowCodexStreamClientRetry: strategy.allowCodexStreamClientRetry,
    allowCodexTurnAccountAvoidance: strategy.allowCodexTurnAccountAvoidance
  }
}

function buildCodexTurnContext(
  req: Request,
  identity: OpenAIGatewayClientStrategyIdentity,
  metadata: Required<Pick<CodexTurnMetadata, 'turnId'>> & CodexTurnMetadata
): OpenAIGatewayCodexTurnContext {
  const rawBodyHash = hashGatewayRequestBody(req)
  const keyPayload = {
    systemAccountId: identity.systemAccountId,
    apiKeyId: identity.apiKeyId ?? 'internal',
    endpoint: identity.endpoint,
    codexTurnId: metadata.turnId
  }
  return {
    turnId: metadata.turnId,
    sessionId: metadata.sessionId,
    threadId: metadata.threadId,
    rawBodyHash,
    stateKey: createHash('sha256').update(JSON.stringify(keyPayload)).digest('hex')
  }
}

function parseCodexTurnMetadata(value: string | undefined): (Required<Pick<CodexTurnMetadata, 'turnId'>> & CodexTurnMetadata) | undefined {
  const rawValue = stringValue(value)
  if (!rawValue) {
    return undefined
  }
  const parsed = parseJsonObject(rawValue)
  if (!parsed) {
    return undefined
  }
  const turnId = stringValue(parsed.turn_id)
  if (!turnId) {
    return undefined
  }
  return {
    turnId,
    sessionId: stringValue(parsed.session_id),
    threadId: stringValue(parsed.thread_id)
  }
}

function parseJsonObject(value: string | undefined): Record<string, unknown> | undefined {
  if (!value) {
    return undefined
  }
  try {
    const parsed = JSON.parse(value) as unknown
    return typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)
      ? parsed as Record<string, unknown>
      : undefined
  } catch {
    return undefined
  }
}

function hashGatewayRequestBody(req: Request): string {
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (rawBody) {
    return hashGatewayRawBody(rawBody)
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'deferred_large_json') {
    return createHash('sha256').update(`deferred_large_json:${bodyState.rawBodyBytes}`).digest('hex')
  }
  return hashBoundedJsonLikeValue(req.body ?? {})
}

function hashGatewayRawBody(rawBody: Buffer): string {
  if (rawBody.byteLength <= gatewayClientStrategyFullHashMaxBytes) {
    return createHash('sha256').update(rawBody).digest('hex')
  }
  const hash = createHash('sha256')
  hash.update(`sampled:${rawBody.byteLength}:`)
  const sampleBytes = Math.min(gatewayClientStrategyHashSampleBytes, rawBody.byteLength)
  const maxStart = Math.max(0, rawBody.byteLength - sampleBytes)
  for (let index = 0; index < gatewayClientStrategyHashSampleCount; index += 1) {
    const start = Math.floor((maxStart * index) / Math.max(1, gatewayClientStrategyHashSampleCount - 1))
    hash.update(rawBody.subarray(start, start + sampleBytes))
  }
  return hash.digest('hex')
}

function hashBoundedJsonLikeValue(value: unknown): string {
  const hash = createHash('sha256')
  const context = {
    nodes: 0,
    truncated: false,
    seen: new WeakSet<object>()
  }
  updateHashWithJsonLikeValue(hash, value, context, 0)
  if (context.truncated) {
    hash.update('|truncated')
  }
  return hash.digest('hex')
}

function updateHashWithJsonLikeValue(
  hash: ReturnType<typeof createHash>,
  value: unknown,
  context: { nodes: number; truncated: boolean; seen: WeakSet<object> },
  depth: number
): void {
  if (context.nodes >= gatewayClientStrategyBodyHashMaxNodes || depth > gatewayClientStrategyBodyHashMaxDepth) {
    context.truncated = true
    hash.update('|limit')
    return
  }
  context.nodes += 1

  if (value === null || value === undefined) {
    hash.update(String(value))
    return
  }
  const valueType = typeof value
  if (valueType === 'string') {
    updateHashWithBoundedString(hash, value as string)
    return
  }
  if (valueType === 'number' || valueType === 'boolean' || valueType === 'bigint') {
    hash.update(`${valueType}:${String(value)}`)
    return
  }
  if (Buffer.isBuffer(value)) {
    hash.update(`buffer:${value.byteLength}:`)
    hash.update(value.subarray(0, Math.min(value.byteLength, gatewayClientStrategyHashSampleBytes)))
    return
  }
  if (value instanceof Date) {
    hash.update(`date:${value.toISOString()}`)
    return
  }
  if (typeof value !== 'object') {
    hash.update(`${valueType}:${String(value)}`)
    return
  }
  if (context.seen.has(value)) {
    hash.update('|circular')
    return
  }
  context.seen.add(value)

  if (Array.isArray(value)) {
    hash.update(`array:${value.length}:`)
    const length = Math.min(value.length, gatewayClientStrategyBodyHashMaxArrayItems)
    for (let index = 0; index < length; index += 1) {
      hash.update(`[${index}]`)
      updateHashWithJsonLikeValue(hash, value[index], context, depth + 1)
    }
    if (value.length > length) {
      context.truncated = true
      hash.update(`|array_truncated:${value.length - length}`)
    }
    return
  }

  hash.update('object:{')
  let visited = 0
  const record = value as Record<string, unknown>
  for (const key in record) {
    if (!Object.prototype.hasOwnProperty.call(record, key)) continue
    if (visited >= gatewayClientStrategyBodyHashMaxObjectKeys) {
      context.truncated = true
      hash.update('|object_truncated')
      break
    }
    updateHashWithBoundedString(hash, key)
    hash.update(':')
    updateHashWithJsonLikeValue(hash, record[key], context, depth + 1)
    visited += 1
  }
  hash.update('}')
}

function updateHashWithBoundedString(hash: ReturnType<typeof createHash>, value: string): void {
  hash.update(`string:${value.length}:`)
  if (value.length <= gatewayClientStrategyBodyHashMaxStringChars) {
    hash.update(value)
    return
  }
  hash.update(value.slice(0, gatewayClientStrategyBodyHashStringEdgeChars))
  hash.update('|...')
  hash.update(value.slice(Math.max(0, value.length - gatewayClientStrategyBodyHashStringEdgeChars)))
}

function normalizedOpenAIRequestPath(req: Request): string {
  const rawPath = (req.originalUrl || req.path || '').split('?', 1)[0] || '/'
  const path = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  return path.replace(/^\/v1(?=\/|$)/, '') || '/'
}

function requestAcceptsEventStream(req: Request): boolean {
  const accept = req.header('accept')
  return typeof accept === 'string' && accept.toLowerCase().includes('text/event-stream')
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
