import { createHash } from 'node:crypto'
import type { Request } from 'express'

import { getGatewayRequestBodyState, type GatewayRawBodyRequest } from './openai-gateway-request-body.js'
import { requestStream } from './openai-gateway-usage.js'

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
    groupId: identity.groupId,
    endpoint: identity.endpoint,
    codexTurnId: metadata.turnId,
    rawBodyHash
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
  const parsed = parseJsonObject(rawValue) ?? parseJsonObject(decodeURIComponentSafely(rawValue))
  if (!parsed) {
    return undefined
  }
  const turnId = stringValue(parsed.turn_id) ?? stringValue(parsed.turnId)
  if (!turnId) {
    return undefined
  }
  return {
    turnId,
    sessionId: stringValue(parsed.session_id) ?? stringValue(parsed.sessionId),
    threadId: stringValue(parsed.thread_id) ?? stringValue(parsed.threadId)
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

function decodeURIComponentSafely(value: string): string | undefined {
  try {
    return decodeURIComponent(value)
  } catch {
    return undefined
  }
}

function hashGatewayRequestBody(req: Request): string {
  const rawBody = (req as GatewayRawBodyRequest).rawBody
  if (rawBody) {
    return createHash('sha256').update(rawBody).digest('hex')
  }
  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'deferred_large_json') {
    return createHash('sha256').update(`deferred_large_json:${bodyState.rawBodyBytes}`).digest('hex')
  }
  return createHash('sha256').update(JSON.stringify(req.body ?? {})).digest('hex')
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
