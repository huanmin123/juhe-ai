import { strict as assert } from 'node:assert'
import type { Request } from 'express'

import {
  gatewayClientProfileHeader,
  resolveOpenAIGatewayClientStrategy
} from '../../modules/gateway/client-profiles/strategy.js'

const baseIdentity = {
  systemAccountId: 'sys_a',
  apiKeyId: 'key_a',
  groupId: 'group_a',
  endpoint: 'POST /v1/messages'
}

const anthropicIdentity = {
  ...baseIdentity,
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
}

const openAIIdentity = {
  ...baseIdentity,
  endpoint: 'POST /v1/responses',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

function main(): void {
  testExplicitClaudeCodeHeaderUsesAnthropicProfile()
  testRealClaudeCodeSignatureUsesAnthropicProfile()
  testSingleClaudeCodeSignalDoesNotUpgrade()
  testGenericAnthropicWithoutExplicitHeader()
  testClaudeCodeHeaderDoesNotAffectOpenAIProtocol()
  testClaudeCodeProfileRequiresSupportedAnthropicProtocolShape()
  console.log('Claude Code 客户端画像回归通过：显式 header 命中、真实 CLI 多信号命中、普通 Anthropic 隔离、OpenAI 协议不误判、未知流形态不升级')
}

function testExplicitClaudeCodeHeaderUsesAnthropicProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/messages', {
    model: 'claude-haiku-4-5',
    stream: true
  }, {
    [gatewayClientProfileHeader]: 'claude_code'
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'claude_code')
  assert.equal(strategy.clientProfileSource, 'explicit_header')
  assert.equal(strategy.downstreamProtocol, 'messages_sse')
  assert.equal(strategy.upstreamAdapter, 'anthropic_api_key')
  assert.equal(strategy.allowCodexStreamClientRetry, false)
  assert.equal(strategy.allowCodexTurnAccountAvoidance, false)
}

function testRealClaudeCodeSignatureUsesAnthropicProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/messages?beta=true', {
    model: 'claude-haiku-4-5',
    stream: true
  }, {
    'user-agent': 'claude-cli/2.1.181 (external, sdk-cli)',
    'anthropic-beta': 'claude-code-20250219,interleaved-thinking-2025-05-14',
    'x-claude-code-session-id': 'session_123'
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'claude_code')
  assert.equal(strategy.clientProfileSource, 'claude_code_request_signature')
  assert.equal(strategy.downstreamProtocol, 'messages_sse')
  assert.equal(strategy.upstreamAdapter, 'anthropic_api_key')
}

function testSingleClaudeCodeSignalDoesNotUpgrade(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/messages', {
    model: 'claude-haiku-4-5',
    stream: true
  }, {
    'user-agent': 'claude-cli/2.1.181 (external, sdk-cli)'
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'generic_anthropic')
  assert.equal(strategy.clientProfileSource, 'default')
  assert.equal(strategy.downstreamProtocol, 'messages_sse')
}

function testGenericAnthropicWithoutExplicitHeader(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/messages', {
    model: 'claude-haiku-4-5',
    stream: false
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'generic_anthropic')
  assert.equal(strategy.clientProfileSource, 'default')
  assert.equal(strategy.downstreamProtocol, 'json')
  assert.equal(strategy.upstreamAdapter, 'anthropic_api_key')
}

function testClaudeCodeHeaderDoesNotAffectOpenAIProtocol(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.5-codex',
    input: 'hello',
    stream: true
  }, {
    [gatewayClientProfileHeader]: 'claude_code'
  }), openAIIdentity)

  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.downstreamProtocol, 'responses_sse')
  assert.equal(strategy.upstreamAdapter, 'openai_mixed')
}

function testClaudeCodeProfileRequiresSupportedAnthropicProtocolShape(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/unknown', {
    model: 'claude-haiku-4-5',
    stream: true
  }, {
    [gatewayClientProfileHeader]: 'claude-code'
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'generic_anthropic')
  assert.equal(strategy.downstreamProtocol, 'unknown_stream')
  assert.equal(strategy.clientProfileSource, 'default')
}

function createRequest(path: string, body: Record<string, unknown>, headers: Record<string, string> = {}): Request {
  const normalizedHeaders = new Map(Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value]))
  return {
    method: 'POST',
    originalUrl: path,
    path,
    body,
    header(name: string) {
      return normalizedHeaders.get(name.toLowerCase())
    }
  } as unknown as Request
}

main()
