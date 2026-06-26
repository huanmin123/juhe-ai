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
  endpoint: 'POST /v1beta/models/gemini-3.5-flash:streamGenerateContent'
}

const geminiIdentity = {
  ...baseIdentity,
  providerCode: 'gemini',
  providerProtocolProfileId: 'profile_gemini_native_v1beta',
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
}

const openAIIdentity = {
  ...baseIdentity,
  endpoint: 'POST /v1/responses',
  providerCode: 'gpt',
  providerProtocolProfileId: 'profile_gpt_openai_v1',
  protocolCode: 'openai',
  protocolVersion: 'v1'
}

const anthropicIdentity = {
  ...baseIdentity,
  endpoint: 'POST /v1/messages',
  providerCode: 'anthropic',
  providerProtocolProfileId: 'profile_anthropic_anthropic_v1',
  protocolCode: 'anthropic',
  protocolVersion: 'v1'
}

function main(): void {
  testExplicitGeminiCliHeaderUsesGeminiProfile()
  testGeminiCliUserAgentSignatureUsesGeminiProfile()
  testCloudCodeProxyClientSignatureUsesGeminiProfile()
  testGenericGeminiWithoutCliSignals()
  testSdkHeadersDoNotBecomeGeminiCli()
  testGeminiCliHeaderDoesNotAffectOpenAIProtocol()
  testGeminiCliHeaderDoesNotAffectAnthropicProtocol()
  testGeminiStreamGenerateContentPathIsStream()
  console.log('Gemini CLI 客户端画像回归通过：显式 header、真实 CLI User-Agent、Cloud Code 代理、通用 Gemini 隔离、SDK header 不误判、跨协议不污染均符合预期')
}

function testExplicitGeminiCliHeaderUsesGeminiProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }, {
    [gatewayClientProfileHeader]: 'gemini-cli'
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'gemini_cli')
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.clientProfileSource, 'explicit_header')
  assert.equal(strategy.downstreamProtocol, 'gemini_stream_generate_content_sse')
  assert.equal(strategy.upstreamAdapter, 'gemini_api_key')
  assert.equal(strategy.allowCodexStreamClientRetry, false)
  assert.equal(strategy.allowCodexTurnAccountAvoidance, false)
}

function testGeminiCliUserAgentSignatureUsesGeminiProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1beta/models/gemini-3.5-flash:generateContent', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }, {
    'user-agent': 'GeminiCLI/0.12.0/gemini-3.5-flash (win32; x64)',
    'x-goog-api-key': 'sk-downstream-key'
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'gemini_cli')
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.clientProfileSource, 'gemini_cli_request_signature')
  assert.equal(strategy.downstreamProtocol, 'json')
  assert.equal(strategy.upstreamAdapter, 'gemini_api_key')
}

function testCloudCodeProxyClientSignatureUsesGeminiProfile(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }, {
    'user-agent': 'CloudCodeVSCode/1.2.3 proxy_client=geminicli',
    authorization: 'Bearer downstream-token'
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'gemini_cli')
  assert.equal(strategy.clientProfileSource, 'gemini_cli_request_signature')
  assert.equal(strategy.downstreamProtocol, 'gemini_stream_generate_content_sse')
}

function testGenericGeminiWithoutCliSignals(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1beta/models/gemini-3.5-flash:generateContent', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }, {
    'x-goog-api-key': 'sk-downstream-key'
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'generic_gemini')
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.clientProfileSource, 'default')
  assert.equal(strategy.downstreamProtocol, 'json')
  assert.equal(strategy.upstreamAdapter, 'gemini_api_key')
}

function testSdkHeadersDoNotBecomeGeminiCli(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }, {
    'user-agent': 'google-genai-sdk/1.2.3 gl-node/22',
    'x-goog-api-client': 'google-genai-sdk/1.2.3 gl-node/22',
    'x-goog-api-key': 'sk-downstream-key'
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'generic_gemini')
  assert.equal(strategy.clientProfileSource, 'default')
  assert.equal(strategy.downstreamProtocol, 'gemini_stream_generate_content_sse')
}

function testGeminiCliHeaderDoesNotAffectOpenAIProtocol(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/responses', {
    model: 'gpt-5.5',
    input: 'hello',
    stream: true
  }, {
    [gatewayClientProfileHeader]: 'gemini_cli'
  }), openAIIdentity)

  assert.equal(strategy.clientProfile, 'generic_openai')
  assert.equal(strategy.requestClientCompatibility, 'openai_standard')
  assert.equal(strategy.downstreamProtocol, 'responses_sse')
  assert.equal(strategy.upstreamAdapter, 'openai_mixed')
}

function testGeminiCliHeaderDoesNotAffectAnthropicProtocol(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/v1/messages', {
    model: 'claude-haiku-4-5',
    stream: true
  }, {
    [gatewayClientProfileHeader]: 'gemini_cli'
  }), anthropicIdentity)

  assert.equal(strategy.clientProfile, 'generic_anthropic')
  assert.equal(strategy.requestClientCompatibility, 'anthropic_native')
  assert.equal(strategy.downstreamProtocol, 'messages_sse')
  assert.equal(strategy.upstreamAdapter, 'anthropic_api_key')
}

function testGeminiStreamGenerateContentPathIsStream(): void {
  const strategy = resolveOpenAIGatewayClientStrategy(createRequest('/models/gemini-3.5-flash:streamGenerateContent', {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }), geminiIdentity)

  assert.equal(strategy.clientProfile, 'generic_gemini')
  assert.equal(strategy.downstreamProtocol, 'gemini_stream_generate_content_sse')
}

function createRequest(path: string, body: Record<string, unknown>, headers: Record<string, string> = {}): Request {
  const normalizedHeaders = new Map(Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value]))
  return {
    method: 'POST',
    originalUrl: path,
    path: path.split('?', 1)[0],
    body,
    header(name: string) {
      return normalizedHeaders.get(name.toLowerCase())
    }
  } as unknown as Request
}

main()
