import type { Request } from 'express'

import { gatewayClientProfileHeader } from '../client-profiles/strategy.js'
import { isAnthropicModelsRequest } from '../protocols/anthropic-v1/route-helpers.js'
import { isGeminiModelsRequest } from '../protocols/gemini-v1beta/route-helpers.js'
import { isOpenAIModelsRequest, splitPathAndQuery } from '../protocols/openai-v1/route-helpers.js'
import type { ResponseProtocolCode } from '../protocols/openai-v1/response-semantics.js'

export function resolveGatewayModelsResponseProtocol(req: Request): ResponseProtocolCode | undefined {
  if (isGeminiModelsRequest(req) && isExplicitGeminiModelsClient(req)) {
    return 'gemini_v1beta'
  }
  if (isAnthropicModelsRequest(req) && isExplicitAnthropicModelsClient(req)) {
    return 'anthropic_v1'
  }
  if (isOpenAIModelsRequest(req) || isAnthropicModelsRequest(req) || isGeminiModelsRequest(req)) {
    return 'openai_v1'
  }
  return undefined
}

function isExplicitGeminiModelsClient(req: Request): boolean {
  const { path, query } = splitPathAndQuery(req.originalUrl || req.path)
  if (path.toLowerCase() === '/v1beta/models') return true

  const profile = normalizedHeaderToken(req.header(gatewayClientProfileHeader))
  if (profile === 'gemini' || profile === 'generic_gemini' || profile === 'gemini_cli') {
    return true
  }
  if (lowerHeaderToken(req.header('x-goog-api-key'))) return true
  const key = new URLSearchParams(query.startsWith('?') ? query.slice(1) : query).get('key')
  if (typeof key === 'string' && key.trim()) return true

  const userAgent = lowerHeaderToken(req.header('user-agent'))
  return Boolean(userAgent && (/\bgeminicli(?:[-/]|$)/i.test(userAgent) || /proxy_client=geminicli\b/i.test(userAgent)))
}

function isExplicitAnthropicModelsClient(req: Request): boolean {
  const profile = normalizedHeaderToken(req.header(gatewayClientProfileHeader))
  if (profile === 'anthropic' || profile === 'generic_anthropic' || profile === 'claude_code') {
    return true
  }
  return Boolean(
    normalizedHeaderToken(req.header('anthropic-version'))
      || normalizedHeaderToken(req.header('anthropic-beta'))
      || normalizedHeaderToken(req.header('x-claude-code-session-id'))
      || normalizedHeaderToken(req.header('x-claude-code-agent-id'))
      || claudeCodeUserAgent(req)
  )
}

function claudeCodeUserAgent(req: Request): boolean {
  const userAgent = lowerHeaderToken(req.header('user-agent'))
  return Boolean(userAgent && (userAgent.startsWith('claude-cli/') || userAgent.includes(' claude-cli/')))
}

function normalizedHeaderToken(value: unknown): string | undefined {
  return lowerHeaderToken(value)?.replace(/[-\s]+/g, '_')
}

function lowerHeaderToken(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim()
    ? value.trim().toLowerCase()
    : undefined
}
