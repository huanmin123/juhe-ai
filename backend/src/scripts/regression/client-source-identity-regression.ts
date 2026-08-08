import assert from 'node:assert/strict'

import type { Request } from 'express'

import {
  resolveAnthropicGatewayClientStrategy,
  resolveGeminiGatewayClientStrategy,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyIdentity
} from '../../modules/gateway/client-profiles/strategy.js'

const identity: OpenAIGatewayClientStrategyIdentity = {
  systemAccountId: 'system_source_test',
  apiKeyId: 'api_source_test',
  groupId: 'group_source_test',
  endpoint: '/gateway',
  clientIp: '203.0.113.20'
}

const codex = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'shared-conversation',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_a' })
  }
}), identity)
assert.equal(codex.clientSource?.kind, 'official_session')
assert.ok(codex.clientSource?.sourceKey)
assert.ok(codex.codexTurn?.stateKey)

const codexDifferentApiKey = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'shared-conversation',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_a' })
  }
}), { ...identity, apiKeyId: 'api_source_other' })
assert.notEqual(codexDifferentApiKey.clientSource?.sourceKey, codex.clientSource?.sourceKey)

const claude = resolveAnthropicGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/messages?beta=true',
  headers: {
    accept: 'text/event-stream',
    'user-agent': 'claude-cli/2.0',
    'anthropic-beta': 'claude-code-20250219',
    'x-claude-code-session-id': 'shared-conversation'
  }
}), identity)
assert.equal(claude.clientSource?.kind, 'official_session')
assert.ok(claude.clientSource?.sourceKey)
assert.notEqual(claude.clientSource?.sourceKey, codex.clientSource?.sourceKey, 'profile namespaces must prevent cross-provider source collisions')

const geminiInteraction = resolveGeminiGatewayClientStrategy(request({
  method: 'GET',
  path: '/v1beta/interactions/interaction_123?stream=true',
  headers: { accept: 'text/event-stream' }
}), identity)
assert.equal(geminiInteraction.clientSource?.kind, 'protocol_resource')
assert.equal(geminiInteraction.clientSource?.semanticNamespace, 'google.gemini.interaction')
assert.ok(geminiInteraction.clientSource?.sourceKey)

const genericFallback = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/chat/completions',
  headers: {}
}), identity)
assert.equal(genericFallback.clientSource?.kind, 'ip_api_key_fallback')
assert.ok(genericFallback.clientSource?.sourceKey)

const differentIpFallback = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/chat/completions',
  headers: {}
}), { ...identity, clientIp: '203.0.113.21' })
assert.notEqual(differentIpFallback.clientSource?.sourceKey, genericFallback.clientSource?.sourceKey)

const explicitClaude = resolveAnthropicGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/messages',
  headers: {
    'x-juhe-client-profile': 'claude_code',
    'x-claude-code-session-id': 'untrusted-header-only'
  }
}), identity)
assert.equal(explicitClaude.clientSource?.kind, 'ip_api_key_fallback', 'self-declared profile must not promote a header into an official source')

const genericWithoutIp = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/chat/completions',
  headers: {}
}), { ...identity, clientIp: undefined })
assert.equal(genericWithoutIp.clientSource?.status, 'missing')
assert.equal(genericWithoutIp.clientSource?.sourceKey, undefined)

const invalidCodex = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'bad\u0000session',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_bad' })
  }
}), identity)
assert.equal(invalidCodex.clientSource?.status, 'invalid')
assert.equal(invalidCodex.clientSource?.sourceKey, undefined)
assert.equal(invalidCodex.codexTurn?.stateKey, undefined)

const conflictingCodex = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'session-a',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'turn_conflict' })
  },
  headersDistinct: { 'session-id': ['session-a', 'session-b'] }
}), identity)
assert.equal(conflictingCodex.clientSource?.status, 'conflict')
assert.equal(conflictingCodex.clientSource?.sourceKey, undefined)
assert.equal(conflictingCodex.codexTurn?.stateKey, undefined)

console.log('client-source-identity regression passed')

function request(input: {
  method: string
  path: string
  headers: Record<string, string>
  headersDistinct?: Record<string, string[]>
}): Request {
  const headers = Object.fromEntries(Object.entries(input.headers).map(([key, value]) => [key.toLowerCase(), value]))
  return {
    method: input.method,
    path: input.path.split('?', 1)[0],
    originalUrl: input.path,
    headers,
    headersDistinct: input.headersDistinct,
    header(name: string) {
      return headers[name.toLowerCase()]
    }
  } as Request
}
