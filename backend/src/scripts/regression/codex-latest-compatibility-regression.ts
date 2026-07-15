import assert from 'node:assert/strict'
import type { Request } from 'express'

import { normalizeOpenAICodexClientHeaders } from '../../modules/gateway/adapters/gpt-codex/client-headers.js'
import { buildOpenAIOAuthCodexRequestParts } from '../../modules/gateway/adapters/gpt-codex/oauth-adapter.js'
import { buildOpenAIModelsResponse } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'

const solHeaders = new Headers({
  originator: 'codex_cli_rs',
  'user-agent': 'codex_cli_rs/0.125.0',
  version: '0.125.0',
  'openai-beta': 'responses=experimental'
})
normalizeOpenAICodexClientHeaders(solHeaders, 'gpt-5.6-sol')
assert.equal(solHeaders.get('user-agent'), 'codex_cli_rs/0.144.4')
assert.equal(solHeaders.get('version'), null)
assert.equal(solHeaders.get('openai-beta'), null)
assert.equal(solHeaders.get('x-openai-internal-codex-responses-lite'), 'true')

const standardHeaders = new Headers({ originator: 'codex_vscode' })
normalizeOpenAICodexClientHeaders(standardHeaders, 'gpt-5.5')
assert.equal(standardHeaders.get('user-agent'), 'codex_vscode/0.144.4')
assert.equal(standardHeaders.get('x-openai-internal-codex-responses-lite'), null)

const request = createRequest('/v1/responses', {
  model: 'gpt-5.6-sol',
  input: '只输出 OK',
  prompt_cache_key: 'codex-latest-session'
})
const oauthParts = await buildOpenAIOAuthCodexRequestParts(request, request.headers, {
  apiKey: 'oauth-access-token'
}, {
  systemAccountId: 'system-a',
  apiKeyId: 'key-a',
  groupId: 'group-a'
})
assert.equal(oauthParts.headers.get('user-agent'), 'codex_cli_rs/0.144.4')
assert.equal(typeof oauthParts.headers.get('session-id'), 'string')
assert.equal(typeof oauthParts.headers.get('thread-id'), 'string')
assert.equal(oauthParts.headers.get('session_id'), null)
assert.equal(oauthParts.headers.get('conversation_id'), null)
assert.equal(oauthParts.headers.get('x-openai-internal-codex-responses-lite'), 'true')

const modelsResponse = buildOpenAIModelsResponse([
  modelCatalogItem('gpt-5.6-sol'),
  modelCatalogItem('gpt-5.5')
], createRequest('/v1/models?client_version=0.125.0', undefined, 'GET'))
assert('models' in modelsResponse)
assert.deepEqual(modelsResponse.models.map((item) => item.slug), ['gpt-5.6-sol', 'gpt-5.5'])

console.log('Codex 最新兼容契约回归通过')

function createRequest(path: string, body?: unknown, method = 'POST'): Request {
  const headers: Record<string, string> = {}
  return {
    method,
    path: path.split('?', 1)[0],
    originalUrl: path,
    body,
    headers,
    header(name: string) {
      return headers[name.toLowerCase()]
    },
    get(name: string) {
      return headers[name.toLowerCase()]
    }
  } as unknown as Request
}

function modelCatalogItem(model: string): Parameters<typeof buildOpenAIModelsResponse>[0][number] {
  return {
    providerCode: 'gpt',
    model,
    mode: 'text',
    supportedApiProtocols: ['responses'],
    supportsPromptCaching: false,
    supportedServiceTiers: [],
    supportedReasoningEfforts: [],
    defaultReasoningEffort: null,
    codexSupportedReasoningLevels: [],
    supportsServiceTier: false,
    catalogVisible: true,
    source: 'built-in',
    scope: 'built_in',
    status: 'active'
  }
}
