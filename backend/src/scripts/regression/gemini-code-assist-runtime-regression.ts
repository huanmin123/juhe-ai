import assert from 'node:assert/strict'
import type { Request } from 'express'

import {
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_PROVIDER_CODE,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION
} from '../../domain/provider-protocol.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { geminiProviderDriver } from '../../modules/providers/drivers/gemini/driver.js'
import {
  GEMINI_CLI_USER_AGENT,
  GEMINI_CODE_ASSIST_STREAM_URL,
  geminiOAuthRuntimeMode
} from '../../modules/providers/drivers/gemini/code-assist-runtime.js'
import type { GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

const codeAssistAccount = geminiAccount('code_assist')
const googleOneAccount = geminiAccount('google_one')

assert.equal(geminiOAuthRuntimeMode(codeAssistAccount), 'code_assist')
assert.equal(geminiOAuthRuntimeMode(googleOneAccount), 'google_one')
assert.equal(geminiOAuthRuntimeMode({
  ...codeAssistAccount,
  credentials: { ...codeAssistAccount.credentials, oauth_type: undefined }
}), 'code_assist', '有 project_id 的旧 Google OAuth 账户必须按 sub2api 兼容为 Code Assist')

const streamRequest = geminiRequest('/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent?alt=sse', {
  contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
})
assert.equal(geminiProviderDriver.accountSupportsRequest(streamRequest, codeAssistAccount), true)
assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(codeAssistAccount, streamRequest), [GEMINI_CODE_ASSIST_STREAM_URL])

const streamParts = await geminiProviderDriver.buildUpstreamRequestParts(streamRequest, codeAssistAccount, {
  systemAccountId: 'sys',
  groupId: 'grp'
})
assert.equal(streamParts.headers.get('authorization'), 'Bearer code-assist-access-token')
assert.equal(streamParts.headers.get('content-type'), 'application/json')
assert.equal(streamParts.headers.get('user-agent'), GEMINI_CLI_USER_AGENT)
assert.equal(streamParts.headers.get('x-goog-api-key'), null)
assert.deepEqual(JSON.parse(String(streamParts.body)), {
  model: 'gemini-3.1-pro-preview',
  project: 'cloud-ai-companion-project',
  request: {
    contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
  }
})

const countRequest = geminiRequest('/v1beta/models/gemini-3.1-pro-preview:countTokens', {
  contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
})
assert.equal(geminiProviderDriver.accountSupportsRequest(countRequest, codeAssistAccount), false)
assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(codeAssistAccount, countRequest), [])
await assert.rejects(
  geminiProviderDriver.buildUpstreamRequestParts(countRequest, codeAssistAccount, { systemAccountId: 'sys', groupId: 'grp' }),
  /仅支持 generateContent 与 streamGenerateContent/
)

const interactionsRequest = geminiRequest('/v1beta/interactions', { model: 'gemini-3.1-pro-preview' })
assert.equal(geminiProviderDriver.accountSupportsRequest(interactionsRequest, googleOneAccount), false)
assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(googleOneAccount, interactionsRequest), [])

await assert.rejects(
  geminiProviderDriver.buildUpstreamRequestParts(streamRequest, {
    ...googleOneAccount,
    credentials: { ...googleOneAccount.credentials, project_id: undefined }
  }, { systemAccountId: 'sys', groupId: 'grp' }),
  /缺少 project_id/
)

const transformedStream = geminiProviderDriver.transformUpstreamResponse!(
  streamRequest,
  codeAssistAccount,
  upstreamResponse([
    'data: {"response":{"candidates":[{"content":{"parts":[{"text":"hel"}]}}]}}\n',
    '\n',
    'data: {"response":{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}}\n\n',
    'data: [DONE]\n\n'
  ])
)
assert.equal(transformedStream.headers.get('content-type'), 'text/event-stream; charset=utf-8')
assert.equal(await bodyText(transformedStream), [
  'data: {"candidates":[{"content":{"parts":[{"text":"hel"}]}}]}',
  '',
  'data: {"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}',
  '',
  'data: [DONE]',
  '',
  ''
].join('\n'))

const nonStreamRequest = geminiRequest('/v1beta/models/gemini-3.1-pro-preview:generateContent', {
  contents: [{ role: 'user', parts: [{ text: 'hello' }] }]
})
assert.deepEqual(geminiProviderDriver.buildUpstreamUrls(googleOneAccount, nonStreamRequest), [GEMINI_CODE_ASSIST_STREAM_URL])
const transformedJson = geminiProviderDriver.transformUpstreamResponse!(
  nonStreamRequest,
  googleOneAccount,
  upstreamResponse([
    'data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}\n\n',
    'data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}}\n\n'
  ])
)
assert.equal(transformedJson.headers.get('content-type'), 'application/json; charset=utf-8')
const aggregated = JSON.parse(await bodyText(transformedJson))
assert.equal(aggregated.candidates[0].content.parts[0].text, 'hello')
assert.equal(aggregated.candidates[0].finishReason, 'STOP')
assert.deepEqual(aggregated.usageMetadata, { promptTokenCount: 2, candidatesTokenCount: 2 })

const aiStudioAccount = geminiAccount('ai_studio')
assert.deepEqual(
  geminiProviderDriver.buildUpstreamUrls(aiStudioAccount, countRequest),
  ['https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-pro-preview:countTokens']
)
assert.equal(geminiProviderDriver.accountSupportsRequest(countRequest, aiStudioAccount), true)

console.log('Gemini Code Assist runtime 回归通过：OAuth 分流、Cloud Code 包裹、能力隔离、SSE 解包与非流聚合符合 sub2api 契约')

function geminiAccount(oauthType: 'ai_studio' | 'code_assist' | 'google_one'): DispatchAccountSecret {
  return {
    id: `acc_gemini_${oauthType}`,
    providerCode: GEMINI_PROVIDER_CODE,
    providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
    protocolCode: GEMINI_PROTOCOL_CODE,
    protocolVersion: GEMINI_PROTOCOL_VERSION,
    systemAccountId: 'sys',
    accountOwnerSystemAccountId: 'sys',
    groupOwnerSystemAccountId: 'sys',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: `Gemini ${oauthType}`,
    type: 'google_oauth',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: oauthType === 'ai_studio'
      ? ['generate_content_json', 'generate_content_sse', 'count_tokens', 'interactions_json', 'interactions_sse']
      : ['generate_content_json', 'generate_content_sse'],
    supportedModels: ['gemini-3.1-pro-preview'],
    healthCheckModel: 'gemini-3.1-pro-preview',
    healthCheckEndpointMode: 'generate_content_json',
    baseUrl: 'https://generativelanguage.googleapis.com',
    apiKey: oauthType === 'google_one' ? 'google-one-access-token' : 'code-assist-access-token',
    streamFailureCount: 0,
    credentials: {
      access_token: oauthType === 'google_one' ? 'google-one-access-token' : 'code-assist-access-token',
      oauth_type: oauthType,
      project_id: oauthType === 'ai_studio' ? undefined : 'cloud-ai-companion-project',
      tier_id: oauthType === 'google_one' ? 'google_one_free' : 'gcp_standard',
      supported_endpoint_modes: oauthType === 'ai_studio'
        ? ['generate_content_json', 'generate_content_sse', 'count_tokens', 'interactions_json', 'interactions_sse']
        : ['generate_content_json', 'generate_content_sse']
    }
  }
}

function geminiRequest(originalUrl: string, body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body), 'utf8')
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      accept: originalUrl.includes(':streamGenerateContent') ? 'text/event-stream' : 'application/json',
      authorization: 'Bearer downstream-key',
      'content-type': 'application/json'
    },
    body,
    rawBody
  } as unknown as Request
}

function upstreamResponse(chunks: string[]): GatewayUpstreamResponse {
  return {
    status: 200,
    ok: true,
    headers: new Headers({
      'content-type': 'text/event-stream',
      'content-length': '999'
    }),
    body: (async function * () {
      for (const chunk of chunks) yield Buffer.from(chunk, 'utf8')
    })()
  }
}

async function bodyText(response: GatewayUpstreamResponse): Promise<string> {
  assert(response.body)
  const chunks: Buffer[] = []
  for await (const chunk of response.body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}
