import assert from 'node:assert/strict'
import { performance } from 'node:perf_hooks'

import type { Request } from 'express'

import {
  resolveAnthropicGatewayClientStrategy,
  resolveGeminiGatewayClientStrategy,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyIdentity
} from '../../modules/gateway/client-profiles/strategy.js'

const sourceCount = 12_000
const identity: OpenAIGatewayClientStrategyIdentity = {
  systemAccountId: 'system_source_saturation',
  apiKeyId: 'api_source_saturation',
  groupId: 'group_source_saturation',
  endpoint: '/gateway'
}

const startedAt = performance.now()
const codexSources = new Set<string>()
const codexStates = new Set<string>()
const claudeSources = new Set<string>()
const geminiSources = new Set<string>()
const fallbackSources = new Set<string>()

for (let index = 1; index <= sourceCount; index += 1) {
  const clientIp = sourceIp(index)
  const codex = resolveOpenAIGatewayClientStrategy(request({
    method: 'POST',
    path: '/v1/responses',
    headers: {
      accept: 'text/event-stream',
      'session-id': `codex-session-${index}`,
      'x-codex-turn-metadata': JSON.stringify({ turn_id: `turn-${index}` })
    }
  }), { ...identity, clientIp })
  assert.equal(codex.clientSource?.kind, 'official_session')
  assert.ok(codex.clientSource?.sourceKey)
  assert.ok(codex.codexTurn?.stateKey)
  codexSources.add(codex.clientSource.sourceKey)
  codexStates.add(codex.codexTurn.stateKey)

  const claude = resolveAnthropicGatewayClientStrategy(request({
    method: 'POST',
    path: '/v1/messages?beta=true',
    headers: {
      accept: 'text/event-stream',
      'user-agent': 'claude-cli/2.0',
      'anthropic-beta': 'claude-code-20250219',
      'x-claude-code-session-id': `claude-session-${index}`
    }
  }), { ...identity, clientIp })
  assert.equal(claude.clientSource?.kind, 'official_session')
  assert.ok(claude.clientSource?.sourceKey)
  claudeSources.add(claude.clientSource.sourceKey)

  const gemini = resolveGeminiGatewayClientStrategy(request({
    method: 'GET',
    path: `/v1beta/interactions/interaction-${index}?stream=true`,
    headers: { accept: 'text/event-stream' }
  }), { ...identity, clientIp })
  assert.equal(gemini.clientSource?.kind, 'protocol_resource')
  assert.ok(gemini.clientSource?.sourceKey)
  geminiSources.add(gemini.clientSource.sourceKey)

  const fallback = resolveOpenAIGatewayClientStrategy(request({
    method: 'POST',
    path: '/v1/chat/completions',
    headers: {}
  }), { ...identity, clientIp })
  assert.equal(fallback.clientSource?.kind, 'ip_api_key_fallback')
  assert.ok(fallback.clientSource?.sourceKey)
  fallbackSources.add(fallback.clientSource.sourceKey)
}

assert.equal(codexSources.size, sourceCount, 'Codex 官方会话在压力下不能串桶')
assert.equal(codexStates.size, sourceCount, 'Codex turn 子作用域在压力下不能串桶')
assert.equal(claudeSources.size, sourceCount, 'Claude Code 官方会话在压力下不能串桶')
assert.equal(geminiSources.size, sourceCount, 'Gemini Interaction 资源在压力下不能串桶')
assert.equal(fallbackSources.size, sourceCount, 'IP + API Key 软桶在压力下不能串桶')

const repeatedSourceKeys = new Set<string>()
const repeatedStateKeys = new Set<string>()
for (let index = 0; index < 4_096; index += 1) {
  const repeated = resolveOpenAIGatewayClientStrategy(request({
    method: 'POST',
    path: '/v1/responses',
    headers: {
      accept: 'text/event-stream',
      'session-id': 'stable-session',
      'x-codex-turn-metadata': JSON.stringify({ turn_id: 'stable-turn' })
    }
  }), { ...identity, clientIp: '203.0.113.10' })
  assert.ok(repeated.clientSource?.sourceKey)
  assert.ok(repeated.codexTurn?.stateKey)
  repeatedSourceKeys.add(repeated.clientSource.sourceKey)
  repeatedStateKeys.add(repeated.codexTurn.stateKey)
}
assert.equal(repeatedSourceKeys.size, 1, '同一来源的并发等价请求必须稳定复用来源键')
assert.equal(repeatedStateKeys.size, 1, '同一 turn 的并发等价请求必须稳定复用状态键')

const invalid = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'invalid\u0000session',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'invalid-turn' })
  }
}), { ...identity, clientIp: '203.0.113.11' })
assert.equal(invalid.clientSource?.status, 'invalid')
assert.equal(invalid.clientSource?.sourceKey, undefined)
assert.equal(invalid.codexTurn?.stateKey, undefined)

const conflict = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/responses',
  headers: {
    accept: 'text/event-stream',
    'session-id': 'session-a',
    'x-codex-turn-metadata': JSON.stringify({ turn_id: 'conflict-turn' })
  },
  headersDistinct: { 'session-id': ['session-a', 'session-b'] }
}), { ...identity, clientIp: '203.0.113.12' })
assert.equal(conflict.clientSource?.status, 'conflict')
assert.equal(conflict.clientSource?.sourceKey, undefined)
assert.equal(conflict.codexTurn?.stateKey, undefined)

const missingIp = resolveOpenAIGatewayClientStrategy(request({
  method: 'POST',
  path: '/v1/chat/completions',
  headers: {}
}), identity)
assert.equal(missingIp.clientSource?.status, 'missing')
assert.equal(missingIp.clientSource?.sourceKey, undefined)

const elapsedMs = performance.now() - startedAt
assert.ok(elapsedMs < 30_000, `来源身份饱和回归超时：${elapsedMs.toFixed(0)}ms`)
console.log(`client source identity saturation regression passed: sources=${sourceCount * 4}, repeated=4096, elapsedMs=${elapsedMs.toFixed(0)}`)

function sourceIp(index: number): string {
  return `198.51.${Math.floor(index / 255)}.${(index % 255) + 1}`
}

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
