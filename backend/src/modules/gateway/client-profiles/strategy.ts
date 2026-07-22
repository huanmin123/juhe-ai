import { createHash } from 'node:crypto'
import type { Request } from 'express'

import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import { gatewayProtocolResponseProtocolForRequest } from '../protocols/registry.js'
import { getGatewayRequestBodyState, type GatewayRawBodyRequest } from '../request/body.js'
import { requestStream } from '../request/metadata.js'
import { codexCompactionExpectedForRequest } from '../response/codex-compaction-contract.js'

export const gatewayClientProfileHeader = 'x-juhe-client-profile'

export type OpenAIGatewayClientProfile = 'codex' | 'generic_openai' | 'claude_code' | 'generic_anthropic' | 'gemini_cli' | 'generic_gemini'
export type OpenAIGatewayDownstreamProtocol = 'responses_sse' | 'chat_completions_sse' | 'messages_sse' | 'gemini_stream_generate_content_sse' | 'gemini_interactions_sse' | 'json' | 'unknown_stream'
export type OpenAIGatewayUpstreamAdapter = 'openai_api_key' | 'openai_oauth_codex' | 'openai_mixed' | 'anthropic_api_key' | 'gemini_api_key'
export type GatewayPreCommitFailureSignal = 'protocol_error_event' | 'http_error'
export type GatewayCommittedFailureSignal = 'protocol_error_event' | 'disconnect'

export interface GatewayClientRetryCoordination {
  preCommitFailureSignal: GatewayPreCommitFailureSignal
  committedFailureSignal: GatewayCommittedFailureSignal
}

export interface OpenAIGatewayClientStrategyIdentity {
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  endpoint: string
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
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
  requestClientCompatibility: ClientCompatibilityCapability
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
  upstreamAdapter: OpenAIGatewayUpstreamAdapter
  codexCompactionExpected: boolean
  codexTurn?: OpenAIGatewayCodexTurnContext
  clientProfileSource?: 'default' | 'explicit_header' | 'codex_turn_metadata' | 'claude_code_request_signature' | 'gemini_cli_request_signature'
  retryCoordination: GatewayClientRetryCoordination
  allowCodexTurnAccountAvoidance: boolean
}

export function gatewayClientAllowsUpstreamSemanticInterpretation(
  strategy: Pick<OpenAIGatewayClientStrategyContext, 'clientProfile'>
): boolean {
  return strategy.clientProfile === 'codex'
    || strategy.clientProfile === 'claude_code'
    || strategy.clientProfile === 'gemini_cli'
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
  const responseProtocol = gatewayProtocolResponseProtocolForRequest(req, identity)
  if (responseProtocol === 'anthropic_v1') {
    return resolveAnthropicGatewayClientStrategy(req)
  }
  if (responseProtocol === 'gemini_v1beta') {
    return resolveGeminiGatewayClientStrategy(req)
  }
  const downstreamProtocol = resolveOpenAIGatewayDownstreamProtocol(req)
  const codexCompactionExpected = codexCompactionExpectedForRequest(req)
  const codexMetadata = parseCodexTurnMetadata(req.header('x-codex-turn-metadata'))
  const canUseCodexProfile = Boolean(codexMetadata?.turnId)
    && (downstreamProtocol === 'responses_sse' || isOpenAICodexCompactPostRequest(req))
  const codexTurn = canUseCodexProfile && codexMetadata?.turnId
    ? buildCodexTurnContext(req, identity, codexMetadata)
    : undefined

  const explicitProfile = parseGatewayClientProfileHeader(req.header(gatewayClientProfileHeader))
  const clientProfile = codexTurn || explicitProfile === 'codex' ? 'codex' : 'generic_openai'
  return {
    clientProfile,
    requestClientCompatibility: codexTurn ? 'codex_responses' : 'openai_standard',
    downstreamProtocol,
    upstreamAdapter: 'openai_mixed',
    codexCompactionExpected: Boolean(codexTurn) && codexCompactionExpected,
    codexTurn,
    clientProfileSource: codexTurn ? 'codex_turn_metadata' : explicitProfile === 'codex' ? 'explicit_header' : 'default',
    retryCoordination: resolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
    allowCodexTurnAccountAvoidance: Boolean(codexTurn)
  }
}

export function resolveAnthropicGatewayClientStrategy(req: Request): OpenAIGatewayClientStrategyContext {
  const downstreamProtocol = resolveAnthropicGatewayDownstreamProtocol(req)
  const explicitProfile = parseGatewayClientProfileHeader(req.header(gatewayClientProfileHeader))
  const supportedAnthropicShape = downstreamProtocol !== 'unknown_stream'
  const explicitClaudeCode = explicitProfile === 'claude_code' && supportedAnthropicShape
  const signatureClaudeCode = !explicitClaudeCode && supportedAnthropicShape && isClaudeCodeAnthropicRequestSignature(req)
  const claudeCode = explicitClaudeCode || signatureClaudeCode
  const clientProfile = claudeCode ? 'claude_code' : 'generic_anthropic'
  return {
    clientProfile,
    requestClientCompatibility: claudeCode ? 'claude_code' : 'anthropic_native',
    downstreamProtocol,
    upstreamAdapter: 'anthropic_api_key',
    codexCompactionExpected: false,
    clientProfileSource: explicitClaudeCode ? 'explicit_header' : signatureClaudeCode ? 'claude_code_request_signature' : 'default',
    retryCoordination: resolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
    allowCodexTurnAccountAvoidance: false
  }
}

export function resolveGeminiGatewayClientStrategy(req: Request): OpenAIGatewayClientStrategyContext {
  const downstreamProtocol = resolveGeminiGatewayDownstreamProtocol(req)
  const explicitProfile = parseGatewayClientProfileHeader(req.header(gatewayClientProfileHeader))
  const supportedGeminiShape = downstreamProtocol !== 'unknown_stream'
  const explicitGeminiCli = explicitProfile === 'gemini_cli' && supportedGeminiShape
  const signatureGeminiCli = !explicitGeminiCli && supportedGeminiShape && isGeminiCliRequestSignature(req)
  const geminiCli = explicitGeminiCli || signatureGeminiCli
  const clientProfile = geminiCli ? 'gemini_cli' : 'generic_gemini'
  return {
    clientProfile,
    requestClientCompatibility: 'openai_standard',
    downstreamProtocol,
    upstreamAdapter: 'gemini_api_key',
    codexCompactionExpected: false,
    clientProfileSource: explicitGeminiCli ? 'explicit_header' : signatureGeminiCli ? 'gemini_cli_request_signature' : 'default',
    retryCoordination: resolveGatewayClientRetryCoordination(clientProfile, downstreamProtocol),
    allowCodexTurnAccountAvoidance: false
  }
}

export function resolveGatewayClientRetryCoordination(
  clientProfile: OpenAIGatewayClientProfile,
  downstreamProtocol: OpenAIGatewayDownstreamProtocol
): GatewayClientRetryCoordination {
  const protocolEventSupported = (
    (clientProfile === 'codex' && downstreamProtocol === 'responses_sse')
    || (clientProfile === 'claude_code' && downstreamProtocol === 'messages_sse')
    || (clientProfile === 'gemini_cli' && (downstreamProtocol === 'gemini_stream_generate_content_sse' || downstreamProtocol === 'gemini_interactions_sse'))
  )
  return protocolEventSupported
    ? {
        preCommitFailureSignal: 'protocol_error_event',
        committedFailureSignal: 'protocol_error_event'
      }
    : {
        preCommitFailureSignal: 'http_error',
        committedFailureSignal: 'disconnect'
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

export function resolveAnthropicGatewayDownstreamProtocol(req: Request): OpenAIGatewayDownstreamProtocol {
  const normalizedPath = normalizedAnthropicRequestPath(req)
  const acceptsEventStream = requestAcceptsEventStream(req)
  const streamRequested = requestStream(req) || acceptsEventStream
  if (req.method.toUpperCase() === 'POST' && normalizedPath === '/messages' && streamRequested) {
    return 'messages_sse'
  }
  if (streamRequested || acceptsEventStream) {
    return 'unknown_stream'
  }
  return 'json'
}

export function resolveGeminiGatewayDownstreamProtocol(req: Request): OpenAIGatewayDownstreamProtocol {
  const normalizedPath = normalizedGeminiRequestPath(req)
  const acceptsEventStream = requestAcceptsEventStream(req)
  const interactionStreamRequested = requestStream(req) || acceptsEventStream
  const streamRequested = interactionStreamRequested || geminiAltSseQuery(req)
  if (req.method.toUpperCase() === 'POST' && /^\/models\/[^/]+:streamgeneratecontent$/.test(normalizedPath)) {
    return 'gemini_stream_generate_content_sse'
  }
  if (req.method.toUpperCase() === 'POST' && /^\/models\/[^/]+:generatecontent$/.test(normalizedPath) && streamRequested) {
    return 'gemini_stream_generate_content_sse'
  }
  if (req.method.toUpperCase() === 'POST' && normalizedPath === '/interactions') {
    return interactionStreamRequested ? 'gemini_interactions_sse' : 'json'
  }
  if (req.method.toUpperCase() === 'GET' && /^\/interactions\/[^/]+$/.test(normalizedPath)) {
    return interactionStreamRequested ? 'gemini_interactions_sse' : 'json'
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
    requestClientCompatibility: strategy.requestClientCompatibility,
    clientProfileSource: strategy.clientProfileSource,
    downstreamProtocol: strategy.downstreamProtocol,
    upstreamAdapter: strategy.upstreamAdapter,
    codexCompactionExpected: strategy.codexCompactionExpected,
    codexTurnIdPresent: Boolean(strategy.codexTurn?.turnId),
    codexSessionIdPresent: Boolean(strategy.codexTurn?.sessionId),
    codexThreadIdPresent: Boolean(strategy.codexTurn?.threadId),
    codexRawBodyHash: strategy.codexTurn?.rawBodyHash,
    codexTurnStateKey: strategy.codexTurn?.stateKey,
    preCommitFailureSignal: strategy.retryCoordination.preCommitFailureSignal,
    committedFailureSignal: strategy.retryCoordination.committedFailureSignal,
    allowCodexTurnAccountAvoidance: strategy.allowCodexTurnAccountAvoidance
  }
}

function parseGatewayClientProfileHeader(value: string | undefined): OpenAIGatewayClientProfile | undefined {
  const normalized = stringValue(value)?.toLowerCase().replace(/[-\s]+/g, '_')
  if (normalized === 'codex') return 'codex'
  if (normalized === 'claude_code') return 'claude_code'
  if (normalized === 'gemini_cli') return 'gemini_cli'
  return undefined
}

function isClaudeCodeAnthropicRequestSignature(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST' || normalizedAnthropicRequestPath(req) !== '/messages') {
    return false
  }
  const signals = [
    hasClaudeCodeUserAgent(req),
    hasClaudeCodeBetaHeader(req),
    hasClaudeCodeSessionHeader(req),
    hasAnthropicBetaQuery(req)
  ].filter(Boolean).length
  return signals >= 2
}

function hasClaudeCodeUserAgent(req: Request): boolean {
  const userAgent = stringValue(req.header('user-agent'))?.toLowerCase()
  return Boolean(userAgent && (userAgent.startsWith('claude-cli/') || userAgent.includes(' claude-cli/')))
}

function hasClaudeCodeBetaHeader(req: Request): boolean {
  const betaHeader = stringValue(req.header('anthropic-beta'))?.toLowerCase()
  if (!betaHeader) {
    return false
  }
  return betaHeader
    .split(',')
    .map((item) => item.trim())
    .some((item) => item.startsWith('claude-code-'))
}

function hasClaudeCodeSessionHeader(req: Request): boolean {
  return Boolean(
    stringValue(req.header('x-claude-code-session-id'))
      || stringValue(req.header('x-claude-code-agent-id'))
  )
}

function hasAnthropicBetaQuery(req: Request): boolean {
  const originalUrl = req.originalUrl || req.path || ''
  const queryIndex = originalUrl.indexOf('?')
  if (queryIndex < 0) {
    return false
  }
  return new URLSearchParams(originalUrl.slice(queryIndex + 1)).get('beta') === 'true'
}

function isGeminiCliRequestSignature(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') {
    return false
  }
  if (!/^\/models\/[^/]+:(generatecontent|streamgeneratecontent)$/.test(normalizedGeminiRequestPath(req))) {
    return false
  }
  return hasGeminiCliUserAgent(req) && hasGeminiAuthSignal(req)
}

function hasGeminiCliUserAgent(req: Request): boolean {
  const userAgent = stringValue(req.header('user-agent'))
  if (!userAgent) return false
  return /\bGeminiCLI(?:[-/]|$)/i.test(userAgent) || /proxy_client=geminicli\b/i.test(userAgent)
}

function hasGeminiAuthSignal(req: Request): boolean {
  return Boolean(
    stringValue(req.header('x-goog-api-key'))
      || stringValue(req.header('x-api-key'))
      || stringValue(req.header('authorization'))
      || geminiKeyQuery(req)
  )
}

function geminiAltSseQuery(req: Request): boolean {
  return geminiQueryParam(req, 'alt')?.toLowerCase() === 'sse'
}

function geminiKeyQuery(req: Request): boolean {
  return Boolean(geminiQueryParam(req, 'key'))
}

function geminiQueryParam(req: Request, name: string): string | undefined {
  const originalUrl = req.originalUrl || req.path || ''
  const queryIndex = originalUrl.indexOf('?')
  if (queryIndex < 0) {
    return undefined
  }
  return stringValue(new URLSearchParams(originalUrl.slice(queryIndex + 1)).get(name))
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

function isOpenAICodexCompactPostRequest(req: Request): boolean {
  return req.method.toUpperCase() === 'POST'
    && normalizedOpenAIRequestPath(req) === '/responses/compact'
}

function normalizedAnthropicRequestPath(req: Request): string {
  const rawPath = (req.originalUrl || req.path || '').split('?', 1)[0] || '/'
  const path = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  return path.replace(/^\/v1(?=\/|$)/, '') || '/'
}

function normalizedGeminiRequestPath(req: Request): string {
  const rawPath = (req.originalUrl || req.path || '').split('?', 1)[0] || '/'
  const path = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  return path.replace(/^\/v1beta(?=\/|$)/i, '').toLowerCase() || '/'
}

function requestAcceptsEventStream(req: Request): boolean {
  const accept = req.header('accept')
  return typeof accept === 'string' && accept.toLowerCase().includes('text/event-stream')
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
