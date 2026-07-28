import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import type http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID,
  XAI_OPENAI_V1_PROFILE_ID
} from '../../domain/provider-protocol.js'
import {
  buildAnthropicOAuthCredentials,
  exchangeAnthropicAuthCode,
  generateAnthropicAuthURL,
  refreshAnthropicAuthToken
} from '../../modules/anthropic-oauth/anthropic-oauth.service.js'
import {
  buildGeminiOAuthCredentials,
  exchangeGeminiAuthCode,
  generateGeminiAuthURL,
  refreshGeminiAuthToken
} from '../../modules/gemini-oauth/gemini-oauth.service.js'
import {
  buildGrokOAuthCredentials,
  exchangeGrokAuthCode,
  generateGrokAuthURL,
  refreshGrokAuthToken
} from '../../modules/grok-oauth/grok-oauth.service.js'
import {
  buildOpenAIOAuthCredentials,
  exchangeOpenAIAuthCode,
  generateOpenAIAuthURL,
  refreshOpenAIOAuthToken
} from '../../modules/openai-oauth/openai-oauth.service.js'
import { runWithProviderOAuthTokenTransportForTest } from '../../modules/providers/drivers/_shared/provider-oauth-token-transport.js'
import {
  buildGatewayUpstreamRequestParts,
  buildGatewayUpstreamUrlsForAccount,
  prepareGatewayUpstreamAccount
} from '../../modules/providers/drivers/registry.js'
import { requestUpstream } from '../../modules/gateway/upstream/request.js'
import { runWithOpenAICodexBaseUrlForTest } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { logger } from '../../shared/logger.js'
import { startProviderOAuthMockUpstream } from './support/provider-oauth-mock-upstream.js'

const mock = await startProviderOAuthMockUpstream()

try {
  await runWithProviderOAuthTokenTransportForTest(mock.tokenTransport, async () => {
    await testOpenAI()
    await testAnthropic()
    await testGemini()
    await testGrok()
    await runWithOpenAICodexBaseUrlForTest(mock.inferenceBaseUrl('openai'), testSystemApiAccountBinding)
  })
  console.log('四供应商 OAuth 本地模拟上游 E2E 通过：authorize、owner/state、PKCE、换票重放、刷新轮换、凭据落库与首次上游请求均符合参考契约')
} finally {
  await mock.close()
}

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

interface AuthUrlResponse {
  authUrl: string
  sessionId: string
}

interface CreatedAccountResponse {
  id: string
  providerCode: string
}

async function testSystemApiAccountBinding(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-provider-oauth-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  mkdirSync(tempRoot, { recursive: true })
  runtimeConfig.databasePath = join(tempRoot, 'oauth-e2e.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.secret = 'provider-oauth-mock-upstream-e2e-secret'
  process.env.JUHE_AI_SECRET = runtimeConfig.secret
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.development.autoLoginUsername = 'admin'
  runtimeConfig.auth.captchaDisabled = true
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  logger.level = 'silent'

  const [{ createSystemApiApp }, repositories, databaseModule, sqliteReadWorkerPool] = await Promise.all([
    import('../../modules/system-api/system-api-app.js'),
    import('../../storage/repositories.js'),
    import('../../storage/database.js'),
    import('../../storage/sqlite-read-worker-pool.js')
  ])
  let server: http.Server | undefined
  try {
    assert(repositories.findSystemAccountByUsername('admin'), 'OAuth System API E2E 必须先初始化独立数据库和默认管理员')
    server = createSystemApiApp({
      systemApiPrefix: '/__aisys__/api',
      bypassSystemApiRateLimitForTest: true
    }).listen(0, '127.0.0.1')
    await waitForListen(server)
    const address = server.address()
    assert(address && typeof address === 'object')
    const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api`

    await bindAccount(baseUrl, 'openai', GPT_OPENAI_V1_PROFILE_ID, 'gpt')
    await bindAccount(baseUrl, 'anthropic', ANTHROPIC_ANTHROPIC_V1_PROFILE_ID, 'anthropic')
    await bindAccount(baseUrl, 'gemini', GEMINI_NATIVE_V1BETA_PROFILE_ID, 'gemini', {
      oauthType: 'ai_studio',
      clientId: 'gemini-route-e2e-client',
      clientSecret: 'gemini-route-e2e-secret'
    })
    await bindAccount(baseUrl, 'grok', XAI_OPENAI_V1_PROFILE_ID, 'xai')

    async function bindAccount(
      baseUrl: string,
      route: 'openai' | 'anthropic' | 'gemini' | 'grok',
      providerProtocolProfileId: string,
      expectedProviderCode: string,
      oauthFields: Record<string, unknown> = {}
    ): Promise<void> {
      const auth = await postJson<AuthUrlResponse>(`${baseUrl}/${route}-oauth/auth-url`, oauthFields)
      const authorization = await mock.authorize(route, auth.authUrl)
      const callbackUrl = route !== 'grok'
        ? `${authorization.code}#${authorization.state}`
        : authorization.callbackUrl
      const created = await postJson<CreatedAccountResponse>(`${baseUrl}/${route}-oauth/create-from-code`, {
        sessionId: auth.sessionId,
        callbackUrl,
        providerProtocolProfileId,
        name: `${route} OAuth mock E2E`,
        groupId: (await createGroupForProvider(repositories, expectedProviderCode)).id,
        accountExpiresAt: null,
        availabilitySchedule: null,
        temporaryUnavailableContinuousProbeEnabled: false,
        ...oauthFields
      })
      assert.equal(created.providerCode, expectedProviderCode, `${route} 绑定后 providerCode`)
      const persisted = repositories.findAccountForTest(created.id, { systemAccountId: 'sys_admin', role: 'admin' })
      assert(persisted, `${route} OAuth 账户必须已落库`)
      assert.equal(typeof persisted.credentials.access_token, 'string', `${route} OAuth 账户必须持久化 access_token`)
      assert.equal(typeof persisted.credentials.refresh_token, 'string', `${route} OAuth 账户必须持久化 refresh_token`)
      if (route === 'openai') assert.equal(persisted.credentials.account_id, 'chatgpt-account-mock')

      const groupId = persisted.boundGroupId
      assert(groupId, `${route} OAuth 账户必须绑定测试分组`)
      await repositories.updateAccount(created.id, {
        credentials: { ...persisted.credentials, base_url: mock.inferenceBaseUrl(route) }
      }, { systemAccountId: 'sys_admin', role: 'admin' })
      repositories.recordAccountHealthCheckSuccess(created.id, {
        intervalHours: 1,
        jitterMinutes: 0,
        failureThreshold: 3,
        statusCode: 200
      })
      const selected = await repositories.findOpenAIAccountForGroupAsync(groupId, created.id, 'sys_admin')
      assert(selected, `${route} OAuth 账户必须能被 selector 选中`)
      const prepared = await prepareGatewayUpstreamAccount(selected)
      const request = inferenceRequest(route)
      const urls = buildGatewayUpstreamUrlsForAccount(prepared, request)
      assert.equal(urls.length, 1, `${route} 首个上游请求必须生成唯一目标`)
      const parts = await buildGatewayUpstreamRequestParts(request, prepared, {
        systemAccountId: 'sys_admin',
        groupId
      })
      const upstreamResponse = await requestUpstream(urls[0]!, {
        method: request.method,
        headers: parts.headers,
        body: parts.body,
        proxyUrl: prepared.proxyUrl
      })
      assert.equal(upstreamResponse.ok, true, `${route} 首个上游请求必须成功`)
      assert.match(await responseBodyText(upstreamResponse.body), /mock/u)
      assert.equal(mock.inferenceRequestCount(route), 1, `${route} mock inference 必须收到首个请求`)
    }
  } finally {
    await closeHttpServer(server)
    await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
    databaseModule.closeStorageDatabases()
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function createGroupForProvider(repositories: typeof import('../../storage/repositories.js'), providerCode: string): Promise<{ id: string }> {
  return repositories.createGroup({ providerCode, name: `OAuth mock E2E ${providerCode}` }, { systemAccountId: 'sys_admin', role: 'admin' })
}

function inferenceRequest(route: 'openai' | 'anthropic' | 'gemini' | 'grok'): Request {
  const definitions = {
    openai: { originalUrl: '/v1/responses', path: '/v1/responses', body: { model: 'gpt-5', input: 'ping', stream: false } },
    anthropic: { originalUrl: '/v1/messages', path: '/v1/messages', body: { model: 'claude-sonnet-4-20250514', max_tokens: 16, messages: [{ role: 'user', content: 'ping' }] } },
    gemini: { originalUrl: '/v1beta/models/gemini-2.5-flash:generateContent', path: '/v1beta/models/gemini-2.5-flash:generateContent', body: { contents: [{ role: 'user', parts: [{ text: 'ping' }] }] } },
    grok: { originalUrl: '/v1/responses', path: '/v1/responses', body: { model: 'grok-4.5', input: 'ping', stream: false } }
  }[route]
  const rawBody = Buffer.from(JSON.stringify(definitions.body))
  return {
    method: 'POST',
    originalUrl: definitions.originalUrl,
    path: definitions.path,
    headers: { accept: 'application/json', authorization: 'Bearer downstream-test-key', 'content-type': 'application/json' },
    body: definitions.body,
    rawBody
  } as unknown as Request
}

async function responseBodyText(body: AsyncIterable<Uint8Array> | null): Promise<string> {
  if (!body) return ''
  const chunks: Buffer[] = []
  for await (const chunk of body) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

async function postJson<T>(url: string, body: Record<string, unknown>): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const envelope = await response.json() as ApiEnvelope<T>
  if (!response.ok || !envelope.data) {
    throw new Error(`${url} 返回 HTTP ${response.status}：${envelope.message ?? JSON.stringify(envelope)}`)
  }
  return envelope.data
}

function waitForListen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolveListen, reject) => {
    server.once('error', reject)
    server.once('listening', () => {
      server.off('error', reject)
      resolveListen()
    })
  })
}

function closeHttpServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return Promise.resolve()
  return new Promise((resolveClose) => server.close(() => resolveClose()))
}

async function testOpenAI(): Promise<void> {
  const session = await generateOpenAIAuthURL('oauth-e2e-owner')
  const authorization = await mock.authorize('openai', session.authUrl)
  await assert.rejects(exchangeOpenAIAuthCode({
    sessionId: session.sessionId,
    code: authorization.code,
    state: authorization.state,
    ownerSystemAccountId: 'wrong-oauth-e2e-owner'
  }), 'OpenAI owner 不匹配不得消费 OAuth session')
  await assert.rejects(exchangeOpenAIAuthCode({
    sessionId: session.sessionId,
    code: authorization.code,
    state: 'wrong-state',
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), 'OpenAI state 不匹配不得消费 OAuth session')
  mock.failNextTokenRequest('openai')
  await assert.rejects(
    exchangeOpenAIAuthCode({
      sessionId: session.sessionId,
      code: authorization.code,
      state: authorization.state,
      ownerSystemAccountId: 'oauth-e2e-owner'
    }),
    /HTTP 503/u
  )
  const token = await exchangeOpenAIAuthCode({
    sessionId: session.sessionId,
    code: authorization.code,
    state: authorization.state,
    ownerSystemAccountId: 'oauth-e2e-owner'
  })
  assert.equal(token.email, 'openai@example.test')
  assert.equal(token.accountId, 'chatgpt-account-mock', 'OpenAI account id 必须从 access token 逐字段回退')
  const credentials = buildOpenAIOAuthCredentials(token)
  assert.equal(credentials.account_id, 'chatgpt-account-mock')
  const refreshed = await refreshOpenAIOAuthToken({ refreshToken: token.refreshToken!, clientId: token.clientId })
  assert.notEqual(refreshed.refreshToken, token.refreshToken, 'OpenAI refresh token 必须轮换')
}

async function testAnthropic(): Promise<void> {
  const session = await generateAnthropicAuthURL('oauth-e2e-owner')
  const authorization = await mock.authorize('anthropic', session.authUrl)
  await assert.rejects(exchangeAnthropicAuthCode({
    sessionId: session.sessionId,
    callbackUrl: `${authorization.code}#${authorization.state}`,
    ownerSystemAccountId: 'wrong-oauth-e2e-owner'
  }), 'Anthropic owner 不匹配不得消费 OAuth session')
  await assert.rejects(exchangeAnthropicAuthCode({
    sessionId: session.sessionId,
    callbackUrl: `${authorization.code}#wrong-state`,
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), 'Anthropic state 不匹配不得消费 OAuth session')
  mock.failNextTokenRequest('anthropic')
  await assert.rejects(exchangeAnthropicAuthCode({
    sessionId: session.sessionId,
    callbackUrl: `${authorization.code}#${authorization.state}`,
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), /HTTP 503/u, 'Anthropic token 端点临时失败必须保留可重放 session')
  const token = await exchangeAnthropicAuthCode({
    sessionId: session.sessionId,
    callbackUrl: `${authorization.code}#${authorization.state}`,
    ownerSystemAccountId: 'oauth-e2e-owner'
  })
  const credentials = buildAnthropicOAuthCredentials(token)
  assert.equal(credentials.account_id, 'anthropic-account-mock')
  assert.equal(credentials.organization_id, 'anthropic-organization-mock')
  const refreshed = await refreshAnthropicAuthToken({ refreshToken: token.refreshToken!, clientId: token.clientId })
  assert.notEqual(refreshed.refreshToken, token.refreshToken, 'Anthropic refresh token 必须轮换')

  const bareSession = await generateAnthropicAuthURL('oauth-e2e-owner')
  const bareAuthorization = await mock.authorize('anthropic', bareSession.authUrl)
  const bareToken = await exchangeAnthropicAuthCode({
    sessionId: bareSession.sessionId,
    callbackUrl: bareAuthorization.code,
    ownerSystemAccountId: 'oauth-e2e-owner'
  })
  assert.equal(bareToken.accountId, 'anthropic-account-mock', 'Anthropic 必须接受 platform 页面返回的裸 code')
}

async function testGemini(): Promise<void> {
  const clientId = 'gemini-e2e-client'
  const clientSecret = 'gemini-e2e-secret'
  const session = await generateGeminiAuthURL({ oauthType: 'ai_studio', clientId, clientSecret, ownerSystemAccountId: 'oauth-e2e-owner' })
  const authorization = await mock.authorize('gemini', session.authUrl)
  await assert.rejects(exchangeGeminiAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    oauthType: 'ai_studio',
    clientId,
    clientSecret,
    ownerSystemAccountId: 'wrong-oauth-e2e-owner'
  }), 'Gemini owner 不匹配不得消费 OAuth session')
  const wrongStateCallback = new URL(authorization.callbackUrl)
  wrongStateCallback.searchParams.set('state', 'wrong-state')
  await assert.rejects(exchangeGeminiAuthCode({
    sessionId: session.sessionId,
    callbackUrl: wrongStateCallback.toString(),
    oauthType: 'ai_studio',
    clientId,
    clientSecret,
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), 'Gemini state 不匹配不得消费 OAuth session')
  mock.failNextTokenRequest('gemini')
  await assert.rejects(exchangeGeminiAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    oauthType: 'ai_studio',
    clientId,
    clientSecret,
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), /HTTP 503/u, 'Gemini token 端点临时失败必须保留可重放 session')
  const token = await exchangeGeminiAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    oauthType: 'ai_studio',
    clientId,
    clientSecret,
    ownerSystemAccountId: 'oauth-e2e-owner'
  })
  const credentials = buildGeminiOAuthCredentials(token)
  assert.equal(credentials.oauth_type, 'ai_studio')
  assert.equal(credentials.tier_id, 'aistudio_free')
  mock.failNextTokenRequest('gemini')
  const refreshed = await refreshGeminiAuthToken({
    oauthType: 'ai_studio',
    refreshToken: token.refreshToken!,
    clientId,
    clientSecret
  })
  assert.notEqual(refreshed.refreshToken, token.refreshToken, 'Gemini refresh token 必须轮换')

  mock.failNextTokenRequest('gemini', 400, 'unauthorized_client')
  const legacyClientRefreshed = await refreshGeminiAuthToken({
    oauthType: 'code_assist',
    refreshToken: refreshed.refreshToken!,
    clientId: 'legacy-gemini-client',
    clientSecret: 'legacy-gemini-secret',
    projectId: 'legacy-gemini-project',
    tierId: 'gcp_standard'
  })
  assert.equal(legacyClientRefreshed.clientId, 'legacy-gemini-client', 'Gemini 内置 client 被拒后应回退到凭据原 client')
}

async function testGrok(): Promise<void> {
  const session = await generateGrokAuthURL('oauth-e2e-owner')
  const authorization = await mock.authorize('grok', session.authUrl)
  await assert.rejects(exchangeGrokAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    ownerSystemAccountId: 'wrong-oauth-e2e-owner'
  }), 'Grok owner 不匹配不得消费 OAuth session')
  const wrongStateCallback = new URL(authorization.callbackUrl)
  wrongStateCallback.searchParams.set('state', 'wrong-state')
  await assert.rejects(exchangeGrokAuthCode({
    sessionId: session.sessionId,
    callbackUrl: wrongStateCallback.toString(),
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), 'Grok state 不匹配不得消费 OAuth session')
  mock.failNextTokenRequest('grok')
  await assert.rejects(exchangeGrokAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    ownerSystemAccountId: 'oauth-e2e-owner'
  }), /HTTP 503/u, 'Grok token 端点临时失败必须保留可重放 session')
  const token = await exchangeGrokAuthCode({
    sessionId: session.sessionId,
    callbackUrl: authorization.callbackUrl,
    ownerSystemAccountId: 'oauth-e2e-owner'
  })
  const credentials = buildGrokOAuthCredentials(token)
  assert.equal(credentials.email, 'grok@example.test')
  assert.equal(credentials.team_id, 'grok-team-mock', 'Grok claims 必须合并 id/access token')
  const refreshed = await refreshGrokAuthToken({ refreshToken: token.refreshToken!, clientId: token.clientId })
  assert.notEqual(refreshed.refreshToken, token.refreshToken, 'Grok refresh token 必须轮换')
}
