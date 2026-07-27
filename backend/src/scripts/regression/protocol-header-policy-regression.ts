import { strict as assert } from 'node:assert'

import {
  anthropicMessagesScopedHeaderNames,
  codexResponsesScopedHeaderNames,
  geminiGenerateContentScopedHeaderNames,
  isAnthropicMessagesScopedHeaderName,
  isCodexResponsesScopedHeaderName
} from '../../modules/gateway/upstream/header-policy.js'
import { prepareAnthropicMessagesChatBridgeHeaders } from '../../modules/providers/drivers/_shared/anthropic-openai-chat-bridge.js'
import { prepareCodexResponsesChatBridgeHeaders } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import { prepareGeminiGenerateContentChatBridgeHeaders } from '../../modules/providers/drivers/_shared/gemini-openai-chat-bridge.js'
import { prepareOpenAIToAnthropicBridgeHeaders } from '../../modules/providers/drivers/_shared/openai-anthropic-bridge.js'
import { prepareOpenAIOrAnthropicToGeminiNativeHeaders } from '../../modules/providers/drivers/_shared/openai-anthropic-gemini-native-bridge.js'

function main(): void {
  testCodexProtocolHeadersAreRemovedFromChatBridge()
  testCodexProtocolHeadersAreRemovedBeforeAnthropicCompatibility()
  testAnthropicProtocolHeadersAreRemovedFromChatBridge()
  testGeminiProtocolHeadersAreRemovedFromChatBridge()
  testSourceProtocolHeadersAreRemovedFromGeminiBridge()
  console.log('protocol header policy regression: passed')
}

function testCodexProtocolHeadersAreRemovedFromChatBridge(): void {
  const headers = codexBridgeFixture()
  prepareCodexResponsesChatBridgeHeaders(headers)
  assertCodexHeadersRemoved(headers)
  assert.equal(headers.get('accept'), 'text/event-stream')
  assert.equal(headers.get('content-type'), 'application/json')
}

function testCodexProtocolHeadersAreRemovedBeforeAnthropicCompatibility(): void {
  const headers = codexBridgeFixture()
  prepareOpenAIToAnthropicBridgeHeaders(headers, requestFixture('/v1/responses'))
  assertCodexHeadersRemoved(headers)
  assert.equal(headers.get('accept'), 'application/json')
  assert.equal(headers.get('content-type'), 'application/json')
}

function testAnthropicProtocolHeadersAreRemovedFromChatBridge(): void {
  const headers = new Headers({
    ...Object.fromEntries(anthropicMessagesScopedHeaderNames.map((name) => [name, `value:${name}`])),
    'x-claude-code-future-capability': 'future-value',
    'x-stainless-future-sdk-header': 'sdk-noise',
    'x-custom-header': 'preserved'
  })
  prepareAnthropicMessagesChatBridgeHeaders(headers, requestFixture('/v1/messages'))
  for (const name of anthropicMessagesScopedHeaderNames) {
    assert.equal(headers.get(name), null, `${name} must not cross the Anthropic-to-Chat bridge`)
  }
  assert.equal(headers.get('x-stainless-future-sdk-header'), null)
  assert.equal(headers.get('x-claude-code-future-capability'), null)
  assert.equal(headers.get('x-custom-header'), 'preserved')
  assert.equal(isAnthropicMessagesScopedHeaderName('X-Stainless-Future-Sdk-Header'), true)
}

function testGeminiProtocolHeadersAreRemovedFromChatBridge(): void {
  const headers = new Headers({
    ...Object.fromEntries(geminiGenerateContentScopedHeaderNames.map((name) => [name, `value:${name}`])),
    'x-gemini-future-capability': 'future-value',
    'x-goog-future-sdk-header': 'sdk-noise',
    'x-custom-header': 'preserved'
  })
  prepareGeminiGenerateContentChatBridgeHeaders(headers, requestFixture('/v1beta/models/gemini-2.5-pro:generateContent'))
  for (const name of geminiGenerateContentScopedHeaderNames) {
    assert.equal(headers.get(name), null, `${name} must not cross the Gemini-to-Chat bridge`)
  }
  assert.equal(headers.get('x-gemini-future-capability'), null)
  assert.equal(headers.get('x-goog-future-sdk-header'), null)
  assert.equal(headers.get('x-custom-header'), 'preserved')
}

function testSourceProtocolHeadersAreRemovedFromGeminiBridge(): void {
  const headers = new Headers({
    ...Object.fromEntries(codexResponsesScopedHeaderNames.map((name) => [name, `codex:${name}`])),
    ...Object.fromEntries(anthropicMessagesScopedHeaderNames.map((name) => [name, `anthropic:${name}`])),
    'x-codex-future-dynamic-header': 'future-value',
    'x-stainless-future-sdk-header': 'sdk-noise',
    'x-custom-header': 'preserved'
  })
  prepareOpenAIOrAnthropicToGeminiNativeHeaders(headers, requestFixture('/v1/responses'))
  assertCodexHeadersRemoved(headers)
  for (const name of anthropicMessagesScopedHeaderNames) assert.equal(headers.get(name), null)
  assert.equal(headers.get('x-stainless-future-sdk-header'), null)
  assert.equal(headers.get('x-custom-header'), 'preserved')
}

function codexBridgeFixture(): Headers {
  return new Headers({
    ...Object.fromEntries(codexResponsesScopedHeaderNames.map((name) => [name, `value:${name}`])),
    'x-codex-future-dynamic-header': 'future-value',
    'x-custom-header': 'preserved'
  })
}

function assertCodexHeadersRemoved(headers: Headers): void {
  for (const name of codexResponsesScopedHeaderNames) {
    assert.equal(headers.get(name), null, `${name} must not cross the Responses bridge`)
  }
  assert.equal(headers.get('x-codex-future-dynamic-header'), null, 'future x-codex-* headers must fail closed at protocol bridges')
  assert.equal(headers.get('x-custom-header'), 'preserved')
  assert.equal(isCodexResponsesScopedHeaderName('X-Codex-Future-Dynamic-Header'), true)
  assert.equal(isCodexResponsesScopedHeaderName('x-custom-header'), false)
}

function requestFixture(path: string): never {
  return {
    body: { stream: false },
    method: 'POST',
    originalUrl: path,
    path
  } as never
}

main()
