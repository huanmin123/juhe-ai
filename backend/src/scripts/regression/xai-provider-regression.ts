import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  XAI_OPENAI_V1_PROFILE_ID,
  XAI_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'
import {
  DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS,
  DEFAULT_PROVIDER_SEEDS,
  XAI_OPENAI_V1_PROFILE_SEED,
  XAI_PROVIDER_SEED
} from '../../storage/schema-defaults.js'
import type { DispatchAccountSecret } from '../../storage/openai-account-selector.types.js'
import { normalizeAccountCredentialsForWrite } from '../../storage/account-credentials-normalization.js'
import { providerModelSupportsProtocolProfile } from '../../storage/account-model-normalization.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamRequestParts,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount,
  usageSemanticForProfile
} from '../../modules/providers/drivers/registry.js'
import { mergeAccountCredentialsForUpdate } from '../../modules/accounts/account-credential-update.js'
import {
  prepareXaiAccountBeforeDispatch,
  type XaiOAuthDispatchPreparationDependencies
} from '../../modules/providers/drivers/xai/oauth-dispatch-preparation.js'
import { applyGrokAccessDeniedFallback } from '../../modules/providers/drivers/xai/grok-access-denied-fallback.js'
import { UpstreamRequestTimeoutError, type GatewayUpstreamResponse } from '../../modules/gateway/upstream/request.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-xai-provider-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'xai-provider-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
assert.equal(XAI_PROVIDER_CODE, 'xai')
assert.equal(XAI_OPENAI_V1_PROFILE_ID, 'profile_xai_openai_v1')
assert(DEFAULT_PROVIDER_SEEDS.some((seed) => seed.code === XAI_PROVIDER_CODE), '默认供应商种子应包含 xAI')
assert(DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS.some((seed) => seed.id === XAI_OPENAI_V1_PROFILE_ID), '默认协议档案种子应包含 xAI OpenAI v1')
assert.deepEqual(XAI_PROVIDER_SEED.defaultSupportedModels, ['grok-4.5'], 'xAI 默认模型应使用资料完整的当前官方文本模型')
assert.deepEqual(XAI_OPENAI_V1_PROFILE_SEED.accountTypes, ['api_key', 'oauth'], 'xAI 官方 API 档案应允许 API Key 与 Grok OAuth')
assert.deepEqual(
  XAI_OPENAI_V1_PROFILE_SEED.endpointFamilies,
  ['chat_completions', 'responses'],
  'xAI OpenAI v1 档案应原生支持 Chat Completions 与 Responses'
)
assert.equal(XAI_OPENAI_V1_PROFILE_SEED.baseUrl, 'https://api.x.ai/v1')
assert.equal(
  providerModelSupportsProtocolProfile(['chat_completions', 'responses'], XAI_OPENAI_V1_PROFILE_SEED),
  true,
  'xAI 文本模型应进入 OpenAI v1 账户模型池'
)
assert.equal(
  providerModelSupportsProtocolProfile(['images'], XAI_OPENAI_V1_PROFILE_SEED),
  false,
  'xAI image-only 模型不得通过 OpenAI v1 文本档案的后端保存校验'
)

const normalizedCredentials = normalizeAccountCredentialsForWrite('api_key', {
  api_key: 'xai-upstream-key',
  base_url: XAI_OPENAI_V1_PROFILE_SEED.baseUrl
}, {
  accountType: 'api_key',
  providerCode: XAI_PROVIDER_CODE,
  providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION
})
assert.deepEqual(
  normalizedCredentials.supported_endpoint_modes,
  ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
  'xAI API Key 默认应启用 Chat 与 Responses 的 JSON/SSE 能力'
)

const normalizedOAuthCredentials = normalizeAccountCredentialsForWrite('oauth', {
  access_token: 'xai-oauth-access-token',
  refresh_token: 'xai-oauth-refresh-token',
  client_id: 'xai-oauth-client',
  token_type: 'Bearer',
  scope: 'openid offline_access grok-cli:access api:access',
  sub: 'xai-user',
  team_id: 'xai-team',
  base_url: 'https://cli-chat-proxy.grok.com/v1'
}, {
  accountType: 'oauth',
  providerCode: XAI_PROVIDER_CODE,
  providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION
})
assert.deepEqual(
  normalizedOAuthCredentials.supported_endpoint_modes,
  ['responses_json', 'responses_sse'],
  'Grok OAuth 默认只应启用 Responses JSON/SSE'
)
const mergedOAuthCredentials = mergeAccountCredentialsForUpdate({
  type: 'oauth',
  credentials: {
    ...normalizedOAuthCredentials,
    subscription_tier: 'supergrok',
    entitlement_status: 'active'
  }
} as unknown as Parameters<typeof mergeAccountCredentialsForUpdate>[0], {
  access_token: 'xai-oauth-access-token-updated',
  base_url: 'https://cli-chat-proxy.grok.com/v1'
})
assert.equal(mergedOAuthCredentials.token_type, 'Bearer', 'Grok OAuth 普通编辑必须保留 token_type')
assert.equal(mergedOAuthCredentials.scope, 'openid offline_access grok-cli:access api:access', 'Grok OAuth 普通编辑必须保留 scope')
assert.equal(mergedOAuthCredentials.sub, 'xai-user', 'Grok OAuth 普通编辑必须保留 sub')
assert.equal(mergedOAuthCredentials.team_id, 'xai-team', 'Grok OAuth 普通编辑必须保留 team_id')
assert.equal(mergedOAuthCredentials.subscription_tier, 'supergrok', 'Grok OAuth 普通编辑必须保留订阅层级')
assert.equal(mergedOAuthCredentials.entitlement_status, 'active', 'Grok OAuth 普通编辑必须保留 entitlement 状态')

const account = xaiAccount()
assert.equal(providerDriverForAccount(account)?.id, 'xai', 'xAI 档案应由独立 xAI driver 处理')
assert.equal(usageSemanticForProfile(account), 'openai', 'xAI 文本 usage 应复用 OpenAI 语义')

const chatRequest = openAIRequest('/v1/chat/completions?trace=xai-chat', {
  model: 'grok-4.5',
  messages: [{ role: 'user', content: 'hello xAI' }],
  stream: false
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(account, chatRequest),
  ['https://api.x.ai/v1/chat/completions?trace=xai-chat']
)
assert.equal(accountSupportsGatewayRequest(chatRequest, account), true)

const responsesRequest = openAIRequest('/v1/responses?trace=xai-responses', {
  model: 'grok-4.5',
  input: 'hello xAI responses',
  stream: true
})
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(account, responsesRequest),
  ['https://api.x.ai/v1/responses?trace=xai-responses']
)
assert.equal(accountSupportsGatewayRequest(responsesRequest, account), true)

const requestParts = await buildGatewayUpstreamRequestParts(responsesRequest, account, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_xai'
})
assert.equal(requestParts.headers.get('authorization'), 'Bearer xai-upstream-key', 'xAI API Key 应使用 Bearer Authorization')
assert.equal(requestParts.headers.get('x-api-key'), null, 'xAI 上游不应收到 Anthropic x-api-key')
assert.equal(JSON.parse(String(requestParts.body)).model, 'grok-4.5', 'xAI Responses 请求体应保留模型')

const anthropicRequest = openAIRequest('/v1/messages', {
  model: 'grok-4.5',
  messages: [{ role: 'user', content: 'not supported' }]
})
assert.deepEqual(buildGatewayUpstreamUrlsForAccount(account, anthropicRequest), [], 'xAI 档案不应承接 Anthropic Messages 原生请求')
assert.equal(accountSupportsGatewayRequest(anthropicRequest, account), false)

const oauthAccount: DispatchAccountSecret = {
  ...account,
  id: 'acc_xai_oauth',
  name: 'Grok OAuth',
  type: 'oauth',
  clientCompatibility: 'openai_standard',
  supportedEndpointModes: ['responses_json', 'responses_sse'],
  baseUrl: 'https://cli-chat-proxy.grok.com/v1',
  apiKey: 'xai-oauth-access-token',
  refreshToken: 'xai-oauth-refresh-token',
  credentials: normalizedOAuthCredentials
}
let releaseOAuthRefresh: (() => void) | undefined
const oauthRefreshGate = new Promise<void>((resolve) => { releaseOAuthRefresh = resolve })
let oauthRefreshCalls = 0
let persistedOAuthCredentials: Record<string, unknown> | undefined
const oauthRefreshDependencies: XaiOAuthDispatchPreparationDependencies = {
  async loadAccount() {
    return {
      providerCode: XAI_PROVIDER_CODE,
      type: 'oauth',
      configRevision: 5,
      credentials: {
        ...normalizedOAuthCredentials,
        expires_at: '2000-01-01T00:00:00.000Z',
        metadata_marker: 'preserved'
      }
    }
  },
  async refreshToken() {
    oauthRefreshCalls += 1
    await oauthRefreshGate
    return {
      accessToken: 'xai-oauth-access-token-refreshed',
      refreshToken: 'xai-oauth-refresh-token-rotated',
      clientId: 'xai-oauth-client',
      expiresAt: '2099-01-01T00:00:00.000Z',
      expiresIn: 21_600,
      tokenType: 'Bearer'
    }
  },
  async persistCredentials(_accountId, credentials, expectedConfigRevision) {
    assert.equal(expectedConfigRevision, 5)
    persistedOAuthCredentials = credentials
    return { providerCode: XAI_PROVIDER_CODE, type: 'oauth', configRevision: 6, credentials }
  }
}
const cancelledWaiter = new AbortController()
const firstOAuthRefresh = prepareXaiAccountBeforeDispatch({
  ...oauthAccount,
  id: 'acc_xai_oauth_refresh_shared',
  expiresAt: '2000-01-01T00:00:00.000Z',
  credentials: { ...oauthAccount.credentials, expires_at: '2000-01-01T00:00:00.000Z' }
}, cancelledWaiter.signal, oauthRefreshDependencies)
const secondOAuthRefresh = prepareXaiAccountBeforeDispatch({
  ...oauthAccount,
  id: 'acc_xai_oauth_refresh_shared',
  expiresAt: '2000-01-01T00:00:00.000Z',
  credentials: { ...oauthAccount.credentials, expires_at: '2000-01-01T00:00:00.000Z' }
}, undefined, oauthRefreshDependencies)
cancelledWaiter.abort()
await assert.rejects(firstOAuthRefresh, /请求已取消/u, '单个 Grok 请求取消不得中止共享 refresh token 轮换')
releaseOAuthRefresh?.()
const refreshedOAuthAccount = await secondOAuthRefresh
assert.equal(oauthRefreshCalls, 1, '同一 Grok 物理账户的并发刷新必须单飞')
assert.equal(refreshedOAuthAccount.apiKey, 'xai-oauth-access-token-refreshed')
assert.equal(refreshedOAuthAccount.refreshToken, 'xai-oauth-refresh-token-rotated')
assert.equal(persistedOAuthCredentials?.metadata_marker, 'preserved', 'Grok token rotation 不得丢弃扩展元数据')
let xaiRebaseLoadCount = 0
const xaiRebasePersistRevisions: number[] = []
const rebasedOAuthAccount = await prepareXaiAccountBeforeDispatch({
  ...oauthAccount,
  id: 'acc_xai_oauth_refresh_rebase',
  expiresAt: '2000-01-01T00:00:00.000Z',
  credentials: { ...oauthAccount.credentials, expires_at: '2000-01-01T00:00:00.000Z' }
}, undefined, {
  async loadAccount() {
    xaiRebaseLoadCount += 1
    return {
      providerCode: XAI_PROVIDER_CODE,
      type: 'oauth',
      configRevision: xaiRebaseLoadCount === 1 ? 20 : 21,
      credentials: {
        ...normalizedOAuthCredentials,
        expires_at: '2000-01-01T00:00:00.000Z',
        metadata_marker: xaiRebaseLoadCount === 1 ? 'before-edit' : 'after-edit'
      }
    }
  },
  async refreshToken() {
    return {
      accessToken: 'xai-rebased-access-token',
      refreshToken: 'xai-rebased-refresh-token',
      clientId: 'xai-oauth-client',
      expiresAt: '2099-01-01T00:00:00.000Z',
      expiresIn: 21_600,
      tokenType: 'Bearer'
    }
  },
  async persistCredentials(_accountId, credentials, expectedConfigRevision) {
    xaiRebasePersistRevisions.push(expectedConfigRevision)
    if (expectedConfigRevision === 20) {
      throw new repositories.AccountConfigRevisionConflictError('acc_xai_oauth_refresh_rebase', 20, 21)
    }
    return { providerCode: XAI_PROVIDER_CODE, type: 'oauth', configRevision: 22, credentials }
  }
})
assert.deepEqual(xaiRebasePersistRevisions, [20, 21], 'Grok 无关配置冲突后必须 rebase 并写回已轮换的 refresh token')
assert.equal(rebasedOAuthAccount.refreshToken, 'xai-rebased-refresh-token')
assert.equal(accountSupportsGatewayRequest(chatRequest, oauthAccount), false, 'Grok OAuth 不应承接 Chat Completions')
assert.equal(accountSupportsGatewayRequest(responsesRequest, oauthAccount), true, 'Grok OAuth 应承接 Responses')
assert.deepEqual(
  buildGatewayUpstreamUrlsForAccount(oauthAccount, responsesRequest),
  ['https://cli-chat-proxy.grok.com/v1/responses?trace=xai-responses']
)
const oauthRequestParts = await buildGatewayUpstreamRequestParts(responsesRequest, oauthAccount, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_xai'
})
assert.equal(oauthRequestParts.headers.get('authorization'), 'Bearer xai-oauth-access-token')
assert.equal(oauthRequestParts.headers.get('user-agent'), 'xai-grok-workspace/0.2.93')
assert.equal(oauthRequestParts.headers.get('x-xai-token-auth'), 'xai-grok-cli')
assert.equal(oauthRequestParts.headers.get('x-grok-client-version'), '0.2.93')
const customOAuthRequestParts = await buildGatewayUpstreamRequestParts(responsesRequest, {
  ...oauthAccount,
  baseUrl: 'https://api.x.ai/v1',
  credentials: { ...oauthAccount.credentials, base_url: 'https://api.x.ai/v1' }
}, {
  systemAccountId: 'sys_admin',
  groupId: 'grp_xai'
})
assert.equal(customOAuthRequestParts.headers.get('x-xai-token-auth'), null, '非 CLI proxy 上游不得携带 xAI CLI token 身份头')
assert.equal(customOAuthRequestParts.headers.get('x-grok-client-version'), null, '非 CLI proxy 上游不得携带 Grok CLI version')
assert.notEqual(customOAuthRequestParts.headers.get('user-agent'), 'xai-grok-workspace/0.2.93', 'api.x.ai 上游不得泄漏 Grok CLI User-Agent identity')

let fallbackRequests = 0
const fallbackResult = await applyGrokAccessDeniedFallback({
  upstreamUrl: 'https://cli-chat-proxy.grok.com/v1/responses?trace=fallback',
  headers: oauthRequestParts.headers,
  body: oauthRequestParts.body,
  response: gatewayResponse(403, '{"error":"Access denied"}'),
  async requestFallback(url, headers) {
    fallbackRequests += 1
    assert.equal(url, 'https://api.x.ai/v1/responses?trace=fallback')
    assert.equal(headers.get('authorization'), 'Bearer xai-oauth-access-token')
    assert.equal(headers.get('x-xai-token-auth'), null)
    assert.equal(headers.get('x-grok-client-version'), null)
    assert.equal(headers.get('user-agent'), null)
    return gatewayResponse(200, '{"id":"grok-fallback-success"}')
  }
})
assert.equal(fallbackResult.usedFallback, true, 'CLI proxy 的 Access denied 403 必须精确回退官方 API')
assert.equal(fallbackRequests, 1)
assert.equal(await bodyText(fallbackResult.response), '{"id":"grok-fallback-success"}')

const noFallbackResult = await applyGrokAccessDeniedFallback({
  upstreamUrl: 'https://cli-chat-proxy.grok.com/v1/responses',
  headers: oauthRequestParts.headers,
  body: oauthRequestParts.body,
  response: gatewayResponse(403, '{"error":"subscription required"}'),
  async requestFallback() {
    throw new Error('非 Access denied 403 不得回退')
  }
})
assert.equal(noFallbackResult.usedFallback, false)
assert.equal(await bodyText(noFallbackResult.response), '{"error":"subscription required"}', '不回退时必须完整保留原始 403 body')

const rejectedFallbackResult = await applyGrokAccessDeniedFallback({
  upstreamUrl: 'https://cli-chat-proxy.grok.com/v1/responses',
  headers: oauthRequestParts.headers,
  body: oauthRequestParts.body,
  response: gatewayResponse(403, '{"error":"Access denied"}'),
  async requestFallback() {
    return gatewayResponse(403, '{"error":"official endpoint rejected"}')
  }
})
assert.equal(rejectedFallbackResult.usedFallback, false, '官方端点非 2xx 时必须保留 CLI proxy 原响应')
assert.equal(rejectedFallbackResult.response.status, 403)
assert.equal(await bodyText(rejectedFallbackResult.response), '{"error":"Access denied"}')

let hangingBodyClosed = false
const hangingBody: AsyncIterable<Uint8Array> = {
  [Symbol.asyncIterator]() {
    return {
      next: async () => await new Promise<IteratorResult<Uint8Array>>(() => undefined),
      return: async () => {
        hangingBodyClosed = true
        return { done: true, value: undefined }
      }
    }
  }
}
await assert.rejects(applyGrokAccessDeniedFallback({
  upstreamUrl: 'https://cli-chat-proxy.grok.com/v1/responses',
  headers: oauthRequestParts.headers,
  body: oauthRequestParts.body,
  response: { ...gatewayResponse(403, ''), body: hangingBody },
  bodyInspectionTimeoutMs: 20,
  async requestFallback() {
    throw new Error('正文检查超时后不得触发 fallback')
  }
}), UpstreamRequestTimeoutError, 'Grok 403 正文不结束时必须受有界期限保护')
await new Promise<void>((resolve) => setImmediate(resolve))
assert.equal(hangingBodyClosed, true, 'Grok 403 正文检查超时后必须主动关闭上游迭代器')

let slowDripBodyClosed = false
let slowDripChunks = 0
const slowDripBody: AsyncIterable<Uint8Array> = {
  [Symbol.asyncIterator]() {
    return {
      next: async () => {
        await new Promise<void>((resolve) => setTimeout(resolve, 12))
        slowDripChunks += 1
        return { done: false, value: Buffer.from('x') }
      },
      return: async () => {
        slowDripBodyClosed = true
        return { done: true, value: undefined }
      }
    }
  }
}
await assert.rejects(applyGrokAccessDeniedFallback({
  upstreamUrl: 'https://cli-chat-proxy.grok.com/v1/responses',
  headers: oauthRequestParts.headers,
  body: oauthRequestParts.body,
  response: { ...gatewayResponse(403, ''), body: slowDripBody },
  bodyInspectionTimeoutMs: 30,
  async requestFallback() {
    throw new Error('慢滴正文超过总预算后不得触发 fallback')
  }
}), UpstreamRequestTimeoutError, 'Grok 403 慢滴正文必须共享总检查期限，不能按 chunk 重置计时')
await new Promise<void>((resolve) => setImmediate(resolve))
assert(slowDripChunks >= 2, '慢滴回归必须证明多个 chunk 均在单次 idle timeout 内到达')
assert.equal(slowDripBodyClosed, true, 'Grok 403 慢滴正文超时后必须主动关闭上游迭代器')

const xaiGroup = repositories.createGroup({
  providerCode: XAI_PROVIDER_CODE,
  name: 'xAI 文本模型写入回归分组'
}, access)
const xaiCreateInput = {
  providerCode: XAI_PROVIDER_CODE,
  providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
  name: 'xAI 文本模型写入回归账户',
  type: 'api_key',
  credentials: {
    api_key: 'xai-write-regression-key',
    base_url: 'https://api.x.ai/v1'
  },
  groupId: xaiGroup.id
}
assert.throws(() => repositories.createAccount({
  ...xaiCreateInput,
  name: 'xAI 图片模型创建拒绝回归账户',
  supportedModels: ['grok-imagine-image'],
  healthCheckModel: 'grok-imagine-image'
}, access), /账户支持模型不在供应商模型目录中：grok-imagine-image/, 'xAI 图片专用模型不得写入 Chat/Responses 文本账户')
const savedXaiAccount = repositories.createAccount({
  ...xaiCreateInput,
  supportedModels: ['grok-4.5'],
  healthCheckModel: 'grok-4.5'
}, access)
assert.throws(() => repositories.updateAccount(savedXaiAccount.id, {
  supportedModels: ['grok-imagine-image'],
  healthCheckModel: 'grok-imagine-image'
}, access), /账户支持模型不在供应商模型目录中：grok-imagine-image/, 'xAI 图片专用模型不得通过更新写入 Chat/Responses 文本账户')

console.log('xAI provider 回归通过：seed、API Key/Grok OAuth 凭据、路由、Bearer 请求构造和跨协议隔离符合预期')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function xaiAccount(): DispatchAccountSecret {
  return {
    id: 'acc_xai_api_key',
    providerCode: XAI_PROVIDER_CODE,
    providerProtocolProfileId: XAI_OPENAI_V1_PROFILE_ID,
    protocolCode: OPENAI_PROTOCOL_CODE,
    protocolVersion: OPENAI_PROTOCOL_VERSION,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'xAI API Key',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedEndpointModes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse'],
    supportedModels: ['grok-4.5'],
    healthCheckModel: 'grok-4.5',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.x.ai/v1',
    apiKey: 'xai-upstream-key',
    streamFailureCount: 0,
    credentials: {
      api_key: 'xai-upstream-key',
      base_url: 'https://api.x.ai/v1',
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    }
  }
}

function gatewayResponse(status: number, body: string): GatewayUpstreamResponse {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: new Headers({ 'content-type': 'application/json' }),
    body: (async function* () { yield Buffer.from(body) })()
  }
}

async function bodyText(response: GatewayUpstreamResponse): Promise<string> {
  if (!response.body) return ''
  const chunks: Buffer[] = []
  for await (const chunk of response.body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

function openAIRequest(originalUrl: string, body: Record<string, unknown>): Request {
  const rawBody = Buffer.from(JSON.stringify(body))
  return {
    method: 'POST',
    originalUrl,
    path: originalUrl.split('?', 1)[0],
    headers: {
      accept: body.stream === true ? 'text/event-stream' : 'application/json',
      authorization: 'Bearer downstream-key',
      'content-type': 'application/json'
    },
    body,
    rawBody
  } as unknown as Request
}
