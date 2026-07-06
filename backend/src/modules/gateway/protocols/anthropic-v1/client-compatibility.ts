import { randomUUID } from 'node:crypto'
import type { Request } from 'express'

import type { ClientCompatibilityCapability } from '../../../../domain/types.js'

const gatewayClientProfileHeader = 'x-juhe-client-profile'
const anthropicClaudeCodeSessionIdProperty = '__juheAnthropicClaudeCodeSessionId'
export const anthropicClaudeCodeVersion = '2.1.201'
export const anthropicClaudeCodeUserAgent = `claude-cli/${anthropicClaudeCodeVersion} (external, sdk-cli)`
export const anthropicClaudeCodeBetaHeaders = [
  'claude-code-20250219',
  'interleaved-thinking-2025-05-14',
  'effort-2025-11-24'
] as const

interface AnthropicClientCompatibilityOptions {
  requestClientCompatibility?: ClientCompatibilityCapability
  targetPathAndQuery?: string
}

export function applyAnthropicClientCompatibilityHeaders(
  req: Request,
  headers: Headers,
  options: AnthropicClientCompatibilityOptions = {}
): void {
  if (!shouldApplyClaudeCodeMessagesCompatibility(req, options)) {
    return
  }
  const userAgent = headers.get('user-agent')
  if (!isClaudeCodeUserAgent(userAgent)) {
    headers.set('user-agent', anthropicClaudeCodeUserAgent)
  }
  headers.set('anthropic-beta', mergeAnthropicBetaHeader(headers.get('anthropic-beta'), anthropicClaudeCodeBetaHeaders))
  if (!headers.get('x-claude-code-session-id')) {
    headers.set('x-claude-code-session-id', anthropicClaudeCodeSessionIdForRequest(req))
  }
}

export function anthropicClaudeCodeSessionIdForRequest(req: Request): string {
  const existing = stringValue(requestHeader(req, 'x-claude-code-session-id'))
    ?? stringValue(requestHeader(req, 'x-client-request-id'))
    ?? stringValue(requestHeader(req, 'x-request-id'))
  if (existing) {
    return existing
  }
  const requestWithSession = req as Request & { [anthropicClaudeCodeSessionIdProperty]?: string }
  requestWithSession[anthropicClaudeCodeSessionIdProperty] ??= randomUUID()
  return requestWithSession[anthropicClaudeCodeSessionIdProperty]
}

export function anthropicClaudeCodePathAndQueryForRequest(
  req: Request,
  pathAndQuery = req.originalUrl || req.path || '',
  options: AnthropicClientCompatibilityOptions = {}
): string {
  if (!shouldApplyClaudeCodeMessagesCompatibility(req, {
    ...options,
    targetPathAndQuery: options.targetPathAndQuery ?? pathAndQuery
  })) {
    return pathAndQuery
  }
  return withQueryParamIfMissing(pathAndQuery, 'beta', 'true')
}

export function shouldApplyClaudeCodeMessagesCompatibility(
  req: Request,
  options: AnthropicClientCompatibilityOptions | ClientCompatibilityCapability = {}
): boolean {
  const normalizedOptions = normalizeAnthropicClientCompatibilityOptions(options)
  const originalMessagesRequest = isAnthropicMessagesPostRequest(req)
  const targetMessagesRequest = normalizedOptions.targetPathAndQuery
    ? isAnthropicMessagesPostRequest(req, normalizedOptions.targetPathAndQuery)
    : originalMessagesRequest
  if (!originalMessagesRequest && !targetMessagesRequest) {
    return false
  }
  return normalizedOptions.requestClientCompatibility === 'claude_code'
    || parseGatewayClientProfileHeader(requestHeader(req, gatewayClientProfileHeader)) === 'claude_code'
    || (originalMessagesRequest && isClaudeCodeAnthropicRequestSignature(req))
}

function normalizeAnthropicClientCompatibilityOptions(
  options: AnthropicClientCompatibilityOptions | ClientCompatibilityCapability
): AnthropicClientCompatibilityOptions {
  return typeof options === 'string'
    ? { requestClientCompatibility: options }
    : options
}

function mergeAnthropicBetaHeader(current: string | null | undefined, required: readonly string[]): string {
  const normalized = new Map<string, string>()
  for (const value of [current, ...required]) {
    for (const item of splitAnthropicBetaHeader(value)) {
      const key = item.toLowerCase()
      if (!normalized.has(key)) {
        normalized.set(key, item)
      }
    }
  }
  return [...normalized.values()].join(',')
}

function splitAnthropicBetaHeader(value: string | null | undefined): string[] {
  if (!value) return []
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

function isClaudeCodeUserAgent(value: string | null | undefined): boolean {
  if (!value) return false
  const normalized = value.toLowerCase()
  return normalized.startsWith('claude-cli/') || normalized.includes(' claude-cli/')
}

function isClaudeCodeAnthropicRequestSignature(req: Request): boolean {
  const signals = [
    isClaudeCodeUserAgent(requestHeader(req, 'user-agent')),
    hasClaudeCodeBetaHeader(req),
    hasClaudeCodeSessionHeader(req),
    hasAnthropicBetaQuery(req)
  ].filter(Boolean).length
  return signals >= 2
}

function hasClaudeCodeBetaHeader(req: Request): boolean {
  const betaHeader = requestHeader(req, 'anthropic-beta')?.toLowerCase()
  if (!betaHeader) return false
  return betaHeader
    .split(',')
    .map((item) => item.trim())
    .some((item) => item.startsWith('claude-code-'))
}

function hasClaudeCodeSessionHeader(req: Request): boolean {
  return Boolean(
    stringValue(requestHeader(req, 'x-claude-code-session-id'))
      || stringValue(requestHeader(req, 'x-claude-code-agent-id'))
  )
}

function hasAnthropicBetaQuery(req: Request): boolean {
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  if (!query) return false
  return new URLSearchParams(query.startsWith('?') ? query.slice(1) : query).get('beta') === 'true'
}

function parseGatewayClientProfileHeader(value: string | undefined): string | undefined {
  const normalized = typeof value === 'string' && value.trim()
    ? value.trim().toLowerCase().replace(/[-\s]+/g, '_')
    : undefined
  return normalized || undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function requestHeader(req: Request, name: string): string | undefined {
  if (typeof req.header === 'function') {
    return req.header(name)
  }
  const headers = (req as Request & { headers?: Record<string, string | string[] | undefined> }).headers
  const value = headers?.[name.toLowerCase()] ?? headers?.[name]
  if (Array.isArray(value)) return value.join(', ')
  return typeof value === 'string' ? value : undefined
}

function isAnthropicMessagesPostRequest(req: Request, pathAndQuery = req.originalUrl || req.path || ''): boolean {
  if (req.method.toUpperCase() !== 'POST') {
    return false
  }
  const { path } = splitPathAndQuery(pathAndQuery)
  const requestPath = path.startsWith('/') ? path : `/${path}`
  return requestPath.replace(/^\/v1(?=\/|$)/, '') === '/messages'
}

function withQueryParamIfMissing(pathAndQuery: string, name: string, value: string): string {
  const { path, query } = splitPathAndQuery(pathAndQuery || '/')
  const params = new URLSearchParams(query.startsWith('?') ? query.slice(1) : query)
  if (params.get(name) !== value) {
    params.set(name, value)
  }
  const serialized = params.toString()
  return serialized ? `${path}?${serialized}` : path
}

function splitPathAndQuery(pathAndQuery: string): { path: string; query: string } {
  const queryIndex = pathAndQuery.indexOf('?')
  if (queryIndex < 0) {
    return { path: pathAndQuery, query: '' }
  }
  return {
    path: pathAndQuery.slice(0, queryIndex),
    query: pathAndQuery.slice(queryIndex)
  }
}
